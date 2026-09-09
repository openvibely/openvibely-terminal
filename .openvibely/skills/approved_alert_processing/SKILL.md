---
kind: openvibely.agent_skill
version: 3
skill:
    key: approved_alert_processing
    name: Approved Alert Implementation Processing
    scope: project
    description: Safely snapshot, inspect, claim, link, execute, and finalize approved actionable notifications.
---

# Approved Alert Implementation Processing

Use this skill for scheduled inbox runs that turn approved, actionable backend alerts into implementation tasks. Treat the current task's persisted project as authoritative; never copy a project ID from earlier messages, examples, memory, or unrelated tool output.

## Snapshot Before Mutation

- Run the primary inbox query with `decision_state=approved`, `processing_state=unclaimed`, `implementation_task_linked=false`, a bounded limit, and stable pagination offsets. Omit read, type, and source filters so both read states and all actionable sources qualify. Do not pass `project_id` when the runtime supplies the scheduled task's project.
- Query failed notifications separately using the task's recovery-query contract. Do not broaden or combine the primary unclaimed query with failed recovery records.
- Follow every returned pagination offset for each query and collect complete non-mutating snapshots before claiming, linking, or processing any notification. Linkage removes rows from filtered results, so mutating while paging can make offset pagination skip notifications.
- Only after all primary and recovery pages have been collected, call `get_alert` for every collected notification. Inspect the full body, acceptance criteria, source context, and metadata before making the first claim.
- Deduplicate by notification ID if records can appear across snapshots. Skip completed historical entries, and only resume a recoverable claim when the recovery contract permits it and ownership/linkage state is safe.

## Link And Start

- For each processable alert, claim it, then call `create_alert_implementation_task` with a focused Backlog title and a prompt containing the notification ID, reviewed context, acceptance criteria, and direct instructions to implement in the repository, add or update tests, and run required validation.
- The created task is already the linked implementation task. State that it must begin implementation directly, must not run notification intake or call `get_alert`, and must not create or search for another implementation task.
- Pass the exact approved lifecycle goal required by the inbox policy. Human approval permits implementation-task creation and execution only; do not authorize merge, release, deployment, destructive remediation, or credential changes. Do not imply that the task lacks authorization to implement.
- Immediately call `execute_tasks` with the exact `implementation_task_id` returned by creation/linkage. Do not leave the linked task waiting in Backlog.

## Finalize Safely

- Call `complete_alert_processing` only after task execution succeeds.
- If creation, linkage, or execution fails, call `fail_alert_processing` with a concise recovery-oriented error so the linked task can be inspected; do not report completion.
- Call `release_alert_claim` only when no task was linked and another scan should retry immediately. Never release a claim after a task has been linked.
- Keep per-alert status and returned task IDs so the final report can distinguish fully processed alerts from failures without re-paginating after mutations.
