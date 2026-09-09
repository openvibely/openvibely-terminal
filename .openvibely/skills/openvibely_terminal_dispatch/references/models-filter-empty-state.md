# Models List Filter Empty-State Regression

Use this check when auditing or changing `/models` list rendering and filtering.

- Distinguish the underlying configured-model count from the count after applying a user filter. A configured project with zero filtered matches is not an unconfigured project.
- Branch on true source-list emptiness before or separately from filtered-row emptiness. Reserve the existing `no models configured` setup guidance for an actually empty source collection.
- When configuration exists but no model matches, render a concise `no models match "<filter>"`-style state. Echo the active filter only after stripping ANSI/control characters, collapsing it to one line, and bounding it by display width so hostile or very long input cannot corrupt terminal output.
- Preserve case-insensitive matching across model display name, model identifier, and provider. Do not alter successful table rows, capacity enrichment, or JSON/output contracts while fixing the empty state.
- Add focused renderer coverage for true emptiness, no-match output, terminal sanitization/bounding, and case-insensitive matches in each searchable field.
- Exercise the same behavior through both interactive dispatch and one-shot CLI tests. Assert the no-match message contains the safe filter, omits false configuration guidance, and matching filters still render the normal table.
- Run `go build ./...`, `go vet ./...`, and `go test ./... -count=1` after the focused regressions pass.
- Keep this separate from project-scope and provider-capacity checks: those validate which models are fetched and what diagnostics are shown, while this check validates renderer semantics and dispatch-mode parity.
