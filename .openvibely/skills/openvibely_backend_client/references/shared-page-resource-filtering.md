# Shared-Page Resource Filtering

Use this guidance when one backend HTML/HTMX page contains multiple resource families but a client command must expose only one family.

## Procedure

1. Inspect the authoritative backend template and copy its actual DOM relationships into a regression fixture. Do not infer an enclosing `section`, card wrapper, or sibling boundary from a synthetic fixture.
2. Inventory all residue belonging to the excluded resource, not only its cards. Check page menus, headings, controls, pagination status and retry text, mixed empty-state copy, dialogs/modals, hidden data nodes, endpoint/path text, and scripts whose text extraction may become visible.
3. Prefer positive selection of structured desired cards when the page provides stable semantic markers. For a messaging-only channels view, the presence of `#webhook-card-list` identifies modern mixed markup; render only non-webhook `[data-channel-type]` cards rather than deleting an assumed webhook ancestor from whole-page prose.
4. Use the narrowest compatibility fallback only when the modern marker is absent. A legacy card-only fragment may remove `[data-webhook-id]` nodes while retaining its established surrounding prose behavior.
5. If modern markup contains no desired cards, return a client-owned resource-specific empty state rather than backend copy that mentions both included and excluded resource families.
6. Keep pagination ownership explicit. A filtered first-page command must not accidentally follow continuation metadata belonging to the excluded resource.

## Regression Checklist

- The fixture matches current backend nesting, including the excluded list directly under the shared container when authoritative markup does so.
- Multiple desired resource cards remain visible.
- Excluded add-menu entries, cards, endpoint text, pagination/loading/retry text, mixed empty-state text, modal labels/buttons, and hidden data are all absent.
- The command performs only the expected request count.
- Legacy fallback behavior and generic prose fallback remain covered separately.
- Run focused parser tests, then `go build ./...`, `go vet ./...`, `go test ./... -count=1`, `gofmt -l`, and `git diff --check`. Parser/filtering changes do not require the race detector unless concurrent loading or shared state also changes.
