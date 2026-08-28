# Slash Subcommand Completion

Use this note when debugging or extending Tab completion for slash commands in `internal/tui/`.

## Core Pattern

Slash completion must be token-aware and registry-driven:

- Top-level command completion should continue to complete the command token, e.g. `/sk` to `/skills`.
- After a recognized command plus a space, complete the first subcommand/action token from that command's `actions` metadata in `registry.go`.
- Do not hardcode one-off subcommands in the Tab handler. If UX expects a subcommand, make it a real registered action and route it in the command dispatch switch.
- Do not rewrite `/command partial` back to `/command ` when the action token is ambiguous or unknown. Leave the current input intact so Tab never deletes the user's partial token.
- Preserve trailing arguments when completing the action token, e.g. `/tasks ru extra` should keep `extra` after completing `ru` to `run` if that is uniquely matched.
- Treat `/command action ` with a trailing space as already past the action token. Do not collapse or erase the action.

## Files To Inspect

- `internal/tui/model.go` — Bubble Tea key handling and the Tab branch that applies completions.
- `internal/tui/command.go` — command/completion helper logic and menu candidate construction.
- `internal/tui/registry.go` — command registry metadata, especially each command's `actions` list and dispatch cases.
- `internal/tui/model_test.go` — regression tests that exercise the real key path.

## Test Coverage Checklist

Add tests through the actual Bubble Tea update/key path, not only helper-level tests:

- Top-level completion still works.
- The reported shape works, e.g. `/skills lo<Tab>` completes to `/skills load` when `load` is registered.
- At least two other command groups with registered actions complete subcommands, such as `tasks`, `models`, or `automations`.
- Ambiguous prefixes do not erase the partial token.
- Unknown partials/resource refs do not erase the partial token.
- Trailing-space inputs and inputs with additional args preserve spacing and args.
