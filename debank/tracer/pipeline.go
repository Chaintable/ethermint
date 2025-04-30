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
	var to = common.Address{}
	if tx.To() != nil {
		to = *tx.To()
	}
	transaction := dtypes.Transaction{
		From:             strings.ToLower(from.Hex()),
		To:               strings.ToLower(to.Hex()),
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

func BuildPilelineBlockHeader(header map[string]interface{}) *dtypes.Header {
	blockHeader := dtypes.Header{
		Number:           (*hexutil.Big)(big.NewInt(int64(header["number"].(hexutil.Uint64)))),
		Hash:             common.BytesToHash(header["hash"].(hexutil.Bytes)),
		ParentHash:       header["parentHash"].(common.Hash),
		Nonce:            header["nonce"].(ethtypes.BlockNonce),
		MixHash:          header["mixHash"].(common.Hash),
		Sha3Uncles:       header["sha3Uncles"].(common.Hash),
		LogsBloom:        header["logsBloom"].(ethtypes.Bloom),
		StateRoot:        common.BytesToHash(header["stateRoot"].(hexutil.Bytes)),
		Miner:            header["miner"].(common.Address),
		Difficulty:       header["difficulty"].(*hexutil.Big),
		ExtraData:        hexutil.Bytes{},
		GasLimit:         header["gasLimit"].(hexutil.Uint64),
		GasUsed:          hexutil.Uint64((*big.Int)(header["gasUsed"].(*hexutil.Big)).Uint64()),
		Timestamp:        header["timestamp"].(hexutil.Uint64),
		TransactionsRoot: header["transactionsRoot"].(common.Hash),
		ReceiptsRoot:     header["receiptsRoot"].(common.Hash),
	}
	if baseFeePerGas, ok := header["baseFeePerGas"]; ok {
		blockHeader.BaseFeePerGas = baseFeePerGas.(*hexutil.Big)
	}
	return &blockHeader
}

func BuildPipelineTxEvents(logs []*types.Log, txHash common.Hash, logIndex int) []dtypes.Event {
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
			Idx:           logIndex + i,
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
