# Alert Card Pagination Regression Pattern

Use this reference when changing alert list parsing, continuation traversal, alert detail lookup, or alert dispatch/CLI actions.

## Continuation Validation

- Parse `X-OpenVibely-Card-Page-Has-More` strictly when present. Values that are not valid booleans are malformed transport metadata and must return a clear error rather than being treated as end-of-list.
- If a page explicitly advertises `has-more=true`, require usable semantic continuation metadata: a pagination root, card selector, and stable key. Missing or invalid metadata must fail visibly rather than return a successful truncated collection.
- Preserve the exact-boundary behavior: a first page containing exactly 20 cards does not imply another request unless the explicit continuation contract says more pages exist.
- Keep the existing traversal ceiling observable. A fixture that continually advertises a valid next page should fail with the established safety-limit error after exactly 200 total requests, not silently truncate or issue request 201.
- Validate continuation-at-limit immediately after accepting every page and before invoking any early-stop predicate. If an accepted page reaches the 200-page or 10,000-card ceiling while still advertising `has-more=true`, return the established safety-limit error even when that boundary page contains the requested record; never let a match bypass the limit check.

## Canonical Alert Detail Fast Path

- Restrict incremental early-stop lookup to the proven canonical alert identifier shape: exactly 32 lowercase hexadecimal characters. Keep titles, prefixes, substrings, uppercase IDs, malformed ID-like values, selectors, list filters, and mutation actions on the existing complete-list plus reference-matching path.
- A canonical-shaped reference is not proof that the user intended an ID. If incremental traversal completes without an exact ID match, pass the fully aggregated, validated, deduplicated cards from that same traversal through the existing reference matcher so a 32-lowercase-hex exact title, prefix, substring, ambiguity, and normal diagnostics retain prior behavior. Do not return `nothing matches` immediately, and do not start a second list traversal.
- Implement early stopping as a private predicate or callback over the shared bounded paginator, not as a second pagination implementation. Validate the matching page's headers, selector, offsets, page/card bounds, and continuation metadata before accepting the match.
- Preserve first-seen duplicate precedence. A canonical match on the current page returns without fetching a continuation solely to discover a duplicate; cross-page list deduplication behavior remains unchanged for full collection callers.
- Unknown canonical IDs must safely traverse to normal completion or the established pagination error/limit and must not issue a detail request. Preserve selected `project_id`, authentication/backend error classification, timeout, and cancellation on every continuation.
- Route only alert detail display through the incremental client method. Plain and JSON detail output must use the same resolved alert and remain equivalent to the legacy full-scan result.

## Regression Matrix

- At the client layer, cover multiple pages, stable first-seen order, overlapping-card deduplication, exact 20-card termination, malformed boolean headers, missing continuation metadata, continuation HTTP/parse errors, selected-project query propagation, and 200-page exhaustion.
- For canonical early-stop lookup, cover first, 50th, 51st, middle, and final-card boundaries; unknown IDs; duplicate IDs; a canonical-shaped exact title after an ID miss; matching-page malformed metadata; backend/auth failures; timeout; cancellation; project query propagation; and exact request counts.
- Add explicit continuation-at-limit hit regressions for both limits: a target on page 200 with `has-more=true`, and a target on a page that reaches exactly 10,000 cards with `has-more=true`. Both must return the safety-limit error, report no successful match, and make no request beyond the boundary; retain an unknown-ID exhaustion case alongside them.
- At interactive dispatch, use a real paginated `httptest` fixture and prove list/filter plus show, approve, reject, dismiss, read, and confirmed delete can resolve a record that exists only on page two. Assert canonical `show` uses early stop while every noncanonical reference and mutation retains complete-scan matching and diagnostics.
- At one-shot CLI dispatch, cover paginated JSON list/show and all later-page mutations. Preserve the destructive-action contract by requiring `--force` for delete, and compare canonical fast-path plain/JSON output against a legacy-path oracle rather than hand-authored fragments.
- Record every fixture URL and assert `project_id` remains present on continuation, detail, and mutation requests. Do not validate only the first list request.
- For performance claims, use controlled 10/100/1,000-card histories and separate list-request and response-byte counters. Include no-delay allocation measurements and fixed per-page-delay latency measurements. Collect an odd number of paired samples after warming both paths, alternate legacy/incremental execution order, sort duration copies, and directly calculate median plus nearest-rank p95 for each size/delay case. Report both paths and percentage improvements. Gate the large delayed median improvement and use combined relative-plus-absolute tolerances for p95 and 10-alert median regressions so scheduler noise alone does not fail the benchmark.
- Verify the large page-one case stops after one request while the legacy full scan traverses every continuation. Run the percentile benchmark explicitly with one benchmark iteration so its internal sample count remains controlled.
- Run focused regressions and benchmarks first, then `go build ./...`, `go vet ./...`, `go test ./... -count=1`, `gofmt -l`, and `git diff --check`. Pagination/parser changes do not require the race detector by default; add focused `-race` coverage only if concurrent fetching or shared state changes.
