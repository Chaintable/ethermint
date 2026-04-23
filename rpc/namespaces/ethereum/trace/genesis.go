package trace

import (
	dtracer "github.com/evmos/ethermint/debank/tracer"
	dtypes "github.com/evmos/ethermint/debank/types"
	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

func (api *API) onGenesisBlock(block map[string]interface{}) (*dtypes.DebankOutPut, error) {
	header := dtracer.BuildPilelineBlockHeader(block)
	blockDiff := &dtypes.BlockStorageDiff{
		Hash:            header.StateRoot,
		ParentHash:      ethtypes.EmptyRootHash,
		NewAccounts:     make([]dtypes.NewAccount, 0),
		DeletedAccounts: make([]common.Hash, 0),
		StorageDiff:     make([]dtypes.AccountStorageDiff, 0),
		NewCodes:        make([]dtypes.NewCode, 0),
	}
	blockFile := &dtypes.BlockFile{
		Block:            dtracer.BuildPipelineBlock(block),
		Txs:              make([]dtypes.Transaction, 0),
		Events:           make([]dtypes.Event, 0),
		Traces:           make([]dtypes.Trace, 0),
		ErrorEvents:      make([]dtypes.Event, 0),
		ErrorTraces:      make([]dtypes.Trace, 0),
		StorageContracts: make([]string, 0),
	}
	return &dtypes.DebankOutPut{
		BlockFile:      blockFile,
		Header:         header,
		StateDiff:      blockDiff,
		ValidationHash: blockFile.Validation().ValidationHash,
	}, nil
}
