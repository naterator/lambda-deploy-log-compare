package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

func TestFailureEvidenceSurvivesBenignWording(t *testing.T) {
	for _, line := range []string{
		"ERROR: request failed; error_count=1", "[ERROR] request failed; errors=0",
		"no error; error: database failed", "level=error message=failed",
		`{"level" : "ERROR", "message":"error budget exhausted"}`,
		"fatal: no errors previously", "error_count=4", "error count=1",
		"error budget exhausted", "error rate exceeded",
	} {
		if !lineLooksLikeError(line) {
			t.Errorf("missed error %q", line)
		}
	}
	for _, line := range []string{"error_count=0", "errors=0", "errors: 0", "error: 0", "no errors detected in prior batch", "without error", "error rate=0%", "ordinary log"} {
		if lineLooksLikeError(line) {
			t.Errorf("false error %q", line)
		}
	}
}

func TestReportFieldsMatchWholeNames(t *testing.T) {
	for _, separator := range []string{"\t", " ", "\n"} {
		report := strings.Join([]string{"REPORT RequestId: req", "Billed Duration: 20 ms", "Init Duration: 8 ms", "Memory Size: 128 MB", "Status: timeout", "Max Memory Used: 40 MB"}, separator)
		if got := extractField(report, "Duration: "); got != "" {
			t.Fatalf("missing duration matched another field: %q", got)
		}
		report += separator + "Duration: 10 ms"
		for key, want := range map[string]string{"Duration: ": "10 ms", "Billed Duration: ": "20 ms", "Status: ": "timeout", "Max Memory Used: ": "40 MB"} {
			if got := extractField(report, key); got != want {
				t.Errorf("%s=%q want %q", key, got, want)
			}
		}
	}
}

func TestRuntimeReportFailuresRoundTripAndCompare(t *testing.T) {
	for _, suffix := range []string{"Status: timeout", "Status: error\tError Type: Runtime.ExitError", "ErrorType: Runtime.ExitError"} {
		events := []types.OutputLogEvent{
			{Timestamp: aws.Int64(1000), Message: aws.String("START RequestId: req Version: $LATEST")},
			{Timestamp: aws.Int64(1010), Message: aws.String("END RequestId: req")},
			{Timestamp: aws.Int64(1011), Message: aws.String("REPORT RequestId: req\tDuration: 10 ms\tBilled Duration: 20 ms\tMemory Size: 128 MB\tMax Memory Used: 40 MB\t" + suffix)},
		}
		invocations := parseInvocations(events)
		if len(invocations) != 1 || !invocations[0].IsError {
			t.Fatalf("missed %q: %+v", suffix, invocations)
		}
		snap := Snapshot{FunctionName: "f", Invocations: []InvocationRecord{toRecord(invocations[0])}}
		data, err := json.Marshal(snap)
		if err != nil {
			t.Fatal(err)
		}
		var loaded Snapshot
		if err := json.Unmarshal(data, &loaded); err != nil {
			t.Fatal(err)
		}
		summary := buildCompareSummary("a", "b", Snapshot{FunctionName: "f"}, loaded, nil, CompareOptions{FailOnRegression: true})
		if summary.Errors.NewCount != 1 || len(summary.Errors.NewPatterns) != 1 || !summary.Regression.WillFail {
			t.Fatalf("lost runtime failure: %+v", summary)
		}
		if !strings.Contains(loaded.Invocations[0].ErrorLines[0], "status=") {
			t.Fatal("failure reason was not persisted")
		}
	}
}
