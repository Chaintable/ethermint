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

// knownBalanceSlots maps kava mainnet evmutil ERC20 contract addresses to
// their _balances mapping base slot. Verified against live mainnet storage.
// Slot = keccak256(abi.encode(holder, uint256(baseSlot)))
var knownBalanceSlots = map[common.Address]int{
	common.HexToAddress("0xeb466342c4d449bc9f53a865d5cb90586f405215"): 0,  // erc20/axelar/usdc
	common.HexToAddress("0xfa9343c3897324496a05fc75abed6bac29f8a40f"): 2,  // erc20/multichain/usdc
	common.HexToAddress("0xb44a9b6905af7c801311e8f4e76932ee959c663c"): 2,  // erc20/multichain/usdt
	common.HexToAddress("0x765277eebeca2e31912c9946eae1021199b39c61"): 2,  // erc20/multichain/dai
	common.HexToAddress("0x818ec0a7fe18ff94269904fced6ae3dae6d6dc0b"): 2,  // erc20/multichain/wbtc
	common.HexToAddress("0x1a35ee4640b0a3b87705b0a4b45d227ba60ca2ad"): 0,  // erc20/axelar/wbtc
	common.HexToAddress("0xb5c4423a65b953905949548276654c96fcae6992"): 0,  // erc20/bitgo/wbtc
	common.HexToAddress("0x919c1c267bc06a7039e03fcc2ef738525769109c"): 51, // erc20/tether/usdt
	common.HexToAddress("0x23a6486099f740b7688a0bb7aed7c912015ca2f0"): 0,  // bnb
	common.HexToAddress("0x94fc70ef7791ee857a1f420b9a471a55f32382be"): 0,  // btcb
	common.HexToAddress("0x4d84e25cea9447581867fe9f2329b972f532da2c"): 0,  // busd
	common.HexToAddress("0x8e20a0a1b4664d1ae5d18cc48ba6fad4d9569406"): 0,  // xrpb
	common.HexToAddress("0x59889b7021243db5b1e065385f918316cd90d46c"): 0,  // erc20/meson/mbtc
	common.HexToAddress("0x25e9171C98Fc1924Fa9415CF50750274F0664764"): 0,  // hard (deployed cosmos coin)
	common.HexToAddress("0x15932E26f5BD4923d46a2b205191C4b5d5f43FE3"): 0,  // ATOM/IBC (deployed cosmos coin)
}

// knownTotalSupplySlots maps kava mainnet evmutil ERC20 contract addresses to
// their _totalSupply storage slot (scalar, not mapping).
var knownTotalSupplySlots = map[common.Address]int64{
	common.HexToAddress("0xeb466342c4d449bc9f53a865d5cb90586f405215"): 2,  // erc20/axelar/usdc
	common.HexToAddress("0xfa9343c3897324496a05fc75abed6bac29f8a40f"): 3,  // erc20/multichain/usdc
	common.HexToAddress("0xb44a9b6905af7c801311e8f4e76932ee959c663c"): 3,  // erc20/multichain/usdt
	common.HexToAddress("0x765277eebeca2e31912c9946eae1021199b39c61"): 3,  // erc20/multichain/dai
	common.HexToAddress("0x818ec0a7fe18ff94269904fced6ae3dae6d6dc0b"): 3,  // erc20/multichain/wbtc
	common.HexToAddress("0x1a35ee4640b0a3b87705b0a4b45d227ba60ca2ad"): 2,  // erc20/axelar/wbtc
	common.HexToAddress("0xb5c4423a65b953905949548276654c96fcae6992"): 1,  // erc20/bitgo/wbtc
	common.HexToAddress("0x919c1c267bc06a7039e03fcc2ef738525769109c"): 53, // erc20/tether/usdt
	common.HexToAddress("0x23a6486099f740b7688a0bb7aed7c912015ca2f0"): 2,  // bnb
	common.HexToAddress("0x94fc70ef7791ee857a1f420b9a471a55f32382be"): 2,  // btcb
	common.HexToAddress("0x4d84e25cea9447581867fe9f2329b972f532da2c"): 2,  // busd
	common.HexToAddress("0x8e20a0a1b4664d1ae5d18cc48ba6fad4d9569406"): 2,  // xrpb
	common.HexToAddress("0x59889b7021243db5b1e065385f918316cd90d46c"): 2,  // erc20/meson/mbtc
	common.HexToAddress("0x25e9171C98Fc1924Fa9415CF50750274F0664764"): 2,  // hard (deployed cosmos coin)
	common.HexToAddress("0x15932E26f5BD4923d46a2b205191C4b5d5f43FE3"): 2,  // ATOM/IBC (deployed cosmos coin)
}

// evmutilModuleEVMAddress is the EVM address of kava's x/evmutil module account.
// = common.BytesToAddress(authtypes.NewModuleAddress("evmutil"))
// = SHA256("evmutil")[:20]
var evmutilModuleEVMAddress = common.HexToAddress("0x71586e5b3468b5720baa9162a02366fae6933bfe")

// evmutilEvent is a parsed evmutil conversion event.
type evmutilEvent struct {
	contractAddr common.Address
	userAddr     common.Address
	isMint       bool
	// isTransfer is true for EVM-native conversion pairs (ConvertCoinToERC20 /
	// ConvertERC20ToCoin) which use transfer() instead of mint()/burn().
	// For transfers: both user and module balances change, totalSupply does NOT.
	// For mint/burn: only user balance + totalSupply change.
	isTransfer bool
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

// reconstructEvmutilDiff builds a TransactionStateDiff from evmutil conversion
// events. This is the fallback for historical blocks processed before
// StateDiffCollector was deployed. It uses knownBalanceSlots/knownTotalSupplySlots
// (hardcoded from live mainnet storage verification) to find the exact storage
// slots for each ERC20 contract.
//
// NOTE: the evmutil event whitelist (evtConvert*) must be kept in sync with
// kava's x/evmutil/types/events.go. Any new conversion event types added to
// evmutil will be silently missed unless added here.
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

	// hasMintBurn tracks contracts that have mint/burn events (need totalSupply check).
	hasMintBurn := make(map[common.Address]struct{})
	for _, evt := range events {
		affectedAddrs[evt.contractAddr] = struct{}{}
		affectedAddrs[evt.userAddr] = struct{}{}
		seen[contractUser{evt.contractAddr, evt.userAddr}] = struct{}{}
		if evt.isTransfer {
			// EVM-native path: transfer() also changes module account balance.
			affectedAddrs[evmutilModuleEVMAddress] = struct{}{}
			seen[contractUser{evt.contractAddr, evmutilModuleEVMAddress}] = struct{}{}
		} else {
			// Cosmos-native path: mint()/burn() changes totalSupply.
			hasMintBurn[evt.contractAddr] = struct{}{}
		}
	}

	heightOrHash := rpctypes.BlockNumberOrHash{BlockNumber: &blockHeight}
	storageDiffMap := make(map[common.Hash]map[common.Hash]*uint256.Int)

	prevHeight := blockHeight - 1
	prevHeightOrHash := rpctypes.BlockNumberOrHash{BlockNumber: &prevHeight}

	for cu := range seen {
		addrHash := crypto.Keccak256Hash(cu.contract.Bytes())
		if _, ok := storageDiffMap[addrHash]; !ok {
			storageDiffMap[addrHash] = make(map[common.Hash]*uint256.Int)
		}

		if baseSlot, ok := knownBalanceSlots[cu.contract]; ok {
			// Known contract: use verified slot directly (O(1) lookup).
			rawSlot := erc20BalanceSlotAt(cu.user, baseSlot)
			addStorageChange(b, cu.contract, rawSlot, heightOrHash, prevHeightOrHash, storageDiffMap[addrHash])
		} else {
			// Unknown contract: probe slots 0-255 to find balance change.
			// This handles new conversion pairs added via governance.
			for probe := 0; probe <= 255; probe++ {
				rawSlot := erc20BalanceSlotAt(cu.user, probe)
				if addStorageChange(b, cu.contract, rawSlot, heightOrHash, prevHeightOrHash, storageDiffMap[addrHash]) {
					break
				}
			}
		}
	}

	// Check totalSupply changes only for contracts with mint/burn events.
	// EVM-native conversion pairs use transfer(), which does not change totalSupply.
	for contract := range hasMintBurn {
		addrHash := crypto.Keccak256Hash(contract.Bytes())
		if _, exists := storageDiffMap[addrHash]; !exists {
			storageDiffMap[addrHash] = make(map[common.Hash]*uint256.Int)
		}

		if tsBase, ok := knownTotalSupplySlots[contract]; ok {
			tsSlot := common.BigToHash(big.NewInt(tsBase))
			addStorageChange(b, contract, tsSlot, heightOrHash, prevHeightOrHash, storageDiffMap[addrHash])
		} else {
			// Unknown contract: probe common totalSupply slots.
			for _, tsBase := range []int64{0, 1, 2, 3, 4, 5, 51, 52, 53} {
				tsSlot := common.BigToHash(big.NewInt(tsBase))
				addStorageChange(b, contract, tsSlot, heightOrHash, prevHeightOrHash, storageDiffMap[addrHash])
			}
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
		if len(slots) == 0 {
			continue
		}
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

// extractEvmutilAffectedAddresses returns all addresses involved in evmutil
// conversion events (contract addresses + user addresses + module address).
// Used to ensure addGasUsedStateDiff queries their code (for NewCodes).
func extractEvmutilAffectedAddresses(txResults []*abci.ResponseDeliverTx) map[common.Address]struct{} {
	events := extractEvmutilEvents(txResults)
	if len(events) == 0 {
		return nil
	}
	addrs := make(map[common.Address]struct{})
	for _, evt := range events {
		addrs[evt.contractAddr] = struct{}{}
		addrs[evt.userAddr] = struct{}{}
		if evt.isTransfer {
			addrs[evmutilModuleEVMAddress] = struct{}{}
		}
	}
	return addrs
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
	var isMint, isTransfer bool
	switch event.Type {
	case evtConvertCosmosCoinToERC20:
		// Cosmos-native → ERC20: calls mint() on deployed ERC20
		isMint = true
	case evtConvertCosmosCoinFromERC20:
		// ERC20 → Cosmos-native: calls burn() on deployed ERC20
		isMint = false
	case evtConvertCoinToERC20:
		// EVM-native: unlock/transfer() from module to user
		isMint = true
		isTransfer = true
	case evtConvertERC20ToCoin:
		// EVM-native: lock/transfer() from user to module
		isMint = false
		isTransfer = true
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
		isTransfer:   isTransfer,
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

// addStorageChange reads storage at the given slot for both current and previous
// block heights. If the value changed, it adds the (slotHash -> newValue) to dest
// and returns true.
func addStorageChange(
	b *backend.Backend,
	contract common.Address,
	slot common.Hash,
	curHeight, prevHeight rpctypes.BlockNumberOrHash,
	dest map[common.Hash]*uint256.Int,
) bool {
	curValue, err := b.GetStorageAt(contract, slot.Hex(), curHeight)
	if err != nil {
		return false
	}
	prevValue, err := b.GetStorageAt(contract, slot.Hex(), prevHeight)
	if err != nil {
		return false
	}
	if string(curValue) != string(prevValue) {
		slotHash := crypto.Keccak256Hash(slot.Bytes())
		v := uint256.NewInt(0)
		if len(curValue) > 0 {
			v = uint256.NewInt(0).SetBytes(curValue)
		}
		dest[slotHash] = v
		return true
	}
	return false
}

// erc20BalanceSlotAt computes the storage slot for an ERC20 balance mapping
// entry at the given base slot index.
// slot = keccak256(abi.encode(address, uint256(baseSlot)))
func erc20BalanceSlotAt(addr common.Address, baseSlot int) common.Hash {
	key := make([]byte, 64)
	copy(key[12:32], addr.Bytes())
	// baseSlot in big-endian at bytes 32-63
	key[63] = byte(baseSlot & 0xff)
	if baseSlot > 0xff {
		key[62] = byte((baseSlot >> 8) & 0xff)
	}
	return crypto.Keccak256Hash(key)
}
