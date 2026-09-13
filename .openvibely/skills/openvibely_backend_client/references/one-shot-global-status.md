# One-Shot Global Status With Partial Project Discovery

Use this guidance when changing the OpenVibely terminal client's one-shot `status` command or startup preflight.

## Execution Contract

- Do not make `/api/projects` a mandatory preflight for one-shot global status. Start project discovery in the same wave as independent global health, authentication, and capacity checks.
- Construct commands before launching concurrent work, keep mutable model state out of goroutines, store each result independently, then apply messages to the model in an established deterministic order, currently projects first and global connection/capacity second. Concurrency must not change output or error precedence.
- Propagate the CLI context into every request so cancellation promptly stops the command and cannot leave overlapped global checks or scoped counts running after the command returns.
- Retain a project-list failure for the command's final nonzero result, but continue independent global checks. Render every trustworthy global status row and an explicit projects-unavailable or partial-status row so stdout cannot be mistaken for complete success.
- Keep failure classes distinct: authentication-required is reachable and nonzero, transport failure is offline, and a project-list HTTP/decode failure is partial rather than fully offline when global checks still succeed.
- Select project scope only when discovery yields exactly one unambiguous project. With zero, multiple, or failed project discovery, issue no scoped requests. Once the project result has selected exactly one project, launch both project-scoped alert/task counts immediately while the first-wave global checks continue; do not wait for capacity/auth completion merely to begin the scoped requests.
- Keep the five-request shape unchanged: one projects request, the existing global capacity/auth requests, and one request for each scoped count. Forward the exact selected `project_id` on both scoped requests, and run those independent counts concurrently.
- Apply results only after all started work has been joined, in the established deterministic order: resolve project selection, apply global connection/auth state and precedence, then apply count results. A concurrent global auth failure must retain its precedence without causing an already-started valid count result to be discarded solely because the global check advanced a generation before the deterministic drain; reconcile the one-shot result with the current state at drain time while retaining stale-result protection for genuinely obsolete work.
- Do not allow local preflight or error-return branches to exit after scoped counts have launched but before their result channel or wait group has been drained. Every started request must either complete or observe context cancellation before return.
- Call each endpoint at most once. Preserve the healthy single-project path and avoid changing interactive `/status` dispatch when the requested contract is specific to one-shot CLI orchestration.
- Keep static help and command-discovery paths backend-independent.

## Regression Matrix

Add CLI-level tests, not only renderer or client tests, for:

- Healthy single-project status with expected global rows and project-scoped counts.
- A synchronization-barrier test proving projects, capacity, and authentication all enter the first request wave before any is released; after unique project selection, separately prove alerts/tasks start before the delayed global checks are released.
- Per-endpoint counters proving every endpoint is called at most once and the exact selected project query reaches both scoped count requests.
- Controlled delayed-server trials for the 20/200/80 ms case. Compare repeated medians against the old three-wave path and require at least the agreed latency improvement, with a target no higher than 250 ms for the 311.8 ms baseline.
- Balanced-delay trials where overlap provides little benefit and slow-project-discovery trials where it cannot help. Assert no material regression and no more than the agreed 5% regression when the overlap is unavailable.
- Zero and multiple projects with global rows, no ambiguous selection, and zero project-scoped requests.
- `/api/projects` returning `503` or dropping the connection while global endpoints are called, useful rows plus an explicit partial-project row are printed, scoped endpoints are untouched, and the command returns nonzero.
- Count transport/auth/decode failures with the other count retained, preserving global authentication and transport error precedence and byte-for-byte output for matched fixtures.
- Cancellation and deadline delivery after project selection and count launch. Assert prompt return, cancellation observed by every started request, no extra requests, and no goroutine/test deadlock.
- Interactive status and static/offline help behavior remaining unchanged.

Use deterministic `httptest` barriers and request counters for ordering and call-count assertions. Avoid proving concurrency from one elapsed-time sample alone. Keep the exact five-request shape and query propagation assertions separate from latency assertions so a faster test cannot hide a missing or duplicated request. Include a generation/auth race fixture in which a global `401` overlaps a successful count response; verify sign-in/auth precedence and retained successful count data match the established one-shot output contract.

## Validation

Run focused CLI regressions first, then:

```sh
go build ./...
go vet ./...
go test ./... -count=1
gofmt -l .
git diff --check
```

Because this orchestration intentionally overlaps requests, run the focused status/CLI package or synchronization-barrier tests under `-race`. Expand to `go test -race ./...` only if the concurrent behavior crosses package boundaries or the task explicitly requires repository-wide evidence.

Treat any unexpected `gofmt -l` output as a formatting failure.
