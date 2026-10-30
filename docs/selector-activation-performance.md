# Task selector activation performance

The unfiltered task picker used to lowercase and concatenate every row's label,
detail, and reference while handling the selector activation message. The picker
now keeps the original ordered items and creates search text when filtering
starts. Initial filters still build the index during activation.

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
and constructed selector rows, and times `handleSelector`; its allocation
counts and bytes exclude catalog request/JSON decoding. The end-to-end path
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
| 100 | Eager | 43.666 µs | 74.083 µs | 202 | 27,664 | 25,613 |
| 100 | Lazy | 15.000 µs | 33.959 µs | 1 | 3,072 | 25,613 |
| 1,000 | Eager | 223.583 µs | 296.708 µs | 2,002 | 247,456 | 256,047 |
| 1,000 | Lazy | 11.292 µs | 14.959 µs | 1 | 3,072 | 256,047 |
| 10,000 | Eager | 2.052 ms | 2.341 ms | 20,002 | 2,446,912 | 2,560,368 |
| 10,000 | Lazy | 12.375 µs | 16.958 µs | 1 | 3,072 | 2,560,368 |

At 100 tasks, median activation was 65.6% lower and allocated bytes were 88.9%
lower. The count fell from 202 allocations to 1. This passes the 30% latency
and 40% allocation criteria at the ordinary-size profile.

### Request through visible picker and first filter

| Tasks | Mode | Picker median | Picker p95 | First-filter median | First-filter p95 |
|---:|---|---:|---:|---:|---:|
| 100 | Eager | 413.667 µs | 507.375 µs | 427.708 µs | 557.042 µs |
| 100 | Lazy | 389.708 µs | 500.625 µs | 409.541 µs | 533.625 µs |
| 1,000 | Eager | 2.241 ms | 3.022 ms | 2.323 ms | 3.390 ms |
| 1,000 | Lazy | 2.044 ms | 2.814 ms | 2.298 ms | 3.011 ms |
| 10,000 | Eager | 19.904 ms | 21.694 ms | 20.377 ms | 22.115 ms |
| 10,000 | Lazy | 17.577 ms | 18.998 ms | 20.317 ms | 21.684 ms |

First-filter p95 did not regress at any measured size. The index work moves to
the first non-empty filter; subsequent typing continues to use the existing
ordered match-index cache.

## Subsequent filter changes

`BenchmarkSelectorFilterChanges` remains a separate subsequent-filter measure.
It prepares the lowercase search index outside the timed loop, so its results do
not include selector activation or deferred first-index construction. Run it
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
