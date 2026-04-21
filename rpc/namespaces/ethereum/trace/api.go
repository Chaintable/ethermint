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
	// Build tx hash -> failed status map from block results (Cosmos layer).
	// Uses blockRes we already have — zero extra RPC calls.
	// When a Cosmos tx containing MsgEthereumTx has Code != 0,
	// the EVM execution was reverted and its state changes should be discarded.
	txFailedMap := make(map[string]bool) // lowercase tx hash -> true if failed
	if blockRes != nil && resBlock != nil {
		txDecoder := api.clientCtx.TxConfig.TxDecoder()
		for i, rawTx := range resBlock.Block.Txs {
			if i >= len(blockRes.TxsResults) {
				break
			}
			txResult := blockRes.TxsResults[i]
			if txResult.Code == 0 {
				continue // success, skip
			}
			// Decode to find MsgEthereumTx hash
			decodedTx, err := txDecoder(rawTx)
			if err != nil {
				continue
			}
			for _, msg := range decodedTx.GetMsgs() {
				if ethMsg, ok := msg.(*evmtypes.MsgEthereumTx); ok {
					txHash := ethMsg.AsTransaction().Hash().Hex()
					txFailedMap[strings.ToLower(txHash)] = true
				}
			}
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

		// Cross-validate: fix failed tx status/gasUsed and discard
		// their state changes (Bug B & C fix).
		txID := strings.ToLower(traceResult.Transaction.ID)

		// Detect suspect OOG txs: tracer reports gasUsed == gasLimit/2
		// (minGasMultiplier=0.5 signature). These txs appear successful
		// in tracer and Cosmos layer but failed in receipt.
		isSuspectOOG := false
		if traceResult.Transaction.Gas != nil && traceResult.Transaction.GasUsed != nil &&
			traceResult.Transaction.Gas.Sign() > 0 {
			gasLimit := traceResult.Transaction.Gas
			gasUsed := traceResult.Transaction.GasUsed
			half := new(big.Int).Div(gasLimit, big.NewInt(2))
			isSuspectOOG = gasUsed.Cmp(half) == 0
		}

		isFailed := txFailedMap[txID] || !traceResult.Transaction.Status
		// For suspect OOG, verify against receipt
		if isSuspectOOG && !isFailed {
			receipt, err := api.backend.GetTransactionReceipt(common.HexToHash(traceResult.Transaction.ID))
			if err == nil && receipt != nil {
				var rStatus bool
				switch st := receipt["status"].(type) {
				case hexutil.Uint:
					rStatus = uint64(st) == 1
				}
				if !rStatus {
					isFailed = true
					// Override gasUsed from receipt
					switch gu := receipt["gasUsed"].(type) {
					case hexutil.Uint64:
						traceResult.Transaction.GasUsed = new(big.Int).SetUint64(uint64(gu))
					}
				}
			}
		}

		if isFailed {
			traceResult.Transaction.Status = false
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
	// If no stored non-EVM diff (historical blocks processed before
	// StateDiffCollector), reconstruct from evmutil block events + archive state.
	if nonEVMStateDiff == nil {
		evtDiff, evtAddrs := reconstructEvmutilDiff(api.backend, blockRes.TxsResults, blockHeight)
		if evtDiff != nil {
			nonEVMStateDiff = evtDiff
		}
		// Include evmutil-affected addresses in fromToAddress so
		// addGasUsedStateDiff queries their final account state.
		for addr := range evtAddrs {
			fromToAddress[addr] = struct{}{}
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
	newAccounts, storageContracts, err := api.addGasUsedStateDiff(fromToAddress, stateDiff.NewAccounts, blockFile.StorageContracts, blockHeight)
	if err != nil {
		return nil, err
	}
	stateDiff.NewAccounts = newAccounts
	blockFile.StorageContracts = storageContracts
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

func (api API) addGasUsedStateDiff(txFromAddress map[common.Address]struct{}, newAccount []dtypes.NewAccount, storageChange []string, number rpctypes.BlockNumber) ([]dtypes.NewAccount, []string, error) {
	var (
		newAccountMap    = make(map[common.Hash]dtypes.NewAccount)
		storageChangeMap = make(map[common.Address]struct{})
	)
	for _, account := range newAccount {
		newAccountMap[account.Address] = account
	}
	for _, address := range storageChange {
		storageChangeMap[common.HexToAddress(address)] = struct{}{}
	}
	for addr := range txFromAddress {
		var addrHash = crypto.Keccak256Hash(addr.Bytes())
		balance, err := api.backend.GetBalance(addr, rpctypes.BlockNumberOrHash{BlockNumber: &number})
		if err != nil {
			return nil, nil, err
		}
		nonce, err := api.backend.GetTransactionCount(addr, number)
		if err != nil {
			return nil, nil, err
		}
		code, err := api.backend.GetCode(addr, rpctypes.BlockNumberOrHash{BlockNumber: &number})
		if err != nil {
			return nil, nil, err
		}
		newAccountMap[addrHash] = dtypes.NewAccount{
			Address:  addrHash,
			Balance:  new(uint256.Int).SetBytes((*big.Int)(balance).Bytes()),
			Nonce:    uint64(*nonce),
			CodeHash: crypto.Keccak256Hash(code),
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
	return resNewAccount, resStorageChange, nil
}
