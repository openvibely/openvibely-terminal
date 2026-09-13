# Automation Typed-Reference Resolution

Use this reference when changing or consolidating typed-reference handling for `/automations show` (including `open`), `edit`, lifecycle actions (`run`, `run-now`, `pause`, `resume`, `delete`), or an interactive edit entry point.

- Keep one narrow automation resolver responsible for loading the selected project's automation list and applying the established exact-ID/name, unique-prefix, then unique-substring matching policy. It should return the canonical automation record or the existing unknown/ambiguous error.
- Route each typed-reference flow through that resolver instead of independently calling `ListAutomations` and matching ID/name variants. Preserve each caller's work after resolution: detail fetching/rendering, edit preview/save setup, action normalization, confirmation timing, selector behavior, and CLI/TUI mode rules are not part of resolution.
- Do not use the resolver to replace a selector callback that already has the selected canonical record; avoid a second list request after picker selection. Keep typed and picker request counts distinct and intentional.
- Preserve project scoping on every list, follow-up fetch, and mutation request. Unknown or ambiguous typed refs must perform no detail, preview, save, or action request.

Regression coverage should table-test exact ID, exact name, unique prefix, unique substring, unknown, and ambiguous references through every affected typed path. Assert the same canonical target or local error, one list lookup per typed invocation, no follow-up request on resolution failure, and no duplicate list lookup after interactive selection.
