package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

type LogsClient interface {
	DescribeLogStreams(ctx context.Context, params *cloudwatchlogs.DescribeLogStreamsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogStreamsOutput, error)
	GetLogEvents(ctx context.Context, params *cloudwatchlogs.GetLogEventsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error)
}

const logEventsPageLimit int32 = 10_000

func logGroupForFunction(name string) string {
	return "/aws/lambda/" + name
}

func runCapture(client LogsClient, funcName, logGroup string, count, offset int, label, outDir string) error {
	if count <= 0 {
		return fmt.Errorf("count must be greater than 0")
	}
	if offset < 0 {
		return fmt.Errorf("offset must be greater than or equal to 0")
	}

	ctx := context.Background()
	needed := offset + count

	if offset > 0 {
		fmt.Fprintf(stdout, "Capturing logs for %s (log group: %s, offset: %d, count: %d) ...\n", funcName, logGroup, offset, count)
	} else {
		fmt.Fprintf(stdout, "Capturing logs for %s (log group: %s) ...\n", funcName, logGroup)
	}

	var allInvocations []InvocationSummary
	var streamCount int
	var processedStreams int
	var nextToken *string

	for len(allInvocations) < needed {
		streamCtx, streamCancel := context.WithTimeout(ctx, 2*time.Minute)
		streams, newToken, err := fetchLogStreamPage(streamCtx, client, logGroup, 50, nextToken)
		streamCancel()
		if err != nil {
			return fmt.Errorf("describe-log-streams: %w", err)
		}
		if len(streams) == 0 {
			break
		}

		startIdx := streamCount + 1
		streamCount += len(streams)
		fmt.Fprintf(stdout, "  Scanning streams %d-%d (collected %d invocations so far) ...\n", startIdx, streamCount, len(allInvocations))

		for _, stream := range streams {
			if stream.LogStreamName == nil {
				continue
			}
			processedStreams++

			evCtx, evCancel := context.WithTimeout(ctx, 30*time.Second)
			events, err := fetchLogEvents(evCtx, client, logGroup, *stream.LogStreamName, needed-len(allInvocations))
			evCancel()
			if err != nil {
				fmt.Fprintf(stderr, "  Warning: failed to get events from stream %s: %v\n", *stream.LogStreamName, err)
				continue
			}

			invocations := parseInvocations(events)
			allInvocations = append(allInvocations, invocations...)

			if len(allInvocations) >= needed {
				fmt.Fprintf(stdout, "  Collected enough invocations (%d >= %d), stopping early after stream %d\n", len(allInvocations), needed, processedStreams)
				break
			}
		}

		nextToken = newToken
		if nextToken == nil {
			break
		}
	}
	if streamCount == 0 {
		fmt.Fprintf(stdout, "  No log streams found for %s\n", logGroup)
		return nil
	}
	fmt.Fprintf(stdout, "  Scanned %d log streams\n", streamCount)

	selectedInvocations := selectInvocations(allInvocations, count, offset)
	if offset > 0 && len(selectedInvocations) == 0 {
		fmt.Fprintf(stdout, "  Warning: only found %d invocations, but offset is %d - no invocations to capture\n", len(allInvocations), offset)
	}

	fmt.Fprintf(stdout, "  Selected %d invocations\n", len(selectedInvocations))

	var records []InvocationRecord
	for _, inv := range selectedInvocations {
		records = append(records, toRecord(inv))
	}

	snapshot := Snapshot{
		FunctionName: funcName,
		LogGroup:     logGroup,
		CapturedAt:   time.Now().UTC().Format(time.RFC3339),
		Label:        label,
		Invocations:  records,
	}

	outPath := filepath.Join(outDir, snapshotFileName(funcName, label))
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if err := os.WriteFile(outPath, data, 0644); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}

	fmt.Fprintf(stdout, "  Wrote snapshot to %s (%d invocations)\n", outPath, len(records))
	return nil
}

func fetchLogStreamPage(ctx context.Context, client LogsClient, logGroup string, limit int32, nextToken *string) ([]types.LogStream, *string, error) {
	out, err := client.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: &logGroup,
		OrderBy:      types.OrderByLastEventTime,
		Descending:   aws.Bool(true),
		Limit:        aws.Int32(limit),
		NextToken:    nextToken,
	})
	if err != nil {
		return nil, nil, err
	}
	return out.LogStreams, out.NextToken, nil
}

func fetchLogEvents(ctx context.Context, client LogsClient, logGroup, streamName string, neededInvocations int) ([]types.OutputLogEvent, error) {
	if neededInvocations <= 0 {
		neededInvocations = 1
	}

	var eventPages [][]types.OutputLogEvent
	var nextToken *string
	tracker := newInvocationCompletenessTracker()

	for {
		out, err := client.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
			LogGroupName:  &logGroup,
			LogStreamName: &streamName,
			Limit:         aws.Int32(logEventsPageLimit),
			StartFromHead: aws.Bool(false),
			NextToken:     nextToken,
		})
		if err != nil {
			return flattenLogEventPages(eventPages), err
		}

		// When the backward token stops moving, the API has no older events to page in.
		// Some terminal responses can repeat the current position, so skip appending them.
		if nextToken != nil && out.NextBackwardToken != nil && *out.NextBackwardToken == *nextToken {
			break
		}

		if len(out.Events) > 0 {
			pageEvents := append([]types.OutputLogEvent(nil), out.Events...)
			reverseLogEvents(pageEvents)
			eventPages = append(eventPages, pageEvents)
			tracker.addPage(pageEvents)

			if tracker.count() >= neededInvocations {
				break
			}
		}

		if out.NextBackwardToken == nil {
			break
		}
		nextToken = out.NextBackwardToken
	}
	return flattenLogEventPages(eventPages), nil
}

func flattenLogEventPages(pages [][]types.OutputLogEvent) []types.OutputLogEvent {
	total := 0
	for _, page := range pages {
		total += len(page)
	}

	events := make([]types.OutputLogEvent, 0, total)
	for i := len(pages) - 1; i >= 0; i-- {
		events = append(events, pages[i]...)
	}
	return events
}

type invocationCompletenessTracker struct {
	seen map[string]struct{}
}

func newInvocationCompletenessTracker() *invocationCompletenessTracker {
	return &invocationCompletenessTracker{seen: make(map[string]struct{})}
}

func (t *invocationCompletenessTracker) addPage(events []types.OutputLogEvent) {
	currentReqID := ""

	for _, ev := range events {
		if ev.Message == nil || ev.Timestamp == nil {
			continue
		}
		msg := strings.TrimSpace(*ev.Message)

		switch {
		case strings.HasPrefix(msg, "START RequestId: "):
			currentReqID = extractRequestID(msg, "START RequestId: ")
			t.mark(currentReqID)

		case strings.HasPrefix(msg, "END RequestId: "):
			currentReqID = extractRequestID(msg, "END RequestId: ")

		case strings.HasPrefix(msg, "REPORT RequestId: "):
			currentReqID = extractRequestID(msg, "REPORT RequestId: ")
			t.mark(currentReqID)

		default:
			if reqID := extractInlineRequestID(msg); reqID != "" {
				t.mark(reqID)
				continue
			}
			t.mark(currentReqID)
		}
	}
}

func (t *invocationCompletenessTracker) mark(reqID string) {
	if reqID == "" {
		return
	}
	t.seen[reqID] = struct{}{}
}

func (t *invocationCompletenessTracker) count() int {
	return len(t.seen)
}

func reverseLogEvents(events []types.OutputLogEvent) {
	for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
		events[left], events[right] = events[right], events[left]
	}
}

func snapshotFileName(funcName, label string) string {
	return fmt.Sprintf("%s_%s.json", sanitizeFileComponent(funcName, "function"), sanitizeFileComponent(label, "snapshot"))
}

func sanitizeFileComponent(raw, fallback string) string {
	raw = strings.TrimSpace(raw)

	var b strings.Builder
	lastUnderscore := false
	for _, r := range raw {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '.', r == '-', r == '_':
			b.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}

	sanitized := strings.Trim(b.String(), "._-")
	if sanitized == "" || sanitized == "." || sanitized == ".." {
		return fallback
	}
	return sanitized
}

func selectInvocations(allInvocations []InvocationSummary, count, offset int) []InvocationSummary {
	sort.SliceStable(allInvocations, func(i, j int) bool {
		if allInvocations[i].StartTime.Equal(allInvocations[j].StartTime) {
			return allInvocations[i].RequestID < allInvocations[j].RequestID
		}
		return allInvocations[i].StartTime.After(allInvocations[j].StartTime)
	})

	if offset > 0 {
		if offset >= len(allInvocations) {
			return nil
		}
		allInvocations = allInvocations[offset:]
	}
	if len(allInvocations) > count {
		allInvocations = allInvocations[:count]
	}
	return allInvocations
}

func toRecord(inv InvocationSummary) InvocationRecord {
	return InvocationRecord{
		RequestID:       inv.RequestID,
		Timestamp:       inv.StartTime.UTC().Format(time.RFC3339),
		Duration:        inv.Duration,
		BilledMs:        inv.BilledMs,
		MemorySizeMB:    inv.MemorySizeMB,
		MaxMemoryUsedMB: inv.MaxMemoryUsedMB,
		IsError:         inv.IsError,
		ErrorLines:      inv.ErrorLines,
		LogLines:        inv.LogLines,
	}
}
