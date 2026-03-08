package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

func TestRun_ShowsUsageOnMissingCommand(t *testing.T) {
	code, _, stderrText := runCLI(t, []string{})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderrText, "Usage:") {
		t.Fatalf("stderr missing usage: %s", stderrText)
	}
	if !strings.Contains(stderrText, "capture") || !strings.Contains(stderrText, "compare") {
		t.Fatalf("stderr missing command list: %s", stderrText)
	}
}

func TestRun_CaptureAndCompareEndToEnd(t *testing.T) {
	base := time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC).UnixMilli()
	oldFactory := logsClientFactory
	defer func() { logsClientFactory = oldFactory }()

	factoryCalls := 0
	logsClientFactory = func(region, profile string) (LogsClient, error) {
		factoryCalls++
		switch factoryCalls {
		case 1:
			return newCLIClient(base, "req-baseline", []string{"baseline log"}, false), nil
		case 2:
			return newCLIClient(base+10_000, "req-new", []string{"FATAL: database connection lost"}, true), nil
		default:
			t.Fatalf("unexpected logsClientFactory call %d", factoryCalls)
			return nil, nil
		}
	}

	outDir := t.TempDir()
	code, captureStdout, captureStderr := runCLI(t, []string{"capture", "--function", "test-func", "--label", "baseline", "--count", "1", "--out", outDir})
	if code != 0 {
		t.Fatalf("baseline capture exit code = %d, stderr = %s", code, captureStderr)
	}
	if captureStderr != "" {
		t.Fatalf("baseline capture stderr = %q, want empty", captureStderr)
	}
	if !strings.Contains(captureStdout, "Wrote snapshot") {
		t.Fatalf("baseline capture stdout missing success message: %s", captureStdout)
	}

	code, _, captureStderr = runCLI(t, []string{"capture", "--function", "test-func", "--label", "new", "--count", "1", "--out", outDir})
	if code != 0 {
		t.Fatalf("new capture exit code = %d, stderr = %s", code, captureStderr)
	}
	if captureStderr != "" {
		t.Fatalf("new capture stderr = %q, want empty", captureStderr)
	}

	baselinePath := filepath.Join(outDir, "test-func_baseline.json")
	newPath := filepath.Join(outDir, "test-func_new.json")
	data, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatalf("failed to read baseline snapshot: %v", err)
	}
	if !strings.Contains(string(data), "\"memory_size_mb\"") || !strings.Contains(string(data), "\"max_memory_used_mb\"") {
		t.Fatalf("snapshot did not use renamed memory fields: %s", string(data))
	}
	if strings.Contains(string(data), "\"mem_used_mb\"") || strings.Contains(string(data), "\"max_mem_mb\"") {
		t.Fatalf("snapshot still contains legacy memory fields: %s", string(data))
	}

	code, compareStdout, compareStderr := runCLI(t, []string{"compare", "--a", baselinePath, "--b", newPath})
	if code != 0 {
		t.Fatalf("compare exit code = %d, stderr = %s", code, compareStderr)
	}
	if compareStderr != "" {
		t.Fatalf("compare stderr = %q, want empty", compareStderr)
	}
	if !strings.Contains(compareStdout, "=== Comparison: test-func ===") {
		t.Fatalf("compare stdout missing header: %s", compareStdout)
	}
	if !strings.Contains(compareStdout, "*** WARNING: Error count increased! ***") {
		t.Fatalf("compare stdout missing error warning: %s", compareStdout)
	}
}

func TestRun_CompareStrictFlag(t *testing.T) {
	dir := t.TempDir()
	writeSnapshotFile(t, filepath.Join(dir, "a.json"), Snapshot{FunctionName: "func-a", LogGroup: "/aws/lambda/func-a"})
	writeSnapshotFile(t, filepath.Join(dir, "b.json"), Snapshot{FunctionName: "func-b", LogGroup: "/aws/lambda/func-b"})

	code, stdoutText, stderrText := runCLI(t, []string{"compare", "--strict", "--a", filepath.Join(dir, "a.json"), "--b", filepath.Join(dir, "b.json")})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdoutText != "" {
		t.Fatalf("stdout = %q, want empty", stdoutText)
	}
	if !strings.Contains(stderrText, "strict comparison failed") {
		t.Fatalf("stderr missing strict failure: %s", stderrText)
	}
}

func runCLI(t *testing.T, args []string) (int, string, string) {
	t.Helper()

	oldStdout := stdout
	oldStderr := stderr
	var outBuf, errBuf bytes.Buffer
	stdout = &outBuf
	stderr = &errBuf
	defer func() {
		stdout = oldStdout
		stderr = oldStderr
	}()

	return run(args), outBuf.String(), errBuf.String()
}

func newCLIClient(ts int64, requestID string, logLines []string, isError bool) LogsClient {
	return &mockLogsClient{
		describeLogStreamsFn: func(ctx context.Context, params *cloudwatchlogs.DescribeLogStreamsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogStreamsOutput, error) {
			return &cloudwatchlogs.DescribeLogStreamsOutput{
				LogStreams: []types.LogStream{{LogStreamName: aws.String("stream-1")}},
			}, nil
		},
		getLogEventsFn: func(ctx context.Context, params *cloudwatchlogs.GetLogEventsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
			events := []types.OutputLogEvent{
				{Message: aws.String("START RequestId: " + requestID + " Version: $LATEST"), Timestamp: aws.Int64(ts)},
			}
			for i, line := range logLines {
				events = append(events, types.OutputLogEvent{
					Message:   aws.String(line),
					Timestamp: aws.Int64(ts + int64((i+1)*100)),
				})
			}
			events = append(events, types.OutputLogEvent{
				Message:   aws.String("REPORT RequestId: " + requestID + "\tDuration: 50 ms\tBilled Duration: 100 ms\tMemory Size: 128 MB\tMax Memory Used: 64 MB"),
				Timestamp: aws.Int64(ts + 1000),
			})
			if isError {
				events = append(events, types.OutputLogEvent{
					Message:   aws.String("RequestId: " + requestID + " process exited before completing request"),
					Timestamp: aws.Int64(ts + 1100),
				})
			}
			return &cloudwatchlogs.GetLogEventsOutput{
				Events:           events,
				NextForwardToken: aws.String("done"),
			}, nil
		},
	}
}

func writeSnapshotFile(t *testing.T, path string, snap Snapshot) {
	t.Helper()

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent error: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("os.WriteFile error: %v", err)
	}
}
