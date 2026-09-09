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
`5a42c533a341be1b8d85763b07d2051e756bdf5a` (`Preserve stateful automation
edge correlation`). The fixture hash was verified before each run.

## Raw Results

Each list preserves the ten samples in Go benchmark output order. `ns/op` and
`B/op` are the acceptance metrics; `allocs/op` was constant within each record
size (`2,773` baseline versus `2,958` optimized at 10; `44,675–44,676` versus
`27,848–27,849` at 100; `622,973–622,975` versus `138,548–138,551` at 500).

| Records | Parser | ns/op samples | B/op samples |
| --- | --- | --- | --- |
| 10 | Baseline | 245994, 208948, 230488, 234107, 219466, 233525, 228508, 233743, 227367, 226520 | 155113, 155349, 155512, 155422, 155538, 155514, 155330, 155469, 155563, 155569 |
| 10 | Optimized | 203633, 219024, 221829, 222302, 219138, 251549, 237463, 230445, 220606, 198247 | 169386, 169704, 169612, 169740, 169806, 169530, 169733, 169912, 169865, 169796 |
| 100 | Baseline | 3772520, 3696559, 3695287, 3741689, 3548365, 3632294, 3698001, 3475065, 3630164, 3566769 | 1920605, 1921831, 1924560, 1923301, 1922576, 1923504, 1920454, 1922396, 1920162, 1922221 |
| 100 | Optimized | 2008662, 1976456, 1791532, 2430251, 2976453, 2818101, 2876053, 2110876, 1857419, 2064797 | 1579393, 1578906, 1577444, 1577969, 1576711, 1575366, 1577674, 1578494, 1578176, 1577867 |
| 500 | Baseline | 49745311, 49985652, 49150008, 47894502, 48324102, 49004276, 48640988, 48230953, 48560158, 49336510 | 19070442, 19070353, 19076137, 19070400, 19070035, 19074210, 19084799, 19071130, 19066130, 19069285 |
| 500 | Optimized | 10520033, 10713772, 10914902, 11140133, 10153544, 10160538, 10798479, 11221998, 11604325, 11747571 | 8143437, 8145758, 8161115, 8148276, 8146401, 8146876, 8153856, 8151680, 8145336, 8152021 |

Medians are the mean of sorted samples five and six, computed independently
for `ns/op` and `B/op`. For example, the following standard-library-only
snippet derives each median from the raw lists above:

```python
from statistics import median

assert median([49745311, 49985652, 49150008, 47894502, 48324102,
               49004276, 48640988, 48230953, 48560158, 49336510]) == 48822632
assert median([10520033, 10713772, 10914902, 11140133, 10153544,
               10160538, 10798479, 11221998, 11604325, 11747571]) == 10856690.5
```

| Records | Baseline median ns/op | Optimized median ns/op | Time change | Baseline median B/op | Optimized median B/op | B/op change |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 10 | 229,498.0 | 221,217.5 | -3.6% | 155,490.5 | 169,736.5 | +9.2% |
| 100 | 3,663,790.5 | 2,087,836.5 | -43.0% | 1,922,308.5 | 1,577,918.0 | -17.9% |
| 500 | 48,822,632.0 | 10,856,690.5 | -77.8% | 19,070,421.0 | 8,147,576.0 | -57.3% |

The optimized unique sparse 100-to-500 time scaling is `5.20x`. At 500
records, median time improves by `77.8%` and median allocated bytes improve by
`57.3%`, satisfying the required at-least-50% and at-least-30% reductions.
At 10 records, time improves by `3.6%` and allocated bytes increase by `9.2%`,
within the maximum 10% regression allowance.
