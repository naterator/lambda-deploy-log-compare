# Improvement ideas

Review baseline: commit `af2da33` (`feat: harden Lambda snapshot capture and comparison`). Scope: all Go source and tests, snapshot fixtures, README, and GitHub workflows. The original findings and acceptance criteria are preserved below; current implementation evidence is recorded separately.

## Implementation status

`IDEAS.md` must remain untracked and excluded from commits, as requested by the user.

| Order | Idea | Status and evidence |
| --- | --- | --- |
| 1 | IDEA-010 | Implemented locally. Reusable CI gates release uploads; pinned checkout/setup-go; native check script passed on Go 1.25.0 and 1.27.1; actionlint passed; intentional failing test was rejected; all ten release targets built on Go 1.25.0 with verified checksum pairs. Hosted GitHub jobs have not been run from this worktree. |
| 2 | IDEA-008 | Implemented and tested. `cli_validation_test.go` covers operands before/between/after flags, hidden regression flags, help exits, and rejection before AWS setup. |
| 3 | IDEA-004 | Implemented and tested. `numeric_test.go` covers non-finite/negative values, overflow-safe aggregates, zero baselines, exact boundaries, JSON validity, and agreement between output modes. |
| 4 | IDEA-003 | Implemented and tested. `failure_detection_test.go` covers REPORT statuses, full field-name matching, explicit severity versus benign wording, zero/positive counters, and serialization into comparison error counts/patterns. |
| 5 | IDEA-006 | Implemented and tested. `pattern_identity_test.go` covers suffix-only error changes, full JSON patterns, long request-ID normalization, and Unicode-safe display limits. |
| 6 | IDEA-007 | Implemented and tested. `snapshot_io_test.go` covers write/short-write/chmod/sync/close/publish failures, preserved originals, concurrent exclusive writers, symlinks, permission preservation, collisions before AWS, and explicit post-publication errors. |
| 7 | IDEA-001 | In progress. Live fixture requires an AWS profile/region and authorized test log group/stream; requested from the user. Existing page reversal has not been changed without service evidence. |
| 8 | IDEA-015 | Pending; shared contract design is next. |
| 9 | IDEA-005 | Pending. |
| 10 | IDEA-012 | Pending. |
| 11 | IDEA-002 | Pending. |
| 12 | IDEA-009 | Pending. |
| 13 | IDEA-011 | Pending. |
| 14 | IDEA-013 | Pending. |
| 15 | IDEA-014 | Pending. |
| 16 | IDEA-016 | Pending. |
| 17 | IDEA-017 | Pending. |
| 18 | IDEA-018 | Pending. |

Latest full Go test run, including the race detector, passed after IDEA-007. No production captures, actual self-update, release upload, commit, or push has been performed.

The project already has useful safeguards: exclusive snapshot creation, explicit overwrite, backward-compatible memory fields, partial-event preservation, deterministic output tests, JSON reports, and checksum verification with staged executable replacement. Preserve those behaviors while addressing the gaps below.

Validation during review: `go test ./... -race -coverprofile=/tmp/lambda-deploy-log-compare-review-coverage.out` passed with **89.5% statement coverage** on Go 1.27.1, `go vet ./...` passed, and a local binary built successfully. Focused CLI probes reproduced several comparison gaps despite the passing suite. No live AWS capture or real self-update was performed.

Priorities rank work within this repository: **P0** fixes current correctness or data-loss risks; **P1** improves reliability and coverage of important paths; **P2** adds useful capabilities. Effort is relative: **S** is localized, **M** crosses a few components, and **L** changes capture or snapshot contracts. Keep the `IDEA-NNN` IDs stable when implementing or moving items.

| ID | Priority | Improvement | Effort |
| --- | --- | --- | --- |
| IDEA-002 | P0 | Select the newest invocations across streams | L |
| IDEA-003 | P0 | Detect failures without suppressing explicit error evidence | M |
| IDEA-004 | P0 | Reject invalid numeric metrics and thresholds | S |
| IDEA-005 | P0 | Make incomplete captures and unevaluated gates visible | M |
| IDEA-006 | P0 | Separate pattern identity from display truncation | S |
| IDEA-007 | P0 | Publish snapshots atomically and handle path collisions | M |
| IDEA-008 | P0 | Reject unexpected CLI operands before doing work | S |
| IDEA-001 | P1 | Verify CloudWatch page ordering with service fixtures | M |
| IDEA-009 | P1 | Support structured Lambda logs | L |
| IDEA-010 | P1 | Run tests in CI and gate release publication | M |
| IDEA-011 | P1 | Exercise self-update failure and recovery paths | M |
| IDEA-012 | P1 | Bound capture work and propagate cancellation | M |
| IDEA-013 | P2 | Capture explicit deployment windows and source identity | L |
| IDEA-014 | P2 | Compare error rates and define statistical semantics | M |
| IDEA-015 | P2 | Version and validate snapshot/report contracts | M |
| IDEA-016 | P2 | Show changes in pattern frequency | M |
| IDEA-017 | P2 | Offer controlled storage of sensitive log content | M |
| IDEA-018 | P2 | Compare a complete multi-function deployment | M |

## Correctness

### IDEA-002 — Select the newest invocations across streams

**Evidence:** `capture.go:runCaptureWithOptions` stops reading streams as soon as `len(allInvocations) >= offset + count`. `selectInvocations` sorts only the records already fetched. For a requested count of two, stream A can contain starts at 12:00 and 09:00 while stream B contains 11:59; satisfying the count from A prevents B from being considered. Ordering streams by their last event does not order every invocation inside them.

A temporary mock-based probe reproduced this with 20 requested invocations: the first stream supplied one recent invocation and 19 old ones, and the second stream's 20 newer candidates were never read. AWS also documents that `lastEventTimestamp` is eventually consistent, so it cannot provide an exact cutoff for skipping remaining streams. [AWS DescribeLogStreams reference](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_DescribeLogStreams.html).

**Change:** Define selection against invocation start time across candidate streams. For an explicit time window, enumerate and merge the eligible streams, then apply offset/count globally. If retaining a fast approximate mode, label its sampling behavior and store that mode in the snapshot. Do not use the number of records from one stream as proof that the globally newest sample is complete.

**Acceptance:** Multi-stream fixtures interleave timestamps, include a long-running invocation and late logs, and span multiple stream pages. The selected IDs must match a full-collection reference result for both zero and nonzero offsets.

### IDEA-003 — Detect failures without suppressing explicit error evidence

**Evidence:** `parse.go:parseInvocations` extracts metrics from `REPORT` but does not interpret failure status or error type. Separately, `lineLooksLikeError` calls `isBenignErrorLine` before checking explicit severity; any occurrence of `error_count`, `error rate`, or `no error` can suppress the whole line. For example, an explicit `ERROR: request failed; error_count=1` should not be discarded as a benign counter. A line starting with `level=error` also misses the current whitespace-prefixed checks.

AWS's new-format examples identify crashes and timeouts in the `REPORT` line using `Status: error` or `Status: timeout`, without a separate application error line. These are concrete formats the current parser can miss. [AWS Lambda runtime lifecycle](https://docs.aws.amazon.com/lambda/latest/dg/lambda-runtime-environment.html).

**Change:** Parse runtime status/error fields as explicit invocation failure evidence. Match complete REPORT field names: the current substring search for `Duration:` can otherwise match `Billed Duration:` when duration is absent. Give structured severity and clear fatal/error markers precedence over benign wording. Restrict counter suppression to recognized zero-valued counter messages; avoid suppressing unrelated error text on the same line. Preserve why an invocation was classified as failed.

**Acceptance:** Cover old/new REPORT formats, missing/reordered metric fields, status-only timeout/runtime failures, leading `level=error`, explicit errors containing benign phrases, positive counters, and genuinely harmless zero counters. Verify both error counts and error-pattern output after snapshot serialization.

### IDEA-004 — Reject invalid numeric metrics and thresholds

**Evidence:** `compare.go:validateCompareOptions` only checks `< 0`; `parseNumberWithSuffixes` accepts anything `strconv.ParseFloat` accepts, including non-finite and negative values. CLI probes confirmed that a `NaN` duration threshold allows a 100 ms → 900 ms increase to exit successfully, a `"NaN"` metric breaks JSON encoding, and a negative baseline is treated as valid data. `percentIncrease` also returns zero for a zero baseline followed by a positive value.

**Change:** Require finite, nonnegative thresholds and metric values, counting rejected metrics as malformed. Define zero-baseline behavior explicitly: use an absolute threshold, fail with a clear reason, or mark the relative gate unevaluable. Validate derived aggregates too, since finite inputs can overflow a naive sum. Keep JSON output finite and valid.

**Acceptance:** Table-driven cases cover `NaN`, both infinities, negative values, very large finite values, zero baselines, and exact threshold boundaries. Human and JSON modes must reach the same gate decision; invalid flags must fail before reading snapshots.

### IDEA-005 — Make incomplete captures and unevaluated gates visible

**Evidence:** Capture warns and continues after stream-read errors, but `Snapshot` stores no failure/completeness information. Missing `REPORT` fields are preserved as blanks without a completion status. In comparison, `metricRegressionReasons` skips a gate unless both sides have data. A probe with an empty new snapshot, matching identity, `--strict`, `--fail-on-regression`, and an enabled duration gate exited zero with no warnings. The same happens when the new invocation has no metrics.

**Change:** Persist requested/selected counts, stream failures, observed invocation boundaries, and capture warnings. Preserve partial invocations, while distinguishing them from completed invocations. Give each configured gate an explicit result such as passed, failed, or insufficient data. Add a minimum-sample/completeness policy for CI; document how no streams, exhausted offsets, and partial reads affect exit status. Keep missing metric counts separate from malformed values.

**Acceptance:** Cover all reads failing, a later page failing, fewer than the requested count, an empty sample, missing reports, and entirely malformed metrics. JSON must explain unevaluated gates; an enabled completeness policy must return nonzero when evidence is insufficient. Existing best-effort capture remains available with explicit diagnostics.

### IDEA-006 — Separate pattern identity from display truncation

**Evidence:** `parse.go:normalizeLogLine` truncates normalized content to 100 bytes before `compare.go:collectPatterns` uses it as a map key. Distinct messages with the same long prefix become the same pattern. A CLI probe with different suffixes after a shared prefix reported no pattern differences. `parse_test.go` currently requires this truncation. Byte slicing can also split a UTF-8 character.

**Change:** Compare the complete normalized string, or a stable digest paired with that string. Apply a rune-safe length limit only while rendering human output. Preserve meaningful suffixes in JSON and provide a full example for each pattern.

**Acceptance:** Two error messages differing only after byte 100 produce a new error pattern and can fail the regression gate. Include multibyte text at the display boundary and confirm that dynamic request IDs still normalize together. Replace the existing normalization-truncation assertion with separate identity and rendering tests.

### IDEA-007 — Publish snapshots atomically and handle path collisions

**Evidence:** `capture.go:writeSnapshotOutputFile` uses `os.WriteFile` for overwrite, which truncates an existing snapshot before the new write completes. The exclusive-create path leaves the destination behind if writing fails. Overwrite also follows a destination symlink. Separately, `sanitizeFileComponent` can map distinct names to the same filename: `orders` and `orders-` both become `orders`. Duplicate validation checks original names only.

**Change:** Stage a complete snapshot in the destination directory, flush/close it, and publish it atomically. Preserve exclusive no-overwrite semantics under concurrent writers. Define symlink handling and clean up failed temporary files. Before contacting AWS, resolve all output names and reject collisions, including collisions caused by sanitization; make existing-file conflicts actionable at that point.

**Acceptance:** Inject write/close/publish failures and verify the old snapshot survives. Test concurrent no-overwrite writers, a destination symlink, sanitized-name collisions with and without `--overwrite`, and temporary-file cleanup. Filename sanitization must remain in place.

### IDEA-008 — Reject unexpected CLI operands before doing work

**Evidence:** `main.go:run` never checks `FlagSet.Args()` after parsing `capture` or `compare`. A positional token stops flag parsing, and later flags are silently ignored. With the repository's comparison fixtures, `--fail-on-regression` exits one normally, but `unexpected --fail-on-regression` exits zero. Help currently exits one, and `TestRun_FlagExitPaths` requires that behavior.

**Change:** Reject leftover positional arguments for both commands and report the unexpected token. Handle `flag.ErrHelp` as successful help output. Keep validation before AWS client initialization, snapshot writes, or comparison execution.

**Acceptance:** Exercise operands before, between, and after flags; an operand followed by a regression flag must never silently disable the gate. Help should exit zero, unknown flags should fail, and invalid capture input should make no AWS calls.

## Reliability and coverage

### IDEA-001 — Verify CloudWatch page ordering with service fixtures

**Evidence:** `capture.go:fetchLogEvents` reverses every returned `Events` slice, while `flattenLogEventPages` separately reverses page order. `capture_test.go:latestFirstEvents` and `singleTailPage` construct reversed responses, so the tests encode the same assumption as production. `parseInvocations` depends on chronological input to associate ordinary messages with the current request ID.

This is a **contract-verification gap, not a confirmed ordering bug**. AWS describes `startFromHead=false` as returning the latest events first, while its example response contains ascending timestamps. That evidence alone does not settle within-page ordering for every pagination case. AWS also explicitly allows empty pages with advancing continuation tokens. [AWS GetLogEvents reference](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_GetLogEvents.html).

**Change:** Capture a small, sanitized service-response fixture from an authorized test log group, with the request flags and tokens recorded. Establish backward-page order separately from within-page order. Adjust reversal only if that evidence requires it, and retain stable ordering for equal timestamps. Keep service fixtures independent of production transformation helpers.

**Acceptance:** A fixture with two `START → application log → REPORT` sequences keeps each message with its request ID. Cover a request split across pages, equal timestamps, an empty page with a continuation token, and a partial read failure. Assert final log ownership, not just event counts.

### IDEA-009 — Support structured Lambda logs

**Evidence:** `parse.go` and `capture.go:invocationCompletenessTracker.addPage` recognize text `START`, `END`, and `REPORT` prefixes. `extractInlineRequestID` recognizes a few text forms but not a JSON `requestId` field. JSON-only platform events therefore cannot establish the invocation boundaries these components require.

AWS supports JSON system logs and application records with explicit request-ID and severity fields; this is an existing input format rather than a speculative format extension. [AWS Lambda log formats](https://docs.aws.amazon.com/lambda/latest/dg/monitoring-cloudwatchlogs-logformat.html).

**Change:** Introduce a shared event decoder for text and structured JSON records. Decode request ID, platform event type, severity, timestamps, and report metrics into one internal representation used by both completeness tracking and parsing. Preserve raw messages for diagnosis and count unsupported/malformed events rather than silently treating them as successful invocations.

**Acceptance:** Add representative structured platform/application fixtures, mixed text/JSON streams, malformed JSON, and unfamiliar event types. Equivalent text and JSON invocations should produce equivalent summaries. A JSON-only stream must yield invocations without scanning indefinitely for text markers.

### IDEA-010 — Run tests in CI and gate release publication

**Evidence:** `.github/workflows/govulncheck.yml` runs on pull requests, while `.github/workflows/build.yml` only builds/uploads release binaries on release creation. Neither workflow runs the repository's unit tests, race detector, or `go vet`. The release matrix spans five OSes and two architectures without a preceding shared validation job. Release tooling uses a moving action tag and the job grants package-write permission even though the shown workflow publishes release assets.

**Change:** Add pull-request and main-branch validation with formatting, vet, and the existing tests. Test the minimum supported Go version as well as the chosen current toolchain. Require validation of the release commit before publishing assets. Make the toolchain used by the actual release builder explicit, pin release tooling to reviewed revisions, and narrow job permissions. Check every expected binary/checksum pair and verify embedded version output on native runners. Document the supported OS/architecture matrix, including the current lack of Windows release assets and Windows self-update support.

**Acceptance:** A deliberately failing test blocks release publication. The supported Go minimum builds/tests successfully. Each matrix target produces the expected filenames consumed by `releaseAssetCandidates`, with matching SHA-256 files. Native smoke tests run `version` and a fixture comparison. Cross-compilation alone must not be described as runtime verification.

### IDEA-011 — Exercise self-update failure and recovery paths

**Evidence:** `selfupdate_test.go` covers an up-to-date release and successful replacement, but has no dedicated checksum-mismatch, interrupted-download, chmod-failure, rename-failure, or directory-sync-failure tests. `downloadAndReplace` has multiple cleanup branches, and an error after rename means the executable has already changed. The successful test stubs directory syncing and replaces the executable with arbitrary bytes, so it does not establish that the resulting artifact runs.

**Change:** Add failure-injection tests around the existing seams and clarify which failures occur before versus after replacement. Bound checksum and binary response sizes; the current `io.ReadAll` and `io.Copy` paths have no size limit. Validate artifact identity/format or use a safe staged `version` smoke check before replacement where appropriate. Add a native integration test using a disposable executable, exercising the real file operations and verifying that the installed program starts. Improve post-replacement error wording so users know which version is on disk.

**Acceptance:** Before-rename failures preserve the original executable and remove temporary files. Checksum mismatch, oversized responses, and canceled downloads never replace it. A post-rename sync failure reports that replacement already happened. Integration tests verify executable permissions, version output, and cleanup without touching the test runner or a user's installation.

### IDEA-012 — Bound capture work and propagate cancellation

**Evidence:** `main.go` and `capture.go` create background contexts. Capture applies fresh per-stream/page deadlines but has no total capture budget or event/byte limit. `fetchLogEvents` retains all fetched pages; the parser retains log lines; capture also marshals the entire snapshot in memory. A stream with many logs and few recognizable invocation markers can consume substantial work. `needed := offset + count` is unchecked for integer overflow.

**Change:** Thread a signal-aware context from the CLI through configuration and capture. Add an overall timeout and explicit limits on pages/events/bytes, alongside the existing per-stream timeout. Reject count-plus-offset overflow before AWS access. Preserve partial results with the completeness diagnostics from IDEA-005. Only add bounded concurrency after the selection semantics in IDEA-002 are settled.

**Acceptance:** Simulate a marker-free stream, an advancing token with empty pages, repeated stream tokens, cancellation, and count/offset integer limits. Capture must stop within its declared budget and identify the limiting condition. Add a representative large-stream benchmark to guide memory work instead of assuming concurrency is the remedy.

## Product and data-contract improvements

### IDEA-013 — Capture explicit deployment windows and source identity

**Evidence:** `capture` selects only count/offset, derives `/aws/lambda/<function>`, and defaults `--region` to `us-west-2`. `Snapshot` records function name/log group but no region, account, executed function version, stream, or requested time range. Strict comparison only checks those names. Matching names can therefore conceal a cross-environment comparison, and a post-deploy capture can include pre-deploy invocations.

**Change:** Add explicit start/end time selection and record the actual selected range. Allow a custom log group and preserve executed function version/stream provenance. Store resolved region/account identity when available, and distinguish source identity from the deployment version expected to change. Define region precedence so environment/profile configuration can be used deliberately. Warn on overlapping request IDs, overlapping capture windows, or mismatched environments; extend strict validation accordingly.

**Acceptance:** Test a deployment boundary, identical names in different regions/accounts, a custom log group, qualified versions, and overlapping snapshots. Legacy files lacking new metadata should remain readable with an explicit unknown-provenance warning.

### IDEA-014 — Compare error rates and define statistical semantics

**Evidence:** `compare.go:regressionReasons` compares absolute error counts. A CLI probe with the same error pattern in 2/20 baseline invocations and 2/2 new invocations passes `--fail-on-regression`, despite rates of 10% and 100%. This follows the documented count-based contract, but becomes misleading with unequal samples. `computeMetricStats` uses zero-based `floor(n * 0.9)` for p90; values 1–20 produce 19, while nearest-rank p90 would be 18. The README does not name the quantile method.

**Change:** Report both counts and rates, warn about unequal sample sizes, and offer an explicit error-rate gate with a minimum sample policy. Define whether rate thresholds use percentage points or relative percentages. Choose and document the quantile convention before changing it. Distinguish an undefined relative change from zero change, and report signed metric deltas so improvements are visible as well as regressions.

**Acceptance:** Cover 2/20 versus 2/2 errors, proportional count increases with unchanged rates, zero-error and empty baselines, minimum-sample boundaries, and percentile examples for 1, 2, 10, and 20 values. Preserve or explicitly migrate existing count-gate behavior.

### IDEA-015 — Version and validate snapshot/report contracts

**Evidence:** `types.go` already contains a custom compatibility layer for renamed memory fields, but neither snapshots nor JSON comparison summaries have a schema version. `validateLoadedSnapshot` accepts any object with one nonempty metadata field, and invocation validation does not check timestamp validity, duplicate request IDs, or consistency between `is_error` and `error_lines`. Empty arrays are frequently emitted as `null`, and JSON failures use the same generic exit status as detected regressions.

**Change:** Add schema/tool versions and a documented migration strategy. Keep permissive legacy reading separate from opt-in strict record validation. Define required fields, metric units, completion status, null/empty-array conventions, and gate-result fields. Give CI consumers stable reason codes and a documented way to distinguish invalid input, capture failure, insufficient data, and a detected regression.

**Acceptance:** Golden JSON fixtures cover both legacy memory names and the new schema. Unknown future versions fail clearly; malformed individual records produce actionable diagnostics. Test invalid timestamps, duplicate IDs, contradictory error fields, empty arrays, and stable machine-readable outcomes. Include concise JSON and CI examples in the README alongside the contract change.

### IDEA-016 — Show changes in pattern frequency

**Evidence:** `compare.go:collectPatterns` stores only `map[string]bool`. A warning emitted once in the baseline and on every new invocation produces no pattern difference. The existing output can only say that a pattern appeared or disappeared.

**Change:** Count both total occurrences and the number of invocations containing each normalized pattern. Show rates and the largest increases, with a representative request ID/example for investigation. Keep presence/absence output, and make any frequency gate explicit to avoid treating repeated lines within one invocation as many failed requests.

**Acceptance:** Test a repeated line within one invocation, the same line across many invocations, unequal snapshot sizes, and deterministic ranking ties. JSON should expose both count types and their denominators; human output should stay bounded.

### IDEA-017 — Offer controlled storage of sensitive log content

**Evidence:** `toRecord` copies raw `LogLines` and `ErrorLines` into snapshots, and `writeSnapshotOutputFile` requests mode `0644` for new files. Comparison normalization happens after snapshots have already been written; it is not storage redaction. Users may need to retain or share comparisons without retaining all application payloads.

**Change:** Offer restrictive file permissions and an explicit redaction/summary-only capture mode. Apply configured redaction before serialization, including duplicated error-line content. Record the redaction policy/version so readers understand what was removed and so comparisons use compatible policies. Document the raw-content default and sharing behavior accurately.

**Acceptance:** Place synthetic secrets in ordinary and error logs and verify that every persisted copy is redacted under the selected policy. Check permissions on supported platforms, preservation of original logs in memory only as needed, and comparability across equal/different redaction policies. Do not claim that a finite regex list guarantees all sensitive data is removed.

### IDEA-018 — Compare a complete multi-function deployment

**Evidence:** `capture --function` accepts multiple functions and writes separate snapshots, but `compare` accepts only one `--a`/`--b` pair. CI users must discover files, pair functions, invoke comparison repeatedly, and aggregate exit results themselves. Missing captures are especially easy to overlook when a function has no streams or a partial failure.

**Change:** Emit an optional capture manifest listing intended functions, output files, and outcomes, then support comparison of two manifests. Pair by source identity rather than sanitized filename, reuse the single-function comparison engine, and provide a compact per-function result plus an aggregate JSON status. Define policies for missing, added, and removed functions.

**Acceptance:** Cover all-success deployments, one regressing function, a missing snapshot, a renamed function, a failed capture, and sanitized filename collisions. Every expected function must appear in the aggregate result, and aggregate failure rules must be documented.

## Implementation order

Use this order for a single implementation stream. It puts CI and shared foundations ahead of dependent changes, while addressing the smaller correctness fixes early. The order is separate from the priority labels and permanent idea IDs.

| Order | Idea | Work and rationale |
| --- | --- | --- |
| 1 | IDEA-010 | **CI and release gates** — protect every subsequent change with automated checks. |
| 2 | IDEA-008 | **CLI argument validation** — close a small, confirmed regression-gate bypass. |
| 3 | IDEA-004 | **Numeric validation** — prevent invalid thresholds and metrics from producing misleading results. |
| 4 | IDEA-003 | **Failure detection** — correctly identify runtime failures and explicit errors. |
| 5 | IDEA-006 | **Pattern identity** — stop truncation from hiding distinct errors. |
| 6 | IDEA-007 | **Atomic snapshot writes** — protect existing snapshots and reject filename collisions. |
| 7 | IDEA-001 | **CloudWatch ordering verification** — establish the event-order contract before changing collection. |
| 8 | IDEA-015 | **Snapshot/report contracts** — establish versioning, compatibility rules, validation, and stable result codes. |
| 9 | IDEA-005 | **Completeness and gate outcomes** — distinguish insufficient evidence from a passing comparison. |
| 10 | IDEA-012 | **Capture limits and cancellation** — bound work and report partial results before expanding collection. |
| 11 | IDEA-002 | **Global invocation selection** — collect the correct newest sample across streams using those safeguards. |
| 12 | IDEA-009 | **Structured JSON logs** — extend the established parsing and completeness model. |
| 13 | IDEA-011 | **Self-update failure coverage** — verify preservation, cleanup, and post-replacement behavior. |
| 14 | IDEA-013 | **Deployment windows and source identity** — make samples specific to the intended deployment and environment. |
| 15 | IDEA-014 | **Error rates and statistical semantics** — improve decisions using trustworthy samples and explicit completeness. |
| 16 | IDEA-016 | **Pattern frequency** — build occurrence comparisons on corrected pattern identities. |
| 17 | IDEA-017 | **Sensitive-log storage controls** — add redaction and storage policies to the established capture format. |
| 18 | IDEA-018 | **Multi-function deployment comparison** — aggregate the completed identity, completeness, and comparison behavior. |

### Dependency notes

- **Design the shared contract before step 8.** Agree on the snapshot/report fields needed by IDEA-005, IDEA-009, IDEA-013, and IDEA-015 together. Establish the versioning and compatibility foundation in IDEA-015, then extend validation and fixtures alongside each feature. This avoids repeatedly redesigning the format as completeness, structured logs, and provenance are added.
- **Establish ordering before changing global selection.** IDEA-001 supplies the service-order evidence needed for IDEA-002. Keep that verification separate from the already reproduced cross-stream selection bug; change page reversal only when the evidence supports it.
- **Make bounded capture report incomplete results.** IDEA-012 follows IDEA-005 so timeout, cancellation, or resource-limit outcomes can carry explicit completeness diagnostics. IDEA-002 then uses those safeguards when scanning across more streams; hitting a budget must not imply that the globally newest sample is complete.
- **Defer capture concurrency until after step 11.** Step 10 implements budgets and cancellation first. Any concurrent capture work must follow verification of global selection in IDEA-002.
- **Build richer comparisons on the corrected foundations.** IDEA-014 uses reliable selection, completeness, and source identity; IDEA-016 depends on the full pattern identities from IDEA-006. IDEA-017 extends the atomic storage and versioned format established by IDEA-007 and IDEA-015. IDEA-018 comes last so missing captures, identity mismatches, and individual comparison outcomes remain visible in the aggregate result.
