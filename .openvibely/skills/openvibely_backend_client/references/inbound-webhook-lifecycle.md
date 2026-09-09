# Inbound Webhook Lifecycle Contract

Use this reference when extending or auditing terminal support for project-scoped inbound webhooks.

## Transport And Scope

- Verify the current backend handlers before coding. The established web lifecycle is rooted at `/channels/webhooks`; CRUD is form-based, while detail and the test/rotation success responses have JSON branches when HTMX is not requested.
- Carry the selected project ID on list, detail, create, edit, test, rotate, and delete. Resolve all existing-resource references from that project's structured collection before mutation so missing, ambiguous, and foreign-project references fail locally.
- Traverse every advertised list continuation, preserve first-seen order, and deduplicate by stable webhook ID before applying standard exact, unique-prefix, and unique-substring matching.

## Mixed Channel Page Filtering

- The ordinary channel list and inbound webhooks can share one HTML page. When webhook management is exposed separately, removing only nodes with `data-webhook-id` is insufficient because it can leave the webhook heading, empty-state text, create controls, and other section chrome in channel output.
- Prefer removing the nearest semantic `section` containing the stable `#webhook-card-list` marker. If older fragment markup has webhook cards but no identifiable enclosing section, fall back to removing only `data-webhook-id` cards rather than deleting an unrelated parent container.
- Regression fixtures should include at least two messaging integrations plus the webhook heading, create control, empty state, and an entry/endpoint. Assert every webhook-specific string is absent, every messaging integration remains, and plain channel listing still makes only its intended first-page request.

## Secret Safety

- Define allowlisted client response structs. Do not retain or expose backend secret fields in list/detail/create/edit/test output, JSON output, diagnostics, or errors.
- Render only a safe usable endpoint URL or path. Run all backend-derived names, paths, statuses, and errors through the terminal sanitizer before plain output.
- Rotation may return a newly usable endpoint value as its explicit command result, but it must not make secrets part of default list/detail output or diagnostics.

## Mutation Semantics

- Treat edit as read-modify-write when the backend form replaces configuration: fetch secret-free detail, apply only explicitly supplied options, and submit the complete non-secret configuration so omitted enabled state, priority, instructions, templates, and agent assignments are preserved.
- Decode test responses into a narrow result containing the created task ID and render that ID in both plain and JSON modes.
- Resolve rotate/delete targets before confirmation. Interactive mode must use the shared confirmation flow; one-shot CLI mode must require `--force`. Cancellation, ambiguity, missing refs, and project mismatch must issue no mutation.
- Keep inbound-webhook lifecycle commands separate from legacy outbound channel action dispatch when adding the new surface, so existing `/channels` behavior remains unchanged.

## Regression Coverage

- Client tests: exact method/path/project scope/form fields, multi-page order and deduplication, detail parsing, complete update forms, task-ID decoding, rotation decoding, API failures, and secret absence from structs and surfaced errors.
- Dispatch tests: every lifecycle verb, multiword/exact/prefix/substring matching, ambiguity and foreign-project isolation, partial-edit preservation, selector behavior, safe endpoint rendering, confirmation cancel/accept, and terminal sanitization.
- CLI tests: plain and JSON output, empty collections, test task IDs, unforced rotate/delete rejection, forced mutation, help/usage consistency, and secret sentinel absence from stdout/stderr/errors.
- Finish transport/dispatch changes with focused tests, then `go build ./...`, `go vet ./...`, `go test ./... -count=1`, `gofmt -l`, and `git diff --check`. Add focused race-detector coverage only if asynchronous dispatch, cancellation, or shared model state changes.
