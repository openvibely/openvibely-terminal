# Inbound Webhook Lifecycle Contract

Use this reference when extending or auditing terminal support for project-scoped inbound webhooks.

## Establish The Backend Contract

- Inspect the current backend handler, persistence model, and templates or Swagger before changing webhook resolution. The established lifecycle is rooted at `/channels/webhooks`; CRUD is form-based, while detail and the test/rotation success responses have JSON branches when HTMX is not requested.
- In the current backend contract, a canonical inbound-webhook ID is a globally unique SQLite primary key of exactly 32 lowercase hexadecimal characters. Treat this grammar and lower-case case contract as strict and backend-derived, not as a general "hex-like" heuristic. Add a grammar-locking unit test whenever this fast path is changed.
- Carry the selected project ID on list, detail, create, edit, test, rotate, and delete. A direct detail request must remain project-scoped even though the stored ID is globally unique.

## Reference Resolution

- For a fully validated canonical ID, call the scoped `GetWebhook` detail endpoint first rather than fetching the paginated card catalog merely to rediscover the ID. Apply this fast path to `show`, `edit`, `test`, `rotate`, and `delete`.
- Only a typed detail `404` may fall back to the existing catalog resolver. Authentication/authorization failures, transport errors, `5xx`, decode errors, malformed detail, identity mismatch, and foreign-project detail are terminal; never turn any of those into a catalog scan or a different target.
- Keep catalog resolution for names, aliases, prefixes, substrings, malformed or uppercase ID-like strings, and option-like values. Traverse every advertised continuation, preserve first-seen order, deduplicate by stable webhook ID, then retain the established exact-ID/name, unique-prefix, unique-substring, case, and ambiguity behavior.
- The fallback after a typed canonical-ID `404` must use that same catalog behavior. It preserves legitimate noncanonical/reference matching without broadening direct-detail error handling.
- Interactive selectors and ordinary list views remain catalog-backed. Do not replace selector/list fetching with direct-detail logic.

## Detail Response Trust Boundary

- Decode detail into a complete, secret-free allowlisted struct. Before rendering it or using it in a lifecycle action, validate that the returned ID is present and exactly equals the requested canonical ID, and that its project identity matches the selected project when the response carries project scope.
- For pointer or optional string IDs, presence and equality are insufficient: reject an empty or whitespace-only returned ID before equality. Otherwise an empty response ID can pass comparison when the caller’s requested ID is also empty, allowing malformed detail to be trusted.
- Treat malformed, mismatched-ID, or cross-project detail as terminal errors. A same-project but wrong-ID response is a mutation hazard, not a harmless equivalent lookup.
- Reuse the verified detail object for `show` and for lifecycle verbs requiring current configuration. For form-replacement edit, preserve all complete non-secret configuration fields, including explicitly empty template fields, while applying only requested changes.

## Mutation Semantics

- Resolve and validate the canonical target before creating a confirmation closure. Capture that resolved object in the deferred closure; never resolve the raw reference again after confirmation.
- Preserve interactive confirmation, headless `--force`, cancellation, action-specific request routes, aliases, selected-project scope, and `show` plain/JSON output. Ambiguous, unknown, canceled, unforced, malformed-detail, and project-mismatch paths must issue no mutation.
- Decode test and rotate results into narrow safe result structs. Do not retain or expose secret fields in list/detail/create/edit/test output, JSON, diagnostics, or errors; rotation may render only its explicit newly usable endpoint result.
- Keep inbound-webhook lifecycle commands separate from legacy outbound channel action dispatch so `/channels` behavior does not regress.

## Regression And Performance Evidence

- Client coverage must test direct canonical success, typed-404 catalog fallback, every non-404 terminal class, strict grammar/casing, selected-project queries, malformed/mismatched/foreign detail rejection, complete edit form replacement, and secret absence. Add absent-ID, blank-ID, and same-project wrong-ID cases; exercise a blank requested ID with a blank response ID so equality cannot mask malformed identity.
- Dispatch and CLI coverage must test all five fast-path actions, direct-detail reuse where applicable, text-reference catalog resolution, exact/prefix/substring ambiguity, aliases, selector/list catalog reads, show plain/JSON stability, confirmation cancellation, `--force`, and captured target identity.
- Add a same-project mismatched-detail safety regression for every mutation-capable fast-path action (`edit`, `test`, `rotate`, `delete`). Assert no mutation request, no confirmation success, and no catalog fallback after invalid direct detail.
- For scan-elimination work, use deterministic paginated fixtures with a fixed page size and controlled server delay. At 10, 100, and 1,000 cards, record catalog request count, catalog bytes, elapsed time, and allocations. A known canonical ID must make zero catalog requests and transfer zero catalog bytes; names, partials, malformed IDs, and option-like references must retain the expected catalog behavior. With 50-card pages, the name-path scan shape is 1, 2, and 20 pages respectively.
- Finish transport/dispatch changes with focused tests, then `go build ./...`, `go vet ./...`, `go test ./... -count=1`, `gofmt -l`, and `git diff --check`. Add focused race coverage only when async dispatch, cancellation, or shared state changes.
