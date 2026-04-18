package keeper

import (
	"encoding/binary"
	"encoding/json"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/common"

	dtypes "github.com/evmos/ethermint/debank/types"
	"github.com/evmos/ethermint/x/evm/types"
)

// nonEVMDiffKey returns the store key for a block's non-EVM state diff.
func nonEVMDiffKey(height int64) []byte {
	key := make([]byte, len(types.KeyPrefixNonEVMStateDiff)+8)
	copy(key, types.KeyPrefixNonEVMStateDiff)
	binary.BigEndian.PutUint64(key[len(types.KeyPrefixNonEVMStateDiff):], uint64(height))
	return key
}

// AppendNonEVMStateDiff merges diff into the stored non-EVM state diff for
// the current block height. Multiple calls within the same block accumulate.
func (k *Keeper) AppendNonEVMStateDiff(ctx sdk.Context, diff dtypes.TransactionStateDiff) error {
	height := ctx.BlockHeight()
	existing := k.GetNonEVMStateDiff(ctx, height)
	if existing != nil {
		diff = mergeTransactionStateDiffs(*existing, diff)
	}
	return k.setNonEVMStateDiff(ctx, height, diff)
}

// GetNonEVMStateDiff reads the stored non-EVM state diff for the given height.
// Returns nil if no diff exists.
func (k *Keeper) GetNonEVMStateDiff(ctx sdk.Context, height int64) *dtypes.TransactionStateDiff {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(nonEVMDiffKey(height))
	if bz == nil {
		return nil
	}
	var diff dtypes.TransactionStateDiff
	if err := json.Unmarshal(bz, &diff); err != nil {
		return nil
	}
	return &diff
}

func (k *Keeper) setNonEVMStateDiff(ctx sdk.Context, height int64, diff dtypes.TransactionStateDiff) error {
	bz, err := json.Marshal(diff)
	if err != nil {
		return err
	}
	store := ctx.KVStore(k.storeKey)
	store.Set(nonEVMDiffKey(height), bz)
	return nil
}

// mergeTransactionStateDiffs merges b into a, with b taking precedence for
// overlapping accounts/slots.
func mergeTransactionStateDiffs(a, b dtypes.TransactionStateDiff) dtypes.TransactionStateDiff {
	// Merge NewAccounts: b overwrites a for same address
	acctMap := make(map[[32]byte]dtypes.NewAccount)
	for _, acc := range a.NewAccounts {
		acctMap[acc.Address] = acc
	}
	for _, acc := range b.NewAccounts {
		acctMap[acc.Address] = acc
	}
	merged := dtypes.TransactionStateDiff{
		NewAccounts:     make([]dtypes.NewAccount, 0, len(acctMap)),
		DeletedAccounts: make([]common.Hash, 0),
		StorageDiff:     make([]dtypes.AccountStorageDiff, 0),
		NewCodes:        make([]dtypes.NewCode, 0),
	}
	for _, acc := range acctMap {
		merged.NewAccounts = append(merged.NewAccounts, acc)
	}

	// Merge DeletedAccounts: union
	delSet := make(map[[32]byte]struct{})
	for _, h := range a.DeletedAccounts {
		delSet[h] = struct{}{}
	}
	for _, h := range b.DeletedAccounts {
		delSet[h] = struct{}{}
	}
	for h := range delSet {
		merged.DeletedAccounts = append(merged.DeletedAccounts, h)
	}

	// Merge StorageDiff: per-address, b overwrites a for same slot
	type slotKey struct {
		addr [32]byte
		slot [32]byte
	}
	storageMap := make(map[[32]byte]map[[32]byte]dtypes.IndexValuePair)
	addStorage := func(diffs []dtypes.AccountStorageDiff) {
		for _, sd := range diffs {
			if _, ok := storageMap[sd.Address]; !ok {
				storageMap[sd.Address] = make(map[[32]byte]dtypes.IndexValuePair)
			}
			for _, v := range sd.Values {
				storageMap[sd.Address][v.Index] = v
			}
		}
	}
	addStorage(a.StorageDiff)
	addStorage(b.StorageDiff)
	for addr, slots := range storageMap {
		pairs := make([]dtypes.IndexValuePair, 0, len(slots))
		for _, v := range slots {
			pairs = append(pairs, v)
		}
		merged.StorageDiff = append(merged.StorageDiff, dtypes.AccountStorageDiff{
			Address: addr,
			Values:  pairs,
		})
	}

	// Merge NewCodes: b overwrites a for same codeHash
	codeMap := make(map[[32]byte]dtypes.NewCode)
	for _, c := range a.NewCodes {
		codeMap[c.CodeHash] = c
	}
	for _, c := range b.NewCodes {
		codeMap[c.CodeHash] = c
	}
	for _, c := range codeMap {
		merged.NewCodes = append(merged.NewCodes, c)
	}

	return merged
}
