package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCountErrors(t *testing.T) {
	snap := Snapshot{
		Invocations: []InvocationRecord{
			{IsError: false},
			{IsError: true},
			{IsError: true},
			{IsError: false},
		},
	}
	got := countErrors(snap)
	if got != 2 {
		t.Errorf("countErrors = %d, want 2", got)
	}
}

func TestErrorPatterns(t *testing.T) {
	snap := Snapshot{
		Invocations: []InvocationRecord{
			{ErrorLines: []string{"connection error on host abc"}},
			{ErrorLines: []string{"connection error on host xyz", "timeout panic"}},
		},
	}
	got := errorPatterns(snap)
	if len(got) != 3 {
		t.Errorf("expected 3 patterns, got %d", len(got))
	}
}

func TestLogPatterns(t *testing.T) {
	snap := Snapshot{
		Invocations: []InvocationRecord{
			{LogLines: []string{"starting process", "done"}},
			{LogLines: []string{"starting process", "new line"}},
		},
	}
	got := logPatterns(snap)
	// "starting process", "done", "new line" = 3 unique
	if len(got) != 3 {
		t.Errorf("expected 3 unique patterns, got %d", len(got))
	}
}

func TestDiffPatterns(t *testing.T) {
	t.Run("new patterns detected", func(t *testing.T) {
		baseline := map[string]bool{"a": true, "b": true}
		other := map[string]bool{"b": true, "c": true, "d": true}
		got := diffPatterns(baseline, other)
		if len(got) != 2 || got[0] != "c" || got[1] != "d" {
			t.Errorf("expected [c d], got %v", got)
		}
	})

	t.Run("no diff when identical", func(t *testing.T) {
		both := map[string]bool{"a": true, "b": true}
		got := diffPatterns(both, both)
		if len(got) != 0 {
			t.Errorf("expected empty, got %v", got)
		}
	})

	t.Run("empty baseline", func(t *testing.T) {
		got := diffPatterns(map[string]bool{}, map[string]bool{"x": true})
		if len(got) != 1 || got[0] != "x" {
			t.Errorf("expected [x], got %v", got)
		}
	})
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly ten", 11, "exactly ten"},
		{"this is a long string", 10, "this is..."},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := truncate(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestLoadSnapshot(t *testing.T) {
	snap := Snapshot{
		FunctionName: "test-func",
		LogGroup:     "/aws/lambda/test-func",
		CapturedAt:   "2026-02-25T10:00:00Z",
		Label:        "test-label",
		Invocations: []InvocationRecord{
			{
				RequestID: "req-1",
				Timestamp: "2026-02-25T10:00:00Z",
				Duration:  "100 ms",
				IsError:   false,
				LogLines:  []string{"hello"},
			},
		},
	}

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	tmpFile := filepath.Join(t.TempDir(), "test-snapshot.json")
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadSnapshot(tmpFile)
	if err != nil {
		t.Fatalf("loadSnapshot error: %v", err)
	}
	if loaded.FunctionName != "test-func" {
		t.Errorf("FunctionName = %q, want %q", loaded.FunctionName, "test-func")
	}
	if loaded.Label != "test-label" {
		t.Errorf("Label = %q, want %q", loaded.Label, "test-label")
	}
	if len(loaded.Invocations) != 1 {
		t.Fatalf("expected 1 invocation, got %d", len(loaded.Invocations))
	}
	if loaded.Invocations[0].RequestID != "req-1" {
		t.Errorf("RequestID = %q, want %q", loaded.Invocations[0].RequestID, "req-1")
	}
}

func TestLoadSnapshot_FileNotFound(t *testing.T) {
	_, err := loadSnapshot("/nonexistent/path.json")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadSnapshot_MalformedJSON(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(tmpFile, []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := loadSnapshot(tmpFile)
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestRunCompare(t *testing.T) {
	snapA := Snapshot{
		FunctionName: "test-func",
		LogGroup:     "/aws/lambda/test-func",
		CapturedAt:   "2026-02-25T10:00:00Z",
		Label:        "baseline",
		Invocations: []InvocationRecord{
			{
				RequestID:       "req-1",
				Timestamp:       "2026-02-25T10:00:00Z",
				Duration:        "100 ms",
				MaxMemoryUsedMB: "85 MB",
				IsError:         false,
				LogLines:        []string{"starting"},
			},
		},
	}
	snapB := Snapshot{
		FunctionName: "test-func",
		LogGroup:     "/aws/lambda/test-func",
		CapturedAt:   "2026-02-25T12:00:00Z",
		Label:        "new",
		Invocations: []InvocationRecord{
			{
				RequestID:       "req-2",
				Timestamp:       "2026-02-25T12:00:00Z",
				Duration:        "120 ms",
				MaxMemoryUsedMB: "90 MB",
				IsError:         true,
				ErrorLines:      []string{"connection error"},
				LogLines:        []string{"starting", "connection error"},
			},
		},
	}

	dir := t.TempDir()
	fileA := filepath.Join(dir, "a.json")
	fileB := filepath.Join(dir, "b.json")

	for _, pair := range []struct {
		path string
		snap Snapshot
	}{{fileA, snapA}, {fileB, snapB}} {
		data, err := json.MarshalIndent(pair.snap, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pair.path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}

	if err := runCompare(fileA, fileB); err != nil {
		t.Fatalf("runCompare error: %v", err)
	}
}

func TestRunCompare_FileNotFound(t *testing.T) {
	err := runCompare("/nonexistent/a.json", "/nonexistent/b.json")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestComparisonWarnings(t *testing.T) {
	t.Run("no mismatch", func(t *testing.T) {
		got := comparisonWarnings(
			Snapshot{FunctionName: "func-a", LogGroup: "/aws/lambda/func-a"},
			Snapshot{FunctionName: "func-a", LogGroup: "/aws/lambda/func-a"},
		)
		if len(got) != 0 {
			t.Fatalf("comparisonWarnings returned %v, want no warnings", got)
		}
	})

	t.Run("function and log group mismatch", func(t *testing.T) {
		got := comparisonWarnings(
			Snapshot{FunctionName: "func-a", LogGroup: "/aws/lambda/func-a"},
			Snapshot{FunctionName: "func-b", LogGroup: "/aws/lambda/func-b"},
		)
		if len(got) != 2 {
			t.Fatalf("comparisonWarnings returned %v, want 2 warnings", got)
		}
		if got[0] != "snapshot function names differ (func-a vs func-b)" {
			t.Fatalf("first warning = %q", got[0])
		}
		if got[1] != "snapshot log groups differ (/aws/lambda/func-a vs /aws/lambda/func-b)" {
			t.Fatalf("second warning = %q", got[1])
		}
	})
}

func TestFormatInvocationSummary(t *testing.T) {
	inv := InvocationRecord{
		Timestamp:       "2026-02-25T12:00:00Z",
		Duration:        "120 ms",
		MaxMemoryUsedMB: "90 MB",
		MemorySizeMB:    "128 MB",
		IsError:         true,
	}

	got := formatInvocationSummary(inv)
	want := "    2026-02-25T12:00:00Z  dur=120 ms  peak_mem=90 MB  mem_size=128 MB [ERROR]"
	if got != want {
		t.Fatalf("formatInvocationSummary() = %q, want %q", got, want)
	}
}

func TestPrintSnapshotSummary_ShowsMostRecentInvocationsFirst(t *testing.T) {
	snap := Snapshot{
		FunctionName: "test-func",
		LogGroup:     "/aws/lambda/test-func",
		Label:        "baseline",
		Invocations: []InvocationRecord{
			{Timestamp: "2026-02-25T10:06:00Z", Duration: "60 ms", MaxMemoryUsedMB: "96 MB", MemorySizeMB: "128 MB"},
			{Timestamp: "2026-02-25T10:05:00Z", Duration: "50 ms", MaxMemoryUsedMB: "95 MB", MemorySizeMB: "128 MB"},
			{Timestamp: "2026-02-25T10:04:00Z", Duration: "40 ms", MaxMemoryUsedMB: "94 MB", MemorySizeMB: "128 MB"},
			{Timestamp: "2026-02-25T10:03:00Z", Duration: "30 ms", MaxMemoryUsedMB: "93 MB", MemorySizeMB: "128 MB"},
			{Timestamp: "2026-02-25T10:02:00Z", Duration: "20 ms", MaxMemoryUsedMB: "92 MB", MemorySizeMB: "128 MB"},
			{Timestamp: "2026-02-25T10:01:00Z", Duration: "10 ms", MaxMemoryUsedMB: "91 MB", MemorySizeMB: "128 MB"},
		},
	}

	output := captureStdout(t, func() {
		printSnapshotSummary("BASELINE", snap)
	})

	if !strings.Contains(output, "2026-02-25T10:06:00Z") {
		t.Fatalf("output did not include newest invocation: %s", output)
	}
	if strings.Contains(output, "2026-02-25T10:01:00Z") {
		t.Fatalf("output included the sixth-oldest invocation: %s", output)
	}
}

func TestRunCompare_WarnsOnMismatchedSnapshots(t *testing.T) {
	snapA := Snapshot{
		FunctionName: "func-a",
		LogGroup:     "/aws/lambda/func-a",
		CapturedAt:   "2026-02-25T10:00:00Z",
		Label:        "baseline",
	}
	snapB := Snapshot{
		FunctionName: "func-b",
		LogGroup:     "/aws/lambda/func-b",
		CapturedAt:   "2026-02-25T12:00:00Z",
		Label:        "new",
	}

	dir := t.TempDir()
	fileA := filepath.Join(dir, "a.json")
	fileB := filepath.Join(dir, "b.json")
	for _, pair := range []struct {
		path string
		snap Snapshot
	}{{fileA, snapA}, {fileB, snapB}} {
		data, err := json.MarshalIndent(pair.snap, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pair.path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}

	output := captureStdout(t, func() {
		if err := runCompare(fileA, fileB); err != nil {
			t.Fatalf("runCompare error: %v", err)
		}
	})

	if !strings.Contains(output, "WARNING: snapshot function names differ (func-a vs func-b)") {
		t.Fatalf("output missing function-name warning: %s", output)
	}
	if !strings.Contains(output, "WARNING: snapshot log groups differ (/aws/lambda/func-a vs /aws/lambda/func-b)") {
		t.Fatalf("output missing log-group warning: %s", output)
	}
}

func TestParseDurationMs(t *testing.T) {
	tests := []struct {
		input string
		want  float64
		ok    bool
	}{
		{"123.45 ms", 123.45, true},
		{"0.5ms", 0.5, true},
		{"  200 ms  ", 200.0, true},
		{"", 0.0, false},
		{"abc ms", 0.0, false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, ok := parseDurationMs(tt.input)
			if ok != tt.ok || got != tt.want {
				t.Errorf("parseDurationMs(%q) = (%f, %t), want (%f, %t)", tt.input, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	oldStdout := stdout
	var buf bytes.Buffer
	stdout = &buf
	defer func() { stdout = oldStdout }()

	fn()
	return buf.String()
}

func TestParseMemMB(t *testing.T) {
	tests := []struct {
		input string
		want  float64
		ok    bool
	}{
		{"128 MB", 128.0, true},
		{"85MB", 85.0, true},
		{"  256 MB  ", 256.0, true},
		{"", 0.0, false},
		{"oops", 0.0, false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, ok := parseMemMB(tt.input)
			if ok != tt.ok || got != tt.want {
				t.Errorf("parseMemMB(%q) = (%f, %t), want (%f, %t)", tt.input, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestLoadSnapshot_BackwardCompatibleMemoryFields(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "legacy.json")
	data := []byte(`{
  "function_name": "test-func",
  "log_group": "/aws/lambda/test-func",
  "captured_at": "2026-02-25T10:00:00Z",
  "label": "legacy",
  "invocations": [
    {
      "request_id": "req-1",
      "timestamp": "2026-02-25T10:00:00Z",
      "duration": "100 ms",
      "billed_ms": "100 ms",
      "mem_used_mb": "128 MB",
      "max_mem_mb": "85 MB",
      "is_error": false,
      "log_lines": ["hello"]
    }
  ]
}`)
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadSnapshot(tmpFile)
	if err != nil {
		t.Fatalf("loadSnapshot error: %v", err)
	}
	if got := loaded.Invocations[0].MemorySizeMB; got != "128 MB" {
		t.Fatalf("MemorySizeMB = %q, want %q", got, "128 MB")
	}
	if got := loaded.Invocations[0].MaxMemoryUsedMB; got != "85 MB" {
		t.Fatalf("MaxMemoryUsedMB = %q, want %q", got, "85 MB")
	}
}

func TestRunCompare_StrictMismatchFails(t *testing.T) {
	dir := t.TempDir()
	fileA := filepath.Join(dir, "a.json")
	fileB := filepath.Join(dir, "b.json")
	for _, pair := range []struct {
		path string
		snap Snapshot
	}{
		{fileA, Snapshot{FunctionName: "func-a", LogGroup: "/aws/lambda/func-a"}},
		{fileB, Snapshot{FunctionName: "func-b", LogGroup: "/aws/lambda/func-b"}},
	} {
		data, err := json.MarshalIndent(pair.snap, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pair.path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}

	err := runCompareWithOptions(fileA, fileB, CompareOptions{Strict: true})
	if err == nil {
		t.Fatal("expected strict compare error")
	}
	if !strings.Contains(err.Error(), "strict comparison failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrintDurationStats_IgnoresMalformedValues(t *testing.T) {
	snap := Snapshot{
		Invocations: []InvocationRecord{
			{Duration: "100 ms"},
			{Duration: "oops"},
			{Duration: "50 ms"},
		},
	}

	output := captureStdout(t, func() {
		printDurationStats("Baseline", snap)
	})
	if !strings.Contains(output, "ignored=1 malformed") {
		t.Fatalf("output missing malformed count: %s", output)
	}
}

func TestPrintMemoryStats_IgnoresMalformedValues(t *testing.T) {
	snap := Snapshot{
		Invocations: []InvocationRecord{
			{MaxMemoryUsedMB: "90 MB"},
			{MaxMemoryUsedMB: "bad"},
			{MaxMemoryUsedMB: "80 MB"},
		},
	}

	output := captureStdout(t, func() {
		printMemoryStats("Baseline", snap)
	})
	if !strings.Contains(output, "ignored=1 malformed") {
		t.Fatalf("output missing malformed count: %s", output)
	}
}

func TestRunCompare_GoldenOutput(t *testing.T) {
	output := captureStdout(t, func() {
		if err := runCompare("testdata/compare_baseline.json", "testdata/compare_new.json"); err != nil {
			t.Fatalf("runCompare error: %v", err)
		}
	})

	want, err := os.ReadFile("testdata/compare_report.golden")
	if err != nil {
		t.Fatalf("failed to read golden file: %v", err)
	}
	if output != string(want) {
		t.Fatalf("compare output mismatch\n--- got ---\n%s\n--- want ---\n%s", output, string(want))
	}
}
