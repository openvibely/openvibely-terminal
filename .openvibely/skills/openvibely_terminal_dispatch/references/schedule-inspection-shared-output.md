# Shared Schedule Inspection Output

Use this when `/schedule show`/`open` reaches the same inspection behavior through different selection routes, such as a typed reference and an inline picker.

- Consolidate only after each route has resolved its `ScheduleEntry`. Keep typed matching, ambiguity behavior, picker labels, selector state, cancellation, and cached-list reuse in their existing route-specific paths.
- Route the resolved entry, selected project ID, and existing output mode through one post-selection routine. The routine must load the entry's exact `TaskID` with the selected project ID and construct the established plain or JSON schedule/task result; it must not re-resolve the schedule ref or derive a task from display text.
- Preserve the existing distinctions: an absent `TaskID` and an authoritative 404 bound-task lookup render the established unavailable result, while auth, cancellation, transport, HTTP 5xx, decode/malformed detail, and foreign/mismatched identity failures remain visible errors.
- Keep compatibility aliases such as `open` normalized to the same behavior without changing their command registration or confirmation/mutation paths.
- Add a parity matrix that drives typed and picker-selected routes through both canonical and alias actions. Compare exact plain bytes and JSON shape for representative task states, no-TaskID, and 404 cases; assert each bound-task request carries the selected project ID. Separately assert non-404 failures propagate unchanged through the shared boundary.
