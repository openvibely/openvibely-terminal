# Compact Project-Scoped Status Counts

Use this reference when one-shot project status needs aggregate alert/task counts but the existing client path downloads full HTML card collections.

## Contract First

- Inspect the backend handlers, route registration, and generated Swagger before choosing an endpoint. Do not substitute an existing aggregate route unless its predicates and project scoping exactly match the status contract.
- If backend changes are in scope, add the narrowest read-only JSON response that contains only the fields consumed by status. Require a non-empty `project_id` and verify that the handler actually scopes the query to that project.
- Add Swagger annotations and regenerate checked-in API docs when the repository has a route-contract test; a working handler alone is insufficient.
- Do not silently fall back to `/alerts` or `/tasks` collection downloads when the compact contract is unavailable. Return the typed endpoint/API error or document the compatible narrow implementation.
- Treat the compact backend contract as unavailable until its implementation is committed and reachable from the designated integration/default or release ref. A route present only in a private task worktree, an uncommitted diff, or a non-delivered branch cannot support a client completion claim; record the delivered backend SHA and ref before relying on it.

## Board-Equivalent Task Predicates

- Derive compact task predicates from the historical `/tasks` card projection, not from the raw `tasks` table or the endpoint field names. A query that counts every project row with `category = 'active'` or `status = 'queued'` can silently diverge from the client, which previously counted parsed board cards.
- Preserve board visibility exclusions: omit swarm-child roles, chat tasks, and ordinary scheduled tasks. Include only the scheduled rows that the board exposes, such as a running scheduled row or a scheduled automation occurrence marked as capacity-queued by the backend's reservation/outbox contract.
- Preserve board normalization: active rows with terminal `failed` or `cancelled` status are moved out of the active board projection and must not inflate `active_tasks`; cover this explicitly rather than assuming category alone is sufficient.
- Keep project scoping on every predicate and subquery, including automation reservation/outbox checks. Prefer one aggregate SQL query or another bounded compact implementation; do not restore full task-card loading merely to reproduce visibility semantics.
- Add backend regressions for chat, ordinary scheduled, terminal active, swarm-child, selected scheduled, and foreign-project rows. Assert both `active_tasks` and `queued_tasks` against the old rendered-card behavior, including the expected exclusion of swarm children.
- For the selected-scheduled regression, create a scheduled task with `status = 'queued'` plus the same durable unfinished dispatch/outbox and task-run reservation rows used by the board projection. Verify it increments `queued_tasks`; keep a second scheduled queued task without that reservation and verify it remains excluded. Exercise each accepted unfinished dispatch state (`pending`, `processing`, or `submitted`) only if the board contract treats them equivalently, and ensure the reservation and dispatch belong to the same task.

## Client And Terminal Integration

- Add dedicated compact client methods and reuse the established JSON transport helper so authentication, HTTP/decode, transport, and context-cancellation classification stay consistent.
- Forward the selected `project_id` on every scoped count request, including both concurrent requests. Validate missing scope before issuing HTTP.
- Keep the two independent count requests concurrent, merge their results after both complete, and preserve existing best-effort partial-failure semantics: a failed side must not erase a successful side, while authentication errors retain their established classification.
- Preserve the exact status predicates exposed by the backend contract: pending alerts use the alert decision badge, while task counts must represent the board-equivalent active and queued projections described above. Do not weaken or alter ordinary full alert/task list methods, pagination, parsing, ordering, or rendering.
- Preserve zero-project and ambiguous-project behavior, global capacity/auth checks, output text, and one-shot wave ordering: discover/select exactly one project before starting scoped counts.
- Never convert a failed, missing, or undecodable compact count response into a normal numeric zero without an explicit unavailable/error state. Otherwise an older backend that returns 404 can make real pending or active work appear absent while the best-effort path reports success.

## Regression And Measurement

Use deterministic request barriers and counters rather than elapsed time alone. Cover mixed and empty states, exact non-default project scope, no full-list fallback, auth/transport/cancellation errors, one-count failure with the other count retained, delayed overlap, zero/ambiguous selection, unchanged full-list behavior, and unchanged rendered output.

Record an allocation benchmark or profiling run at representative 100, 1,000, and 5,000 card-equivalent workloads. Assert that compact response bytes and allocations remain bounded rather than scaling with card HTML; a compact response test should also reject requests to full collection endpoints. Capture the exact benchmark command, observed output, environment, and final commit instead of relying on a narrative result or a constant-size fixture alone.

For a transport-focused Go change, finish with focused tests, `go build ./...`, `go vet ./...`, `go test ./... -count=1`, `gofmt -l`, and `git diff --check`. If backend edits are in scope, run the backend handler/route-contract tests and the repository suite separately so unrelated baseline failures are reported without obscuring the changed-contract result. After committing/delivering each repository, bind the validation note to the exact final SHA and dependency SHA, then perform a fresh read-only audit from those delivered refs.
