# Canonical User Action Versus Legacy Transport

Use this note when renaming a registered slash action while the backend still exposes a legacy route or when preserving an old spelling as a compatibility alias.

## Workflow

- Inspect the backend handler or route read-only before changing client code. Do not infer a transport path from the new user-facing action; if the backend still exposes a legacy path such as `/automations/:automationId/run-now`, keep that path as an explicit client transport mapping.
- Treat the registry action metadata as the source for advertised actions, help, and slash completion. Replace the canonical advertised token with the new token; do not leave the legacy token in the action list where it would remain canonical.
- Normalize the new spelling and any retained legacy alias at one dispatch boundary before selector setup, validation, or mutation dispatch. Use the canonical token for command state, selector prefills, usage/output prefixes, help, documentation, and test expectations; translate only the HTTP transport segment when calling the backend.
- Keep the metadata layers aligned: the canonical `actions`/help surface identifies the advertised action, while `selectorPaths` controls ref-required completion behavior. When a compatibility alias remains accepted, include both the canonical action path and the alias path in `selectorPaths`; otherwise Tab on the canonical action followed by a space can bypass the required resource picker even though the action dispatches correctly.
- Decide alias policy explicitly. If the old spelling remains accepted, test it as an undocumented or compatibility-only alias and ensure it does not appear in normal completion/help. If it is rejected, return canonical usage consistently in interactive and headless modes.
- Preserve behavior unrelated to the rename: project guards before action work, selector behavior for missing refs, confirmation and `--force` rules, lifecycle/error propagation, JSON/plain output shape, and other actions in the command group.

## Regression Matrix

Cover the registry/action list, help and documentation examples, real slash completion, interactive no-argument selector dispatch, headless execution, canonical result text, legacy-alias normalization, exact backend method/path, and `project_id` propagation. For ref-required actions, send the actual Bubble Tea `KeyTab` event for both the canonical spelling and any retained alias, assert that each opens the searchable picker without mutation, and assert that alias dispatch normalizes pending command state to the canonical spelling. Include both output modes and the unchanged confirmation/lifecycle/error paths. Build a fresh binary and inspect runtime help so the shipped command surface matches source metadata.

Finish a repository-wide Go command change with focused tests followed by `go build ./...`, `go vet ./...`, `go test ./... -count=1`, `gofmt -l`, and `git diff --check`. Add focused race-detector coverage only when command execution changes asynchronous or shared-state behavior.
