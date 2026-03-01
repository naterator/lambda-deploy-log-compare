package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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
				RequestID: "req-1",
				Timestamp: "2026-02-25T10:00:00Z",
				Duration:  "100 ms",
				MaxMemMB:  "85 MB",
				IsError:   false,
				LogLines:  []string{"starting"},
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
				RequestID: "req-2",
				Timestamp: "2026-02-25T12:00:00Z",
				Duration:  "120 ms",
				MaxMemMB:  "90 MB",
				IsError:   true,
				ErrorLines: []string{"connection error"},
				LogLines:  []string{"starting", "connection error"},
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

func TestParseDurationMs(t *testing.T) {
	tests := []struct {
		input string
		want  float64
	}{
		{"123.45 ms", 123.45},
		{"0.5ms", 0.5},
		{"  200 ms  ", 200.0},
		{"", 0.0},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseDurationMs(tt.input)
			if got != tt.want {
				t.Errorf("parseDurationMs(%q) = %f, want %f", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseMemMB(t *testing.T) {
	tests := []struct {
		input string
		want  float64
	}{
		{"128 MB", 128.0},
		{"85MB", 85.0},
		{"  256 MB  ", 256.0},
		{"", 0.0},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseMemMB(tt.input)
			if got != tt.want {
				t.Errorf("parseMemMB(%q) = %f, want %f", tt.input, got, tt.want)
			}
		})
	}
}
