package types

import (
	"math/big"

	dtypes "github.com/evmos/ethermint/debank/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

const (
	SimulateErrorUnKnown            = -39004
	SimulateErrorInsufficientBalane = -39001
	SimulateErrorReverted           = -39000
)

type DebankSingleSimulateResult struct {
	Code    int           `json:"code"`
	Err     string        `json:"err"`
	GasUsed uint64        `json:"gas_used"`
	Traces  []DebankTrace `json:"traces"`
	Events  []DebankEvent `json:"events"`
}

type DebankTrace struct {
	ID                string        `json:"id"`
	From              string        `json:"from_addr"`
	Gas               *big.Int      `json:"gas_limit"`
	Input             hexutil.Bytes `json:"input"`
	To                string        `json:"to_addr"`
	Value             *hexutil.Big  `json:"value"`
	GasUsed           *big.Int      `json:"gas_used"`
	Output            hexutil.Bytes `json:"output"`
	CallCreateType    string        `json:"type"` // ['create', 'suicide', 'call', 'empty']
	CallType          string        `json:"call_type"`
	TxID              common.Hash   `json:"tx_id"`
	ParentTraceID     string        `json:"parent_trace_id"`
	PosInParentTrace  int64         `json:"pos_in_parent_trace"`
	SelfStorageChange bool          `json:"self_storage_change"`
	StorageChange     bool          `json:"storage_change"`
}

func FromTracerTrace(trace dtypes.Trace) DebankTrace {
	return DebankTrace{
		ID:                trace.ID,
		From:              trace.From,
		Gas:               trace.Gas,
		Input:             trace.Input,
		To:                trace.To,
		Value:             trace.Value,
		GasUsed:           trace.GasUsed,
		Output:            trace.Output,
		CallCreateType:    trace.CallCreateType,
		CallType:          trace.CallType,
		ParentTraceID:     trace.ParentTraceID,
		PosInParentTrace:  trace.PosInParentTrace,
		SelfStorageChange: trace.SelfStorageChange,
		StorageChange:     trace.StorageChange,
	}
}

type DebankEvent struct {
	ID            string        `json:"id"`
	Address       string        `json:"contract_id"`
	Selector      string        `json:"selector"`
	Topics        []string      `json:"topics"`
	Data          hexutil.Bytes `json:"data"`
	TxId          common.Hash   `json:"tx_id"`
	ParentTraceID string        `json:"parent_trace_id"`
	Position      int64         `json:"pos_in_parent_trace"`
}

func FromTracerEvent(event dtypes.Event) DebankEvent {
	return DebankEvent{
		ID:            event.ID,
		Address:       event.Address,
		Selector:      event.Selector,
		Topics:        event.Topics,
		Data:          event.Data,
		ParentTraceID: event.ParentTraceID,
		Position:      event.Position,
	}
}
