package main

import (
	"encoding/json"
	"time"
)

type InvocationSummary struct {
	RequestID       string
	StartTime       time.Time
	Duration        string
	BilledMs        string
	MemorySizeMB    string
	MaxMemoryUsedMB string
	IsError         bool
	ErrorLines      []string
	LogLines        []string
}

type Snapshot struct {
	FunctionName string             `json:"function_name"`
	LogGroup     string             `json:"log_group"`
	CapturedAt   string             `json:"captured_at"`
	Label        string             `json:"label"`
	Invocations  []InvocationRecord `json:"invocations"`
}

type InvocationRecord struct {
	RequestID       string
	Timestamp       string
	Duration        string
	BilledMs        string
	MemorySizeMB    string
	MaxMemoryUsedMB string
	IsError         bool
	ErrorLines      []string
	LogLines        []string
}

type invocationRecordJSON struct {
	RequestID       string   `json:"request_id"`
	Timestamp       string   `json:"timestamp"`
	Duration        string   `json:"duration"`
	BilledMs        string   `json:"billed_ms"`
	MemorySizeMB    string   `json:"memory_size_mb,omitempty"`
	MaxMemoryUsedMB string   `json:"max_memory_used_mb,omitempty"`
	LegacyMemUsedMB string   `json:"mem_used_mb,omitempty"`
	LegacyMaxMemMB  string   `json:"max_mem_mb,omitempty"`
	IsError         bool     `json:"is_error"`
	ErrorLines      []string `json:"error_lines,omitempty"`
	LogLines        []string `json:"log_lines"`
}

func (r InvocationRecord) MarshalJSON() ([]byte, error) {
	return json.Marshal(invocationRecordJSON{
		RequestID:       r.RequestID,
		Timestamp:       r.Timestamp,
		Duration:        r.Duration,
		BilledMs:        r.BilledMs,
		MemorySizeMB:    r.MemorySizeMB,
		MaxMemoryUsedMB: r.MaxMemoryUsedMB,
		IsError:         r.IsError,
		ErrorLines:      r.ErrorLines,
		LogLines:        r.LogLines,
	})
}

func (r *InvocationRecord) UnmarshalJSON(data []byte) error {
	var raw invocationRecordJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	*r = InvocationRecord{
		RequestID:       raw.RequestID,
		Timestamp:       raw.Timestamp,
		Duration:        raw.Duration,
		BilledMs:        raw.BilledMs,
		MemorySizeMB:    firstNonEmpty(raw.MemorySizeMB, raw.LegacyMemUsedMB),
		MaxMemoryUsedMB: firstNonEmpty(raw.MaxMemoryUsedMB, raw.LegacyMaxMemMB),
		IsError:         raw.IsError,
		ErrorLines:      raw.ErrorLines,
		LogLines:        raw.LogLines,
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
