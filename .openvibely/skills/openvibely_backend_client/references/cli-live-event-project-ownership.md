# CLI Live-Event Project Ownership

Use this reference when changing the headless CLI formatter or SSE consumption for `GET /events/live`.

## Ownership Predicate

- Keep the selected project as the `project_id` query on `/events/live`.
- Treat any event with a non-empty `task_id` as strictly project-scoped: render it only when `project_id` is non-empty and exactly equals the selected project ID.
- Drop task events whose `project_id` is missing or belongs to another project. Do not synthesize the selected project before applying this check.
- Preserve legacy compatibility for taskless unscoped project/chat events: after the task-ownership gate, a missing `project_id` may be synthesized from the selected project for rendering.
- Apply the same predicate before both plain-text and JSON formatting so output modes cannot diverge.

## Regression Pattern

Use an `httptest` SSE server that emits, in order, an unscoped task event, a foreign-project task event, an exact selected-project task event, and a taskless unscoped chat/project event. Run the fixture in plain and JSON modes and assert:

- Only the exact-project task and taskless unscoped event render.
- The unscoped task and foreign task never appear.
- The taskless event receives the legacy selected-project value where the output contract exposes it.
- The request path remains `/events/live` and its query contains the selected `project_id`.

Run focused SSE tests first, then `go build ./...`, `go vet ./...`, `go test ./... -count=1`, `gofmt -l`, and `git diff --check`. Because live-event ownership includes concurrent stream behavior, run the narrowest relevant SSE/transport tests under `-race`; expand to `./...` only when the behavior crosses package boundaries or the task explicitly requires it.
