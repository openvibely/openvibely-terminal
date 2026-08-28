---
name: openvibely_tui_project
type: project
created: 2026-07-04
updated: 2026-08-27
source: update_memory
source_id: f9225dfc4125507a8163724b08e8908e:425f4e9cb3831ffa
confidence: high
title: OpenVibely TUI
---

# OpenVibely TUI

Go terminal UI at `/Users/dubee/go/src/github.com/openvibely/openvibely-tui`, built with Bubble Tea, Bubbles, and Lipgloss. It is separate from the Echo backend at `/Users/dubee/go/src/github.com/openvibely/openvibely`, which normally serves on port `3001`.

## Durable Constraints

- Treat the backend repository as read-only unless the user explicitly re-authorizes changes; do not modify, build, or test it.
- The primary TUI is a chat window, not a navigation shell. Chat and slash commands share one input, and command results render in the conversation.
- Slash commands call real backend APIs and support resource/action chains such as `/alerts delete <id>` and `/skills add <value>`.
- Match web UI features and naming, using the web UI and Swagger as parity sources. `Collisions`, `Events`, and `Dashboard` are not top-level screens.
- Analytics parity requires terminal graph coverage wherever graphs exist in the web UI; text/table metrics alone are insufficient.
- Destructive commands require `⚠ Type 'yes' to confirm or Esc to cancel` in the TUI, or `--force`/`-f` in CLI mode.

## Backend Contracts

- Important routes include `/api/projects`, async chat (`POST /api/chat/message`, then poll `/api/chat/message/:id`), `/api/capacity/*`, `/api/analytics/*`, and the Swagger routes registered by backend `cmd/server/main.go`.
- Task, chat, and file-change updates arrive through `GET /events/live` SSE, including `chat_response_done`. The TUI exposes this as `/events on|off` and scopes the stream to the selected project with `?project_id=`.
- Authentication uses `AUTH_ENABLED` and cookie sessions: `POST /login` sets `ov_session`, while `GET /auth/me` reports session state.
- Many routes return HTMX/templ HTML fragments rather than JSON. Use the real web routes with HTMX headers and parse stable `data-*` attributes.
- Duplicate-card normalization is centralized in `internal/client/html.go` via `dedupeScrapedCards`: empty markers are skipped, first-seen order is preserved, and richer duplicate data wins. `dedupedCards` and `dedupedCardsWithoutText` share this policy; the unused `dedupeCards` implementation is not part of the active path.
- `/tasks` is an HTML/HTMX contract, not a JSON board endpoint. Task parsing depends on `data-task-*`; titles come from the card-title task link. Task detail uses backend `tab-*` panel IDs plus separate thread and changes fragment requests.
- Skill mutations use backend-compatible JSON and preserve HTMX/Accept headers. Create sends `handle`, `name`, `description`, `scope`, and `body`; edit sends existing metadata and enabled state plus the replacement body; enabled and always-use writes send their boolean field plus `scope`; deletion remains form-based with its required `scope` query. Resolved scope must flow through every mutation.
- Shared HTML/form mutation transport accepts only `2xx` responses. `401` and redirects whose parsed path is exactly `/login` are unauthorized without following the redirect or exposing its body; other redirects are ordinary errors. Valid `200` fragments and `204` responses succeed. JSON and chat mutations use the same authentication distinction.

## Current State And Architecture

- `cmd/tui/main.go` supports interactive Bubble Tea mode with no arguments and headless CLI mode with command arguments. Server, credentials, and project are configurable through flags/environment, including `-project` and `OPENVIBELY_PROJECT`.
- `--json`/`-json` emits machine-readable JSON with snake_case tags for list/show CLI commands and is ignored interactively. Command areas cover Tasks, Schedule, Alerts, Skills, Agents, Models, Workers, Channels, Personality, Pulse, Reflection, Grades, Insights, Automations, Analytics, Projects, status, builds, SSE events, clear, help, and quit.
- `/projects create <name> <path>` works interactively and one-shot, including pipe-delimited input for names or paths containing spaces. Interactive creation selects the backend-created project and refreshes scoped SSE; CLI creation reports failures nonzero and prints the backend ID plus an `openvibely-tui -project <ID> tasks` follow-up because one-shot selection is not persisted. Project list/create responses use monotonic request tokens and ignore stale responses.
- `setActiveProject(client.Project)` in `internal/tui/model.go` is the shared transition for explicit `/project` selection and successful creation. It assigns ID/name, clears `threadID` and `threadTitle`, and restores the default placeholder only when the ID changes; same-ID selection preserves the current thread and placeholder. Project changes invalidate project-scoped command, chat, selector, thread, status, confirmation, project-load, and SSE work through `projectGeneration`.
- Async state is guarded by three monotonic identities: `sessionGeneration` for login/auth transitions, `projectGeneration` for active-project changes, and `sseGeneration` for stream lifecycles. Stale results and foreign-project task/chat payloads are rejected before display, status fetch, completion, or active-project mutation. Auth state is not cleared by a project load unless a current health result confirms `Authenticated:true`; transport failures clear online state while preserving sign-in-required precedence and recovery guidance.
- Live SSE starts only after a project is selected and includes `?project_id=`. Connected, event, disconnected, and reconnect messages carry the stream generation. Login invalidates the current stream before authentication and can restore the prior stream on cancellation; periodic health launches are suppressed while login is active. Health results also carry a request generation, so pre-login and older responses cannot overwrite newer auth state.
- Pending SSE `chat_response_done` events settle only on a non-empty exact `ExecID` match for the original pending input ID or a promoted execution ID established by a session/project-validated status response. Matching events use `CompletedOutput` when present and otherwise fetch status; disconnected mode retains a `1500 ms` polling fallback. The promoted alias resets on project changes, new sends, and terminal completion/failure.
- Project loading is request-mode aware: startup, interactive selection, and headless list-only paths request only `/api/projects`; `/projects` additionally fetches `/api/capacity/projects` and renders running/queued counts. Capacity errors there are non-fatal. Status renders worker rows with project alert/task counts; one-shot CLI status drains one count-refresh wave and skips the post-render refresh, while interactive `/status` keeps its follow-up refresh. Independent key fetches intentionally run concurrently.
- Resource references resolve in ordered tiers: unique exact ID, exact case-insensitive name, unique prefix, then unique substring. Duplicate exact IDs or exact names are rejected with candidate-bearing ambiguity errors; unique exact IDs and names retain precedence. Task and automation dispatch must not issue a request on ambiguity.
- Startup/auth/offline behavior classifies unauthorized `302`/`401` responses from health, project, HTML, JSON, form, and SSE transports as typed auth-required failures while retaining offline guidance for connection-refused, timeout, invalid-server, and other transport failures. Interactive `/login` (also `signin`/`auth`) uses a masked retryable/cancelable password field and the cookie-session flow without persistence or credential output. Credential failures are generic and redacted. `-user`/`-pass` and environment credentials remain supported; `OPENVIBELY_AUTH_PASSWORD` is used only when `-pass` was not explicitly supplied, and static help returns before client creation or authentication.

## Command And UI Coverage

- `/tasks open <ref>` enters durable task-thread input mode; plain text posts to `/tasks/<id>/thread`, and `/chat` returns to project chat. Task reviews use `/tasks/:taskId/reviews` with show/list/add flows, HTMX parsing, empty states, and CLI JSON output.
- Help and slash-command Tab completion are registry-driven. Canonical action usage metadata drives help and validation/selector prompts for `tasks reviews add`, `tasks new`, `tasks edit`, `schedule add`, `skills add`, and `skills edit`. Interactive tokenization supports single/double quotes, removes delimiters, preserves pipe-delimited free text, and rejects unmatched quotes before lookup; headless CLI retains `strings.Fields` behavior.
- Ref-required interactive subcommands without an argument open the inline searchable picker; CLI mode retains usage errors. Selector state, filtering, and wrapped transcript sizing are cached and invalidated on picker/filter/candidate changes and resize. Piped commands prefill `/cmd ref | `, ref-first commands prefill `/cmd ref `. Keep separate Bubble Tea `KeySpace` and `KeyRunes` handling to avoid double spaces. Direct selected-resource callbacks avoid a second list fetch where available.
- `/tasks show <ref> <tab>` strips a trailing valid tab before resolving the task. Detail, thread, changes, schedule, chaining, attachment, and lifecycle views support aliases such as `chat`, `diff`, `schedule`, `chain`, and `attach`.
- `/tasks lifecycle <task> [execution]` and `/tasks logs` list project-scoped lifecycle executions and ordered event details. A sole execution opens automatically; multiple executions list in CLI mode or use a picker; empty executions and empty event responses have explicit states. Plain text includes sequence, timestamp, event type, and compact payload; JSON preserves snake_case API fields and converts nil slices to `[]`.
- Task attachments support project-scoped add/list/delete through `/tasks attachments add <task> <file>...`, `list`, and `delete`, with `attach`/`attachment` aliases. Uploads use repeated multipart `files`; lists expose filename, byte size, and stable IDs. TUI deletion requires confirmation and CLI deletion requires `--force`; cross-project markers are rejected.
- `/grades` is read-only through `GetGrades`; `/grades run` posts to `/history/grade-ideas` then fetches grade content. `/workers limit` and `/workers project` accept `0` as unlimited and share a private form/POST helper. `/schedule add` accepts seconds, minutes, and hours; `hourly` normalizes to a one-hour interval because the backend enum has no `hourly` value.
- Pulse, Reflection, Grades, Insights, Skills, Automations, and Alerts require a selected project before parsing actions, opening selectors/confirmations, or dispatching requests. Skills edits preserve metadata and resolved `Enabled` while replacing only the body; all skill mutations carry resolved scope. Blank post-split arguments are guarded for skills add, tasks new/edit/goal/reply. `/channels` supports list/test/remove, translating Slack removal to `disconnect`; `/automations` supports list/run-now/pause/resume/delete; `/alerts` supports list, bulk, and destructive/ref-based actions.

## Rendering And Performance Decisions

- Connection presentation is centralized in `internal/tui/view.go` through `connectionPhase()`, deriving connecting, online, and offline states for the header, `/status`, and input hint. Auth-required presentation has priority; transport details remain visible in `/status`.
- Analytics renders terminal bar/gauge graphs for model usage, success/failure, execution time, frequent tasks, failure patterns, and skill follow-through. Execution-time sections retain at most the bounded top 12 candidates, sort descending `AvgMs`, preserve earlier-input tie order and caller immutability, and keep independent section errors/output.
- `/models capacity` retains the worker-capacity table and best-effort appends provider/account health, quota percentage, reset time, and provider errors. `/analytics usage` shares primary-first limit normalization with capacity, deduplicates exact primary/secondary duplicates, and never exposes account details or credentials in plain text.
- `internal/tui/model.go` `truncate` uses a parser-backed bounded UTF-8/display-width scan for newline-free inputs. It preserves newline normalization, ANSI, malformed-byte handling, wide/combining Unicode, whitespace trimming, ellipsis, exact-width, and `n <= 0` behavior without mutating decoded lifecycle payloads; however, any newline currently triggers whole-input `strings.ReplaceAll` plus a full rescan, so large newline-bearing previews remain unbounded and the existing newline-free benchmark does not cover that path. Append-only transcript blocks and non-final table widths are cached; caches rebuild on resize, clear, mismatch, or retained-history eviction.

## Resolved Incidents And Safeguards

- Repeated authentication/recovery races were resolved by the session/project/SSE generation model, authenticated-health gating, login-time health suppression, explicit prior-stream resume intent, scoped project recovery, and propagation of status-count auth failures.
- Stale project results, foreign SSE events, delayed chat/status responses, wrong-message completions, and promoted chat execution IDs are now rejected or correlated before state mutation.
- Redirect handling was tightened after near-match `/login` paths and ordinary non-login redirects were misclassified; exact parsed-path matching now distinguishes authentication from normal API errors, and mutation responses are strictly `2xx`.

## Known Findings And Gaps

- Custom assistant personality management exists in the backend and web UI, but the shared terminal registry exposes only `/personality show` and `/personality set`. TUI and CLI users cannot discover or create, edit, or delete custom personalities. Native SDLC suggestion `302456eeaec0c196069e64a3a977197d` is pending human review.
- There is no TUI memory inspection/curation command even though project memory is first-class (`.openvibely/memories`, `MEMORIES.md`, selected memory, and `memory_view`).
- Chat completion finalization is duplicated between the polling `completed` branch and the SSE `chat_response_done` `CompletedOutput` path in `internal/tui/model.go`. A narrow shared reply/state-transition extraction should preserve SSE routing, project guards, polling task-ID annotations, and failure/fallback behavior. Native SDLC suggestion `7e41e381f274a8ec672fbaf9fb83d14a` is pending human review.
- Offline recovery guidance is formatted in both `cmd/tui/main.go` and `internal/tui/model.go`. A narrow shared formatter would prevent drift while preserving configured-credential and interactive recovery behavior. Native SDLC suggestion `547e074527d9167198d663e9e2905264` is pending human review.
- The terminal-text truncation optimization is not fully bounded for newline-bearing input: `truncate` calls `strings.ReplaceAll` over the entire string and rescans after any newline, so large over-limit previews containing newlines still incur input-sized CPU and heap work. The existing large-payload benchmark uses newline-free fixtures and does not cover this path. A fresh strict read-only audit of commit `9e0f4d8` on 2026-08-27 identified this issue; a follow-up fix and newline-bearing benchmark coverage are required.
- `GetTask` lazily fetches thread, changes, and lifecycle tabs concurrently after main detail succeeds but discards lazy-request errors. `tasks show` can render incomplete sections as success; make each lazy failure visible without masking successfully loaded sections and add regressions. Native SDLC bug notification `bcb4063d6d9deb6c79dc43b440bf2a11` is pending human review.

## Workflow Constraints

- Live backend integration is opt-in and validates the HTML contract against the local backend. Stale binaries have previously obscured source changes, so runtime checks must use a fresh build.
- Implementation work must target `.worktrees/<task-id>/` or explicitly change into the intended worktree first. Scoped file tools are rooted at the main repository and can edit the wrong checkout; verify the intended worktree before editing.
