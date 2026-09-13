# Project Deletion Lifecycle

Use this reference when adding or auditing destructive project deletion in the terminal after the companion backend-client contract has been verified.

## Terminal State Recovery

- Resolve the reference before confirmation or the headless `--force` gate using the established exact ID/name, unique prefix, and unique substring precedence. Build the confirmation from a sanitized canonical label and close over the resolved ID; never resolve the raw reference again after confirmation.
- Treat deletion and catalog refresh as separate phases. After a successful DELETE, refresh the authoritative project catalog and never leave the deleted ID selected, even if the refresh fails. When refreshed projects are available, prefer the backend redirect-selected project only if it is present; otherwise use the catalog's backend order (which may place the default first). Do not locally infer a default when the backend has rejected deletion.
- A committed deletion followed by refresh transport/decode failure must report deletion success plus an explicit classified refresh-unavailable warning or status-only result. It must not claim deletion failed solely because follow-up refresh was unavailable, render the deleted project, or render an unverified empty catalog as authoritative. A DELETE failure must remain fatal and must not trigger a refresh or success message.
- If the post-delete catalog refresh is auth-required, enter the normal sign-in/session recovery state, but keep the already-committed deletion successful and present the refresh warning non-fatally. Do not replace the auth transition with a generic error or treat the refresh failure as a failed DELETE.
- Sanitize backend-controlled deletion errors only at the terminal presentation boundary. Use the established safe diagnostic formatter and an error wrapper with `Unwrap` so terminal controls, URLs, and credentials are not exposed while auth, transport, and reachable HTTP classification remain discoverable by callers.
- Clear project-bound active/selection state consistently before rendering any refreshed catalog, warning, or auth state, and ensure interactive and one-shot output use the same canonical target and failure semantics.

## Regression Matrix

Cover unknown/ambiguous/partial references, typed `yes` confirmation, Esc cancellation, headless no-force/force precedence, and no mutation before confirmation. Add state tests for deleting the active project, redirect-selected remaining project, backend-ordered fallback, refresh transport/decode failure, refresh auth failure, hostile backend deletion text, stale deleted-ID exclusion from rendered catalogs, and malformed/unexpected successful redirect hints. Assert that mutation failures issue no refresh, while successful mutations do not become failures when only refresh fails.

When the command surface changes, verify generated help, completion, README, and user-guide examples describe the same action and its destructive data consequence. Finish with focused client/terminal tests, then `gofmt -l`, `go build ./...`, `go vet ./...`, `go test ./... -count=1`, and `git diff --check`; inspect the final diff/status for scope boundaries.
