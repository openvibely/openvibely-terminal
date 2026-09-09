# CLI JSON Transcript Normalization Audit

Use this reference when auditing or implementing headless CLI output in `internal/terminal/cli.go`, especially when explicit-project and implicit-project JSON paths have separate renderers.

- Compare the complete preparation of `m.log[start:]` entries in every JSON branch, not only the final serialization. If branches apply the same role exclusions, trailing-newline cleanup, and empty-entry filtering, treat that as one duplicated transcript-normalization responsibility even when their JSON envelopes or keys intentionally differ.
- Propose the smallest private helper that returns the normalized transcript entries, while leaving project selection, output mode selection, and each JSON envelope/serialization format in its existing branch. Do not merge paths merely because they both emit JSON.
- Verify the helper against start-offset behavior, excluded roles, blank/empty entries, trailing newline variants, and non-empty ordering. Preserve the already-shared plain-text writer and avoid changing unrelated output semantics.
- Add focused regression coverage for both explicit-project and implicit-project JSON modes, asserting identical normalized entries and unchanged mode-specific envelopes. After an approved refactor, format changed Go files and run `go build ./...`, `go vet ./...`, and `go test ./...`.
