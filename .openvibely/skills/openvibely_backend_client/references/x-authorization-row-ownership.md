# X Authorization Row Ownership Contract

Use this reference when implementing or reviewing X mention authorization access in the terminal client and supported backend.

## Contract

- A route query or page-level authorization form `project_id` establishes request/page scope, not ownership of an individual parsed row.
- The supported X settings template must emit persisted, row-level safe fields: `data-x-authorized-user-id` for the canonical authorization record ID, `data-x-authorized-project-id` for the stored project owner, `data-x-user-id` for the numeric X identity, and `data-x-username` when a username is stored. The row's canonical relative delete control must also contain exactly one matching `project_id` query value and the matching row ID.
- The client must require the structured row ID, project marker, and numeric identity, cross-check them against the delete control and selected project, and reject missing, empty, malformed, duplicate, conflicting, foreign, or otherwise inconsistent markers. Do not fall back to visible `@username` or `ID` prose, because rendered text is not persisted ownership evidence.
- Keep parsed output limited to the canonical ID, selected project ID, numeric X user ID, and optional normalized username. Preserve fail-closed behavior and destructive revalidation immediately before deletion.

## Authorized Backend Contract Changes

- Keep the backend read-only when the task does not explicitly authorize backend changes. If the reviewed client/backend contract requires a supported backend fix and the task explicitly authorizes it, update the source `.templ` file and regenerate tracked `_templ.go` output from the backend repository root with `make templ`; do not hand-edit generated output.
- Add backend render regressions proving the persisted markers are emitted and remain secret-free. Keep client fixtures copied from that real markup and add regressions for markerless prose, missing or foreign ownership, duplicate/conflicting markers, malformed scope, canonical identity parsing, and delete revalidation.
- In a multi-repository task, commit and validate backend and client changes as separate exact objects. Stage only the intended backend paths when unrelated worktree changes exist, and report backend full-suite failures outside the changed package without altering unrelated behavior.

## Validation Handoff

- For each repository, record the full `git rev-parse HEAD`, focused regressions, build, vet, uncached tests, formatting, and diff checks independently. A client exact-HEAD note does not prove the backend commit or generated template was validated.
- Compare full SHA values in guards; do not compare a full `git rev-parse HEAD` result to an abbreviated literal. If a validation wrapper stops on a bad guard, state that no product command ran and rerun with the full expected SHA.
- Treat unrelated or reproducible pre-existing backend browser/layout failures as a validation limitation, not as a reason to change unrelated code. Re-run the failing test in isolation and report both the passed feature-focused checks and the blocked broader suite accurately.
