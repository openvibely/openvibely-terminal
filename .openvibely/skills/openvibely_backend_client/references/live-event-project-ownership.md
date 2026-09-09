# Live-Event Project Ownership

Use this guidance when implementing or auditing `/events/live` consumers in the OpenVibely TUI or headless CLI.

## Ownership Invariant

- Always send the selected project as `project_id` on the `/events/live` request.
- Treat any payload with a non-empty `task_id` as task-scoped. Accept it only when its trimmed `project_id` is non-empty and exactly equals the selected project ID.
- Drop task-scoped payloads with an omitted, blank, or foreign `project_id`; never synthesize selected-project ownership for them.
- Preserve legacy compatibility only for taskless project/chat payloads whose `project_id` is omitted. Those may be associated with the selected project for rendering.
- Apply the same predicate before both plain-text and JSON rendering so output mode cannot alter ownership filtering.

## Regression Shape

Use an `httptest` SSE endpoint that emits, in order, an unscoped task, a foreign-project task, an exact selected-project task, and a taskless unscoped chat event. Run the test in plain and JSON modes and assert:

- Only the exact-project task and taskless unscoped chat are rendered.
- The unscoped and foreign tasks are absent.
- JSON remains line-oriented and assigns the selected project only to the compatible taskless event.
- The stream request is exactly project-scoped, for example `/events/live?project_id=p1`.

This contract was implemented and independently audited clean in task `a2e9e48f5b488197ffe0e82510b70fb1` (`Fix CLI live-event project ownership`).
