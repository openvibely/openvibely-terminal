# Bounded Analytics Result Contract

Use when a terminal analytics view displays only a fixed top `N` execution-time rows but the existing API client decodes complete agent or task histories.

1. Keep the existing full-history client method and its public signature for callers that need all rows. Add an explicit opt-in bounded method or private scoped helper that appends `limit` only when the requested value is positive.
2. Send both the selected `project_id` and `limit=N` only from views whose visible contract is the top `N` execution-time rows. Do not add the limit to unrelated analytics requests.
3. The backend must produce the same visible result before bounding: order all eligible rows by average execution time descending, preserve original/source order for equal averages, then take the first `N`. Never truncate an unordered source collection before ordering.
4. Once the bounded endpoint guarantees this order, render the returned rows directly. Do not fetch the full payload and repeat client-side sort/select work in bounded callers.
5. Preserve the existing concurrent analytics fan-out, context cancellation, authorization/project-scope propagation, and distinct error behavior for a single requested section versus all-sections rendering.

Regression coverage should:

- Assert legacy/full-history calls omit `limit`, bounded calls send exactly `project_id` and `limit`, and unrelated analytics endpoint queries stay unchanged.
- Compare bounded agent and task output against the legacy visible top-`N` output with at least `N+1` rows.
- Include cutoff ties, zero and negative values, display names, and ID fallback labels so stable ordering and rendering are verified rather than only row count.
- Exercise agent-only, task/trends-only, and all-sections paths, including project scoping and established error semantics.

For client/terminal transport changes, run focused tests first, then `go test ./... -count=1`, the narrow relevant `go test -race` invocation when the concurrent load path is touched, `go vet ./...`, `gofmt -l`, `git diff --check`, and `go build ./...`.
