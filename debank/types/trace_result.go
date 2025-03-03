package types

type TraceResult struct {
	Transaction Transaction `json:"transaction"`
	Traces      []Trace     `json:"traces"`
	Events      []Event     `json:"events"`
}
