package keeper

import (
	"context"

	"github.com/cosmos/cosmos-sdk/telemetry"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/zeta-chain/ethermint/x/pipeline/types"
)

func (k *Keeper) EndBlock(ctx context.Context) error {
	defer telemetry.ModuleMeasureSince(types.ModuleName, telemetry.Now(), telemetry.MetricKeyBeginBlocker)
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	return k.SetHistoricalInfo(ctx, sdkCtx.BlockHeight(), &types.HistoricalInfo{
		Header:     sdkCtx.BlockHeader(),
		HeaderHash: sdkCtx.HeaderHash(),
	})
}
