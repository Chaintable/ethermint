package tracer

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/holiman/uint256"
	dtypes "github.com/zeta-chain/ethermint/debank/types"
	"github.com/zeta-chain/ethermint/x/evm/statedb"
)

const (
	Name = "debank"
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
	Calls        []callFrame     `json:"calls,omitempty" rlp:"optional"`
	Logs         []dtypes.Event  `json:"logs,omitempty" rlp:"optional"`

	PosInParentTrace  int    `json:"pos_in_parent_trace"`
	ParentTraceID     string `json:"parent_trace_id"`
	TraceID           string `json:"trace_id"`
	StorageChange     bool   `json:"storageChange"`
	SelfStorageChange bool   `json:"self_storage_change"`
	Subtraces         int    `json:"subtraces"`
	TraceAddress      []int  `json:"trace_address"`

	// Placed at end on purpose. The RLP will be decoded to 0 instead of
	// nil if there are non-empty elements after in the struct.
	Value            *big.Int `json:"value,omitempty" rlp:"optional"`
	revertedSnapshot bool
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
	f.revertedSnapshot = reverted
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

func (t *CallTracer) ToTrace(f *callFrame) dtypes.Trace {
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
		Subtraces:         f.Subtraces,
		TraceAddress:      f.TraceAddress,
	}
}

type CallTracer struct {
	callstack []callFrame
	gasLimit  uint64
	reason    error
	Evm       *vm.EVM
	ctx       *tracers.Context
	traces    []dtypes.Trace
	logs      []dtypes.Event

	// to get state diff
	DeletedAccounts map[common.Hash]struct{}
	NewAccounts     map[common.Hash]statedb.Account
	StorageDiff     map[common.Hash]map[common.Hash][]byte
	NewCodes        map[common.Hash][]byte // The mutated contract code
}

func NewCallTracer(ctx *tracers.Context) *CallTracer {
	tracer := &CallTracer{
		ctx:    ctx,
		traces: make([]dtypes.Trace, 0),
	}
	tracer.DeletedAccounts = make(map[common.Hash]struct{})
	tracer.NewAccounts = make(map[common.Hash]statedb.Account)
	tracer.StorageDiff = make(map[common.Hash]map[common.Hash][]byte)
	tracer.NewCodes = make(map[common.Hash][]byte)
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
	isHomestead := t.Evm.ChainConfig().IsHomestead(t.ctx.BlockNumber)
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
	if !call.failed() {
		call.PosInParentTrace = len(t.callstack[size-1].Calls) + len(t.callstack[size-1].Logs)
		t.callstack[size-1].Calls = append(t.callstack[size-1].Calls, call)
	}
}

func (t *CallTracer) CaptureEnd(output []byte, usedGas uint64, err error) {
	var reverted bool
	if err != nil {
		reverted = true
	}
	isHomestead := t.Evm.ChainConfig().IsHomestead(t.ctx.BlockNumber)
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
	if err != nil {
		return
	}
}

func (t *CallTracer) CaptureFault(pc uint64, op vm.OpCode, gas, cost uint64, scope *vm.ScopeContext, depth int, err error) {
}

func clearFailedLogs(cf *callFrame, parentFailed bool) {
	failed := cf.failed() || parentFailed
	// Clear own logs
	if failed {
		cf.Logs = nil
	}
	for i := range cf.Calls {
		clearFailedLogs(&cf.Calls[i], failed)
	}
}

func setStorageChange(cf *callFrame) {
	subCallStorageChange := false
	for i := range cf.Calls {
		setStorageChange(&cf.Calls[i])
		if cf.Calls[i].StorageChange {
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
	clearFailedLogs(&t.callstack[0], false)
	setStorageChange(&t.callstack[0])
	if len(t.callstack) == 1 && !t.callstack[0].failed() {
		topCall := &t.callstack[0]
		topCall.TraceID = dtypes.ToHash([]string{t.ctx.TxHash.Hex(), "", "0"})
		topCall.TraceAddress = []int{}
		topCall.Subtraces = len(topCall.Calls)
		t.traces = append(t.traces, t.ToTrace(topCall))
		t.addTraceAndLog(topCall, []int{})
	}
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
		Idx:      int(log.Index),
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
}

func (t *CallTracer) OnCodeSet(codeHash []byte, code []byte) {
	t.NewCodes[crypto.Keccak256Hash(codeHash)] = code
}

func (t *CallTracer) addTraceAndLog(cf *callFrame, traceAddress []int) {
	for i := range cf.Calls {
		childAddr := childTraceAddress(traceAddress, i)
		cf.Calls[i].ParentTraceID = cf.TraceID
		cf.Calls[i].TraceAddress = childAddr
		cf.Calls[i].Subtraces = len(cf.Calls[i].Calls)
		cf.Calls[i].TraceID = dtypes.ToHash([]string{t.ctx.TxHash.Hex(), cf.TraceID, fmt.Sprintf("%d", cf.Calls[i].PosInParentTrace)})
		t.addTraceAndLog(&cf.Calls[i], childAddr)
	}
	logIndex := len(t.logs)
	for i := range cf.Logs {
		cf.Logs[i].ParentTraceID = cf.TraceID
		cf.Logs[i].Idx = logIndex + i
		cf.Logs[i].ID = dtypes.ToHash([]string{cf.Logs[i].ParentTraceID, fmt.Sprintf("%d", cf.Logs[i].Position)})
		t.logs = append(t.logs, cf.Logs[i])
	}
	for i := range cf.Calls {
		t.traces = append(t.traces, t.ToTrace(&cf.Calls[i]))
	}
}

func (t *CallTracer) GetTraces() []dtypes.Trace {
	res := make([]dtypes.Trace, len(t.traces))
	copy(res, t.traces)
	return res
}

func (t *CallTracer) GetLogs() []dtypes.Event {
	res := make([]dtypes.Event, len(t.logs))
	copy(res, t.logs)
	return res
}

func (t *CallTracer) ToStorageDiff() dtypes.TransactionStateDiff {
	stateDiff := dtypes.TransactionStateDiff{}
	for hash := range t.DeletedAccounts {
		stateDiff.DeletedAccounts = append(stateDiff.DeletedAccounts, hash)
	}
	for addr, account := range t.NewAccounts {
		stateDiff.NewAccounts = append(stateDiff.NewAccounts, dtypes.NewAccount{
			Address:  addr,
			Balance:  account.Balance,
			Nonce:    account.Nonce,
			CodeHash: crypto.Keccak256Hash(account.CodeHash),
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
	traces := t.GetTraces()
	events := t.GetLogs()
	storageDiff := t.ToStorageDiff()
	result := &dtypes.TraceResult{
		Traces:    traces,
		Events:    events,
		StateDiff: storageDiff,
	}
	return json.Marshal(result)
}

func (t *CallTracer) Stop(err error) {

}

func childTraceAddress(a []int, i int) []int {
	child := make([]int, 0, len(a)+1)
	child = append(child, a...)
	child = append(child, i)
	return child
}
