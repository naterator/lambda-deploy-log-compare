package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

const maxPatternDisplay = 20

func runCompare(fileA, fileB string) error {
	snapA, err := loadSnapshot(fileA)
	if err != nil {
		return fmt.Errorf("load %s: %w", fileA, err)
	}
	snapB, err := loadSnapshot(fileB)
	if err != nil {
		return fmt.Errorf("load %s: %w", fileB, err)
	}

	fmt.Printf("=== Comparison: %s ===\n", snapA.FunctionName)
	fmt.Printf("  Baseline: %s (label: %s, captured: %s)\n", fileA, snapA.Label, snapA.CapturedAt)
	fmt.Printf("  New:      %s (label: %s, captured: %s)\n\n", fileB, snapB.Label, snapB.CapturedAt)

	printSnapshotSummary("BASELINE", snapA)
	fmt.Println()
	printSnapshotSummary("NEW", snapB)
	fmt.Println()

	// Error comparison
	fmt.Println("=== Error Comparison ===")
	errorsA := countErrors(snapA)
	errorsB := countErrors(snapB)
	fmt.Printf("  Baseline errors: %d / %d invocations\n", errorsA, len(snapA.Invocations))
	fmt.Printf("  New errors:      %d / %d invocations\n", errorsB, len(snapB.Invocations))

	if errorsB > errorsA {
		fmt.Println("  *** WARNING: Error count increased! ***")
	} else if errorsB < errorsA {
		fmt.Println("  Error count decreased (good)")
	} else {
		fmt.Println("  Error count unchanged")
	}

	patternsA := errorPatterns(snapA)
	patternsB := errorPatterns(snapB)

	newPatterns := diffPatterns(patternsA, patternsB)
	if len(newPatterns) > 0 {
		fmt.Println("\n  *** NEW error patterns in new deployment: ***")
		for _, p := range newPatterns {
			fmt.Printf("    - %s\n", p)
		}
	}
	gonePatterns := diffPatterns(patternsB, patternsA)
	if len(gonePatterns) > 0 {
		fmt.Println("\n  Error patterns no longer appearing:")
		for _, p := range gonePatterns {
			fmt.Printf("    - %s\n", p)
		}
	}

	// Duration comparison
	fmt.Println("\n=== Duration Comparison ===")
	printDurationStats("Baseline", snapA)
	printDurationStats("New     ", snapB)

	// Memory comparison
	fmt.Println("\n=== Memory Usage Comparison ===")
	printMemoryStats("Baseline", snapA)
	printMemoryStats("New     ", snapB)

	// Log pattern diff
	fmt.Println("\n=== Log Pattern Diff ===")
	logPatsA := logPatterns(snapA)
	logPatsB := logPatterns(snapB)

	newLogPats := diffPatterns(logPatsA, logPatsB)
	goneLogPats := diffPatterns(logPatsB, logPatsA)

	if len(newLogPats) > 0 {
		fmt.Println("  New log patterns (only in new deployment):")
		limit := maxPatternDisplay
		for i, p := range newLogPats {
			if i >= limit {
				fmt.Printf("    ... and %d more\n", len(newLogPats)-limit)
				break
			}
			fmt.Printf("    + %s\n", truncate(p, 120))
		}
	}
	if len(goneLogPats) > 0 {
		fmt.Println("  Gone log patterns (only in baseline):")
		limit := maxPatternDisplay
		for i, p := range goneLogPats {
			if i >= limit {
				fmt.Printf("    ... and %d more\n", len(goneLogPats)-limit)
				break
			}
			fmt.Printf("    - %s\n", truncate(p, 120))
		}
	}
	if len(newLogPats) == 0 && len(goneLogPats) == 0 {
		fmt.Println("  No significant log pattern differences detected")
	}

	return nil
}

func loadSnapshot(path string) (Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, err
	}
	var snap Snapshot
	err = json.Unmarshal(data, &snap)
	return snap, err
}

func printSnapshotSummary(label string, snap Snapshot) {
	fmt.Printf("--- %s: %s [%s] (log group: %s) ---\n", label, snap.FunctionName, snap.Label, snap.LogGroup)
	fmt.Printf("  Invocations captured: %d\n", len(snap.Invocations))
	fmt.Printf("  Errors: %d\n", countErrors(snap))

	if len(snap.Invocations) > 0 {
		fmt.Println("  Recent invocations:")
		shown := 0
		for i := len(snap.Invocations) - 1; i >= 0 && shown < 5; i-- {
			inv := snap.Invocations[i]
			errMark := ""
			if inv.IsError {
				errMark = " [ERROR]"
			}
			fmt.Printf("    %s  dur=%s  mem=%s%s\n", inv.Timestamp, inv.Duration, inv.MemUsedMB, errMark)
			shown++
		}
	}
}

func countErrors(snap Snapshot) int {
	count := 0
	for _, inv := range snap.Invocations {
		if inv.IsError {
			count++
		}
	}
	return count
}

func errorPatterns(snap Snapshot) map[string]bool {
	return collectPatterns(snap, func(inv InvocationRecord) []string { return inv.ErrorLines })
}

func logPatterns(snap Snapshot) map[string]bool {
	return collectPatterns(snap, func(inv InvocationRecord) []string { return inv.LogLines })
}

func collectPatterns(snap Snapshot, lines func(InvocationRecord) []string) map[string]bool {
	patterns := make(map[string]bool)
	for _, inv := range snap.Invocations {
		for _, line := range lines(inv) {
			patterns[normalizeLogLine(line)] = true
		}
	}
	return patterns
}

func diffPatterns(baseline, other map[string]bool) []string {
	var diff []string
	for pattern := range other {
		if !baseline[pattern] {
			diff = append(diff, pattern)
		}
	}
	sort.Strings(diff)
	return diff
}

func parseDurationMs(dur string) float64 {
	dur = strings.TrimSpace(dur)
	dur = strings.TrimSuffix(dur, " ms")
	dur = strings.TrimSuffix(dur, "ms")
	var ms float64
	fmt.Sscanf(dur, "%f", &ms)
	return ms
}

func parseMemMB(mem string) float64 {
	mem = strings.TrimSpace(mem)
	mem = strings.TrimSuffix(mem, " MB")
	mem = strings.TrimSuffix(mem, "MB")
	var mb float64
	fmt.Sscanf(mem, "%f", &mb)
	return mb
}

func printDurationStats(label string, snap Snapshot) {
	var durations []float64
	for _, inv := range snap.Invocations {
		if inv.Duration != "" {
			durations = append(durations, parseDurationMs(inv.Duration))
		}
	}
	if len(durations) == 0 {
		fmt.Printf("  %s: no duration data\n", label)
		return
	}
	sort.Float64s(durations)
	sum := 0.0
	for _, d := range durations {
		sum += d
	}
	avg := sum / float64(len(durations))
	p50 := durations[len(durations)/2]
	p90idx := int(float64(len(durations)) * 0.9)
	if p90idx >= len(durations) {
		p90idx = len(durations) - 1
	}

	fmt.Printf("  %s: min=%.1fms avg=%.1fms p50=%.1fms p90=%.1fms max=%.1fms (n=%d)\n",
		label, durations[0], avg, p50, durations[p90idx], durations[len(durations)-1], len(durations))
}

func printMemoryStats(label string, snap Snapshot) {
	var mems []float64
	for _, inv := range snap.Invocations {
		if inv.MaxMemMB != "" {
			mems = append(mems, parseMemMB(inv.MaxMemMB))
		}
	}
	if len(mems) == 0 {
		fmt.Printf("  %s: no memory data\n", label)
		return
	}
	sort.Float64s(mems)
	sum := 0.0
	for _, m := range mems {
		sum += m
	}
	fmt.Printf("  %s: min=%.0fMB avg=%.0fMB max=%.0fMB (n=%d)\n",
		label, mems[0], sum/float64(len(mems)), mems[len(mems)-1], len(mems))
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
