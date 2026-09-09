# Review Comment Multiline Text Contract

Use this guidance when changing task review-comment parsing or rendering.

- Keep review extraction separate from generic `NodeText`; generic text extraction intentionally collapses text-node whitespace and must retain its existing behavior.
- Walk review-comment HTML safely and preserve text-node newlines and blank lines. Decode entities through the HTML parser, emit exactly one newline for each `<br>`, skip non-visible elements, and trim only the outer representation.
- Preserve empty and single-line comments plus all path, line, and other review metadata.
- Treat terminal rendering separately from parsing. Only the review-comment body may preserve newlines and blank lines. Render task titles, file paths, IDs, status/state, line type, and author as single-line terminal-safe metadata: strip ANSI, replace newline/carriage-return/tab separators with spaces, and remove remaining control/format characters. Apply this rule to both the add confirmation and the review table/header so repeated metadata cannot inject lines later in the same output.
- Exercise the shared parser through both review list (`GET`) and add (`POST`) responses.
- Add exact CLI JSON assertions and readable plain-output assertions for both command paths. Include entities, source newlines, blank lines, escaped `<br>`, empty comments, and metadata. For plain output, inject ANSI, bell/control characters, and embedded newlines into backend titles and user-supplied paths; inspect raw output before ANSI stripping to prove controls and `\ninjected`-style metadata line breaks are absent, while body newlines remain present. Add an explicit regression proving generic `NodeText` remains unchanged.
- Remember that Go JSON escaping may encode characters such as `&` as `\u0026`; derive exact expected output from the established encoder contract rather than hand-normalizing it.

Validate focused parser and CLI tests first, then run `go build ./...`, `go vet ./...`, `go test ./... -count=1`, `gofmt -l`, and `git diff --check`. Multiline parsing alone does not require the race detector; add focused `-race` coverage only if concurrent request or model behavior changes.
