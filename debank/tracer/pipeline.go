package tracer

import (
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	dtypes "github.com/zeta-chain/ethermint/debank/types"
	"github.com/zeta-chain/ethermint/x/evm/statedb"
	"github.com/zeta-chain/ethermint/x/evm/types"
)

func BuildPipelineBlock(rawBlock map[string]interface{}) dtypes.Block {
	block := dtypes.Block{
		ID:                    rawBlock["hash"].(hexutil.Bytes).String(),
		Height:                big.NewInt(int64(rawBlock["number"].(hexutil.Uint64))),
		ParentID:              rawBlock["parentHash"].(common.Hash).Hex(),
		BaseFeePerGas:         big.NewInt(0),
		Miner:                 strings.ToLower(rawBlock["miner"].(common.Address).Hex()),
		GasLimit:              big.NewInt(int64(rawBlock["gasLimit"].(hexutil.Uint64))),
		GasUsed:               (*big.Int)(rawBlock["gasUsed"].(*hexutil.Big)),
		Timestamp:             uint64(rawBlock["timestamp"].(hexutil.Uint64)),
		ProcessStartTimestamp: time.Now().UnixMilli(),
	}
	if baseFeePerGas, ok := rawBlock["baseFeePerGas"]; ok {
		block.BaseFeePerGas = (*big.Int)(baseFeePerGas.(*hexutil.Big))
	}
	return block
}

func BuildPipelineTransaction(
	tx *ethtypes.Transaction,
	txConfig statedb.TxConfig,
	from common.Address,
	gasUsed *big.Int,
	success bool,
) dtypes.Transaction {
	transaction := dtypes.Transaction{
		From:             strings.ToLower(from.Hex()),
		To:               strings.ToLower(tx.To().Hex()),
		Gas:              big.NewInt(int64(tx.Gas())),
		GasUsed:          gasUsed,
		Status:           success,
		GasFeeCap:        common.Big0,
		GasTipCap:        common.Big0,
		Input:            tx.Data(),
		Nonce:            big.NewInt(int64(tx.Nonce())),
		TransactionIndex: int64(txConfig.TxIndex),
		Value:            (*hexutil.Big)(tx.Value()),
	}
	switch tx.Type() {
	case ethtypes.DynamicFeeTxType | ethtypes.BlobTxType:
		transaction.GasFeeCap = tx.GasFeeCap()
		transaction.GasTipCap = tx.GasTipCap()
	}
	return transaction
}

func BuildPilelineBlockHeader(header *ethtypes.Header) *dtypes.Header {
	blockHeader := dtypes.Header{
		Number:           (*hexutil.Big)(header.Number),
		Hash:             header.Hash(),
		ParentHash:       header.ParentHash,
		Nonce:            header.Nonce,
		MixHash:          header.MixDigest,
		Sha3Uncles:       header.UncleHash,
		LogsBloom:        header.Bloom,
		StateRoot:        header.Root,
		Miner:            header.Coinbase,
		Difficulty:       (*hexutil.Big)(header.Difficulty),
		ExtraData:        hexutil.Bytes(header.Extra),
		GasLimit:         hexutil.Uint64(header.GasLimit),
		GasUsed:          hexutil.Uint64(header.GasUsed),
		Timestamp:        hexutil.Uint64(header.Time),
		TransactionsRoot: header.TxHash,
		ReceiptsRoot:     header.ReceiptHash,
	}
	if header.BaseFee != nil {
		blockHeader.BaseFeePerGas = (*hexutil.Big)(header.BaseFee)
	}
	if header.WithdrawalsHash != nil {
		blockHeader.WithdrawalsRoot = header.WithdrawalsHash
	}
	if header.BlobGasUsed != nil {
		blockHeader.BlobGasUsed = (*hexutil.Uint64)(header.BlobGasUsed)
	}
	if header.ExcessBlobGas != nil {
		blockHeader.ExcessBlobGas = (*hexutil.Uint64)(header.ExcessBlobGas)
	}
	if header.ParentBeaconRoot != nil {
		blockHeader.ParentBeaconBlockRoot = header.ParentBeaconRoot
	}
	//if header.RequestsHash != nil {
	//	blockHeader.RequestsRoot = header.RequestsHash
	//}
	return &blockHeader
}

func BuildPipelineTxEvents(logs []*types.Log, txHash common.Hash) []dtypes.Event {
	events := make([]dtypes.Event, 0, len(logs))

	for i, log := range logs {
		var selector string
		var remainingTopics []string

		if len(log.Topics) > 0 {
			selector = log.Topics[0]
			remainingTopics = log.Topics[1:]
		}
		events = append(events, dtypes.Event{
			Address:       strings.ToLower(log.Address),
			Selector:      selector,
			Topics:        remainingTopics,
			Data:          log.Data,
			Position:      int64(i),
			ParentTraceID: txHash.Hex(),
			ID:            dtypes.ToHash([]string{txHash.Hex(), fmt.Sprintf("%d", i)}),
		})
	}
	return events
}

func BuildBlockStateDiff(parentRoot common.Hash, root common.Hash, diffs []dtypes.TransactionStateDiff) dtypes.BlockStateDiff {
	storageDiff := dtypes.BlockStateDiff{
		Hash:       root,
		ParentHash: parentRoot,
	}
	accountStorageDiffMap := make(map[common.Hash]dtypes.AccountStorageDiff)
	for _, diff := range diffs {
		for _, newCode := range diff.NewCodes {
			storageDiff.NewCodes = append(storageDiff.NewCodes, newCode)
		}
		for _, newAccount := range diff.NewAccounts {
			storageDiff.NewAccounts = append(storageDiff.NewAccounts, newAccount)
		}
		for _, deletedAccount := range diff.DeletedAccounts {
			storageDiff.DeletedAccounts = append(storageDiff.DeletedAccounts, deletedAccount)
		}
		for _, accountStorageDiff := range diff.StorageDiff {
			accountStorageDiffMap[accountStorageDiff.Address] = accountStorageDiff
		}
	}
	for _, diff := range accountStorageDiffMap {
		storageDiff.StorageDiff = append(storageDiff.StorageDiff, diff)
	}
	return storageDiff
}
