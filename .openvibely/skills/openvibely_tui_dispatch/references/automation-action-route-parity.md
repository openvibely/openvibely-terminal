# Automation Action Route Parity

Use this reference when changing `/automations run`, `run-now`, `pause`, `resume`, or `delete` in `internal/tui/registry.go`.

- Keep picker and typed-reference flows separate only through selection, matching, confirmation, cancellation, and busy-state handling. Once an automation ID is resolved, both must call one local shared completion helper.
- The shared helper must call `AutomationAction` exactly once with the resolved ID and current selected project, then return the canonical action status plus the structured refreshed automation list. Do not use the generic text-page reload path for these mutations.
- Preserve transport and presentation normalization: user action `run` and alias `run-now` use backend action `run-now`, while their success status starts with canonical `run:` wording.
- On mutation failure, return the error without success text or refresh. If the mutation succeeds but the structured refresh fails, retain only the success status.
- Do not move typed-reference ambiguity/unknown handling, picker cancellation, delete confirmation timing, aliases, or single-item selection into the shared completion helper. They are route-specific preconditions.

Regression coverage should compare typed and picker results for every action, aliases, mutation and refresh failures, ambiguous/unknown refs, picker and confirmation cancellation, and single-item auto-selection. Assert request counts and `project_id` to prevent duplicate or unscoped mutations.
