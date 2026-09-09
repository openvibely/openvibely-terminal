# Slash Command Completion And Interactive Operands

Use this note when debugging or extending Tab/Enter behavior for slash commands in `internal/tui/`.

## Registry-Driven Grammar

Completion must use the command registry as the source of truth, not help text or one-off rules in the key handler:

- Keep structured metadata for command paths, aliases, operand kinds, static choice values, and any structural boundary needed before a later choice is safe. The metadata should describe nested subcommands and argument-bearing positions at every depth, not only the first action token.
- Use the same canonical metadata and boundary predicate for Tab candidates and interactive option lists. Keep command handlers authoritative for execution and validation; completion must not duplicate mutation logic.
- Normalize aliases to the canonical command/action before looking up deeper candidates, while preserving existing alias behavior for dispatch.
- Cover static subcommands, enum-like operands, numeric and boolean choices, and resource-reference operands consistently. A path such as `/works limit` must expose the next operand choices just as a deeper path such as `/tasks reviews add` does.

## Positional Operand Safety

Completion metadata must distinguish required resource operands from later typed choices. Do not use an unconstrained token-count wildcard such as `**` to expose an enum at every later position: token count cannot tell whether an unquoted multi-word resource reference is complete, and it cannot prove that a required schedule date exists.

- Incomplete unquoted multi-word refs and missing or invalid date operands must remain unchanged. For example, `/tasks move Fix login ac`, `/tasks show Fix login rev`, and `/schedule add Daily report mon` must not rewrite `ac`, `rev`, or `mon` into a later enum/tab value.
- Use an explicit grammar or operand-state matcher that proves the prerequisite boundary. A quote-aware tokenizer can retain a token's explicit quote boundary without changing the dispatched argument value; a syntactically valid schedule timestamp can be recognized with the command's exact timestamp layout. Do not infer completion safety from token count alone.
- Do not implement the safeguard as a blanket `onlyEmpty` rule that disables every partial-token completion. Once a boundary is positively established, partial later values remain completable: `/tasks move "Fix login" ac` can complete to `active`, `/tasks show "Fix login" rev` can complete to the unique matching detail tab, and `/schedule add "Daily report" 2026-01-20T09:00 mon` can complete to `monthly`.
- Test both sides of every boundary. Incomplete refs/dates stay byte-for-byte unchanged, while quoted or otherwise structurally complete refs, valid timestamps, partial enum values, ambiguous prefixes, cursor placement, suffix text, and intentional spacing follow their respective safe behavior. Do not let preservation-only tests encode the old deep-completion defect.

## Span-Preserving Completion

Completion should identify the token under the cursor and retain raw spans, then replace only that token. Do not rebuild the line with `strings.Fields` and `strings.Join`; that loses quoted delimiters, pipe-delimited free text, repeated spacing, and intentional trailing arguments.

- Preserve all text outside the completed token, including quoted or pipe-delimited suffixes and arguments after the cursor.
- Keep cursor placement correct after replacement and handle trailing whitespace as already being past the completed token.
- Complete only unique matches. For ambiguous or unknown prefixes, leave the input unchanged rather than guessing, erasing a partial token, or auto-running a command.
- Preserve root completion and make a unique completion idempotent when the canonical command path is already present.

## Resource And Option Selectors

When interactive selection is meaningful, omitted operands should open the existing searchable UI instead of producing an opaque usage error:

- In TUI mode, Enter with a missing resource ref opens the resource selector. Tab at an empty or partially typed resource slot opens that same selector, using the partial ref as its initial filter. Never auto-execute a unique resource match from Tab.
- In TUI mode, Enter with an omitted enum, numeric, or boolean operand opens a searchable option list when the registry declares choices for that position. For a partial final operand, open a filtered option picker only after the same structural-boundary predicate used by Tab succeeds; preserve the preceding resource/reference text and replace the partial option rather than appending to it.
- Exact valid option operands must bypass the partial-option interception and dispatch through the normal command path. A picker must not reopen for already complete values, and selection must continue through normal dispatch rather than bypassing project guards or confirmation gates.
- Carry canonical selected refs or records from a selector when possible so selection does not immediately issue a duplicate lookup request. For commands needing more input after a selected ref, prefill the input with the established suffix and wait for the user instead of mutating immediately.
- Destructive commands must still reach `confirmOr` only after any missing ref/option has been selected. A canceled selector or confirmation must not issue a mutation.
- If an action has compatibility spellings at a nested path, register completion values and selector paths for every alias/depth combination, then normalize the selected path to the canonical action before dispatch. For task attachments, `attachments`, `attach`, and `attachment` must offer the same nested `add`/`list`/`delete` selector coverage; `/works` should resolve to canonical `workers` behavior while retaining discoverability.

The partial-option interception is interactive-only. Headless CLI behavior is a separate boundary: preserve the existing usage error for missing or malformed operands, do not fetch selector data in CLI mode, and keep `--force` confirmation requirements unchanged.

## Regression Coverage

Exercise the actual Bubble Tea key/update path as well as focused helpers. Cover:

- Root completion plus second-, third-, and deeper-level subcommand completion across several command groups.
- Enum-like, numeric, boolean, empty-resource, and partially typed resource operands, including aliases and ambiguous prefixes.
- Both safe sides of structural boundaries: incomplete unquoted multi-word refs/missing dates remain unchanged, while quoted refs/valid dates permit their later partial enum completion.
- Cursor-local replacement, trailing arguments, repeated spaces, quoted arguments, and pipe-delimited text.
- Enter-driven omitted-ref and omitted-option selectors, filtered partial-option selectors with replacement semantics, Tab-driven empty/filtered resource selectors, exact-option dispatch, and destructive selection followed by confirmation.
- Headless CLI usage errors and zero selector-fetch requests, while retaining normal CLI parsing, aliases, and existing command behavior.

Run `git diff --check`, `go build ./...`, `go vet ./...`, and uncached `go test ./... -count=1` from the active TUI worktree when validating a completion/dispatch change. Inspect `git status --short` and branch ancestry/ahead-behind state, then perform a fresh strictly read-only audit when the task lifecycle requires one; do not fix findings during that audit.

## Existing Files

- `internal/tui/model.go` — Bubble Tea key handling and the Tab/Enter paths.
- `internal/tui/command.go` — command lookup, quote-aware tokenization, completion helpers, structural boundary predicates, and menu candidates.
- `internal/tui/registry.go` — registry metadata and dispatch cases.
- `internal/tui/selector.go` and `internal/tui/messages.go` — searchable selector state and dispatch/prefill messages.
- `internal/tui/model_test.go`, `internal/tui/command_test.go`, `internal/tui/selector_test.go`, and `internal/tui/dispatch_test.go` — key-path, completion, selector, and CLI/dispatch regressions.
