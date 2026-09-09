# One-Shot Global Status With Partial Project Discovery

Use this guidance when changing the OpenVibely terminal client's one-shot `status` command or startup preflight.

## Execution Contract

- Do not make `/api/projects` a mandatory preflight for one-shot global status. Start project discovery in the same wave as independent global health, authentication, and capacity checks.
- Construct commands before launching concurrent work, keep mutable model state out of goroutines, store each result independently, then apply messages to the model in the established deterministic order, currently projects first and global connection/capacity second. Concurrency must not change output or error precedence.
- Propagate the CLI context into every first-wave request so cancellation promptly stops the command and cannot leave overlapped global checks running.
- Retain a project-list failure for the command's final nonzero result, but continue independent global checks. Render every trustworthy global status row and an explicit projects-unavailable or partial-status row so stdout cannot be mistaken for complete success.
- Keep failure classes distinct: authentication-required is reachable and nonzero, transport failure is offline, and a project-list HTTP/decode failure is partial rather than fully offline when global checks still succeed.
- Select project scope only when discovery yields exactly one unambiguous project. Start alert/task counts only after that selection; with zero, multiple, or failed project discovery, issue no scoped requests. Run independent scoped counts concurrently as a later wave.
- Call each endpoint at most once. Preserve the healthy single-project path and avoid changing interactive `/status` dispatch when the requested contract is specific to one-shot CLI orchestration.
- Keep static help and command-discovery paths backend-independent.

## Regression Matrix

Add CLI-level tests, not only renderer or client tests, for:

- Healthy single-project status with expected global rows and project-scoped counts.
- A synchronization-barrier test proving projects, capacity, and authentication all enter the first request wave before any is released; separately prove alerts/tasks do not start until project selection completes and then enter a concurrent second wave.
- Per-endpoint counters proving every endpoint is called at most once.
- Zero and multiple projects with global rows, no ambiguous selection, and zero project-scoped requests.
- `/api/projects` returning `503` or dropping the connection while global endpoints are called, useful rows plus an explicit partial-project row are printed, scoped endpoints are untouched, and the command returns nonzero.
- Cancellation during the first wave returning promptly, canceling in-flight requests, and launching no scoped requests.
- Output, project scope, authentication precedence, and established error precedence remaining unchanged.
- Interactive status and static/offline help behavior remaining unchanged.

Use deterministic `httptest` barriers and request counters for ordering and call-count assertions. Avoid proving concurrency from one elapsed-time sample alone. For latency acceptance, run repeated delayed-server trials and compare the median against a bound that clearly separates two waves from three; with 100 ms endpoints, a bound around 225 ms demonstrates at least a 25% improvement below the old roughly 300 ms path while allowing modest scheduler noise. Keep a separate immediate-backend test or benchmark to catch material orchestration overhead. If the visible stdout/exit contract changes, update the README concisely.

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
