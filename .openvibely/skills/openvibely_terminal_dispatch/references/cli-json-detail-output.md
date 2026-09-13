# CLI JSON Detail Output

Use this guidance when a headless terminal command supports both human-readable and JSON output for a resolved resource detail.

## Implementation

- Keep reference resolution, scope selection, detail fetching, identity checks, authentication classification, and error behavior unchanged.
- Branch only after the complete structured resource has been resolved successfully: in JSON mode marshal the structured object directly; in plain mode retain the existing human-readable renderer.
- Serialize the complete detail object, including multiline content and explicit stable `snake_case` JSON tags. Do not route successful JSON detail output through styled or ANSI-producing view helpers, headers, or extra status text.
- Ensure every failure returns before marshaling a success payload. Unknown and ambiguous references, auth/transport errors, and detail identity mismatches must preserve their existing exit status and leave stdout free of misleading JSON.
- Preserve the existing JSON behavior of sibling list commands; a detail fix must not reduce list objects to summaries or alter their content contract.

## Regression Coverage

- Exercise exact handles, names, unique prefixes, and unique substrings, including both project and global scope resolution.
- Decode stdout as exactly one structured JSON object and assert complete multiline content, stable keys, and absence of ANSI styling or extra headers.
- Keep a plain-output regression proving the human-readable path remains non-JSON and retains its expected rendering.
- Cover unknown and ambiguous references, authentication or transport failures, and detail identity mismatches; assert the command fails without emitting a successful JSON object.
- Retain a JSON list regression proving list output remains a complete-content array.

## Validation

After the focused terminal/client regressions, run `gofmt` checks, `go build ./...`, `go vet ./...`, `go test ./... -count=1`, and `git diff --check`. Report the exact focused and repository-wide checks performed.
