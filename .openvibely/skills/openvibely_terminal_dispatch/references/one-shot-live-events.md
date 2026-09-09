# One-Shot Live Events

Use this reference when auditing or changing the `/events` command, `RunCLI`, or SSE behavior in interactive versus one-shot execution.

## Contract Check

Interactive startup may open the project-scoped SSE stream, while one-shot `RunCLI` can load projects without starting a long-lived stream. Do not assume that an in-memory display flag activates the stream. If the CLI `events on` handler only flips a flag and returns success, it is a false-success path: no `/events/live` request is made and the short-lived process cannot provide live monitoring.

Choose and test an explicit product contract when this mismatch is found:

- Reject an unsupported one-shot live-events operation with a clear nonzero, actionable error; or
- Implement a deliberate one-shot lifecycle that opens the selected project's stream and keeps the process alive long enough to consume events.

Never report success solely because `showEvents` changed in memory. Verify the behavior through the actual CLI dispatch path and distinguish it from server-side project filtering or stale-event handling during project switches.

## Project Selection Safety

In headless `RunCLI` mode, never let a project-scoped command silently target the server's first project when multiple projects are available and the user omitted `-project`. Keeping the current convenience for exactly one project is safe; with two or more projects, require an explicit `-project <name|id>` before task/list, mutation, destructive, or JSON operations that depend on project data. Return a clear, actionable ambiguity error and preserve interactive selection behavior rather than silently changing the TUI flow.

Regression coverage should exercise at least one read, one mutation, one destructive command, and JSON output with multiple projects and no `-project`, asserting that no project-scoped operation is sent and that the error identifies the required option. Also cover the single-project default and explicit name/ID selection so the safety guard does not remove intended shell convenience or valid targeting.

## Event Ownership Parity

Keep headless event rendering as strict as interactive SSE handling for project-owned task events. For a selected project, accept a task/execution event only when its `project_id` is non-empty and exactly matches the selected project; filter both missing and foreign IDs before plain-text or JSON output. Never normalize a missing task-event `project_id` by filling it with the selected project, because that can make an unowned task update appear to belong to the active project.

Keep this validation event-type aware. If the backend contract intentionally permits an empty `project_id` on a project-level chat event, preserve that compatibility without extending it to task events. Regression coverage should include matched, missing, and foreign task-event IDs plus an unscoped project-chat event, asserting that only the contractually valid events render in both interactive and headless paths and that JSON output does not rewrite ownership fields.

## Single-Owner Argument Validation

When the one-shot entrypoint and the events command handler both inspect the same arguments, do not duplicate parsing or rejection logic. In particular, compare `RunCLIContext` preflight checks with `runCLIEvents` before adding or changing validation for `events` options such as `off`; keep one canonical parser/validator or establish a clear boundary where one layer passes normalized arguments to the other. Preserve the local, nonzero, no-network behavior for unsupported `events off`, and add coverage through the real CLI path so validation remains consistent without two implementations drifting.

## Foreground Implementation Pattern

For a supported foreground monitor, keep the headless path separate from interactive model-owned stream state:

- Resolve the selected project through the normal preload and project-name/ID selection rules, but pass the caller's context through preload as well as stream consumption. An empty project list must fail locally with an actionable error and must not issue an unscoped `/events/live` request.
- After resolution, create exactly one `StreamEvents` consumer for the selected project and do not call interactive `connectSSE`, reconnect machinery, or a second stream. Assert the exact `GET /events/live?project_id=<selected-id>` request count in tests.
- Keep the process alive until caller/context cancellation, `Ctrl-C`, or clean stream EOF. Treat clean EOF and cancellation as normal termination, close the response body, and ensure the producer's channel sends select on `ctx.Done()` so an exited output consumer cannot strand the SSE reader goroutine.
- Render each task/chat event immediately as stable line-oriented plain text. If JSON mode exists, use explicit stable `json` tags and emit one valid, unstyled JSON object per line; defensively ignore non-empty event project IDs that do not match the selected project.
- Treat `events off` as a local limitation: another process's foreground stream cannot be disabled by changing this process's model flag. Reject it before project loading or network access with clear guidance to stop the monitoring process, and return a nonzero result rather than a success transcript.
- Wire the executable's `Ctrl-C` to the same context that owns project preload and stream consumption. Keep interactive `/events` and `/events off` as display toggles for the existing model-owned stream, with their project scoping and stale-stream protections unchanged.

## Audit And Regression Checks

Inspect `internal/terminal/cli.go` (`RunCLI`), the registry's events handler, the SSE connection/`StreamEvents` path, and CLI/dispatch tests. Compare the behavior with the repository vision and existing automation findings before filing a new product gap. For a supported stream, assert the request is project-scoped as `/events/live?project_id=<selected>`; for an intentionally unsupported one-shot command, assert no misleading success output and a nonzero actionable result. Keep interactive startup coverage separate from one-shot coverage.

The focused regression matrix should cover explicit IDs and project-name resolution, bare/default `events`, empty projects, exact request count and query scope, plain and JSON event output, task-event ownership with matched/missing/foreign `project_id`, intentionally unscoped project-chat events, clean EOF, cancellation-driven connection closure, authenticated cookie-session reuse, local `events off` with zero project/stream requests, and interactive toggle compatibility. Finish with `go build ./...`, `go vet ./...`, `go test ./... -count=1`, `gofmt -l`, and `git diff --check`. For concurrency changes, run the narrowest relevant event/stream tests under `-race`; expand to `./...` only when behavior crosses package boundaries or the task explicitly requires it.
