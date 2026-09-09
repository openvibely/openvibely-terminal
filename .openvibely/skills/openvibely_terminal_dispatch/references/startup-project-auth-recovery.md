# Startup Project Preference During Authentication Recovery

Use this reference when auditing or changing project selection, project loading, or login/session recovery in `internal/terminal/`.

A startup-only project preference such as `wantProject` or a `-project` argument is an initial selection hint, not a permanent source of truth. Once the user explicitly selects another project, post-login or session-recovery reloads must use the current selected project and must not reapply the stale startup preference. Clear, supersede, or otherwise scope the startup preference when the manual selection succeeds; keep the login retry bound to the current project ID before reconnecting project-scoped events.

Concrete failure path: launch with startup project `alpha`, manually select `beta`, let authentication expire, then complete `/login`. If the retry passes the stale startup reference `alpha` into project loading, the response can select `alpha` again and reconnect live events for the wrong project. Expected behavior is that `beta` remains selected and the refreshed SSE request is scoped to `beta`.

Regression coverage should exercise both the startup-preference path and the override path: load with `alpha` and verify it is initially selected; switch explicitly to `beta`; deliver login success and the subsequent project-load result; assert `selectedID`, visible project state, load arguments, and `/events/live?project_id=beta`. Also cover recovery with no startup preference and the unchanged behavior when the user never overrides the startup choice. Deliver delayed pre-login project/auth messages where applicable and assert that project/auth epochs prevent them from changing the current selection.

Relevant areas are project-selection and project-load/login handlers in `internal/terminal/model.go`, their message types in `internal/terminal/messages.go`, and end-to-end model tests using the existing `httptest`/SSE test helpers. Run the focused regression and the standard `go test ./... -count=1` suite after implementation.
