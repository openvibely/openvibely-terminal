---
kind: openvibely.agent_skill
version: 10
skill:
    key: openvibely_automation_parser
    name: OpenVibely Automation Parser Refactoring
    scope: project
    description: Preserve graph/detail automation parsing semantics while consolidating extraction or optimizing large-automation parsing.
---

# OpenVibely Automation Parser Refactoring

Use this skill when changing automation graph-node or detail-node HTML parsing in `openvibely-tui`, especially when consolidating duplicated extraction, adding config summaries, or optimizing large-automation parsing.

## Safe Refactor Boundary

- Establish behavior with focused regressions before changing extraction or correlation code.
- A private common helper may extract configurable ID, key, and state attributes plus shared name, type, role, and count fields.
- Keep representation-specific fallbacks in their wrappers and in their existing order. Graph-only behavior includes `strong`/CSS-state/state-text/first-line-name fallbacks and parent task-link counts; detail-only behavior includes heading, metadata-paragraph, and badge fallbacks.
- Preserve count precedence and merging, record correlation, duplicate handling, unmatched-record provenance, availability, warnings, rendering, and JSON shape.
- Do not infer expected canonical state strings directly from visible text. In the established parser, visible `Waiting` normalizes to `waiting_human`; lock existing normalization into tests rather than changing it during an extraction-only refactor.

## Configuration Summaries

- Derive the operational-field allowlist from the current backend renderer, not from product-language guesses. When the web template turns persisted keys into title-cased labels, fixtures must use those exact emitted labels, such as `run_at` → `Run At`, `repeat_type` → `Repeat Type`, `agent_ref` → `Agent Ref`, `base` → `Base`, and `draft` → `Draft`.
- Test realistic trigger and action records copied from current template output. Preserve the renderer's actual field ordering, including sorted-key output; hand-ordering important fields first can hide production truncation. Synthetic labels such as `Time`, `Repeat`, `Primary Agent`, `Base Branch`, or `Open As Draft Pr` can make tests pass while all authoritative values are discarded in production.
- Keep prompt/instruction bodies secret-safe by reporting only `configured`; omit secret, token, password, credential, and API-key fields. Bound retained operational values and the number of summary fields.
- Apply field-count bounds after collecting and prioritizing useful fields, not by stopping the source-order walk. A schedule node can contain generic task fields before `Repeat Interval`, `Repeat Type`, and `Run At`; reserve room for role-specific operational fields and include state such as `Enabled` when it changes execution behavior.
- Cover representative schedule triggers, task nodes, notifications, and pull-request actions so key-to-label drift, ordering, and field-budget starvation are visible before release.

## Edge Conditions

- Treat an edge condition as part of user-visible topology, not parser-only metadata. If the backend detail contract exposes a condition, terminal graph output must distinguish conditional branches even when edge labels are empty or duplicated.
- Render only an allowlisted, bounded condition summary. Do not dump arbitrary condition JSON, prompts, expressions containing credentials, or unbounded backend text; use a safe configured/omitted marker when a condition cannot be summarized without exposing content.
- Cover labelled and unlabelled conditional edges, duplicate labels with different conditions, malformed or oversized condition payloads, terminal-control injection, and unavailable-graph diagnostic rows. Assert that conditions remain associated with the correct edge after correlation and sorting, while JSON retains the established structured contract.

## Correlation Performance

- Before optimizing, profile identity calculation, existence checks, merging, duplicate grouping, endpoint uniqueness, candidate construction, and endpoint-name resolution. Repeated linear scans across every parsed node or edge can combine into quadratic work even when each helper looks inexpensive in isolation.
- Benchmark generated 10, 100, and 500-node and edge fragments, including duplicate identifiers, conflicting candidates, unmatched detail records, and endpoint-name fallbacks. The response boundary permits multi-megabyte inputs, so include a realistic large fragment rather than extrapolating only from tiny fixtures.
- Build stable multi-entry indexes for every exact identity or endpoint reference used by the current correlation rules. Retain candidates that collide within or across identity namespaces; preserve document order before applying the old exact-match and ambiguity predicates rather than selecting a map winner.
- Preserve the established identity comparator exactly. For `strings.EqualFold`, every equivalent value must generate the same index key, including mixed ASCII/Unicode fold cycles. A pure-ASCII `strings.ToLower` fast path is valid only when non-ASCII handling maps any cycle with an ASCII member to that same lowercase ASCII representative; otherwise use one stable representative from the full cycle. Do not combine `strings.ToLower` for ASCII with minimum-rune cycle canonicalization for non-ASCII values: it splits `ſ`/`S` and `K`/`K` classes.
- Treat ambiguity as monotonic during indexed candidate construction: once multiple distinct candidates are observed, a third candidate must not make the lookup appear unique. Merge indexes consistently when records merge, including duplicate grouping and endpoint/name resolution paths.
- Preserve sequential correlation semantics when a successful merge hydrates missing IDs, keys, endpoints, or names on the authoritative row. A batch optimization must not precompute an initially unique graph position and merge directly if prior detail records can change its later candidate set; retain an equivalent stateful final-candidate check against the mutable output.
- Avoid eagerly allocating specialized indexes needed only by later optional resolution paths when doing so would regress small fixtures; measure 10-record allocation and latency along with large-input throughput.
- Compare before and after with matched fixtures and report median latency, allocations, and allocated bytes. Require a material large-input improvement, near-linear scaling across the benchmark matrix, strict parsed/rendered output equivalence, and no meaningful regression for small automations. Record fixture identity/checksum, source revision, Go version, raw runs, and median calculations for reproducibility.

## Regression Matrix

- Cover structured graph and detail records.
- Cover graph-specific and detail-specific fallback records.
- Cover safely correlated records and intentionally conflicting or uncorrelated detail records.
- Assert structured-versus-text count precedence, parent-count merging, provenance, availability, warnings, rendered state, and JSON separation where applicable.
- For config summaries, compare fixtures against labels and ordering emitted by the current backend template; assert required role-specific operational values survive the configured field cap and that secret/prompt bodies do not.
- For indexing changes, add duplicate node and edge identities, ambiguous endpoint names, unmatched records, and reordered input; compare complete parser output against the pre-change behavior.
- Test the canonical key directly, plus key-only node and edge correlation fixtures that omit matching IDs, for `ſ`/`S` and `K`/`K`; reverse EqualFold-duplicate input and assert identical ordered output and duplicate diagnostics.
- Include a three-or-more-candidate collision and an ID/key cross-namespace collision. Assert all ambiguous candidates remain unmerged and that the prior document-order exact-match result is preserved where the parser formerly resolved one.
- Include sequential detail records where the first hydrates a graph edge's missing identity and the next would become ambiguous. Assert the second remains unmatched, with original graph count, provenance, warnings, and partial state unchanged from the pre-index parser.
- Keep transport-level regressions for cancellation, response size limits, project and automation identity validation, and malformed-value partial behavior when refactoring the parser boundary.

## Validation

Run focused automation parser tests and benchmarks first, then finish with:

```bash
go test ./... -run 'Automation|Graph|Detail' -count=1
go test ./... -bench 'Automation|Graph|Detail' -benchmem -count=10
go build ./...
go vet ./...
go test ./... -count=1
git diff --check
```

Do not run the race detector for parser-only, indexing, rendering, benchmark, or other single-threaded changes. Add the narrowest relevant `go test -race` package or test invocation only when the diff changes goroutines, synchronization, shared mutable state, cancellation between concurrent operations, or another concurrency-sensitive path, or when the task explicitly requires race-detector evidence. Avoid a repository-wide race run unless the concurrent behavior crosses package boundaries or repository policy explicitly requires the full suite.

Review the final diff to ensure only the intended parser, indexes, callers, benchmarks, evidence record, and additive regressions changed, with no generated or unrelated files.
