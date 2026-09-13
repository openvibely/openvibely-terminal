---
kind: openvibely.agent_skill
version: 8
skill:
    key: native_sdlc_bug_audit
    name: Native SDLC Bug Audit
    scope: project
    description: Perform a focused read-only correctness audit and file one deduplicated bug suggestion.
---

Use this skill for Native SDLC Bug Finder runs that inspect a bounded repository component or user workflow for a concrete correctness defect and, at most, one pending bug notification.

## Procedure

- Select one focused component or workflow and vary the area across runs. Keep the audit read-only: do not change repository files, create implementation tasks, or use GitHub issues for duplicate detection. When a strict read-only boundary applies, also do not run tests, builds, formatters, or commands that can write caches or artifacts.
- Report only a demonstrated correctness defect, edge-case failure, broken behavior, or meaningful missing regression coverage. Trace a concrete failure path and record expected versus actual behavior, user impact and risk, likely implementation direction, file or symbol evidence, acceptance criteria, and regression cases. Do not file performance-only or duplication-only observations.
- For project-scoped workflows, trace scope end to end from the selected project and resource resolution through every follow-up lookup, default/delete action, refresh, and mutation request. Compare actual query/path parameters rather than relying on in-memory selection; a resource resolved within the selected project can still be rejected or mutate a same-ID resource elsewhere when a downstream request omits `project_id`. Require a non-default-project regression that asserts the intended scope on every related request and rules out cross-project mutation.
- For settings or configuration values where zero has a domain meaning such as unlimited, disabled, or no limit, trace the full parse, storage, and render round trip. Check whether pointers or presence flags distinguish an explicit zero from an omitted value, and ensure renderers do not collapse explicit zero into inherit/default; require a regression that verifies the user-visible result after setting and reloading zero.
- For HTML or server-rendered form parsing, treat absent controls and attributes, including a missing `selected` marker, as realistic malformed or version-skewed inputs rather than assuming browser-generated forms are complete. Trace selector results through attribute helpers before mutation; require nil-safe error handling that performs no update, with regressions for both a missing selected option and a missing form control.
- For CLI or TUI workflows that advertise machine-readable output, trace every relevant action/detail branch end to end instead of inferring behavior from a list path. Verify that the requested output mode reaches the final renderer, that structured backend data is serialized in the promised format, and that human-only styling or labels are absent; make the regression case assert parseable output and the expected detail schema.
- When a command supports user-facing filters alongside multiple output modes, compare the filter pipeline in every mode, especially human and JSON branches. Confirm that free-text, status, project, and other advertised filters are applied before serialization in each branch; do not assume a shared fetch means equivalent results. Require a regression that invokes the JSON mode with a selective filter and asserts unrelated records are absent as well as matching records being present.
- For live-event or stream-driven state machines, do not infer semantics from the transport event name alone. Inspect the payload discriminator (`type` or equivalent), identity fields, aliases, and normalization order; an aliased or generic event name can carry a lifecycle payload that bypasses an identity guard. Trace terminal-state transitions and verify that a stale or identity-less event cannot complete/fail the current pending thread and thereby suppress later updates. Require regressions for each accepted event-name/type combination, including unnamed or aliased lifecycle events, stale execution identity, and a later update proving the current thread continues processing.
- Before filing, call `list_existing_automation_notifications` through every page until `next_offset` is zero. Compare IDs, titles, types, and lifecycle states. For every plausible overlap, call `get_alert` and read its full body; if it covers the same user problem or implementation scope, skip it and inspect another independently actionable candidate. Create at most one notification.
- File only a distinct finding with notification type `bug_suggestion`. Put the detailed notification text in the API's long-form `body` field, not the short-message field. Begin the body exactly with `## Summary`, then give 2-4 plain-language sentences explaining the problem, one user-visible example, and why it matters. Put all technical evidence after that section.
- Before reporting, run validation appropriate to the audited surface when no strict immutable-audit boundary applies. For Go code, run focused regressions plus `go build ./...`, `go vet ./...`, `go test ./... -count=1`, and `git diff --check` when practical. Report the exact commands and outcomes; passing validation supports the evidence but does not disprove a workflow or contract defect.
- State that the notification is pending human review, that approval authorizes task creation only and not merge, release, or deployment, and that the audit did not change code or create implementation tasks. If no distinct defect remains, state that no new notification was found.

## Pitfalls

- Do not regard a clean checkout, source tests, or an assistant assertion as proof that validation covered the audited commit. Under a strict read-only boundary, state that builds and tests were intentionally not run and base the finding on inspected evidence.
- Do not stop duplicate review at the first alert page or compare titles alone; a differently titled or completed alert can still cover the same defect.
- Do not lead with internal implementation detail. The user-facing summary must precede the failure path and engineering guidance.
- Do not treat an event-name check as proof that a stream event is non-lifecycle. Backends and clients can encode the lifecycle discriminator in the payload or use generic/aliased transport names; inspect and test the actual combinations before concluding an identity guard is effective.
