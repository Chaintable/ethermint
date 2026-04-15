// Copyright 2026 DeBank.
// State diff utilities for trace_debankBlock to capture
// state changes from non-EVM paths (BeginBlocker/EndBlocker via evmutil etc).

package keeper

import (
	"bytes"
	"encoding/json"
	"math/big"
	"os"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"

	dtypes "github.com/evmos/ethermint/debank/types"
	ethermint "github.com/evmos/ethermint/types"
	"github.com/evmos/ethermint/x/evm/statedb"
	"github.com/evmos/ethermint/x/evm/types"
)

// NonEVMStateDiffSentinelKey marks a TxTraceResult.Result that carries the
// block-level state diff (for non-EVM-path state changes).
const NonEVMStateDiffSentinelKey = "_non_evm_state_diff"

// nonEVMStateDiffSentinel is the structure JSON-serialized into a TxTraceResult.Result
// to communicate the block-level state diff to the JSON-RPC layer.
type nonEVMStateDiffSentinel struct {
	NonEVMStateDiff *dtypes.TransactionStateDiff `json:"_non_evm_state_diff"`
}

// buildNonEVMStateDiffSentinel is intended to compute a block-level state diff
// capturing non-EVM-path changes (BeginBlocker/EndBlocker via evmutil, etc.),
// but the implementation is NOT YET FUNCTIONAL:
//
//  1. `ctx.WithBlockHeight(h)` only changes metadata; the KVStore it references
//     still points to the current state. We'd need rootmulti.CacheMultiStoreWithVersion
//     (which requires BaseApp access not available from within the keeper).
//  2. Even with correct historical store access, ForEachStorage-based diff on a
//     large archive node is O(storage size) per contract (hundreds of thousands
//     of IOPS) and takes minutes per call.
//
// Until we have a proper fix (likely: hook evmutil.CallEVMWithData to persist
// dirty contracts to a per-block cache during normal block production, then read
// from cache here), this function is DISABLED by default. Opt in via env var
// ONLY for manual debugging — it will hang on large chains.
//
// Opt-in: ETHERMINT_ENABLE_NON_EVM_DIFF=1
func (k *Keeper) buildNonEVMStateDiffSentinel(ctx sdk.Context, blockNumber int64) *types.TxTraceResult {
	if os.Getenv("ETHERMINT_ENABLE_NON_EVM_DIFF") != "1" {
		return nil
	}
	if blockNumber <= 1 {
		return nil
	}

	watch := parseWatchContractsEnv()

	parentCtx := ctx.WithBlockHeight(blockNumber - 1)
	currentCtx := ctx.WithBlockHeight(blockNumber)

	diff := k.DiffEVMState(parentCtx, currentCtx, watch)
	if diffEmpty(diff) {
		return nil
	}

	payload := nonEVMStateDiffSentinel{NonEVMStateDiff: &diff}
	raw, err := json.Marshal(payload)
	if err != nil {
		k.Logger(ctx).Error("non-evm state diff JSON encode failed",
			"height", blockNumber, "err", err)
		return nil
	}
	return &types.TxTraceResult{Result: json.RawMessage(raw)}
}

// diffEmpty reports whether a TransactionStateDiff has zero entries.
func diffEmpty(d dtypes.TransactionStateDiff) bool {
	return len(d.NewAccounts) == 0 &&
		len(d.DeletedAccounts) == 0 &&
		len(d.StorageDiff) == 0 &&
		len(d.NewCodes) == 0
}

// DiffEVMState computes a focused EVM state diff between two contexts.
//
// To make this affordable on an archive node (a full scan of the EVM module's
// storage prefix takes minutes), the storage diff is restricted to
// watchContracts. Account-level diff (balance / nonce / codeHash) runs over all
// EthAccounts and is cheap relative to storage iteration.
//
// Returned TransactionStateDiff fields:
//   - NewAccounts: accounts whose nonce / balance / codeHash differ from parent.
//   - DeletedAccounts: accounts existing in parent but missing in current.
//   - StorageDiff: for each watched contract, slots whose values differ.
//   - NewCodes: code blobs (codeHash -> code) for newly deployed contracts
//     detected via NewAccounts.
//
// Address fields use crypto.Keccak256Hash(addr) to match the existing pipeline encoding.
func (k *Keeper) DiffEVMState(parentCtx, currentCtx sdk.Context, watchContracts []common.Address) dtypes.TransactionStateDiff {
	diff := dtypes.TransactionStateDiff{
		NewAccounts:     make([]dtypes.NewAccount, 0),
		DeletedAccounts: make([]common.Hash, 0),
		StorageDiff:     make([]dtypes.AccountStorageDiff, 0),
		NewCodes:        make([]dtypes.NewCode, 0),
	}

	currentAddrs := collectEthAddresses(currentCtx, k.accountKeeper)
	parentAddrs := collectEthAddresses(parentCtx, k.accountKeeper)

	for addr := range currentAddrs {
		curr := k.GetAccount(currentCtx, addr)
		if curr == nil {
			continue
		}
		prev := k.GetAccount(parentCtx, addr)

		if !accountChanged(prev, curr) {
			continue
		}
		balance := uint256.NewInt(0)
		if curr.Balance != nil {
			balance = uint256.NewInt(0).SetBytes(curr.Balance.Bytes())
		}
		diff.NewAccounts = append(diff.NewAccounts, dtypes.NewAccount{
			Address:  crypto.Keccak256Hash(addr.Bytes()),
			Balance:  balance,
			Nonce:    curr.Nonce,
			CodeHash: common.BytesToHash(curr.CodeHash),
		})
		if curr.IsContract() {
			prevCodeHash := types.EmptyCodeHash
			if prev != nil {
				prevCodeHash = prev.CodeHash
			}
			if !bytes.Equal(curr.CodeHash, prevCodeHash) {
				code := k.GetCode(currentCtx, common.BytesToHash(curr.CodeHash))
				if len(code) > 0 {
					diff.NewCodes = append(diff.NewCodes, dtypes.NewCode{
						CodeHash: common.BytesToHash(curr.CodeHash),
						Code:     code,
					})
				}
			}
		}
	}

	for addr := range parentAddrs {
		if _, ok := currentAddrs[addr]; ok {
			continue
		}
		diff.DeletedAccounts = append(diff.DeletedAccounts,
			crypto.Keccak256Hash(addr.Bytes()))
	}

	for _, addr := range watchContracts {
		slots := diffContractStorage(parentCtx, currentCtx, k, addr)
		if len(slots) == 0 {
			continue
		}
		diff.StorageDiff = append(diff.StorageDiff, dtypes.AccountStorageDiff{
			Address: crypto.Keccak256Hash(addr.Bytes()),
			Values:  slots,
		})
	}

	return diff
}

// collectEthAddresses returns the set of EthAccount addresses present at ctx.
func collectEthAddresses(ctx sdk.Context, ak types.AccountKeeper) map[common.Address]struct{} {
	out := make(map[common.Address]struct{})
	ak.IterateAccounts(ctx, func(acc authtypes.AccountI) bool {
		if eth, ok := acc.(ethermint.EthAccountI); ok {
			out[common.BytesToAddress(eth.GetAddress().Bytes())] = struct{}{}
		}
		return false
	})
	return out
}

// accountChanged reports whether nonce / balance / codeHash differs between prev and curr.
// A nil prev (account did not exist) counts as changed.
func accountChanged(prev, curr *statedb.Account) bool {
	if prev == nil {
		return true
	}
	if prev.Nonce != curr.Nonce {
		return true
	}
	if !bytes.Equal(prev.CodeHash, curr.CodeHash) {
		return true
	}
	prevBal, currBal := big.NewInt(0), big.NewInt(0)
	if prev.Balance != nil {
		prevBal = prev.Balance
	}
	if curr.Balance != nil {
		currBal = curr.Balance
	}
	return prevBal.Cmp(currBal) != 0
}

// diffContractStorage returns the list of slots whose value in `current` differs
// from `parent` for a single contract. Uses ForEachStorage at both contexts and
// merges results — O(contract storage size), i.e. only the state of one address.
func diffContractStorage(parentCtx, currentCtx sdk.Context, k *Keeper, addr common.Address) []dtypes.IndexValuePair {
	prev := map[common.Hash]common.Hash{}
	k.ForEachStorage(parentCtx, addr, func(key, value common.Hash) bool {
		prev[key] = value
		return true
	})
	curr := map[common.Hash]common.Hash{}
	k.ForEachStorage(currentCtx, addr, func(key, value common.Hash) bool {
		curr[key] = value
		return true
	})

	out := make([]dtypes.IndexValuePair, 0)
	for key, cval := range curr {
		pval, existed := prev[key]
		if !existed || pval != cval {
			v := uint256.NewInt(0)
			if cval != (common.Hash{}) {
				v = uint256.NewInt(0).SetBytes(cval.Bytes())
			}
			out = append(out, dtypes.IndexValuePair{Index: key, Value: v})
		}
	}
	for key := range prev {
		if _, existed := curr[key]; !existed {
			out = append(out, dtypes.IndexValuePair{Index: key, Value: uint256.NewInt(0)})
		}
	}
	return out
}

// parseWatchContractsEnv reads a comma-separated hex address list from env var
// ETHERMINT_NON_EVM_WATCH_ADDRS. Empty / invalid addresses are skipped.
// Example: ETHERMINT_NON_EVM_WATCH_ADDRS=0xfa9343c3...,0x...
func parseWatchContractsEnv() []common.Address {
	raw := strings.TrimSpace(os.Getenv("ETHERMINT_NON_EVM_WATCH_ADDRS"))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]common.Address, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !common.IsHexAddress(p) {
			continue
		}
		out = append(out, common.HexToAddress(p))
	}
	return out
}
