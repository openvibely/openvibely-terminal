# Canonical Task Detail Fast Path

Use this guidance when optimizing `tasks show` or another detail command that currently lists an entire project board before fetching a resource already identified by a canonical ID.

## Routing Rules

- Restrict direct lookup to the backend's proven canonical identifier shape. For current task IDs, that is exactly 32 lowercase hexadecimal characters.
- Keep titles, prefixes, uppercase IDs, selectors, mutations, and every noncanonical reference on the existing list-and-`matchRef` path. Do not issue speculative detail requests for ambiguous references. Review output may use the direct path only if every metadata and review request satisfies the same scope and parity checks below.
- Require a selected project before dispatch and include the selected `project_id` on every direct request.
- On unknown, malformed, or foreign direct-lookup responses, return the established error. Never fall back to a board scan, because fallback changes request cost and can weaken project isolation.

## Project Isolation

- A `project_id` query parameter is not proof that an endpoint is project-scoped. Inspect the authoritative handler and repository call to verify the parameter is consumed before data access, preferably through a project-qualified lookup such as `GetByIDForProject`.
- Client-side response validation prevents display of foreign metadata but does not make a global backend lookup safe. A route that globally loads a task or related children and rejects the foreign project only after receiving the response still violates a strict project-isolation or no-speculative-lookup requirement.
- Do not use `GET /api/tasks/:id/swarm` as the metadata source for this fast path under the current backend contract. Its handler ignores `project_id`, calls global `GetByID`, and loads swarm children before the client can reject a foreign project response. It can also make valid detail reads fail because of an unrelated secondary endpoint.
- Before rendering or launching lazy tab requests, require the HTML detail response to contain the exact requested task ID and selected project marker. If any additional direct source is considered, verify handler-level project qualification first, then independently validate task and project identity in the response.
- Keep strict validation in narrowly named client methods or private helpers used only by the canonical fast path. Do not silently tighten an established permissive detail API used by legacy paths.

## Output Compatibility

- A list-bypass optimization removes the board card that previously supplied `Task` metadata. Inventory every field consumed by plain detail headers, review headers, and JSON, including title, prompt, category, status, display order, and badges.
- Compare exact legacy board-derived values with the direct result, not merely semantically similar values. The current board projection truncates the raw prompt to 300 Unicode code points before HTML text extraction folds all whitespace runs and trims the result. Direct previews must preserve that exact operation order: truncate first, then normalize with the same whitespace semantics as card parsing.
- Reconstruct badge output only from sources proven equivalent to the board template. The detail page's `Model:` label is not equivalent: for an implicit model it says `Default model`, while the board card emits the configured default model name when models exist and no model badge when the model collection is empty. Apply the same source-parity check to agent, tag, priority, goal, chain, chained, and swarm badges and preserve board order.
- Preserve nonzero `display_order`, prompt-preview semantics, and a non-nil empty badge slice so JSON remains byte/shape compatible where promised.
- Use current detail HTML only for fields whose template representation is demonstrably equivalent. Do not assume the detail page repeats card attributes or heading elements; inspect the actual handler/template and copy realistic markup into fixtures.
- If no project-qualified direct source can reproduce a board-derived field, do not silently use an unscoped endpoint or emit a guessed/zero value. Either derive it safely from validated scoped markup, retain a scoped board lookup for that output mode, or explicitly narrow/change the requirement with user approval.
- Preserve selected-tab behavior, partial-detail errors, authentication classification, context cancellation, and request economy. Metadata-only JSON or review branches should not gain unrelated lazy panel requests.

## Regression Evidence

- Establish an oracle from the real legacy path. Decode and compare the complete old and new `Task` values for canonical IDs rather than asserting hand-authored expected fragments that may encode the new implementation's assumptions.
- Include realistic cases for a 301-plus-code-point prompt with leading, repeated, tab, and newline whitespace; nonzero display order; implicit/default, explicit, unknown, and absent model catalogs; absent/present agent; tag and priority; goal; chain; parent/chained; swarm; and an empty badge list.
- Use an `httptest` server with separate counters for board, detail, metadata, reviews, and lazy panel routes. Assert canonical show makes zero board requests and exactly the intended safe direct requests; unknown or foreign IDs must make no fallback or lazy requests.
- Add focused cases for exact success, HTML task-ID mismatch, HTML project mismatch, any secondary metadata mismatch, auth errors on each request leg, cancellation, selected tabs, JSON output, review output, and noncanonical references retaining board matching.
- When evaluating a candidate endpoint, add or inspect a backend contract test proving the handler consumes `project_id` before task and related-record lookup. A client fixture that merely checks the query string or returns a foreign payload does not establish backend isolation.
- Keep fixtures structurally faithful to current backend templates. Synthetic convenience elements or mutually inconsistent JSON/HTML metadata can hide production parity failures.
- Compare against a large synthetic board, such as 2,500 cards. Record board-request count, elapsed time, and allocations, and enforce the requested improvement threshold deterministically rather than relying only on an informal benchmark.
- Finish transport/dispatch changes with focused tests, `go build ./...`, `go vet ./...`, `go test ./... -count=1`, `gofmt -l`, and `git diff --check`. Add focused race-detector coverage only if concurrent detail loading, cancellation, or shared state changes.
