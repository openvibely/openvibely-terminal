# CLI Signal Cancellation

Use this reference when adding a caller-owned context, signal handler, or foreground/long-running command to `RunCLI`.

## Cancellation Contract

- A process-level `signal.NotifyContext` changes the default `Ctrl-C` behavior for the entire CLI invocation. Do not install one for only one command family unless every command that can outlive the dispatch call observes the resulting context.
- If `RunCLIContext` accepts a caller context, propagate it through project preload, model command closures, chat polling/status fetches, thread and selector requests, and other backend operations that can block. A context used only by preload and one special streaming loop leaves ordinary long-running commands uncancellable.
- Alternatively, scope signal handling to the foreground stream entry point and leave ordinary CLI invocations on their existing termination behavior. Keep the ownership and cleanup contract explicit.
- In the executable wrapper, route only the foreground live-events command and its supported aliases (currently `events`, `stream`, and `log`, with the same slash/case normalization as command parsing) through `signal.NotifyContext` plus `RunCLIContext`; route ordinary commands through the existing `RunCLI` path. This prevents a process-level signal handler from consuming `SIGINT` for commands that still use independent background contexts.
- On cancellation, cancel the HTTP request and return promptly without reporting a false command failure. Ensure all stream/event channels and response bodies can shut down without goroutine or connection leaks.

## Audit Checks

- Compare the signal handler's scope with every command path in `RunCLI`, not only the newly added foreground command.
- Trace the context used by `run`, `sendChat`, polling/status, thread, selector, and project-loading helpers; flag any `context.Background()` or independent timeout that bypasses the caller-owned cancellation when the CLI promises `Ctrl-C` cancellation.
- Add a deterministic regression for `Ctrl-C` during the new foreground command and for at least one pre-existing long-running CLI command, asserting prompt return, request cancellation, and no spurious success/error output.
- Add wrapper-level routing coverage for supported foreground aliases and ordinary commands such as tasks, chat, and help. A helper-classification test complements, but does not replace, coverage that the executable entry point selects the signal-backed path only for foreground streaming.
