# Task selector activation performance

The unfiltered task picker lowercases and concatenates every row's label,
detail, and reference during selector activation. A lazy-index candidate was
measured against that behavior, but the optimization is not retained: this
checkout has no observed representative project catalog sizes, so the synthetic
measurements do not establish that the acceptance threshold applies to a size
users encounter. Production continues to build the search index during activation.

## Evidence harness

Run the controlled comparison with:

```bash
OPENVIBELY_SELECTOR_PERF_EVIDENCE=1 go test ./internal/terminal -run '^TestSelectorActivationPerformanceEvidence$' -count=1 -v
```

It generates task-reference catalogs with valid project IDs, UUID-shaped
36-character task IDs, 78–83 character titles, and supported category/status
values. It validates catalogs through `client.ListTaskReferences` and measures
100, 1,000, and 10,000 tasks. The 1,000- and 10,000-task catalogs are stress
cases: this checkout has no captured catalogs from representative live
projects, so their sizes are not presented as observed customer project sizes.
The 100-task case is the required ordinary-size comparison profile, not a live
project sample. The endpoint and client accept complete catalogs without a
configured task-count cap.

The test runs 8 warmups and 50 matched samples per implementation and size. It
alternates eager and lazy order. Isolated activation starts with decoded tasks
and constructed selector rows, and times the shared activation handler used by
production; its allocation counts and bytes exclude catalog request/JSON decoding. The end-to-end path
starts before selector command execution, requests the JSON catalog from a
controlled local HTTP server, decodes it, constructs task rows, activates and
renders the picker. The first-filter path continues through typing `review`
and rendering the filtered results. Both request paths make one catalog
request, and response bytes are recorded. Automatic GC is disabled during the
evidence run, with an explicit GC before each measured sample.

Machine and controls for the recorded run: Mac17,8, Darwin 27.0.0, Go 1.27.3,
arm64, 18 logical CPUs, GOMAXPROCS=18, width 120, filter `review`, 8 warmups,
50 samples per implementation and size. The local server adds no artificial
delay.

### Isolated activation

| Tasks | Mode | Median | p95 | Allocations/op | Bytes/op | Response bytes |
|---:|---|---:|---:|---:|---:|---:|
| 100 | Eager | 26.917 µs | 35.250 µs | 202 | 27,664 | 25,613 |
| 100 | Lazy | 4.625 µs | 11.292 µs | 1 | 3,072 | 25,613 |
| 1,000 | Eager | 187.334 µs | 202.417 µs | 2,002 | 247,456 | 256,047 |
| 1,000 | Lazy | 4.334 µs | 6.958 µs | 1 | 3,072 | 256,047 |
| 10,000 | Eager | 1.730 ms | 1.989 ms | 20,002 | 2,446,912 | 2,560,368 |
| 10,000 | Lazy | 4.542 µs | 6.291 µs | 1 | 3,072 | 2,560,368 |

For the generated 100-task profile, lazy candidate median activation was 82.8%
lower and allocated bytes were 88.9% lower; the count fell from 202 allocations
to 1. Those synthetic results meet the numeric thresholds, but do not satisfy
the observed-project-size requirement. The optimization therefore remains
rejected pending measurements from representative project catalogs.

### Request through visible picker and first filter

| Tasks | Mode | Picker median | Picker p95 | First-filter median | First-filter p95 |
|---:|---|---:|---:|---:|---:|
| 100 | Eager | 286.625 µs | 323.250 µs | 292.542 µs | 337.791 µs |
| 100 | Lazy | 267.833 µs | 297.458 µs | 291.333 µs | 334.875 µs |
| 1,000 | Eager | 1.699 ms | 1.751 ms | 1.723 ms | 1.790 ms |
| 1,000 | Lazy | 1.490 ms | 1.545 ms | 1.733 ms | 1.792 ms |
| 10,000 | Eager | 15.505 ms | 16.007 ms | 15.832 ms | 16.442 ms |
| 10,000 | Lazy | 13.750 ms | 14.127 ms | 15.987 ms | 16.480 ms |

On these generated profiles, first-filter p95 stayed within 0.3% of eager at
every measured size, within the 10% guard. This is supporting comparison data
only; it does not establish user-facing performance without observed
representative catalog sizes. The lazy candidate moves index work to the first
non-empty filter; subsequent typing uses the existing ordered match-index cache.

## Subsequent filter changes

`BenchmarkSelectorFilterChanges` remains a separate subsequent-filter measure.
It prepares the lowercase search index outside the timed loop, so its results do
not include selector activation indexing or first-index construction. Run it
independently with:

```bash
go test ./internal/terminal -run '^$' -bench '^BenchmarkSelectorFilterChanges$' -benchtime=100ms -benchmem -count=10
```

On the recorded machine, the ten-run median for high-match, low-match,
no-match, and character-by-character filtering was respectively 19.0, 17.2,
12.6, and 69.1 µs/op at 1,000 items, and 155.0, 148.4, 117.7, and 641.2
µs/op at 10,000 items. The single-filter profiles used 8,192 B/op and 1
alloc/op at 1,000 items and 81,920 B/op and 1 alloc/op at 10,000 items. The
character-by-character profiles used 8,352 B/op and 15 allocs/op at 1,000
items and 82,080 B/op and 15 allocs/op at 10,000 items. These remain separate
from the activation comparison and the first-filter request path.
