# Active Task-Response Steering Command

Use this reference when extending `tasks` with an active-response control that must work in both slash-command TUI and one-shot CLI modes.

- Define the action once in shared command metadata so registry dispatch, usage/help, completion, and documentation stay aligned. Keep the interactive missing-ref path selector-driven with the existing pipe prefill (`<task> | <message>`); keep headless missing-ref behavior as usage/nonzero error without selector fetches.
- Reuse project-scoped task reference resolution and require a selected project before the thread read or mutation. Both TUI and CLI must execute the same guarded GET-active-turn then POST-steer flow, not separate implementations.
- Make JSON acknowledgement fields stable and explicit. Include status, canonical task ID, exact expected turn ID, and the optional backend pending-input ID when present. Successful steering must refresh or otherwise appear in the open task thread.
- Regression coverage should assert TUI/CLI parity, selected-project query propagation, escaped task paths, exact active-turn forwarding, no-active local rejection, stale `409` handling, and zero ordinary-reply fallback mutations. Keep ordinary `tasks reply` queue/execution-promotion and JSON acknowledgement tests unchanged.
- When pending-input controls are not shipped, state the active-only scope in generated help and user documentation; do not claim listing, cancellation, or queued-input steering support.
- For live SSE refreshes, validate task/project ownership and treat the steering event as a control-plane thread refresh even if an ordinary reply is concurrently pending.

## Delayed Result Correlation

A successful steering acknowledgement is an asynchronous mutation result, not merely generic command output. Capture its origin before starting network work and reject it before any generic result-handler side effect when the view has changed.

- Mark the result as steering-specific and carry the originating selected project, active thread ID, and monotonic thread-open/navigation request token. Project and session generations remain necessary, but they do not prove that the same task thread is still visible.
- In `Model.Update`, validate the steering target immediately after session/project generation checks and before clearing `busy`, handling errors, appending acknowledgement text, or scheduling a thread refresh. Reject stale errors as well as stale successes; otherwise a late error can still mutate the wrong view.
- Treat `/chat` and opening another task as invalidating the origin token. A result from task A must not append to project chat or task B, even when the backend operation succeeded.
- Add delayed-command regressions through the real dispatch/update path for both leaving to project chat and opening another task in the same project. Assert the stale acknowledgement is absent, the current transcript/thread is preserved, and stale handling does not change the current view's `busy` state. Keep the normal same-thread success test asserting the acknowledgement and post-mutation refresh.

## Acknowledgement Output Safety

- Treat the plain-text success acknowledgement as a terminal-safety boundary too, not only errors, confirmations, or detail renderers. Task titles and other resolved resource metadata are backend/user-controlled; sanitize them before interpolation into TUI transcript entries or raw CLI output. Do not assume a later transcript renderer, table, or detail sanitizer will clean an earlier acknowledgement.
- Preserve raw task identity for resolution and JSON fields, but use the package's single-line terminal sanitizer for the displayed title. Replace or remove ANSI/OSC escapes, newlines, carriage returns, tabs, bells, and other non-printing controls while retaining readable title text.
- Add both TUI and CLI regressions with hostile task titles containing ANSI and line/control injection. Assert against the raw transcript and raw CLI output before `stripANSI`, verify no injected bytes or extra lines are emitted, and verify the readable title and acknowledgement remain present. Keep JSON acknowledgement semantics unchanged because JSON escaping protects its structured string fields.

## Backend Error Output Safety

- Treat every backend error that can reach terminal-facing steering output as untrusted, not only stale-turn `409` conflicts. `HTTPStatusError` preserves arbitrary response `error` or `message` text, so raw details from `400`, `404`, `409`, `422`, `500`, or other non-success responses must never be interpolated into TUI transcript errors or CLI/stderr output.
- Sanitize diagnostics with the connection-diagnostic redaction pipeline rather than only the control-character sanitizer. That pipeline must remove terminal controls, strip URL userinfo, query, and fragment components while retaining a safe scheme/host/path when possible, redact credential-bearing assignments such as `token=`, `api_key:`, `password=`, and `Authorization: Bearer ...`, then apply the single-line length bound.
- Route both the conflict-specific message and generic non-conflict steering errors through the same diagnostic helper. Keep conflict classification and no-fallback behavior unchanged; do not replace a typed status error with a plain display error.
- Preserve machine classification while changing display text: wrap the original error in the existing task-steer error type with `Unwrap`, and add a direct `errors.As` regression proving the original `HTTPStatusError` remains discoverable after redaction.
- Audit both TUI transcript rendering and CLI/stderr propagation because the same returned error can reach both paths. Add hostile-output regressions for conflict and non-conflict statuses in both modes, asserting raw output contains no ANSI, control, carriage-return, tab, injected line-break, URL-userinfo, query, fragment, or credential values while retaining safe URL host/path, actionable status guidance, and zero ordinary-reply fallback mutations.
