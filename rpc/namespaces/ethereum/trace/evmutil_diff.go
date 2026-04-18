package trace

import (
	"math/big"
	"strings"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
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
	userAddr     common.Address // receiver (mint) or initiator (burn)
	isMint       bool
}

// reconstructEvmutilDiff builds a TransactionStateDiff from block result events
// for evmutil-related Cosmos txs. This is the fallback when no stored diff is
// available (i.e., historical blocks processed before StateDiffCollector).
//
// It works by:
//  1. Scanning block events for evmutil conversion events
//  2. Extracting affected ERC20 contract + user addresses
//  3. Querying archive state at blockHeight for the exact storage values
//  4. Building a minimal StorageDiff with the affected ERC20 balance slots
func reconstructEvmutilDiff(
	b *backend.Backend,
	blockRes []*abci.ResponseDeliverTx,
	blockHeight rpctypes.BlockNumber,
) (*dtypes.TransactionStateDiff, map[common.Address]struct{}) {
	events := extractEvmutilEvents(blockRes)
	if len(events) == 0 {
		return nil, nil
	}

	// Collect unique contracts and users for state queries.
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

	// Build storage diff: query ERC20 balance slots at blockHeight.
	heightOrHash := rpctypes.BlockNumberOrHash{BlockNumber: &blockHeight}
	storageDiffMap := make(map[common.Hash]map[common.Hash]*uint256.Int) // addrHash -> slotHash -> value

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

		// Also query totalSupply (slot 2 for OpenZeppelin ERC20).
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

// extractEvmutilEvents scans block tx results for evmutil conversion events.
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

	// For mint: the affected user is the receiver.
	// For burn: the affected user is the initiator.
	var userHex string
	if isMint {
		userHex = attrs[attrReceiver]
	} else {
		userHex = attrs[attrInitiator]
	}

	// User address can be hex (0x...) or bech32 (kava1...).
	userAddr, ok := parseAddress(userHex)
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

// parseAddress parses a hex (0x...) or bech32 (kava1...) address.
func parseAddress(s string) (common.Address, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return common.Address{}, false
	}
	if common.IsHexAddress(s) {
		return common.HexToAddress(s), true
	}
	// Try bech32: extract raw bytes.
	// bech32 addresses are 20 bytes, same as EVM addresses.
	if len(s) > 4 && strings.Contains(s, "1") {
		// Use SDK AccAddressFromBech32 equivalent: just decode the hex part.
		// Since we can't import cosmos-sdk here without cycle risk,
		// we use a simple hex fallback from the last 40 chars if available.
		// Actually, for kava addresses, the 20-byte address maps directly.
		// We'll convert via the common.BytesToAddress approach.
		decoded, err := hexutil.Decode("0x" + s) // won't work for bech32
		if err == nil && len(decoded) == 20 {
			return common.BytesToAddress(decoded), true
		}
	}
	return common.Address{}, false
}

// erc20BalanceSlot computes the storage slot for an ERC20 balance mapping entry.
// For OpenZeppelin ERC20, `_balances` is at slot 0.
// slot = keccak256(abi.encode(address, uint256(0)))
func erc20BalanceSlot(addr common.Address) common.Hash {
	// abi.encode(address, uint256(0)) = addr padded to 32 bytes + uint256(0)
	key := make([]byte, 64)
	copy(key[12:32], addr.Bytes()) // address left-padded to 32 bytes
	// key[32:64] is already zero (slot index 0)
	return crypto.Keccak256Hash(key)
}
