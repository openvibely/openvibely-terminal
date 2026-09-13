# JSON RawMessage Optimization And Benchmarking

Use this reference for performance work in `internal/terminal/cli.go` where recognized JSON event data is parsed for routing or scope validation and then emitted in a JSON envelope.

## Implementation

- Keep parse, project/task ownership, explicit-versus-omitted project semantics, and filtering decisions unchanged before optimizing presentation. Do not alter plain recognized output or raw fallback behavior.
- Start with `json.RawMessage` for validated payload bytes so the envelope marshal performs compaction and JSON escaping without an intermediate decoded payload. In live streams, prefer a byte-returning formatter so a large final string is not copied merely to write it to the output sink.
- If a large-event fast path avoids reflective decoding or a second payload scan, make it a validated optimization rather than a new semantic parser: use the standard library as the output oracle, decode only fields needed for filtering, and retain the existing `json.Unmarshal` path for malformed, unsupported, or uncertain input. Apply the fast path only above a measured size threshold when lexer overhead harms small events.
- Do not reuse raw JSON for known envelope strings merely because the token is valid. Retain the raw spelling only when it is equivalent to the `encoding/json` canonical spelling; noncanonical Unicode escapes, invalid UTF-8, type mismatches, duplicate/case-folded fields, and other uncertain cases must use the standard fallback while preserving prior decoded values and defaults. For large known strings such as chat/task message fields, validated raw values can avoid materializing a second large Go string.
- For payload data, append directly only when its bytes are already compact-safe for the required envelope semantics. Otherwise use the standard compaction/HTML-escaping behavior. Preserve escaping of `\\u003c`, `\\u003e`, `\\u0026`, U+2028, and U+2029, raw arrays/strings/numbers/null, whitespace compaction, and stable envelope field order.
- Preserve malformed-event semantics: emit the metadata record but omit `data`; continue filtering foreign-project and unscoped task events, while retaining compatible taskless-chat project synthesis.

## Regression Coverage

- Assert byte-for-byte or decoded-equivalent JSON output for field order, synthesized `project_id`, compact whitespace, malformed metadata-only output, escaping, UTF-8, raw top-level values, known-field type mismatches, duplicate/case-insensitive fields, nested unknown values, and malformed fallback.
- Add production-shaped large `chat_new_message` and task payloads, including escaped and UTF-8 content, and compare the optimized formatter with a standard `encoding/json` oracle.
- Exercise selected, foreign, missing, explicit, and omitted ownership cases, plus plain-mode parity, cancellation, and burst ordering/output. Assert filtering is unchanged and no stale or out-of-order event is emitted.

## Benchmark And Validation

- Add a benchmark companion beside the existing plain recognized-event benchmark using identical recognized and unknown fixtures at 1 KiB, 64 KiB, and 1 MiB, plus production-shaped chat/task payloads. Use at least five repetitions with `-benchmem`; report median `ns/op`, `B/op`, and `allocs/op` for each case.
- For credible attribution, temporarily restore only the old formatter for the baseline, collect the exact same matrix, restore the optimized source, and verify the final worktree before validation. Check both large-size reductions and the smallest-size regression guard rather than assuming every size improves.
- Finish with `gofmt -d` or repository formatting checks, focused regressions, targeted `-race` coverage for live-stream ordering/cancellation, `go build ./...`, `go vet ./...`, `go test ./... -count=1`, `git diff --check`, and final worktree/status checks. Report the exact benchmark command and measured before/after medians.
