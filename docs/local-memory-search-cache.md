# Local memory search cache

Local fallback searches retain query-specific scanner output (matches, metadata,
snippets, and warnings) on the client. The key includes the project ID, absolute
memory root, index entry, and normalized query. The index is reread on every search
so additions, removals, ordering, duplicate warnings, and metadata edits remain
visible. Previously unseen queries and invalidated entries use the full scanner.
Backend search behavior is unchanged.

Each lookup uses the existing safe-open path, including permission, symlink,
regular-file, and descriptor identity checks. Content reuse requires matching
device, inode, size, mode, modification time, and inode change time. Entries are
only stored when before/after fingerprints agree and both timestamps are older
than two seconds, avoiding reuse within a coarse filesystem timestamp tick.
Platforms without the required change-time information continue scanning.
Errors and canceled scans are never cached.

Retention is bounded by a 32 MiB accounting budget and 16,384 entries per client.
Keys and values own their strings so substrings cannot retain entire index files.
The cache clears when either limit is reached. This favors a small working set of
repeated queries; novel queries, recently modified files, and eviction can still
require full content reads. Safe file opens are deliberately preserved on hits.

## Reproduction

The generated benchmark covers 100 and 1,000 notes of 64 KiB and 1 MiB, with
negative, Unicode body, and metadata queries. Cold queries use a new client.
Warm benchmarks prime three queries outside the timer, then measure
30 repeat queries per iteration. Therefore warm `B/op` and `allocs/op` cover 30 queries; divide
by 30 for per-query allocations. `read-B/query` counts note-content bytes consumed
by the scanner, excluding the small index file. `opened/query` counts note files.

```sh
MEMORY_SEARCH_BASELINE=1 go test ./internal/client -run '^$' -bench 'BenchmarkSearchMemoriesCorpus/.*/warm' -benchtime=1x -count=1
go test ./internal/client -run '^$' -bench BenchmarkSearchMemoriesCorpus -benchtime=1x -count=1
MEMORY_SEARCH_BASELINE=1 go test ./internal/client -run '^$' -bench 'BenchmarkSearchMemoriesCorpus/100x64KiB/cold' -benchtime=30x -count=1
go test ./internal/client -run '^$' -bench 'BenchmarkSearchMemoriesCorpus/100x64KiB/cold' -benchtime=30x -count=1
```

`MEMORY_SEARCH_BASELINE=1` selects the uncached local scanner in benchmark code
only. It is not a production setting. The baseline uses the same IO counters as
the cache-enabled run.

## Measurement environment and cold-search acceptance

Measured on Darwin/arm64, Apple M5 Pro, Go 1.27.1. Other work was running on
this host, so these are practical observations rather than isolated laboratory
measurements. Five alternating baseline/cache runs, each with 30 cold queries on
100 × 64 KiB notes, produced the following per-run p95 values (milliseconds):

| Run | Full scan | Cache enabled, cold |
| --- | ---: | ---: |
| 1 | 80.91 | 76.38 |
| 2 | 78.36 | 83.75 |
| 3 | 75.77 | 75.76 |
| 4 | 104.61 | 77.27 |
| 5 | 75.34 | 76.54 |
| Median | 78.36 | 76.54 |

The median cold p95 improved 2.3%, meeting the within-10% requirement. An initial
single paired run measured 94.55 → 105.14 ms (+11.2%); the repeated alternating
samples above were collected to resolve that noisy result. Cold content reads
remain 6,553,600 bytes and 100 note opens per query. Approximate cold allocations
are 82.62 → 82.71 MB and 1,289,161 → 1,290,172 allocations per query.

## Validation

- `go build ./...`: passed.
- `go test ./... -timeout=90s`: client and command packages passed; terminal
  package timed out in `TestRunCLIEventsJSONPreservesScopeAndBurstOrder`.
- Retrying the suite with that test excluded exposed a second timeout in
  `TestCLIEventsOnStreamsSelectedProjectAndWritesLiveLines`.
- Both terminal timeouts reproduced individually with the tracked client and
  memory implementation restored to `HEAD` through a Go build overlay. They are
  pre-existing failures, not changes introduced by this cache.
- `go test ./internal/terminal -run 'Memory' -timeout=60s`: passed.
- `go test -race ./internal/client -run 'TestMemorySearchCache' -count=1`: passed.
- `GOOS=linux GOARCH=amd64 go test -c ./internal/client`: compiled successfully.

Regression coverage compares cached searches with uncached results through
same-size in-place rewrites with restored mtime (both inside and outside the
settling window), inode replacements, permission/metadata changes, index metadata
edits, additions, deletion/rename, duplicate entries and matches, oversized files,
symlink escapes, and project changes. It also checks mid-scan cancellation,
warning preservation, caller mutation, concurrent cache hits, cache limits, and
nil-client fallback behavior.

## Warm-search measurements

All values are per query, with 30 measured warm queries per corpus.

| Corpus | Full-scan p95 (ms) | Cached p95 (ms) | Content bytes, before → after | Note opens, before → after |
| --- | ---: | ---: | ---: | ---: |
| 100x64KiB | 97.78 | 9.01 | 6,553,600 → 0 | 100 → 100 |
| 100x1024KiB | 1,180.28 | 13.92 | 104,857,600 → 0 | 100 → 100 |
| 1000x64KiB | 890.67 | 188.44 | 65,536,000 → 0 | 1,000 → 1,000 |
| 1000x1024KiB | 10,618.91 | 105.37 | 1,048,576,000 → 0 | 1,000 → 1,000 |

| Corpus | Allocated MB/query, before → after | Allocations/query, before → after |
| --- | ---: | ---: |
| 100x64KiB | 82.63 → 3.02 | 1,289,170 → 29,650 |
| 100x1024KiB | 1,174.20 → 3.04 | 20,090,988 → 29,650 |
| 1000x64KiB | 825.85 → 29.94 | 12,886,604 → 291,571 |
| 1000x1024KiB | 11,741.65 → 30.10 | 200,904,838 → 291,572 |

For 1,000 × 1 MiB notes, warm p95 decreases 99.01% and note-content reads decrease 100%, exceeding the required 50% and 90% reductions.

Raw output, including all cold samples and allocation metrics, is recorded in
[local-memory-search-benchmarks.txt](local-memory-search-benchmarks.txt).
