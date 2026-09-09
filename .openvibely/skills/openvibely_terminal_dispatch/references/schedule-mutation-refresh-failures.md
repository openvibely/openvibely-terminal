# Schedule Mutation Refresh Failures

Use this reference when auditing or changing `/schedule` creation, deletion, or toggling that renders a refreshed schedule page after a successful write.

## Failure Rule

A successful schedule mutation and its follow-up schedule-page fetch are separate operations. In `internal/terminal/registry.go`, keep the mutation result independent from `GetSchedule`: do not discard the fetch error and call `renderSchedule(nil, "")` as though nil data were an authoritative empty schedule. If `add`, `delete`, or `toggle` succeeds but the scoped refresh returns a 5xx or transport error, preserve the confirmed action status and use a safe status-only or explicit unavailable-data result. Never render `nothing scheduled`, `0 scheduled`, or another empty-state claim for data that was not fetched.

This failure path is user-visible: a write can commit even when the subsequent GET times out or fails, so a false empty state can prompt a duplicate user action. Keep mutation-error handling separate: return the create/delete/toggle error before attempting the refresh, and never combine a failed mutation with a success message or refreshed-list output.

Normal successful reloads must retain the existing schedule table and output. Preserve project scoping on every follow-up GET, including the refresh after a mutation.

## Audit Steps

Trace each `add`, `delete`, and `toggle` branch through the write request, scoped page/list reload, error handling, and final rendering. Confirm that write and reload errors are handled independently and that a failed reload cannot be represented by nil data as a valid empty result. Compare `actAndReloadText` or other nearby status-preserving helpers, but verify the schedule-specific output contract rather than assuming a generic reload policy is safe for every resource.

Before filing, inspect both dispatch tests and client page-fetch behavior. Existing successful-mutation and mutation-error coverage is insufficient: the key missing cases are successful create, delete, and toggle followed by a failed schedule-page fetch.

## Regression Coverage

Prefer a table-driven HTTP dispatch matrix for `add`, `delete`, and `toggle`. For each action, cover a successful mutation followed by a scoped schedule GET returning 5xx and a transport failure. Assert that the confirmed status is present, empty-state text is absent, no false success/error combination appears, and every schedule GET carries the selected `project_id`. Also retain successful mutation plus successful reload coverage and mutation-failure cases; mutation failures must issue no follow-up refresh request.

When simulating a dropped HTTP connection, account for Go's default transport retry behavior on an idempotent GET: assert at least one failed refresh attempt or use a transport stub that makes retry behavior explicit instead of assuming exactly one request. Use a structured action-error response when the test needs to assert the backend's exact mutation-error message; status-only client errors may normalize plain `http.Error` text.

Run focused schedule tests followed by `go build ./...`, `go vet ./...`, and `go test ./...`.
