package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

type LogsClient interface {
	DescribeLogStreams(ctx context.Context, params *cloudwatchlogs.DescribeLogStreamsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogStreamsOutput, error)
	GetLogEvents(ctx context.Context, params *cloudwatchlogs.GetLogEventsInput, optFns ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error)
}

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
			events, err := fetchLogEvents(evCtx, client, logGroup, *stream.LogStreamName)
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

	outPath := filepath.Join(outDir, fmt.Sprintf("%s_%s.json", funcName, label))
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

func fetchLogStreams(ctx context.Context, client LogsClient, logGroup string, limit int) ([]types.LogStream, error) {
	var allStreams []types.LogStream
	var nextToken *string

	for {
		pageLimit := int32(50)
		remaining := limit - len(allStreams)
		if remaining <= 0 {
			break
		}
		if remaining < int(pageLimit) {
			pageLimit = int32(remaining)
		}

		streams, newToken, err := fetchLogStreamPage(ctx, client, logGroup, pageLimit, nextToken)
		if err != nil {
			return allStreams, err
		}
		allStreams = append(allStreams, streams...)
		nextToken = newToken
		if nextToken == nil || len(streams) == 0 {
			break
		}
	}
	return allStreams, nil
}

func fetchLogEvents(ctx context.Context, client LogsClient, logGroup, streamName string) ([]types.OutputLogEvent, error) {
	var allEvents []types.OutputLogEvent
	var nextToken *string

	for {
		out, err := client.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
			LogGroupName:  &logGroup,
			LogStreamName: &streamName,
			StartFromHead: aws.Bool(true),
			NextToken:     nextToken,
		})
		if err != nil {
			return allEvents, err
		}
		allEvents = append(allEvents, out.Events...)

		// GetLogEvents always returns a nextForwardToken; it stops when
		// the token is the same as the one we sent in.
		if out.NextForwardToken == nil || (nextToken != nil && *out.NextForwardToken == *nextToken) {
			break
		}
		nextToken = out.NextForwardToken
	}
	return allEvents, nil
}

func selectInvocations(allInvocations []InvocationSummary, count, offset int) []InvocationSummary {
	sort.Slice(allInvocations, func(i, j int) bool {
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
