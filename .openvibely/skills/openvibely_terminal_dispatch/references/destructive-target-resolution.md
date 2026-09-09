# Resolve Destructive Targets Before Confirmation

Use this pattern for destructive TUI commands that accept a typed resource reference and may require asynchronous listing or matching before confirmation.

- Resolve and validate a non-empty typed reference before calling `confirmOr`. Unknown and ambiguous references must fail without opening confirmation and without issuing the mutation request.
- Build confirmation and headless `--force` guidance from the resolved canonical resource identity, not the raw shorthand supplied by the user. In headless mode, matching errors take precedence over the force-required error.
- Canonical display labels are still backend-controlled terminal data. Preserve raw values for matching and capture the resolved stable ID for mutation, but render a separately sanitized single-line label in confirmations, force guidance, success status, and ambiguity diagnostics. Strip ANSI/control sequences and collapse CR/LF/tab so a title cannot alter prompt layout or disguise the target.
- Capture the resolved stable resource ID in the confirmation closure. Never list or match again after the user confirms, because catalog changes could rebind the same name or partial reference to another record.
- Keep selector behavior separate when the selector already captures a canonical record. Do not add a duplicate list request merely to force typed and selector paths through identical code.
- Treat pre-confirmation resolution as project- and auth/session-scoped asynchronous work. Include the originating project and session epoch or equivalent freshness tokens in the result message, reject stale results before changing `busy`, transcript, or confirmation state, and never install an old confirmation after a project or session transition.
- Test interactive unknown and ambiguous refs, canonical prompt text for a unique partial ref, cancellation, no mutation before `yes`, stable-ID deletion after catalog replacement, and stale project/session result delivery. For raw ANSI/control/newline titles, assert the confirmation and result bytes contain no injected controls or layout-breaking lines before any display-normalization helper is used. Test headless no-force unknown/ambiguous precedence, canonical force guidance for valid shorthand, forced stable-ID deletion, full-name matching, and selector deletion parity.

Run `gofmt` on changed Go files, then `go build ./...`, `go vet ./...`, `go test ./... -count=1`, and `git diff --check`.
