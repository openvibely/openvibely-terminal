# Project schedule catalog client contract

The terminal client prefers the bounded, project-scoped catalog:

```http
GET /api/schedules/catalog?project_id=<selected-project-id>
Accept: application/json
```

The backend implements this route as a direct schedule and task-title query. It includes schedules whose tasks are hidden from the task board or task-reference list, and schedules with next runs outside the calendar's current week. Older servers that do not expose the route continue through the calendar, task-reference catalog, and per-task schedule-tab fallback. That fallback may omit non-visible scheduled tasks, may download attachment metadata, and makes one request per reference-visible task in addition to its calendar, reference-catalog, and failed catalog-probe requests.

## Response

Return a project-scoped envelope containing schedule display fields only:

```json
{
  "project_id": "project-123",
  "summary": "Upcoming schedules",
  "schedules": [
    {
      "project_id": "project-123",
      "schedule_id": "schedule-456",
      "task_id": "task-789",
      "task_title": "Nightly report",
      "next_run": "2030-04-05T09:30:00.125-07:00",
      "enabled": false,
      "display_text": "Nightly report — daily"
    }
  ]
}
```

`project_id` on each item is optional; when present, it must match the envelope. `enabled` is required so disabled schedules remain distinguishable. `next_run` may be `null`; when present it is an RFC3339 timestamp and retains its exact offset and fractional seconds. `task_title` and `display_text` are display-only fields. Do not include task prompts, attachment records, schedule edit forms, or other task detail data.

Include every schedule in the selected project, including one-time schedules and recurring schedules whose next occurrence falls outside the calendar's default week. Keep response order deterministic: schedules with a next run come first in ascending next-run order, followed by null next runs; ties use newest creation time and then schedule ID. Return an empty `schedules` array for an empty project.

The backend rejects catalogs above 10,000 entries or 8 MiB with HTTP 413. The client independently caps the response body at 8 MiB and the decoded catalog at 10,000 records, and rejects a response whose envelope project ID differs from the requested project or whose records omit schedule ID, task ID, or enabled state. The client preserves first-seen schedule order and the earliest exact next-run timestamp if a server repeats a schedule row.
