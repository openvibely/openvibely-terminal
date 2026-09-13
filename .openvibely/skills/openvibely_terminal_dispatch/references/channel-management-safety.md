# Channel Management Safety

Use this reference when implementing or auditing `/channels` add, edit, test, disconnect, or remove behavior in `internal/terminal/registry.go` and its interactive/headless regressions.

## Destructive Disconnects

- Treat credential-clearing disconnects as destructive even when non-credential configuration is retained.
- Resolve the channel to its canonical identity before confirmation, then route both interactive and headless paths through `confirmOr`.
- Interactive mode must require the standard typed `yes`; one-shot CLI mode must require `--force`/`-f` and make no mutation without it.
- Preserve transport semantics: GitHub and Slack disconnect use their disconnect endpoints, and Slack remove may intentionally map to the safe disconnect endpoint. Confirmation must not accidentally change route selection.
- Cover GitHub and Slack in both modes. Assert canonical prompt/force guidance, zero POST before approval, the exact POST after approval, and absence of remove-route calls for explicit disconnect.

## Conditional Edit Transitions

- Partial edits may preserve omitted settings only when those settings remain compatible with the current mode/provider.
- Capture an immutable copy of authoritative original editable settings separately from the mutable edit form. Also track which fields the user explicitly supplied.
- When an edit changes GitHub PAT to App, GitHub App to PAT, Slack OAuth to manual, or an Email preset to custom, require the target mode's credentials or hosts to be explicitly supplied. Existing stale fields from the old mode must not satisfy the transition.
- Re-entering the unchanged mode/provider is not a transition and must retain normal partial-edit preservation semantics.
- For GitHub App require app ID, app slug, and private key; for PAT require PAT; for Slack manual require bot token; for custom Email require IMAP and SMTP hosts.
- Separate stateless argument validation from state-dependent transition validation. Parsing may validate option shape, enum values, URLs, email addresses, and explicitly supplied credential formats without a request. It must not classify an explicit mode/provider as a transition when the authoritative current value is unknown.
- Headless edits that include a mode/provider require an authoritative read before state-dependent validation. Compare the submitted target mode/provider with the fetched current value, require fresh target fields only for a real transition, then merge and submit through the same read-modify-write path. Do not reject an unchanged explicit `--auth-mode app`, `--auth-mode pat`, `--bot-token-mode manual`, or `--provider custom` merely because preserved credentials or hosts were omitted.
- Ensure invalid real transitions fail before the mutation request. If zero-request validation is required, the allowed preflight request is the authoritative read; assert zero POST rather than zero HTTP requests.
- Test both sides of the distinction in interactive and headless modes: incompatible real transitions with omitted target fields must fail without POST, while unchanged explicit modes/providers must successfully preserve authoritative secrets/hosts. Also cover successful transitions with all required fields and verify the intended canonical configuration is submitted.

## Shared Channel Mutation Completion

- Keep guided wizard and headless option parsing, authoritative edit snapshot loading, transition validation, route selection, masking, credential clearing, and confirmation in their respective upstream paths. Share only the identical post-success tail after a successful channel mutation.
- Route successful `ConfigureChannel`, `UpdateChannel`, and `ChannelAction` calls through one narrow completion helper such as `completeChannelMutation`, while callers supply the status wording already required by their operation. Preserve `added <name>`, `edited <name>`, and action-specific `test: <name>`, `remove: <name>`, or `disconnect: <name>` output exactly.
- The helper must emit the supplied status, attempt `ListChannels`, and append `renderChannels` only when the refresh succeeds. A refresh error is intentionally non-fatal: return the success-only status rather than a false failure.
- Never call this helper after a mutation error. Return the original mutation error directly so no completion refresh request is issued and no success output is reported.
- Keep channel-access and webhook mutation workflows outside this channel completion helper unless their contracts independently require the same policy.
- Exercise guided and headless add/edit plus test/remove/disconnect in a focused matrix. Cover successful canonical display-name output and rendered refresh, mutation failure with no refresh, refresh failure with success-only output, exact action routes, confirmation gating, and output parity.
- When testing channel actions, model the client response contract rather than treating every successful POST as equivalent. For example, test actions may require a success marker in the returned HTML before the completion callback runs, while form actions may not. Confirmation prompts are rendered in the model `View()`; do not assert them only through transcript output.

## Multiline PEM Input

- Bubble Tea `textinput` can flatten a pasted multiline PEM into one whitespace-separated line. Normalize the secret before submission rather than assuming literal newlines survive.
- Accept literal newlines and escaped `\\n` forms. For a flattened PEM, identify only supported private-key boundaries, remove whitespace from the encoded body, reconstruct canonical PEM line breaks, decode it with `encoding/pem`, reject trailing non-whitespace data, and re-encode canonically.
- Fail locally with a generic validation error. Never include the key or backend response body in transcript, view, history, diagnostics, or errors.
- Test with a real generated RSA private key, not arbitrary bytes alone. Pass it through the same flattening behavior as terminal input, verify the submitted form decodes as a valid RSA key, and assert the secret is absent from rendered/transcript output.

## X Authorized Mention Access

- The delivered X settings contract is a structured project-scoped representation inside the channels/settings page, not a JSON list and not a guaranteed per-row `data-project-id` contract. The page must contain exactly one X authorization form with a hidden `project_id` equal to the selected project.
- For each row, take the canonical authorization record ID only from its matching `hx-delete` control. Require that delete control to use the X authorization route and carry the selected project's `project_id`; the control query is the row's delivered scope-bearing structure, while the page form proves the selected settings page scope. Do not treat a route query alone as generic ownership proof outside this backend contract.
- Do not reject a real backend row merely because it lacks an invented row-level project marker. If optional row markers such as `data-project-id`, `data-x-authorized-project-id`, or explicit record IDs are emitted, validate them against the canonical delete control and selected project, and fail closed on any conflict.
- Preserve structured identity validation: prefer complete `data-x-user-id`/`data-x-username` metadata when available, otherwise parse the dedicated username and numeric-ID spans. Normalize positive numeric X IDs, normalize optional usernames, reject malformed or ambiguous identity metadata, and never use display-name prose as identity.
- Keep removal fail-closed: re-list the ownership-validated rows immediately before DELETE, match the captured canonical row ID first, and only then issue the project-scoped delete request. Numeric identity aliases may normalize leading zeros consistently, but must not replace canonical row-ID validation.
- Regression fixtures should include the actual backend row shape without a row marker, a foreign/conflicting explicit row marker, a mismatched delete-control scope, malformed identity data, secret-free output, and the list/add/remove request sequence for a non-default project.

## GitHub Authorized Actor Access

- GitHub access management is a separate nested command family, `channels access github list|add|remove`, and must be registered consistently in help, completion, README/user-guide examples, interactive dispatch, and one-shot CLI validation. Do not infer support from ordinary GitHub channel configuration or route it through X's authorization parser.
- Require a selected project before parsing or issuing any GitHub access request. Carry that selected `project_id` on the runtime-settings list, actor add, and actor delete requests exactly as the backend contract requires; system-level actor-list semantics do not permit an unscoped or fallback-project request.
- Keep GitHub parsing and transport in the client package. List through `GET /channels/github/runtime-settings` and parse the stable `github-runtime-settings` container, one structured login/display-name pair per actor, and the canonical scoped `hx-delete` control. Treat display names as optional metadata: an omitted or empty semantic display-name element is valid and should render as no display name when exactly one valid canonical login is present. Reject malformed or unavailable fragments and malformed controls fail-closed; never derive actors from flattened page prose or silently ignore invalid structured rows.
- Normalize login operands once at the client boundary, including a leading `@` such as `@Alice`, and use the same canonical login for duplicate detection, rendering, lookup, and remove resolution. Accept login-only and login-plus-display-name add forms. Reject missing, malformed, duplicate, or surplus operands before POST.
- Resolve a remove target from the selected scoped list and capture exactly one canonical actor/delete ID before confirmation. Re-list immediately before DELETE and require the captured actor and scope to remain valid; reject unknown, ambiguous, foreign-context, or stale targets. Do not re-match raw user input after confirmation.
- Destructive removal uses the normal shared safety gate: interactive mode requires typed `yes`, headless mode requires `--force`/`-f`, and cancellation or missing force must issue no DELETE. A successful add/remove may render a fresh safe list, but refresh failure is non-fatal and must not be described as a rollback; mutation failure must not emit success or trigger refresh.
- Render only safe actor identity fields in plain and `--json` output, with explicit stable JSON tags and non-nil empty slices. Exclude credentials, OAuth state, tokens, hidden form values, delete-control details, raw HTML, and backend response bodies. Preserve typed authentication/transport errors without leaking response content.

## Browser-Parity Validation

- Terminal commands must locally enforce meaningful browser constraints before HTTP mutation. For GitHub API endpoints, require an absolute `http` or `https` URL with a non-empty host. For Email addresses, parse a mailbox and reject display-name forms or values whose parsed address differs from the supplied value.
- A digits-only lexical match is not sufficient for provider identifiers stored as a bounded numeric type. Validate the backend's actual domain locally before mutation, including parseability, range, and sentinel exclusions. In particular, Telegram access numeric IDs must be positive, parseable `int64` values: reject `0` (the backend's username-only sentinel) and decimal overflow rather than letting the handler reinterpret them as a username row and report a non-authorizing success.
- Run the same validators from headless mutation parsing and interactive wizard field handling. Empty values may remain valid when omission means preserve/default, but explicitly malformed non-empty values must fail.
- Cover representative valid and invalid values and assert invalid inputs make zero backend mutation requests. For bounded numeric identities, include zero and a value above the storage type's maximum alongside malformed input.

## Validation

Run focused channel tests first, then the exact repository gate from the active task worktree:

```bash
gofmt -w internal/terminal/registry.go internal/terminal/cli_test.go internal/terminal/dispatch_test.go internal/terminal/command_test.go
go build ./...
go vet ./...
go test ./... -count=1
git diff --check
git status --short --branch
```

Channel parsing, validation, and synchronous request changes do not require the race detector by default. Add the narrowest relevant `go test -race` invocation only when asynchronous model state, goroutine ownership, cancellation, or another concurrent path changes, or when the task explicitly requires it.

If the lifecycle requires a separate audit-only turn, stop after implementation reporting and validation. Do not perform that audit in the fix turn.
