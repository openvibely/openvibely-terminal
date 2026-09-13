# Task Goal Mutation Contract

Use this note when extending task completion-goal mutations or adding task-goal lifecycle actions.

## Scope And Routes

- Every goal mutation must accept the selected project ID and append it as a URL-encoded `project_id` query parameter, including set, clear, pause, and resume. Do not rely on backend default-project fallback.
- Preserve existing public set/clear method signatures when compatibility requires them; add explicit project-scoped methods or a private scoped helper and route new terminal callers through the scoped path.
- Verify exact backend methods and paths from the current contract. The pause and resume lifecycle actions use `POST /tasks/<taskID>/goal/pause?project_id=<selectedID>` and `POST /tasks/<taskID>/goal/resume?project_id=<selectedID>`.
- Add request-level tests with a non-default project containing URL-reserved characters, asserting the decoded query value and exact method/path. Test backend failures and ensure callers do not report success after a non-2xx response.

## Compatibility

- Keep legacy set syntax unchanged. A pipe-delimited objective remains a normal set-goal request, so `tasks goal <task> | pause` sets the objective text `pause`; only the explicit nested forms `tasks goal pause <task>` and `tasks goal resume <task>` invoke lifecycle routes.
- Verify that pause/resume preserve the existing goal rather than supplying or replacing an objective. Cover the same task-reference resolution and project scope for interactive and headless paths.

## Validation

For client/transport changes, run focused route and error tests, then `gofmt -l`, `go build ./...`, `go vet ./...`, `go test ./... -count=1`, and `git diff --check` from the active TUI worktree. Treat any formatter or whitespace output as a failure.
