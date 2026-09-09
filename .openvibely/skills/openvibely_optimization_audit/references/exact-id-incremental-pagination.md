# Exact-Identifier Lookup With Incomplete Detail Responses

Use this guidance when an exact resource identifier currently triggers eager pagination of a collection before a detail request.

## Choose The Fast Path From The Response Contract

- Inspect both the collection-card and detail response contracts before proposing that exact IDs bypass collection lookup. The detail response may omit title, status, summary, project membership, or other fields rendered by the command.
- Determine whether collection lookup also enforces existence, project scoping, authorization, or canonical-reference validation. Do not bypass those semantics merely because the identifier syntax is valid.
- If the detail response is sufficient and the backend safely validates scope and existence, measure a direct-detail fast path.
- Check lifecycle-specific visibility before treating a detail miss as definitive. If draft or otherwise non-public resources are present in collection metadata but the detail endpoint returns `404`, call detail first for a validated exact ID and fall back to collection resolution only for that documented miss. Do not fall back on authorization, transport, cancellation, or server errors, and preserve the original not-found and draft rendering behavior with explicit regression cases.
- If list-card data or collection membership remains necessary, retain pagination but fetch pages incrementally and stop as soon as the canonical exact ID is found. Do not materialize every page before resolution.
- Keep fuzzy title and partial-ID matching on the complete existing resolution path unless the backend provides equivalent indexed lookup semantics.
- Treat identifier syntax as a fast-path hint, not proof that the user's reference can only be an ID. Inspect the existing matcher precedence first. If an exact title or alternate key may legally have canonical-ID shape, an ID miss must resume or preserve the full matcher without refetching already inspected pages; add a regression where a canonical-shaped token matches a title but no resource ID.

## Measurement And Acceptance

- Compare collection sizes such as 10, 100, and 1,000 cards, with the target on the first, middle, and last page plus a not-found case.
- Record list-page request count, transferred bytes, median latency, p95 latency when required, allocations, and the required detail request separately. Use controlled per-request delay when needed to expose pagination latency.
- Do not use standard Go benchmark `ns/op` as evidence of median or p95 latency: it is an arithmetic mean. When percentile requirements exist, collect enough per-operation durations in a dedicated test or benchmark harness, sort them, report the defined median and p95 indices, and use enough repeated samples to make tail claims credible. Compare matched baseline and candidate workloads, including delayed large collections and ordinary small collections.
- For incremental lookup, acceptance should require early-page exact IDs to stop pagination immediately and avoid materializing later pages. Last-page and not-found behavior may still scan all pages and must preserve current diagnostics.
- Preserve rendered card/detail fields, project scoping, authorization, ambiguity handling, malformed-ID behavior, page-boundary matches, cancellation, and backend errors.
- State the bound honestly: incremental lookup improves common early matches but does not make worst-case work constant. A true constant-request fast path requires a sufficient detail endpoint or a dedicated exact-lookup endpoint.
