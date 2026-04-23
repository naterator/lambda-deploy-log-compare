package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxPatternDisplay = 20

func runCompare(fileA, fileB string) error {
	return runCompareWithOptions(fileA, fileB, CompareOptions{})
}

type CompareOptions struct {
	Strict bool
}

func runCompareWithOptions(fileA, fileB string, opts CompareOptions) error {
	snapA, err := loadSnapshot(fileA)
	if err != nil {
		return fmt.Errorf("load %s: %w", fileA, err)
	}
	snapB, err := loadSnapshot(fileB)
	if err != nil {
		return fmt.Errorf("load %s: %w", fileB, err)
	}

	warnings := comparisonWarnings(snapA, snapB)
	if opts.Strict && len(warnings) > 0 {
		return fmt.Errorf("strict comparison failed: %s", strings.Join(warnings, "; "))
	}

	displayNamesDiffer := snapshotDisplayName(snapA) != snapshotDisplayName(snapB)
	fmt.Fprintf(stdout, "=== Comparison: %s ===\n", comparisonTitle(snapA, snapB))
	printSnapshotSource("Baseline", fileA, snapA, displayNamesDiffer)
	printSnapshotSource("New", fileB, snapB, displayNamesDiffer)
	fmt.Fprintln(stdout)
	for _, warning := range warnings {
		fmt.Fprintf(stdout, "  WARNING: %s\n", warning)
	}
	if len(warnings) > 0 {
		fmt.Fprintln(stdout)
	}

	printSnapshotSummary("BASELINE", snapA)
	fmt.Fprintln(stdout)
	printSnapshotSummary("NEW", snapB)
	fmt.Fprintln(stdout)

	// Error comparison
	fmt.Fprintln(stdout, "=== Error Comparison ===")
	errorsA := countErrors(snapA)
	errorsB := countErrors(snapB)
	fmt.Fprintf(stdout, "  Baseline errors: %d / %d invocations\n", errorsA, len(snapA.Invocations))
	fmt.Fprintf(stdout, "  New errors:      %d / %d invocations\n", errorsB, len(snapB.Invocations))

	if errorsB > errorsA {
		fmt.Fprintln(stdout, "  *** WARNING: Error count increased! ***")
	} else if errorsB < errorsA {
		fmt.Fprintln(stdout, "  Error count decreased (good)")
	} else {
		fmt.Fprintln(stdout, "  Error count unchanged")
	}

	patternsA := errorPatterns(snapA)
	patternsB := errorPatterns(snapB)

	newPatterns := diffPatterns(patternsA, patternsB)
	if len(newPatterns) > 0 {
		fmt.Fprintln(stdout, "\n  *** NEW error patterns in new deployment: ***")
		for _, p := range newPatterns {
			fmt.Fprintf(stdout, "    - %s\n", p)
		}
	}
	gonePatterns := diffPatterns(patternsB, patternsA)
	if len(gonePatterns) > 0 {
		fmt.Fprintln(stdout, "\n  Error patterns no longer appearing:")
		for _, p := range gonePatterns {
			fmt.Fprintf(stdout, "    - %s\n", p)
		}
	}

	// Duration comparison
	fmt.Fprintln(stdout, "\n=== Duration Comparison ===")
	printDurationStats("Baseline", snapA)
	printDurationStats("New     ", snapB)

	// Memory comparison
	fmt.Fprintln(stdout, "\n=== Memory Usage Comparison ===")
	printMemoryStats("Baseline", snapA)
	printMemoryStats("New     ", snapB)

	// Log pattern diff
	fmt.Fprintln(stdout, "\n=== Log Pattern Diff ===")
	logPatsA := logPatterns(snapA)
	logPatsB := logPatterns(snapB)

	newLogPats := diffPatterns(logPatsA, logPatsB)
	goneLogPats := diffPatterns(logPatsB, logPatsA)

	if len(newLogPats) > 0 {
		fmt.Fprintln(stdout, "  New log patterns (only in new deployment):")
		limit := maxPatternDisplay
		for i, p := range newLogPats {
			if i >= limit {
				fmt.Fprintf(stdout, "    ... and %d more\n", len(newLogPats)-limit)
				break
			}
			fmt.Fprintf(stdout, "    + %s\n", truncate(p, 120))
		}
	}
	if len(goneLogPats) > 0 {
		fmt.Fprintln(stdout, "  Gone log patterns (only in baseline):")
		limit := maxPatternDisplay
		for i, p := range goneLogPats {
			if i >= limit {
				fmt.Fprintf(stdout, "    ... and %d more\n", len(goneLogPats)-limit)
				break
			}
			fmt.Fprintf(stdout, "    - %s\n", truncate(p, 120))
		}
	}
	if len(newLogPats) == 0 && len(goneLogPats) == 0 {
		fmt.Fprintln(stdout, "  No significant log pattern differences detected")
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
	snap.Invocations = sortedInvocations(snap.Invocations)
	return snap, err
}

func printSnapshotSummary(label string, snap Snapshot) {
	fmt.Fprintf(stdout, "--- %s: %s [%s] (log group: %s) ---\n", label, snap.FunctionName, snap.Label, snap.LogGroup)
	fmt.Fprintf(stdout, "  Invocations captured: %d\n", len(snap.Invocations))
	fmt.Fprintf(stdout, "  Errors: %d\n", countErrors(snap))

	invocations := sortedInvocations(snap.Invocations)
	if len(invocations) > 0 {
		fmt.Fprintln(stdout, "  Recent invocations:")
		for i, inv := range invocations {
			if i >= 5 {
				break
			}
			fmt.Fprintln(stdout, formatInvocationSummary(inv))
		}
	}
}

func comparisonTitle(snapA, snapB Snapshot) string {
	nameA := snapshotDisplayName(snapA)
	nameB := snapshotDisplayName(snapB)
	if nameA == nameB {
		return nameA
	}
	return fmt.Sprintf("%s vs %s", nameA, nameB)
}

func printSnapshotSource(label, path string, snap Snapshot, includeDisplayName bool) {
	if includeDisplayName {
		fmt.Fprintf(stdout, "  %-10s%s (%s, label: %s, captured: %s)\n", label+":", path, snapshotDisplayName(snap), snap.Label, snap.CapturedAt)
		return
	}
	fmt.Fprintf(stdout, "  %-10s%s (label: %s, captured: %s)\n", label+":", path, snap.Label, snap.CapturedAt)
}

func snapshotDisplayName(snap Snapshot) string {
	switch {
	case snap.FunctionName != "":
		return snap.FunctionName
	case snap.LogGroup != "":
		return snap.LogGroup
	default:
		return "unknown snapshot"
	}
}

func sortedInvocations(invocations []InvocationRecord) []InvocationRecord {
	sorted := append([]InvocationRecord(nil), invocations...)
	sort.SliceStable(sorted, func(i, j int) bool {
		tsI, okI := parseInvocationTimestamp(sorted[i].Timestamp)
		tsJ, okJ := parseInvocationTimestamp(sorted[j].Timestamp)

		switch {
		case okI && okJ:
			if tsI.Equal(tsJ) {
				return false
			}
			return tsI.After(tsJ)
		case okI:
			return true
		case okJ:
			return false
		default:
			return sorted[i].Timestamp > sorted[j].Timestamp
		}
	})
	return sorted
}

func parseInvocationTimestamp(timestamp string) (time.Time, bool) {
	if timestamp == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func comparisonWarnings(snapA, snapB Snapshot) []string {
	var warnings []string
	if snapA.FunctionName != "" && snapB.FunctionName != "" && snapA.FunctionName != snapB.FunctionName {
		warnings = append(warnings, fmt.Sprintf("snapshot function names differ (%s vs %s)", snapA.FunctionName, snapB.FunctionName))
	}
	if snapA.LogGroup != "" && snapB.LogGroup != "" && snapA.LogGroup != snapB.LogGroup {
		warnings = append(warnings, fmt.Sprintf("snapshot log groups differ (%s vs %s)", snapA.LogGroup, snapB.LogGroup))
	}
	return warnings
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

func parseDurationMs(dur string) (float64, bool) {
	return parseNumberWithSuffixes(dur, " ms", "ms")
}

func parseMemMB(mem string) (float64, bool) {
	return parseNumberWithSuffixes(mem, " MB", "MB")
}

func parseNumberWithSuffixes(value string, suffixes ...string) (float64, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, false
	}
	for _, suffix := range suffixes {
		trimmed = strings.TrimSuffix(trimmed, suffix)
	}
	trimmed = strings.TrimSpace(trimmed)
	number, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0, false
	}
	return number, true
}

func printDurationStats(label string, snap Snapshot) {
	var durations []float64
	ignored := 0
	for _, inv := range snap.Invocations {
		if inv.Duration == "" {
			continue
		}
		duration, ok := parseDurationMs(inv.Duration)
		if !ok {
			ignored++
			continue
		}
		durations = append(durations, duration)
	}
	if len(durations) == 0 {
		if ignored > 0 {
			fmt.Fprintf(stdout, "  %s: no duration data (ignored=%d malformed)\n", label, ignored)
			return
		}
		fmt.Fprintf(stdout, "  %s: no duration data\n", label)
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

	fmt.Fprintf(stdout, "  %s: min=%.1fms avg=%.1fms p50=%.1fms p90=%.1fms max=%.1fms (n=%d, ignored=%d malformed)\n",
		label, durations[0], avg, p50, durations[p90idx], durations[len(durations)-1], len(durations), ignored)
}

func printMemoryStats(label string, snap Snapshot) {
	var mems []float64
	ignored := 0
	for _, inv := range snap.Invocations {
		if inv.MaxMemoryUsedMB == "" {
			continue
		}
		mem, ok := parseMemMB(inv.MaxMemoryUsedMB)
		if !ok {
			ignored++
			continue
		}
		mems = append(mems, mem)
	}
	if len(mems) == 0 {
		if ignored > 0 {
			fmt.Fprintf(stdout, "  %s: no memory data (ignored=%d malformed)\n", label, ignored)
			return
		}
		fmt.Fprintf(stdout, "  %s: no memory data\n", label)
		return
	}
	sort.Float64s(mems)
	sum := 0.0
	for _, m := range mems {
		sum += m
	}
	fmt.Fprintf(stdout, "  %s: min=%.0fMB avg=%.0fMB max=%.0fMB (n=%d, ignored=%d malformed)\n",
		label, mems[0], sum/float64(len(mems)), mems[len(mems)-1], len(mems), ignored)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func formatInvocationSummary(inv InvocationRecord) string {
	parts := []string{
		inv.Timestamp,
		fmt.Sprintf("dur=%s", inv.Duration),
	}
	if inv.MaxMemoryUsedMB != "" {
		parts = append(parts, fmt.Sprintf("peak_mem=%s", inv.MaxMemoryUsedMB))
	}
	if inv.MemorySizeMB != "" {
		parts = append(parts, fmt.Sprintf("mem_size=%s", inv.MemorySizeMB))
	}

	line := "    " + strings.Join(parts, "  ")
	if inv.IsError {
		line += " [ERROR]"
	}
	return line
}
