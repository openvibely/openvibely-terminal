# Selector Ref-Required Command Audit

Use this reference when auditing or implementing inline selector fallback for ref-required slash commands in `internal/tui/`. Audit every registered dispatch path that eventually calls `resolveTask` or `matchRef`, including multi-argument commands where the first argument is the resource ref.

## Required Audit Steps

1. In `internal/tui/registry.go`, grep for `resolveTask(` and `matchRef(` and map each call back to the command/action and its missing-argument guard.
2. For each command that can run in TUI mode and requires a user resource ref, the empty-ref path must use `selectorOr(...)` so TUI opens a selector while CLI keeps the usage error.
3. Include multi-argument ref-first commands in the selector table even if they also need additional arguments. These need a configurable prefill path rather than immediate dispatch.
4. Check resize behavior with a selector already active. `tea.WindowSizeMsg` can arrive while the picker is open; `resize()` must preserve selector-aware transcript height instead of restoring the normal `height-5` viewport.
5. Add or update tests for both activation and visible layout. State-only checks such as `selectorActive == true` can pass while the rendered selector is off-screen.

## Multi-Argument Prefill

- Commands whose next argument is not pipe-delimited need a configurable selector prefill suffix. Do not reuse the pipe-only `/cmd <ref> | ` behavior for commands such as `/tasks move`, `/tasks order`, or `/schedule add`; selecting a task should prime input as `/tasks move <task> `, `/tasks order <task> `, or `/schedule add <task> `.
- Keep CLI behavior unchanged for these no-ref paths. In `cliMode`, missing refs must still return the original usage error and make zero backend fetch or mutation calls.
- Extend `TestNoArgOpensSelectorPerArea` whenever adding a ref-required slash command.
- For selector-prefill commands, assert Enter closes the selector, primes the input with the exact suffix, and performs no mutating backend request.
- Add or maintain CLI-mode coverage asserting missing refs produce a usage error and no backend calls.

## Resize

- Centralize transcript-height calculation so `handleSelector`, `clearSelector`, and `resize()` use the same selector-aware formula rather than duplicating `height-5` and `height-14` logic.
- Include a resize-while-active test: open a selector, deliver `tea.WindowSizeMsg`, then assert `selectorActive` remains true and `transcript.Height` is still the selector-reserved height.

## Known Regression Patterns

- `resize()` unconditionally setting `m.transcript.Height = max(3, m.height-5)` while `selectorActive` is true undoes the selector shrink and can push the picker below the visible terminal after a terminal resize. The resize path should apply the selector reservation, e.g. the same `height-14` style formula used when activating the selector.
- Commands such as `/tasks move`, `/tasks order`, and `/schedule add` are ref-first multi-argument commands. If invoked with no args, they should enter selector-prefill mode in TUI rather than returning only `usage:`. A boolean pipe-only `selectorPrefill` may be too narrow; support a configurable suffix such as `" "` versus `" | "` when adding selectors for these commands.
