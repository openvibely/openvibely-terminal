# Analytics Usage Limit Rendering

Use this reference when changing provider quota or account-diagnostic rendering in `internal/tui/`.

- Treat `AccountUsage.PrimaryLimit` and `AccountUsage.Limits` as one logical collection. Route `/analytics usage` through the same primary-first normalization used by `/models capacity` (currently `providerLimitRows`), rather than iterating only `Limits`. Preserve first-seen order and render an exact primary/secondary duplicate once.
- Keep quota rows allowlisted to the established operational fields: limit label/type, used percentage, and reset time. Render provider name/status/plan/error independently so metadata and error text remain visible when limits are absent or a provider fails; never render `AccountDetail`, tokens, API keys, or credential-like values.
- Test both layers: direct renderer cases for primary-only, primary-plus-duplicate, secondary-only, and no-limit/error providers; dispatch/JSON cases for totals, model breakdowns, provider metadata, selected `project_id` query scoping, and privacy; and interactive versus CLI plain-text parity. Confirm `/models capacity` output remains unchanged.
- Preserve direct renderer, dispatch, JSON, privacy, and interactive-versus-CLI parity coverage for the changed usage-limit behavior.
