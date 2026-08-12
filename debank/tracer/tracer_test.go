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
