package main

import (
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func TestNumericInputsMustBeFiniteAndNonnegative(t *testing.T) {
	for _, value := range []string{"NaN", "+Inf", "-Inf", "-1", "1e999"} {
		for _, suffix := range []string{"", " ms", " MB"} {
			if number, ok := parseNumberWithSuffixes(value+suffix, " ms", " MB"); ok {
				t.Fatalf("accepted %q: %v", value+suffix, number)
			}
		}
		for _, flag := range []string{"--max-duration-regression-pct", "--max-memory-regression-pct"} {
			code, out, diagnostic := runCLI(t, []string{"compare", "--a", "missing", "--b", "missing", flag, value})
			if code == 0 || out != "" || strings.Contains(diagnostic, "load missing") {
				t.Fatalf("invalid flag was not rejected before IO: %s %s: %d %q %q", flag, value, code, out, diagnostic)
			}
		}
	}
}

func TestMetricAggregatesDoNotOverflow(t *testing.T) {
	stats := computeMetricStats([]float64{math.MaxFloat64, math.MaxFloat64, math.NaN(), -1}, 0)
	if stats.Count != 2 || stats.IgnoredMalformed != 2 || stats.Avg != math.MaxFloat64 || stats.P50 != math.MaxFloat64 {
		t.Fatalf("invalid aggregate: %+v", stats)
	}
	if _, err := json.Marshal(stats); err != nil {
		t.Fatal(err)
	}
}

func TestRelativeChangeNumericalBoundaries(t *testing.T) {
	for _, tt := range []struct {
		before, after, want float64
		defined             bool
	}{
		{100, 120, 20, true}, {100, 80, 0, true},
		{0, 0, 0, false}, {0, 100, 0, false},
		{math.SmallestNonzeroFloat64, math.MaxFloat64, 0, false},
	} {
		got, defined := percentIncrease(tt.before, tt.after)
		if got != tt.want || defined != tt.defined {
			t.Fatalf("%v -> %v: %v %v", tt.before, tt.after, got, defined)
		}
	}
}

func TestNumericGateDecisionsAgreeAcrossOutputFormats(t *testing.T) {
	for _, tt := range []struct {
		name, before, after string
		fail                bool
	}{
		{"at boundary", "100 ms", "120 ms", false},
		{"over boundary", "100 ms", "120.1 ms", true},
		{"zero baseline", "0 ms", "100 ms", true},
		{"zero to zero", "0 ms", "0 ms", true},
		{"unrepresentable", "5e-324 ms", "1.7976931348623157e308 ms", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			a, b := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")
			for path, duration := range map[string]string{a: tt.before, b: tt.after} {
				writeSnapshotFile(t, path, Snapshot{FunctionName: "f", LogGroup: "/aws/lambda/f", Invocations: []InvocationRecord{{Duration: duration}}})
			}
			for _, machine := range []bool{false, true} {
				args := []string{"compare", "--a", a, "--b", b, "--max-duration-regression-pct", "20"}
				if machine {
					args = append(args, "--json")
				}
				code, out, diagnostic := runCLI(t, args)
				if (code != 0) != tt.fail {
					t.Fatalf("json=%t: exit=%d stdout=%s stderr=%s", machine, code, out, diagnostic)
				}
				if machine && !json.Valid([]byte(out)) {
					t.Fatalf("invalid JSON: %s", out)
				}
			}
		})
	}
}

func TestMalformedMetricsStillProduceValidJSON(t *testing.T) {
	snap := Snapshot{FunctionName: "f", Invocations: []InvocationRecord{{Duration: "NaN", MaxMemoryUsedMB: "-Inf"}, {Duration: "-1 ms", MaxMemoryUsedMB: "-4 MB"}}}
	summary := buildCompareSummary("a", "b", snap, snap, nil, CompareOptions{})
	if summary.Duration.New.IgnoredMalformed != 2 || summary.Memory.New.IgnoredMalformed != 2 || summary.Duration.New.HasData {
		t.Fatalf("invalid numeric metrics were accepted: %+v", summary)
	}
	if _, err := json.Marshal(summary); err != nil {
		t.Fatal(err)
	}
}
