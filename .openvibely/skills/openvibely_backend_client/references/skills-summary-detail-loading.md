# Skills Summary/Detail Loading

Use this guidance when changing skill catalog reads in the Go client or terminal dispatch.

## Retrieval Split

- Treat ordinary skill lists and ref-less pickers as metadata-only reads. Do not parse, transfer into `client.Skill`, or retain instruction bodies from list-card attributes such as `data-skill-content` when the caller cannot display them.
- Preserve the existing complete-content JSON list contract through an explicit content-bearing path, such as `ListSkillsWithContent`, that enriches resolved summaries only when JSON output requires `content`.
- Resolve `/skills show <ref>` against summaries first, then fetch exactly one scoped body for the selected result through a detail method such as `GetSkillDetail(ctx, projectID, handle, scope)`.
- When the input is a supported canonical project-scoped handle, bypass catalog enumeration: perform one scoped detail request and no list request. Retain existing summary-based matching for global, prefix, substring, duplicate, or ambiguous references rather than broadening the fast path.
- A speculative project-scope fast path must not prevent global-scope resolution. The backend returns `503` when no project skill root is configured even though `GET /skills` can list global summaries. On that scope-unavailable result, fall back to summary matching and fetch the matched scope; preserve authentication, transport, malformed-response, and unrelated server errors instead of masking them as fallback candidates.
- Keep the body read project and scope aware. Preserve source, enabled, always-use, action metadata, ordering, de-duplication, limits, error wording, cancellation, and post-mutation refresh behavior from the pre-split flow.

## Bounded Content Enrichment

- Only parallelize the content-bearing enrichment path. Plain interactive lists, summary-only CLI output, pickers, and single-skill show must retain their metadata-only or single-detail request behavior.
- Benchmark the unchanged serial implementation against a bounded, order-preserving candidate before changing production behavior. A controlled `httptest` fixture should keep the server, transport, response sizes, initial catalog latency, cache state, and request count equivalent while varying catalog sizes such as 10, 100, and 1,000 skills and detail delays such as 0, 25, and 100 ms.
- Use a fixed worker pool or semaphore, not one goroutine per skill. The bound must be observable in tests and benchmark reports; an eight-detail worker bound is an acceptable starting point when measurements justify it, but do not treat the number as universal without evidence.
- Store each fetched detail in its catalog-indexed result slot and merge only after all workers have joined. This preserves catalog order and avoids concurrent writes to shared output state even though request arrival order is nondeterministic.
- On detail failure, cancel derived work promptly, but always join started workers before returning so no goroutines or requests are leaked. Preserve the existing HTTP, authentication, transport, JSON, trailing-data, identity, project, scope, and escaped-handle semantics. If concurrent failures arrive in different orders, select the error according to the original catalog order where the prior serial contract exposed deterministic first-error behavior; retain a cancellation error as a fallback when every recorded failure is cancellation-related.
- Consider a small-catalog serial fallback when zero-delay measurements show that worker startup and concurrent transport overhead violate the allocation/latency budget. The fallback threshold must be justified by the controlled measurements and covered by an in-flight-bound regression; do not add unbounded fan-out merely to avoid this tradeoff.
- Prove structural or byte-equivalent serial/candidate JSON output, including complete bodies, metadata, enabled/always-use fields, mixed global/project scope, escaped handles, empty and one-skill catalogs, large catalogs, and request count. Add wave/bound tests that demonstrate the configured maximum rather than relying only on elapsed time.

## Measurement And Regressions

- Report serial and candidate results separately by size and delay, including median and p95 wall time, allocations, allocated bytes, request count, and maximum in-flight detail requests. Run at least 10 samples where practical and sort latency samples before calculating p95; execution order is not sample order.
- Keep cancellation/deadline tests deterministic with a server that blocks or delays details, then assert prompt return after started work is joined, bounded in-flight requests, and no leaked workers. Cover first, middle, and final detail failures plus authentication, transport, malformed JSON, trailing JSON, identity, and scope errors.
- Because concurrent request arrival reorders server-side capture, assert scoped request identity as an order-independent set while asserting returned JSON order separately. Do not weaken the output-order contract to match network arrival order.
- Retain focused client and terminal regressions for summary-only no-body requests and single-skill show request counts before full repository validation. For transport concurrency changes, run the narrowest relevant `go test -race` coverage in addition to `go build ./...`, `go vet ./...`, `go test ./... -count=1`, `gofmt -l`, and `git diff --check`.

## Regressions

- Use request counters in `httptest` handlers to prove plain lists and ref-less selectors make no detail/content transfer, canonical show makes one scoped detail request and zero list requests, and resolved fuzzy show makes one list resolution plus one selected detail request.
- Cover a global-only catalog with no project skill root: the speculative `scope=project` detail response is `503`, summary resolution identifies the global skill, and the final global detail request succeeds. Verify auth, transport, decode, and unrelated server failures do not incorrectly trigger this fallback.
- Cover project mismatch, exact `/login` redirects, non-login redirects, transport errors, continuation/pagination errors, cancellation, empty and overlapping pages, duplicate/ambiguous/unknown references, and fresh content after a mutation.
- Assert user-visible plain list, picker, show, and complete `--json` output remain stable. For JSON, verify every expected item still has its complete `content` value and empty collections retain their established shape.
- Benchmark content-bearing versus summary fixtures at representative 100- and 1,000-skill catalog sizes. Measure decoded/request payload and allocations as well as median and p95 latency; make the threshold test deterministic enough to catch accidental body parsing or storage without relying on an absolute machine-specific speed alone.

## Validation

Run focused client and terminal dispatch tests first, followed by `go build ./...`, `go vet ./...`, `go test ./... -count=1`, `gofmt -l`, and `git diff --check`. Treat output from the final formatting or whitespace checks as a failure.
