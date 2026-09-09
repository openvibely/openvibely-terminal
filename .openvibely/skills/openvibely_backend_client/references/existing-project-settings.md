# Existing Project Settings Contract

Use this reference when implementing or auditing terminal-native project settings show/edit behavior against the OpenVibely backend.

## Contract Discovery

- Treat the backend repository as contract-only when it is out of scope: inspect the registered project edit route, handler, edit form/template, and conditional rendering branches, but do not edit, build, or generate backend files.
- Existing-project settings are exposed through the authoritative HTML edit form and `PUT /projects/:id`; do not assume a JSON settings endpoint exists or that `Accept: application/json` changes this route.
- Read the edit form before mutation. Parse current name, description, repository source, local repository path, GitHub URL, default agent, max workers, and available agent choices from the form.
- Verify that the form action identifies the exact requested project, not merely a route prefix. Reject missing, malformed, or foreign form identity before rendering or mutation.
- Classify required, optional, and conditionally omitted controls from every backend template branch. A missing control must not automatically become an empty replacement value. Either reconstruct a proven current value from authoritative metadata, preserve the existing client state when safe, or reject an incomplete form.
- Percent-escape project IDs in request paths and use the mutation authentication classifier: `401` or a redirect whose parsed path is exactly `/login` means authentication is required.

## Safe Update Workflow

1. Resolve the project reference with canonical exact/prefix/substring rules before fetching settings or creating confirmation state. Reject unknown and ambiguous references before mutation, and sanitize typed references plus backend-controlled candidate labels in diagnostics.
2. Fetch and validate the authoritative edit form, merge only options explicitly supplied by the user, and submit every current field back under the exact form field names. Never let absent conditional controls silently clear backend-owned values.
3. Resolve a default-agent operand against choices in that same authoritative form. Parse option labels without truncating valid names that contain parentheses: remove only a structurally proven provider/model suffix, or retain an independent canonical name source. Reject an invalid or nonexistent agent locally without submitting a mutation.
4. Validate source-specific option combinations before mutation. A GitHub URL must not silently succeed while the effective source remains local, and a local path must not silently succeed while it remains GitHub. Require an explicit compatible source or define and document deterministic source-switch semantics.
5. Preserve backend validation and environment behavior rather than recreating it incompletely. Surface disabled-local-path restrictions, malformed GitHub URL validation, global worker-limit failures, and clone failures from the backend's HTMX toast/error response as command failures.
6. Sanitize backend toast text and all matching errors before terminal output. Regressions must assert raw ANSI, OSC, control, newline, and carriage-return sequences are absent, not only compare output after stripping ANSI.
7. After a successful PUT, fetch and parse the authoritative form again and use that refreshed object for output/state. Tests should persist PUT state in fixtures; a fixture that always returns its pre-save form does not model the real post-save refresh contract.
8. Classify errors from the post-save authoritative refresh before applying generic “saved but refresh failed” behavior. If the refresh returns typed authentication-required, retain that type: interactive mode must enter the normal login-recovery state, and headless mode must fail nonzero with the standard sign-in guidance. Do not hide auth expiry behind a partial-success message merely because the PUT already succeeded.
9. Emit stable JSON from an explicitly tagged settings/success type and keep terminal errors nonzero, bounded, and control-sequence safe.

## Option-Like References And Separators

- Treat edit parsing as a grammar across all viable reference/option boundaries, not as “the first `--*` token starts options.” Resource names can begin with or contain complete option-looking tokens and option/value pairs.
- If a standalone separator such as `|` is supported, a resolving prefix plus a valid suffix makes the separator interpretation viable, not automatically authoritative. Also evaluate complete parses where `|` is a literal setting value, including before later options, then compare all interpretations with canonical reference precedence. An exact project-ID literal-value parse must beat a longer project-name separator interpretation so a value cannot silently rebind the mutation target.
- Audit the executable's global-flag parser separately from the command dispatcher. A resource name equal to a registered global flag such as `--json`, `--force`, or `--project` can be consumed before command parsing even when quoted. Document and test any required end-of-global-options grammar such as leading `--`.
- When more than one boundary is viable, compare resolution tiers and fail closed on equally strong candidates for different resources. Do not shorten or rebind a reference merely because one suffix parses as options.
- Keep completion grammar synchronized with separator grammar. Test option-name and option-value completion after a separator and after multi-token option-like project names, not only ordinary one-token references.
- Cover interactive tokenization, exported one-shot CLI entry parsing, and direct dispatcher parsing independently; a dispatcher-only test cannot prove shipped CLI behavior.

## Replacement Risk

- Gate only repository transitions that can replace local content or trigger a re-clone. Interactive mode should show a clear destructive warning and defer the exact resolved project plus merged settings in the confirmation closure; headless mode should require the established force flag.
- Compare GitHub repository identity using backend-equivalent normalization so HTTPS, SSH, bare host/path, and optional `.git` forms for the same repository do not produce false replacement warnings.
- Keep comparison normalization separate from validation. Do not reject malformed URLs in the warning predicate; submit them so the authoritative backend validation and clone-error behavior remain visible.
- Cancellation must issue no PUT. Confirmation must mutate the captured project/settings object without re-resolving a possibly changed reference.

## Model State And Parity

- A successful same-ID rename or metadata edit should update the selected project record/display while preserving project-scoped chat/thread/task context. Only an actual project-ID switch should clear that context.
- Do not overwrite cached list metadata with an empty value merely because a conditionally rendered edit form omitted that field. For managed GitHub projects, preserve or authoritatively refresh the repository path so selectors and project state remain accurate after edits.
- Tag settings read/update result messages with the originating project and session/auth generations. Reject delayed results before changing selection metadata, transcript, busy state, authentication state, or rendered output.
- Route interactive slash commands and one-shot CLI commands through the same parser and dispatcher. Keep help, README examples, force guidance, path quoting, accepted repository-source values, and JSON shape synchronized.
- Completion rules must work after every repeated option/value pair, not only the first pair. Test second and later option-name completion plus source-, worker-, and default-agent value completion at realistic command depths.

## Regression Matrix

Cover form identity mismatch and missing controls; every conditional template branch; all editable fields and omitted-field preservation; same-ID rename/context and cached-path preservation; exact, partial, duplicate, ambiguous, and unknown project and agent references; agent names containing parentheses; Unix, Windows drive, UNC, and space-containing paths; incompatible source-specific options; invalid and globally excessive worker limits; disabled local paths; malformed GitHub URLs; clone failures and terminal-unsafe toast content; exact-login and near-login redirects; post-save auth expiry in interactive and headless modes; cancellation, confirmation, forced and unforced CLI mutation; equivalent GitHub URL forms; option-like project names; separator-as-literal-value collisions against longer matching projects; registered-global-flag project names; ambiguous grammar boundaries; separator-aware completion; interactive/one-shot parity; and post-save authoritative refresh.

For the Go client/TUI integration, finish with focused contract tests followed by `gofmt -l`, `go build ./...`, `go vet ./...`, `go test ./... -count=1`, and `git diff --check`. Add focused race-detector coverage only if asynchronous selection, refresh, or shared model state changes. If the task branch is merged or rebased after validation, rerun the applicable checks on the final audited HEAD, especially when the integration changes overlapping files; pre-merge evidence is stale for that HEAD.
