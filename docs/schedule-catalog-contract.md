# Project schedule catalog client contract

The terminal client prefers a bounded project schedule catalog at:

```http
GET /api/schedules/catalog?project_id=<selected-project-id>
Accept: application/json
```

The current backend checkout does not register this route. Until the backend implements it, the client falls back to the calendar, the complete task-reference catalog, and each task's schedule tab. The task board is not a complete source because it omits ordinary pending scheduled tasks. Legacy task-reference responses may include attachment metadata, and this fallback still makes one request per task. The 1–3 request listing target and a guarantee against downloading attachment snapshots therefore depend on backend support for this route.

## Response

Return one project-scoped envelope and only schedule list fields:

```json
{
  "project_id": "project-123",
  "summary": "Upcoming schedules",
  "schedules": [
    {
      "project_id": "project-123",
      "schedule_id": "schedule-456",
      "task_id": "task-789",
      "title": "Nightly report",
      "task_title": "Nightly report",
      "next_run": "2030-04-05T09:30:00.125-07:00",
      "enabled": false,
      "display_text": "Nightly report — daily"
    }
  ]
}
```

`project_id` on each item is optional; when present, it must match the envelope. `enabled` is required so disabled schedules remain distinguishable. `next_run` may be `null`; when present it must be an RFC3339 timestamp and retain its exact offset and fractional seconds. `title`, `task_title`, and `display_text` are display-only fields. Do not include task prompts, attachment records, schedule edit forms, or other task detail data.

Include all schedules in the selected project, including one-time schedules and recurring schedules whose next occurrence falls outside the calendar's default week. Keep response order stable. The client deduplicates repeated recurring occurrence rows by `schedule_id`, preserves first-seen order, and keeps the earliest exact next-run timestamp for duplicates.

The client rejects a response whose envelope project ID differs from the requested project, rejects records without schedule/task IDs or enabled state, caps the JSON body at 8 MiB, and caps the catalog at 10,000 records. Empty projects should return `"schedules": []` and the same project ID envelope.
