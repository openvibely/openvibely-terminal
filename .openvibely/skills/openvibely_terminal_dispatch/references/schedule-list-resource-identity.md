# Schedule List Resource Identity

Use this reference when auditing schedule listing, parsing, rendering, or schedule-resource selection in `internal/terminal/` and its client helpers.

## Rule

A task may have multiple schedule records. Treat `data-schedule-id` as the individual schedule resource identity; `data-task-id` is only ownership/context. Never deduplicate schedule cards or parsed entries solely by task ID. The deduplication key passed to a shared card helper must be the schedule ID (or equivalent canonical schedule-resource ID).

Preserve first-seen order for distinct schedule IDs. Duplicate markup for one schedule ID should remain collapsed according to the helper's existing duplicate policy, including retaining the richer/most informative card when that is the established behavior. Do not change query scoping, list refresh, or schedule-add behavior while correcting identity.

## Failure Path

If a page contains two schedules for one task and parsing stores entries in a map keyed by task ID, the second entry is discarded. `/schedule` then hides a real schedule, and its schedule ID cannot be selected for toggle or delete. This is a correctness defect, not merely a display issue.

## Regression Coverage

Add a fixture with two schedule records sharing one task ID but having different schedule IDs. Assert that both records survive parsing/listing and remain independently addressable by schedule ID for toggle/delete; also retain a distinct-task case. Include duplicate markup for one schedule ID and assert it is collapsed without losing the established richer-card behavior or changing output order.

For TUI coverage, prove both schedule IDs appear in rendered/selector data and target the second ID through both direct `/schedule toggle <id>` and `/schedule delete <id>` dispatch, asserting the expected method/path/query or form endpoint. If selector and mutation resolution already use `ScheduleID`, keep those paths unchanged and verify them with regressions rather than introducing a second identity fix.
