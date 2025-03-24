package types

type TraceResult struct {
	Transaction Transaction          `json:"transaction"`
	StateDiff   TransactionStateDiff `json:"statediff"`
	Traces      []Trace              `json:"traces"`
	Events      []Event              `json:"events"`
}
