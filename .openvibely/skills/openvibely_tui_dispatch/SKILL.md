---
kind: openvibely.agent_skill
version: 59
skill:
    key: openvibely_tui_dispatch
    name: OpenVibely TUI Dispatch Patterns
    scope: project
    description: Known correctness patterns, pitfalls, and filed bugs for the openvibely-tui internal/tui registry dispatch layer.
---

# OpenVibely TUI Dispatch Patterns

Use this skill when inspecting, debugging, implementing, or testing slash-command dispatch, Bubble Tea model routing, selector behavior, CLI one-shot behavior, command help, or related `internal/tui/` regressions in `openvibely-tui`.

## Scope Boundary

This skill owns `internal/tui/` command, model, selector, rendering, and dispatch behavior. The companion `openvibely_backend_client` skill owns current backend route/payload discovery and client transport contracts; when a change crosses that boundary, load the companion skill rather than duplicating backend-specific rules here.

## Key Files

- `internal/tui/registry.go` — command dispatch switches and selector/confirmation guards.
- `internal/tui/command.go` — command lookup, `splitAction`, `matchRef`, menu refresh.
- `internal/tui/selector.go` — inline selector helpers, key handling, render function, dispatch/prefill logic.
- `internal/tui/messages.go` — selector and model message types.
- `internal/tui/model.go` — Bubble Tea `Update`, key routing, layout/resize, SSE/chat status handling.
- `internal/tui/view.go` — top-level view, help rendering, tables, status and analytics rendering.
- `internal/client/tasks.go` — task-detail tab metadata, aliases, backend panel parsing, and field access consumed by TUI detail commands.
- `internal/tui/selector_test.go` — canonical selector regression tests.
- `internal/tui/dispatch_test.go` — command dispatch and mutation-safety tests.
- `internal/tui/cli.go` and `internal/tui/cli_test.go` — headless CLI execution, `cliMode`/`forceMode`/`jsonMode` behavior.

## Worktree Discipline

Make implementation edits only inside the active task worktree, usually `.worktrees/task_<id>/`. Verify the intended worktree before editing. Do not accidentally edit the main checkout’s `internal/tui/*.go`; if it happens, restore that checkout without disturbing unrelated work.

## Audit And Branch Synchronization

- When a task objective or lifecycle explicitly requires the task branch to be synchronized with `main` or another base branch, verify that requirement during the audit instead of judging only the task commit’s diff. Inspect `git status --short`, `git branch --show-current`, ancestry/ahead-behind output such as `git log --oneline HEAD..main`, and the incoming diff for affected paths such as `git diff HEAD..main -- internal/tui internal/client`.
- Treat missing required base-branch integration or unresolved conflicts in affected files as material audit findings. Ordinary divergence is not itself a defect when synchronization is not part of the objective; report only divergence that violates the stated workflow or requirement.
- Keep an audit-only pass strictly read-only: report the missing synchronization or conflict resolution and stop. Perform merge/rebase/conflict resolution, formatting, and validation only in a separate implementation/fix turn, then run a fresh audit from scratch.
- After required integration fixes, rerun the repository validation from the active worktree (`go build ./...`, `go vet ./...`, and `go test ./...`). When the change touches concurrency, add the narrowest relevant package or tests under `-race`; use `go test -race ./...` only when the behavior crosses package boundaries or the task explicitly requires repository-wide evidence. Then inspect status and the resulting diff before the fresh audit.

## Connection-State Presentation

- When connection-state wording or layout is changed, centralize the ordinary online/connecting/offline decision in one narrow private classifier in `internal/tui/view.go`, and route `renderHeader`, `renderStatus`, and connection-related branches of `hint` through it. Keep authentication-required presentation as a higher-priority override unless the product contract explicitly changes that precedence.
- Preserve the existing truth table: `connected` wins as online; otherwise a completed check or non-empty connection error is offline; before either signal the state is connecting. Do not alter model transition code merely to share presentation logic.
- Preserve intentional hint priority separately from header/status state. In particular, a checked offline state with no diagnostic error may fall through to task-thread or general input guidance, while an offline state with `connErr` shows recovery guidance. Test this distinction rather than making every offline state use the same hint.
- Add table-driven visible-output coverage for initial connecting, successful health, failed health, recovery, repeated failure, and task-thread hints. Assert header/status words, detailed error and recovery rows, exact hint priority, and unchanged ANSI-stripped output; use the existing model health messages so the transition behavior remains covered.

## Project-Scoped Command Guard

Project-scoped commands must not rely on backend fallback scoping when the TUI has no selected project. Before list/read/mutation routes that require a project, require `m.selectedID` to be non-empty or ensure the request explicitly includes the intended `project_id`; otherwise return the same local "no project selected" style error used by `/tasks`, `/schedule`, `/build`, and project worker-limit commands. Cover no-active-project cases in `internal/tui/dispatch_test.go`, especially for destructive or state-changing alert/resource commands.

Apply the same guard to `/alerts` list/read/approve/reject/dismiss/delete/clear and `/automations` list/run-now/pause/resume/delete. Put the guard at the top of each command handler, before `splitAction`, ref parsing, selector setup, or copying `m.selectedID`, and assert zero HTTP requests plus inactive selector/confirmation state in no-project tests.

Apply the same guard to `/pulse`, `/reflection`, `/grades`, and `/insights`, including aliases `/upcoming`, `/history`, and `/suggestions`, across their default and explicit actions (`summary`, `run`, and `analyze` as applicable). Call `m.needProject()` before action parsing or capturing the project ID; with no selection, the handler must append the existing guidance, clear `busy`, return no command, and open neither a selector nor confirmation. In CLI mode, an empty project list leaves `selectedID` empty after startup loading, so the same handler guard must produce a nonzero actionable error before any briefing or analysis request. Use table-driven TUI and CLI coverage for every default/explicit invocation, asserting zero requests when unselected and `project_id` on every selected-project GET/POST while preserving fetched output and trigger/fetch ordering, including trigger-failure short-circuit behavior.

Apply the same guard to `/skills` whenever its list or mutation route can include project-scoped data. The skills page may also show global skills, but an empty project ID lets the backend choose a fallback project; do not treat the presence of a global item as permission to send an unscoped request. Guard `/skills` list/show/add/edit/delete/enable/disable/always/load before `splitAction`, ref parsing, selector setup, confirmation, or copying `m.selectedID`. For regression coverage, use a fresh no-project model and table-test the bare list plus each ref-based action and ref-less destructive action; assert the existing local error, zero HTTP requests, and inactive selector/confirmation state. Include the complete `/skills` action surface in that matrix when the command is project-scoped.

For project-scoped detail or trace commands, carry the selected project through every related request, not only the initial resource list. In particular, `/tasks lifecycle` must pass `m.selectedID` to both lifecycle execution-list and event-trace client methods, and those methods must send `project_id`; the backend uses that query context for ownership checks and may reject a valid task from a non-default project when it is omitted. Add dispatch and CLI coverage with a second selected project and assert `project_id` on every lifecycle request.

For task attachment commands, apply the project guard before task-reference resolution, selector setup, or any attachment request. The shared registry should expose `/tasks attachments add`, `/tasks attachments list`, and `/tasks attachments delete` for interactive and one-shot CLI use, with `attach`/`attachment` compatibility aliases normalized through the same action path. Add accepts one or more local file operands and should render the server-refreshed filename/size list; delete may resolve a stable attachment ID or filename from the selected project's structured attachment list. Keep the existing `/tasks show <task> attachments` tab and all other detail tabs unchanged.

## Ref-Required Selector Pattern

Every TUI slash command with a missing existing-resource ref should open the inline selector instead of returning usage. Headless CLI mode must keep the original usage error and must not fetch selector data just to recover from a missing ref.

Use `selectorOr(m, "usage: /tasks open <id|title>", selectorFor(...))` or `selectorForWithSuffix(...)` as the single branch point:

```go
if ref == "" {
    return selectorOr(m, "usage: /tasks open <id|title>",
        selectorFor("Tasks", "tasks open", "no tasks yet — /tasks new <title> creates one", false, fetchItems))
}
```

For commands that require additional user input after the selected ref, prefill the input instead of dispatching immediately:

- Pipe-delimited suffix `" | "`: `/tasks edit`, `/tasks goal`, `/tasks reply`, `/skills edit`.
- Space suffix `" "`: `/tasks move`, `/tasks order`, `/schedule add`.
- No suffix: actions that can run immediately, such as `/tasks run`, `/tasks stop`, `/tasks delete`, `/alerts approve`, `/models default`, `/automations pause`, `/schedule toggle`.

Keep `selectorActiveMsg` and `Model` state in sync with suffix support: both need the `prefill` boolean plus `prefillSuffix` / `selectorPrefillSuffix`. `selectorDispatch` should build `/command <ref>`, then either dispatch it or prime the input with the exact suffix and make no backend mutation.

When a selector callback already fetched the candidate records, do not immediately issue the same list request again just to resolve the chosen ref. Carry the selected canonical ref or record into dispatch, or reuse the selector lookup result, while preserving exact/prefix/substring and ambiguity semantics. Add request-count coverage for selector selection so a single command does not perform duplicate lookups.

Covered no-arg selector paths are:

- Tasks: `open`, `show`, `reviews`, `edit`, `run`, `stop`, `delete`, `move`, `order`, `goal`, `reply`.
- Schedule: `add`, `delete`, `toggle`.
- Alerts: `read`, `approve`, `reject`, `dismiss`, `delete`.
- Skills: `show`, `edit`, `delete`, `enable`, `disable`, `always`.
- Agents: `delete` only; `show`/`edit`/`enable`/`disable` are not registered agent actions.
- Models: `default`, `delete`.
- Automations: `run-now`, `pause`, `resume`, `delete`.
- Channels: `test`, `remove`.
- Projects: bare `/project` opens a picker only when multiple projects are already loaded in TUI mode.

When adding a new ref-required command, add it to `TestNoArgOpensSelectorPerArea` in `selector_test.go`. If it is destructive, also cover the empty-ref flow in `dispatch_test.go` so the selector/empty-state guard fires before `confirmOr`.

## Selector Pitfalls

Keep selector key handling exclusive for space input. Bubble Tea can set both `Runes` and `KeySpace` for one physical spacebar press. Use mutually exclusive branches so a single press appends exactly one space:

```go
switch {
case msg.Type == tea.KeySpace:
    m.selectorFilter += " "
case msg.Type == tea.KeyRunes:
    m.selectorFilter += string(msg.Runes)
}
```

Do not duplicate the selector usage hint in `renderSelector`; `View()` renders it once through `m.hint()` while `selectorActive` is true.

Keep selector layout resize-aware. Centralize transcript height in `transcriptHeight()` and have `handleSelector`, `clearSelector`, and `resize()` all use it. Normal layout reserves 5 rows. Active selector layout reserves 14 rows, covering header/chrome plus title, filter, up to 8 items, overflow footer, hint, and margin. `WindowSizeMsg` must preserve the selector reservation while active and restore normal height only after `clearSelector()`.

## Dispatch Correctness

Guard after parsing, not before. For pipe-based commands, call `splitPipe` or `strings.SplitN` first, trim the parsed name/title/ref segment, then reject blanks. This prevents inputs like `/tasks edit some-task | | prompt` or `/skills add | desc` from slipping through.

Use `matchRef` consistently for existing-resource refs: exact ID/name, then unique prefix, then unique substring; ambiguous matches must error rather than guess. `/skills edit` must resolve via `ListSkills` plus `matchRef`, then pass the canonical `s.Handle` together with the existing `s.Name`, `s.Description`, and `s.Enabled` to `UpdateSkill` while replacing only the body text. Add a contract test with non-empty metadata and a disabled skill to ensure the backend cannot clear metadata or silently re-enable it when the user edits body text.

Preserve resource scope end-to-end for skills. A resolved `client.Skill` carries `s.Scope`; pass that value to every existing-skill mutation instead of hardcoding `project`. JSON edit, enable/disable, and always-use writes must send the actual scope, and delete must include the backend-required scope query. Add a global-skill dispatch/client test that verifies the global scope reaches the request and a project-skill case that preserves the existing behavior; request-shape checks should be paired with persisted-state or handler-root assertions.

When a chained command accepts both a free-form task ref and typed operands, find a structural operand boundary instead of assuming a fixed arity. For example, `/tasks reviews add <task> <file>:<line> <comment>` should locate the first valid `file:line` token so multi-word task titles remain resolvable.

Interactive command input is tokenized by the shared parser, so compare tokenizer behavior with quoted examples in help or documentation before trusting multi-word refs. A `strings.Fields` tokenizer preserves quote characters rather than treating them as grouping syntax; consequently a documented ref such as `"Fix login bug"` can be looked up literally and fail to match the stored title `Fix login bug`, including in `/tasks reviews add` where the file/line token and free-form comment already create a structural boundary. Prefer a quote-aware tokenizer or change the documented syntax consistently, and add regression coverage for quoted multi-word refs through the actual interactive dispatch path, unquoted refs, and malformed or unmatched quotes as appropriate.

Keep the parser split explicit: interactive `Model.runCommand` may use a quote-aware tokenizer, but headless CLI dispatch must continue passing the pre-tokenized `strings.Fields` result so shell argument handling remains unchanged. Treat `|` as ordinary token content for the existing `splitPipe` stage, strip matching single/double quote delimiters only, and fail with a clear parse/usage error before command lookup when quotes are unmatched. Cover both tokenizer units and real dispatch, including exactly one mutation request for quoted review commands.

For `/schedule add`, parse repeat-type trailing numeric intervals only after identifying the repeat type, then validate intervals against the backend `1..365` rule before resolving task refs or creating a schedule. Invalid `0`, negative, and above-bound intervals should return a clear local error and make no `/tasks/<id>/schedule` POST; dispatch tests should cover no mutation for invalid inputs and exact `repeat_interval` form values for valid inputs such as `seconds 1`, `minutes 15`, and `hours 4`.

For `/tasks show <task> <tab>`, treat task detail tabs as shared client metadata, not TUI-local strings. The canonical names, display labels, backend panel IDs, aliases such as `chat`, `diff`, `schedule`, `chain`, and `attach`, and field accessors live together in `internal/client/tasks.go`; `registry.go` command recognition, `view.go` full-detail sections, and help hints should delegate to that source. When adding or renaming a tab, update focused coverage so `TaskDetail.TabText` and trailing command-tab stripping accept the same vocabulary, and renderer/help tests derive expectations from the metadata instead of hard-coded duplicate lists.

### Canonical Action Usage

Keep high-churn action syntax in a small canonical per-action usage representation alongside the command help metadata in `internal/tui/command.go`. Render help and runtime validation or selector usage messages from that same representation; do not copy action literals into `internal/tui/registry.go` error paths such as `errCmd`, `selectorOr`, `taskSelector`, or `taskSelectorWithSuffix`.

Use a prefix-aware formatter or lookup so the canonical action syntax preserves `cmdPrefix` differences between interactive slash commands and headless CLI output. Keep the representation narrowly scoped to action usage and descriptions rather than introducing a general command DSL, and do not change parsing, backend calls, selector dispatch, or unrelated guidance while consolidating strings.

When changing `/help tasks`, `/help schedule`, or `/help skills` actions, keep their existing user-facing syntax stable and add a focused regression test that compares a representative runtime usage error with the canonical help syntax. Test both interactive and CLI prefix forms when `cmdPrefix` is involved, then run the standard build, vet, and test suite.

Destructive commands must always route through `confirmOr` after the missing-ref selector guard. In TUI mode they park a pending confirmation. In CLI mode they require `--force`; without it, return the CLI hint and do not mutate.

For task attachment deletion specifically, retain the normal confirmation path in interactive mode and require `--force` in one-shot CLI mode. Resolve the selected attachment from the project-scoped structured list, send the attachment ID and selected project to the client, and render the server-refreshed list after a confirmed delete. A cancellation or client/server error must issue no success message; an upload must likewise report failure without claiming that files were added. Test add, confirmed delete, canceled delete, force rejection/acceptance, refreshed output, and visible error paths in both dispatch modes.

For text-page mutation commands that should show `status + "\n\n" + refreshed page` after success, use the private `actAndReloadText` helper instead of inlining action/reload logic. Preserve the established behavior that action errors are returned, but post-action page reload errors are swallowed and the command returns only the action status. Cover both successful refresh and reload-failure fallback in `internal/tui/dispatch_test.go` for commands such as `/channels test <ref>` and `/automations pause <ref>`.

For `generateThenFetch`-style commands such as `/pulse summary`, `/reflection summary`, `/grades run`, and `/insights analyze`, keep the trigger callback side-effect-only and put exactly one content read in the fetch callback. Do not let the client trigger method also fetch rendered content, or the command will duplicate page reads. Regression tests should record ordered requests and assert that bare show does not post, trigger failure short-circuits before fetch, and successful trigger performs the expected single read.

When a command has independent backend calls, use the existing `sync.WaitGroup` fan-out pattern and add a timing/concurrency regression test when meaningful. `agents metrics` is the canonical example. If one of the independent calls supplies optional diagnostics for a critical primary result, this fan-out rule takes precedence when latency matters or the contract explicitly requires overlap: start both with dedicated result/error slots and a shared context, wait for both, then return a primary error as fatal while rendering the primary result with a concise fallback on secondary failure. Serialize the optional fetch only when it depends on primary output or skipping it after primary failure is itself part of the command contract.

For `--json` support, only emit real fetched data, ensure scraper-derived structs have snake_case `json:` tags, and add a CLI test that unmarshals and asserts key fields.

## One-Shot CLI Refreshes

- Treat data prefetched by `RunCLI` as authoritative for output rendered synchronously by the command. If the same command schedules a follow-up refresh for interactive mode, suppress only that post-render refresh while `cliMode` is active; preserve the interactive refresh path.
- For status-like commands, regression-test exact per-endpoint request counts, accurate rendered values, and best-effort behavior when alert/task refreshes fail. A failed optional count fetch must not prevent the status block from rendering. Preserve authentication failures from those fan-out requests separately from ordinary count failures so a session-expiry response can transition the model to sign-in-required instead of being silently converted to zero counts.
- Add a delayed-endpoint timing test when duplicate waves are a performance risk. Use independent delayed endpoints and assert one request per endpoint plus completion within a single wave, rather than relying only on output correctness.
- Treat project activation separately in interactive and one-shot modes. `RunCLI` discards its `Model` after printing, so setting `m.selectedID` or appending to `m.projects` only affects the current invocation; it does not persist the active project for a later CLI process. If project creation promises an active project or follow-up commands, either persist selection through a supported backend/config mechanism or print the created ID/name with explicit `-project <id|name>` guidance and test a second invocation. Do not infer cross-invocation persistence from an in-memory success message.

## Active Project State Transitions

- Centralize the shared active-project transition in one model helper and call it from every path that can replace the current project, including explicit `/project` selection, selector-driven selection, deferred selection after project loading, and successful project creation. Keep request/list/output behavior around the helper unchanged.
- Compare project IDs before clearing project-scoped thread state. When the ID changes, clear `threadID` and `threadTitle` and restore the default input placeholder; when the ID is unchanged, update the selected project metadata but preserve the active thread and its placeholder.
- Invalidate older project-load and project-creation responses when an explicit selection succeeds, and reject stale async messages before changing selection, thread state, busy state, project lists, transcript output, SSE scope, or project-bound command output. A fresh valid creation should select and append the backend-owned project, while an old creation or list response must have no visible effect.
- Use a monotonic project epoch in addition to request IDs and payload project IDs. Capture it when launching every non-SSE operation tied to the selected project and propagate it through generic command, selector, confirmation, chat, thread, project-load/create, and status-count message wrappers. Guard those messages before any `busy`, transcript, pending-chat, selector, auth, or project-list mutation; request IDs only order one operation family, and payload scope may be absent or insufficient. When switching projects, clear pending project-bound chat identity, pending confirmation/selector state, and old SSE ownership before installing the new project.
- Reconnect an active SSE stream only after the new project ID is installed, and ensure the stream request carries that ID. Do not duplicate the transition logic in selection and creation handlers or unconditionally clear thread state during a same-ID metadata update.
- If an explicit `/project <name>` arrives before the initial project load completes, the load started for that deferred selection must carry an explicit `startSSE`/retry intent. After resolving and installing the requested project, open the project-scoped SSE stream even when no earlier stream exists; otherwise the explicit load can supersede startup and permanently leave live events disabled. Add a regression that delivers the deferred load, executes its returned reconnect command, and asserts `/events/live?project_id=<selected>`.
- Treat the inverse recovery path as equally important: if the initial or a later project load fails for a non-auth transport reason before any SSE stream exists, preserve a retry/start intent for the next successful project refresh, explicit selection, or successful project creation (`projectCreatedMsg`). A recovered `/projects`, `/project`, or project-creation install path must open the selected project’s scoped stream; `/events on` must not be the only way to recover a stream that was never created. Add a regression for failed load → successful refresh/selection/creation with no prior `sseCancel`, asserting `/events/live?project_id=<selected>`.
- Add focused regression coverage for selection from an old project, same-ID selection, creation from an old project, same-ID creation, stale creation/list delivery, delayed non-SSE command/chat/status/selector/thread results after A-to-B switching, list/creation output modes, and SSE reconnect query scoping. Assert both state fields and observable side effects such as transcript output, request counts, pending-chat preservation/clearing, and `project_id`.

## Authentication Recovery

- Keep authentication-required state distinct from offline state. Route a shared client-level auth signal from `401` and login redirects in health, project, command, chat/thread, and SSE requests into the model’s auth-required state; ordinary connection-refused, timeout, invalid-server, and other transport errors retain offline behavior.
- Register `/login` as a backend-independent recovery action and normalize any aliases through the same dispatch path. Use a two-stage username then password interaction with masked password input; `Esc` must cancel, failed attempts must clear the password field and remain retryable, and credentials or login failure bodies must never enter input history, transcript, status, logs, or surfaced errors.
- Reuse the existing cookie-session login endpoint and jar. After success, retry health and project loading and reconnect SSE without restarting the TUI. Establish the selected project before opening the refreshed stream so the request is project-scoped; do not start an unscoped stream concurrently with project loading or leave duplicate streams after login.
- Treat interactive login transport failures separately from credential rejection. A dial/refused, timeout, invalid-server, or request-execution failure must enter the existing offline recovery state and guidance while leaving the masked password stage retryable; a credential failure must remain generic, clear the password, and stay retryable. Keep a typed or equivalent transport classification that never includes submitted credentials or response bodies.
- When health fans out capacity/health and `/auth/me`, treat capacity reachability alone as insufficient proof that the current session is authenticated. Only a current, successful auth/session confirmation may clear `authRequired` or permit auth-gated SSE recovery; an `authenticated:false` response, an auth endpoint error, or a mixed authorized/unauthorized endpoint result must retain sign-in precedence while preserving any safe data. Add a regression for capacity `200` plus anonymous or failed `/auth/me` after a project `401`, and a separate regression for a genuinely authenticated health result.
- Give every asynchronous operation that can outlive a login/session transition a monotonic auth/session epoch in addition to any per-request ID. Tag health, project-load, command, selector, chat-send/status, and thread messages when their errors or results can mutate model state; advance the epoch before starting a login retry or invalidating a session, and reject stale messages before changing state or calling `markAuthRequired`. A request token only orders requests of one kind and does not prevent a late pre-login `401` from re-entering sign-in-required state after a successful login.
- Cancel and invalidate the active SSE stream when `/login` begins, not only after login succeeds. A queued auth-required disconnect from the old stream must not advance the session epoch or reject the in-flight login result, which would leave `loginSubmitting` stuck and prevent retry or `Esc` cancellation. Test the ordering by starting login, delivering the old stream’s auth failure, then delivering the login result and asserting the form remains usable.
- Suspend or ignore recurring `tickMsg` health checks while `loginActive` is true, or otherwise ensure they cannot advance the connection/session epoch or call `markAuthRequired` against the in-flight login. A tick that reports an old auth failure must not reject the current `loginResultMsg`, leave `loginSubmitting` stuck, or disable `Esc`; add a regression that delivers a periodic tick during password submission and then delivers the login result.
- Preserve the intent to resume an SSE stream from the moment login begins independently of mutable `connected`/offline state. Do not derive that intent from the later `connected` flag: capture whether a selected project had an active stream before login invalidates it, and retain it through login transport failures so cancellation can reconnect. Test the online active-stream → begin login → transport error → `Esc` ordering, including an active stream with `connected=false`, and assert a new project-scoped stream is opened.
- Never clear `authRequired` merely because one project or data request succeeded. Concurrent endpoints can return mixed authorized/unauthorized results, so a project-load success may retain useful project data but must not clear sign-in guidance or start/restart SSE unless it belongs to the current auth/session epoch and a current health/login transition has established the session. Preserve auth-required precedence until the current-session retry has been accepted.
- Do not erase known `authRequired` state merely because the login request itself failed at the transport layer. Keep sign-in precedence across the temporary offline presentation until a current successful auth/session confirmation establishes the session; a later capacity-only success or anonymous/unavailable `/auth/me` result must not render the model online. Add the exact regression ordering: protected session → `/login` transport failure → health capacity success plus non-authenticated or failed `/auth/me`.
- On a non-auth project/data transport failure after a previously healthy check, clear `connected` and set the offline error state before appending or rendering recovery guidance. If `authRequired` is already true, preserve it rather than unconditionally clearing it; temporary offline presentation must not erase sign-in precedence. Keep header and `/status` precedence consistent with the model state so the transcript cannot say offline while the visible status still says online.
- Apply the same offline transition to every non-health asynchronous transport error that can reach the model, including generic command results, chat-send failures, chat-status failures, thread loads, and selector fetches. These paths must not merely append a raw error or silently continue polling while leaving a stale online header; clear stale `connected`, set the offline error state, and show the existing recovery guidance. Auth-required errors remain a separate sign-in transition, and a pending chat may remain pending while recovery/retry is offered.
- Treat status-count fan-out as state-bearing asynchronous work, not disposable enrichment. Preserve per-endpoint errors in `statusCountsMsg` (at minimum the typed auth-required error), reject stale count messages by session/project epoch, and route an auth failure through the same sign-in transition as other protected requests while retaining best-effort behavior for ordinary count failures. Cover session expiry during `/status` so stale counts do not leave the UI presenting an online/authenticated session.
- In `View`, `/status`, and connection-state transitions, check auth-required before stale `connected` or offline flags. A delayed unauthorized response must not briefly present the session as connected or replace sign-in guidance with generic server-start advice.
- Test this as an end-to-end model flow with an `httptest` server: unauthorized health/project responses show sign-in guidance, successful masked login reuses the cookie and refreshes health/projects/SSE with `project_id`, failed login is generic and redacted but retryable/cancelable, login transport failure shows offline recovery while preserving the masked retry, and genuine transport failure remains offline. Execute `tea.Batch` subcommands when asserting the resulting HTTP calls or messages. Also deliver an older health result after login or a newer check, a project success racing an unauthorized result, a late pre-login command `401` after login, an online-to-project-transport-failure transition, an initial failed project load followed by recovery, and status-count auth expiry; assert that none can clear auth guidance, resurrect stale state, or leave the header/status falsely online.

## Async Model Message Safety

When launching asynchronous fetches or opening task/project-specific UI state, include enough context in the returned message to prove it still applies when `Update` handles it. If the user can switch projects before the response arrives, carry the originating project ID, task ID, or other stable scope token in the message and ignore stale messages whose scope no longer matches `m.selectedID` or the active resource. Add regression coverage that starts the async action, switches project or active resource, then delivers the old message and asserts no stale transcript, panel, or status update is applied.

Apply the same freshness rule across authentication transitions, not only project switches. A delayed result from before login/logout can be unauthorized or otherwise stale even when its project ID still matches; carry the auth/session epoch on messages that can alter model state and reject it before any handler side effect. Test the ordering explicitly by queuing a pre-login result, completing login and the current-session refresh, then delivering the old result.

Apply this rule to project creation and project-list loads explicitly. Creation messages need a request/generation token so a delayed `projectCreatedMsg` cannot select an older operation after a later `/project` switch. List-load messages need equivalent freshness/scoping protection or a merge policy so a delayed `projectsLoadedMsg` cannot overwrite a newer selection or remove a project created by a later operation. Add out-of-order tests for delayed creation after a project switch and delayed list delivery after creation; assert that stale messages do not change selection/transcript and that the newly created project remains available.

## SSE And Chat Status

For immediate chat completion on SSE `chat_response_done`, keep `pollChat` as the disconnected fallback and use `fetchChatStatus` for the immediate path. If the SSE payload includes completed output, use the fast path to append the agent reply and avoid an extra HTTP call.

Keep SSE project scoping intact: `connectSSE` passes `m.selectedID` to `StreamEvents`; `pickProject` updates `m.selectedID` before reconnecting; and the `chat_response_done` handler ignores non-empty foreign `ProjectID` values before both fast path and `fetchChatStatus`.

Correlate `chat_response_done` with the pending chat before settling any state, but do not assume the initially accepted identifier is always the eventual execution identifier. For an immediately running chat, require a non-empty `ChatEvent.ExecID` to equal the pending execution/message ID. For a queued API chat, the accepted `message_id` may be a queue/thread-input ID; the backend can later promote it to a different execution ID, and polling by the original ID can return a terminal status whose `message_id` is that promoted execution. Track the original queue ID and any verified promoted execution alias, and use the backend status/promotion evidence to accept the transition rather than rejecting it or stopping polling on an ID mismatch. Events with no ID or an unverified unrelated ID must still be ignored and only re-armed on the current stream. Add regressions for direct completion, queued acceptance → promotion → polling completion, queued SSE completion, and unrelated same-project events, asserting pending/transcript/request state for each.

When testing `tea.Batch`, execute the outer command to get `tea.BatchMsg`, then execute each sub-command and assert on the resulting messages or HTTP calls.

## Optional Diagnostic Enrichment

- If a slash command has a useful primary result and optional provider/account diagnostics, dispatch the primary fetch first and only enrich after it succeeds when the diagnostic request depends on the primary result or the command contract intentionally avoids optional work after a primary failure. For independent diagnostic calls with a latency requirement, start both concurrently instead, then preserve the primary renderer/table unchanged and append diagnostics only when the secondary response contains usable data.
- Treat secondary account/analytics transport, decode, and fetch errors as non-fatal. Preserve the successful primary result and give a concise hint to the dedicated diagnostics command instead of returning a misleading command failure.
- Keep the enrichment terminal-native and safe for one-shot CLI output: render plain text, keep provider rows deterministic and single-line, allowlist status/quota/reset/error fields, and omit account-detail fields or credentials. If the command supports JSON, preserve its existing valid schema and key casing.
- Update static command help when the output now includes health information or points to the dedicated command. Test primary-only output, provider errors, multiple providers, secondary-fetch failure, and absence of secrets in addition to the normal success path.

## References

- `references/analytics-usage-limit-rendering.md` — provider/account diagnostic output and optional-enrichment lessons.
- `references/bounded-terminal-truncation.md` — bounded UTF-8/display-width truncation and cache invalidation lessons.
- `references/lifecycle-json-slice-normalization.md` — nil-to-empty lifecycle JSON output and focused regression lessons.
- `references/selector-ref-required-command-audit.md` — command-surface, selector-prefill, and resize audit lessons.
- `references/slash-subcommand-completion.md` — registry-driven slash subcommand completion behavior and pitfalls.
- `references/sse-generation-isolation.md` — SSE lifecycle, generation, cancellation, and project-scope isolation.
