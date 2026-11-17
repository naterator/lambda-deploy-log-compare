package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

type cloudWatchFixturePage struct {
	StartFromHead bool                  `json:"start_from_head"`
	Limit         int32                 `json:"limit"`
	NextToken     *string               `json:"next_token"`
	Response      fixtureEventsResponse `json:"response"`
}

type fixtureEventsResponse struct {
	Events            []types.OutputLogEvent `json:"events"`
	NextBackwardToken *string                `json:"next_backward_token"`
}

type cloudWatchFixture struct {
	Provenance string                  `json:"provenance"`
	RecordedAt string                  `json:"recorded_at"`
	SourceHash string                  `json:"source_sha256"`
	Region     string                  `json:"region"`
	Pages      []cloudWatchFixturePage `json:"pages"`
}

// Opt-in only: reads a user-selected test stream, stores sanitized service
// responses, and never creates logs, invokes Lambda, or changes AWS resources.
func TestRecordLiveCloudWatchFixture(t *testing.T) {
	group, stream := os.Getenv("LAMBDA_FIXTURE_LOG_GROUP"), os.Getenv("LAMBDA_FIXTURE_LOG_STREAM")
	if group == "" || stream == "" {
		t.Skip("set LAMBDA_FIXTURE_LOG_GROUP and LAMBDA_FIXTURE_LOG_STREAM for a live fixture")
	}
	region, output := os.Getenv("LAMBDA_FIXTURE_REGION"), os.Getenv("LAMBDA_FIXTURE_OUT")
	if region == "" || output == "" {
		t.Fatal("LAMBDA_FIXTURE_REGION and LAMBDA_FIXTURE_OUT are required")
	}
	client, err := newLogsClient(region, os.Getenv("LAMBDA_FIXTURE_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	fixture := cloudWatchFixture{Provenance: "live AWS GetLogEvents; messages and identifiers sanitized", RecordedAt: time.Now().UTC().Format(time.RFC3339), Region: region}
	fixture.SourceHash = fmt.Sprintf("%x", sha256.Sum256([]byte(group+"\n"+stream)))
	var token *string
	for i := 0; i < 8; i++ {
		response, err := client.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{LogGroupName: &group, LogStreamName: &stream, Limit: aws.Int32(100), StartFromHead: aws.Bool(false), NextToken: token})
		if err != nil {
			t.Fatal(err)
		}
		fixture.Pages = append(fixture.Pages, cloudWatchFixturePage{Limit: 100, NextToken: token, Response: fixtureEventsResponse{Events: response.Events, NextBackwardToken: response.NextBackwardToken}})
		if response.NextBackwardToken == nil || token != nil && *token == *response.NextBackwardToken {
			break
		}
		token = response.NextBackwardToken
	}
	sanitizeCloudWatchFixture(&fixture)
	starts := map[string]bool{}
	reports := map[string]bool{}
	for _, page := range fixture.Pages {
		for _, event := range page.Response.Events {
			message := aws.ToString(event.Message)
			if strings.HasPrefix(message, "START RequestId: ") {
				starts[extractRequestID(message, "START RequestId: ")] = true
			}
			if strings.HasPrefix(message, "REPORT RequestId: ") {
				reports[extractRequestID(message, "REPORT RequestId: ")] = true
			}
		}
	}
	complete := 0
	for id := range starts {
		if reports[id] {
			complete++
		}
	}
	if complete < 2 {
		t.Fatal("test stream must contain at least two text START/REPORT invocation sequences within 800 events")
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeSnapshotOutputFile(output, data, false); err != nil {
		t.Fatal(err)
	}
	t.Logf("Saved sanitized service fixture to %s; run TestRecordedCloudWatchOrdering to check the production reader", output)
}

func sanitizeCloudWatchFixture(fixture *cloudWatchFixture) {
	ids, tokens := map[string]string{}, map[string]string{}
	mapValue := func(values map[string]string, raw, prefix string) string {
		if result, ok := values[raw]; ok {
			return result
		}
		result := fmt.Sprintf("%s-%03d", prefix, len(values)+1)
		values[raw] = result
		return result
	}
	mapToken := func(raw *string) *string {
		if raw == nil {
			return nil
		}
		return aws.String(mapValue(tokens, *raw, "token"))
	}
	for i := range fixture.Pages {
		page := &fixture.Pages[i]
		page.NextToken = mapToken(page.NextToken)
		page.Response.NextBackwardToken = mapToken(page.Response.NextBackwardToken)
		for j := range page.Response.Events {
			event := &page.Response.Events[j]
			if event.Message == nil {
				continue
			}
			message := strings.TrimSpace(*event.Message)
			sanitized := "fixture application log"
			if lineLooksLikeError(message) {
				sanitized = "ERROR: fixture application failure"
			}
			if id := extractInlineRequestID(message); id != "" {
				sanitized += " RequestId: " + mapValue(ids, id, "request")
			}
			for _, kind := range []string{"START", "END", "REPORT"} {
				prefix := kind + " RequestId: "
				if !strings.HasPrefix(message, prefix) {
					continue
				}
				sanitized = prefix + mapValue(ids, extractRequestID(message, prefix), "request")
				if kind == "START" {
					sanitized += " Version: $LATEST"
				}
				if kind == "REPORT" {
					for _, field := range []string{"Duration", "Billed Duration", "Memory Size", "Max Memory Used"} {
						value := extractField(message, field+": ")
						if number, valid := parseNumberWithSuffixes(value, " ms", " MB"); valid {
							unit := " ms"
							if strings.Contains(field, "Memory") {
								unit = " MB"
							}
							sanitized += "\t" + field + ": " + strconv.FormatFloat(number, 'g', -1, 64) + unit
						}
					}
					status := extractField(message, "Status: ")
					if status == "error" || status == "timeout" || status == "success" {
						sanitized += "\tStatus: " + status
					}
				}
			}
			event.Message = aws.String(sanitized)
		}
	}
}

func TestRecordedCloudWatchOrdering(t *testing.T) {
	paths, err := filepath.Glob("testdata/cloudwatch/live-*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Skip("no live-service fixture has been recorded yet")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var fixture cloudWatchFixture
			if err := json.Unmarshal(data, &fixture); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(fixture.Provenance, "live AWS") || len(fixture.Pages) == 0 {
				t.Fatal("missing service provenance/pages")
			}
			index := 0
			client := &mockLogsClient{getLogEventsFn: func(_ context.Context, request *cloudwatchlogs.GetLogEventsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
				if index == len(fixture.Pages) {
					return &cloudwatchlogs.GetLogEventsOutput{NextBackwardToken: request.NextToken}, nil
				}
				page := fixture.Pages[index]
				index++
				return &cloudwatchlogs.GetLogEventsOutput{Events: page.Response.Events, NextBackwardToken: page.Response.NextBackwardToken}, nil
			}}
			events, err := fetchLogEvents(context.Background(), client, "fixture-group", "fixture-stream", 2)
			if err != nil {
				t.Fatal(err)
			}
			for i := 1; i < len(events); i++ {
				if aws.ToInt64(events[i].Timestamp) < aws.ToInt64(events[i-1].Timestamp) {
					t.Fatal("production reader returned nonchronological events; inspect service ordering before changing reversal")
				}
			}
			if len(parseInvocations(events)) < 2 {
				t.Fatal("reader lost invocation boundaries")
			}
		})
	}
}

func TestServiceFixtureSanitization(t *testing.T) {
	fixture := cloudWatchFixture{Pages: []cloudWatchFixturePage{{Response: fixtureEventsResponse{NextBackwardToken: aws.String("sensitive-token"), Events: []types.OutputLogEvent{
		{Timestamp: aws.Int64(10), Message: aws.String("START RequestId: secret-request Version: sensitive-version")},
		{Timestamp: aws.Int64(11), Message: aws.String("ERROR: secret payload RequestId: secret-request")},
		{Timestamp: aws.Int64(12), Message: aws.String("REPORT RequestId: secret-request\tDuration: 20 ms\tMax Memory Used: 40 MB\tStatus: timeout")},
	}}}}}
	sanitizeCloudWatchFixture(&fixture)
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "sensitive") {
		t.Fatalf("fixture leaked content: %s", data)
	}
	if len(fixture.Pages[0].Response.Events) != 3 || aws.ToInt64(fixture.Pages[0].Response.Events[0].Timestamp) != 10 {
		t.Fatal("sanitization altered event order/timing")
	}
	invocations := parseInvocations(fixture.Pages[0].Response.Events)
	if len(invocations) != 1 || !invocations[0].IsError || invocations[0].Duration != "20 ms" {
		t.Fatalf("lost parser evidence: %+v", invocations)
	}
}
