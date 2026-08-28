# SSE Generation And Project Isolation

Use this checklist for SSE lifecycle changes that can overlap a project switch.

## Message Contract

- Allocate one generation per `connectSSE` attempt and carry it through every message emitted by that attempt: connected, event, disconnected, and reconnect/tick commands. Do not tag only the event payload; a late lifecycle message can overwrite current connection state or rearm the canceled stream.
- Carry the generation on reconnect commands created from a disconnect. A reconnect tick already queued before a project switch must be rejected just like a late stream message.
- Keep hand-built zero-generation test messages compatible only if the model's established test contract requires it; runtime stream messages must always carry the generation.

## Update Ordering

- In `Update`, reject a non-current SSE generation before calling the event formatter/handler. This prevents stale events from appending transcripts, fetching chat status, completing pending chats, or mutating active-project state.
- For current-generation task/chat payloads, reject a non-empty `ProjectID` that differs from `m.selectedID` before any display or processing. Keep the same check in the lower-level formatter/handler as defense in depth for direct callers and tests.
- Treat missing `ProjectID` explicitly: preserve compatibility for current-generation events when the existing single-project behavior allows it, but reject stale-generation messages even when their payload has no project metadata. Generation freshness is the authority for stale completions; payload scope cannot rescue an old stream.
- Install the new selected project ID before starting its replacement SSE stream. Old connected/disconnected messages and reconnect ticks must not change the new stream's state or schedule another old stream.

## Startup And Non-SSE Async Work

- Do not launch a project-dependent SSE connection unscoped in parallel with the initial project load unless the startup path is guaranteed to reconnect after automatic first-project or requested-project selection. Trace the ordering of the initial reconnect command, project-load response, default selection, and any explicit selection; add a regression that asserts the eventual stream request contains the selected project ID.
- Treat chat send acknowledgements and polling/status fetches as project-scoped asynchronous work, not only SSE messages. Tag `chatSentMsg`, `chatStatusMsg`, and their commands with the originating project/message or a generation token, or invalidate the pending chat when the project changes. Reject delayed A results before they can install `pendingMsgID`, change `busy`, or append a response in project B.
- Test a delayed chat acknowledgement and a delayed completion/status response delivered after switching projects. The old response must not replace current pending state, clear current work, or render old-project transcript content.

## Health And Login State Generations

- Treat connection-health/auth checks as asynchronous state snapshots too. Allocate a monotonic check generation for each `checkConnection` invocation, carry it in `connCheckedMsg`, and reject results from older generations before updating `connected`, `authRequired`, `connErr`, `capacity`, or `auth`.
- A successful login or project transition must invalidate pre-login and old-project health results before launching replacement checks. A delayed unauthorized or transport result from before login must not restore sign-in/offline state after the new session is established; a delayed healthy result must not clear a newer auth-required state.
- Keep login failure classification separate: credential rejection remains retryable sign-in guidance, while request timeout, connection refusal, malformed server URL, and other transport failures must transition or preserve genuine offline state and show offline recovery guidance. Do not let generic login error text mask the connection state.
- Add deterministic regressions that delay an old health response until after successful login, deliver it alongside the fresh response, and assert the fresh session state wins. Also cover login transport failure while the form is active and verify offline guidance, no password/body leakage, and retry/cancel behavior.

## Regression Matrix

Add deterministic tests that queue an A event, switch to B, and then deliver the queued A event followed by a valid B event. Cover foreign task and chat payloads, stale `chat_response_done` with matching and missing project metadata, pending-message/transcript preservation, absence of status-fetch requests, and active-project state stability. Also assert selected-project query parameters, current-generation missing-metadata compatibility, stale connected/disconnected/reconnect behavior, initial automatic project-selection reconnect scoping, and delayed non-SSE chat result rejection.

Preserve focused deterministic TUI coverage for affected SSE generations, goroutine lifecycles, cancellation, and project isolation.
