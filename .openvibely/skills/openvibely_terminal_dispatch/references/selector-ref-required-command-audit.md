# Selector Ref-Required Command Audit

Use this reference when auditing or implementing inline selector fallback for ref-required slash commands in `internal/terminal/`.

## Required Audit Steps

1. In `internal/terminal/registry.go`, grep for `resolveTask(` and `matchRef(` and map each call back to the command/action and its missing-argument guard.
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

## Review Comment Ref-First Commands

- Treat `/tasks reviews add <task> <file>:<line> <comment>` as a ref-first multi-argument command. Coverage for the parent `/tasks reviews` list command does not prove that the nested `add` action handles a missing task reference.
- In TUI mode, `/tasks reviews add` with no task reference must open the task selector and, after selection, prime the input as `/tasks reviews add <canonical-task-ref> ` so the user can enter the file/line and comment. It must not return only the usage error or dispatch an incomplete review request.
- In headless CLI mode, preserve the usage error and make no selector list or review mutation request when the task reference is missing.
- Add an actual registry-dispatch regression for the no-reference nested action, including selector activation, exact prefill, and the eventual structural `file:line` operand parsing. Keep the existing review transport contract unchanged and ensure selector selection does not cause a redundant task lookup.

## Prefill Record Reuse

- If selection only prefills a later multi-argument command, carry the selected canonical record with the prefilled input instead of resolving the resource with a second list request when the command is submitted.
- Scope the carried record to the selected project and the exact generated reference. Before reuse, require that the cached record's stable ID still matches the generated reference; otherwise fall back to the normal `resolveTask`/`matchRef` path rather than bypassing reference semantics.
- Clear the carried record on project changes, selector cancellation/replacement, command submission, empty-input cancellation, and any user edit that changes the generated prefix. This prevents a stale selection from being used after the user changes the resource reference or project.
- Preserve normal resolution for explicitly typed commands and for prefill commands whose input no longer has the generated prefix. The optimization must not alter exact, prefix, substring, or ambiguity behavior.
- Add request-count coverage that separates selector population from final submission: one task-list fetch to populate the picker, no second task-list fetch after selecting and completing the prefilled review command, and exactly one review mutation.

## Resize

- Centralize transcript-height calculation so `handleSelector`, `clearSelector`, and `resize()` use the same selector-aware formula rather than duplicating `height-5` and `height-14` logic.
- Include a resize-while-active test: open a selector, deliver `tea.WindowSizeMsg`, then assert `selectorActive` remains true and `transcript.Height` is still the selector-reserved height.

## Known Regression Patterns

- `resize()` unconditionally setting `m.transcript.Height = max(3, m.height-5)` while `selectorActive` is true undoes the selector shrink and can push the picker below the visible terminal after a terminal resize. The resize path should apply the selector reservation, e.g. the same `height-14` style formula used when activating the selector.
- Commands such as `/tasks move`, `/tasks order`, and `/schedule add` are ref-first multi-argument commands. If invoked with no args, they should enter selector-prefill mode in TUI rather than returning only `usage:`. A boolean pipe-only `selectorPrefill` may be too narrow; support a configurable suffix such as `" "` versus `" | "` when adding selectors for these commands.

## Selector Fixture Cardinality

- The selector intentionally auto-selects when action-specific filtering leaves exactly one eligible item. When a regression asserts that the picker remains active or inspects multiple visible candidates, provide at least two eligible records after filtering; a fixture with Base plus one editable personality will select the sole editable item immediately and make `selectorActive` false. Test direct selection separately when the single-candidate behavior is the subject under test.
