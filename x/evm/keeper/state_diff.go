// Copyright 2026 DeBank.
// State diff utilities for trace_debankBlock to capture
// state changes from non-EVM paths (evmutil etc).

package keeper

import (
	"encoding/json"

	sdk "github.com/cosmos/cosmos-sdk/types"

	dtypes "github.com/evmos/ethermint/debank/types"
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

// buildNonEVMStateDiffFromStore reads the per-block non-EVM state diff that was
// recorded during normal block execution by StateDiffCollector (used in
// evmutil.CallEVMWithData). Returns nil if no diff is stored for this block.
func (k *Keeper) buildNonEVMStateDiffFromStore(ctx sdk.Context, blockNumber int64) *types.TxTraceResult {
	diff := k.GetNonEVMStateDiff(ctx, blockNumber)
	if diff == nil {
		return nil
	}
	if len(diff.NewAccounts) == 0 && len(diff.DeletedAccounts) == 0 &&
		len(diff.StorageDiff) == 0 && len(diff.NewCodes) == 0 {
		return nil
	}

	payload := nonEVMStateDiffSentinel{NonEVMStateDiff: diff}
	raw, err := json.Marshal(payload)
	if err != nil {
		k.Logger(ctx).Error("non-evm state diff JSON encode failed",
			"height", blockNumber, "err", err)
		return nil
	}
	return &types.TxTraceResult{Result: json.RawMessage(raw)}
}
