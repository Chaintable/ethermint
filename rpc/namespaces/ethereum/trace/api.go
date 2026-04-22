package trace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"strings"
	"sync"

	"github.com/cometbft/cometbft/libs/log"
	evmtypes "github.com/evmos/ethermint/x/evm/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/server"
	"github.com/evmos/ethermint/rpc/backend"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"

	dtracer "github.com/evmos/ethermint/debank/tracer"
	dtypes "github.com/evmos/ethermint/debank/types"
	rpctypes "github.com/evmos/ethermint/rpc/types"
)

var (
	LatestBlockNumber = metrics.NewRegisteredGauge("pipeline/block_num", nil)

	LatestBlockTime = metrics.NewRegisteredGauge("pipeline/block_time", nil)
)

// HandlerT keeps track of the cpu profiler and trace execution
type HandlerT struct {
	cpuFilename   string
	cpuFile       io.WriteCloser
	mu            sync.Mutex
	traceFilename string
	traceFile     io.WriteCloser
}

// API is the collection of tracing APIs exposed over the private debugging endpoint.
type API struct {
	ctx         *server.Context
	logger      log.Logger
	backend     *backend.Backend
	clientCtx   client.Context
	queryClient *rpctypes.QueryClient
	handler     *HandlerT
}

// NewAPI creates a new API definition for the tracing methods of the Ethereum service.
func NewAPI(
	ctx *server.Context,
	logger log.Logger,
	backend *backend.Backend,
	clientCtx client.Context,
) *API {
	return &API{
		ctx:         ctx,
		logger:      logger.With("module", "trace"),
		backend:     backend,
		clientCtx:   clientCtx,
		queryClient: rpctypes.NewQueryClient(clientCtx),
		handler:     new(HandlerT),
	}
}

func (api *API) DebankBlockRaw(ctx context.Context, blockNrOrHash rpctypes.BlockNumberOrHash) (*dtypes.DebankOutPut, error) {
	blockHeight, err := api.backend.BlockNumberFromTendermint(blockNrOrHash)
	if err != nil {
		return nil, err
	}
	if blockHeight == 0 {
		return nil, fmt.Errorf("can't trace block 0")
	}

	resBlock, err := api.backend.TendermintBlockByNumber(blockHeight)
	if err != nil {
		return nil, nil
	}

	// return if requested block height is greater than the current one
	if resBlock == nil || resBlock.Block == nil {
		return nil, fmt.Errorf("cannot trace nil block")
	}

	blockRes, err := api.backend.TendermintBlockResultByNumber(&resBlock.Block.Height)
	if err != nil {
		api.logger.Debug("failed to fetch block result from Tendermint", "height", blockHeight, "error", err.Error())
		return nil, fmt.Errorf("failed to fetch block result from Tendermint")
	}
	block, err := api.backend.RPCBlockFromTendermintBlock(resBlock, blockRes, true)
	if err != nil {
		api.logger.Debug("GetEthBlockFromTendermint failed", "height", blockHeight, "error", err.Error())
		return nil, err
	}
	if blockHeight == 1 {
		return api.onGenesisBlock(block)
	}
	transactions := block["transactions"].([]interface{})
	stateHeader := dtracer.BuildPilelineBlockHeader(block)
	parentHeader, err := api.backend.HeaderByNumber(blockHeight - 1)
	if err != nil {
		return nil, err
	}
	blockFile := &dtypes.BlockFile{
		Block:            dtracer.BuildPipelineBlock(block),
		Events:           make([]dtypes.Event, 0),
		Txs:              make([]dtypes.Transaction, 0),
		Traces:           make([]dtypes.Trace, 0),
		ErrorEvents:      make([]dtypes.Event, 0),
		ErrorTraces:      make([]dtypes.Trace, 0),
		StorageContracts: make([]string, 0),
	}
	traceResults, err := api.backend.TraceBlock(blockHeight, &evmtypes.TraceConfig{Tracer: dtracer.Name}, resBlock)
	if err != nil {
		return nil, err
	}
	transactionStates := make([]dtypes.TransactionStateDiff, 0)
	fromToAddress := make(map[common.Address]struct{})
	for i := range transactions {
		transaction := transactions[i].(*rpctypes.RPCTransaction)
		fromToAddress[transaction.From] = struct{}{}
		if transaction.To != nil && transaction.To.Hex() != "" {
			fromToAddress[*transaction.To] = struct{}{}
		}
	}
	// Include addresses affected by Cosmos-native operations
	// (BeginBlocker/EndBlocker: staking rewards, kavadist, IBC transfers, etc.)
	// These balance changes are invisible to the EVM tracer.
	cosmosAddrs := extractCosmosAffectedAddressesFromBlock(
		blockRes.BeginBlockEvents, blockRes.EndBlockEvents, blockRes.TxsResults)
	for addr := range cosmosAddrs {
		fromToAddress[addr] = struct{}{}
	}
	// Include all Cosmos tx signers — pure Cosmos txs (MsgDelegate, MsgVote, etc.)
	// change the signer's nonce but don't emit transfer/coin events.
	if txDecoder := api.clientCtx.TxConfig.TxDecoder(); txDecoder != nil {
		for _, txBytes := range resBlock.Block.Txs {
			decodedTx, err := txDecoder(txBytes)
			if err != nil {
				continue
			}
			for _, signer := range decodedTx.GetMsgs() {
				for _, signerAddr := range signer.GetSigners() {
					if len(signerAddr) == 20 {
						fromToAddress[common.BytesToAddress(signerAddr)] = struct{}{}
					}
				}
			}
		}
	}
	// Build tx hash -> receipt map for cross-validation of trace results.
	// Tracer may incorrectly report OOG transactions as successful, so we
	// use receipts as the source of truth for status and gasUsed.
	type receiptInfo struct {
		status  bool
		gasUsed uint64
	}
	receiptMap := make(map[string]receiptInfo)
	for i := range transactions {
		transaction := transactions[i].(*rpctypes.RPCTransaction)
		txHash := transaction.Hash.Hex()
		receipt, err := api.backend.GetTransactionReceipt(transaction.Hash)
		if err != nil || receipt == nil {
			// Receipt can be nil for MsgEthereumTx that failed at Cosmos ante
			// handler level (e.g. block gas limit exceeded, code=11). These txs
			// are included in the EVM block by TxSuccessOrExceedsBlockGasLimit
			// but the EVM never executed, so no receipt was indexed. Skip them.
			api.logger.Info("receipt not found, skipping tx (likely block gas limit exceeded)", "hash", txHash)
			continue
		}
		var rStatus bool
		var rGasUsed uint64
		switch st := receipt["status"].(type) {
		case hexutil.Uint:
			rStatus = uint64(st) == 1
		case string:
			rStatus = st == "0x1"
		default:
			// P0-4: Unknown status type — fail loud rather than defaulting
			// to false (which would mark all txs as failed).
			return nil, fmt.Errorf("unexpected receipt status type %T for tx %s", receipt["status"], txHash)
		}
		switch gu := receipt["gasUsed"].(type) {
		case hexutil.Uint64:
			rGasUsed = uint64(gu)
		case string:
			rGasUsed, _ = hexutil.DecodeUint64(gu)
		default:
			return nil, fmt.Errorf("unexpected receipt gasUsed type %T for tx %s", receipt["gasUsed"], txHash)
		}
		receiptMap[strings.ToLower(txHash)] = receiptInfo{
			status:  rStatus,
			gasUsed: rGasUsed,
		}
	}

	// nonEVMStateDiff is the block-level state diff for non-EVM paths.
	var nonEVMStateDiff *dtypes.TransactionStateDiff
	for _, result := range traceResults {
		if result.Error != "" {
			api.logger.Error("trace result error", "error", result.Error)
			continue
		}
		traceResultRaw, ok := result.Result.(map[string]interface{})
		if !ok {
			api.logger.Error("failed to parse trace result: %+v", result)
			return nil, status.Error(codes.Internal, "trace result parse error")
		}
		// Detect non-EVM state diff sentinel.
		if rawDiff, isSentinel := traceResultRaw["_non_evm_state_diff"]; isSentinel {
			parsed, err := decodeNonEVMStateDiff(rawDiff)
			if err != nil {
				api.logger.Error("failed to decode non-evm state diff", "err", err)
				continue
			}
			nonEVMStateDiff = parsed
			continue
		}
		decoded, err := json.Marshal(traceResultRaw)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		var traceResult dtypes.TraceResult
		if err = json.Unmarshal(decoded, &traceResult); err != nil {
			return nil, status.Error(codes.Internal, fmt.Sprintf("trace result parse error: %v", err))
		}

		// Cross-validate with receipt: fix OOG tx status/gasUsed and
		// discard state changes from failed txs (Bug B & C fix).
		txID := strings.ToLower(traceResult.Transaction.ID)
		if ri, ok := receiptMap[txID]; ok && !ri.status {
			// Receipt says tx failed — override tracer's incorrect success status.
			traceResult.Transaction.Status = false
			traceResult.Transaction.GasUsed = new(big.Int).SetUint64(ri.gasUsed)
			// Move events/traces to error buckets; discard StorageDiff.
			traceResult.ErrorEvents = append(traceResult.ErrorEvents, traceResult.Events...)
			traceResult.Events = nil
			traceResult.ErrorTraces = append(traceResult.ErrorTraces, traceResult.Traces...)
			traceResult.Traces = nil
			traceResult.StateDiff = dtypes.TransactionStateDiff{
				NewAccounts:     make([]dtypes.NewAccount, 0),
				DeletedAccounts: make([]common.Hash, 0),
				StorageDiff:     make([]dtypes.AccountStorageDiff, 0),
				NewCodes:        make([]dtypes.NewCode, 0),
			}
		}

		blockFile.Txs = append(blockFile.Txs, traceResult.Transaction)
		blockFile.Traces = append(blockFile.Traces, traceResult.Traces...)
		blockFile.Events = append(blockFile.Events, traceResult.Events...)
		blockFile.ErrorEvents = append(blockFile.ErrorEvents, traceResult.ErrorEvents...)
		blockFile.ErrorTraces = append(blockFile.ErrorTraces, traceResult.ErrorTraces...)
		blockFile.StorageContracts = append(blockFile.StorageContracts, traceResult.StorageContracts...)
		transactionStates = append(transactionStates, traceResult.StateDiff)
	}
	// Always include evmutil-affected addresses in fromToAddress so
	// addGasUsedStateDiff queries their final account state and code.
	// This is needed even when sentinel is present, because the sentinel
	// only covers StorageDiff — NewCodes for deployed contracts must come
	// from addGasUsedStateDiff's eth_getCode queries.
	evtAddrs := extractEvmutilAffectedAddresses(blockRes.TxsResults)
	for addr := range evtAddrs {
		fromToAddress[addr] = struct{}{}
	}
	// If no stored non-EVM diff (historical blocks processed before
	// StateDiffCollector), reconstruct from evmutil block events + archive state.
	if nonEVMStateDiff == nil {
		evtDiff, _, err := reconstructEvmutilDiff(api.backend, blockRes.TxsResults, blockHeight)
		if err != nil {
			return nil, fmt.Errorf("reconstructEvmutilDiff failed at height %d: %w", blockHeight, err)
		}
		if evtDiff != nil {
			nonEVMStateDiff = evtDiff
		}
	}
	// Append non-EVM diff LAST so BuildBlockStateDiff's per-address overwrite
	// semantics make it the source of truth (reflects actual final state).
	if nonEVMStateDiff != nil {
		transactionStates = append(transactionStates, *nonEVMStateDiff)
	}
	for i := range blockFile.Events {
		blockFile.Events[i].LogIndex = int64(i)
	}
	stateDiff := dtracer.BuildBlockStateDiff(parentHeader.Root, stateHeader.StateRoot, transactionStates)
	// 通过tracer获得的stateDiff拿不到tx的gasUsed的变化，进行后处理
	// evm暂时有bug 无法trace失败的transaction，hack导致to地址的balance不准确
	newAccounts, extraNewCodes, storageContracts, err := api.addGasUsedStateDiff(fromToAddress, stateDiff.NewAccounts, stateDiff.NewCodes, blockFile.StorageContracts, blockHeight)
	if err != nil {
		return nil, err
	}
	stateDiff.NewAccounts = newAccounts
	stateDiff.NewCodes = append(stateDiff.NewCodes, extraNewCodes...)
	blockFile.StorageContracts = storageContracts

	// For newly deployed contracts, enumerate ALL storage via gRPC StorageAll
	// at height N to catch constructor-written metadata slots (name/symbol/owner).
	api.enrichNewContractStorage(&stateDiff, fromToAddress, blockHeight)

	out := &dtypes.DebankOutPut{
		BlockFile:      blockFile,
		Header:         stateHeader,
		StateDiff:      &stateDiff,
		ValidationHash: blockFile.Validation().ValidationHash,
	}
	return out, nil
}

func (api API) DebankBlock(ctx context.Context, blockNrOrHash rpctypes.BlockNumberOrHash) (*rpctypes.DebankOutPutJs, error) {
	output, err := api.DebankBlockRaw(ctx, blockNrOrHash)
	if err != nil {
		return nil, err
	}
	data, err := rlp.EncodeToBytes(output.StateDiff)
	if err != nil {
		return nil, err
	}
	LatestBlockNumber.Update(int64(output.Header.Number.ToInt().Uint64()))
	LatestBlockTime.Update(int64(output.Header.Timestamp))

	return &rpctypes.DebankOutPutJs{
		BlockFile:      output.BlockFile,
		Header:         output.Header,
		StateDiff:      data,
		ValidationHash: output.ValidationHash,
	}, nil
}

// decodeNonEVMStateDiff converts the JSON-decoded sentinel payload (already a
// map[string]interface{}) into a TransactionStateDiff via re-marshal + unmarshal.
// The double round-trip is needed because traceResults arrive as generic JSON.
func decodeNonEVMStateDiff(raw interface{}) (*dtypes.TransactionStateDiff, error) {
	if raw == nil {
		return nil, fmt.Errorf("empty diff")
	}
	buf, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var diff dtypes.TransactionStateDiff
	if err := json.Unmarshal(buf, &diff); err != nil {
		return nil, err
	}
	return &diff, nil
}

func (api API) addGasUsedStateDiff(txFromAddress map[common.Address]struct{}, newAccount []dtypes.NewAccount, existingNewCodes []dtypes.NewCode, storageChange []string, number rpctypes.BlockNumber) ([]dtypes.NewAccount, []dtypes.NewCode, []string, error) {
	var (
		newAccountMap    = make(map[common.Hash]dtypes.NewAccount)
		storageChangeMap = make(map[common.Address]struct{})
		// Track code hashes already in NewCodes from CallTracer.
		knownCodeHashes  = make(map[common.Hash]struct{})
		newCodes         = make([]dtypes.NewCode, 0)
	)
	for _, account := range newAccount {
		newAccountMap[account.Address] = account
	}
	for _, nc := range existingNewCodes {
		knownCodeHashes[nc.CodeHash] = struct{}{}
	}
	for _, address := range storageChange {
		storageChangeMap[common.HexToAddress(address)] = struct{}{}
	}
	emptyCodeHash := crypto.Keccak256Hash(nil)
	for addr := range txFromAddress {
		var addrHash = crypto.Keccak256Hash(addr.Bytes())
		balance, err := api.backend.GetBalance(addr, rpctypes.BlockNumberOrHash{BlockNumber: &number})
		if err != nil {
			return nil, nil, nil, err
		}
		nonce, err := api.backend.GetTransactionCount(addr, number)
		if err != nil {
			return nil, nil, nil, err
		}
		code, err := api.backend.GetCode(addr, rpctypes.BlockNumberOrHash{BlockNumber: &number})
		if err != nil {
			return nil, nil, nil, err
		}
		var nonceVal uint64
		if nonce != nil {
			nonceVal = uint64(*nonce)
		}
		codeHash := crypto.Keccak256Hash(code)
		newAccountMap[addrHash] = dtypes.NewAccount{
			Address:  addrHash,
			Balance:  new(uint256.Int).SetBytes((*big.Int)(balance).Bytes()),
			Nonce:    nonceVal,
			CodeHash: codeHash,
		}
		// If this address has contract code and the code hash is not already
		// in NewCodes (from CallTracer), add it. This catches contract
		// deployments from non-EVM paths (e.g. evmutil ConvertCosmosCoinToERC20).
		if len(code) > 0 && codeHash != emptyCodeHash {
			if _, known := knownCodeHashes[codeHash]; !known {
				knownCodeHashes[codeHash] = struct{}{}
				newCodes = append(newCodes, dtypes.NewCode{
					CodeHash: codeHash,
					Code:     code,
				})
			}
		}
		storageChangeMap[addr] = struct{}{}
	}

	var (
		resNewAccount    = make([]dtypes.NewAccount, 0, len(newAccountMap))
		resStorageChange = make([]string, 0, len(storageChangeMap))
	)
	for _, acc := range newAccountMap {
		resNewAccount = append(resNewAccount, acc)
	}
	for address := range storageChangeMap {
		resStorageChange = append(resStorageChange, strings.ToLower(address.String()))
	}
	return resNewAccount, newCodes, resStorageChange, nil
}

// enrichNewContractStorage detects contracts newly deployed in this block
// (code was empty at N-1, non-empty at N) and enumerates ALL their storage
// at height N via gRPC StorageAll (ForEachStorage). Missing slots are added
// to stateDiff.StorageDiff.
func (api *API) enrichNewContractStorage(
	stateDiff *dtypes.BlockStorageDiff,
	fromToAddress map[common.Address]struct{},
	blockHeight rpctypes.BlockNumber,
) {
	emptyCodeHash := crypto.Keccak256Hash(nil)
	prevHeight := blockHeight - 1

	for addr := range fromToAddress {
		// Check code at N
		heightOrHash := rpctypes.BlockNumberOrHash{BlockNumber: &blockHeight}
		codeN, err := api.backend.GetCode(addr, heightOrHash)
		if err != nil || len(codeN) == 0 || crypto.Keccak256Hash(codeN) == emptyCodeHash {
			continue
		}

		// Check code at N-1 — if non-empty, not a new deployment
		prevHeightOrHash := rpctypes.BlockNumberOrHash{BlockNumber: &prevHeight}
		codePrev, _ := api.backend.GetCode(addr, prevHeightOrHash)
		if len(codePrev) > 0 && crypto.Keccak256Hash(codePrev) != emptyCodeHash {
			continue
		}

		// New contract: enumerate all storage at height N
		allStorage, err := api.backend.GetAllContractStorage(addr, blockHeight)
		if err != nil {
			api.logger.Debug("failed to enumerate new contract storage", "addr", addr.Hex(), "err", err)
			continue
		}
		if len(allStorage) == 0 {
			continue
		}

		// Merge into existing StorageDiff entry or create new one
		addrHash := crypto.Keccak256Hash(addr.Bytes())
		var found bool
		for i, entry := range stateDiff.StorageDiff {
			if entry.Address == addrHash {
				existingSlots := make(map[common.Hash]struct{})
				for _, pair := range entry.Values {
					existingSlots[pair.Index] = struct{}{}
				}
				for slotKey, slotValue := range allStorage {
					// Hash the raw key to match pipeline convention: Index = keccak(rawKey)
					slotIndex := crypto.Keccak256Hash(slotKey.Bytes())
					if _, exists := existingSlots[slotIndex]; !exists {
						v := uint256.NewInt(0).SetBytes(slotValue.Bytes())
						stateDiff.StorageDiff[i].Values = append(stateDiff.StorageDiff[i].Values, dtypes.IndexValuePair{
							Index: slotIndex,
							Value: v,
						})
					}
				}
				found = true
				break
			}
		}
		if !found {
			pairs := make([]dtypes.IndexValuePair, 0, len(allStorage))
			for slotKey, slotValue := range allStorage {
				slotIndex := crypto.Keccak256Hash(slotKey.Bytes())
				v := uint256.NewInt(0).SetBytes(slotValue.Bytes())
				pairs = append(pairs, dtypes.IndexValuePair{Index: slotIndex, Value: v})
			}
			stateDiff.StorageDiff = append(stateDiff.StorageDiff, dtypes.AccountStorageDiff{
				Address: addrHash,
				Values:  pairs,
			})
		}
	}
}

// enrichNewContractStorage detects contracts newly deployed in this block and
// enumerates ALL their storage slots via ABCI subspace query at height N.
// Missing slots are added to stateDiff.StorageDiff. This catches constructor-
// written metadata (name/symbol/owner) from non-EVM deployment paths (evmutil).
