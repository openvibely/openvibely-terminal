---
name: openvibely_tui_project
type: project
created: 2026-07-04
updated: 2026-08-16
source: manual consolidation
confidence: high
title: OpenVibely TUI
---

# OpenVibely TUI

Go terminal UI at `/Users/dubee/go/src/github.com/openvibely/openvibely-tui`, built with Bubble Tea, Bubbles, and Lipgloss. It is separate from the Echo backend at `/Users/dubee/go/src/github.com/openvibely/openvibely`, whose default port is `3001`.

## Durable Constraints

- Treat the backend repo as read-only unless the user explicitly re-authorizes changes; do not modify, build, or test it. Missing machine-readable task-board support is a blocked parity gap, not permission to alter backend handlers.
- The primary TUI is a chat window, not a persistent navigation menu/sidebar or slash-command navigation shell. Ordinary chat and slash commands share one input, and command results render into the conversation.
- Slash commands must invoke real backend APIs and support resource/action chains such as `/alerts delete <id-or-name>` and `/skills add <value>`.
- Match web UI features and naming, using the web UI and Swagger as parity sources. Do not expose internal/backend APIs as top-level screens; `Collisions`, `Events`, and `Dashboard` were specifically rejected there.
- Analytics parity requires the web UI's graph coverage; text or table metrics are insufficient where graphs exist.

## Backend Contract

- Machine-oriented REST APIs include `/api/projects`, async chat (`POST /api/chat/message`, then poll `/api/chat/message/:id`), `/api/capacity/*`, `/api/analytics/*`, and other Swagger routes in `cmd/server/main.go`.
- Task, chat, and file-change updates share `GET /events/live` SSE, including `chat_response_done`; the TUI exposes this only as `/events on|off` and appends events inline to chat.
- Optional auth uses `AUTH_ENABLED` and cookie sessions. `POST /login` sets `ov_session`, redirecting to `/login` on failure and `/` on success; `GET /auth/me` reports session state.
- Many non-JSON routes return HTMX/templ fragments. The TUI uses real web routes with HTMX headers and parses stable `data-*` attributes from rendered HTML; this integration depends on those template attributes remaining available.
- `/tasks` uses the backend's HTML/HTMX contract because no JSON task-board endpoint exists. Task board parsing depends on `data-task-*` attributes; titles must come from the card's title-bearing task link, not earlier kebab-menu or child-task/tab links.
- Task detail parsing follows the backend's real `tab-*` panel IDs and separately fetches thread and changes fragments.

## Current State

- `cmd/tui/main.go` supports both the interactive Bubble Tea lifecycle and a one-shot CLI mode. With no positional arguments it starts the TUI; with command arguments it runs the same slash-command registry headlessly, prints results to stdout, reports errors on stderr, and exits nonzero on failure.
- Server, credential, and project selection are available through flags/environment variables; `-project`/`OPENVIBELY_PROJECT` works in both modes.
- `/tasks open <ref>` enters a durable task-thread input mode where plain text posts to `/tasks/<id>/thread`; `/chat` returns to project chat, with `/back` and `/leave` retained as aliases. Changing projects exits task-thread mode. Outside a task thread, `/chat <message>` sends directly to the project agent; inside one, it exits the thread before sending.
- Terminal tables and truncation are display-width aware via Lipgloss so ANSI styling and multibyte characters do not break alignment.
- Project and resource references resolve by exact ID/name, unique prefix, then unique substring. Ambiguous matches are rejected. Explicit `/project <name>` resolution precedes default-project fallback, preventing failed or ambiguous lookups from silently selecting the first project.
- Chained commands cover the 15 authoritative web-sidebar areas: Tasks, Schedule, Alerts, Skills, Agents, Models, Workers, Channels, Personality, Pulse, Reflection, Grades, Insights, Automations, and Analytics. Utility commands cover Projects, project selection, status, builds, inline SSE events, clear, help, and quit.
- Help is registry-driven and mode-aware: `--help` lists all registered commands, one-shot CLI help uses bare command names, interactive help uses slash-prefixed names, and `help <command>` documents exact per-action syntax. Structural tests require every registered command and advertised action to remain documented.
- Task commands cover list, detail, lifecycle, mutation, ordering, goals, thread replies, attachments, schedules, chaining, activation, sweep, and clear through real backend routes.
- Analytics renders terminal bar/gauge graphs for model usage, success/failure, execution time, frequent tasks, failure patterns, and skill follow-through. Prose-heavy HTML-only pages render backend text where templates expose no structured item attributes.

## Workflow

- Tests live under `internal/client/*_test.go` and `internal/tui/*_test.go`; verify source edits with `go build ./...`, `go vet ./...`, and `go test ./...`.
- An opt-in live backend integration test runs when `OPENVIBELY_LIVE` is set and validates the HTML contract against the server on `localhost:3001`.
- Rebuild `bin/openvibely-tui` with `make build` or `make run` before manual checks; a stale binary previously obscured source changes.
