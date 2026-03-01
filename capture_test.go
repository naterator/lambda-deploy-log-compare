package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

type mockLogsClient struct {
	describeLogStreamsFn func(ctx context.Context, params *cloudwatchlogs.DescribeLogStreamsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogStreamsOutput, error)
	getLogEventsFn       func(ctx context.Context, params *cloudwatchlogs.GetLogEventsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error)
}

func (m *mockLogsClient) DescribeLogStreams(ctx context.Context, params *cloudwatchlogs.DescribeLogStreamsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogStreamsOutput, error) {
	return m.describeLogStreamsFn(ctx, params, optFns...)
}

func (m *mockLogsClient) GetLogEvents(ctx context.Context, params *cloudwatchlogs.GetLogEventsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
	return m.getLogEventsFn(ctx, params, optFns...)
}

func TestLogGroupForFunction(t *testing.T) {
	tests := []struct {
		name     string
		funcName string
		want     string
	}{
		{"simple", "pact_cash_actuator", "/aws/lambda/pact_cash_actuator"},
		{"with dash", "pact_dlq_alert-west", "/aws/lambda/pact_dlq_alert-west"},
		{"arbitrary", "my-custom-function", "/aws/lambda/my-custom-function"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := logGroupForFunction(tt.funcName)
			if got != tt.want {
				t.Errorf("logGroupForFunction(%q) = %q, want %q", tt.funcName, got, tt.want)
			}
		})
	}
}

func TestToRecord(t *testing.T) {
	ts := time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC)
	inv := InvocationSummary{
		RequestID:  "req-123",
		StartTime:  ts,
		Duration:   "100 ms",
		BilledMs:   "200 ms",
		MemUsedMB:  "128 MB",
		MaxMemMB:   "256 MB",
		IsError:    true,
		ErrorLines: []string{"panic: something"},
		LogLines:   []string{"starting", "panic: something"},
	}
	rec := toRecord(inv)
	if rec.RequestID != "req-123" {
		t.Errorf("RequestID = %q, want %q", rec.RequestID, "req-123")
	}
	if rec.Timestamp != "2026-02-25T10:00:00Z" {
		t.Errorf("Timestamp = %q, want %q", rec.Timestamp, "2026-02-25T10:00:00Z")
	}
	if rec.Duration != "100 ms" || rec.BilledMs != "200 ms" {
		t.Error("duration/billed fields mismatch")
	}
	if !rec.IsError || len(rec.ErrorLines) != 1 {
		t.Error("error fields mismatch")
	}
}

func TestFetchLogStreams(t *testing.T) {
	t.Run("single page", func(t *testing.T) {
		mock := &mockLogsClient{
			describeLogStreamsFn: func(ctx context.Context, params *cloudwatchlogs.DescribeLogStreamsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogStreamsOutput, error) {
				return &cloudwatchlogs.DescribeLogStreamsOutput{
					LogStreams: []types.LogStream{
						{LogStreamName: aws.String("stream-1")},
						{LogStreamName: aws.String("stream-2")},
					},
				}, nil
			},
		}

		streams, err := fetchLogStreams(context.Background(), mock, "/aws/lambda/test", 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(streams) != 2 {
			t.Fatalf("expected 2 streams, got %d", len(streams))
		}
		if *streams[0].LogStreamName != "stream-1" {
			t.Errorf("first stream = %q, want %q", *streams[0].LogStreamName, "stream-1")
		}
	})

	t.Run("pagination", func(t *testing.T) {
		callCount := 0
		mock := &mockLogsClient{
			describeLogStreamsFn: func(ctx context.Context, params *cloudwatchlogs.DescribeLogStreamsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogStreamsOutput, error) {
				callCount++
				if callCount == 1 {
					return &cloudwatchlogs.DescribeLogStreamsOutput{
						LogStreams: []types.LogStream{
							{LogStreamName: aws.String("stream-1")},
						},
						NextToken: aws.String("token-1"),
					}, nil
				}
				return &cloudwatchlogs.DescribeLogStreamsOutput{
					LogStreams: []types.LogStream{
						{LogStreamName: aws.String("stream-2")},
					},
				}, nil
			},
		}

		streams, err := fetchLogStreams(context.Background(), mock, "/aws/lambda/test", 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(streams) != 2 {
			t.Fatalf("expected 2 streams, got %d", len(streams))
		}
		if callCount != 2 {
			t.Errorf("expected 2 API calls, got %d", callCount)
		}
	})

	t.Run("error", func(t *testing.T) {
		mock := &mockLogsClient{
			describeLogStreamsFn: func(ctx context.Context, params *cloudwatchlogs.DescribeLogStreamsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogStreamsOutput, error) {
				return nil, errors.New("api error")
			},
		}

		_, err := fetchLogStreams(context.Background(), mock, "/aws/lambda/test", 10)
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestFetchLogEvents(t *testing.T) {
	t.Run("single page", func(t *testing.T) {
		callCount := 0
		mock := &mockLogsClient{
			getLogEventsFn: func(ctx context.Context, params *cloudwatchlogs.GetLogEventsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
				callCount++
				if callCount == 1 {
					return &cloudwatchlogs.GetLogEventsOutput{
						Events: []types.OutputLogEvent{
							{Message: aws.String("hello"), Timestamp: aws.Int64(1000)},
						},
						NextForwardToken: aws.String("token-1"),
					}, nil
				}
				// Second call returns same token — signals end of data
				return &cloudwatchlogs.GetLogEventsOutput{
					NextForwardToken: aws.String("token-1"),
				}, nil
			},
		}

		events, err := fetchLogEvents(context.Background(), mock, "/aws/lambda/test", "stream-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(events))
		}
	})

	t.Run("pagination stops on same token", func(t *testing.T) {
		callCount := 0
		mock := &mockLogsClient{
			getLogEventsFn: func(ctx context.Context, params *cloudwatchlogs.GetLogEventsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
				callCount++
				if callCount == 1 {
					return &cloudwatchlogs.GetLogEventsOutput{
						Events: []types.OutputLogEvent{
							{Message: aws.String("event-1"), Timestamp: aws.Int64(1000)},
						},
						NextForwardToken: aws.String("token-2"),
					}, nil
				}
				return &cloudwatchlogs.GetLogEventsOutput{
					Events: []types.OutputLogEvent{
						{Message: aws.String("event-2"), Timestamp: aws.Int64(2000)},
					},
					NextForwardToken: aws.String("token-2"), // same as sent
				}, nil
			},
		}

		events, err := fetchLogEvents(context.Background(), mock, "/aws/lambda/test", "stream-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(events) != 2 {
			t.Fatalf("expected 2 events, got %d", len(events))
		}
		if callCount != 2 {
			t.Errorf("expected 2 API calls, got %d", callCount)
		}
	})

	t.Run("error", func(t *testing.T) {
		mock := &mockLogsClient{
			getLogEventsFn: func(ctx context.Context, params *cloudwatchlogs.GetLogEventsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
				return nil, errors.New("api error")
			},
		}

		_, err := fetchLogEvents(context.Background(), mock, "/aws/lambda/test", "stream-1")
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestRunCapture(t *testing.T) {
	ts := time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC).UnixMilli()

	mock := &mockLogsClient{
		describeLogStreamsFn: func(ctx context.Context, params *cloudwatchlogs.DescribeLogStreamsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogStreamsOutput, error) {
			return &cloudwatchlogs.DescribeLogStreamsOutput{
				LogStreams: []types.LogStream{
					{LogStreamName: aws.String("stream-1")},
				},
			}, nil
		},
		getLogEventsFn: func(ctx context.Context, params *cloudwatchlogs.GetLogEventsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
			return &cloudwatchlogs.GetLogEventsOutput{
				Events: []types.OutputLogEvent{
					{Message: aws.String("START RequestId: req-1 Version: $LATEST"), Timestamp: aws.Int64(ts)},
					{Message: aws.String("processing"), Timestamp: aws.Int64(ts + 100)},
					{Message: aws.String("END RequestId: req-1"), Timestamp: aws.Int64(ts + 200)},
					{Message: aws.String("REPORT RequestId: req-1\tDuration: 50 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 64 MB"), Timestamp: aws.Int64(ts + 300)},
				},
				NextForwardToken: aws.String("done"),
			}, nil
		},
	}

	outDir := t.TempDir()
	err := runCapture(mock, "test-func", "/aws/lambda/test-func", 5, 0, "test-label", outDir)
	if err != nil {
		t.Fatalf("runCapture error: %v", err)
	}

	outPath := filepath.Join(outDir, "test-func_test-label.json")
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("failed to unmarshal snapshot: %v", err)
	}

	if snap.FunctionName != "test-func" {
		t.Errorf("FunctionName = %q, want %q", snap.FunctionName, "test-func")
	}
	if snap.Label != "test-label" {
		t.Errorf("Label = %q, want %q", snap.Label, "test-label")
	}
	if len(snap.Invocations) != 1 {
		t.Fatalf("expected 1 invocation, got %d", len(snap.Invocations))
	}
	if snap.Invocations[0].RequestID != "req-1" {
		t.Errorf("RequestID = %q, want %q", snap.Invocations[0].RequestID, "req-1")
	}
	if snap.Invocations[0].Duration != "50 ms" {
		t.Errorf("Duration = %q, want %q", snap.Invocations[0].Duration, "50 ms")
	}
}

func TestRunCapture_NoStreams(t *testing.T) {
	mock := &mockLogsClient{
		describeLogStreamsFn: func(ctx context.Context, params *cloudwatchlogs.DescribeLogStreamsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogStreamsOutput, error) {
			return &cloudwatchlogs.DescribeLogStreamsOutput{
				LogStreams: []types.LogStream{},
			}, nil
		},
	}

	outDir := t.TempDir()
	err := runCapture(mock, "empty-func", "/aws/lambda/empty-func", 5, 0, "test", outDir)
	if err != nil {
		t.Fatalf("runCapture error: %v", err)
	}

	// No file should be written when there are no streams
	outPath := filepath.Join(outDir, "empty-func_test.json")
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Error("expected no output file when no streams found")
	}
}
