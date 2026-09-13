# Task-Thread SSE Identity Guards

Use this reference when changing interactive open task-thread SSE routing or its post-ack event tests.

## Classification And Guarding

- Classify the semantic event using the trimmed payload discriminator `type` when it is present; fall back to the SSE frame name only when the payload has no discriminator. Do not let aliases such as `task_update`, or an empty frame name, bypass a `task_status_changed` or `chat_new_message` identity guard.
- After task/project and current-generation checks, ignore same-task, same-project lifecycle or chat events that have neither a matching non-empty `exec_id` nor a matching `pending_input_id`. An identity-less alias must not change `threadStatus`, request current-reply status, append/replace transcript content, terminalize the reply, or take ownership of the task stream.
- Preserve the established promotion rules: matching execution identity continues normal progress/completion handling, and a matching pending-input identity may promote to the verified execution identity. Foreign executions, unrelated pending-input IDs, project mismatches, and stale generations remain rejected.
- Treat identity-less aliased or unnamed `chat_new_message` frames the same as canonical identity-less frames: re-arm the owned stream if required, but never duplicate or replace the current task-thread stream.

## Regression Matrix

- Extend the post-ack task-event regression with canonical, aliased, and unnamed frame names for both `task_status_changed` and `chat_new_message` payloads. Include the canonical-name fallback case for payloads without `type` so compatibility behavior remains explicit.
- For every ignored case, assert unchanged thread status/transcript and no `fetchChatStatus` request or status-fetch subcommand. Prove stream continuity by sending owned stream output after the ignored frame, then deliver a subsequent execution-matched completion and assert that it still updates/completes the current reply.
- When the model stores `sseEvents` as receive-only, retain a local channel handle for test re-arming and output assertions instead of trying to refill the model field directly. For Bubble Tea commands, execute the outer `tea.Batch` and inspect each subcommand before asserting HTTP calls or messages.

## Validation

Run the focused regression and neighboring terminal SSE/thread tests, then the repository gates: `go build ./...`, `go vet ./...`, `go test ./... -count=1`, the repository's `gofmt` check, and `git diff --check`.
