---
kind: openvibely.agent_skill
version: 11
skill:
    key: openvibely_backend_client
    name: OpenVibely Backend Client Integration
    scope: project
    description: API integration and repository-boundary guidance for Go clients, CLIs, and TUIs that consume the OpenVibely backend.
---

# OpenVibely Backend Client Integration

Use this skill when building or extending a Go client, CLI, or TUI that talks to the OpenVibely backend (`github.com/openvibely/openvibely`). Verify current routes and payloads from backend source or generated Swagger before relying on these patterns.

## Backend Integration

- The Echo HTTP server defaults to port `3001`. Machine-facing JSON includes `/api/projects`, asynchronous chat at `/api/chat/message`, capacity and analytics endpoints, plus current routes documented in Swagger.
- For asynchronous chat, POST returns `201` with an ID; poll `GET /api/chat/message/:id` until `completed`, `failed`, or `cancelled`.
- Consume real-time task, chat, and file-change events from `GET /events/live`. Use context cancellation and capped reconnect backoff; treat SSE completion as an acceleration signal rather than replacing polling.
- Authentication is optional and cookie-session based. `POST /login` redirects and sets a cookie; inspect redirect location and cookie state. `GET /auth/me` reports session state. Treat redirects or `401` as authentication required.
- There is no general Go client SDK in the backend repository. Mirror machine-facing request and response structs field-for-field from current Swagger or handler source.

## HTML And HTMX Routes

- Do not assume `Accept: application/json` makes web routes JSON. Confirm a handler has an explicit JSON branch such as `wantsJSON(c)`; `/tasks` can return HTML even with a JSON Accept header.
- For a web-only resource, inspect `internal/handler/handler.go`, its handler, and the corresponding `web/templates/**/*.templ` file. Reuse stable semantic attributes such as `data-task-id`, `data-task-status`, `data-task-category`, `data-alert-id`, or `data-skill-handle` rather than CSS classes or visual text layout.
- Implement HTML transport and parsing in the client package, not in command handlers. Parse with `golang.org/x/net/html`, preserve entity IDs from `data-*` and `hx-*` attributes, and render prose-heavy pages as cleaned text only when no structured attributes exist.
- Do not derive a card title from the first text line: menus and controls may precede content. Select the title-bearing element structurally, prefer semantic attributes such as `title`, exclude action/deep links such as `tab=`, and ensure nested child links cannot replace a parent title.
- Verify task-detail selectors from current templates. Current task panels use `tab-details`, `tab-chat`, `tab-schedules`, `tab-chaining`, `tab-attachments`, `tab-lifecycle`, and `tab-changes`; do not invent panel IDs from labels.
- Match browser mutations exactly: verify HTTP method, route, query parameters, form fields, expected status, and whether the handler branches on `HX-Request: true`. Send the HTMX header when needed to receive a successful fragment/no-content response instead of a redirect.
- Treat backend template attributes as a compatibility contract that can drift. Add fixture tests copied from representative current markup, including controls before titles and nested child links, plus dispatch tests that assert exact method, path, query, form, and HTMX headers.
- When a local backend is available, run an opt-in live parser check against representative tasks, all task tabs, and alerts. Keep deterministic fixture tests as the normal suite and skip live checks unless explicitly enabled.

## Repository Boundary

- Resolve and record the client and backend repository roots before editing. If changes are limited to the client repository, do not edit, generate files in, build, or test the backend repository.
- Keep client code dependent only on backend behavior that already exists when backend changes are out of scope. Missing machine-readable support is a parity constraint, not permission to add backend endpoints.
- Before resuming interrupted work, re-read the latest scope and inspect `git status --short` in every in-scope repository. If a directory is not a Git worktree, report that fact rather than assuming it is clean.

## Web UI Parity

1. Inventory actual navigation from `web/templates/layout/sidebar.templ`, route registration, handlers, templates, and sub-screens.
2. Cross-check each visible feature against Swagger and handler source.
3. Classify each feature as native JSON, HTML/HTMX parsing, or blocked pending backend support.
4. Preserve feature parity without copying web navigation chrome.
5. Do not promote API-only concepts into top-level resources. Collisions, raw events, and a generic dashboard are not parity targets unless current navigation proves otherwise.
6. Account for tasks and task details, thread, changes, schedules, chaining, attachments, lifecycle, grades, pulse, reflection, alerts, skills, agents, models, workers, channels, personality, automations, insights, and full analytics.

## Chat-Centric TUI

- Make the primary TUI one chat transcript and one always-available message/command input. Ordinary text sends chat; input beginning with `/` invokes local command dispatch. Do not substitute a command palette, tab set, sidebar, or separate resource screens for this interaction model.
- Render command results and API errors into the same transcript. Bare resource commands list or summarize resources; chained verbs perform actions, for example `/alerts delete <id>` or `/skills add <name> | <body>`.
- For task-thread focus, use `/tasks open <ref>` to enter and `/chat` to return to project chat. Keep `back`/`leave` only as compatibility aliases when useful, show the active task in the header/input placeholder, route ordinary text to `POST /tasks/:id/thread`, and keep slash commands available. Failed entry must not change mode, and project switching must leave task-thread mode.
- Make `/chat <message>` leave task-thread focus before sending to the project agent so a message cannot accidentally land on the task.
- Route every command through real backend client methods. Resolve human references in ordered tiers: exact ID or exact case-insensitive name, then unique ID/name prefix, then unique case-insensitive name substring. Evaluate all candidates within a tier and list them instead of selecting the first match when ambiguous.
- When an explicit project reference requires an asynchronous project-list load, resolve it before applying any default-first-project fallback. A missing or ambiguous explicit reference must leave the selection unchanged rather than silently selecting the first project.
- Provide `/help` and suggestions/completion. Parse quoted arguments or an explicit delimiter where free-form bodies are needed, validate arity, and show command-specific usage for incomplete input.
- Test ordinary-chat versus slash-command routing, nested commands, unknown and incomplete commands, ambiguous references, backend failures, transcript rendering, exact endpoint dispatch, and task-thread enter/exit/routing/failure behavior. For reference resolution, include reversed list order, exact-name-versus-longer-name, prefix-versus-substring, ambiguity, and deferred-load cases.

## Headless CLI

- Reuse the TUI command registry and model rather than maintaining a second command implementation. Execute the selected command, feed each returned Bubble Tea command message back through `Update`, and continue until the operation settles; this preserves async chat polling and multi-step command behavior.
- With no positional arguments, start the interactive TUI. With positional arguments, run one command, print results to stdout, errors to stderr, and return a nonzero exit code on failure. Accept commands with or without the leading slash.
- Resolve an explicit project before command execution and fail on missing or ambiguous references instead of selecting the default project. Keep local help usable without a running backend.
- Treat help as part of the command contract: top-level `--help` must list every registered command, while `help <command>` must show exact per-action syntax and arguments, not only action names. Render slash-prefixed forms in TUI help and bare subcommands in CLI help.
- Derive help from the shared registry and add structural tests that every registered command is listed, every advertised action has a syntax line, and every advertised action is accepted by dispatch. Prefer registry-driven assertions over hardcoded command lists so newly added commands cannot silently become undiscoverable.
- Keep long action lists readable by wrapping them rather than widening or truncating the terminal table.
- Test result printing, errors and exit behavior, async chat completion, task mutations, unknown commands, requested-project scoping, offline help, and both accepted input forms. Prove project-scoping tests are non-vacuous by temporarily bypassing selection and confirming they fail against the default project's request.

## Terminal Rendering

- Measure styled terminal cells with `lipgloss.Width`, not `len`; ANSI escape sequences and wide Unicode otherwise break table alignment.
- Truncate by display width and rune boundaries so accented, CJK, and styled text is never cut mid-rune or counted by raw bytes.
- Add regression tests that verify visible column starts after stripping ANSI styling and that revert to the old width logic would fail.

## Validation And Delivery

- Run the repository's formatting target, or format only tracked Go files with `git ls-files '*.go' | xargs gofmt -w`; avoid unscoped recursive formatting that can traverse linked worktrees or modify unrelated files. Then run `go build ./...`, `go vet ./...`, and `go test ./...`. Add endpoint, HTML parser, command-dispatch, CLI, and terminal-rendering tests for each changed feature group.
- Run backend validation only when backend edits are in scope.
- Rebuild the shipped binary with `make build` after source changes; stale binaries can hide correct source changes.
- When a backend is already running, smoke-test representative read-only CLI commands, explicit project selection, ambiguity, and unknown-command exit paths against it.
- For large parity work, land and validate one feature group at a time.
