package main

import (
	"fmt"
	"strings"
)

type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Stream  string `json:"stream,omitempty"`
}

type CaptureMetadata struct {
	RequestedCount     int          `json:"requested_count"`
	Offset             int          `json:"offset"`
	SelectedCount      int          `json:"selected_count"`
	SelectionMode      string       `json:"selection_mode"`
	Complete           bool         `json:"complete"`
	ScannedStreams     int          `json:"scanned_streams"`
	FailedStreams      []string     `json:"failed_streams"`
	PartialInvocations int          `json:"partial_invocations"`
	Diagnostics        []Diagnostic `json:"diagnostics"`
}

type InvocationCompletion struct {
	Status     string `json:"status"`
	StartSeen  bool   `json:"start_seen"`
	ReportSeen bool   `json:"report_seen"`
}

func newInvocationCompletion(start, report bool) *InvocationCompletion {
	status := "partial"
	if start && report {
		status = "complete"
	}
	return &InvocationCompletion{Status: status, StartSeen: start, ReportSeen: report}
}

type GateResult struct {
	Name            string `json:"name"`
	Status          string `json:"status"`
	Code            string `json:"code,omitempty"`
	Message         string `json:"message,omitempty"`
	RequiredSamples int    `json:"required_samples,omitempty"`
	BaselineSamples int    `json:"baseline_samples"`
	NewSamples      int    `json:"new_samples"`
}

func applyComparisonGates(summary *compareSummary, a, b Snapshot, opts CompareOptions) {
	required := max(1, opts.MinInvocations)
	summary.Gates = []GateResult{}
	countGate := GateResult{Name: "sample_count", Status: "disabled", RequiredSamples: required, BaselineSamples: len(a.Invocations), NewSamples: len(b.Invocations)}
	if opts.MinInvocations > 0 {
		countGate.Status = "passed"
		if len(a.Invocations) < required || len(b.Invocations) < required {
			countGate.Status, countGate.Code = "insufficient_data", "insufficient_invocations"
			countGate.Message = fmt.Sprintf("need at least %d invocations on each side; got %d and %d", required, len(a.Invocations), len(b.Invocations))
		}
	}
	summary.Gates = append(summary.Gates, countGate)
	errorGate := GateResult{Name: "errors", Status: "disabled", RequiredSamples: required, BaselineSamples: len(a.Invocations), NewSamples: len(b.Invocations)}
	if opts.FailOnRegression {
		errorGate.Status = "passed"
		if len(a.Invocations) < required || len(b.Invocations) < required {
			errorGate.Status, errorGate.Code, errorGate.Message = "insufficient_data", "insufficient_error_samples", "not enough invocations to evaluate the error gate"
		} else if summary.Errors.NewCount > summary.Errors.BaselineCount || len(summary.Errors.NewPatterns) > 0 {
			errorGate.Status, errorGate.Code = "failed", "error_regression"
			errorGate.Message = strings.Join(regressionReasons(summary.Errors.BaselineCount, summary.Errors.NewCount, summary.Errors.NewPatterns), "; ")
		}
	}
	summary.Gates = append(summary.Gates, errorGate)
	for _, metric := range []struct {
		name, label, unit string
		comparison        metricComparison
		limit             float64
		before, after     float64
	}{
		{"duration", "p90 duration", "ms", summary.Duration, opts.MaxDurationRegressionPercent, summary.Duration.Baseline.P90, summary.Duration.New.P90},
		{"memory", "max memory used", "MB", summary.Memory, opts.MaxMemoryUsedRegressionPercent, summary.Memory.Baseline.Max, summary.Memory.New.Max},
	} {
		gate := GateResult{Name: metric.name, Status: "disabled", RequiredSamples: required, BaselineSamples: metric.comparison.Baseline.Count, NewSamples: metric.comparison.New.Count}
		if metric.limit > 0 {
			gate.Status = "passed"
			switch {
			case gate.BaselineSamples < required || gate.NewSamples < required:
				gate.Status, gate.Code = "insufficient_data", "insufficient_"+metric.name+"_samples"
				gate.Message = fmt.Sprintf("%s needs %d valid samples on each side; got %d and %d", metric.label, required, gate.BaselineSamples, gate.NewSamples)
			case !metric.comparison.RelativeChangeDefined:
				gate.Status, gate.Code, gate.Message = "insufficient_data", metric.name+"_relative_change_undefined", metric.label+" relative change cannot be evaluated: zero baseline or unrepresentable percentage"
			case metric.comparison.ObservedRegressionPercent > metric.limit:
				gate.Status, gate.Code = "failed", metric.name+"_regression"
				gate.Message = fmt.Sprintf("%s increased by %.1f%% (%.1f%s to %.1f%s), over %.1f%% limit", metric.label, metric.comparison.ObservedRegressionPercent, metric.before, metric.unit, metric.after, metric.unit, metric.limit)
				if metric.name == "memory" {
					gate.Message = fmt.Sprintf("max memory used increased by %.1f%% (%.0fMB to %.0fMB), over %.1f%% limit", metric.comparison.ObservedRegressionPercent, metric.before, metric.after, metric.limit)
				}
			}
		}
		summary.Gates = append(summary.Gates, gate)
	}
	completeGate := GateResult{Name: "capture_completeness", Status: "disabled"}
	if opts.RequireComplete {
		completeGate.Status = "passed"
		if !snapshotIsComplete(a) || !snapshotIsComplete(b) {
			completeGate.Status, completeGate.Code, completeGate.Message = "insufficient_data", "capture_incomplete", "both snapshots must have verified complete capture metadata"
		}
	}
	summary.Gates = append(summary.Gates, completeGate)
	summary.ExitCode = exitOK
	for _, gate := range summary.Gates {
		if gate.Status != "failed" && gate.Status != "insufficient_data" {
			continue
		}
		if gate.Status == "failed" {
			summary.ExitCode = exitRegression
			summary.Regression.Detected = true
		} else if summary.ExitCode == exitOK {
			summary.ExitCode = exitInsufficientData
		}
		summary.Regression.Reasons = appendUnique(summary.Regression.Reasons, gate.Message)
		summary.Regression.ReasonCodes = appendUnique(summary.Regression.ReasonCodes, gate.Code)
	}
	summary.Regression.WillFail = summary.ExitCode != exitOK
	summary.Status = statusForExit(summary.ExitCode)
	for _, side := range []struct {
		name string
		snap Snapshot
	}{{"baseline", a}, {"new", b}} {
		if len(side.snap.Invocations) == 0 {
			summary.Warnings = append(summary.Warnings, side.name+" snapshot has no invocations")
		}
		if side.snap.Capture != nil && !side.snap.Capture.Complete {
			summary.Warnings = append(summary.Warnings, side.name+" capture is incomplete")
			for _, diagnostic := range side.snap.Capture.Diagnostics {
				summary.Warnings = append(summary.Warnings, side.name+": "+diagnostic.Message)
			}
		}
	}
}

func snapshotIsComplete(snap Snapshot) bool {
	metadata := snap.Capture
	if metadata == nil || !metadata.Complete || metadata.RequestedCount <= 0 || metadata.SelectedCount != len(snap.Invocations) || metadata.SelectedCount != metadata.RequestedCount || metadata.Offset < 0 || metadata.PartialInvocations != 0 || len(metadata.Diagnostics) != 0 || len(metadata.FailedStreams) != 0 {
		return false
	}
	for _, inv := range snap.Invocations {
		if inv.Completion == nil || inv.Completion.Status != "complete" || !inv.Completion.StartSeen || !inv.Completion.ReportSeen {
			return false
		}
	}
	return true
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
