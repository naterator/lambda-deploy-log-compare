package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
		RequestID:       "req-123",
		StartTime:       ts,
		Duration:        "100 ms",
		BilledMs:        "200 ms",
		MemorySizeMB:    "128 MB",
		MaxMemoryUsedMB: "256 MB",
		IsError:         true,
		ErrorLines:      []string{"panic: something"},
		LogLines:        []string{"starting", "panic: something"},
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
	if rec.MemorySizeMB != "128 MB" || rec.MaxMemoryUsedMB != "256 MB" {
		t.Error("memory fields mismatch")
	}
	if !rec.IsError || len(rec.ErrorLines) != 1 {
		t.Error("error fields mismatch")
	}
}

func TestFetchLogStreamPage(t *testing.T) {
	t.Run("returns page and next token", func(t *testing.T) {
		mock := &mockLogsClient{
			describeLogStreamsFn: func(ctx context.Context, params *cloudwatchlogs.DescribeLogStreamsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogStreamsOutput, error) {
				if got := aws.ToString(params.LogGroupName); got != "/aws/lambda/test" {
					t.Fatalf("LogGroupName = %q, want %q", got, "/aws/lambda/test")
				}
				if got := aws.ToInt32(params.Limit); got != 25 {
					t.Fatalf("Limit = %d, want 25", got)
				}
				if params.NextToken == nil || *params.NextToken != "page-1" {
					t.Fatalf("NextToken = %v, want page-1", aws.ToString(params.NextToken))
				}
				if params.OrderBy != types.OrderByLastEventTime {
					t.Fatalf("OrderBy = %q, want %q", params.OrderBy, types.OrderByLastEventTime)
				}
				if !aws.ToBool(params.Descending) {
					t.Fatalf("Descending = %v, want true", aws.ToBool(params.Descending))
				}
				return &cloudwatchlogs.DescribeLogStreamsOutput{
					LogStreams: []types.LogStream{
						{LogStreamName: aws.String("stream-1")},
						{LogStreamName: aws.String("stream-2")},
					},
					NextToken: aws.String("page-2"),
				}, nil
			},
		}

		streams, nextToken, err := fetchLogStreamPage(context.Background(), mock, "/aws/lambda/test", 25, aws.String("page-1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(streams) != 2 {
			t.Fatalf("expected 2 streams, got %d", len(streams))
		}
		if *streams[0].LogStreamName != "stream-1" {
			t.Fatalf("first stream = %q, want %q", *streams[0].LogStreamName, "stream-1")
		}
		if nextToken == nil || *nextToken != "page-2" {
			t.Fatalf("nextToken = %v, want page-2", aws.ToString(nextToken))
		}
	})

	t.Run("error", func(t *testing.T) {
		mock := &mockLogsClient{
			describeLogStreamsFn: func(ctx context.Context, params *cloudwatchlogs.DescribeLogStreamsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogStreamsOutput, error) {
				return nil, errors.New("api error")
			},
		}

		_, _, err := fetchLogStreamPage(context.Background(), mock, "/aws/lambda/test", 10, nil)
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
				if params.NextToken != nil {
					t.Fatalf("unexpected follow-up page request with token %q", *params.NextToken)
				}
				if params.StartFromHead == nil || *params.StartFromHead {
					t.Fatalf("StartFromHead = %v, want false", aws.ToBool(params.StartFromHead))
				}
				if got := aws.ToInt32(params.Limit); got != logEventsPageLimit {
					t.Fatalf("Limit = %d, want %d", got, logEventsPageLimit)
				}
				return &cloudwatchlogs.GetLogEventsOutput{
					Events: latestFirstEvents([]types.OutputLogEvent{
						{Message: aws.String("START RequestId: req-1 Version: $LATEST"), Timestamp: aws.Int64(1000)},
						{Message: aws.String("REPORT RequestId: req-1\tDuration: 50 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 64 MB"), Timestamp: aws.Int64(1100)},
					}),
					NextBackwardToken: aws.String("token-1"),
				}, nil
			},
		}

		events, err := fetchLogEvents(context.Background(), mock, "/aws/lambda/test", "stream-1", 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(events) != 2 {
			t.Fatalf("expected 2 events, got %d", len(events))
		}
		if got := aws.ToString(events[0].Message); got != "START RequestId: req-1 Version: $LATEST" {
			t.Fatalf("first event = %q, want START line", got)
		}
		if callCount != 1 {
			t.Fatalf("expected 1 API call, got %d", callCount)
		}
	})

	t.Run("paginates backward until enough invocations are available", func(t *testing.T) {
		callCount := 0
		mock := &mockLogsClient{
			getLogEventsFn: func(ctx context.Context, params *cloudwatchlogs.GetLogEventsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
				callCount++
				if callCount == 1 {
					return &cloudwatchlogs.GetLogEventsOutput{
						Events: latestFirstEvents([]types.OutputLogEvent{
							{Message: aws.String("START RequestId: req-b Version: $LATEST"), Timestamp: aws.Int64(3000)},
							{Message: aws.String("REPORT RequestId: req-b\tDuration: 20 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 64 MB"), Timestamp: aws.Int64(3100)},
						}),
						NextBackwardToken: aws.String("token-older"),
					}, nil
				}
				if params.NextToken == nil || *params.NextToken != "token-older" {
					t.Fatalf("NextToken = %v, want token-older", aws.ToString(params.NextToken))
				}
				return &cloudwatchlogs.GetLogEventsOutput{
					Events: latestFirstEvents([]types.OutputLogEvent{
						{Message: aws.String("START RequestId: req-a Version: $LATEST"), Timestamp: aws.Int64(1000)},
						{Message: aws.String("REPORT RequestId: req-a\tDuration: 10 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 64 MB"), Timestamp: aws.Int64(1100)},
					}),
					NextBackwardToken: aws.String("token-oldest"),
				}, nil
			},
		}

		events, err := fetchLogEvents(context.Background(), mock, "/aws/lambda/test", "stream-1", 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(events) != 4 {
			t.Fatalf("expected 4 events, got %d", len(events))
		}
		if callCount != 2 {
			t.Errorf("expected 2 API calls, got %d", callCount)
		}
	})

	t.Run("does not count split invocation pages twice", func(t *testing.T) {
		callCount := 0
		mock := &mockLogsClient{
			getLogEventsFn: func(ctx context.Context, params *cloudwatchlogs.GetLogEventsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
				callCount++
				switch callCount {
				case 1:
					return &cloudwatchlogs.GetLogEventsOutput{
						Events: latestFirstEvents([]types.OutputLogEvent{
							{Message: aws.String("REPORT RequestId: req-split\tDuration: 20 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 64 MB"), Timestamp: aws.Int64(3100)},
						}),
						NextBackwardToken: aws.String("token-middle"),
					}, nil
				case 2:
					if params.NextToken == nil || *params.NextToken != "token-middle" {
						t.Fatalf("NextToken = %v, want token-middle", aws.ToString(params.NextToken))
					}
					return &cloudwatchlogs.GetLogEventsOutput{
						Events: latestFirstEvents([]types.OutputLogEvent{
							{Message: aws.String("START RequestId: req-split Version: $LATEST"), Timestamp: aws.Int64(3000)},
						}),
						NextBackwardToken: aws.String("token-older"),
					}, nil
				default:
					if params.NextToken == nil || *params.NextToken != "token-older" {
						t.Fatalf("NextToken = %v, want token-older", aws.ToString(params.NextToken))
					}
					return &cloudwatchlogs.GetLogEventsOutput{
						Events: latestFirstEvents([]types.OutputLogEvent{
							{Message: aws.String("START RequestId: req-older Version: $LATEST"), Timestamp: aws.Int64(1000)},
							{Message: aws.String("REPORT RequestId: req-older\tDuration: 10 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 64 MB"), Timestamp: aws.Int64(1100)},
						}),
						NextBackwardToken: aws.String("token-oldest"),
					}, nil
				}
			},
		}

		events, err := fetchLogEvents(context.Background(), mock, "/aws/lambda/test", "stream-1", 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if callCount != 3 {
			t.Fatalf("expected 3 API calls, got %d", callCount)
		}

		invocations := parseInvocations(events)
		if len(invocations) != 2 {
			t.Fatalf("expected 2 unique invocations, got %d", len(invocations))
		}
	})

	t.Run("continues until newest split invocation has start boundary", func(t *testing.T) {
		callCount := 0
		mock := &mockLogsClient{
			getLogEventsFn: func(ctx context.Context, params *cloudwatchlogs.GetLogEventsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
				callCount++
				switch callCount {
				case 1:
					return &cloudwatchlogs.GetLogEventsOutput{
						Events: latestFirstEvents([]types.OutputLogEvent{
							{Message: aws.String("REPORT RequestId: req-split\tDuration: 20 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 64 MB"), Timestamp: aws.Int64(3100)},
						}),
						NextBackwardToken: aws.String("token-start"),
					}, nil
				case 2:
					if params.NextToken == nil || *params.NextToken != "token-start" {
						t.Fatalf("NextToken = %v, want token-start", aws.ToString(params.NextToken))
					}
					return &cloudwatchlogs.GetLogEventsOutput{
						Events: latestFirstEvents([]types.OutputLogEvent{
							{Message: aws.String("START RequestId: req-split Version: $LATEST"), Timestamp: aws.Int64(3000)},
							{Message: aws.String("panic: lost work before report"), Timestamp: aws.Int64(3050)},
						}),
						NextBackwardToken: aws.String("token-older"),
					}, nil
				default:
					t.Fatalf("unexpected GetLogEvents call %d", callCount)
					return nil, nil
				}
			},
		}

		events, err := fetchLogEvents(context.Background(), mock, "/aws/lambda/test", "stream-1", 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if callCount != 2 {
			t.Fatalf("expected 2 API calls, got %d", callCount)
		}

		invocations := parseInvocations(events)
		if len(invocations) != 1 {
			t.Fatalf("expected 1 invocation, got %d", len(invocations))
		}
		if invocations[0].StartTime.UnixMilli() != 3000 {
			t.Fatalf("StartTime = %d, want 3000", invocations[0].StartTime.UnixMilli())
		}
		if !invocations[0].IsError || len(invocations[0].ErrorLines) != 1 {
			t.Fatalf("expected split-page error line to be preserved: %+v", invocations[0])
		}
	})

	t.Run("stops when the backward token stops moving", func(t *testing.T) {
		callCount := 0
		mock := &mockLogsClient{
			getLogEventsFn: func(ctx context.Context, params *cloudwatchlogs.GetLogEventsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
				callCount++
				if callCount == 1 {
					return &cloudwatchlogs.GetLogEventsOutput{
						Events: latestFirstEvents([]types.OutputLogEvent{
							{Message: aws.String("START RequestId: req-1 Version: $LATEST"), Timestamp: aws.Int64(1000)},
							{Message: aws.String("REPORT RequestId: req-1\tDuration: 50 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 64 MB"), Timestamp: aws.Int64(1100)},
						}),
						NextBackwardToken: aws.String("token-final"),
					}, nil
				}
				return &cloudwatchlogs.GetLogEventsOutput{
					Events: latestFirstEvents([]types.OutputLogEvent{
						{Message: aws.String("START RequestId: req-1 Version: $LATEST"), Timestamp: aws.Int64(1000)},
						{Message: aws.String("REPORT RequestId: req-1\tDuration: 50 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 64 MB"), Timestamp: aws.Int64(1100)},
					}),
					NextBackwardToken: aws.String("token-final"),
				}, nil
			},
		}

		events, err := fetchLogEvents(context.Background(), mock, "/aws/lambda/test", "stream-1", 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(events) != 2 {
			t.Fatalf("expected 2 events without terminal duplication, got %d", len(events))
		}
		if callCount != 2 {
			t.Fatalf("expected 2 API calls, got %d", callCount)
		}
	})

	t.Run("error", func(t *testing.T) {
		mock := &mockLogsClient{
			getLogEventsFn: func(ctx context.Context, params *cloudwatchlogs.GetLogEventsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
				return nil, errors.New("api error")
			},
		}

		_, err := fetchLogEvents(context.Background(), mock, "/aws/lambda/test", "stream-1", 1)
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("returns partial events with later page error", func(t *testing.T) {
		callCount := 0
		mock := &mockLogsClient{
			getLogEventsFn: func(ctx context.Context, params *cloudwatchlogs.GetLogEventsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
				callCount++
				if params.NextToken != nil {
					return nil, errors.New("api error")
				}
				return &cloudwatchlogs.GetLogEventsOutput{
					Events: latestFirstEvents([]types.OutputLogEvent{
						{Message: aws.String("START RequestId: req-1 Version: $LATEST"), Timestamp: aws.Int64(1000)},
						{Message: aws.String("REPORT RequestId: req-1\tDuration: 50 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 64 MB"), Timestamp: aws.Int64(1100)},
					}),
					NextBackwardToken: aws.String("token-older"),
				}, nil
			},
		}

		events, err := fetchLogEvents(context.Background(), mock, "/aws/lambda/test", "stream-1", 2)
		if err == nil {
			t.Fatal("expected error")
		}
		if callCount != 2 {
			t.Fatalf("GetLogEvents calls = %d, want 2", callCount)
		}
		if len(events) != 2 {
			t.Fatalf("partial events = %d, want 2", len(events))
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
			return singleTailPage([]types.OutputLogEvent{
				{Message: aws.String("START RequestId: req-1 Version: $LATEST"), Timestamp: aws.Int64(ts)},
				{Message: aws.String("processing"), Timestamp: aws.Int64(ts + 100)},
				{Message: aws.String("END RequestId: req-1"), Timestamp: aws.Int64(ts + 200)},
				{Message: aws.String("REPORT RequestId: req-1\tDuration: 50 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 64 MB"), Timestamp: aws.Int64(ts + 300)},
			})(ctx, params, optFns...)
		},
	}

	outDir := t.TempDir()
	err := runCapture(mock, "test-func", "/aws/lambda/test-func", 1, 0, "test-label", outDir)
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

func TestRunCapture_DoesNotOverwriteExistingSnapshot(t *testing.T) {
	ts := time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC).UnixMilli()
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "test-func_baseline.json")

	if err := runCapture(newSingleInvocationCaptureClient(ts, "req-original"), "test-func", "/aws/lambda/test-func", 1, 0, "baseline", outDir); err != nil {
		t.Fatalf("initial runCapture error: %v", err)
	}
	originalData, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("failed to read original snapshot: %v", err)
	}

	err = runCapture(newSingleInvocationCaptureClient(ts+10_000, "req-new"), "test-func", "/aws/lambda/test-func", 1, 0, "baseline", outDir)
	if err == nil {
		t.Fatal("runCapture returned nil, want existing-file error")
	}
	if !strings.Contains(err.Error(), "snapshot already exists") || !strings.Contains(err.Error(), "--overwrite") {
		t.Fatalf("runCapture error = %q, want overwrite guidance", err.Error())
	}

	currentData, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("failed to read current snapshot: %v", err)
	}
	if string(currentData) != string(originalData) {
		t.Fatalf("snapshot was overwritten despite missing overwrite option")
	}
}

func TestRunCapture_OverwriteExistingSnapshotWhenRequested(t *testing.T) {
	ts := time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC).UnixMilli()
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "test-func_baseline.json")

	if err := runCapture(newSingleInvocationCaptureClient(ts, "req-original"), "test-func", "/aws/lambda/test-func", 1, 0, "baseline", outDir); err != nil {
		t.Fatalf("initial runCapture error: %v", err)
	}
	if err := runCaptureWithOptions(newSingleInvocationCaptureClient(ts+10_000, "req-new"), "test-func", "/aws/lambda/test-func", 1, 0, "baseline", outDir, CaptureOptions{Overwrite: true}); err != nil {
		t.Fatalf("overwrite runCapture error: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("failed to read overwritten snapshot: %v", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("failed to unmarshal snapshot: %v", err)
	}
	if len(snap.Invocations) != 1 || snap.Invocations[0].RequestID != "req-new" {
		t.Fatalf("captured invocations = %+v, want overwritten req-new snapshot", snap.Invocations)
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

func TestRunCapture_DescribeLogStreamsError(t *testing.T) {
	describeErr := errors.New("api unavailable")
	mock := &mockLogsClient{
		describeLogStreamsFn: func(ctx context.Context, params *cloudwatchlogs.DescribeLogStreamsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogStreamsOutput, error) {
			return nil, describeErr
		},
	}

	err := runCapture(mock, "test-func", "/aws/lambda/test-func", 1, 0, "test", t.TempDir())
	if err == nil {
		t.Fatal("expected runCapture error")
	}
	if !errors.Is(err, describeErr) {
		t.Fatalf("runCapture error does not wrap describe error: %v", err)
	}
	if !strings.Contains(err.Error(), "describe-log-streams: api unavailable") {
		t.Fatalf("runCapture error = %q, want describe-log-streams context", err.Error())
	}
}

func TestRunCapture_GetLogEventsErrorWarnsAndContinues(t *testing.T) {
	ts := time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC).UnixMilli()
	mock := &mockLogsClient{
		describeLogStreamsFn: func(ctx context.Context, params *cloudwatchlogs.DescribeLogStreamsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogStreamsOutput, error) {
			return &cloudwatchlogs.DescribeLogStreamsOutput{
				LogStreams: []types.LogStream{
					{LogStreamName: aws.String("stream-bad")},
					{LogStreamName: aws.String("stream-good")},
				},
			}, nil
		},
		getLogEventsFn: func(ctx context.Context, params *cloudwatchlogs.GetLogEventsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
			if aws.ToString(params.LogStreamName) == "stream-bad" {
				return nil, errors.New("stream unavailable")
			}
			return singleTailPage([]types.OutputLogEvent{
				{Message: aws.String("START RequestId: req-good Version: $LATEST"), Timestamp: aws.Int64(ts)},
				{Message: aws.String("REPORT RequestId: req-good\tDuration: 50 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 64 MB"), Timestamp: aws.Int64(ts + 100)},
			})(ctx, params, optFns...)
		},
	}

	outDir := t.TempDir()
	var err error
	stderrText := captureStderr(t, func() {
		err = runCapture(mock, "test-func", "/aws/lambda/test-func", 1, 0, "test", outDir)
	})
	if err != nil {
		t.Fatalf("runCapture error: %v", err)
	}
	if !strings.Contains(stderrText, "Warning: failed to get events from stream stream-bad: stream unavailable") {
		t.Fatalf("stderr missing per-stream warning: %s", stderrText)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "test-func_test.json"))
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("failed to unmarshal snapshot: %v", err)
	}
	if len(snap.Invocations) != 1 || snap.Invocations[0].RequestID != "req-good" {
		t.Fatalf("captured invocations = %+v, want req-good", snap.Invocations)
	}
}

func TestRunCapture_UsesPartialEventsWhenGetLogEventsFailsAfterPages(t *testing.T) {
	ts := time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC).UnixMilli()
	callCount := 0
	mock := &mockLogsClient{
		describeLogStreamsFn: func(ctx context.Context, params *cloudwatchlogs.DescribeLogStreamsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogStreamsOutput, error) {
			return &cloudwatchlogs.DescribeLogStreamsOutput{
				LogStreams: []types.LogStream{{LogStreamName: aws.String("stream-partial")}},
			}, nil
		},
		getLogEventsFn: func(ctx context.Context, params *cloudwatchlogs.GetLogEventsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
			callCount++
			if params.NextToken != nil {
				return nil, errors.New("next page unavailable")
			}
			return &cloudwatchlogs.GetLogEventsOutput{
				Events: latestFirstEvents([]types.OutputLogEvent{
					{Message: aws.String("START RequestId: req-partial Version: $LATEST"), Timestamp: aws.Int64(ts)},
					{Message: aws.String("REPORT RequestId: req-partial\tDuration: 50 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 64 MB"), Timestamp: aws.Int64(ts + 100)},
				}),
				NextBackwardToken: aws.String("token-older"),
			}, nil
		},
	}

	outDir := t.TempDir()
	var err error
	stderrText := captureStderr(t, func() {
		err = runCapture(mock, "test-func", "/aws/lambda/test-func", 2, 0, "partial", outDir)
	})
	if err != nil {
		t.Fatalf("runCapture error: %v", err)
	}
	if callCount != 2 {
		t.Fatalf("GetLogEvents calls = %d, want 2", callCount)
	}
	if !strings.Contains(stderrText, "Warning: failed to get complete events from stream stream-partial: next page unavailable; using 2 event(s) fetched before the failure") {
		t.Fatalf("stderr missing partial-use warning: %s", stderrText)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "test-func_partial.json"))
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("failed to unmarshal snapshot: %v", err)
	}
	if len(snap.Invocations) != 1 || snap.Invocations[0].RequestID != "req-partial" {
		t.Fatalf("captured invocations = %+v, want partial invocation", snap.Invocations)
	}
}

func TestRunCapture_CreatesOutputDirectory(t *testing.T) {
	ts := time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC).UnixMilli()

	mock := &mockLogsClient{
		describeLogStreamsFn: func(ctx context.Context, params *cloudwatchlogs.DescribeLogStreamsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogStreamsOutput, error) {
			return &cloudwatchlogs.DescribeLogStreamsOutput{
				LogStreams: []types.LogStream{{LogStreamName: aws.String("stream-1")}},
			}, nil
		},
		getLogEventsFn: func(ctx context.Context, params *cloudwatchlogs.GetLogEventsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
			return singleTailPage([]types.OutputLogEvent{
				{Message: aws.String("START RequestId: req-1 Version: $LATEST"), Timestamp: aws.Int64(ts)},
				{Message: aws.String("REPORT RequestId: req-1\tDuration: 50 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 64 MB"), Timestamp: aws.Int64(ts + 100)},
			})(ctx, params, optFns...)
		},
	}

	outDir := filepath.Join(t.TempDir(), "nested", "snapshots")
	if err := runCapture(mock, "test-func", "/aws/lambda/test-func", 1, 0, "test-label", outDir); err != nil {
		t.Fatalf("runCapture error: %v", err)
	}

	outPath := filepath.Join(outDir, "test-func_test-label.json")
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("expected output file to exist: %v", err)
	}
}

func TestRunCapture_SelectsMostRecentInvocationsAfterOffset(t *testing.T) {
	base := time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC).UnixMilli()
	streamEvents := map[string][]types.OutputLogEvent{
		"stream-1": {
			{Message: aws.String("START RequestId: req-1 Version: $LATEST"), Timestamp: aws.Int64(base + 1000)},
			{Message: aws.String("REPORT RequestId: req-1\tDuration: 10 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 40 MB"), Timestamp: aws.Int64(base + 1100)},
		},
		"stream-2": {
			{Message: aws.String("START RequestId: req-2 Version: $LATEST"), Timestamp: aws.Int64(base + 2000)},
			{Message: aws.String("REPORT RequestId: req-2\tDuration: 20 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 50 MB"), Timestamp: aws.Int64(base + 2100)},
		},
		"stream-3": {
			{Message: aws.String("START RequestId: req-3 Version: $LATEST"), Timestamp: aws.Int64(base + 3000)},
			{Message: aws.String("REPORT RequestId: req-3\tDuration: 30 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 60 MB"), Timestamp: aws.Int64(base + 3100)},
		},
	}

	mock := &mockLogsClient{
		describeLogStreamsFn: func(ctx context.Context, params *cloudwatchlogs.DescribeLogStreamsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogStreamsOutput, error) {
			return &cloudwatchlogs.DescribeLogStreamsOutput{
				LogStreams: []types.LogStream{
					{LogStreamName: aws.String("stream-1")},
					{LogStreamName: aws.String("stream-2")},
					{LogStreamName: aws.String("stream-3")},
				},
			}, nil
		},
		getLogEventsFn: func(ctx context.Context, params *cloudwatchlogs.GetLogEventsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
			if params.NextToken != nil {
				return &cloudwatchlogs.GetLogEventsOutput{NextBackwardToken: params.NextToken}, nil
			}
			return &cloudwatchlogs.GetLogEventsOutput{
				Events:            latestFirstEvents(streamEvents[*params.LogStreamName]),
				NextBackwardToken: aws.String("tail-" + *params.LogStreamName),
			}, nil
		},
	}

	outDir := t.TempDir()
	if err := runCapture(mock, "test-func", "/aws/lambda/test-func", 2, 1, "offset", outDir); err != nil {
		t.Fatalf("runCapture error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "test-func_offset.json"))
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("failed to unmarshal snapshot: %v", err)
	}
	if len(snap.Invocations) != 2 {
		t.Fatalf("expected 2 invocations, got %d", len(snap.Invocations))
	}

	got := []string{snap.Invocations[0].RequestID, snap.Invocations[1].RequestID}
	want := []string{"req-2", "req-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request IDs = %v, want %v", got, want)
	}
}

func TestSelectInvocations_DeterministicWhenTimestampsTie(t *testing.T) {
	ts := time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC)
	invocations := []InvocationSummary{
		{RequestID: "req-c", StartTime: ts},
		{RequestID: "req-a", StartTime: ts},
		{RequestID: "req-b", StartTime: ts},
	}

	selected := selectInvocations(invocations, 2, 1)
	got := []string{selected[0].RequestID, selected[1].RequestID}
	want := []string{"req-b", "req-c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selected request IDs = %v, want %v", got, want)
	}
}

func TestSelectInvocations_ReturnsEmptyWhenOffsetExhaustsInput(t *testing.T) {
	invocations := []InvocationSummary{
		{RequestID: "req-a", StartTime: time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC)},
	}

	selected := selectInvocations(invocations, 1, len(invocations))
	if len(selected) != 0 {
		t.Fatalf("selected invocations = %v, want none", selected)
	}
}

func TestRunCapture_SanitizesSnapshotFilename(t *testing.T) {
	ts := time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC).UnixMilli()

	mock := &mockLogsClient{
		describeLogStreamsFn: func(ctx context.Context, params *cloudwatchlogs.DescribeLogStreamsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogStreamsOutput, error) {
			return &cloudwatchlogs.DescribeLogStreamsOutput{
				LogStreams: []types.LogStream{{LogStreamName: aws.String("stream-1")}},
			}, nil
		},
		getLogEventsFn: func(ctx context.Context, params *cloudwatchlogs.GetLogEventsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
			return singleTailPage([]types.OutputLogEvent{
				{Message: aws.String("START RequestId: req-1 Version: $LATEST"), Timestamp: aws.Int64(ts)},
				{Message: aws.String("REPORT RequestId: req-1\tDuration: 50 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 64 MB"), Timestamp: aws.Int64(ts + 100)},
			})(ctx, params, optFns...)
		},
	}

	outDir := t.TempDir()
	if err := runCapture(mock, "../team/service", "/aws/lambda/test-func", 1, 0, "../../prod/release", outDir); err != nil {
		t.Fatalf("runCapture error: %v", err)
	}

	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("ReadDir error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 output entry, got %d", len(entries))
	}
	if entries[0].IsDir() {
		t.Fatalf("expected a file, got directory %q", entries[0].Name())
	}
	if got, want := entries[0].Name(), "team_service_prod_release.json"; got != want {
		t.Fatalf("output filename = %q, want %q", got, want)
	}
}

func TestRunCapture_InvalidArguments(t *testing.T) {
	tests := []struct {
		name   string
		count  int
		offset int
		want   string
	}{
		{
			name:  "non-positive count",
			count: 0,
			want:  "count must be greater than 0",
		},
		{
			name:   "negative offset",
			count:  1,
			offset: -1,
			want:   "offset must be greater than or equal to 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runCapture(&mockLogsClient{}, "test-func", "/aws/lambda/test-func", tt.count, tt.offset, "label", t.TempDir())
			if err == nil {
				t.Fatalf("runCapture returned nil, want %q", tt.want)
			}
			if err.Error() != tt.want {
				t.Fatalf("runCapture error = %q, want %q", err.Error(), tt.want)
			}
		})
	}
}

func TestRunCapture_PaginatesStreamsUntilItFindsOlderInvocations(t *testing.T) {
	base := time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC).UnixMilli()
	var describeCalls int

	mock := &mockLogsClient{
		describeLogStreamsFn: func(ctx context.Context, params *cloudwatchlogs.DescribeLogStreamsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogStreamsOutput, error) {
			describeCalls++
			makeStream := func(start, end int) []types.LogStream {
				streams := make([]types.LogStream, 0, end-start+1)
				for i := start; i <= end; i++ {
					streams = append(streams, types.LogStream{LogStreamName: aws.String(fmt.Sprintf("stream-%03d", i))})
				}
				return streams
			}

			if params.NextToken == nil {
				return &cloudwatchlogs.DescribeLogStreamsOutput{
					LogStreams: makeStream(1, 50),
					NextToken:  aws.String("page-2"),
				}, nil
			}
			return &cloudwatchlogs.DescribeLogStreamsOutput{
				LogStreams: makeStream(51, 60),
			}, nil
		},
		getLogEventsFn: func(ctx context.Context, params *cloudwatchlogs.GetLogEventsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
			name := *params.LogStreamName
			if params.NextToken != nil {
				return &cloudwatchlogs.GetLogEventsOutput{NextBackwardToken: params.NextToken}, nil
			}
			if name < "stream-056" {
				return &cloudwatchlogs.GetLogEventsOutput{NextBackwardToken: aws.String("tail-" + name)}, nil
			}

			streamIndex := 0
			fmt.Sscanf(name, "stream-%03d", &streamIndex)
			ts := base + int64((61-streamIndex)*1000)
			reqID := fmt.Sprintf("req-%03d", streamIndex)
			return &cloudwatchlogs.GetLogEventsOutput{
				Events: latestFirstEvents([]types.OutputLogEvent{
					{Message: aws.String("START RequestId: " + reqID + " Version: $LATEST"), Timestamp: aws.Int64(ts)},
					{Message: aws.String(fmt.Sprintf("REPORT RequestId: %s\tDuration: %d ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 64 MB", reqID, streamIndex)), Timestamp: aws.Int64(ts + 100)},
				}),
				NextBackwardToken: aws.String("tail-" + name),
			}, nil
		},
	}

	outDir := t.TempDir()
	if err := runCapture(mock, "test-func", "/aws/lambda/test-func", 3, 1, "older", outDir); err != nil {
		t.Fatalf("runCapture error: %v", err)
	}
	if describeCalls != 2 {
		t.Fatalf("DescribeLogStreams calls = %d, want 2", describeCalls)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "test-func_older.json"))
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("failed to unmarshal snapshot: %v", err)
	}
	got := []string{
		snap.Invocations[0].RequestID,
		snap.Invocations[1].RequestID,
		snap.Invocations[2].RequestID,
	}
	want := []string{"req-057", "req-058", "req-059"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request IDs = %v, want %v", got, want)
	}
}

func latestFirstEvents(events []types.OutputLogEvent) []types.OutputLogEvent {
	page := append([]types.OutputLogEvent(nil), events...)
	reverseLogEvents(page)
	return page
}

func newSingleInvocationCaptureClient(ts int64, requestID string) *mockLogsClient {
	return &mockLogsClient{
		describeLogStreamsFn: func(ctx context.Context, params *cloudwatchlogs.DescribeLogStreamsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogStreamsOutput, error) {
			return &cloudwatchlogs.DescribeLogStreamsOutput{
				LogStreams: []types.LogStream{{LogStreamName: aws.String("stream-1")}},
			}, nil
		},
		getLogEventsFn: singleTailPage([]types.OutputLogEvent{
			{Message: aws.String("START RequestId: " + requestID + " Version: $LATEST"), Timestamp: aws.Int64(ts)},
			{Message: aws.String("REPORT RequestId: " + requestID + "\tDuration: 50 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 64 MB"), Timestamp: aws.Int64(ts + 100)},
		}),
	}
}

func singleTailPage(events []types.OutputLogEvent) func(context.Context, *cloudwatchlogs.GetLogEventsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
	return func(ctx context.Context, params *cloudwatchlogs.GetLogEventsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
		if params.NextToken != nil {
			return &cloudwatchlogs.GetLogEventsOutput{NextBackwardToken: params.NextToken}, nil
		}
		return &cloudwatchlogs.GetLogEventsOutput{
			Events:            latestFirstEvents(events),
			NextBackwardToken: aws.String("tail-1"),
		}, nil
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	oldStderr := stderr
	var buf strings.Builder
	stderr = &buf
	defer func() { stderr = oldStderr }()

	fn()
	return buf.String()
}
