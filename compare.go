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
	Strict                         bool
	FailOnRegression               bool
	OutputJSON                     bool
	MaxDurationRegressionPercent   float64
	MaxMemoryUsedRegressionPercent float64
}

type compareSummary struct {
	Title       string              `json:"title"`
	Baseline    snapshotFileSummary `json:"baseline"`
	New         snapshotFileSummary `json:"new"`
	Warnings    []string            `json:"warnings"`
	Errors      errorComparison     `json:"errors"`
	Duration    metricComparison    `json:"duration"`
	Memory      metricComparison    `json:"memory"`
	LogPatterns patternDiff         `json:"log_patterns"`
	Regression  regressionSummary   `json:"regression"`
}

type snapshotFileSummary struct {
	Path            string `json:"path"`
	FunctionName    string `json:"function_name,omitempty"`
	LogGroup        string `json:"log_group,omitempty"`
	Label           string `json:"label,omitempty"`
	CapturedAt      string `json:"captured_at,omitempty"`
	InvocationCount int    `json:"invocation_count"`
	ErrorCount      int    `json:"error_count"`
}

type errorComparison struct {
	BaselineCount int      `json:"baseline_count"`
	NewCount      int      `json:"new_count"`
	NewPatterns   []string `json:"new_patterns"`
	GonePatterns  []string `json:"gone_patterns"`
}

type metricComparison struct {
	Baseline                  metricStats `json:"baseline"`
	New                       metricStats `json:"new"`
	MaxRegressionPercent      float64     `json:"max_regression_percent,omitempty"`
	ObservedRegressionPercent float64     `json:"observed_regression_percent,omitempty"`
}

type metricStats struct {
	HasData          bool    `json:"has_data"`
	Count            int     `json:"count"`
	IgnoredMalformed int     `json:"ignored_malformed"`
	Min              float64 `json:"min"`
	Avg              float64 `json:"avg"`
	P50              float64 `json:"p50"`
	P90              float64 `json:"p90"`
	Max              float64 `json:"max"`
}

type patternDiff struct {
	New  []string `json:"new"`
	Gone []string `json:"gone"`
}

type regressionSummary struct {
	Detected bool     `json:"detected"`
	WillFail bool     `json:"will_fail"`
	Reasons  []string `json:"reasons"`
}

func runCompareWithOptions(fileA, fileB string, opts CompareOptions) error {
	if err := validateCompareOptions(opts); err != nil {
		return err
	}

	snapA, err := loadSnapshot(fileA)
	if err != nil {
		return fmt.Errorf("load %s: %w", fileA, err)
	}
	snapB, err := loadSnapshot(fileB)
	if err != nil {
		return fmt.Errorf("load %s: %w", fileB, err)
	}

	warnings := comparisonWarnings(snapA, snapB)
	if opts.Strict {
		strictWarnings := strictComparisonWarnings(snapA, snapB)
		if len(strictWarnings) > 0 {
			return fmt.Errorf("strict comparison failed: %s", strings.Join(strictWarnings, "; "))
		}
	}

	summary := buildCompareSummary(fileA, fileB, snapA, snapB, warnings, opts)
	if opts.OutputJSON {
		if err := printCompareSummaryJSON(summary); err != nil {
			return err
		}
		if summary.Regression.WillFail {
			return fmt.Errorf("regression detected: %s", strings.Join(summary.Regression.Reasons, "; "))
		}
		return nil
	}

	printCompareSummary(summary, snapA, snapB, opts)
	if summary.Regression.WillFail {
		return fmt.Errorf("regression detected: %s", strings.Join(summary.Regression.Reasons, "; "))
	}

	return nil
}

func validateCompareOptions(opts CompareOptions) error {
	if opts.MaxDurationRegressionPercent < 0 {
		return fmt.Errorf("--max-duration-regression-pct must be greater than or equal to 0")
	}
	if opts.MaxMemoryUsedRegressionPercent < 0 {
		return fmt.Errorf("--max-memory-regression-pct must be greater than or equal to 0")
	}
	return nil
}

func buildCompareSummary(fileA, fileB string, snapA, snapB Snapshot, warnings []string, opts CompareOptions) compareSummary {
	errorsA := countErrors(snapA)
	errorsB := countErrors(snapB)
	patternsA := errorPatterns(snapA)
	patternsB := errorPatterns(snapB)
	newPatterns := diffPatterns(patternsA, patternsB)
	gonePatterns := diffPatterns(patternsB, patternsA)
	logPatsA := logPatterns(snapA)
	logPatsB := logPatterns(snapB)
	newLogPats := diffPatterns(logPatsA, logPatsB)
	goneLogPats := diffPatterns(logPatsB, logPatsA)

	durationComparison := metricComparison{
		Baseline:             durationStats(snapA),
		New:                  durationStats(snapB),
		MaxRegressionPercent: opts.MaxDurationRegressionPercent,
	}
	durationComparison.ObservedRegressionPercent = percentIncrease(durationComparison.Baseline.P90, durationComparison.New.P90)

	memoryComparison := metricComparison{
		Baseline:             memoryStats(snapA),
		New:                  memoryStats(snapB),
		MaxRegressionPercent: opts.MaxMemoryUsedRegressionPercent,
	}
	memoryComparison.ObservedRegressionPercent = percentIncrease(memoryComparison.Baseline.Max, memoryComparison.New.Max)

	basicReasons := regressionReasons(errorsA, errorsB, newPatterns)
	gateReasons := metricRegressionReasons(durationComparison, memoryComparison, opts)
	allReasons := append(append([]string(nil), basicReasons...), gateReasons...)

	return compareSummary{
		Title: comparisonTitle(snapA, snapB),
		Baseline: snapshotFileSummary{
			Path:            fileA,
			FunctionName:    snapA.FunctionName,
			LogGroup:        snapA.LogGroup,
			Label:           snapA.Label,
			CapturedAt:      snapA.CapturedAt,
			InvocationCount: len(snapA.Invocations),
			ErrorCount:      errorsA,
		},
		New: snapshotFileSummary{
			Path:            fileB,
			FunctionName:    snapB.FunctionName,
			LogGroup:        snapB.LogGroup,
			Label:           snapB.Label,
			CapturedAt:      snapB.CapturedAt,
			InvocationCount: len(snapB.Invocations),
			ErrorCount:      errorsB,
		},
		Warnings: warnings,
		Errors: errorComparison{
			BaselineCount: errorsA,
			NewCount:      errorsB,
			NewPatterns:   newPatterns,
			GonePatterns:  gonePatterns,
		},
		Duration: durationComparison,
		Memory:   memoryComparison,
		LogPatterns: patternDiff{
			New:  newLogPats,
			Gone: goneLogPats,
		},
		Regression: regressionSummary{
			Detected: len(allReasons) > 0,
			WillFail: (opts.FailOnRegression && len(basicReasons) > 0) ||
				len(gateReasons) > 0,
			Reasons: allReasons,
		},
	}
}

func printCompareSummaryJSON(summary compareSummary) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(summary)
}

func printCompareSummary(summary compareSummary, snapA, snapB Snapshot, opts CompareOptions) {
	displayNamesDiffer := snapshotDisplayName(snapA) != snapshotDisplayName(snapB)
	fmt.Fprintf(stdout, "=== Comparison: %s ===\n", summary.Title)
	printSnapshotSource("Baseline", summary.Baseline.Path, snapA, displayNamesDiffer)
	printSnapshotSource("New", summary.New.Path, snapB, displayNamesDiffer)
	fmt.Fprintln(stdout)
	for _, warning := range summary.Warnings {
		fmt.Fprintf(stdout, "  WARNING: %s\n", warning)
	}
	if len(summary.Warnings) > 0 {
		fmt.Fprintln(stdout)
	}

	printSnapshotSummary("BASELINE", snapA)
	fmt.Fprintln(stdout)
	printSnapshotSummary("NEW", snapB)
	fmt.Fprintln(stdout)

	// Error comparison
	fmt.Fprintln(stdout, "=== Error Comparison ===")
	fmt.Fprintf(stdout, "  Baseline errors: %d / %d invocations\n", summary.Errors.BaselineCount, summary.Baseline.InvocationCount)
	fmt.Fprintf(stdout, "  New errors:      %d / %d invocations\n", summary.Errors.NewCount, summary.New.InvocationCount)

	if summary.Errors.NewCount > summary.Errors.BaselineCount {
		fmt.Fprintln(stdout, "  *** WARNING: Error count increased! ***")
	} else if summary.Errors.NewCount < summary.Errors.BaselineCount {
		fmt.Fprintln(stdout, "  Error count decreased (good)")
	} else {
		fmt.Fprintln(stdout, "  Error count unchanged")
	}

	if len(summary.Errors.NewPatterns) > 0 {
		fmt.Fprintln(stdout, "\n  *** NEW error patterns in new deployment: ***")
		for _, p := range summary.Errors.NewPatterns {
			fmt.Fprintf(stdout, "    - %s\n", p)
		}
	}
	if len(summary.Errors.GonePatterns) > 0 {
		fmt.Fprintln(stdout, "\n  Error patterns no longer appearing:")
		for _, p := range summary.Errors.GonePatterns {
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

	if opts.MaxDurationRegressionPercent > 0 || opts.MaxMemoryUsedRegressionPercent > 0 {
		fmt.Fprintln(stdout, "\n=== Regression Gates ===")
		gateReasons := metricRegressionReasons(summary.Duration, summary.Memory, opts)
		if len(gateReasons) == 0 {
			fmt.Fprintln(stdout, "  No configured duration or memory regression gates exceeded")
		} else {
			for _, reason := range gateReasons {
				fmt.Fprintf(stdout, "  *** WARNING: %s ***\n", reason)
			}
		}
	}

	// Log pattern diff
	fmt.Fprintln(stdout, "\n=== Log Pattern Diff ===")

	if len(summary.LogPatterns.New) > 0 {
		fmt.Fprintln(stdout, "  New log patterns (only in new deployment):")
		limit := maxPatternDisplay
		for i, p := range summary.LogPatterns.New {
			if i >= limit {
				fmt.Fprintf(stdout, "    ... and %d more\n", len(summary.LogPatterns.New)-limit)
				break
			}
			fmt.Fprintf(stdout, "    + %s\n", truncate(p, 120))
		}
	}
	if len(summary.LogPatterns.Gone) > 0 {
		fmt.Fprintln(stdout, "  Gone log patterns (only in baseline):")
		limit := maxPatternDisplay
		for i, p := range summary.LogPatterns.Gone {
			if i >= limit {
				fmt.Fprintf(stdout, "    ... and %d more\n", len(summary.LogPatterns.Gone)-limit)
				break
			}
			fmt.Fprintf(stdout, "    - %s\n", truncate(p, 120))
		}
	}
	if len(summary.LogPatterns.New) == 0 && len(summary.LogPatterns.Gone) == 0 {
		fmt.Fprintln(stdout, "  No significant log pattern differences detected")
	}
}

func loadSnapshot(path string) (Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, err
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return Snapshot{}, err
	}
	if err := validateLoadedSnapshot(snap); err != nil {
		return Snapshot{}, err
	}
	snap.Invocations = sortedInvocations(snap.Invocations)
	return snap, nil
}

func validateLoadedSnapshot(snap Snapshot) error {
	if snap.FunctionName != "" || snap.LogGroup != "" || snap.CapturedAt != "" || snap.Label != "" {
		return nil
	}

	for _, inv := range snap.Invocations {
		if invocationHasSnapshotData(inv) {
			return nil
		}
	}

	return fmt.Errorf("invalid snapshot: missing snapshot metadata and invocation records")
}

func invocationHasSnapshotData(inv InvocationRecord) bool {
	return inv.RequestID != "" ||
		inv.Timestamp != "" ||
		inv.Duration != "" ||
		inv.BilledMs != "" ||
		inv.MemorySizeMB != "" ||
		inv.MaxMemoryUsedMB != "" ||
		inv.IsError ||
		len(inv.ErrorLines) > 0 ||
		len(inv.LogLines) > 0
}

func printSnapshotSummary(label string, snap Snapshot) {
	fmt.Fprintf(stdout, "--- %s: %s [%s] (log group: %s) ---\n", label, snapshotDisplayName(snap), snap.Label, snap.LogGroup)
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

func strictComparisonWarnings(snapA, snapB Snapshot) []string {
	var warnings []string
	warnings = append(warnings, missingSnapshotIdentityWarnings("baseline", snapA)...)
	warnings = append(warnings, missingSnapshotIdentityWarnings("new", snapB)...)
	warnings = append(warnings, comparisonWarnings(snapA, snapB)...)
	return warnings
}

func missingSnapshotIdentityWarnings(label string, snap Snapshot) []string {
	var warnings []string
	if snap.FunctionName == "" {
		warnings = append(warnings, fmt.Sprintf("%s snapshot missing function_name", label))
	}
	if snap.LogGroup == "" {
		warnings = append(warnings, fmt.Sprintf("%s snapshot missing log_group", label))
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

func regressionReasons(errorsA, errorsB int, newPatterns []string) []string {
	var reasons []string
	if errorsB > errorsA {
		reasons = append(reasons, fmt.Sprintf("error count increased from %d to %d", errorsA, errorsB))
	}
	if len(newPatterns) > 0 {
		reasons = append(reasons, fmt.Sprintf("%d new error pattern(s)", len(newPatterns)))
	}
	return reasons
}

func metricRegressionReasons(duration, memory metricComparison, opts CompareOptions) []string {
	var reasons []string
	if opts.MaxDurationRegressionPercent > 0 &&
		duration.Baseline.HasData &&
		duration.New.HasData &&
		duration.ObservedRegressionPercent > opts.MaxDurationRegressionPercent {
		reasons = append(reasons, fmt.Sprintf("p90 duration increased by %.1f%% (%.1fms to %.1fms), over %.1f%% limit",
			duration.ObservedRegressionPercent, duration.Baseline.P90, duration.New.P90, opts.MaxDurationRegressionPercent))
	}
	if opts.MaxMemoryUsedRegressionPercent > 0 &&
		memory.Baseline.HasData &&
		memory.New.HasData &&
		memory.ObservedRegressionPercent > opts.MaxMemoryUsedRegressionPercent {
		reasons = append(reasons, fmt.Sprintf("max memory used increased by %.1f%% (%.0fMB to %.0fMB), over %.1f%% limit",
			memory.ObservedRegressionPercent, memory.Baseline.Max, memory.New.Max, opts.MaxMemoryUsedRegressionPercent))
	}
	return reasons
}

func percentIncrease(baseline, newValue float64) float64 {
	if baseline <= 0 || newValue <= baseline {
		return 0
	}
	return ((newValue - baseline) / baseline) * 100
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
	stats := durationStats(snap)
	if !stats.HasData {
		if stats.IgnoredMalformed > 0 {
			fmt.Fprintf(stdout, "  %s: no duration data (ignored=%d malformed)\n", label, stats.IgnoredMalformed)
			return
		}
		fmt.Fprintf(stdout, "  %s: no duration data\n", label)
		return
	}

	fmt.Fprintf(stdout, "  %s: min=%.1fms avg=%.1fms p50=%.1fms p90=%.1fms max=%.1fms (n=%d, ignored=%d malformed)\n",
		label, stats.Min, stats.Avg, stats.P50, stats.P90, stats.Max, stats.Count, stats.IgnoredMalformed)
}

func durationStats(snap Snapshot) metricStats {
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
	return computeMetricStats(durations, ignored)
}

func median(sorted []float64) float64 {
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func printMemoryStats(label string, snap Snapshot) {
	stats := memoryStats(snap)
	if !stats.HasData {
		if stats.IgnoredMalformed > 0 {
			fmt.Fprintf(stdout, "  %s: no memory data (ignored=%d malformed)\n", label, stats.IgnoredMalformed)
			return
		}
		fmt.Fprintf(stdout, "  %s: no memory data\n", label)
		return
	}

	fmt.Fprintf(stdout, "  %s: min=%.0fMB avg=%.0fMB max=%.0fMB (n=%d, ignored=%d malformed)\n",
		label, stats.Min, stats.Avg, stats.Max, stats.Count, stats.IgnoredMalformed)
}

func memoryStats(snap Snapshot) metricStats {
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
	return computeMetricStats(mems, ignored)
}

func computeMetricStats(values []float64, ignored int) metricStats {
	if len(values) == 0 {
		return metricStats{IgnoredMalformed: ignored}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	sum := 0.0
	for _, value := range sorted {
		sum += value
	}
	p90idx := int(float64(len(sorted)) * 0.9)
	if p90idx >= len(sorted) {
		p90idx = len(sorted) - 1
	}
	return metricStats{
		HasData:          true,
		Count:            len(sorted),
		IgnoredMalformed: ignored,
		Min:              sorted[0],
		Avg:              sum / float64(len(sorted)),
		P50:              median(sorted),
		P90:              sorted[p90idx],
		Max:              sorted[len(sorted)-1],
	}
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
