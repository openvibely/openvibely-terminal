# Execution Chat Output SSE Contract

Use this reference when integrating the backend's execution-specific chat stream rather than the multiplexed live-event stream, or when changing framing shared by both streams.

## Contract

- `GET /events/chat/:exec_id?offset=N` returns `text/event-stream`. `offset` is a nonnegative UTF-8 byte count, not a rune or display-cell count. The backend replays durable output after that offset and clamps a mid-rune offset to a safe boundary.
- Unnamed SSE events carry output chunks. Named `done` and `error` events are terminal signals; their data is status/error text, not JSON. Output and terminal data may contain multiple SSE `data:` lines, which the SSE parser must join with `\n`.
- Ignore comment/heartbeat frames, preserve event order, and do not JSON-decode output chunks. Use the authenticated cookie jar, a context-cancelable long-lived request, no redirect following, and the same exact-path authentication classification used by other reads.
- A stream disconnect is recoverable: reconnect with the latest received UTF-8 byte offset so replay fills only the missing durable suffix. A clean stream close is not proof that the chat completed unless a terminal frame or authoritative status confirms it.
- Treat `GET /api/chat/message/:id` as the authority for terminal status, final response, and task IDs. A `done`/`error` stream frame should trigger an immediate status fetch, not local completion. Queued chat inputs may later resolve to a different execution ID, so begin the execution stream only after status/promotion evidence establishes that alias.
- Terminal status can race durable stream propagation. Before settling completion, flush accepted stream bytes into the single assistant entry. If the terminal response is empty or a strict prefix of the accepted streamed output, preserve the streamed output rather than erasing or shortening it. A divergent terminal response remains an authoritative correction, and a longer response may extend the entry.

## Shared Framing Changes

- `StreamChatOutput` and `StreamEvents` may share one private raw framing scanner, but only at the framing boundary. Let it own the scanner token limit, raw `event:` and `data:` accumulation, comment suppression, blank-line dispatch, and full frame reset.
- Keep transport and semantic policy in each caller: request and channel ownership, authentication diagnostics, cancellation, payload decoding, endpoint behavior, scanner-error wrapping, disconnect handling, and clean-EOF behavior must not move into the framing helper.
- Preserve caller-specific field normalization. Chat removes at most the one optional separator space after `data:` so `data:  world` yields ` world`; live events retain their established trimming before JSON/name handling. A shared helper must expose enough raw field value for both policies rather than normalizing them identically.
- Dispatch only on a blank line. Comments inside a multiline frame do not dispatch or reset it. After dispatch, reset both event name and all data lines so named and unnamed multiline frames cannot leak state into one another.
- Do not synthesize a final event from an unterminated frame at EOF unless the existing caller contract explicitly does so. Preserve each caller's distinct clean-EOF result.

## Client Regression Coverage

Cover exact route/query construction and path escaping, unnamed and multiline data frames, `done` and `error` frames, nonzero offsets including UTF-8 boundaries, context cancellation, auth/non-200 responses, stream errors, and channel closure. Verify that a reconnect request uses the accumulated byte offset and that terminal status still comes from the status endpoint.

For completion reconciliation, add focused UTF-8 regressions for an empty terminal response, a shorter strict-prefix response, a divergent correction, and a longer final response. Assert that completion leaves exactly one assistant entry, preserves the most advanced accepted output for lagging snapshots, and still clears pending/render state.

For shared framing refactors, add focused cases for exact chat whitespace (`data:  world`), live JSON/name normalization, comments and keepalives, named-to-unnamed and unnamed-to-named multiline reset, oversized scanner tokens, cancellation, authentication, disconnects, and each stream's exact clean-EOF contract. Run focused stream tests before the repository-wide transport validation sequence.
