package keeper

import (
	"fmt"
	"math/big"
	"time"

	"cosmossdk.io/core/gas"
	"cosmossdk.io/log"
	tmtypes "github.com/cometbft/cometbft/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	dtracer "github.com/zeta-chain/ethermint/debank/tracer"
	dtypes "github.com/zeta-chain/ethermint/debank/types"
	"github.com/zeta-chain/ethermint/debank/util"
	rpctypes "github.com/zeta-chain/ethermint/rpc/types"
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
	uploader     *util.Uploader
	header       tmtypes.Header
	traceResults []*dtypes.TraceResult
	baseFee      *big.Int
	gasUsed      gas.Gas
	gasLimit     gas.Gas
	miner        common.Address
	bloom        ethtypes.Bloom
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
	p.header = tmtypes.Header{}
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
		transactionsRoot = common.BytesToHash(p.header.DataHash)
	}
	for _, traceResult := range p.traceResults {
		txs = append(txs, traceResult.Transaction)
		events = append(events, traceResult.Events...)
		traces = append(traces, traceResult.Traces...)
		stateDiffs = append(stateDiffs, traceResult.StateDiff)
	}
	block := dtypes.Block{
		ID:                    p.header.Hash().String(),
		Height:                big.NewInt(p.header.Height),
		ParentID:              common.BytesToHash(p.header.LastCommitHash).String(),
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
		Hash:             common.BytesToHash(p.header.Hash()),
		ParentHash:       common.BytesToHash(p.header.LastBlockID.Hash),
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
	var parentRootHash common.Hash
	if p.header.Height == 1 {
		parentRootHash = ethtypes.EmptyRootHash
	} else {
		lastCtx := ctx.WithBlockHeight(ctx.BlockHeight() - 1)
		parentRootHash = common.BytesToHash(lastCtx.BlockHeader().AppHash)
	}
	blockStateDiff := dtracer.BuildBlockStateDiff(parentRootHash, stateHeader.StateRoot, stateDiffs)

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
