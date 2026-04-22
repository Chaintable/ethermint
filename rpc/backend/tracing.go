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
// along with the Ethermint library. If not, see https://github.com/evmos/ethermint/blob/main/LICENSE
package backend

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strings"

	abci "github.com/cometbft/cometbft/abci/types"
	tmrpctypes "github.com/cometbft/cometbft/rpc/core/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/common"
	rpctypes "github.com/evmos/ethermint/rpc/types"
	evmtypes "github.com/evmos/ethermint/x/evm/types"
	"github.com/pkg/errors"
)

// TraceTransaction returns the structured logs created during the execution of EVM
// and returns them as a JSON object.
func (b *Backend) TraceTransaction(hash common.Hash, config *evmtypes.TraceConfig) (interface{}, error) {
	// Get transaction by hash
	transaction, err := b.GetTxByEthHash(hash)
	if err != nil {
		b.logger.Debug("tx not found", "hash", hash)
		return nil, err
	}

	// check if block number is 0
	if transaction.Height == 0 {
		return nil, errors.New("genesis is not traceable")
	}

	blk, err := b.TendermintBlockByNumber(rpctypes.BlockNumber(transaction.Height))
	if err != nil {
		b.logger.Debug("block not found", "height", transaction.Height)
		return nil, err
	}

	// check tx index is not out of bound
	if uint32(len(blk.Block.Txs)) < transaction.TxIndex {
		b.logger.Debug("tx index out of bounds", "index", transaction.TxIndex, "hash", hash.String(), "height", blk.Block.Height)
		return nil, fmt.Errorf("transaction not included in block %v", blk.Block.Height)
	}

	var predecessors []*evmtypes.MsgEthereumTx
	for _, txBz := range blk.Block.Txs[:transaction.TxIndex] {
		tx, err := b.clientCtx.TxConfig.TxDecoder()(txBz)
		if err != nil {
			b.logger.Debug("failed to decode transaction in block", "height", blk.Block.Height, "error", err.Error())
			continue
		}
		for _, msg := range tx.GetMsgs() {
			ethMsg, ok := msg.(*evmtypes.MsgEthereumTx)
			if !ok {
				continue
			}

			predecessors = append(predecessors, ethMsg)
		}
	}

	tx, err := b.clientCtx.TxConfig.TxDecoder()(blk.Block.Txs[transaction.TxIndex])
	if err != nil {
		b.logger.Debug("tx not found", "hash", hash)
		return nil, err
	}

	// add predecessor messages in current cosmos tx
	for i := 0; i < int(transaction.MsgIndex); i++ {
		ethMsg, ok := tx.GetMsgs()[i].(*evmtypes.MsgEthereumTx)
		if !ok {
			continue
		}
		predecessors = append(predecessors, ethMsg)
	}

	ethMessage, ok := tx.GetMsgs()[transaction.MsgIndex].(*evmtypes.MsgEthereumTx)
	if !ok {
		b.logger.Debug("invalid transaction type", "type", fmt.Sprintf("%T", tx))
		return nil, fmt.Errorf("invalid transaction type %T", tx)
	}

	traceTxRequest := evmtypes.QueryTraceTxRequest{
		Msg:             ethMessage,
		Predecessors:    predecessors,
		BlockNumber:     blk.Block.Height,
		BlockTime:       blk.Block.Time,
		BlockHash:       common.Bytes2Hex(blk.BlockID.Hash),
		ProposerAddress: sdk.ConsAddress(blk.Block.ProposerAddress),
		ChainId:         b.chainID.Int64(),
	}

	if config != nil {
		traceTxRequest.TraceConfig = config
	}

	// minus one to get the context of block beginning
	contextHeight := transaction.Height - 1
	if contextHeight < 1 {
		// 0 is a special value in `ContextWithHeight`
		contextHeight = 1
	}
	traceResult, err := b.queryClient.TraceTx(rpctypes.ContextWithHeight(contextHeight), &traceTxRequest)
	if err != nil {
		return nil, err
	}

	// Response format is unknown due to custom tracer config param
	// More information can be found here https://geth.ethereum.org/docs/dapp/tracing-filtered
	var decodedResult interface{}
	err = json.Unmarshal(traceResult.Data, &decodedResult)
	if err != nil {
		return nil, err
	}

	jsonResult, ok := decodedResult.(map[string]interface{})

	if !ok {
		// gracefully fallback to default behavior
		return decodedResult, nil
	}

	// handle edge cases in differences between traced tx status
	// and actual tx status when it was executed as part of a block
	// this can happen when
	// - tracing a tx succeeds even though when the tx was executed
	// the block gas meter became exhausted
	if transaction.Failed && jsonResult["failed"] != transaction.Failed {
		// override trace transaction status with actual tx status
		jsonResult["failed"] = transaction.Failed
		_, exists := jsonResult["error"]

		if !exists {
			// use tendermint as source of truth for error message
			// and gas usage
			query := fmt.Sprintf("%s.%s='%s'", evmtypes.TypeMsgEthereumTx, evmtypes.AttributeKeyEthereumTxHash, hash.Hex())
			resTxs, err := b.clientCtx.Client.TxSearch(b.ctx, query, false, nil, nil, "")
			if err != nil {
				panic(err)
			}

			if resTxs.TotalCount != 1 {
				// gracefully fallback to default behavior
				return decodedResult, nil
			}

			txMe := resTxs.Txs[0]

			// using the actual gas used amount for when the tx was executed
			jsonResult["gas_used"] = txMe.TxResult.GasUsed

			// TODO: supporting configuring max error string length
			// some indexing services (e.g. blockscout) have a character limit
			// for the field that stores this data
			maxErrorStringLength := math.Min(200, float64(len(txMe.TxResult.Log)-1))
			jsonResult["error"] = txMe.TxResult.Log[0:int64(maxErrorStringLength)]
		}
	}

	return jsonResult, nil
}

// TraceBlock configures a new tracer according to the provided configuration, and
// executes all the transactions contained within. The return value will be one item
// per transaction, dependent on the requested tracer.
func (b *Backend) TraceBlock(height rpctypes.BlockNumber,
	config *evmtypes.TraceConfig,
	block *tmrpctypes.ResultBlock,
) ([]*evmtypes.TxTraceResult, error) {
	txs := block.Block.Txs
	txsLength := len(txs)

	if txsLength == 0 {
		// If there are no transactions return empty array
		return []*evmtypes.TxTraceResult{}, nil
	}

	// Fetch block results to filter out Cosmos-level failed txs
	blockRes, err := b.TendermintBlockResultByNumber(&block.Block.Height)
	if err != nil {
		return nil, err
	}
	txResults := blockRes.TxsResults

	txDecoder := b.clientCtx.TxConfig.TxDecoder()

	var txsMessages []*evmtypes.MsgEthereumTx
	var evmTxBlockIndices []int32
	for i, tx := range txs {
		// Skip txs that failed at Cosmos level (e.g. block gas limit exceeded),
		// consistent with EthMsgsFromTendermintBlock filtering.
		if i < len(txResults) && !rpctypes.TxSuccessOrExceedsBlockGasLimit(txResults[i]) {
			continue
		}

		decodedTx, err := txDecoder(tx)
		if err != nil {
			b.logger.Error("failed to decode transaction", "hash", txs[i].Hash(), "error", err.Error())
			continue
		}

		for _, msg := range decodedTx.GetMsgs() {
			ethMessage, ok := msg.(*evmtypes.MsgEthereumTx)
			if !ok {
				// Just considers Ethereum transactions
				continue
			}
			txsMessages = append(txsMessages, ethMessage)
			evmTxBlockIndices = append(evmTxBlockIndices, int32(i))
		}
	}

	// minus one to get the context at the beginning of the block
	contextHeight := height - 1
	if contextHeight < 1 {
		// 0 is a special value for `ContextWithHeight`.
		contextHeight = 1
	}
	ctxWithHeight := rpctypes.ContextWithHeight(int64(contextHeight))

	// Extract evmutil EVM operations from block events for non-EVM state diff capture.
	var evmutilOpsBytes [][]byte
	if evmutilOps := extractEvmutilOps(txResults); len(evmutilOps) > 0 {
		for _, op := range evmutilOps {
			opBytes, err := json.Marshal(op)
			if err != nil {
				b.logger.Error("failed to marshal evmutil op", "error", err)
				continue
			}
			evmutilOpsBytes = append(evmutilOpsBytes, opBytes)
		}
	}

	traceBlockRequest := &evmtypes.QueryTraceBlockRequest{
		Txs:                txsMessages,
		EvmutilOps:         evmutilOpsBytes,
		TraceConfig:        config,
		EvmTxBlockIndices:  evmTxBlockIndices,
		BlockNumber:     block.Block.Height,
		BlockTime:       block.Block.Time,
		BlockHash:       common.Bytes2Hex(block.BlockID.Hash),
		ProposerAddress: sdk.ConsAddress(block.Block.ProposerAddress),
		ChainId:         b.chainID.Int64(),
	}

	res, err := b.queryClient.TraceBlock(ctxWithHeight, traceBlockRequest)
	if err != nil {
		return nil, err
	}

	decodedResults := make([]*evmtypes.TxTraceResult, txsLength)
	if err := json.Unmarshal(res.Data, &decodedResults); err != nil {
		return nil, err
	}

	return decodedResults, nil
}

// evmutil event types emitted by kava's x/evmutil module.
const (
	evtConvertCosmosCoinToERC20   = "convert_cosmos_coin_to_erc20"
	evtConvertCosmosCoinFromERC20 = "convert_cosmos_coin_from_erc20"
	evtConvertCoinToERC20         = "convert_evm_erc20_from_coin"
	evtConvertERC20ToCoin         = "convert_evm_erc20_to_coin"
)

// bep3 denoms require amount * 10^10 to convert from 8 to 18 decimals.
var bep3Denoms = map[string]bool{
	"bnb": true, "busd": true, "btcb": true, "xrpb": true,
}

var bep3ConversionFactor = new(big.Int).Exp(big.NewInt(10), big.NewInt(10), nil) // 10^10

// ERC20 function selectors
var (
	selectorMint     = common.Hex2Bytes("40c10f19") // mint(address,uint256)
	selectorBurn     = common.Hex2Bytes("9dc29fac") // burn(address,uint256)
	selectorTransfer = common.Hex2Bytes("a9059cbb") // transfer(address,uint256)
)

// extractEvmutilOps scans block tx results for evmutil conversion events
// and reconstructs the EVM call parameters needed for replay.
func extractEvmutilOps(txResults []*abci.ResponseDeliverTx) []evmtypes.EvmutilOp {
	var ops []evmtypes.EvmutilOp
	for i, txResult := range txResults {
		for _, event := range txResult.Events {
			op, ok := parseEvmutilEventToOp(event)
			if ok {
				op.BlockTxIndex = i
				ops = append(ops, op)
			}
		}
	}
	return ops
}

func parseEvmutilEventToOp(event abci.Event) (evmtypes.EvmutilOp, bool) {
	attrs := make(map[string]string)
	for _, attr := range event.Attributes {
		attrs[attr.Key] = attr.Value
	}

	erc20Hex := attrs["erc20_address"]
	if erc20Hex == "" || !common.IsHexAddress(erc20Hex) {
		return evmtypes.EvmutilOp{}, false
	}
	contractAddr := common.HexToAddress(erc20Hex)

	// Parse amount from sdk.Coin.String() format, e.g. "1000000uhard"
	amountStr := attrs["amount"]
	amount, denom := parseCoinString(amountStr)
	if amount == nil {
		return evmtypes.EvmutilOp{}, false
	}

	moduleAddr := evmtypes.EvmutilModuleEVMAddress

	switch event.Type {
	case evtConvertCosmosCoinToERC20:
		// Cosmos-native → ERC20: mint(receiver, amount) from ModuleAddr
		receiver := parseHexOrBech32(attrs["receiver"])
		if receiver == (common.Address{}) {
			return evmtypes.EvmutilOp{}, false
		}
		return evmtypes.EvmutilOp{
			Type: evmtypes.EvmutilOpMint,
			From: moduleAddr,
			To:   contractAddr,
			Data: packABI(selectorMint, receiver, amount),
		}, true

	case evtConvertCosmosCoinFromERC20:
		// ERC20 → Cosmos-native: burn(initiator, amount) from ModuleAddr
		initiator := parseHexOrBech32(attrs["initiator"])
		if initiator == (common.Address{}) {
			return evmtypes.EvmutilOp{}, false
		}
		return evmtypes.EvmutilOp{
			Type: evmtypes.EvmutilOpBurn,
			From: moduleAddr,
			To:   contractAddr,
			Data: packABI(selectorBurn, initiator, amount),
		}, true

	case evtConvertCoinToERC20:
		// EVM-native unlock: transfer(receiver, amount) from ModuleAddr
		receiver := parseHexOrBech32(attrs["receiver"])
		if receiver == (common.Address{}) {
			return evmtypes.EvmutilOp{}, false
		}
		evmAmount := convertBep3Amount(amount, denom)
		return evmtypes.EvmutilOp{
			Type: evmtypes.EvmutilOpUnlock,
			From: moduleAddr,
			To:   contractAddr,
			Data: packABI(selectorTransfer, receiver, evmAmount),
		}, true

	case evtConvertERC20ToCoin:
		// EVM-native lock: transfer(ModuleAddr, amount) from initiator
		initiator := parseHexOrBech32(attrs["initiator"])
		if initiator == (common.Address{}) {
			return evmtypes.EvmutilOp{}, false
		}
		evmAmount := convertBep3Amount(amount, denom)
		return evmtypes.EvmutilOp{
			Type: evmtypes.EvmutilOpLock,
			From: initiator,
			To:   contractAddr,
			Data: packABI(selectorTransfer, moduleAddr, evmAmount),
		}, true
	}

	return evmtypes.EvmutilOp{}, false
}

// parseCoinString parses "1000000uhard" into (big.Int(1000000), "uhard").
func parseCoinString(s string) (*big.Int, string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, ""
	}
	// Find where digits end and denom starts
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9') {
		i++
	}
	if i == 0 || i == len(s) {
		return nil, ""
	}
	amount, ok := new(big.Int).SetString(s[:i], 10)
	if !ok {
		return nil, ""
	}
	return amount, s[i:]
}

// convertBep3Amount multiplies amount by 10^10 if denom is a bep3 asset.
func convertBep3Amount(amount *big.Int, denom string) *big.Int {
	if bep3Denoms[denom] {
		return new(big.Int).Mul(amount, bep3ConversionFactor)
	}
	return amount
}

// packABI encodes a function call: selector + abi.encode(address, uint256)
func packABI(selector []byte, addr common.Address, amount *big.Int) []byte {
	data := make([]byte, 4+64) // 4 bytes selector + 32 bytes address + 32 bytes uint256
	copy(data[0:4], selector)
	copy(data[4+12:4+32], addr.Bytes())                        // address padded to 32 bytes
	amountBytes := amount.Bytes()
	copy(data[4+32+(32-len(amountBytes)):4+64], amountBytes)    // uint256 big-endian padded
	return data
}

// parseHexOrBech32 parses a hex (0x...) or bech32 (kava1...) address.
func parseHexOrBech32(s string) common.Address {
	s = strings.TrimSpace(s)
	if s == "" {
		return common.Address{}
	}
	if common.IsHexAddress(s) {
		return common.HexToAddress(s)
	}
	accAddr, err := sdk.AccAddressFromBech32(s)
	if err == nil && len(accAddr) == 20 {
		return common.BytesToAddress(accAddr)
	}
	return common.Address{}
}
