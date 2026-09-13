# Selector Filter Performance

Use this reference when optimizing inline selector filtering in `internal/terminal/selector.go` without changing selection behavior.

## Correctness Invariants

- Keep the complete ordered set of matching items. Never cap the filter result to the visible viewport; rendering may show eight rows while cursor movement, total counts, and Enter selection must reach later matches.
- Preserve case-insensitive matching across label, detail, and reference fields, exact item order, zero/one/many match behavior, initial filters, force-picker behavior, direct dispatch, and resolved-task identity.
- Prefer a compact ordered index of original items over copying every matching `selectorItem`. Resolve the original item lazily for rendering, cursor movement, and selection, and keep the original item store authoritative.
- Invalidate or rebuild the index whenever selector items are replaced. A narrowing filter may reuse the previous index backing array, but widening, unrelated filters, and item replacement must not retain obsolete matches or stale folded-filter state.
- Cache normalized filter strings used to detect character-by-character narrowing. Repeatedly lowercasing both the old and new filter can reintroduce avoidable allocations even when the index itself is compact.

## Regression Coverage

Add focused tests for zero, one, and many matches; match order and total count; a later-than-visible-row cursor move followed by Enter; initial filters; item replacement; cache invalidation; repeated filter changes; and pointer/identity-sensitive resolved-task dispatch. Assert that repeated changes do not retain obsolete slices or cause unbounded memory growth. Keep existing cached-render benchmark coverage intact.

## Benchmark Protocol

Add a focused filter-change benchmark using identical 1,000-item and 10,000-item fixtures with high-match, low-match, no-match, and realistic character-by-character prefix cases. Run the exact baseline and candidate command with repeated samples and memory metrics:

```text
go test ./internal/terminal -run '^$' -bench '^BenchmarkSelectorFilterChanges$' -benchmem -count=10
```

Record exact commands and all relevant medians for `ns/op`, `B/op`, and `allocs/op`. Compare the 10,000-item high-match character-by-character case against the baseline, and separately verify that ordinary 100-item and already-cached rendering do not regress beyond the stated threshold. Do not claim an optimization from a single sample or from a benchmark that no longer exercises the full filter-change loop.

Run the focused selector tests and preserve the repository gates: `go build ./...`, `go test ./... -count=1`, `go vet ./...`, repository `gofmt` checks, and `git diff --check`.

## Common Pitfalls

- Replacing the full match slice with only the first visible rows breaks later cursor selection and total-match counts.
- Resolving every displayed row through a new abstraction can regress ordinary cached rendering; specialize the empty/all-match path or measure the hot path before accepting the change.
- A benchmark that only renders an already-filtered selector does not measure filter-change allocation savings; exercise filter updates for every required profile.
- Report the baseline and candidate from the same fixture, command, platform, and sample count so percentage claims are reproducible.
