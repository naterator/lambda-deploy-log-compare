package main

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

func TestExtractRequestID(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		prefix string
		want   string
	}{
		{
			"START line",
			"START RequestId: abc-123-def Version: $LATEST",
			"START RequestId: ",
			"abc-123-def",
		},
		{
			"REPORT line",
			"REPORT RequestId: abc-123-def\tDuration: 100 ms",
			"REPORT RequestId: ",
			"abc-123-def",
		},
		{
			"no trailing fields",
			"START RequestId: abc-123-def",
			"START RequestId: ",
			"abc-123-def",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractRequestID(tt.line, tt.prefix)
			if got != tt.want {
				t.Errorf("extractRequestID(%q, %q) = %q, want %q", tt.line, tt.prefix, got, tt.want)
			}
		})
	}
}

func TestExtractField(t *testing.T) {
	report := "REPORT RequestId: abc\tDuration: 123.45 ms\tBilled Duration: 200 ms\tMemory Size: 128 MB\tMax Memory Used: 85 MB"
	tests := []struct {
		name      string
		fieldName string
		want      string
	}{
		{"duration", "Duration: ", "123.45 ms"},
		{"billed", "Billed Duration: ", "200 ms"},
		{"memory size", "Memory Size: ", "128 MB"},
		{"max memory", "Max Memory Used: ", "85 MB"},
		{"missing field", "Missing: ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractField(report, tt.fieldName)
			if got != tt.want {
				t.Errorf("extractField(report, %q) = %q, want %q", tt.fieldName, got, tt.want)
			}
		})
	}
}

func TestNormalizeLogLine(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			"UUID collapsed",
			"Processing request 550e8400-e29b-41d4-a716-446655440000 done",
			"Processing request <UUID> done",
		},
		{
			"short hex not collapsed",
			"Status code: 200 abc123",
			"Status code: 200 abc123",
		},
		{
			"truncated to 100 chars",
			"This is a very long log line that should be truncated because it exceeds the one hundred character limit that is enforced by the normalize function",
			"This is a very long log line that should be truncated because it exceeds the one hundred character l",
		},
		{
			"whitespace trimmed",
			"  some log line  ",
			"some log line",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeLogLine(tt.input)
			if got != tt.want {
				t.Errorf("normalizeLogLine(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCollapseHexRuns(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		minLen int
		want   string
	}{
		{
			"long hex run replaced",
			"id=550e8400e29b41d4a716446655440000",
			32,
			"id=<UUID>",
		},
		{
			"short hex run preserved",
			"code=abcdef",
			32,
			"code=abcdef",
		},
		{
			"multiple UUIDs",
			"from 550e8400-e29b-41d4-a716-446655440000 to 660f9500-f30c-52e5-b827-557766551111",
			32,
			"from <UUID> to <UUID>",
		},
		{
			"no hex at all",
			"plain text message",
			32,
			"plain text message",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collapseHexRuns(tt.input, tt.minLen, "<UUID>")
			if got != tt.want {
				t.Errorf("collapseHexRuns(%q, %d) = %q, want %q", tt.input, tt.minLen, got, tt.want)
			}
		})
	}
}

func TestParseInvocations(t *testing.T) {
	ts := time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC).UnixMilli()

	events := []types.OutputLogEvent{
		{Message: aws.String("START RequestId: req-1 Version: $LATEST"), Timestamp: aws.Int64(ts)},
		{Message: aws.String("Processing item 42"), Timestamp: aws.Int64(ts + 100)},
		{Message: aws.String("END RequestId: req-1"), Timestamp: aws.Int64(ts + 200)},
		{Message: aws.String("REPORT RequestId: req-1\tDuration: 150.5 ms\tBilled Duration: 200 ms\tMemory Size: 128 MB\tMax Memory Used: 85 MB"), Timestamp: aws.Int64(ts + 300)},
	}

	got := parseInvocations(events)
	if len(got) != 1 {
		t.Fatalf("expected 1 invocation, got %d", len(got))
	}
	inv := got[0]
	if inv.RequestID != "req-1" {
		t.Errorf("RequestID = %q, want %q", inv.RequestID, "req-1")
	}
	if inv.Duration != "150.5 ms" {
		t.Errorf("Duration = %q, want %q", inv.Duration, "150.5 ms")
	}
	if inv.MemUsedMB != "128 MB" {
		t.Errorf("MemUsedMB = %q, want %q", inv.MemUsedMB, "128 MB")
	}
	if inv.MaxMemMB != "85 MB" {
		t.Errorf("MaxMemMB = %q, want %q", inv.MaxMemMB, "85 MB")
	}
	if inv.IsError {
		t.Error("expected no error")
	}
	if len(inv.LogLines) != 1 || inv.LogLines[0] != "Processing item 42" {
		t.Errorf("LogLines = %v, want [Processing item 42]", inv.LogLines)
	}
}

func TestParseInvocations_ErrorDetection(t *testing.T) {
	ts := time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC).UnixMilli()

	events := []types.OutputLogEvent{
		{Message: aws.String("START RequestId: req-err Version: $LATEST"), Timestamp: aws.Int64(ts)},
		{Message: aws.String("something normal"), Timestamp: aws.Int64(ts + 50)},
		{Message: aws.String("FATAL: database connection lost"), Timestamp: aws.Int64(ts + 100)},
		{Message: aws.String("END RequestId: req-err"), Timestamp: aws.Int64(ts + 200)},
		{Message: aws.String("REPORT RequestId: req-err\tDuration: 50 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 64 MB"), Timestamp: aws.Int64(ts + 300)},
	}

	got := parseInvocations(events)
	if len(got) != 1 {
		t.Fatalf("expected 1 invocation, got %d", len(got))
	}
	if !got[0].IsError {
		t.Error("expected error to be detected")
	}
	if len(got[0].ErrorLines) != 1 || got[0].ErrorLines[0] != "FATAL: database connection lost" {
		t.Errorf("ErrorLines = %v, want [FATAL: database connection lost]", got[0].ErrorLines)
	}
}

func TestParseInvocations_SkipsWithoutReport(t *testing.T) {
	ts := time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC).UnixMilli()

	events := []types.OutputLogEvent{
		{Message: aws.String("START RequestId: incomplete Version: $LATEST"), Timestamp: aws.Int64(ts)},
		{Message: aws.String("some log"), Timestamp: aws.Int64(ts + 100)},
		// no END or REPORT
	}

	got := parseInvocations(events)
	if len(got) != 0 {
		t.Errorf("expected 0 invocations (no REPORT), got %d", len(got))
	}
}

func TestParseInvocations_MultipleInvocations(t *testing.T) {
	ts := time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC).UnixMilli()

	events := []types.OutputLogEvent{
		{Message: aws.String("START RequestId: req-a Version: $LATEST"), Timestamp: aws.Int64(ts)},
		{Message: aws.String("log a"), Timestamp: aws.Int64(ts + 100)},
		{Message: aws.String("REPORT RequestId: req-a\tDuration: 10 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 50 MB"), Timestamp: aws.Int64(ts + 200)},
		{Message: aws.String("START RequestId: req-b Version: $LATEST"), Timestamp: aws.Int64(ts + 300)},
		{Message: aws.String("log b"), Timestamp: aws.Int64(ts + 400)},
		{Message: aws.String("REPORT RequestId: req-b\tDuration: 20 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 60 MB"), Timestamp: aws.Int64(ts + 500)},
	}

	got := parseInvocations(events)
	if len(got) != 2 {
		t.Fatalf("expected 2 invocations, got %d", len(got))
	}
	if got[0].RequestID != "req-a" || got[1].RequestID != "req-b" {
		t.Errorf("request IDs = [%s, %s], want [req-a, req-b]", got[0].RequestID, got[1].RequestID)
	}
}

func TestParseInvocations_NilTimestampSkipped(t *testing.T) {
	ts := time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC).UnixMilli()

	events := []types.OutputLogEvent{
		{Message: aws.String("START RequestId: req-ts Version: $LATEST"), Timestamp: nil},
		{Message: aws.String("START RequestId: req-ts Version: $LATEST"), Timestamp: aws.Int64(ts)},
		{Message: aws.String("REPORT RequestId: req-ts\tDuration: 5 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 40 MB"), Timestamp: aws.Int64(ts + 100)},
	}

	got := parseInvocations(events)
	if len(got) != 1 {
		t.Fatalf("expected 1 invocation, got %d", len(got))
	}
}

func TestParseInvocations_NilMessageSkipped(t *testing.T) {
	ts := time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC).UnixMilli()

	events := []types.OutputLogEvent{
		{Message: nil, Timestamp: aws.Int64(ts)},
		{Message: aws.String("START RequestId: req-nil Version: $LATEST"), Timestamp: aws.Int64(ts + 100)},
		{Message: aws.String("REPORT RequestId: req-nil\tDuration: 5 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 40 MB"), Timestamp: aws.Int64(ts + 200)},
	}

	got := parseInvocations(events)
	if len(got) != 1 {
		t.Fatalf("expected 1 invocation, got %d", len(got))
	}
}
