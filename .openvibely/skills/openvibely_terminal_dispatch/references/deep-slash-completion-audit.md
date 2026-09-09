# Deep Slash-Command Completion Audit

Use this reference when extending or auditing registry-driven, depth-aware slash-command completion and inline option/resource selectors.

## Operand Boundaries

Do not model a required operand followed by an enum as an unconstrained token-count wildcard such as `**`. Token count cannot tell whether an unquoted multi-word resource reference is complete, and it cannot prove that a required schedule date exists. Completion must understand the command's operand kinds and structural boundaries before offering later values.

Required-resource and date operands must remain unchanged when incomplete. Cover unquoted multi-word refs and missing dates, for example `/tasks move Fix login ac`, `/tasks show Fix login rev`, and `/schedule add Daily report mon`; none should rewrite `ac`, `rev`, or `mon` into a later enum/tab value. Also cover quoted refs, cursor-in-the-middle completion, preserved suffix text, and intentional spacing.

Prefer an explicit grammar or operand-state matcher that distinguishes required resource, date, numeric, boolean, and enum positions. A successful enum completion should be tested only after its prerequisites are structurally satisfied, and ambiguous prefixes should remain unchanged.

## Proven Boundaries And Partial Values

Do not implement the incomplete-reference safeguard as a blanket `onlyEmpty` rule that disables every partial-token completion. Once the grammar has positively established a boundary, a partial enum/tab value must remain completable: for example, `/tasks move "Fix login" ac` can complete `ac` to `active`, `/tasks show "Fix login" rev` can complete `rev` to the matching detail tab, and `/schedule add "Daily report" 2026-01-20T09:00 mon` can complete `mon` to `monthly`. The matcher must distinguish an unquoted free-form reference or missing date from a quoted/otherwise structurally complete reference and a syntactically valid timestamp before considering later enum candidates.

Test both sides of each boundary. Incomplete unquoted multi-word refs and incomplete schedule prerequisites must remain byte-for-byte unchanged, while quoted refs, valid dates, partial enum values, cursor placement, suffix text, and intentional spacing must complete without rewriting unrelated operands. Do not let tests that only assert preservation encode a missing completion behavior.

## Option Replacement

When an interactive option selector is opened for a partially typed or invalid final operand, retain replacement metadata for the operand span. Selecting `active` from `/tasks move api ac` must produce the equivalent of `/tasks move api active`, not append to a pending command that still contains `ac` (`/tasks move api ac active`). The replacement path must preserve the resource portion, quoted/piped arguments, cursor/suffix text, and destructive confirmation gates.

Test both omitted and partial option flows for enum, numeric, and boolean operands. Verify that selection does not mutate before confirmation, that the resulting command parses exactly once, and that CLI mode still returns its normal usage error rather than opening a selector.

## Alias Metadata Parity

Every supported command alias must share the canonical command's completion, selector, and nested-action metadata. Register required root spellings such as `/works` as aliases where the product surface names them, and duplicate or derive nested paths for compatibility aliases such as `attach` and `attachment`; root lookup alone is not enough when depth-aware completion or Tab-to-picker logic examines the path.

Add table-driven tests for canonical and alias roots at every supported depth, including action completion, nested action completion, required-ref picker behavior, and canonicalization of the completed root. Help, dispatch, completion, and selector behavior should agree on the same alias vocabulary.

## Canonical Actions And Transport Aliases

When a user-facing action is renamed, use the canonical action in every registry-owned metadata table, especially `selectorPaths`, completion rules, help usage, and selector pending-command construction. Keep a legacy spelling only in explicit compatibility parsing or backend transport mapping. For example, if `/automations run` is canonical and `run-now` is a compatibility alias for the backend route, Tab on `/automations run ` must still open the automation picker; a `run-now` entry in `selectorPaths` does not cover the canonical path. Add a focused Tab-to-picker regression for both the canonical action and any intentionally supported legacy spelling, and compare the registry action list, help, dispatch, and selector metadata for drift.

## Audit Validation

After fixing these classes of issues, validate from the active worktree with `go build ./...`, `go vet ./...`, and `go test ./...`. When the change touches concurrent model behavior, run the narrowest relevant package or tests under `-race`; expand to `./...` only when behavior crosses package boundaries or the task explicitly requires it. Inspect `git status --short`, branch ancestry/ahead-behind state, the net diff against `main`, and `git diff --check`; then perform a fresh strictly read-only audit. Do not fix findings during that audit.
