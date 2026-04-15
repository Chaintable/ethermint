// Copyright 2026 DeBank.
// State diff utilities for trace_debankBlock to capture
// state changes from non-EVM paths (BeginBlocker/EndBlocker via evmutil etc).

package keeper

import (
	"bytes"
	"encoding/json"
	"math/big"
	"os"

	"github.com/cosmos/cosmos-sdk/store/prefix"
	storetypes "github.com/cosmos/cosmos-sdk/store/types"
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
// block-level IAVL state diff (for non-EVM-path state changes).
const NonEVMStateDiffSentinelKey = "_non_evm_state_diff"

// nonEVMStateDiffSentinel is the structure JSON-serialized into a TxTraceResult.Result
// to communicate the block-level state diff to the JSON-RPC layer.
type nonEVMStateDiffSentinel struct {
	NonEVMStateDiff *dtypes.TransactionStateDiff `json:"_non_evm_state_diff"`
}

// buildNonEVMStateDiffSentinel computes block-level IAVL state diff (parent -> current)
// and packages it into a sentinel TxTraceResult that can be appended to the gRPC
// TraceBlock response. Returns nil when the feature is disabled or computation fails.
//
// Disable via env var: ETHERMINT_DISABLE_NON_EVM_DIFF=1
func (k *Keeper) buildNonEVMStateDiffSentinel(ctx sdk.Context, blockNumber int64) *types.TxTraceResult {
	if os.Getenv("ETHERMINT_DISABLE_NON_EVM_DIFF") == "1" {
		return nil
	}
	if blockNumber <= 1 {
		return nil
	}

	parentCtx := ctx.WithBlockHeight(blockNumber - 1)
	currentCtx := ctx.WithBlockHeight(blockNumber)

	diff := k.DiffEVMState(parentCtx, currentCtx)
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

// DiffEVMState computes the full EVM state diff between two contexts (parent vs current).
// It is intended to be invoked at the end of TraceBlock to补capture state changes that
// happened outside the EVM transaction code path (e.g. BeginBlocker / EndBlocker calls
// that mutate EVM contract storage via evmutil.CallEVMWithData).
//
// Returned TransactionStateDiff fields:
//   - NewAccounts: accounts whose nonce / balance / codeHash differ from parent.
//   - DeletedAccounts: accounts existing in parent but missing in current.
//   - StorageDiff: per-contract storage slots whose values differ from parent.
//   - NewCodes: code blobs (codeHash -> code) referenced by NewAccounts that are
//     newly added in current.
//
// Address fields use crypto.Keccak256Hash(addr) to match the existing pipeline encoding.
//
// IMPORTANT: This function performs a full scan of the EVM module's storage prefix
// at both heights and a full account iteration. Cost is O(EVM state size).
// Only use on archive nodes (pruning="nothing").
func (k *Keeper) DiffEVMState(parentCtx, currentCtx sdk.Context) dtypes.TransactionStateDiff {
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

		if accountChanged(prev, curr) {
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
	}

	for addr := range parentAddrs {
		if _, ok := currentAddrs[addr]; ok {
			continue
		}
		diff.DeletedAccounts = append(diff.DeletedAccounts,
			crypto.Keccak256Hash(addr.Bytes()))
	}

	for _, sd := range diffStoragePrefix(parentCtx, currentCtx, k.storeKey) {
		diff.StorageDiff = append(diff.StorageDiff, sd)
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

// diffStoragePrefix iterates the EVM module storage prefix on both contexts and
// returns per-contract slot diffs. Layout: KeyPrefixStorage / address(20B) / slot(32B) -> value.
//
// Single-pass merge over two sorted iterators avoids loading entire state into memory.
func diffStoragePrefix(parentCtx, currentCtx sdk.Context, storeKey storetypes.StoreKey) []dtypes.AccountStorageDiff {
	parentStore := prefix.NewStore(parentCtx.KVStore(storeKey), types.KeyPrefixStorage)
	currentStore := prefix.NewStore(currentCtx.KVStore(storeKey), types.KeyPrefixStorage)

	pIter := parentStore.Iterator(nil, nil)
	defer pIter.Close()
	cIter := currentStore.Iterator(nil, nil)
	defer cIter.Close()

	// per-address bucket: addr -> []IndexValuePair
	buckets := make(map[common.Address][]dtypes.IndexValuePair)

	addPair := func(addrSlot []byte, value []byte) {
		if len(addrSlot) < common.AddressLength+common.HashLength {
			return
		}
		addr := common.BytesToAddress(addrSlot[:common.AddressLength])
		slot := common.BytesToHash(addrSlot[common.AddressLength:])
		v := uint256.NewInt(0)
		if len(value) > 0 {
			v = uint256.NewInt(0).SetBytes(value)
		}
		buckets[addr] = append(buckets[addr], dtypes.IndexValuePair{
			Index: slot,
			Value: v,
		})
	}

	for pIter.Valid() && cIter.Valid() {
		pk, ck := pIter.Key(), cIter.Key()
		cmp := bytes.Compare(pk, ck)
		switch {
		case cmp == 0:
			if !bytes.Equal(pIter.Value(), cIter.Value()) {
				addPair(ck, cIter.Value())
			}
			pIter.Next()
			cIter.Next()
		case cmp < 0:
			addPair(pk, nil)
			pIter.Next()
		default: // cmp > 0
			addPair(ck, cIter.Value())
			cIter.Next()
		}
	}
	for ; pIter.Valid(); pIter.Next() {
		addPair(pIter.Key(), nil)
	}
	for ; cIter.Valid(); cIter.Next() {
		addPair(cIter.Key(), cIter.Value())
	}

	out := make([]dtypes.AccountStorageDiff, 0, len(buckets))
	for addr, values := range buckets {
		out = append(out, dtypes.AccountStorageDiff{
			Address: crypto.Keccak256Hash(addr.Bytes()),
			Values:  values,
		})
	}
	return out
}

