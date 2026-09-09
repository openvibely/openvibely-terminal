# Completed Request Error Extraction

Use this reference when consolidating duplicated non-nil error branches in asynchronous `internal/tui/model.go` message handlers.

## Extraction Boundary

- Extract only handlers with genuinely identical completed-request error policy. Do not route specialized partial-output, retry, refresh, chat, login, SSE, or other operation-specific paths through the helper merely because they also receive an error.
- Keep freshness and scope guards in each handler before invoking shared error handling. A stale message must return before changing `busy`, authentication state, connection state, transcript output, confirmation state, or success state.
- Keep handler-specific success behavior outside the error helper, including selected-project updates, thread state, attachment confirmation, and any returned command.
- Prefer a narrow private `Model` method that returns whether it consumed the error. A nil error should return false; every non-nil error should return true so callers can return immediately without duplicating policy.

## Error Precedence

Handle non-nil completed-request errors in this order:

1. Authentication classification and the existing sign-in transition.
2. Transport/offline classification and the existing connection-state and recovery guidance transition.
3. One unchanged ordinary transcript error entry.

Authentication must win when an error satisfies more than one classifier. Each branch must emit its transition or transcript entry exactly once.

## Regression Strategy

- Add a table-driven matrix over every migrated message type rather than proving the helper only in isolation.
- Include authentication, transport, ordinary, stale, and success cases for each handler.
- Use a deliberately dual-classified test error to prove authentication precedence over transport handling.
- Count recovery guidance and raw error entries where duplicate emission is a risk; do not rely only on substring presence.
- Assert each handler's distinct successful state and side effects, and assert stale messages leave all relevant state and output unchanged.
- When feasible, run the focused regression before extraction to establish the current behavior as a preservation baseline, then rerun it after the production edit.

## Validation

Run `go build ./...`, `go vet ./...`, `go test ./... -count=1`, and `git diff --check` from the active task worktree. When the changed behavior requires concurrency validation, add the narrowest relevant package or tests under `-race`; use `./...` only when the behavior crosses package boundaries or the task explicitly requires it.
