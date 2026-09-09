# Task-Detail Partial Results In TUI

Use this reference when changing `/tasks show`, task-detail tab rendering, or model handling of client results that contain both output and an error.

- Preserve and render a non-nil partial detail even when the client returns a typed lazy-load error. Append useful output before applying the model's existing authentication or offline transition so a failed command does not erase successful sections.
- Track failed sections using the same canonical task-tab metadata used for command recognition and `TaskDetail.TabText`. A failed thread, changes, or lifecycle load must render an explicit error marker/message, never `(empty)`; a valid successful empty fragment must continue to render `(empty)` without an error.
- Keep single-tab and full-detail semantics distinct: a single-tab command renders the requested tab and its failure, while full detail retains successful sibling sections and marks only the failed sections.
- Feed the typed aggregate through existing auth/transport classification. A `401` or exact login redirect should enter sign-in-required handling without losing any safe partial output; transport/decode failures should use offline/error handling rather than being silently treated as empty data.
- Add dispatch and model regressions for failed thread, changes, and lifecycle loads, full-detail sibling preservation, single-tab visibility, authentication and transport state transitions, and successful empty fragments. When a decoder expects an array, fixtures for an empty lifecycle response must use a valid empty array such as `[]`, not `{}`.
