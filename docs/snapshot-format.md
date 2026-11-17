# Snapshot and comparison contracts

This is the shared design for IDEA-015, IDEA-005, IDEA-009, and IDEA-013. Fields are introduced with their implementation; a reserved field below is not a claim that its feature is already available. IDEA-017 storage policy and IDEA-018 manifests use the same compatibility rules.

## Compatibility

New capture snapshots use integer `schema_version: 1` and a `tool_version`. Missing or zero schema version denotes legacy input. Readers accept legacy memory aliases (`mem_used_mb`, `max_mem_mb`) but writers use `memory_size_mb` and `max_memory_used_mb`. Readers reject negative and unsupported future schema versions. Additive optional fields can extend version 1; changing existing meanings requires a new version and a migration reader.

Arrays are emitted as `[]` when empty. Existing metric strings retain their units: duration and billed duration use milliseconds; memory fields use MB. Blank metrics mean missing data, not zero. Negative, malformed, or non-finite metrics are excluded from ordinary statistics and counted separately; opt-in strict record validation rejects them. Sorting retains timestamp precision.

## Snapshot envelope

Required identity for strict comparison is `function_name` and `log_group`. New captures also include `captured_at` in RFC 3339 UTC and `label`. `invocations` contains the selected invocation records.

The `source` object records resolved `region`, `account_id`, and `log_group_arn` when available. AWS profile names and credentials are not persisted. Executed function version and log stream belong to individual invocation records because one capture can span several versions or streams. Source identity must remain distinct from the deployment version that is expected to change.

The `capture` object records requested count, offset, selected count, requested time bounds, observed time bounds, selection mode, scanned/failed streams, page/event/byte counts, and diagnostics. An explicit completeness result describes whether scanning and selection met the request. Empty input, failed reads, exhausted budgets, truncated invocation boundaries, and fewer than the requested number of invocations cannot be silently labeled complete.

Time selection uses an inclusive start and exclusive end against invocation start time. A finite end time stabilizes collection. An in-window invocation may need events beyond that end to obtain its report; if those events cannot be fetched within budget, its completion state is partial. Actual selected time bounds are reported independently of requested bounds.

## Invocation records

Records retain `request_id`, RFC 3339 `timestamp`, duration/billed/memory metrics, `is_error`, `error_lines`, and `log_lines`. Completion metadata distinguishes known START/REPORT boundaries from legacy unknown state. A missing report is partial evidence, not automatically a successful or failed invocation. Runtime failure status and structured severity supply independent error evidence.

The shared event decoder for text and JSON maps request ID, boundary type, timestamp, metrics, runtime status, and severity into the same internal representation. Unsupported JSON platform records and malformed records generate capture diagnostics. They must not become invented successful invocations. Interleaved records with explicit IDs are attributed by ID; unscoped lines are retained as unattributed diagnostics when no safe ownership is available.

Strict record validation is opt-in and separate from identity checking. It validates required request IDs/timestamps, duplicate invocation identities, metric values/units, completion metadata, and contradictions between failure flags and stored failure evidence. Permissive legacy input remains readable, with missing evidence represented explicitly.

## Machine-readable outcomes

Comparison reports include `schema_version`, `tool_version`, status, exit code, identity summaries, diagnostics, metric statistics, pattern differences, and regression reasons with stable reason codes. Human and JSON output share the same computation. A report is emitted before exiting for a regression or insufficient comparison evidence.

| Exit | Meaning |
| --- | --- |
| 0 | Successful command or comparison under the selected policy |
| 1 | Operational/output/self-update failure not covered below |
| 2 | Invalid arguments, invalid snapshot/schema, or strict-validation failure |
| 3 | Capture failed |
| 4 | Insufficient evidence under an enabled comparison/capture policy |
| 5 | Detected regression under an enabled gate |

Each gate identifies its metric/policy and reports `disabled`, `passed`, `failed`, or `insufficient_data`. Missing and malformed metric counts remain separate. A zero baseline makes a relative metric change undefined; JSON uses an explicit definition flag and never encodes NaN/infinity. A finite percentage that exceeds its limit is a failure; arithmetic that cannot be represented is unevaluable and cannot pass an enabled gate. Error count and error rate gates have separate names and reason codes.

Invalid input in JSON mode emits a versioned error envelope instead of a partial comparison report. Diagnostics use stable codes; text explains the concrete affected file, invocation, stream, or limit.

## Storage policy and deployment manifests

Storage metadata identifies raw, redacted, or summary-only mode plus the redaction policy/version. Redaction occurs before persistence and applies to both ordinary and error content. Summary-only records retain aggregate/pattern identity data needed for comparison without storing raw application text. Comparison warns or fails on incompatible storage policies instead of treating missing content as disappeared behavior.

A capture manifest lists every intended function and its outcome, including failures and missing samples. It references snapshot files relative to the manifest and records source identity. Batch comparison pairs entries by source identity, applies the same single-function policy, reports missing/added/removed functions explicitly, and calculates a documented aggregate exit status. A missing or failed function cannot vanish from the result.
