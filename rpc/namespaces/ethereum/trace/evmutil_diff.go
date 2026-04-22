package trace

import (
	"math/big"
	"strings"

	abci "github.com/cometbft/cometbft/abci/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"

	"github.com/evmos/ethermint/rpc/backend"
	dtypes "github.com/evmos/ethermint/debank/types"
	rpctypes "github.com/evmos/ethermint/rpc/types"
)

// evmutil event types emitted by kava's x/evmutil module.
const (
	evtConvertCosmosCoinToERC20   = "convert_cosmos_coin_to_erc20"
	evtConvertCosmosCoinFromERC20 = "convert_cosmos_coin_from_erc20"
	evtConvertCoinToERC20         = "convert_evm_erc20_from_coin"
	evtConvertERC20ToCoin         = "convert_evm_erc20_to_coin"

	attrERC20Address = "erc20_address"
	attrReceiver     = "receiver"
	attrInitiator    = "initiator"
)

// evmutilEvent is a parsed evmutil conversion event.
type evmutilEvent struct {
	contractAddr common.Address
	userAddr     common.Address
	isMint       bool
}

// extractCosmosAffectedAddresses scans BeginBlock, EndBlock and Cosmos tx
// events for transfer/coin_received events. Returns addresses that had
// balance changes through Cosmos-native paths (staking rewards, IBC,
// kavadist, etc.) which are invisible to the EVM tracer.
func extractCosmosAffectedAddresses(blockRes *abci.ResponseDeliverTx, beginEvents, endEvents []abci.Event) map[common.Address]struct{} {
	addrs := make(map[common.Address]struct{})

	collectFromEvents := func(events []abci.Event) {
		for _, event := range events {
			switch event.Type {
			case "transfer":
				attrs := eventAttrs(event)
				if addr, ok := parseCosmosAddress(attrs["recipient"]); ok {
					addrs[addr] = struct{}{}
				}
				if addr, ok := parseCosmosAddress(attrs["sender"]); ok {
					addrs[addr] = struct{}{}
				}
			case "coin_received":
				attrs := eventAttrs(event)
				if addr, ok := parseCosmosAddress(attrs["receiver"]); ok {
					addrs[addr] = struct{}{}
				}
			case "coin_spent":
				attrs := eventAttrs(event)
				if addr, ok := parseCosmosAddress(attrs["spender"]); ok {
					addrs[addr] = struct{}{}
				}
			}
		}
	}

	collectFromEvents(beginEvents)
	collectFromEvents(endEvents)

	return addrs
}

// extractCosmosAffectedAddressesFromBlock is a convenience wrapper that
// extracts addresses from all block-level events (begin/end block + tx events).
func extractCosmosAffectedAddressesFromBlock(
	beginEvents, endEvents []abci.Event,
	txResults []*abci.ResponseDeliverTx,
) map[common.Address]struct{} {
	addrs := make(map[common.Address]struct{})

	merge := func(src map[common.Address]struct{}) {
		for k, v := range src {
			addrs[k] = v
		}
	}

	merge(extractCosmosAffectedAddresses(nil, beginEvents, endEvents))

	// Also scan Cosmos tx events (non-EVM txs like IBC transfers)
	for _, txResult := range txResults {
		merge(extractCosmosAffectedAddresses(nil, txResult.Events, nil))
	}

	return addrs
}

// reconstructEvmutilDiff builds a TransactionStateDiff from block result events
// for evmutil-related Cosmos txs. This is the fallback when no stored diff is
// available (i.e., historical blocks processed before StateDiffCollector).
func reconstructEvmutilDiff(
	b *backend.Backend,
	blockRes []*abci.ResponseDeliverTx,
	blockHeight rpctypes.BlockNumber,
) (*dtypes.TransactionStateDiff, map[common.Address]struct{}) {
	events := extractEvmutilEvents(blockRes)
	if len(events) == 0 {
		return nil, nil
	}

	type contractUser struct {
		contract common.Address
		user     common.Address
	}
	seen := make(map[contractUser]struct{})
	affectedAddrs := make(map[common.Address]struct{})

	for _, evt := range events {
		affectedAddrs[evt.contractAddr] = struct{}{}
		affectedAddrs[evt.userAddr] = struct{}{}
		seen[contractUser{evt.contractAddr, evt.userAddr}] = struct{}{}
	}

	heightOrHash := rpctypes.BlockNumberOrHash{BlockNumber: &blockHeight}
	storageDiffMap := make(map[common.Hash]map[common.Hash]*uint256.Int)

	for cu := range seen {
		addrHash := crypto.Keccak256Hash(cu.contract.Bytes())
		rawSlot := erc20BalanceSlot(cu.user)
		slotHash := crypto.Keccak256Hash(rawSlot.Bytes())

		value, err := b.GetStorageAt(cu.contract, rawSlot.Hex(), heightOrHash)
		if err != nil {
			continue
		}

		if _, ok := storageDiffMap[addrHash]; !ok {
			storageDiffMap[addrHash] = make(map[common.Hash]*uint256.Int)
		}
		v := uint256.NewInt(0)
		if len(value) > 0 {
			v = uint256.NewInt(0).SetBytes(value)
		}
		storageDiffMap[addrHash][slotHash] = v

		tsSlot := common.BigToHash(big.NewInt(2))
		tsSlotHash := crypto.Keccak256Hash(tsSlot.Bytes())
		tsValue, err := b.GetStorageAt(cu.contract, tsSlot.Hex(), heightOrHash)
		if err == nil && len(tsValue) > 0 {
			storageDiffMap[addrHash][tsSlotHash] = uint256.NewInt(0).SetBytes(tsValue)
		}
	}

	if len(storageDiffMap) == 0 {
		return nil, affectedAddrs
	}

	diff := dtypes.TransactionStateDiff{
		NewAccounts:     make([]dtypes.NewAccount, 0),
		DeletedAccounts: make([]common.Hash, 0),
		StorageDiff:     make([]dtypes.AccountStorageDiff, 0, len(storageDiffMap)),
		NewCodes:        make([]dtypes.NewCode, 0),
	}

	for addrHash, slots := range storageDiffMap {
		pairs := make([]dtypes.IndexValuePair, 0, len(slots))
		for slotHash, val := range slots {
			pairs = append(pairs, dtypes.IndexValuePair{Index: slotHash, Value: val})
		}
		diff.StorageDiff = append(diff.StorageDiff, dtypes.AccountStorageDiff{
			Address: addrHash,
			Values:  pairs,
		})
	}

	return &diff, affectedAddrs
}

func extractEvmutilEvents(txResults []*abci.ResponseDeliverTx) []evmutilEvent {
	var events []evmutilEvent
	for _, txResult := range txResults {
		for _, event := range txResult.Events {
			evt, ok := parseEvmutilEvent(event)
			if ok {
				events = append(events, evt)
			}
		}
	}
	return events
}

func parseEvmutilEvent(event abci.Event) (evmutilEvent, bool) {
	var isMint bool
	switch event.Type {
	case evtConvertCosmosCoinToERC20, evtConvertCoinToERC20:
		isMint = true
	case evtConvertCosmosCoinFromERC20, evtConvertERC20ToCoin:
		isMint = false
	default:
		return evmutilEvent{}, false
	}

	attrs := eventAttrs(event)
	erc20Hex := attrs[attrERC20Address]
	if erc20Hex == "" || !common.IsHexAddress(erc20Hex) {
		return evmutilEvent{}, false
	}

	var userHex string
	if isMint {
		userHex = attrs[attrReceiver]
	} else {
		userHex = attrs[attrInitiator]
	}

	userAddr, ok := parseCosmosAddress(userHex)
	if !ok {
		return evmutilEvent{}, false
	}

	return evmutilEvent{
		contractAddr: common.HexToAddress(erc20Hex),
		userAddr:     userAddr,
		isMint:       isMint,
	}, true
}

func eventAttrs(event abci.Event) map[string]string {
	m := make(map[string]string)
	for _, attr := range event.Attributes {
		m[attr.Key] = attr.Value
	}
	return m
}

// parseCosmosAddress parses a hex (0x...) or bech32 (kava1...) address
// into an EVM common.Address.
func parseCosmosAddress(s string) (common.Address, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return common.Address{}, false
	}
	if common.IsHexAddress(s) {
		return common.HexToAddress(s), true
	}
	// Try bech32 decoding (kava1..., cosmos1..., etc.)
	accAddr, err := sdk.AccAddressFromBech32(s)
	if err == nil && len(accAddr) == 20 {
		return common.BytesToAddress(accAddr), true
	}
	return common.Address{}, false
}

// erc20BalanceSlot computes the storage slot for an ERC20 balance mapping entry.
// For OpenZeppelin ERC20, `_balances` is at slot 0.
// slot = keccak256(abi.encode(address, uint256(0)))
func erc20BalanceSlot(addr common.Address) common.Hash {
	key := make([]byte, 64)
	copy(key[12:32], addr.Bytes())
	return crypto.Keccak256Hash(key)
}
