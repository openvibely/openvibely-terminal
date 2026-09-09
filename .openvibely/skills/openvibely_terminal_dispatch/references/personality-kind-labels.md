# Personality Kind Labels

When personality type labels appear in more than one TUI surface, centralize the classifier in a private helper near the personality renderers instead of duplicating `IsPreset`/`HasCustom` conditionals in list, detail, and selector code. Preserve the established precedence: non-preset entries are `custom`; preset entries with `HasCustom` are `override`; remaining presets are `built-in`. Reuse the helper for selector item details as well as visible list/detail output so all surfaces agree.

Add focused regression coverage for the classifier truth table, including the non-preset plus `HasCustom` case, and assert the user-visible labels in list and detail rendering plus selector details. Keep the change narrow: do not alter personality resolution, CRUD, active-state propagation, project scoping, or mutation behavior while consolidating labels.
