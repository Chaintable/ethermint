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
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/tracers"
	dtypes "github.com/zeta-chain/ethermint/debank/types"
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

var _ tracers.Tracer = (*callTracer)(nil)

func (t *callTracer) ToTrace(f *callFrame) dtypes.Trace {
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

type callTracer struct {
	callstack []callFrame
	gasLimit  uint64
	reason    error
	Evm       *vm.EVM
	ctx       *tracers.Context
	traces    []dtypes.Trace
}

func NewCallTracer(ctx *tracers.Context) *callTracer {
	return &callTracer{
		ctx:    ctx,
		traces: make([]dtypes.Trace, 0),
	}
}

func (t *callTracer) CaptureTxStart(gasLimit uint64) {
	t.gasLimit = gasLimit
}

func (t *callTracer) CaptureStart(env *vm.EVM, from common.Address, to common.Address, create bool, input []byte, gas uint64, value *big.Int) {
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
func (t *callTracer) CaptureEnter(typ vm.OpCode, from common.Address, to common.Address, input []byte, gas uint64, value *big.Int) {
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

func (t *callTracer) CaptureExit(output []byte, usedGas uint64, err error) {
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
		t.callstack[size-1].Calls = append(t.callstack[size-1].Calls, call)
	}
}

func (t *callTracer) CaptureEnd(output []byte, usedGas uint64, err error) {
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

func (t *callTracer) CaptureState(pc uint64, op vm.OpCode, gas, cost uint64, scope *vm.ScopeContext, rData []byte, opDepth int, err error) {
	if op == vm.SSTORE {
		t.callstack[len(t.callstack)-1].SelfStorageChange = true
		t.callstack[len(t.callstack)-1].StorageChange = true
	}
}

func (t *callTracer) CaptureFault(pc uint64, op vm.OpCode, gas, cost uint64, scope *vm.ScopeContext, depth int, err error) {

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

func (t *callTracer) CaptureTxEnd(restGas uint64) {
	clearFailedLogs(&t.callstack[0], false)
	setStorageChange(&t.callstack[0])
	if len(t.callstack) == 1 && !t.callstack[0].failed() {
		topCall := &t.callstack[0]
		topCall.TraceID = dtypes.ToHash([]string{t.ctx.TxHash.Hex(), "", "0"})
		topCall.TraceAddress = []int{}
		topCall.Subtraces = len(topCall.Calls)
		t.traces = append(t.traces, t.ToTrace(topCall))
		t.addTrace(topCall, []int{})
	}
}

func (t *callTracer) addTrace(cf *callFrame, traceAddress []int) {
	for i := range cf.Calls {
		childAddr := childTraceAddress(traceAddress, i)
		cf.Calls[i].ParentTraceID = cf.TraceID
		cf.Calls[i].PosInParentTrace = i
		cf.Calls[i].TraceAddress = childAddr
		cf.Calls[i].Subtraces = len(cf.Calls[i].Calls)
		cf.Calls[i].TraceID = dtypes.ToHash([]string{t.ctx.TxHash.Hex(), cf.TraceID, fmt.Sprintf("%d", cf.Calls[i].PosInParentTrace)})
		t.addTrace(&cf.Calls[i], childAddr)
	}
	for i := range cf.Calls {
		t.traces = append(t.traces, t.ToTrace(&cf.Calls[i]))
	}
}

func (t *callTracer) GetTraces() []dtypes.Trace {
	res := make([]dtypes.Trace, len(t.traces))
	copy(res, t.traces)
	return res
}

func (t *callTracer) GetResult() (json.RawMessage, error) {
	traces := t.GetTraces()
	result := &dtypes.TraceResult{
		Traces: traces,
		Events: nil,
	}
	return json.Marshal(result)
}

func (t *callTracer) Stop(err error) {

}

func childTraceAddress(a []int, i int) []int {
	child := make([]int, 0, len(a)+1)
	child = append(child, a...)
	child = append(child, i)
	return child
}
