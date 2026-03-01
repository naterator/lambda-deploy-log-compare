package main

import "time"

type InvocationSummary struct {
	RequestID  string
	StartTime  time.Time
	Duration   string
	BilledMs   string
	MemUsedMB  string
	MaxMemMB   string
	IsError    bool
	ErrorLines []string
	LogLines   []string
}

type Snapshot struct {
	FunctionName string             `json:"function_name"`
	LogGroup     string             `json:"log_group"`
	CapturedAt   string             `json:"captured_at"`
	Label        string             `json:"label"`
	Invocations  []InvocationRecord `json:"invocations"`
}

type InvocationRecord struct {
	RequestID  string   `json:"request_id"`
	Timestamp  string   `json:"timestamp"`
	Duration   string   `json:"duration"`
	BilledMs   string   `json:"billed_ms"`
	MemUsedMB  string   `json:"mem_used_mb"`
	MaxMemMB   string   `json:"max_mem_mb"`
	IsError    bool     `json:"is_error"`
	ErrorLines []string `json:"error_lines,omitempty"`
	LogLines   []string `json:"log_lines"`
}
