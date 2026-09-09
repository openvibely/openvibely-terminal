# Schedule Edit Option-Boundary Parsing

Use this when changing or auditing `/schedule edit <ref> <setting> <value> [...]` in `internal/tui/registry.go`.

## Deterministic Boundary Rule

Treat the first recognized setting token after the reference as the start of the settings section. The setting names are `run-at`, `repeat`, `interval`, and `clear-context`. Parse the rest only as complete setting/value pairs; never move the boundary later or reinterpret malformed settings as title text after validation fails.

This grammar intentionally gives settings precedence. A command such as `/schedule edit Nightly repeat daily interval 5` means update `Nightly` to repeat every 5 days: send backend `repeat_type=daily` and `repeat_interval=5`. Titles containing setting-like words remain supported by grouping or quoting the entire title, for example `/schedule edit "Run repeat daily report" interval 5`; interactive quote-aware tokenization and shell argument grouping make that title one operand.

Parse and validate the command before schedule discovery or any other HTTP request. Reject missing values, unknown or duplicate settings, malformed timestamps, unknown repeat values, intervals outside `1..365`, invalid booleans, and surplus operands locally. In headless CLI, attach schedule argument validation to the `schedule` command definition so invalid edits fail before project preloading; verify the validator was not accidentally registered on a neighboring command.

## Resolution Semantics

After successful syntactic validation, resolve the single parsed reference with the shared `matchRef` ordering: unique exact ID, unique exact case-insensitive title, unique prefix, then unique substring. Preserve ambiguity errors and issue no detail request or mutation for ambiguous references. Do not restore multi-candidate ranking across possible option boundaries; that strategy conflicts with deterministic setting precedence and zero-request malformed-input validation.

## Regression Coverage

Add focused interactive and headless tests for:

- `repeat daily interval 5`, asserting `repeat_type=daily` and `repeat_interval=5`.
- A quoted or shell-grouped title containing setting words.
- Existing exact-ID, title, prefix, substring, and ambiguity behavior after parsing.
- Invalid timestamp, recurrence, interval, boolean, duplicate setting, missing value, and surplus operand cases, asserting zero HTTP requests, including no project or schedule-list GET in headless mode.
- Existing multi-setting edits and omitted-setting preservation.

Run focused `TestScheduleEdit` and `TestCLIScheduleEdit` tests first, then `gofmt`, `go build ./...`, `go vet ./...`, `go test ./... -count=1`, and `git diff --check`. Option parsing alone does not require the race detector; if asynchronous schedule model behavior changes, run only the relevant package or tests under `-race` unless the task explicitly requires a wider run.
