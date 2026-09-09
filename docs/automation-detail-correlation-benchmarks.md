# Automation Detail Correlation Benchmarks

Command:

```sh
go test ./internal/client -run '^$' -bench '^BenchmarkParseAutomationDetailSparseCorrelation$' -benchmem -count=10
```

Fixture: `automationDetailSparseCorrelationFixture` in
`internal/client/automation_detail_benchmark_test.go` (SHA-256
`f972e9a5f34886992da35fd5c5f818eb23fc073748a0addf34635fa76e6b07e9`). Every record contains a
unique graph node/detail-node ID and key plus a unique graph edge/detail-edge
ID, key, and sparse endpoint pair. The full HTML parser runs for 10, 100, and
500 node/edge record pairs.

Environment: `go version go1.26.4 darwin/arm64`, Apple M5 Pro.

Baseline parser revision: `0de87ecffa41a4f806a370ee061f74f6aa59a335`
(`Rank project edit separator candidates canonically`). The baseline was
measured with the benchmark fixture added but before the correlation-index
implementation. Optimized measurements are from this worktree after the
indexed parser changes.

| Records | Baseline median ns/op | Optimized median ns/op | Time change | Baseline median B/op | Optimized median B/op | B/op change |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 10 | 205,804 | 198,853 | -3.4% | 155,420 | 169,348 | +9.0% |
| 100 | 3,569,546 | 1,885,642 | -47.2% | 1,922,568 | 1,573,910 | -18.1% |
| 500 | 49,907,707 | 10,409,537 | -79.1% | 19,065,338 | 8,124,558 | -57.4% |

The optimized unique sparse 100-to-500 time scaling is `5.52x`. At 500
records, both the required median time reduction of at least 50% and allocated
bytes reduction of at least 30% are met. The 10-record time and allocation
changes remain within the allowed 10% regression cap.
