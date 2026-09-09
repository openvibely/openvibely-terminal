# Analytics Project Scope

Use this reference when auditing or changing `/analytics` dispatch and its project-scoped fan-out.

- Treat analytics as project-scoped in both interactive TUI and headless CLI modes. Interactive `analyticsCommand.run` must require `m.selectedID` through the existing local project guard before capturing the ID, parsing further work, or starting any of the analytics requests.
- With no selected project, return the existing actionable no-project error, clear any busy state as appropriate, and issue zero HTTP requests. Do not let an empty ID silently omit `project_id` and allow backend fallback behavior.
- With a selected project, propagate that ID as `project_id` on every project-scoped analytics request, including all requests in the fan-out, and keep the interactive behavior consistent with CLI project preflight.
- Add focused regressions for no selection (zero requests and no partial output) and a non-default selected project (every request carries the expected query). Preserve the existing successful analytics rendering and optional-enrichment behavior.
