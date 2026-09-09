# Channel Management Safety

Use this reference when implementing or auditing `/channels` add, edit, disconnect, or remove behavior in `internal/tui/registry.go` and its interactive/headless regressions.

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

## Multiline PEM Input

- Bubble Tea `textinput` can flatten a pasted multiline PEM into one whitespace-separated line. Normalize the secret before submission rather than assuming literal newlines survive.
- Accept literal newlines and escaped `\n` forms. For a flattened PEM, identify only supported private-key boundaries, remove whitespace from the encoded body, reconstruct canonical PEM line breaks, decode it with `encoding/pem`, reject trailing non-whitespace data, and re-encode canonically.
- Fail locally with a generic validation error. Never include the key or backend response body in transcript, view, history, diagnostics, or errors.
- Test with a real generated RSA private key, not arbitrary bytes alone. Pass it through the same flattening behavior as terminal input, verify the submitted form decodes as a valid RSA key, and assert the secret is absent from rendered/transcript output.

## Browser-Parity Validation

- Terminal commands must locally enforce meaningful browser constraints before HTTP mutation. For GitHub API endpoints, require an absolute `http` or `https` URL with a non-empty host. For Email addresses, parse a mailbox and reject display-name forms or values whose parsed address differs from the supplied value.
- Run the same validators from headless mutation parsing and interactive wizard field handling. Empty values may remain valid when omission means preserve/default, but explicitly malformed non-empty values must fail.
- Cover representative valid and invalid values and assert invalid inputs make zero backend mutation requests.

## Validation

Run focused channel tests first, then the exact repository gate from the active task worktree:

```bash
gofmt -w internal/tui/registry.go internal/tui/cli_test.go internal/tui/dispatch_test.go internal/tui/command_test.go
go build ./...
go vet ./...
go test ./... -count=1
git diff --check
git status --short --branch
```

Channel parsing, validation, and synchronous request changes do not require the race detector by default. Add the narrowest relevant `go test -race` invocation only when asynchronous model state, goroutine ownership, cancellation, or another concurrent path changes, or when the task explicitly requires it.

If the lifecycle requires a separate audit-only turn, stop after implementation reporting and validation. Do not perform that audit in the fix turn.
