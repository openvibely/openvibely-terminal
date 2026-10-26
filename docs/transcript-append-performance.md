# Transcript append performance evidence

The transcript append benchmark uses a fixed 100 column viewport and agent bodies made from repeated ASCII `x` bytes. Each benchmark operation appends the full burst into an initially empty, refreshed transcript. `ns/op` is full burst latency, `B/op` is allocated bytes for that burst, and `p95-ns/append` is the p95 across its individual appends.

## Repeated burst results

Measured on Go `go1.27.3 darwin/arm64`, Apple M5 Pro. Before and after used the same command and checkout environment; each row is the median of five independent runs:

```sh
go test ./internal/terminal -run '^$' -bench '^BenchmarkTranscriptCumulativeAppend$' -benchmem -benchtime=1x -count=5
```

| Entries | Body | Median allocated bytes before → after | Allocation reduction | Median full burst before → after | Median append p95 before → after | p95 reduction |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 100 | 1 KiB | 1,530,392 → 255,792 | 83.3% | 4.641 → 1.124 ms | 52.459 → 13.583 µs | 74.1% |
| 100 | 16 KiB | 31,816,200 → 3,386,352 | 89.4% | 55.372 → 14.483 ms | 678.708 → 158.500 µs | 76.6% |
| 500 | 1 KiB | 7,388,992 → 1,229,488 | 83.4% | 19.250 → 5.334 ms | 40.709 → 12.166 µs | 70.1% |
| 500 | 16 KiB | 158,732,096 → 17,973,728 | 88.7% | 283.862 → 70.845 ms | 702.083 → 179.583 µs | 74.4% |

The 16 KiB cases exceed the requested allocation and p95 improvements. The 1 KiB cases also improve both measurements.

## Phase measurements

The opt-in phase evidence runs seven repeats and reports median allocated bytes and full-phase time for rendering, rendered-line processing, and cache insertion. The 500 entry, 16 KiB case measured before and after the renderer change:

| Phase | Median allocated bytes before → after | Median phase time before → after |
| --- | ---: | ---: |
| Render | 151,365,936 → 9,280,576 | 294.452 → 15.077 ms |
| Line processing | 1,536,000 → 1,536,000 | 61.152 → 61.848 ms |
| Cache insertion | 7,092,544 → 7,092,544 | 2.095 → 2.630 ms |

This isolates the renderer as the large allocation and latency source for these bodies. The change specializes printable ASCII text without spaces or hyphens; Lipgloss remains responsible for other text. No peak-heap or retained-memory improvement is claimed.

Run the repeat benchmark with the command above. Run the phase evidence with:

```sh
OPENVIBELY_PERF_EVIDENCE=1 go test ./internal/terminal -run '^TestTranscriptAppendPerformanceEvidence$' -count=1 -v
```
