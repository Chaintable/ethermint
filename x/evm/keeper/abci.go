// Copyright 2021 Evmos Foundation
// This file is part of Evmos' Ethermint library.
//
// The Ethermint library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The Ethermint library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the Ethermint library. If not, see https://github.com/zeta-chain/ethermint/blob/main/LICENSE
package keeper

import (
	"errors"

	"cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/common"
	dtypes "github.com/zeta-chain/ethermint/debank/types"
	evmtypes "github.com/zeta-chain/ethermint/x/evm/types"
	pipelinetypes "github.com/zeta-chain/ethermint/x/pipeline/types"

	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

func (k *Keeper) Precommit(ctx sdk.Context) error {
	header := ctx.BlockHeader()
	headerInfo := ctx.HeaderInfo()
	k.Logger(ctx).Info("Precommit", "header", header, "headerInfo", headerInfo)
	return nil
}

// BeginBlock sets the sdk Context and EIP155 chain id to the Keeper.
func (k *Keeper) BeginBlock(ctx sdk.Context) error {
	k.WithChainID(ctx)
	k.Logger(ctx).Info("BeginBlock", "height", ctx.BlockHeight(), "header", ctx.BlockHeader())
	if k.pipelineStorage != nil {
		k.pipelineStorage.traceResults = make([]*dtypes.TraceResult, 0)
	}
	return nil
}

// EndBlock also retrieves the bloom filter value from the transient store and commits it to the
// KVStore. The EVM end block logic doesn't update the validator set, thus it returns
// an empty slice.
func (k *Keeper) EndBlock(ctx sdk.Context) error {
	// Gas costs are handled within msg handler so costs should be ignored
	infCtx := ctx.WithGasMeter(types.NewInfiniteGasMeter())
	bloom := ethtypes.BytesToBloom(k.GetBlockBloomTransient(infCtx).Bytes())
	k.EmitBlockBloomEvent(infCtx, bloom)
	k.Logger(ctx).Info("EndBlock", "height", ctx.BlockHeight(), "pipeline storage", k.pipelineStorage != nil)
	if k.pipelineStorage != nil {
		header := ctx.BlockHeader()
		k.Logger(ctx).Info("EndBlock", "header", header)
		var validatorAccAddr sdk.AccAddress

		res, err := k.ValidatorAccount(ctx, &evmtypes.QueryValidatorAccountRequest{
			ConsAddress: sdk.ConsAddress(header.ProposerAddress).String(),
		})
		if err != nil {
			// use zero address as the validator operator address
			validatorAccAddr = common.Address{}.Bytes()
		} else {
			validatorAccAddr, err = sdk.AccAddressFromBech32(res.AccountAddress)
			if err != nil {
				k.Logger(ctx).Error("AccAddressFromBech32", "error", err.Error())
				return err
			}
		}
		var (
			parentHash      common.Hash
			parentStateRoot common.Hash
		)
		if ctx.BlockHeight() == 1 {
			parentHash = common.Hash{}
			parentStateRoot = ethtypes.EmptyRootHash
		} else {
			info, err := k.GetHistoricalInfo(ctx, ctx.BlockHeight()-1)
			switch {
			case errors.Is(err, pipelinetypes.ErrNoHistoricalInfo):
				k.Logger(ctx).Error("get empty parent info", "height", ctx.BlockHeight()-1, "error", err.Error())
				parentHash = common.Hash{}
				parentStateRoot = ethtypes.EmptyRootHash
			case err == nil:
				k.Logger(ctx).Info("GetHistoricalInfo", "header", info.Header)
				getHeader := info.GetHeader()
				parentHash = common.BytesToHash(info.GetHeaderHash())
				parentStateRoot = common.BytesToHash(getHeader.GetDataHash())
			default:
				return err
			}
		}
		params := k.GetParams(ctx)
		ethCfg := params.ChainConfig.EthereumConfig(k.eip155ChainID)
		baseFee := k.GetBaseFee(ctx, ethCfg)
		gasMeter := ctx.BlockGasMeter()
		k.pipelineStorage.header = ctx.HeaderInfo()
		k.pipelineStorage.headerHash = common.BytesToHash(ctx.HeaderHash())
		k.pipelineStorage.parentHeaderHash = parentHash
		k.pipelineStorage.parentStateRoot = parentStateRoot
		k.pipelineStorage.baseFee = baseFee
		k.pipelineStorage.gasUsed = gasMeter.GasConsumedToLimit()
		k.pipelineStorage.gasLimit = gasMeter.Limit()
		k.pipelineStorage.miner = common.BytesToAddress(validatorAccAddr)
		k.pipelineStorage.bloom = bloom
		if err := k.pipelineStorage.commit(ctx); err != nil {
			return err
		}
		k.pipelineStorage.clear()
	}
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	return k.SetHistoricalInfo(ctx, sdkCtx.BlockHeight(), &evmtypes.HistoricalInfo{
		Header:     sdkCtx.BlockHeader(),
		HeaderHash: sdkCtx.HeaderHash(),
	})
}
