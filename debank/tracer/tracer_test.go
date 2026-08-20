package tracer

import (
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/tracers"
	dtypes "github.com/evmos/ethermint/debank/types"
)

func testAddress(value byte) *common.Address {
	address := common.BytesToAddress([]byte{value})
	return &address
}

func TestFailedParentRoutesWholeSubtreeToErrorTraces(t *testing.T) {
	grandchild := callFrame{Type: vm.CALL, To: testAddress(3), Error: vm.ErrOutOfGas.Error()}
	child := callFrame{
		Type:  vm.CALL,
		To:    testAddress(2),
		Calls: []callFrame{grandchild},
		Logs:  []dtypes.Event{{Address: "0x02", LogIndex: 7}},
	}
	root := callFrame{
		Type:  vm.CALL,
		To:    testAddress(1),
		Error: vm.ErrExecutionReverted.Error(),
		Calls: []callFrame{child},
	}

	tracer := NewCallTracer(&tracers.Context{TxHash: common.HexToHash("0x1")})
	tracer.callstack = []callFrame{root}
	tracer.CaptureTxEnd(0)

	if len(tracer.traces) != 0 || len(tracer.errorTraces) != 3 {
		t.Fatalf("traces/error_traces = %d/%d, want 0/3", len(tracer.traces), len(tracer.errorTraces))
	}
	findTrace := func(address *common.Address) dtypes.Trace {
		want := strings.ToLower(address.Hex())
		for _, trace := range tracer.errorTraces {
			if trace.To == want {
				return trace
			}
		}
		t.Fatalf("error trace to %s not found", want)
		return dtypes.Trace{}
	}
	if got := findTrace(testAddress(1)).Error; got != vm.ErrExecutionReverted.Error() {
		t.Errorf("root error = %q", got)
	}
	if got := findTrace(testAddress(2)).Error; got != "parent call failed" {
		t.Errorf("successful descendant error = %q", got)
	}
	if got := findTrace(testAddress(3)).Error; got != vm.ErrOutOfGas.Error() {
		t.Errorf("failed descendant error = %q", got)
	}
	if len(tracer.logs) != 0 || len(tracer.errorLogs) != 1 {
		t.Errorf("logs/error_logs = %d/%d, want 0/1", len(tracer.logs), len(tracer.errorLogs))
	}
}

func TestFailedBranchDoesNotMoveSuccessfulSibling(t *testing.T) {
	failedDescendant := callFrame{Type: vm.CALL, To: testAddress(3)}
	failedChild := callFrame{
		Type:  vm.CALL,
		To:    testAddress(2),
		Error: vm.ErrExecutionReverted.Error(),
		Calls: []callFrame{failedDescendant},
	}
	successfulChild := callFrame{Type: vm.CALL, To: testAddress(4)}
	root := callFrame{
		Type:  vm.CALL,
		To:    testAddress(1),
		Calls: []callFrame{failedChild, successfulChild},
	}

	tracer := NewCallTracer(&tracers.Context{TxHash: common.HexToHash("0x2")})
	tracer.callstack = []callFrame{root}
	tracer.CaptureTxEnd(0)

	if len(tracer.traces) != 2 || len(tracer.errorTraces) != 2 {
		t.Fatalf(
			"traces/error_traces = %d/%d, want 2/2",
			len(tracer.traces),
			len(tracer.errorTraces),
		)
	}
	if got := findTraceError(t, tracer.traces, testAddress(4)); got != "" {
		t.Errorf("successful sibling error = %q, want empty", got)
	}
	if got := findTraceError(t, tracer.errorTraces, testAddress(3)); got != "parent call failed" {
		t.Errorf("failed descendant error = %q, want parent call failed", got)
	}
}

func TestAllSuccessNoErrorTraces(t *testing.T) {
	child := callFrame{
		Type: vm.CALL,
		To:   testAddress(2),
		Logs: []dtypes.Event{{Address: "0x02", LogIndex: 1}},
	}
	root := callFrame{Type: vm.CALL, To: testAddress(1), Calls: []callFrame{child}}

	tracer := NewCallTracer(&tracers.Context{TxHash: common.HexToHash("0x3")})
	tracer.callstack = []callFrame{root}
	tracer.CaptureTxEnd(0)

	if len(tracer.traces) != 2 || len(tracer.errorTraces) != 0 {
		t.Fatalf(
			"traces/error_traces = %d/%d, want 2/0",
			len(tracer.traces),
			len(tracer.errorTraces),
		)
	}
	if len(tracer.logs) != 1 || len(tracer.errorLogs) != 0 {
		t.Fatalf(
			"events/error_events = %d/%d, want 1/0",
			len(tracer.logs),
			len(tracer.errorLogs),
		)
	}
}

func findTraceError(t *testing.T, traces []dtypes.Trace, address *common.Address) string {
	t.Helper()
	want := strings.ToLower(address.Hex())
	for _, trace := range traces {
		if trace.To == want {
			return trace.Error
		}
	}
	t.Fatalf("trace to %s not found", want)
	return ""
}
