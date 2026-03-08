# lambda-deploy-log-compare

A CLI tool for capturing and comparing AWS Lambda invocation logs across deployments. Helps you verify that a new deployment isn't introducing errors, performance regressions, or unexpected behavior by comparing log snapshots taken before and after a deploy.

## Install

Download a binary for your OS from [release page](https://github.com/naterator/lambda-deploy-log-compare/releases). Put the executable in a directory in `$PATH`.

Or you can install by building from source directly as follows. Go 1.25 or later is necessary.

```bash
go install github.com/naterator/lambda-deploy-log-compare@latest
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

If `./snapshots` does not exist yet, the tool creates it before writing snapshot files.

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
| `--count` | `20` | Number of invocations to capture. Must be greater than `0`. |
| `--offset` | `0` | Skip this many recent invocations before capturing. Must be `0` or greater. |
| `--out` | `.` | Output directory for snapshot JSON files |
| `--region` | `us-west-2` | AWS region |
| `--profile` | | AWS CLI profile name |

Log groups are derived as `/aws/lambda/<function-name>`.

Output files are named `<function-name>_<label>.json` in the output directory. Each function gets its own snapshot file, and the output directory is created automatically when a snapshot is written.

The `--offset` flag lets you look further back in time. For example, `--offset 100 --count 20` skips the 100 most recent invocations and captures the 20 after that. This is useful for grabbing a historical baseline to compare against.

If no log streams are found for a function, the command exits successfully for that function and does not write a snapshot file.

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
- **Snapshot mismatch warnings** — warns when the two files appear to be from different Lambda functions or log groups

## Snapshot Format

Each snapshot file contains one `Snapshot` object with metadata plus an `invocations` array.

Important field notes:

- `duration` and `billed_ms` come from Lambda `REPORT` lines.
- `mem_used_mb` currently stores Lambda `Memory Size`, which is the configured memory size for the function.
- `max_mem_mb` stores Lambda `Max Memory Used`, which is the value used for memory comparison stats and `peak_mem` in compare output.

## AWS Authentication

Uses the standard AWS SDK credential chain (environment variables, `~/.aws/credentials`, IAM roles, etc.). Use `--profile` to select a named profile and `--region` to set the region.

The IAM principal needs these permissions:

- `logs:DescribeLogStreams` on the target log groups
- `logs:GetLogEvents` on the target log groups

## How It Works

1. **Stream discovery** — Fetches recent log streams ordered by last event time (most recent first), with some extra headroom beyond the requested `offset + count`.
2. **Invocation parsing** — Reads events from each stream, tracks invocations from Lambda `START` and `REPORT` markers, and assigns non-marker log lines to the most recently seen request in that stream. Extracts duration, billed duration, memory size, and max memory used from `REPORT` lines. Each stream gets a 30-second timeout, and collection stops early once enough invocations are found.
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
  Baseline: ./snapshots/my_transformer_func_pre-deploy.json (label: pre-deploy, captured: 2026-02-25T10:00:00Z)
  New:      ./snapshots/my_transformer_func_post-deploy.json (label: post-deploy, captured: 2026-02-25T12:00:00Z)

--- BASELINE: my_transformer_func [pre-deploy] (log group: /aws/lambda/my_transformer_func) ---
  Invocations captured: 20
  Errors: 0
  Recent invocations:
    2026-02-25T09:59:58Z  dur=45.2 ms  peak_mem=85 MB  mem_size=128 MB
    2026-02-25T09:59:52Z  dur=98.3 ms  peak_mem=90 MB  mem_size=128 MB

--- NEW: my_transformer_func [post-deploy] (log group: /aws/lambda/my_transformer_func) ---
  Invocations captured: 20
  Errors: 0
  Recent invocations:
    2026-02-25T11:59:59Z  dur=42.1 ms  peak_mem=84 MB  mem_size=128 MB
    2026-02-25T11:59:54Z  dur=95.0 ms  peak_mem=91 MB  mem_size=128 MB

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
