# Service ordering fixtures

Live fixtures are intentionally opt-in and must come from an authorized test stream with at least two plain-text START/REPORT invocation sequences. No synthetic fixture is represented as a service capture.

```bash
LAMBDA_FIXTURE_PROFILE=test-profile \
LAMBDA_FIXTURE_REGION=us-west-2 \
LAMBDA_FIXTURE_LOG_GROUP=/aws/lambda/test-function \
LAMBDA_FIXTURE_LOG_STREAM=chosen-test-stream \
LAMBDA_FIXTURE_OUT=testdata/cloudwatch/live-ordering.json \
go test -run '^TestRecordLiveCloudWatchFixture$' -count=1 -v

go test -run '^TestRecordedCloudWatchOrdering$' -count=1 -v
```

The recorder performs only bounded GetLogEvents reads (at most eight 100-event pages and 45 seconds). It records paging direction, limits, token relationships, timestamps, region, and a digest of source identity. Application payloads, request IDs, stream/group names, and paging-token values are removed or replaced. It keeps numeric report metrics and known status fields, and refuses to overwrite a fixture. Review the sanitized output before sharing it.

Without a live fixture the ordering test is skipped, not counted as evidence that service ordering is verified. The production reversal must only be changed after inspecting the recorded response order and equal-timestamp cases. Existing synthetic pagination tests remain separate.
