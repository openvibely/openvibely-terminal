# Automation Detail Correlation Benchmarks

## Reproduction

The benchmark exercises the complete HTML parser with `10`, `100`, and `500`
unique graph node/detail-node and graph edge/detail-edge pairs. Each pair has
unique IDs, keys, and sparse endpoints. The fixture is
`automationDetailSparseCorrelationFixture` in
`internal/client/automation_detail_benchmark_test.go`.

```sh
go test ./internal/client -run '^$' -bench '^BenchmarkParseAutomationDetailSparseCorrelation$' -benchmem -count=10
```

Fixture SHA-256:
`f972e9a5f34886992da35fd5c5f818eb23fc073748a0addf34635fa76e6b07e9`.

Environment for both runs: `go version go1.26.4 darwin/arm64`, Apple M5 Pro.

### Executable Baseline Provenance

The baseline parser is
`0de87ecffa41a4f806a370ee061f74f6aa59a335` (`Rank project edit separator
candidates canonically`). It predates the benchmark fixture, so this command
creates a detached baseline worktree and materializes the fixture verbatim
from the fixture-introduction commit
`0ca8d4be96a45a05f6ade8099138ce4f18803239` before running the command above:

```sh
baseline_dir=$(mktemp -d /tmp/automation-detail-baseline.XXXXXX)
git worktree add --detach "$baseline_dir" 0de87ecffa41a4f806a370ee061f74f6aa59a335
git show 0ca8d4be96a45a05f6ade8099138ce4f18803239:internal/client/automation_detail_benchmark_test.go \
  > "$baseline_dir/internal/client/automation_detail_benchmark_test.go"
(
  cd "$baseline_dir"
  shasum -a 256 internal/client/automation_detail_benchmark_test.go
  go test ./internal/client -run '^$' -bench '^BenchmarkParseAutomationDetailSparseCorrelation$' -benchmem -count=10
)
git worktree remove --force "$baseline_dir"
```

The optimized parser revision is
`62ed1532c50f3235624d81a294190b29382dcb23` (`Retain ASCII correlation
key fast path`). The fixture hash was verified before each run.

## Raw Results

Each list preserves the ten samples in Go benchmark output order. `ns/op` and
`B/op` are the acceptance metrics; `allocs/op` was constant within each record
size (`2,773` baseline versus `2,958–2,959` optimized at 10; `44,675–44,676` versus
`27,848` at 100; `622,973–622,975` versus `138,547–138,549` at 500).

| Records | Parser | ns/op samples | B/op samples |
| --- | --- | --- | --- |
| 10 | Baseline | 245994, 208948, 230488, 234107, 219466, 233525, 228508, 233743, 227367, 226520 | 155113, 155349, 155512, 155422, 155538, 155514, 155330, 155469, 155563, 155569 |
| 10 | Optimized | 194116, 199873, 229413, 189603, 191844, 197667, 191098, 189822, 191780, 192378 | 169423, 169579, 170040, 169529, 169487, 169935, 169517, 169464, 169742, 169806 |
| 100 | Baseline | 3772520, 3696559, 3695287, 3741689, 3548365, 3632294, 3698001, 3475065, 3630164, 3566769 | 1920605, 1921831, 1924560, 1923301, 1922576, 1923504, 1920454, 1922396, 1920162, 1922221 |
| 100 | Optimized | 1880945, 1953935, 1868717, 1807868, 1799102, 1826017, 1800452, 2069552, 1924504, 2031465 | 1578788, 1579259, 1579022, 1577908, 1578107, 1578749, 1578274, 1578453, 1578847, 1579380 |
| 500 | Baseline | 49745311, 49985652, 49150008, 47894502, 48324102, 49004276, 48640988, 48230953, 48560158, 49336510 | 19070442, 19070353, 19076137, 19070400, 19070035, 19074210, 19084799, 19071130, 19066130, 19069285 |
| 500 | Optimized | 10329304, 10280072, 11038108, 12449687, 12093042, 12320054, 12295348, 11302977, 12057918, 10916382 | 8147553, 8145316, 8149715, 8143035, 8138104, 8141511, 8143291, 8147656, 8139668, 8146667 |

Medians are the mean of sorted samples five and six, computed independently
for `ns/op` and `B/op`. For example, the following standard-library-only
snippet derives each median from the raw lists above:

```python
from statistics import median

assert median([49745311, 49985652, 49150008, 47894502, 48324102,
               49004276, 48640988, 48230953, 48560158, 49336510]) == 48822632
assert median([10329304, 10280072, 11038108, 12449687, 12093042,
               12320054, 12295348, 11302977, 12057918, 10916382]) == 11680447.5
```

| Records | Baseline median ns/op | Optimized median ns/op | Time change | Baseline median B/op | Optimized median B/op | B/op change |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 10 | 229,498.0 | 192,111.0 | -16.3% | 155,490.5 | 169,554.0 | +9.0% |
| 100 | 3,663,790.5 | 1,874,831.0 | -48.8% | 1,922,308.5 | 1,578,768.5 | -17.9% |
| 500 | 48,822,632.0 | 11,680,447.5 | -76.1% | 19,070,421.0 | 8,144,303.5 | -57.3% |

The optimized unique sparse 100-to-500 time scaling is `6.23x`. At 500
records, median time improves by `76.1%` and median allocated bytes improve by
`57.3%`, satisfying the required at-least-50% and at-least-30% reductions.
At 10 records, time improves by `16.3%` and allocated bytes increase by `9.0%`,
within the maximum 10% regression allowance.
