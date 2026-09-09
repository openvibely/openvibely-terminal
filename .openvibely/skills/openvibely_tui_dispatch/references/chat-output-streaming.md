# Real-Time Chat Output State Machine

Use this reference when adding or auditing execution-specific chat output streaming in the TUI.

## Start And Correlate

- For a directly accepted execution ID, keep normal status polling and start the output stream in parallel. For a queued input, poll the input ID first; connect the stream only after a status response authoritatively promotes it to an execution ID. Keep the original input ID for status polling and the promoted ID as a verified stream alias.
- Tag every stream event, disconnect, and reconnect message with a stream generation, chat submission token, selected project ID, and the applicable session/project generations. Ignore messages that fail any current-scope check before changing transcript, busy, pending, or stream state.
- Cancel and invalidate the stream on project changes, pending-chat cleanup, login/auth transitions, terminal completion/failure, and model cleanup. A stale stream must not reconnect or settle a newer submission.

## Render And Reconcile

- Accumulate output chunks into one agent transcript entry; update that entry in place rather than appending one entry per chunk. Track the reconnect offset as `len([]byte(output))`, not rune count or display width.
- Enforce accepted-byte ordering at the shared transcript append boundary, not at a list of known callers. Before any ordinary later entry is appended, materialize unrendered assistant bytes so unrelated visible SSE events, asynchronous slash-command results, warnings, and state-transition output cannot appear ahead of an already accepted reply. Keep a private raw append primitive for the stream's own agent insertion to avoid recursive flushing; benchmark-only legacy paths should also bypass the new behavior when they intentionally model the old implementation.
- Treat a forced nonterminal flush as cadence-timer invalidation. A Bubble Tea tick cannot be cancelled, so give render messages a render generation distinct from stream/submission/project/execution identity. Advance it when scheduling and whenever a forced flush clears the queued-render flag; stale ticks must be ignored without clearing or firing a replacement timer scheduled for later bytes.
- Polling remains the authoritative source for terminal status, final response, and task IDs. A `done` or `error` stream frame should invalidate the stream and fetch status immediately. If a processing status contains durable partial output, merge only a suffix that extends live output; do not regress newer streamed text. On completion, first materialize a buffered first block, then replace that same streamed entry with the final response so the transcript never duplicates the answer.
- Preserve partial output when the stream disconnects or fails. Reconnect after a bounded delay using the current byte offset while polling continues; authentication failures transition through the normal auth-recovery path instead of looping. Clean terminal/failure paths clear the stream channels, cancel function, alias, offset, and pending-chat state.

## Focused Tests

Cover direct streaming, queued acceptance-to-promotion, incremental chunks including multiline and non-ASCII output, one-entry transcript updates, status partial-output merge, authoritative completion/failure/cancellation, terminal stream signals, disconnect/reconnect offset recovery, stale generation/project/submission events, auth failure, and cleanup. When testing a Bubble Tea `tea.Batch`, execute the returned `BatchMsg` subcommands before asserting HTTP calls or follow-up messages.

Add regressions that deliver an unrelated visible SSE event and a current asynchronous command result while the first assistant bytes await cadence rendering, then assert the assistant entry precedes each later entry exactly once. For timer safety, schedule a tick, force a nonterminal flush, append another delta and schedule its replacement tick, then deliver the old tick first; it must neither redraw nor clear the replacement timer, and only the new render generation may draw the later bytes. Preserve the 33 ms cadence and benchmark redraw counts while adding these correctness boundaries.
