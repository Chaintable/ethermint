package tracer

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	dtypes "github.com/evmos/ethermint/debank/types"
	"github.com/evmos/ethermint/debank/util"
	"github.com/evmos/ethermint/x/evm/statedb"
	"github.com/evmos/ethermint/x/evm/types"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/holiman/uint256"
)

const (
	Name = "debankTracer"
)

type callFrame struct {
	Type         vm.OpCode       `json:"-"`
	From         common.Address  `json:"from"`
	Gas          uint64          `json:"gas"`
	GasUsed      uint64          `json:"gasUsed"`
	To           *common.Address `json:"to,omitempty" rlp:"optional"`
	Input        []byte          `json:"input" rlp:"optional"`
	Output       []byte          `json:"output,omitempty" rlp:"optional"`
	Error        string          `json:"error,omitempty" rlp:"optional"`
	RevertReason string          `json:"revertReason,omitempty"`
	ParentFailed bool
	Calls        []callFrame    `json:"calls,omitempty" rlp:"optional"`
	Logs         []dtypes.Event `json:"logs,omitempty" rlp:"optional"`

	PosInParentTrace  int    `json:"pos_in_parent_trace"`
	ParentTraceID     string `json:"parent_trace_id"`
	TraceID           string `json:"trace_id"`
	StorageChange     bool   `json:"storageChange"`
	SelfStorageChange bool   `json:"self_storage_change"`

	// Placed at end on purpose. The RLP will be decoded to 0 instead of
	// nil if there are non-empty elements after in the struct.
	Value *big.Int `json:"value,omitempty" rlp:"optional"`
}

func (f callFrame) TypeString() string {
	return f.Type.String()
}

func (f callFrame) failed() bool {
	return len(f.Error) > 0
}

func (f *callFrame) processOutput(output []byte, err error, reverted bool) {
	output = common.CopyBytes(output)
	// Clear error if tx wasn't reverted. This happened
	// for pre-homestead contract storage OOG.
	if err != nil && !reverted {
		err = nil
	}
	if err == nil {
		f.Output = output
		return
	}
	f.Error = err.Error()
	if f.Type == vm.CREATE || f.Type == vm.CREATE2 {
		f.To = nil
	}
	if !errors.Is(err, vm.ErrExecutionReverted) || len(output) == 0 {
		return
	}
	f.Output = output
	if len(output) < 4 {
		return
	}
	if unpacked, err := abi.UnpackRevert(output); err == nil {
		f.RevertReason = unpacked
	}
}

var _ tracers.Tracer = (*CallTracer)(nil)

func (t *CallTracer) ToTrace(f *callFrame, traceAddress []int64) dtypes.Trace {
	CallCreateType := ""
	CallType := ""
	switch f.Type {
	case vm.CREATE, vm.CREATE2:
		CallCreateType = strings.ToLower(vm.CREATE.String())
	case vm.SELFDESTRUCT:
		CallCreateType = "suicide"
	case vm.CALL, vm.STATICCALL, vm.CALLCODE, vm.DELEGATECALL:
		CallCreateType = strings.ToLower(vm.CALL.String())
		CallType = strings.ToLower(f.Type.String())
	default:
		CallCreateType = "empty"
	}
	to := common.Address{}
	if f.To != nil {
		to = *f.To
	}
	value := big.NewInt(0)
	if f.Value != nil {
		value = f.Value
	}
	err := ""
	if f.failed() {
		err = f.Error
		if f.RevertReason != "" {
			err = fmt.Sprintf("%s: %s", f.Error, f.RevertReason)
		}
	}
	return dtypes.Trace{
		ID:                f.TraceID,
		From:              strings.ToLower(f.From.Hex()),
		Gas:               big.NewInt(int64(f.Gas)),
		Input:             f.Input,
		To:                strings.ToLower(to.Hex()),
		Value:             (*hexutil.Big)(value),
		GasUsed:           big.NewInt(int64(f.GasUsed)),
		Output:            f.Output,
		CallCreateType:    CallCreateType,
		CallType:          CallType,
		TxID:              t.ctx.TxHash.Hex(),
		ParentTraceID:     f.ParentTraceID,
		PosInParentTrace:  int64(f.PosInParentTrace),
		SelfStorageChange: f.SelfStorageChange,
		StorageChange:     f.StorageChange,
		Subtraces:         int64(len(f.Calls)),
		TraceAddress:      traceAddress,
		Error:             err,
	}
}

type CallTracer struct {
	callstack []callFrame
	gasLimit  uint64
	Evm       *vm.EVM
	ctx       *tracers.Context

	traces      []dtypes.Trace
	logs        []dtypes.Event
	errorTraces []dtypes.Trace
	errorLogs   []dtypes.Event
	transaction dtypes.Transaction

	storageChanges map[common.Address]struct{} // Used to track storage changes for contracts
	// to get state diff
	DeletedAccounts map[common.Hash]struct{}
	NewAccounts     map[common.Hash]statedb.Account
	StorageDiff     map[common.Hash]map[common.Hash][]byte
	NewCodes        map[common.Hash][]byte // The mutated contract code
}

func NewCallTracer(ctx *tracers.Context) *CallTracer {
	tracer := &CallTracer{
		ctx:             ctx,
		traces:          make([]dtypes.Trace, 0),
		logs:            make([]dtypes.Event, 0),
		errorTraces:     make([]dtypes.Trace, 0),
		errorLogs:       make([]dtypes.Event, 0),
		DeletedAccounts: make(map[common.Hash]struct{}),
		NewAccounts:     make(map[common.Hash]statedb.Account),
		NewCodes:        make(map[common.Hash][]byte),
		StorageDiff:     make(map[common.Hash]map[common.Hash][]byte),
		storageChanges:  make(map[common.Address]struct{}),
	}
	return tracer
}

func (t *CallTracer) CaptureTxStart(gasLimit uint64) {
	t.gasLimit = gasLimit
}

func (t *CallTracer) CaptureStart(env *vm.EVM, from common.Address, to common.Address, create bool, input []byte, gas uint64, value *big.Int) {
	toCopy := to
	tpy := vm.CALL
	if create {
		tpy = vm.CREATE
	}
	call := callFrame{
		Type:  tpy,
		From:  from,
		To:    &toCopy,
		Input: common.CopyBytes(input),
		Gas:   gas,
		Value: value,
	}
	t.Evm = env
	t.callstack = append(t.callstack, call)
}
func (t *CallTracer) CaptureEnter(typ vm.OpCode, from common.Address, to common.Address, input []byte, gas uint64, value *big.Int) {
	toCopy := to
	call := callFrame{
		Type:  typ,
		From:  from,
		To:    &toCopy,
		Input: common.CopyBytes(input),
		Gas:   gas,
		Value: value,
	}
	t.callstack = append(t.callstack, call)
}

func (t *CallTracer) CaptureExit(output []byte, usedGas uint64, err error) {
	var reverted bool
	if err != nil {
		reverted = true
	}
	isHomestead := t.Evm.ChainConfig().IsHomestead(t.Evm.Context.BlockNumber)
	if !isHomestead && errors.Is(err, vm.ErrCodeStoreOutOfGas) {
		reverted = false
	}
	size := len(t.callstack)
	if size <= 1 {
		return
	}
	// Pop call.
	call := t.callstack[size-1]
	t.callstack = t.callstack[:size-1]
	size -= 1

	call.GasUsed = usedGas
	call.processOutput(output, err, reverted)

	call.PosInParentTrace = len(t.callstack[size-1].Calls) + len(t.callstack[size-1].Logs)
	t.callstack[size-1].Calls = append(t.callstack[size-1].Calls, call)
}

func (t *CallTracer) CaptureEnd(output []byte, usedGas uint64, _ time.Duration, err error) {
	var reverted bool
	if err != nil {
		reverted = true
	}
	isHomestead := t.Evm.ChainConfig().IsHomestead(t.Evm.Context.BlockNumber)
	if !isHomestead && errors.Is(err, vm.ErrCodeStoreOutOfGas) {
		reverted = false
	}
	if len(t.callstack) != 1 {
		return
	}
	t.callstack[0].GasUsed = usedGas
	t.callstack[0].processOutput(output, err, reverted)
}

func (t *CallTracer) CaptureState(pc uint64, op vm.OpCode, gas, cost uint64, scope *vm.ScopeContext, rData []byte, opDepth int, err error) {
	if op == vm.SSTORE {
		t.callstack[len(t.callstack)-1].SelfStorageChange = true
		t.callstack[len(t.callstack)-1].StorageChange = true
	}
}

func (t *CallTracer) CaptureFault(pc uint64, op vm.OpCode, gas, cost uint64, scope *vm.ScopeContext, depth int, err error) {

}

func setParentFailed(cf *callFrame, parentFailed bool) {
	failed := cf.failed() || parentFailed
	for i := range cf.Calls {
		cf.Calls[i].ParentFailed = failed
		setParentFailed(&cf.Calls[i], failed)
	}
}

func setStorageChange(cf *callFrame) {
	subCallStorageChange := false
	for i := range cf.Calls {
		setStorageChange(&cf.Calls[i])
		if cf.Calls[i].StorageChange && !cf.Calls[i].failed() {
			subCallStorageChange = true
		}
	}
	if subCallStorageChange {
		cf.StorageChange = true
	}
}

func (t *CallTracer) CaptureTxEnd(restGas uint64) {
	if len(t.callstack) < 1 {
		return
	}
	setParentFailed(&t.callstack[0], false)
	setStorageChange(&t.callstack[0])
	if len(t.callstack) == 1 {
		topCall := &t.callstack[0]
		topCall.TraceID = util.ToHash([]string{t.ctx.TxHash.Hex(), "", "0"})
		if topCall.failed() {
			t.errorTraces = append(t.errorTraces, t.ToTrace(topCall, []int64{}))
		} else {
			t.traces = append(t.traces, t.ToTrace(topCall, []int64{}))
		}
		t.addTraceAndLog(topCall, []int64{})
	}
}

func (t *CallTracer) addTraceAndLog(cf *callFrame, traceAddress []int64) {
	for i := range cf.Calls {
		cf.Calls[i].ParentTraceID = cf.TraceID
		cf.Calls[i].TraceID = util.ToHash([]string{t.ctx.TxHash.Hex(), cf.TraceID, fmt.Sprintf("%d", cf.Calls[i].PosInParentTrace)})
		t.addTraceAndLog(&cf.Calls[i], childTraceAddress(traceAddress, int64(i)))
	}
	for i := range cf.Logs {
		cf.Logs[i].ParentTraceID = cf.TraceID
		cf.Logs[i].ID = util.ToHash([]string{cf.Logs[i].ParentTraceID, fmt.Sprintf("%d", cf.Logs[i].Position)})
		if cf.failed() || cf.ParentFailed {
			cf.Logs[i].LogIndex = 0
			t.errorLogs = append(t.errorLogs, cf.Logs[i])
		} else {
			t.logs = append(t.logs, cf.Logs[i])
		}
	}
	for i := range cf.Calls {
		if cf.Calls[i].failed() {
			t.errorTraces = append(t.errorTraces, t.ToTrace(&cf.Calls[i], childTraceAddress(traceAddress, int64(i))))
		} else {
			t.traces = append(t.traces, t.ToTrace(&cf.Calls[i], childTraceAddress(traceAddress, int64(i))))
		}
	}
}

func childTraceAddress(a []int64, i int64) []int64 {
	child := make([]int64, 0, len(a)+1)
	child = append(child, a...)
	child = append(child, i)
	return child
}

func (t *CallTracer) OnLog(log *ethtypes.Log) {
	topics := make([]string, len(log.Topics))
	for i, topic := range log.Topics {
		topics[i] = topic.Hex()
	}
	var selector string
	var remainingTopics []string

	if len(topics) > 0 {
		selector = topics[0]
		remainingTopics = topics[1:]
	}
	l := dtypes.Event{
		Address:  strings.ToLower(log.Address.Hex()),
		Selector: selector,
		Topics:   remainingTopics,
		Data:     log.Data,
		Position: int64(len(t.callstack[len(t.callstack)-1].Calls) + len(t.callstack[len(t.callstack)-1].Logs)),
		LogIndex: int64(log.Index),
	}
	t.callstack[len(t.callstack)-1].Logs = append(t.callstack[len(t.callstack)-1].Logs, l)
}

func (t *CallTracer) OnAccountSet(addr common.Address, account statedb.Account) {
	addrhash := crypto.Keccak256Hash(addr.Bytes())
	t.NewAccounts[addrhash] = account
}

func (t *CallTracer) OnAccountDelete(addr common.Address) {
	addrhash := crypto.Keccak256Hash(addr.Bytes())
	t.DeletedAccounts[addrhash] = struct{}{}
}

func (t *CallTracer) OnStateSet(addr common.Address, key common.Hash, value []byte) {
	addrhash := crypto.Keccak256Hash(addr.Bytes())
	if _, ok := t.StorageDiff[addrhash]; !ok {
		t.StorageDiff[addrhash] = make(map[common.Hash][]byte)
	}
	storageDiff := t.StorageDiff[addrhash]
	storageDiff[crypto.Keccak256Hash(key.Bytes())] = value
	t.storageChanges[addr] = struct{}{}
}

func (t *CallTracer) OnCodeSet(codeHash []byte, code []byte) {
	t.NewCodes[common.BytesToHash(codeHash)] = code
}

func (t *CallTracer) OnTxEnd(from common.Address, tx *ethtypes.Transaction, baseFee *big.Int, res *types.MsgEthereumTxResponse) {
	t.transaction = BuildPipelineTransaction(tx, int64(t.ctx.TxIndex), from, big.NewInt(int64(res.GasUsed)), baseFee, !res.Failed())
}

func (t *CallTracer) GetTraces() []dtypes.Trace {
	res := make([]dtypes.Trace, len(t.traces))
	copy(res, t.traces)
	return res
}

func (t *CallTracer) GetErrorTraces() []dtypes.Trace {
	res := make([]dtypes.Trace, len(t.errorTraces))
	copy(res, t.errorTraces)
	return res
}

func (t *CallTracer) GetLogs() []dtypes.Event {
	res := make([]dtypes.Event, len(t.logs))
	copy(res, t.logs)
	return res
}

func (t *CallTracer) GetErrorLogs() []dtypes.Event {
	res := make([]dtypes.Event, len(t.errorLogs))
	copy(res, t.errorLogs)
	return res
}

func (t *CallTracer) GetStorageAddress() []string {
	res := make([]string, 0, len(t.storageChanges))
	for address, _ := range t.storageChanges {
		res = append(res, strings.ToLower(address.String()))
	}
	return res
}

func (t *CallTracer) ToStorageDiff() dtypes.TransactionStateDiff {
	stateDiff := dtypes.TransactionStateDiff{}
	for hash := range t.DeletedAccounts {
		stateDiff.DeletedAccounts = append(stateDiff.DeletedAccounts, hash)
	}
	for addr, account := range t.NewAccounts {
		balance := uint256.NewInt(0)
		if account.Balance != nil {
			balance = uint256.NewInt(0).SetBytes(account.Balance.Bytes())
		}
		stateDiff.NewAccounts = append(stateDiff.NewAccounts, dtypes.NewAccount{
			Address:  addr,
			Balance:  balance,
			Nonce:    account.Nonce,
			CodeHash: common.BytesToHash(account.CodeHash),
		})
	}
	for account, storage := range t.StorageDiff {
		values := make([]dtypes.IndexValuePair, 0)
		for index, v := range storage {
			value := uint256.NewInt(0)
			if len(v) > 0 {
				value = uint256.NewInt(0).SetBytes(v)
			}
			values = append(values, dtypes.IndexValuePair{
				Index: index,
				Value: value,
			})
		}
		stateDiff.StorageDiff = append(stateDiff.StorageDiff, dtypes.AccountStorageDiff{
			Address: account,
			Values:  values,
		})
	}
	for hash, code := range t.NewCodes {
		stateDiff.NewCodes = append(stateDiff.NewCodes, dtypes.NewCode{
			CodeHash: hash,
			Code:     code,
		})
	}
	return stateDiff
}

func (t *CallTracer) GetResult() (json.RawMessage, error) {
	result := &dtypes.TraceResult{
		Transaction:      t.transaction,
		Traces:           t.GetTraces(),
		Events:           t.GetLogs(),
		ErrorTraces:      t.GetErrorTraces(),
		ErrorEvents:      t.GetErrorLogs(),
		StateDiff:        t.ToStorageDiff(),
		StorageContracts: t.GetStorageAddress(),
	}
	return json.Marshal(result)
}

func (t *CallTracer) Stop(err error) {}
