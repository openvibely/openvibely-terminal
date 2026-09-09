---
kind: openvibely.agent_skill
version: 4
skill:
    key: openvibely_optimization_audit
    name: OpenVibely Optimization Audit
    scope: project
    description: Measure and validate performance opportunities in OpenVibely Go build, test, coverage, CI, and runtime rendering workflows before filing a suggestion.
---

# OpenVibely Optimization Audit

Use this skill when auditing `openvibely-tui` for measurable build, test, coverage, CI, latency, throughput, memory, or workflow efficiency opportunities. Keep the audit read-only and report only opportunities backed by current measurements or a reproducible measurement plan.

## Runtime Rendering And Payload Previews

- Vary the inspected component across runs. For a rendering path, trace the data volume returned by the backend, the display-width or item-count bound, and every conversion or formatting step before choosing a candidate.
- Prefer existing package benchmarks and realistic fixtures over synthetic microbenchmarks. For lifecycle or log details, include a representative large payload and record payload size, benchmark name, invocation, iteration count, wall time, and allocation count/bytes.
- Isolate expensive phases such as JSON serialization, truncation, and table formatting so the finding identifies the measured bottleneck rather than attributing total render time to the wrong phase. If a value is serialized more than once, measure each serialization separately and compare it with the bounded preview work that the user actually sees.
- Establish user-visible equivalence before proposing bounded or summary rendering: previews within the display limit must remain unchanged, oversized payloads must retain the documented truncation behavior, and malformed or unusual values must follow existing fallback behavior.
- Define before-and-after criteria that combine a material reduction in median render time and allocations for the measured large-payload case with unchanged preview output, bounded work relative to displayed content, and no regression for small payloads or normal event counts. Do not file from an allocation count alone or from a large fixture that cannot occur in the product workflow.

## Task Detail Tab Request Fan-Out

- For task/detail views with selectable tabs, trace the complete path from command or route resolution through client/backend requests to the selected renderer. Build a request matrix for each explicit tab and the no-tab/full-detail path, recording which endpoints are called, response sizes, and which returned fields are actually rendered.
- Distinguish unnecessary fan-out from sequential-latency findings: concurrent requests can reduce elapsed time while still downloading unrendered thread, changes, lifecycle, or detail data. Use request counters, controlled responses, payload-byte measurements, or a reproducible benchmark to quantify the extra work rather than filing from call-graph shape alone.
- Compare realistic task sizes and preserve the no-tab/full-detail contract. Acceptance criteria should require unchanged selected-tab output, retained requests for data that tab needs, no unnecessary requests for data it cannot render, unchanged error/loading/cancellation behavior, and regression coverage for every tab plus the full-detail path.

## Direct Identifier Resolution

- For commands that accept either an exact resource ID or a title/partial reference, trace resolution before the detail request. Check whether an exact, unambiguous ID still triggers a full collection or board download solely to rediscover the same ID.
- Measure exact-ID lookup separately from fuzzy lookup at realistic collection sizes such as 10, 100, and 1,000 resources. Record request count, transferred bytes, median latency, and allocations; use a controlled backend delay to expose collection-fetch latency without conflating it with the required detail request.
- Limit any fast path to identifiers whose syntax is fully validated and unambiguous. Preserve title and partial-ID matching, ambiguity and not-found diagnostics, project scoping, authorization behavior, and the existing detail response.
- Define acceptance criteria requiring the exact-ID path to skip the collection request, reduce bytes and materially improve median latency or allocations at scale, while fuzzy references retain their existing resolution behavior and malformed ID-like input does not bypass validation.

## Live Event Formatting

- For high-volume live-event streams, trace output-mode dispatch before serialization or compaction. Measure recognized events separately from JSON output and unknown or unstructured fallback; if recognized plain-text events emit only selected fields, avoid compacting the raw JSON when that value cannot affect the output.
- Use the same event count, parsing/input conditions, and representative payload sizes for current and candidate paths. Report median wall time and allocation/bytes data for small and large payloads, and isolate formatter work from backend or transport latency with controlled local responses when needed.
- Preserve exact output and behavior for recognized event types, JSON mode, unknown events, malformed payloads, filtering, and completion/status handling. File only when matched measurements show a material reduction at a plausible stream volume without regressing small payloads or normal event counts.

## Project Memory Search

- For indexed project-memory search, inspect both the live index and the search implementation before proposing an optimization. Record the real indexed-file count and bytes so a scale-triggered finding is not presented as a current small-project latency problem.
- Use a valid stress fixture within the product's index and per-file size limits. Measure repeated runs with the same fixture and separate no-match from a match-at-end query; a materially slower match path can reveal a second full case-normalization or snippet scan that a single aggregate timing hides.
- Attribute costs by phase: serial file reads, per-file size caps, full-body case normalization, match detection, and line-by-line snippet generation. Capture wall time, user/system CPU when available, peak RSS or allocation data, and query/result semantics rather than relying on a code-shape observation.
- Before filing, establish that the measured cost is material at a plausible indexed scale and define acceptance criteria that preserve case-insensitive matches, result ordering, snippets, empty/no-match behavior, file-size and index bounds, and existing path/security checks while reducing duplicate reads, scans, normalization, or allocations. Recheck the real project corpus so a stress-only improvement does not regress ordinary searches.

## Go Test And Coverage Comparisons

- Record the exact command, Go version, repository state, cache locations, exit status, wall time, CPU time, peak memory when available, package result markers, and coverage output for every run.
- To test whether a workflow unnecessarily bypasses test-result caching, compare the exact current command with an otherwise identical cacheable form, including a second unchanged invocation of each form. Use separate fresh `GOCACHE` directories for the modes, warm each mode with its first invocation, and do not attribute a warm build-cache result to test-result caching.
- Treat `-count=1` as an intentional cache bypass, not as a harmless default. Measure its second-run cost against the cacheable command before proposing a CI change, and preserve a separate fresh-run path when uncached execution is required for live, nondeterministic, or explicitly clean tests.
- Validate that the faster form preserves the required test and coverage semantics: both runs must succeed, the coverage profile must be readable, package coverage must be present, and aggregate coverage should match. Compare profile size or bytes when the environment makes that deterministic, but do not use timing alone as proof of equivalence.
- Use timer syntax supported by the host OS; do not assume GNU-only `time` flags on macOS. If a measurement wrapper or parser fails, discard its rows, fix the wrapper, and rerun the complete comparison rather than reporting partial output. Prefer simple, portable parsing over fragile nested quoting.

## Workflow Cache Policy Checks

- Keep routine CI cacheable by default when tests are hermetic, but expose and document an explicit uncached path for fresh, live-backend, nondeterministic, or otherwise cache-sensitive validation. Verify that the uncached branch retains the intentional bypass flag rather than merely changing a label or cache key.
- Validate workflow changes both syntactically and semantically for every supported trigger and branch. YAML 1.1 parsers such as Ruby Psych may interpret the unquoted `on` trigger key as boolean `true`; use a schema-aware parser or a parser-compatible lookup so this tooling quirk is not mistaken for a workflow defect.
- Exercise a clean-cache run, repeated unchanged runs, and representative source, test, dependency, and environment-input changes. Confirm that affected package results rerun while unaffected packages remain cached, and inspect generated coverage/artifacts for validity after each relevant mode.
- If a cache-policy change conflicts with an existing parallel vet/test workflow, rebase the task branch onto the target branch rather than merging the target into it. Resolve inside the existing shell block: preserve bounded overlap, independent status capture and waiting, non-empty coverage checks, and success-gated summaries, while selecting the cacheable default or explicit `-count=1` command from the workflow input. Verify the target branch ref is unchanged and the rebased task commit is a fast-forward descendant.

## Independent CI Step Scheduling

- When separate CI validation steps appear independent, compare matched sequential and overlapped executions of the exact commands. Keep repository state, cache conditions, environment, and artifact handling equivalent; use a fresh or explicitly controlled cache when evaluating cold-start behavior.
- Repeat each mode enough to report a median or similarly robust summary rather than filing from one timing pair. Capture wall time plus CPU, peak RSS, exit status, test/coverage results, and any resource contention visible in the runner.
- Treat concurrency as an optimization only when the wall-time reduction is material and the added CPU or memory fits the runner budget. Check that parallel execution preserves coverage/artifact outputs, diagnostics, failure propagation, cancellation behavior, and any ordering or shared-cache assumptions; do not infer independence merely because two commands currently pass.
- Define acceptance criteria that include both a target wall-time improvement and explicit resource and correctness safeguards. If the gain is small, unstable, or achieved only by oversubscribing the runner, do not file it as a performance opportunity.
- For a shell implementation with `set -e` or equivalent fail-fast behavior, capture foreground and background statuses independently, await the background process even when the foreground command fails, and gate coverage summaries on both successful commands. Add an explicit non-empty profile check before invoking the coverage summary so failed tests cannot produce misleading success output.
- Validate the committed run block with a fixture or fake-command harness when no workflow runner is available. Cover clean success, vet-only failure, test-only failure, dual failure, wait/completion ordering, repeated coverage output, profile validity, and exact freshness/coverage flags; capture expected simulated diagnostics so harness output cannot hide a real assertion failure.
- Do not execute a GitHub Actions workflow shell block verbatim in a local harness when it contains `${{ ... }}` expressions. Render the workflow input first or substitute those expressions in the fixture; a literal value such as `${{ inputs.uncached }}` can produce a harness failure unrelated to repository behavior. Classify that as harness setup failure and rerun with interpolation simulated before using the result.
- If unrestricted overlap oversubscribes the runner, measure a narrow scheduler guard such as a bounded `GOMAXPROCS` for the background check before adding setup jobs or changing test flags/cache policy. Keep the guard only when matched measurements show the target speedup and resource limits still hold.
- When timing through a shell wrapper that uses functions or local variables, invoke an external worker or otherwise verify subprocess state is preserved. Discard any rows from a broken wrapper, fix it, and rerun the full comparison.

## Independent CLI Request Scheduling

- For one-shot commands that aggregate global and project-scoped status, draw the request dependency graph before measuring. Start global authentication, capacity, or account checks concurrently with project discovery when they do not need a project identifier; launch project-scoped alert/task counts only after discovery completes.
- Measure with a controlled transport that assigns the same fixed delay to each endpoint, records request start and completion times, and verifies the expected dependency edges. Compare latency waves rather than summing endpoint durations: independent global checks plus discovery should form the first wave, followed by project-dependent counts in the second wave.
- Use repeated matched runs and report median end-to-end latency, request count, and the number of critical-path waves. A representative 100 ms delay per endpoint should distinguish a three-wave baseline near 300 ms from a two-wave candidate near 200 ms; require a material target such as at least 25% lower median latency rather than relying on call-graph inspection alone.
- Preserve output ordering and values, authentication and unauthenticated behavior, offline handling, no-project and multi-project behavior, context cancellation, and partial-failure semantics. Ensure every started request is joined or canceled and that concurrency does not introduce goroutine leaks or races.

## Multipart Upload Memory And Startup Latency

- For attachment or file uploads, trace when files are opened and copied relative to request creation and the first network write. A `bytes.Buffer` or equivalent multipart body assembled before `Do` makes peak memory scale with the total selected file size and delays transmission until every file has been read, even when functional tests pass.
- Confirm the complete request sequence, including preliminary list or metadata calls, retries, authentication, and cancellation. Distinguish heap-backed multipart buffering from operating-system file cache, transport buffers, and server-side response buffering when attributing memory growth.
- Benchmark matched 1 MiB, 32 MiB, and 128 MiB single- and multi-file selections. Record peak Go heap and process RSS, allocations, total duration, and time to first request byte; use a controlled local HTTP server or instrumented transport so network variance does not hide pre-send delay.
- Evaluate streaming with `io.Pipe` or an equivalent bounded-memory multipart producer, but include producer-error propagation and cancellation handling in the design. Preserve multipart field names, filenames, content types, file ordering, project/query metadata, authentication, response handling, and errors for missing, unreadable, empty, or partially read files.
- Define acceptance criteria requiring memory to remain bounded rather than proportional to aggregate upload size, earlier first-byte transmission for large uploads, unchanged wire semantics and errors, and no material latency or allocation regression for small uploads. Add regression cases for multiple files, cancellation during streaming, server rejection, reader failure after transmission starts, and cleanup without goroutine or file-descriptor leaks.

## Evidence And Filing

- Define before-and-after criteria before filing: expected unchanged-run wall-time/resource reduction, unchanged coverage semantics, acceptable freshness behavior, and the exact CI command or workflow boundary to change. For runtime findings, include the workload size, benchmark baseline, phase-level measurements, and output-preservation criteria.
- Re-run a final read-only sanity check such as `go build ./...`, `go vet ./...`, and the relevant coverage tests. Keep preliminary failed measurement attempts separate from successful evidence and state them as tooling failures, not repository failures.
- Before creating a Native SDLC notification, paginate `list_existing_automation_notifications` through `next_offset == 0`; inspect the body with `get_alert` for any plausible match and skip covered findings. Create at most one new `performance_suggestion`, with a plain-language `## Summary` first and technical evidence, implementation direction, acceptance criteria, file references, and regression cases afterward.
- Do not modify source, create implementation tasks, inspect GitHub issues for duplicate detection, or turn a speculative code smell into a performance finding without a measured cost or concrete measurement plan.
