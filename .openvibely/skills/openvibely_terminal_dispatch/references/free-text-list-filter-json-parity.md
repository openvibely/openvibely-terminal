# Free-Text List Filtering And JSON Parity

Use this checklist when a terminal list command combines backend state predicates with a locally evaluated free-text filter and supports both plain and `--json` output.

## Implementation

- Keep free-text operands local when the backend contract only supports workflow/state predicates; do not send the search term as an invented backend query parameter.
- Apply one shared selection predicate to the backend result before both plain rendering and JSON marshaling. Do not let the plain branch filter while JSON serializes the unfiltered backend slice.
- Preserve the existing `filterMatch` semantics and searchable fields, including title, searchable text, message, and stable ID, with the same case-sensitivity behavior in both modes.
- Keep retrieval, project scoping, backend workflow predicates, pagination, first-seen ordering, and result ordering unchanged. A local presentation filter must not alter request construction or pagination behavior.
- Preserve the established no-filter contract exactly. When a text filter is present, return a non-nil filtered slice so a no-match JSON result marshals as `[]`, not `null`; do not normalize an unfiltered nil backend slice unless that is already the command contract.

## Regression Coverage

- Add focused cases for matches through title, searchable text, message, and ID, plus a no-match search whose JSON decodes to a non-nil empty array.
- Run the same text filter through plain and JSON modes and compare the selected stable IDs, while retaining an assertion for human-readable output.
- Combine free text with each relevant decision/processing state predicate and assert both local selection and exact backend request predicates.
- Assert preserved backend order, pagination, project scope, and unfiltered JSON behavior.

## Validation

After focused regressions, run `gofmt -l`, `go build ./...`, `go test ./... -count=1`, `go vet ./...`, and `git diff --check`; resolve any formatter or whitespace output before completion.
