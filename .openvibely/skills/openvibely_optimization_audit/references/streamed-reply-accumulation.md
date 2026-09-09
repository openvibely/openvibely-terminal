# Streamed Reply Accumulation Audits

Use this procedure when auditing or implementing interactive output that arrives in many small chunks, especially terminal chat replies.

## Trace The Per-Chunk Path

- Follow one incoming chunk from event handling through accumulated-string updates, transcript reconciliation, styling/wrapping, block construction, and viewport replacement.
- Do not assume an append or width-aware render cache makes the whole path incremental. Identify work performed before the cache lookup and whether every chunk still rebuilds or replaces all retained transcript blocks.
- Check whether immutable string concatenation copies the complete accumulated reply for each chunk. For final reply size `N` and chunk size `c`, there are approximately `N/c` updates and cumulative copied bytes are approximately `N^2/(2c)` under uniform chunks. State that this is a source-derived estimate unless directly measured.
- Separate the costs of reply-string copying, transcript reconciliation, styling/wrapping, cache lookup, and viewport replacement so the proposed fix targets the actual repeated work.

## Evidence And Measurement Plan

- Use a realistic matrix of final reply sizes, frame sizes, and retained transcript lengths. In `openvibely-terminal`, cover 10, 100, and 500 entries; 4, 64, and 256 KiB replies; and 32-byte and 1-KiB chunks.
- Benchmark the current and candidate production mutation path with identical chunk sequences. Record redraw count, per-update p95, total stream-processing time, allocations and bytes, and final-render latency. Save the unchanged baseline before implementation so the comparison is reproducible.
- Drive benchmark chunks through the production `Model.Update` message path rather than calling only the accumulation helper. Simulate the production arrival cadence, deliver actual cadence-render messages, and terminate through a real done/error event so styling, wrapping, mutable-block replacement, viewport content replacement, bottom scrolling, and forced terminal flushing are included.
- Measure render-update p95 from cadence-render `Update` calls, with ingestion outside each timed sample. A p95 of buffer appends alone does not substantiate user-visible render latency.
- Keep a matched legacy or baseline harness for critical and short-response comparisons. Report both time and allocated bytes, plus redraws per operation, so threshold claims remain reproducible and do not hide a small-input regression.
- Treat a 64 KiB reply in 32-byte chunks with 500 retained entries as the critical stress case, but also check 4 KiB replies for small-input regressions.
- If the task is strictly read-only, do not run benchmarks that may write caches or artifacts. A filing can instead use verified control-flow evidence, the explicit cumulative-work calculation, and a concrete matched benchmark plan; label estimates separately from measurements.
- Confirm the issue is current before filing. Historical transcript-redraw findings may already be implemented through block caching, while pre-cache accumulation or reconciliation remains non-incremental.

## Implementation Pattern

- Accumulate incoming bytes in an amortized buffer and advance reconnect offsets from buffered byte length, not rendered string or rune counts. This preserves byte-accurate offsets when UTF-8 sequences are split across frames.
- Bound redraw cadence with at most one outstanding timer. Guard timer ownership with the stream generation and the relevant submission, project, execution, cancellation, and authentication boundaries so a stale tick cannot redraw a replacement stream.
- Let ordinary deltas queue the cadence tick, but synchronously flush terminal `done` and `error` events, failed/cancelled status transitions, durable polling snapshots, disconnects before reconnect ownership advances, and authoritative reconciliation. A durable snapshot equal to buffered but not-yet-rendered output still requires a visibility flush.
- On an auth-required stream disconnect, flush queued output after stale ownership checks but before auth/session handling invalidates stream ownership. This preserves the last accepted partial reply without weakening generation, project, submission, or execution guards.
- Do not create an empty agent transcript block for an empty terminal frame.
- When transcript cache shape and render width match, restyle and replace only the active mutable agent block. Preserve immutable cached blocks and bottom-oriented viewport replacement; fall back to the established full rebuild after width changes, cache mismatch, history mutation, or eviction.
- Keep buffered stream state distinct from rendered state. Rendering cadence may lag, but protocol offsets, cancellation decisions, and final reconciliation must observe the authoritative buffered bytes immediately.

## Acceptance And Regression Criteria

- Require total processing cost to scale near-linearly with delivered bytes for fixed retained history, rather than with the sum of all intermediate reply lengths.
- Set a measurable improvement target for the large-reply/small-frame case while requiring no material regression for short replies or large frames. Verify redraw count is cadence-bounded and forced flushes remain immediate.
- Preserve byte-identical final transcript text, Markdown/styling output, wrapping at each supported width, scrolling/follow behavior, cancellation, error handling, completion transitions, reconnect semantics, and cache invalidation after resize or history changes.
- Add regressions for coalesced redraws; forced `done` and `error` flushes; failed/cancelled status; polling and disconnect flushes; auth-required disconnect flush-before-invalidation; equal authoritative reconciliation; split UTF-8 byte accounting; canonical rendered bytes; immutable-block preservation; full-refresh fallback after width, cache-shape, and history mismatch; bottom-oriented replacement; empty terminal frames; and truncation beyond 500 entries.
- Run `go build ./...`, `go vet ./...`, `go test ./... -count=1`, the complete benchmark matrix, repeated critical benchmarks, and `git diff --check` after implementation.
- Before filing, paginate all existing automation notifications and inspect plausible matches. Distinguish an already-covered full-transcript redraw issue from a new per-chunk accumulation or reconciliation issue, and do not duplicate either.
