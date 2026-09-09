# Task Follow-Up Live View

Use this reference when changing `/tasks open`, task-thread input context, task follow-up submission, or task-specific live output in the TUI.

## Entry And Rendering

- Opening a task should install the task's thread identity and follow-up input context, then load the project-scoped thread fragment directly. Do not invoke the full task-detail loader as a shortcut: it can fetch unrelated sections and render model controls.
- Render the existing conversation, including assistant messages, even when the backend HTML nests transcript content near or inside follow-up controls. Suppress interactive controls (`form`, inputs, buttons) without blindly deleting a wrapper subtree that also contains conversation output. Add a fixture matching the backend markup shape.
- `/chat` must clear the active task-thread context, restore project-chat input behavior, invalidate pending task-thread work, and cancel any task-owned stream.
- Treat bare `/chat` and `/chat <message>` differently while a task reply is pending. Bare `/chat` may explicitly settle/cancel the active task-owned turn before leaving. `/chat <message>` must reject overlap before incrementing thread request tokens, clearing pending ownership, leaving the thread, or canceling its stream; otherwise the installed reply becomes stale and can strand the submission guard indefinitely.
- Treat replacement opens transactionally while a reply is active. Do not flush, clear, cancel, or invalidate the current task reply merely because `/tasks open <ref>` was entered. Preserve the existing thread and stream until the replacement reference resolves and the replacement view is successfully established, or until the user explicitly exits. Missing/unknown/ambiguous refs, selector cancellation, authentication errors, transport errors, and thread-fetch failures must leave the current turn owned and observable.
- Keep `/task open` behavior deliberate: either normalize it through the same path as `/tasks open` or reject it consistently in dispatch and help. Do not leave singular handling accidental.

## Operation Freshness

- Session and project generations protect cross-session and cross-project races, but they do not order successive task-thread operations within the same scope. Allocate a monotonic task-thread operation token for each `/tasks open` request and for leaving/replacing the active thread; carry it in thread result messages and accept a result only while it is current.
- Carry exact project, task, thread, session/auth generation, request ID, and stream generation/ownership on task follow-up messages. Reject stale results before changing `busy`, transcript content, thread identity, status, placeholder, pending execution state, reconnect state, or scheduling more commands.
- Preserve ordered request IDs within each operation family. A matching project/task ID alone is insufficient when opens, replies, refreshes, polling, reconnects, and terminal events overlap.
- Cancel and invalidate a task-owned stream when explicitly leaving its thread or after a replacement thread has been successfully committed. A canceled stream must not reconnect or append output after `/chat`, a completed replacement open, a project switch, or a session transition.

## Follow-Up Streaming

- Plain-text input while a task thread is active is a task follow-up, not project chat. On acceptance, use the returned direct execution ID or queued identity to enter the existing execution poll/stream state machine while retaining strict active-thread ownership.
- Create exactly one mutable assistant transcript entry for a follow-up. Append incremental chunks to that entry rather than emitting one transcript block per chunk; throttle redraws without delaying all visible output until completion.
- Treat stream offsets as byte offsets, not rune counts. Buffer incomplete UTF-8 sequences across chunks, render only complete text, and reconnect from the exact number of bytes authoritatively consumed so multibyte output is neither skipped nor repeated.
- Preserve output received before terminal status. Completion must flush buffered complete output and reconcile authoritative status/output exactly once. Failure or cancellation must make the terminal error visible and settle pending state rather than leaving the assistant entry hidden or indefinitely pending.
- If acceptance intentionally provides no streamable execution identity, use a scoped thread refresh fallback and settle it explicitly. Do not poll an empty ID or pretend streaming is active. Propagate fallback authentication and transport errors through the normal typed handlers; never discard a failed refresh and report `sent`. If neither streaming nor fallback can produce output, surface an explicit terminal failure.
- Direct and queued executions remain pending until authoritative completion, failure, or cancellation. Do not infer completion merely from acceptance or a temporary lack of chunks.

## SSE, Polling, And Reconciliation

- Apply task ownership before generic SSE rendering. Task-scoped events require exact non-empty project/task ownership and current session/project/SSE generations; foreign, stale, or identity-less task events must not mutate either the task thread or generic `/events` transcript.
- For a pending task reply, correlate same-project, same-task status events to the active execution as well. Reject an event whose non-empty `exec_id` is neither the accepted ID nor a verified promoted execution, and reject a non-empty unrelated `pending_input_id`. A new promoted `exec_id` is accepted only when the event's `pending_input_id` matches the installed queued input. Perform this gate before generic `/events` display, thread status mutation, transcript appends, or status fetches so delayed foreign executions cannot suppress or terminate the current turn.
- During the submission-before-acknowledgement window, no event identity is trustworthy. Reject both identity-bearing and identity-less same-task events before they can mutate status, transcript, generic `/events`, pending ownership, or schedule terminal reconciliation. Polling or streaming after the acknowledgement must recover output emitted during this short window.
- After acknowledgement, an identity-less same-task `task_status_changed` or mirrored `chat_new_message` still cannot be attributed to the active reply. Reject it before generic `/events` rendering, status mutation, terminal reconciliation, or assistant output append. Keep legacy identity-less task updates compatible only when no task reply is pending; acknowledgement alone is not correlation evidence.
- Queue promotion is one-way and exclusive. Once a queued input has been verified as promoted to execution `E`, require later identity-bearing events to match `E`; a matching `pending_input_id` must not authorize a conflicting `exec_id`.
- Correlate mirrored `/events/live`, execution-stream, and polling updates to the accepted execution or verified queue-to-execution promotion. Suppress duplicate chunks/completions from mirrored transports while preserving their ordering.
- Once an acknowledged follow-up has an execution identity, terminal task-status events should reconcile through execution status rather than launching a competing HTML thread refresh. Otherwise refreshed HTML can duplicate the same assistant response beside the mutable streaming entry.
- When a correlated terminal task-status event arrives for an acknowledged follow-up, flush accepted buffered assistant bytes before making terminal state observable. Do not append a competing completion/failure diagnostic in the task-event handler; fetch authoritative execution status and let that reconciliation path emit exactly one terminal result or error.
- Terminal state is monotonic. After authoritative completion/failure/cancellation, do not rearm polling, reconnect, append stale chunks, regress to running, or let an older refresh replace the reconciled transcript.

## Regression Matrix

Add focused model/dispatch tests for:

- Opening a thread whose backend markup contains assistant content and follow-up controls.
- Plain-text task reply acceptance for direct execution, queued execution/promotion, and identity-less fallback.
- One mutable assistant entry receiving incremental ASCII and split multibyte UTF-8 chunks with throttled redraws.
- Buffered output followed by completion, explicit failure/cancellation, and no-output terminal behavior.
- Disconnect/reconnect using exact byte resume offsets, including multibyte text.
- Duplicate mirrored SSE/stream/poll messages and terminal HTML-refresh suppression after execution acknowledgement.
- Identity-less and identity-bearing stale events before acknowledgement, proving neither can mutate the current turn or schedule reconciliation.
- Identity-less lifecycle and mirrored chat events after acknowledgement, proving they cannot mutate status, enter either thread or `/events` output, suppress a later correlated terminal event, or duplicate subsequently accepted execution-stream output.
- `/chat <message>` overlap while a task reply is pending, proving thread/request ownership and the active stream remain current and a later submission unblocks after settlement.
- Correlated terminal task events with buffered output, proving assistant bytes precede the sole authoritative terminal diagnostic.
- Same-task foreign `exec_id`/`pending_input_id` events, verified queued-input promotion, and a matching input paired with a conflicting post-promotion execution, proving only the active execution can mutate status, transcript, or pending ownership.
- Missing, unknown, ambiguous, canceled, auth-failed, transport-failed, and fetch-failed replacement opens during an active reply, proving the original stream and submission guard remain current.
- Identity-less fallback refresh auth/transport failures, proving normal diagnostics are preserved and no success is reported.
- Foreign/stale project, task, thread, session, request, and stream generations with no transcript or pending-state mutation.
- Explicitly leaving or successfully replacing a thread while streaming, proving cancellation and no stale reconnect/output.
- Project-chat behavior remaining unchanged.

When testing Bubble Tea commands, execute returned `tea.Batch` commands and their children only when the triggering event was accepted and actually scheduled that work. A rejected pre-ack SSE event commonly returns the normal wait-for-next-event command; executing it synchronously in a unit test without supplying another frame will block and can look like a runtime deadlock. Validate with focused tests first, then `go build ./...`, `go vet ./...`, and `go test ./... -count=1`. Run the narrowest relevant follow-up/stream tests under `-race` for concurrent path changes; expand to `./...` only if behavior crosses package boundaries or the task explicitly requires it. Run long validation stages separately if an aggregate wrapper timeout could obscure which stage failed.
