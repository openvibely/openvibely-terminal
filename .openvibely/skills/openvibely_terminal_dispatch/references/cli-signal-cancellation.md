# CLI Signal Cancellation

Use this reference when adding a caller-owned context, signal handler, or foreground/long-running command to `RunCLI`.

## Cancellation Contract

- A process-level `signal.NotifyContext` changes the default `Ctrl-C` behavior for the entire CLI invocation. Do not install one for only one command family unless every command that can outlive the dispatch call observes the resulting context.
- If `RunCLIContext` accepts a caller context, propagate it through project preload, model command closures, chat polling/status fetches, thread and selector requests, and other backend operations that can block. A context used only by preload and one special streaming loop leaves ordinary long-running commands uncancellable.
- The caller-owned context must remain the source for ordinary dispatched command work after project loading. Audit shared run helpers for a fresh `context.Background()` or independent context that silently severs cancellation; the preload context alone is insufficient.
- Preserve interactive per-operation timeout behavior while adding headless propagation. Prefer a context-aware helper for CLI dispatch and leave the existing background/timeout helper for interactive operations unless the interactive contract is intentionally changing.
- Thread the caller context through every operation in a long-running command family, including list, reference resolution, detail/open, lifecycle, attachment, thread, and mutation paths. Do not stop at the first request if later requests can block.
- Cancellation must be checked again after a blocking reference-resolution step and immediately before a state-changing request. If cancellation occurs while lookup is blocked, releasing lookup must not allow a subsequent `run`, `stop`, or `delete` request to be sent.
- On cancellation, cancel the HTTP request and return promptly without reporting a false command failure. Ensure all stream/event channels and response bodies can shut down without goroutine or connection leaks. Preserve existing `cliContextResult` cancellation semantics and suppress canceled dispatch output rather than turning it into a command error.

## Regression Checks

- Compare the signal handler's scope with every command path in `RunCLI`, not only the newly added foreground command.
- Trace the context used by `run`, `sendChat`, polling/status, thread, selector, and project-loading helpers; flag any `context.Background()` or independent timeout that bypasses the caller-owned cancellation when the CLI promises `Ctrl-C` cancellation.
- Add deterministic `httptest` regressions for both a non-mutating and a mutation-after-resolution path. Block a task-list request, cancel the caller, assert prompt return and that the handler observes `r.Context().Done()`. Separately block task reference lookup, cancel, release lookup, and assert that no later mutation request arrives.
- Add a deterministic regression for `Ctrl-C` during the new foreground command and for at least one pre-existing long-running CLI command, asserting prompt return, request cancellation, and no spurious success/error output.
- Add wrapper-level routing coverage for supported foreground aliases and ordinary commands such as tasks, chat, and help. A helper-classification test complements, but does not replace, coverage that the executable entry point selects the signal-backed path only for foreground streaming.

## Validation

- For cancellation or goroutine-ownership changes, run the focused regression with `go test -race` in addition to the normal package test.
- Finish with `go test ./... -count=1`, `go build ./...`, `go vet ./...`, `gofmt -l` (which must print nothing), and `git diff --check`.
