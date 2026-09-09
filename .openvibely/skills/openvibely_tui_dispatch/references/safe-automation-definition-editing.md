# Safe Automation Definition Editing

Use this reference when adding, auditing, or repairing `/automations edit` or an equivalent terminal workflow. Pair it with `automation-detail-parity.md` for graph parsing and rendering.

## Source Of Truth

- Inspect current backend route registration, handler code, generated Swagger, and the web builder before choosing an edit payload. Do not invent a patch contract from the terminal command shape.
- The established builder contract is complete YAML: fetch the normalized definition from `GET /automations/:id/builder`, preview the edited definition through the builder route without `save_changes`, and persist only with `save_changes=true` after preview succeeds.
- Preserve the fetched definition wholesale. A targeted deterministic replacement may alter an allowlisted field, but it must retain every unchanged field and submit the complete authoritative definition.

## Safe Workflow

- Resolve the automation against the selected project with the normal exact, unique-prefix, unique-substring matcher. Validate response automation identity and project scope before preview or save; stale or cross-project data must never mutate.
- Fail closed when validating builder identity. Require an authoritative form action containing both the expected automation path and selected `project_id`, or require both explicit automation and project identity markers. Do not accept missing markers and then populate the requested IDs locally, because that turns an unverified fragment into authorization for export or save.
- Match the requested interaction requirement exactly. A two-command `--export`/external-edit/`--file` workflow is deterministic and useful for headless use, but it is not an interactive editor. If the requirement calls for intuitive interactive editing, provide an in-TUI field flow or a safe `$EDITOR` temporary-file flow that returns to preview/save; do not claim export/import alone satisfies it.
- When export/import is part of the contract, `--export <yaml>` writes the current complete YAML to a new owner-only file, while `--file <yaml>` applies a deterministic edited definition. Never overwrite an existing export implicitly.
- With a missing ref, interactive mode opens the shared searchable selector and prefills or launches the safe edit path required by the product contract. Headless mode returns deterministic usage and does not open or fetch for a selector.
- Bound local file size before backend discovery or mutation. Reject empty, oversized, unreadable, or invalid YAML locally when possible.
- Compare edited and current definitions before preview. Cancellation and unchanged content issue no preview or save request.
- Send preview first. Treat validation lists, malformed YAML responses, builder alerts, non-success status, parse failures, missing identity, and identity/scope mismatches as hard failures. A preview failure must issue no save.
- Send the save request only after a successful preview, and parse the save response for rendered builder errors before reporting success.

## In-Flight Cancellation

- Stale-message rejection protects UI state only; it does not stop an HTTP mutation already executing. Every interactive preview/save sequence needs an operation-owned cancel function retained in the model until the request finishes.
- Cancel that context before clearing editor state on `Esc`, quit, project transition, authentication/session invalidation, editor replacement, and cleanup. Clear the stored cancel function on terminal success or failure, and use an operation token so a late completion cannot clear or refocus a newer editor.
- Audit the application-level shutdown hook itself, not only key and command handlers that happen to call it. `Model.Cleanup()` or its equivalent must directly cancel editor-owned preview/save work so external Bubble Tea termination, program errors, and final-model cleanup cannot leave a background mutation running. Add a focused regression that starts a blocked POST, calls cleanup directly, and proves the command returns promptly without a subsequent save.
- Keep preview and save under the same cancelable context. Cancellation between the successful preview and save must prevent the save request, and cancellation during save must propagate to the transport rather than merely suppressing the eventual message.
- Regressions must start a blocked preview after `Ctrl+S`, then trigger each ownership transition and prove prompt client-command return plus zero save requests. A server handler observing `r.Context().Done()` is useful supporting evidence but not a reliable sole oracle because Go's HTTP transport does not guarantee when the server notices a disconnected client; release blocked fixtures during cleanup so failed assertions cannot hang `httptest.Server.Close`.
- Also cover cancellation after preview but before save, cancellation while the persistence POST is blocked, stale completion delivery, and a newer edit session remaining intact.

## Secret And Output Safety

- Never echo complete YAML, editor contents, credentials, prompts, instructions, tokens, or secret-like configuration in normal, JSON, diagnostic, or error output.
- Render graph configuration through an allowlist of operational fields. Represent sensitive or unknown secret-like values only as `configured` or omit them.
- Bound graph rows, configuration summaries, warnings, and diagnostics. Report explicit omitted counts instead of silently truncating topology.
- Treat row-count caps as only one layer of bounding. Apply display-width or byte limits to every backend-controlled field used in headings and tables, including automation metadata, node names/types/roles, edge endpoints/labels, resource fields, and warnings. Verify a total rendered-output bound or derive one from bounded row counts and bounded cells so one multi-megabyte HTML value cannot flood the transcript or force huge table-width calculations.
- Add hostile large-field regressions in addition to large-cardinality regressions. Assert maximum cell/output size, terminal-safe truncation, valid UTF-8/display width, and omission metadata where applicable.
- Keep exported files owner-only and avoid exposing the definition through process arguments. Prefer file input and `$EDITOR`/temporary-file interaction over inline YAML.

## Alias And Command Consistency

- Keep `show` as the canonical automation detail action. If `open` remains for compatibility, normalize it to the same path and ensure identical selector, completion, resolution, dispatch, and output behavior.
- Keep help, completion, registry metadata, README examples, runtime usage, and ordinary resource-page next-step hints synchronized. The normal automation list should advertise canonical inspection and editing actions, not only lifecycle mutations; test the rendered list guidance so a feature documented only under `/help` is not treated as discoverable.
- Test registry/documentation parity when introducing `edit` or changing canonical aliases.

## Regression Contract

Cover exact ordered requests for fetch, preview, and save; successful full-definition round-tripping; unchanged-field preservation; cancellation and unchanged input; invalid/oversized files; preview validation errors; malformed builder alerts; save-rendered failures; stale project scope; matching identity; missing form plus missing identity markers; partial identity markers; foreign identity; secure non-overwriting export; missing-ref selector behavior; CLI usage; alias parity; completion/help/list-hint parity; and secret absence. Every cancellation or failure before validated preview must assert zero save requests, and local validation failures should assert zero HTTP requests where applicable.

If interactive editing is required, cover editor launch, temporary-file permissions and cleanup, unchanged content, editor cancellation/failure, project/session changes while editing, preview failure, save failure, and secret-free transcript/history. Include in-flight cancellation after save initiation and direct application cleanup; tests that cancel only before `Ctrl+S` or only through editor key handling do not prove mutation cancellation.

For graph output, cover node/edge topology, trigger/action/config summaries, runtime/status metadata, explicit empty and partial states, hostile values, bounded omission counts, oversized individual fields, total output bounds, and JSON/plain-output contracts.

Finish Go changes with focused client/dispatch/render tests, `gofmt`, `go build ./...`, `go vet ./...`, `go test ./... -count=1`, and `git diff --check`. Add a focused `-race` invocation only if the change affects asynchronous model or request behavior.
