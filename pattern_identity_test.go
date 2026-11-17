package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLongErrorSuffixIsComparedAndPreservedInJSON(t *testing.T) {
	prefix := "ERROR: " + strings.Repeat("shared context ", 15)
	aLine, bLine := prefix+"backend failed", prefix+"authorization denied"
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")
	for path, line := range map[string]string{a: aLine, b: bLine} {
		writeSnapshotFile(t, path, Snapshot{FunctionName: "f", Invocations: []InvocationRecord{{IsError: true, ErrorLines: []string{line}, LogLines: []string{line}}}})
	}
	code, out, diagnostic := runCLI(t, []string{"compare", "--a", a, "--b", b, "--json", "--fail-on-regression"})
	if code == 0 {
		t.Fatalf("missed new error suffix: %s %s", out, diagnostic)
	}
	var summary compareSummary
	if err := json.Unmarshal([]byte(out), &summary); err != nil {
		t.Fatal(err)
	}
	if len(summary.Errors.NewPatterns) != 1 || summary.Errors.NewPatterns[0] != bLine || summary.LogPatterns.New[0] != bLine {
		t.Fatalf("lost full identity: %+v", summary)
	}
}

func TestPatternDisplayTruncationIsRuneSafe(t *testing.T) {
	for _, limit := range []int{-1, 0, 1, 3, 4, 120} {
		got := truncate(strings.Repeat("界🙂", 100), limit)
		if !utf8.ValidString(got) || utf8.RuneCountInString(got) > max(limit, 0) {
			t.Fatalf("invalid truncation at %d: %q", limit, got)
		}
	}
	a := strings.Repeat("long context ", 20) + "RequestId: a failed"
	b := strings.Repeat("long context ", 20) + "RequestId: b failed"
	if normalizeLogLine(a) != normalizeLogLine(b) {
		t.Fatal("request-specific values should still normalize after byte 100")
	}
}
