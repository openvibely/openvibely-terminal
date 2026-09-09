# Memory Search Normalization Implementation Notes

Use these checks when implementing or validating indexed project-memory search optimizations after an approved performance finding.

- Normalize each parsed document body once and share that representation between match detection and snippet selection. Avoid re-lowercasing candidate lines after the full-body match.
- Do not use byte offsets from a normalized Unicode string to slice or index the original body. Go Unicode case conversion can change UTF-8 byte widths. Traverse original and normalized lines independently, use the normalized line only for matching, and return the original line bytes for the snippet.
- Keep normalization downstream of existing file-read, regular-file, path, replacement, index-policy, parsing, warning, cancellation, and per-file size checks so the optimization does not bypass safety or alter error behavior.
- Add exact-output regressions for mixed-case ASCII, Unicode casing with differing encoded widths, metadata-only matches, front matter, malformed metadata warnings, cancellation, missing or replaced files, no-match behavior, and the exact accepted/rejected size boundary.
- Benchmark no-match, first-line match, and final-line match separately on a maximum-size valid fixture, with filesystem setup outside the timed loop and allocation reporting enabled. Assert or otherwise sanity-check that each benchmark query really matches its intended marker; a spelling or diacritic mismatch can silently turn start/end cases into no-match measurements.
- Compare small and large fixtures. Confirm the implementation performs one body-sized normalization allocation and does not restore repeated per-line lowercase allocations, while preserving byte-exact snippets and result ordering.
- Finish with `go build ./...`, `go vet ./...`, and `go test ./... -count=1`, plus focused benchmark samples where practical.
