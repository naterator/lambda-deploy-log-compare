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
	ctx := context.Background()
	needed := offset + count

	if offset > 0 {
		fmt.Printf("Capturing logs for %s (log group: %s, offset: %d, count: %d) ...\n", funcName, logGroup, offset, count)
	} else {
		fmt.Printf("Capturing logs for %s (log group: %s) ...\n", funcName, logGroup)
	}

	// Fetch log streams ordered by last event time (most recent first).
	streamCtx, streamCancel := context.WithTimeout(ctx, 2*time.Minute)
	streams, err := fetchLogStreams(streamCtx, client, logGroup, needed+50)
	streamCancel()
	if err != nil {
		return fmt.Errorf("describe-log-streams: %w", err)
	}
	if len(streams) == 0 {
		fmt.Printf("  No log streams found for %s\n", logGroup)
		return nil
	}
	fmt.Printf("  Found %d log streams\n", len(streams))

	// Collect invocations from streams until we have enough.
	var allInvocations []InvocationSummary
	for i, stream := range streams {
		if i > 0 && i%20 == 0 {
			fmt.Printf("  Processing stream %d/%d (collected %d invocations so far) ...\n", i, len(streams), len(allInvocations))
		}

		evCtx, evCancel := context.WithTimeout(ctx, 30*time.Second)
		events, err := fetchLogEvents(evCtx, client, logGroup, *stream.LogStreamName)
		evCancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: failed to get events from stream %s: %v\n", *stream.LogStreamName, err)
			continue
		}

		invocations := parseInvocations(events)
		allInvocations = append(allInvocations, invocations...)

		if len(allInvocations) >= needed {
			fmt.Printf("  Collected enough invocations (%d >= %d), stopping early at stream %d/%d\n", len(allInvocations), needed, i+1, len(streams))
			break
		}
	}

	// Sort by start time descending (most recent first)
	sort.Slice(allInvocations, func(i, j int) bool {
		return allInvocations[i].StartTime.After(allInvocations[j].StartTime)
	})

	// Apply offset: skip the most recent `offset` invocations, then take `count`
	if offset > 0 {
		if offset >= len(allInvocations) {
			fmt.Printf("  Warning: only found %d invocations, but offset is %d — no invocations to capture\n", len(allInvocations), offset)
			allInvocations = nil
		} else {
			allInvocations = allInvocations[offset:]
		}
	}
	if len(allInvocations) > count {
		allInvocations = allInvocations[:count]
	}

	fmt.Printf("  Selected %d invocations\n", len(allInvocations))

	var records []InvocationRecord
	for _, inv := range allInvocations {
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
	if err := os.WriteFile(outPath, data, 0644); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}

	fmt.Printf("  Wrote snapshot to %s (%d invocations)\n", outPath, len(records))
	return nil
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

		out, err := client.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
			LogGroupName: &logGroup,
			OrderBy:      types.OrderByLastEventTime,
			Descending:   aws.Bool(true),
			Limit:        aws.Int32(pageLimit),
			NextToken:    nextToken,
		})
		if err != nil {
			return allStreams, err
		}
		allStreams = append(allStreams, out.LogStreams...)
		nextToken = out.NextToken
		if nextToken == nil || len(out.LogStreams) == 0 {
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

func toRecord(inv InvocationSummary) InvocationRecord {
	return InvocationRecord{
		RequestID:  inv.RequestID,
		Timestamp:  inv.StartTime.UTC().Format(time.RFC3339),
		Duration:   inv.Duration,
		BilledMs:   inv.BilledMs,
		MemUsedMB:  inv.MemUsedMB,
		MaxMemMB:   inv.MaxMemMB,
		IsError:    inv.IsError,
		ErrorLines: inv.ErrorLines,
		LogLines:   inv.LogLines,
	}
}
