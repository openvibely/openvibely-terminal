---
kind: openvibely.agent_skill
version: 2
skill:
    key: terminal_model_save_completion
    name: Terminal Model Save Completion
    scope: project
    description: Preserve add/edit model mutation completion behavior while extracting narrow shared post-save refresh, status, and output logic.
---

# Terminal Model Save Completion

Use this skill when consolidating duplicated post-save completion logic for model mutations in `internal/terminal/registry.go`, especially add/edit flows that refresh models, inspect OAuth status, and render plain or JSON output.

## Extraction Boundary

1. Establish the current behavior with focused tests before editing. Keep setup, validation, confirmation, mutation requests, and model-ID resolution in each command.
2. Extract only the common completion stage into one narrow private helper. The helper may own post-save refresh, authentication/error classification, OAuth status handling, authorization handoff output, and plain/JSON rendering.
3. Pass operation-specific policy into the helper rather than merging the flows. Add must resolve a newly created OAuth model by configured name; edit must use its already resolved model ID. Preserve any operation-specific refresh-unavailable wording and fallback URL behavior explicitly.
4. Do not initialize a generic authorization URL unless the original branch did so. In particular, an unmatched OAuth model after add should preserve its prior empty handoff URL rather than silently gaining a generic Models-page URL.
5. Preserve the no-premature-success rule: refresh or OAuth status authentication failures must return their existing error behavior without claiming that the mutation completed successfully.

## Regression Matrix

Cover both add and edit through the real dispatch/result paths. Include non-OAuth success and post-save refresh failure, OAuth connected, authorization-required, unknown status, refresh-unavailable, and post-save authentication-error cases. Assert add name-based resolution and edit known-ID lookup independently, including request counts and request arguments.

For add-side OAuth resolution, include competing model names where only the exact configured name may win, and include an empty or unknown backend status that must not render or report `connected`. For refresh failures, exercise both plain and JSON output: non-OAuth mutations should retain only their success status without exposing refresh errors or fabricated model data, while OAuth add should retain its explicit unknown-status handoff and must not issue an OAuth-status request when the model list is unavailable.

For output safety, compare unchanged plain and JSON success/fallback output, assert credentials are absent from transcript, history, and JSON, and exercise authorization handoff output without secrets. Keep project selection, validation, confirmation, mutation payloads, and model-ID resolution assertions in place so the helper extraction cannot broaden scope.

When adding tests beside existing cases, preserve the neighboring function declarations and run the focused package tests immediately after each insertion. Before recording exact-HEAD evidence, verify the anchored `go test -run` selector against the actual `Test...` declarations or `go test -list` output so every intended add/edit regression is matched and no similarly named case is silently omitted.

## Validation

Format changed Go files, run focused model/CLI tests, then run `go build ./...`, `go vet ./...`, uncached `go test ./... -count=1`, and `git diff --check` from the active task worktree. Review the final diff and worktree for unintended changes.
