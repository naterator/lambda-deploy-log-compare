# lambda-deploy-log-compare

A CLI tool for capturing and comparing AWS Lambda invocation logs across deployments. Helps you verify that a new deployment isn't introducing errors, performance regressions, or unexpected behavior by comparing log snapshots taken before and after a deploy.

## Install

Requires Go 1.25.0+.

```bash
go build
```

## Quick Start

```bash
# Capture baseline snapshots
./lambda-deploy-log-compare capture \
    --function my_func_a,my_func_b \
    --label pre-deploy \
    --out ./snapshots

# Deploy your code...

# Capture post-deploy snapshots
./lambda-deploy-log-compare capture \
    --function my_func_a,my_func_b \
    --label post-deploy \
    --out ./snapshots

# Compare
./lambda-deploy-log-compare compare \
    --a ./snapshots/my_func_a_pre-deploy.json \
    --b ./snapshots/my_func_a_post-deploy.json
```

## Commands

### `capture`

Fetches CloudWatch Logs for one or more Lambda functions and saves a snapshot of invocations.

```
lambda-deploy-log-compare capture --function <name>[,<name>,...] --label <label> [options]
```

| Flag | Default | Description |
|---|---|---|
| `--function` | | Lambda function name(s), comma-separated (required) |
| `--label` | | Label for the snapshot, e.g. `pre-deploy` (required) |
| `--count` | `20` | Number of invocations to capture |
| `--offset` | `0` | Skip this many recent invocations before capturing |
| `--out` | `.` | Output directory for snapshot JSON files |
| `--region` | `us-west-2` | AWS region |
| `--profile` | | AWS CLI profile name |

Log groups are derived as `/aws/lambda/<function-name>`.

Output files are named `<function-name>_<label>.json` in the output directory. Each function gets its own snapshot file.

The `--offset` flag lets you look further back in time. For example, `--offset 100 --count 20` skips the 100 most recent invocations and captures the 20 after that. This is useful for grabbing a historical baseline to compare against.

### `compare`

Loads two snapshot files and prints a side-by-side comparison:

```
lambda-deploy-log-compare compare --a <baseline.json> --b <new.json>
```

The comparison includes:

- **Error count** — warns if errors increased
- **Error patterns** — new patterns that appeared, old patterns that disappeared
- **Duration stats** — min, avg, p50, p90, max (in milliseconds)
- **Memory usage** — min, avg, max peak memory (in MB)
- **Log pattern diff** — new and gone log line patterns (UUIDs normalized)

## AWS Authentication

Uses the standard AWS SDK credential chain (environment variables, `~/.aws/credentials`, IAM roles, etc.). Use `--profile` to select a named profile and `--region` to set the region.

The IAM principal needs these permissions:

- `logs:DescribeLogStreams` on the target log groups
- `logs:GetLogEvents` on the target log groups

## How It Works

1. **Stream discovery** — Fetches log streams ordered by last event time (most recent first), with headroom beyond the requested offset + count.
2. **Invocation parsing** — Reads events from each stream and groups them by request ID using Lambda's `START`/`END`/`REPORT` markers. Extracts duration, billed duration, memory size, and max memory used from `REPORT` lines. Each stream gets a 30-second timeout, and collection stops early once enough invocations are found.
3. **Error detection** — Any log line containing `error`, `panic`, `fatal`, `traceback`, or `exception` (case-insensitive) flags the invocation as an error.
4. **Snapshot selection** — From all discovered invocations (sorted by time, most recent first), skips the first `offset` invocations, then takes the next `count`.
5. **Pattern normalization** — For comparison, log lines are normalized by collapsing UUID-like hex strings (32+ chars) to `<UUID>` and truncating to 100 characters. This lets you compare structural patterns rather than exact values.

## Project Structure

```
main.go       CLI entry point, flag parsing, AWS client setup
capture.go    LogsClient interface, log stream/event fetching, snapshot writing
compare.go    Snapshot loading, diff/stats computation, report output
parse.go      Invocation parsing from CloudWatch log events
types.go      Shared data types (InvocationSummary, InvocationRecord, Snapshot)
```

## Example Output

```
=== Comparison: my_transformer_func ===
  Baseline: ./snapshots/pre-deploy.json (label: pre-deploy, captured: 2026-02-25T10:00:00Z)
  New:      ./snapshots/post-deploy.json (label: post-deploy, captured: 2026-02-25T12:00:00Z)

--- BASELINE: my_transformer_func [pre-deploy] ---
  Invocations captured: 20
  Errors: 0

--- NEW: my_transformer_func [post-deploy] ---
  Invocations captured: 20
  Errors: 0

=== Error Comparison ===
  Baseline errors: 0 / 20 invocations
  New errors:      0 / 20 invocations
  Error count unchanged

=== Duration Comparison ===
  Baseline: min=45.2ms avg=120.5ms p50=98.3ms p90=210.1ms max=250.7ms (n=20)
  New     : min=42.1ms avg=115.8ms p50=95.0ms p90=205.3ms max=245.2ms (n=20)

=== Memory Usage Comparison ===
  Baseline: min=85MB avg=92MB max=105MB (n=20)
  New     : min=84MB avg=91MB max=103MB (n=20)

=== Log Pattern Diff ===
  No significant log pattern differences detected
```
