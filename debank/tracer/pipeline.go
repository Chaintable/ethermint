package tracer

import (
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	dtypes "github.com/zeta-chain/ethermint/debank/types"
	"github.com/zeta-chain/ethermint/x/evm/types"
)

func BuildPipelineBlock(rawBlock *ethtypes.Block) dtypes.Block {
	block := dtypes.Block{
		ID:                    rawBlock.Hash().Hex(),
		Height:                rawBlock.Number(),
		ParentID:              rawBlock.ParentHash().Hex(),
		BaseFeePerGas:         big.NewInt(0),
		Miner:                 strings.ToLower(rawBlock.Coinbase().Hex()),
		GasLimit:              big.NewInt(int64(rawBlock.GasLimit())),
		GasUsed:               big.NewInt(int64(rawBlock.GasUsed())),
		Timestamp:             rawBlock.Time(),
		ProcessStartTimestamp: time.Now().UnixMilli(),
	}
	if rawBlock.Header().BaseFee != nil {
		block.BaseFeePerGas = rawBlock.Header().BaseFee
	}
	return block
}

func BuildPipelineWithdrawals(rawBlock *ethtypes.Block) []dtypes.SpecialTransfer {
	//TODO try to fix it in cosmos
	res := make([]dtypes.SpecialTransfer, 0)
	for _, withdrawal := range rawBlock.Withdrawals() {
		specialTransfer := dtypes.SpecialTransfer{
			FromAddress: strings.ToLower("0x00000000219ab540356cBB839Cbe05303d7705Fa"), //eth2 合约
			ToAddress:   strings.ToLower(withdrawal.Address.Hex()),
			Value:       (*hexutil.Big)(big.NewInt(int64(withdrawal.Amount))),
			Memo:        "beacon_withdrawl",
			Idx:         big.NewInt(int64(withdrawal.Index)),
		}
		specialTransfer.ID = dtypes.ToHash([]string{rawBlock.Hash().Hex(), specialTransfer.ToAddress, fmt.Sprintf("%d", withdrawal.Index)})
		res = append(res, specialTransfer)
	}

	return res
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

func BuildPipelineTxEvents(logs []*types.Log) []dtypes.Event {
	events := make([]dtypes.Event, 0, len(logs))

	for i, log := range logs {
		var selector string
		var remainingTopics []string

		if len(log.Topics) > 0 {
			selector = log.Topics[0]
			remainingTopics = log.Topics[1:]
		}
		//TODO how to fix id
		events = append(events, dtypes.Event{
			Address:  strings.ToLower(log.Address),
			Selector: selector,
			Topics:   remainingTopics,
			Data:     log.Data,
			Position: int64(i),
		})
	}
	return events
}
