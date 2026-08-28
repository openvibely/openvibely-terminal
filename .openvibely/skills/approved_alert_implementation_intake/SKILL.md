---
kind: openvibely.agent_skill
version: 1
skill:
    key: approved_alert_implementation_intake
    name: Approved Alert Implementation Intake
    scope: project
    enabled: false
    description: Safely turn approved unlinked alerts into immediately executed, linked implementation tasks.
    archived: true
    absorbed_into: approved_alert_implementation_handoff
    archive_reason: The project intake skill substantially duplicates the global approved alert handoff skill; the global survivor now includes its routing, workflow, and the new snapshot/outcome reconciliation safeguards.
routing:
    triggers:
        - approved actionable notifications
        - approved alert processing
        - claim_alert
        - create_alert_implementation_task
        - complete_alert_processing
    priority: 80
---

# Approved Alert Implementation Intake

Use this skill for scheduled processing of approved, unlinked notifications into repository implementation tasks.

## Snapshot Before Mutation

- Let the scheduled runtime supply the project; call `list_alerts` without `project_id`, using `decision_state=approved`, `implementation_task_linked=false`, a bounded limit, and stable pagination. Omit the read filter so both read and unread approved alerts are eligible.
- Follow every returned pagination offset until there is no continuation. Collect the complete snapshot before claiming or linking anything: linkage removes rows from the filtered result set, so mutating while advancing an offset can skip alerts.
- Call `get_alert` for every collected alert and inspect its complete body and metadata before deciding whether it is processable.

## Per-Alert Sequence

- For a processable alert, call `claim_alert`, then create a focused Backlog task with `create_alert_implementation_task` only after the claim succeeds.
- Include the alert ID, reviewed context, acceptance criteria, and direct instructions to implement the change in its repository, add or update tests, and run required validation. State that the created task is already the linked implementation task, must begin implementation directly, and must not run notification intake, call `get_alert`, or create/find another implementation task.
- Human approval authorizes implementation by the linked task, but not merge, release, deployment, destructive remediation, or credential changes. Do not describe the task as lacking authorization to implement.
- Set `goal` exactly to the scheduler-provided audit lifecycle goal; never paraphrase or weaken its strictly read-only audit, fresh-review, fix-and-repeat requirements.
- Pass the exact returned `implementation_task_id` to `execute_tasks` so the linked task starts immediately; do not leave it waiting in Backlog.
- Call `complete_alert_processing` only after task creation/linkage and `execute_tasks` succeed. If creation, linkage, or execution fails, call `fail_alert_processing` with a concise error and do not report completion. Call `release_alert_claim` only when no task was linked and immediate retry by another scan is appropriate.

## Safety Checks

- Preserve the original collected snapshot while processing; do not re-page the mutating filtered query between alerts.
- Treat each alert as an independent claim/link/execute/complete sequence, and retain the implementation ID and outcome for recovery. Creation/linkage is atomically at most one task and safe to retry after a crash.
- Report the final processed, failed, and released counts from tool results rather than assuming a claim implies successful implementation-task execution.
