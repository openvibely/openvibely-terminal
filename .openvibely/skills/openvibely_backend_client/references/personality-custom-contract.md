# Personality Custom Backend Contract

Use this reference when implementing or auditing personality discovery and custom CRUD in the OpenVibely TUI client.

- Verify the current backend contract rather than treating the rendered page as the only API: the scoped HTML `/personality` page supplies built-in and custom cards, while custom JSON operations use `POST /personality/custom`, `GET /personality/custom/:key`, `PUT /personality/custom/:key`, and `DELETE /personality/custom/:key`.
- Carry the selected `project_id` on every personality list, detail, create, update, delete, and reset request. Project scope is part of the operation, not just a display selection; add a non-default-project request regression.
- The update route also supports the backend's built-in override semantics, and delete/reset must preserve backend behavior for active presets and custom selections. Do not implement a local cache or infer reset state from terminal memory.
- Parse one structured record per personality card and retain canonical key, ID, name, type, description, system prompt, preview, and active/override state. The built-in `Base` card can have an empty key and must remain discoverable; effective built-in overrides must not be lost during parsing or rendering.
- Return non-nil empty collections so CLI JSON emits `[]`, not `null`. Reuse parsed records for list output, reference resolution, selectors, and refreshes where possible, and fetch detail only when the full prompt is required.
- Add request-level tests for exact routes, methods, project query, JSON fields, successful status variants, duplicate/validation/backend errors, and built-in reset/delete behavior. Ensure response bodies are closed and backend error messages reach the command layer without being converted to success.
