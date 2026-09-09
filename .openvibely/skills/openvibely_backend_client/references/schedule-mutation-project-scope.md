# Schedule Mutation Project Scope

Use this reference when auditing or changing schedule add, edit, toggle, or delete behavior in the OpenVibely TUI/backend client.

## Established Contract

Schedule reads and all schedule mutations are scoped to the terminal-selected project. Add, edit, toggle, and delete client APIs must receive that project ID explicitly and append `project_id` through the client's standard query helper so query-sensitive IDs are URL encoded correctly. Never rely on the backend web UI's stored selected-project preference as a fallback.

Thread the same selected project ID through every dispatch route:

- Schedule creation.
- Edit, toggle, and delete using direct schedule references.
- Edit, toggle, and delete using interactive picker or selector-cached callbacks.
- Post-mutation refreshes and any list request used to resolve a schedule reference.

Keep direct-reference, headless, and picker behavior identical. Preserve existing schedule form fields, hourly normalization, validation, confirmation, success rendering, and refresh-failure behavior while adding scope or actions.

## Schedule Edit Contract

The backend update route is `PUT /schedules/:id?project_id=...`. Verify its current form contract before changing the client. The update requires a complete run/repeat tuple, so resolve the selected timeline card first and parse the matching task's existing schedule edit form to preserve omitted settings and schedule identity. Do not reconstruct omitted values from sparse timeline-card text.

When a task page contains multiple schedule forms, match the selected schedule ID and parse only that form; do not default to the first form. Validate the parsed form's project ownership against the terminal-selected project before issuing the PUT. Carry the original schedule ID through the update.

Represent user changes separately from the final full configuration, for example with pointer/optional update fields. Merge supplied `run-at`, recurrence type/interval, and clear-context values onto the parsed existing configuration. Omit `clear_context_on_start` from the request unless the user explicitly sets `clear-context true|false` when the backend uses omission to preserve it.

Validate before HTTP: reject malformed timestamps, unsupported recurrence types, repeat intervals outside `1..365`, unknown edit actions, and surplus operands. Normalize supported aliases such as hourly only through the established helper. Preserve exact/unique-prefix/unique-substring reference matching and reject missing or ambiguous one-shot references before mutation.

After a successful PUT, emit stable plain or typed JSON mutation output before attempting refresh. If refresh fails, retain the successful mutation report and surface the refresh error through the established partial-success path rather than reporting the mutation as failed.

## Regression Contract

- Use two distinct projects: set the backend/web preference to Project A and the terminal selection to Project B.
- Use a query-sensitive selected ID such as `project B&mode=terminal`; assert the handler receives the exact decoded value and the raw request query is correctly encoded.
- Cover add, direct edit/toggle/delete, and picker edit/toggle/delete independently at request and dispatch levels.
- Assert list/resolution, existing-form fetch for edit, mutation, and refresh requests all carry the selected Project B ID.
- Prove Project B succeeds despite the stale Project A web preference.
- Select Project A against a Project B schedule and verify the backend ownership rejection remains visible with no false success output.
- For edit, cover all supported recurrence forms, a non-first schedule form, identity preservation, each independently changed field, omission preservation, explicit clear-context true and false, ambiguous/missing references, stable JSON keys, and successful mutation followed by refresh failure.
- Preserve the mutation transport's established error abstraction. For example, if a `403` body is intentionally reduced to `server error (403)`, assert non-nil failure and status classification rather than requiring backend response text.
- Assert invalid input still fails at the existing boundary and does not issue a mutation request.
- Keep generated help, completion metadata, and README examples synchronized with the edit action.

## Validation

Run focused client and dispatch regressions first, then complete:

```sh
go build ./...
go vet ./...
go test ./... -count=1
gofmt -l
git diff --check
```

Schedule parsing, project scoping, and synchronous mutation changes do not require the race detector by default. Add focused `-race` coverage only when asynchronous model state, goroutine ownership, or cancellation behavior changes, or when the task explicitly requires it.

Treat any output from `gofmt -l` or `git diff --check` as a failure to resolve.
