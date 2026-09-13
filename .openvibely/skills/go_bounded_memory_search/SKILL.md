---
kind: openvibely.agent_skill
version: 3
skill:
    key: go_bounded_memory_search
    name: Bounded Go Memory Search
    scope: project
    description: Optimize indexed Markdown memory search in the Go TUI without changing search semantics or materializing document-sized temporary copies.
---

Use for performance or correctness changes to `SearchMemories` and its indexed-file search path.

1. Keep the full-body parser for operations that need a complete document (such as show/detail); add or preserve a search-specific reader instead of changing general parsing behavior.
2. Parse front matter before body matching, but do not commit parsed fields or malformed-line warnings until a closing delimiter is found. For an unterminated opening fence, treat the complete raw document as body text, retain indexed title/summary, and emit only the legacy unterminated-front-matter warning. Confirmed front matter never contributes a body match. Preserve the legacy parser's Unicode-aware `strings.ToLower` key normalization for supported fields (`name`, `title`, `summary`, and `description`); an ASCII-only byte comparison can silently change valid metadata behavior.
3. Search case-insensitively with byte-native Unicode-aware lowering. Lower the query once and scan bodies in bounded chunks (64 KiB is the established size); do not construct normalized full-body strings or copies. This applies to invalid UTF-8 too: repair/lower incrementally in a reusable bounded buffer, preserve a consecutive invalid-byte run as one replacement rune, and preserve matches across normalization and chunk boundaries.
4. Generate titles, summaries, match snippets, and first-paragraph fallbacks from bounded line/paragraph collectors. Preserve whitespace and the 220-rune snippet limit, including a hit at the end of an 8 MiB allowed file or on one very long line.
5. Retain `readMemoryTarget` as the safe file-read boundary: its pre-open resolution and post-stat validation guard against unsafe paths and races. Do not add redundant path traversals around that call.
6. Preserve result ordering, cancellation, warnings, empty results, metadata-only/body-only/both/neither query behavior, metadata output, 8 MiB limits, and Unicode case-fold semantics.

Tests and benchmarks:

- Cover confirmed and unterminated malformed front matter, including indexed metadata, warning-set, and body-search behavior when the closing fence is absent. Include supported front-matter keys whose Unicode case mappings differ from ASCII folding, and compare search metadata to the full-document parser.
- Cover unsafe/missing/oversized files, cancellation, end-of-8-MiB hits, long lines, and mixed-case Unicode (including Turkish case-fold behavior). Add an 8 MiB invalid-UTF-8 long-line allocation ceiling plus a normalized match spanning a 64 KiB boundary.
- Maintain end-to-end `SearchMemories` benchmarks with representative front matter, mixed case, Unicode, and long lines for 10, 100, and 500 valid 256 KiB documents, in no-match and final-document-match cases. Retain the 8 MiB helper benchmark.
- Add a 10-document 16 KiB guardrail. Compare repeated, like-for-like medians against a clean `HEAD` export with the same benchmark duration; do not infer percentage improvements from one-iteration samples.
- Validate with focused tests, `go build ./...`, `go vet ./...`, `go test ./... -count=1`, a diff check, and repeated benchmark measurements tied to the final commit.
