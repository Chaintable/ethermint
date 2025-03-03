package types

type TraceResult struct {
	Traces []Trace `json:"traces"`
	Events []Event `json:"events"`
}
