---
kind: openvibely.agent_skill
version: 4
skill:
    key: native_sdlc_redundancy_audit
    name: Native SDLC Redundancy Audit
    scope: project
    description: Perform a focused, read-only redundancy audit and file one deduplicated maintenance suggestion.
---

Use this skill for Native SDLC Redundancy Finder runs in which the repository is inspected for demonstrated duplicate code, configuration, or workflow logic and, at most, one maintenance suggestion is filed.

## Procedure

- Choose one bounded project component or workflow and vary the area between runs. Inspect repository-local source, configuration, workflows, tests, and call sites without modifying code or creating implementation tasks. Do not use GitHub issues for duplicate detection.
- Establish the actual checkout before relying on local history: inspect the worktree/branch/commit and confirm that referenced helpers and files exist in the current checkout. Treat later or unrelated history as context only; never file a finding against source that is absent from the audited checkout.
- Require concrete evidence of the same responsibility implemented in at least two active locations. Trace both paths through their callers and user-facing flows, including filtering, rendering, keyboard navigation, cache rebuild/invalidation, and relevant tests when applicable. For client/API request builders, compare empty-value handling, URL escaping, and all callers before treating specialized and general helpers as equivalent. Distinguish true redundancy from a correctness defect, a performance-only opportunity, or merely similar-looking code.
- For commands that support both typed references and interactive picker selection, compare the post-selection action paths. Duplicate request dispatch, error handling, status text, and refreshed-result rendering can usually be consolidated into a command-local action helper, while reference resolution, picker behavior, and confirmation timing remain entry-mode-specific.
- When guided and headless configuration-save paths perform the same mutation, compare their successful completion tails separately from setup. A shared helper may own post-save list refresh and status/table rendering, but preserve mode-specific validation, credential/secret handling, and prompts outside it.
- When the repeated responsibility is parser or model construction, compare field extraction and model initialization separately from format-specific fallbacks, such as graph versus detail representations. Consolidate only the common identity, metadata, display-state, and count rules; leave source-specific fallbacks and traversal outside the shared helper, and require regression coverage for each representation.
- For duplicated streaming protocol readers, separate protocol framing from caller-specific payload semantics. For SSE specifically, compare event-name tracking, multiline `data` accumulation, comment handling, frame emission, scanner errors, and state reset as the potentially shared layer; preserve each caller's whitespace normalization, payload conversion, authentication/status handling, cancellation, disconnect policy, and end-of-stream behavior unless equivalence is independently demonstrated.
- When repeated HTML-to-text conversion appears, compare the generic renderer with workflow-specific recursive walkers at the traversal, whitespace, and block-boundary levels. Preserve meaningful output rules, such as omitting form or button controls in task-thread text while retaining their text in generic fragments; propose sharing only the common traversal/normalization and add parity tests for both output contracts.
- For paginated alert-listing workflows, compare ordinary listing with post-mutation refresh paths: keep page acquisition differences, such as GET versus mutation refresh, separate while considering a shared helper for identical first-seen, ID-based aggregation when ordering and deduplication semantics match. Trace callers that consume the combined inbox and retain regression coverage for each acquisition path.
- Prefer the smallest safe consolidation. For repeated matching or transformation logic, extract a shared helper for the common operation while preserving surrounding cache, invalidation, ordering, and lifecycle behavior. Record the repeated locations, why they own the same responsibility, the concrete paths affected, expected versus actual behavior, risk, implementation direction, acceptance criteria, file/symbol references, and regression cases.
- Before filing, paginate `list_existing_automation_notifications` until `next_offset` is zero. Compare IDs, titles, types, and lifecycle states; for every plausible overlap, call `get_alert` and read the full body. If the same user problem or implementation scope is already covered, skip it and continue only with a separately actionable candidate, restarting evidence and duplicate checks for that candidate. Create at most one notification per run.
- Create a new notification only after the full duplicate review, using type `maintenance_suggestion`. Its body must begin with exactly `## Summary`, followed by 2-4 plain-language sentences explaining the problem, one concrete user-visible example, and why it matters. Put all technical analysis after the summary. State that the suggestion is pending human review and that approval authorizes task creation only, not merge, release, or deployment.
- For a Go repository, run relevant focused tests, `go build ./...`, `go vet ./...`, and an uncached full suite `go test ./... -count=1` when practical. If a stricter selected audit policy makes the workspace immutable, do not run commands that can write caches or artifacts; use source inspection and recorded evidence instead, and explicitly report that validation was excluded by the read-only boundary. Always state that no code or implementation tasks were changed. If no distinct finding remains, report that no new notification was found.

## Pitfalls

- Do not report every repeated string or similarly shaped function; show that both implementations perform the same active responsibility and preserve any meaningful differences.
- Do not collapse cache maintenance or invalidation into a matching helper just because the scan logic is duplicated; keep lifecycle behavior explicit and test both cached and fallback paths.
- Do not stop notification pagination at the first page or compare titles alone. A completed or differently titled alert can still cover the same scope.
- Do not put implementation detail before the user-facing summary, and do not file a finding that is only a bug or speed improvement.
- Do not treat a helper mentioned by later local history as present in the current worktree; reconcile repository state before using that evidence.
