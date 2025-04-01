package keeper

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"cosmossdk.io/core/gas"
	"cosmossdk.io/core/header"
	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	dtracer "github.com/zeta-chain/ethermint/debank/tracer"
	dtypes "github.com/zeta-chain/ethermint/debank/types"
	"github.com/zeta-chain/ethermint/debank/util"
	rpctypes "github.com/zeta-chain/ethermint/rpc/types"
	"github.com/zeta-chain/ethermint/x/evm/types"
)

type PipelineStorageConfig struct {
	Region           string
	NodexBucket      string
	ChainTableBucket string
	Brokers          string
	Topic            string
	ChainID          string
}

func (config PipelineStorageConfig) Validate() error {
	if config.Region == "" || config.NodexBucket == "" || config.ChainTableBucket == "" || config.Brokers == "" || config.Topic == "" || config.ChainID == "" {
		return fmt.Errorf("invalid pipeline configuration")
	}
	return nil
}

type pipelineStorage struct {
	uploader         *util.Uploader
	header           header.Info
	headerHash       common.Hash
	parentHeaderHash common.Hash
	parentStateRoot  common.Hash
	traceResults     []*dtypes.TraceResult
	baseFee          *big.Int
	gasUsed          gas.Gas
	gasLimit         gas.Gas
	miner            common.Address
	bloom            ethtypes.Bloom
}

func newPipelineStorage(config PipelineStorageConfig) (*pipelineStorage, error) {
	uploader, err := util.NewUploader(config.Region, config.NodexBucket, config.ChainTableBucket, config.Brokers, config.Topic, config.ChainID)
	if err != nil {
		return nil, err
	}
	return &pipelineStorage{
		traceResults: make([]*dtypes.TraceResult, 0),
		uploader:     uploader,
	}, nil
}

func (p *pipelineStorage) commit(ctx sdk.Context) error {
	output := p.toDebanOutput(ctx)
	p.logger(ctx).Info("committing output", "output", output)
	fmt.Printf("committing output to %+v\n", output)
	//if err := p.uploader.UploadDebankOutPut(ctx, &output); err != nil {
	//	return err
	//}
	//if err := p.uploader.PushDebankOutPut(ctx, &output); err != nil {
	//	return err
	//}
	return nil
}

func (p *pipelineStorage) clear() {
	p.header = header.Info{}
	p.headerHash = common.Hash{}
	p.parentHeaderHash = common.Hash{}
	p.parentStateRoot = ethtypes.EmptyRootHash
	p.traceResults = p.traceResults[:0]
	p.baseFee = big.NewInt(0)
	p.gasUsed = 0
	p.gasLimit = 0
	p.miner = common.Address{}
	p.bloom = ethtypes.Bloom{}
}

func (p pipelineStorage) toDebanOutput(ctx sdk.Context) rpctypes.DebankOutPut {
	txs := make([]dtypes.Transaction, 0, len(p.traceResults))
	events := make([]dtypes.Event, 0, len(p.traceResults))
	traces := make([]dtypes.Trace, 0, len(p.traceResults))
	stateDiffs := make([]dtypes.TransactionStateDiff, 0, len(p.traceResults))
	var transactionsRoot common.Hash
	if len(txs) == 0 {
		transactionsRoot = ethtypes.EmptyRootHash
	} else {
		//todo
		transactionsRoot = ethtypes.EmptyRootHash
	}
	for _, traceResult := range p.traceResults {
		txs = append(txs, traceResult.Transaction)
		events = append(events, traceResult.Events...)
		traces = append(traces, traceResult.Traces...)
		stateDiffs = append(stateDiffs, traceResult.StateDiff)
	}
	block := dtypes.Block{
		ID:                    p.headerHash.String(),
		Height:                big.NewInt(p.header.Height),
		ParentID:              p.parentHeaderHash.String(),
		BaseFeePerGas:         p.baseFee,
		Miner:                 p.miner.String(),
		GasLimit:              big.NewInt(int64(p.gasLimit)),
		GasUsed:               big.NewInt(int64(p.gasUsed)),
		Timestamp:             uint64(p.header.Time.Unix()),
		ProcessStartTimestamp: time.Now().UnixMilli(),
	}
	blockFile := &dtypes.BlockFile{
		Block:  block,
		Txs:    txs,
		Events: events,
		Traces: traces,
	}
	var stateHeader = &dtypes.Header{
		Number:           (*hexutil.Big)(block.Height),
		Hash:             p.headerHash,
		ParentHash:       p.parentHeaderHash,
		Nonce:            ethtypes.BlockNonce{},
		MixHash:          common.Hash{},
		Sha3Uncles:       ethtypes.EmptyUncleHash,
		LogsBloom:        p.bloom,
		StateRoot:        common.BytesToHash(p.header.AppHash),
		Miner:            p.miner,
		Difficulty:       (*hexutil.Big)(big.NewInt(0)),
		ExtraData:        hexutil.Bytes{},
		GasLimit:         hexutil.Uint64(block.GasLimit.Uint64()),
		GasUsed:          hexutil.Uint64(block.GasUsed.Uint64()),
		Timestamp:        hexutil.Uint64(block.Timestamp),
		TransactionsRoot: transactionsRoot,
		ReceiptsRoot:     ethtypes.EmptyRootHash,
		BaseFeePerGas:    (*hexutil.Big)(block.BaseFeePerGas),
	}
	blockStateDiff := dtracer.BuildBlockStateDiff(p.parentStateRoot, stateHeader.StateRoot, stateDiffs)

	return rpctypes.DebankOutPut{
		BlockFile:      blockFile,
		Header:         stateHeader,
		StateDiff:      &blockStateDiff,
		ValidationHash: blockFile.Validation().ValidationHash,
	}
}

func (p pipelineStorage) logger(ctx sdk.Context) log.Logger {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	return sdkCtx.Logger()
}

// GetHistoricalInfo gets the historical info at a given height
func (k Keeper) GetHistoricalInfo(ctx context.Context, height int64) (types.HistoricalInfo, error) {
	store := k.storeService.OpenKVStore(ctx)
	key := types.GetHistoricalInfoKey(height)

	value, err := store.Get(key)
	if err != nil {
		return types.HistoricalInfo{}, err
	}

	if value == nil {
		return types.HistoricalInfo{}, types.ErrNoHistoricalInfo
	}

	return types.UnmarshalHistoricalInfo(k.cdc, value)
}

// SetHistoricalInfo sets the historical info at a given height
func (k Keeper) SetHistoricalInfo(ctx context.Context, height int64, hi *types.HistoricalInfo) error {
	store := k.storeService.OpenKVStore(ctx)
	key := types.GetHistoricalInfoKey(height)
	value, err := k.cdc.Marshal(hi)
	if err != nil {
		return err
	}
	return store.Set(key, value)
}

// DeleteHistoricalInfo deletes the historical info at a given height
func (k Keeper) DeleteHistoricalInfo(ctx context.Context, height int64) error {
	store := k.storeService.OpenKVStore(ctx)
	key := types.GetHistoricalInfoKey(height)

	return store.Delete(key)
}

// IterateHistoricalInfo provides an iterator over all stored HistoricalInfo
// objects. For each HistoricalInfo object, cb will be called. If the cb returns
// true, the iterator will break and close.
func (k Keeper) IterateHistoricalInfo(ctx context.Context, cb func(types.HistoricalInfo) bool) error {
	store := k.storeService.OpenKVStore(ctx)
	iterator, err := store.Iterator(types.HistoricalInfoKey, storetypes.PrefixEndBytes(types.HistoricalInfoKey))
	if err != nil {
		return err
	}
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		histInfo, err := types.UnmarshalHistoricalInfo(k.cdc, iterator.Value())
		if err != nil {
			return err
		}
		if cb(histInfo) {
			break
		}
	}

	return nil
}

// TrackHistoricalInfo saves the latest historical-info and deletes the oldest
// heights that are below pruning height
func (k Keeper) TrackHistoricalInfo(ctx context.Context) error {
	entryNum := 100

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	for i := sdkCtx.BlockHeight() - int64(entryNum); i >= 0; i-- {
		_, err := k.GetHistoricalInfo(ctx, i)
		if err != nil {
			if errors.Is(err, types.ErrNoHistoricalInfo) {
				break
			}
			return err
		}
		if err = k.DeleteHistoricalInfo(ctx, i); err != nil {
			return err
		}
	}

	// Set latest HistoricalInfo at current height
	return k.SetHistoricalInfo(ctx, sdkCtx.BlockHeight(), &types.HistoricalInfo{
		Header:     sdkCtx.BlockHeader(),
		HeaderHash: sdkCtx.HeaderHash(),
	})
}
