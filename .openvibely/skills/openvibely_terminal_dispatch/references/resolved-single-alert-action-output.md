# Resolved Single-Alert Action Output

Use this pattern when consolidating duplicate terminal action tails for one already-resolved alert.

- Keep selection, picker construction, typed-reference resolution, confirmation, captured-target protection, and CLI `--force` gating outside the output helper. The helper should accept only the canonical resolved alert and the requested single-alert action.
- Keep the helper narrow: for delete, call `DeleteAlertAndList` and render the list returned by the DELETE response; for `read`, `approve`, `reject`, and `dismiss`, call `AlertAction`, then perform the established best-effort alert refresh/render. Preserve existing status text, scoped project ID, endpoint, and JSON/plain output shapes.
- Invoke the helper only after resolution in picker dispatch, typed-reference dispatch, and an interactive delete-confirmation closure that already captured the canonical alert. Do not route bulk actions through it; bulk operations retain their counted JSON output and independent refresh behavior.
- Regression tests should compare picker and typed action endpoint/output parity for every single-alert action, assert no extra picker-resolution fetch, verify DELETE-response list rendering, prove a changed catalog cannot rebind a captured interactive delete target, and retain bulk-output isolation.
- Validate changed Go files with `gofmt`, focused terminal tests, `go build ./...`, `go vet ./...`, uncached `go test ./... -count=1`, and `git diff --check`.
