package main

import (
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

func parseInvocations(events []types.OutputLogEvent) []InvocationSummary {
	type invocationBuilder struct {
		requestID  string
		startTime  time.Time
		duration   string
		billedMs   string
		memUsedMB  string
		maxMemMB   string
		isError    bool
		errorLines []string
		logLines   []string
		hasReport  bool
	}

	builders := make(map[string]*invocationBuilder)
	var order []string

	for _, ev := range events {
		if ev.Message == nil || ev.Timestamp == nil {
			continue
		}
		msg := *ev.Message
		ts := time.UnixMilli(*ev.Timestamp)

		switch {
		case strings.HasPrefix(msg, "START RequestId: "):
			reqID := extractRequestID(msg, "START RequestId: ")
			if _, ok := builders[reqID]; !ok {
				builders[reqID] = &invocationBuilder{requestID: reqID, startTime: ts}
				order = append(order, reqID)
			}

		case strings.HasPrefix(msg, "END RequestId: "):
			// nothing extra needed

		case strings.HasPrefix(msg, "REPORT RequestId: "):
			reqID := extractRequestID(msg, "REPORT RequestId: ")
			b, ok := builders[reqID]
			if !ok {
				b = &invocationBuilder{requestID: reqID, startTime: ts}
				builders[reqID] = b
				order = append(order, reqID)
			}
			b.hasReport = true
			b.duration = extractField(msg, "Duration: ")
			b.billedMs = extractField(msg, "Billed Duration: ")
			b.memUsedMB = extractField(msg, "Memory Size: ")
			b.maxMemMB = extractField(msg, "Max Memory Used: ")

		default:
			if len(order) > 0 {
				currentReqID := order[len(order)-1]
				b := builders[currentReqID]
				trimmed := strings.TrimSpace(msg)
				if trimmed == "" {
					break
				}
				b.logLines = append(b.logLines, trimmed)
				lower := strings.ToLower(trimmed)
				if strings.Contains(lower, "error") ||
					strings.Contains(lower, "panic") ||
					strings.Contains(lower, "fatal") ||
					strings.Contains(lower, "traceback") ||
					strings.Contains(lower, "exception") {
					b.isError = true
					b.errorLines = append(b.errorLines, trimmed)
				}
			}
		}
	}

	var results []InvocationSummary
	for _, reqID := range order {
		b := builders[reqID]
		if !b.hasReport {
			continue
		}
		results = append(results, InvocationSummary{
			RequestID:  b.requestID,
			StartTime:  b.startTime,
			Duration:   b.duration,
			BilledMs:   b.billedMs,
			MemUsedMB:  b.memUsedMB,
			MaxMemMB:   b.maxMemMB,
			IsError:    b.isError,
			ErrorLines: b.errorLines,
			LogLines:   b.logLines,
		})
	}
	return results
}

func extractRequestID(line, prefix string) string {
	after := strings.TrimPrefix(line, prefix)
	parts := strings.Fields(after)
	if len(parts) > 0 {
		return parts[0]
	}
	return after
}

func extractField(report, fieldName string) string {
	idx := strings.Index(report, fieldName)
	if idx < 0 {
		return ""
	}
	rest := report[idx+len(fieldName):]
	end := strings.IndexAny(rest, "\t\n")
	if end < 0 {
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(rest[:end])
}

func normalizeLogLine(line string) string {
	result := collapseHexRuns(line, 32, "<UUID>")
	if len(result) > 100 {
		result = result[:100]
	}
	return strings.TrimSpace(result)
}

func collapseHexRuns(s string, minLen int, replacement string) string {
	isHexDash := func(r rune) bool {
		return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') || r == '-'
	}

	var result strings.Builder
	runes := []rune(s)
	runStart := -1

	for i, r := range runes {
		if isHexDash(r) {
			if runStart < 0 {
				runStart = i
			}
		} else {
			if runStart >= 0 {
				if i-runStart >= minLen {
					result.WriteString(replacement)
				} else {
					result.WriteString(string(runes[runStart:i]))
				}
				runStart = -1
			}
			result.WriteRune(r)
		}
	}
	if runStart >= 0 {
		if len(runes)-runStart >= minLen {
			result.WriteString(replacement)
		} else {
			result.WriteString(string(runes[runStart:]))
		}
	}
	return result.String()
}
