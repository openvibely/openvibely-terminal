# Unsupported User-Command Removal

Use this reference when retiring a slash command or headless CLI command because the client points at an unsupported or obsolete backend operation.

## Audit Before Editing

- Establish the active TUI worktree and the backend repository root. Keep the backend read-only when only the TUI is in scope.
- Verify the client operation against current backend route registration, handler source, and generated Swagger. Search aliases and nearby supported actions before concluding that the command has no contract; do not treat a stale client method or command name as backend support.
- Record supported alternatives separately, then preserve unrelated build, Makefile, CI, and backend automation functionality that is not a user-facing command.

## Remove The Whole Surface

- Prefer the shared command registry as the single removal point so interactive dispatch, slash completion, generated help, and headless lookup cannot diverge.
- Inventory and update every user-facing reference: parser/dispatch branches, aliases, action metadata, help text, completion candidates, README/API documentation, examples, fixtures, and tests. Check both bare and leading-slash headless forms.
- Remove obsolete client methods and endpoint assertions only when they serve the retired command. Keep generic transport/auth tests by redirecting them to a neutral test-only endpoint rather than deleting unrelated coverage.
- Add a failing-first regression while the stale registration still exists. Assert the command is absent from lookup, completion, and generated help, and that both `command` and `/command` return the normal unknown-command error without making a backend request.

## Validate

- Run a repository-wide stale-reference scan for the command spelling, aliases, client method, and endpoint after editing.
- Run focused registry/client tests, then `go build ./...`, `go vet ./...`, uncached `go test ./... -count=1`, `gofmt -l`, and `git diff --check` as applicable. Command removal alone does not require the race detector; add focused `-race` coverage only if concurrent model behavior also changes. Use a freshly built binary for headless smoke checks.
- If race validation exposes a failure outside the retired command's changed paths, leave unrelated behavior unchanged, verify the focused and normal suites, and report the external race explicitly rather than weakening or broadening the removal.
