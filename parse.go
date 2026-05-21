package main

import (
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

var (
	requestIDPattern   = regexp.MustCompile(`(?i)\b(request[_-]?id|requestid)\b([":'\s]*[:=]\s*["']?)[A-Za-z0-9._:/+=-]+`)
	arnPattern         = regexp.MustCompile(`arn:aws[a-zA-Z-]*:[^\s,;\]\)"]+`)
	rfc3339TimePattern = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}[T ][0-9:.]+(?:Z|[+-]\d{2}:?\d{2})?\b`)
	slashTimePattern   = regexp.MustCompile(`\b\d{4}/\d{2}/\d{2}\s+\d{2}:\d{2}:\d{2}(?:\.\d+)?\b`)
	durationPattern    = regexp.MustCompile(`\b\d+(?:\.\d+)?\s*(?:ms|msec|s|sec|secs|seconds)\b`)
	longNumberPattern  = regexp.MustCompile(`\b\d{6,}\b`)
)

func parseInvocations(events []types.OutputLogEvent) []InvocationSummary {
	type invocationBuilder struct {
		requestID       string
		startTime       time.Time
		duration        string
		billedMs        string
		memorySizeMB    string
		maxMemoryUsedMB string
		isError         bool
		errorLines      []string
		logLines        []string
		hasStart        bool
		hasReport       bool
	}

	builders := make(map[string]*invocationBuilder)
	var order []string
	currentReqID := ""

	ensureBuilder := func(reqID string, ts time.Time) *invocationBuilder {
		if reqID == "" {
			return nil
		}
		b, ok := builders[reqID]
		if !ok {
			b = &invocationBuilder{requestID: reqID, startTime: ts}
			builders[reqID] = b
			order = append(order, reqID)
			return b
		}
		if b.startTime.IsZero() || ts.Before(b.startTime) {
			b.startTime = ts
		}
		return b
	}

	appendLogLine := func(b *invocationBuilder, msg string) {
		if b == nil {
			return
		}
		trimmed := strings.TrimSpace(msg)
		if trimmed == "" {
			return
		}
		b.logLines = append(b.logLines, trimmed)
		if lineLooksLikeError(trimmed) {
			b.isError = true
			b.errorLines = append(b.errorLines, trimmed)
		}
	}

	builderForLog := func(msg string, ts time.Time) *invocationBuilder {
		if reqID := extractInlineRequestID(msg); reqID != "" {
			return ensureBuilder(reqID, ts)
		}
		if currentReqID != "" {
			return ensureBuilder(currentReqID, ts)
		}
		if len(order) == 0 {
			return nil
		}
		return builders[order[len(order)-1]]
	}

	for _, ev := range events {
		if ev.Message == nil || ev.Timestamp == nil {
			continue
		}
		msg := strings.TrimSpace(*ev.Message)
		ts := time.UnixMilli(*ev.Timestamp)

		switch {
		case strings.HasPrefix(msg, "START RequestId: "):
			reqID := extractRequestID(msg, "START RequestId: ")
			if b := ensureBuilder(reqID, ts); b != nil {
				b.startTime = ts
				b.hasStart = true
			}
			currentReqID = reqID

		case strings.HasPrefix(msg, "END RequestId: "):
			currentReqID = extractRequestID(msg, "END RequestId: ")

		case strings.HasPrefix(msg, "REPORT RequestId: "):
			reqID := extractRequestID(msg, "REPORT RequestId: ")
			b := ensureBuilder(reqID, ts)
			if b == nil {
				continue
			}
			b.hasReport = true
			b.duration = extractField(msg, "Duration: ")
			b.billedMs = extractField(msg, "Billed Duration: ")
			b.memorySizeMB = extractField(msg, "Memory Size: ")
			b.maxMemoryUsedMB = extractField(msg, "Max Memory Used: ")
			currentReqID = reqID

		default:
			appendLogLine(builderForLog(msg, ts), msg)
		}
	}

	var results []InvocationSummary
	for _, reqID := range order {
		b := builders[reqID]
		if !b.hasReport && !b.hasStart && len(b.logLines) == 0 {
			continue
		}
		results = append(results, InvocationSummary{
			RequestID:       b.requestID,
			StartTime:       b.startTime,
			Duration:        b.duration,
			BilledMs:        b.billedMs,
			MemorySizeMB:    b.memorySizeMB,
			MaxMemoryUsedMB: b.maxMemoryUsedMB,
			IsError:         b.isError,
			ErrorLines:      b.errorLines,
			LogLines:        b.logLines,
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
	return strings.TrimSpace(after)
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

func extractInlineRequestID(line string) string {
	for _, prefix := range []string{"RequestId: ", "RequestID: ", "requestId: ", "requestID: "} {
		if idx := strings.Index(line, prefix); idx >= 0 {
			return extractRequestID(line[idx:], prefix)
		}
	}
	return ""
}

func lineLooksLikeError(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	if lower == "" || isBenignErrorLine(lower) {
		return false
	}

	for _, token := range []string{
		"[error]",
		"panic:",
		"fatal:",
		"fatal ",
		"traceback",
		"exception",
		"runtime.exiterror",
		"task timed out after",
		"process exited before completing request",
	} {
		if strings.Contains(lower, token) {
			return true
		}
	}

	for _, token := range []string{
		" level=error",
		"\tlevel=error",
		"\"level\":\"error\"",
		"\"level\": \"error\"",
		" error:",
		" error ",
	} {
		if strings.Contains(lower, token) {
			return true
		}
	}

	return strings.HasPrefix(lower, "error:") ||
		strings.HasPrefix(lower, "error ") ||
		strings.HasSuffix(lower, " error")
}

func isBenignErrorLine(lower string) bool {
	for _, phrase := range []string{
		"no error",
		"no errors",
		"without error",
		"without errors",
		"error_count",
		"error count",
		"errors=0",
		"error=0",
		"errors: 0",
		"error: 0",
		"error rate",
		"error budget",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

func normalizeLogLine(line string) string {
	result := strings.TrimSpace(line)
	result = requestIDPattern.ReplaceAllString(result, `${1}${2}<REQUEST_ID>`)
	result = arnPattern.ReplaceAllString(result, "<ARN>")
	result = rfc3339TimePattern.ReplaceAllString(result, "<TIMESTAMP>")
	result = slashTimePattern.ReplaceAllString(result, "<TIMESTAMP>")
	result = durationPattern.ReplaceAllString(result, "<DURATION>")
	result = collapseHexRuns(result, 32, "<UUID>")
	result = longNumberPattern.ReplaceAllString(result, "<NUMBER>")
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
