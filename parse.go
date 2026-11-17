package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

var (
	requestIDPattern     = regexp.MustCompile(`(?i)\b(request[_-]?id|requestid)\b([":'\s]*[:=]\s*["']?)[A-Za-z0-9._:/+=-]+`)
	arnPattern           = regexp.MustCompile(`arn:aws[a-zA-Z-]*:[^\s,;\]\)"]+`)
	rfc3339TimePattern   = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}[T ][0-9:.]+(?:Z|[+-]\d{2}:?\d{2})?\b`)
	slashTimePattern     = regexp.MustCompile(`\b\d{4}/\d{2}/\d{2}\s+\d{2}:\d{2}:\d{2}(?:\.\d+)?\b`)
	durationPattern      = regexp.MustCompile(`\b\d+(?:\.\d+)?\s*(?:ms|msec|s|sec|secs|seconds)\b`)
	longNumberPattern    = regexp.MustCompile(`\b\d{6,}\b`)
	reportFieldPattern   = regexp.MustCompile(`(?:^|\s)(RequestId|Billed Duration|Init Duration|Restore Duration|Duration|Memory Size|Max Memory Used|Status|Error Type|ErrorType|Version|XRAY TraceId|SegmentId|Sampled):\s*`)
	severityPattern      = regexp.MustCompile(`(?i)(?:^|[\s,{])(?:["']?(?:level|severity)["']?\s*[:=]\s*["']?(?:error|fatal|panic|critical)\b)|\[(?:error|fatal|panic|critical)\]|(?:^|[\s;])(?:error|panic|fatal|critical):`)
	errorCounterPattern  = regexp.MustCompile(`(?i)\b(?:error_count|error count|errors?)\s*[:=]\s*([0-9]+(?:\.[0-9]+)?)\b`)
	benignCounterPattern = regexp.MustCompile(`^(?:info[:\s]+)?(?:error_count|error count|errors?|error rate)\s*[:=]\s*0(?:\.0+)?\s*%?\s*$`)
	benignPhrasePattern  = regexp.MustCompile(`^(?:no errors?|without errors?)(?: detected(?: in prior batch)?| occurred| found)?[.!]?$`)
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
			if reason := reportFailureReason(extractField(msg, "Status: "), firstNonEmpty(extractField(msg, "Error Type: "), extractField(msg, "ErrorType: "))); reason != "" {
				b.isError = true
				b.errorLines = append(b.errorLines, reason)
			}
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
			HasStart:        b.hasStart,
			HasReport:       b.hasReport,
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
	wanted := strings.TrimSuffix(strings.TrimSpace(fieldName), ":")
	fields := reportFieldPattern.FindAllStringSubmatchIndex(report, -1)
	for i, field := range fields {
		if report[field[2]:field[3]] != wanted {
			continue
		}
		end := len(report)
		if i+1 < len(fields) {
			end = fields[i+1][0]
		}
		return strings.TrimSpace(report[field[1]:end])
	}
	return ""
}

func reportFailureReason(status, errorType string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	if status != "error" && status != "timeout" && status != "failed" && errorType == "" {
		return ""
	}
	if status == "" {
		status = "error"
	}
	reason := fmt.Sprintf("Lambda invocation failed: status=%s", status)
	if errorType != "" {
		reason += " error_type=" + errorType
	}
	return reason
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
	if lower == "" || benignCounterPattern.MatchString(lower) {
		return false
	}
	// Explicit severity always wins over counter or benign wording elsewhere.
	if severityPattern.MatchString(lower) {
		return true
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
	for _, match := range errorCounterPattern.FindAllStringSubmatch(lower, -1) {
		if count, err := strconv.ParseFloat(match[1], 64); err == nil && count > 0 {
			return true
		}
	}
	if isBenignErrorLine(lower) {
		return false
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
	return benignCounterPattern.MatchString(lower) || benignPhrasePattern.MatchString(lower)
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
