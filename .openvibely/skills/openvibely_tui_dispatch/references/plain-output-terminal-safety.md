# Plain Output Terminal Safety

Use this guidance when changing plain-text CLI or TUI confirmations, status lines, errors, headings, tables, or resource-reference matching errors that interpolate backend-controlled or user-supplied strings.

## Implementation

- Treat every dynamic interpolation site as a terminal-safety boundary, including short success confirmations that precede an already-sanitized renderer and errors produced by shared reference matchers.
- Sanitize each untrusted field before `fmt.Sprintf`, concatenation, style rendering, or insertion into an error. Do not assume a later table/detail renderer or transcript renderer sanitizes an earlier confirmation or error line.
- For matching errors, preserve raw values for comparison and resolution. Sanitize only the values interpolated into ambiguous/not-found diagnostics so terminal safety does not change exact, prefix, substring, or ambiguity semantics.
- When a generic matcher is shared by output contexts with different safety needs, add a narrow display/presentation callback rather than normalizing the candidate collection or user reference before matching.
- Reuse the package terminal-text sanitizer when its semantics fit. Preserve ordinary readable text while stripping ANSI and OSC escape sequences and replacing or removing newline, carriage return, tab, C0, and other non-printing controls so a nominally single-line diagnostic remains single-line.
- When sanitizing an error returned from a backend read, preserve typed authentication and transport behavior. Return known auth/transport errors unchanged, and sanitize only ordinary completed-request or parser errors at the terminal-facing command boundary. Avoid generic `%v` rewrapping that makes `IsAuthRequired`, transport classification, login guidance, retry logic, or offline handling stop working.
- Keep structured JSON output unchanged when valid JSON escaping already prevents raw terminal control emission. Apply plain-rendering sanitization only at the presentation boundary unless the underlying data contract requires normalization.

## Regression Tests

- Inject ANSI, OSC, newline, carriage return, tab, and representative C0 controls into backend-controlled names and user-supplied references involved in the vulnerable output path.
- Inspect `out.String()`, the raw transcript, or the raw returned CLI error first. Assert it contains neither injected escape sequences nor raw control bytes and that a single-line diagnostic cannot create extra terminal lines.
- Exercise both interactive and headless command paths. A safe transcript renderer does not prove a raw one-shot error is safe, and a safe CLI wrapper does not prove an interactive result message is safe.
- Add separate classification checks proving auth-required and transport errors retain their typed predicates after passing through the terminal-safety helper. Also cover ordinary HTTP/backend-body and parser errors to prove those are sanitized.
- Do not sanitize fixtures before matching and do not call `stripANSI` before the safety assertions. Either technique can normalize away the defect and produce a false-positive regression test.
- Only after raw safety assertions, call `stripANSI` for ordinary readable-content and formatting assertions.
- Assert sanitized readable values remain present, so a fix that drops the entire confirmation, error, or field does not pass.
- For matching errors, cover both ambiguous backend names and unmatched hostile user references while asserting ordinary canonical, exact, prefix, and substring matching behavior remains unchanged.
- When practical, demonstrate the focused test fails before the fix, then run the focused test and the repository validation sequence after the fix.

## Validation

Run:

```bash
gofmt -w <changed-go-files>
go test ./internal/tui -run '<focused-test>' -count=1
go build ./...
go vet ./...
go test ./... -count=1
git diff --check
```

When the fix adds asynchronous model messages, deferred confirmation resolution, or other concurrency-sensitive paths, run the narrowest relevant package or tests under `-race`; expand to `go test -race ./...` only when behavior crosses package boundaries or the task explicitly requires repository-wide evidence.
