# Interactive Events Operand Validation

Use this reference when auditing or changing interactive `/events [on|off]` dispatch in `internal/tui/registry.go`.

- Parse the optional interactive mode explicitly. Accept the documented bare toggle, `/events on`, and `/events off`, plus the intentionally supported compatibility aliases `/events true` and `/events false`. Reject unknown operands such as `/events typo` and all surplus operands such as `/events off extra` with the canonical local usage `usage: /events [on|off]`.
- Validate the complete operand list before changing `showEvents`, `busy`, or stream ownership; invoking or replacing an existing SSE cancel function; appending a success message; or otherwise claiming the command ran. Invalid input must leave event visibility and SSE lifecycle unchanged.
- Keep this interactive argument contract separate from one-shot stream lifecycle validation in `references/one-shot-live-events.md`. Do not change the headless CLI path while tightening interactive dispatch unless the task explicitly changes that contract.
- Add table-driven regressions through the real dispatch path, crossing both initial visibility states with bare toggle, `on`, `off`, `true`, `false`, unknown mode, and extra operands. Assert exact state transitions for valid forms and unchanged state, no success text, no returned backend/SSE work, no HTTP requests, and unchanged existing SSE cancellation ownership for invalid forms.
- In Bubble Tea dispatch tests, distinguish the local command that returns the usage/error message from backend or SSE work. Inspect immediate dispatcher state before executing that returned command when proving the handler itself did not mutate fields such as `busy`; normal centralized result handling may clear `busy` while rendering the error.
- Run focused dispatch tests first, then verify the final tree with `go build ./...`, `go vet ./...`, and `go test ./... -count=1`.
