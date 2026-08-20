# OpenVibely TUI

A terminal client for the [OpenVibely](https://github.com/openvibely/openvibely)
backend, built with [Bubble Tea](https://github.com/charmbracelet/bubbletea),
Bubbles and Lip Gloss.

**The whole app is one chat window.** Type a message to talk to the project
agent; start a line with `/` to run a command. Command output is rendered back
into the same conversation, so browsing the task board, approving an alert and
asking the agent a question all happen in one continuous transcript.

No server changes are required — the TUI drives the same HTTP routes the web UI
uses.

```
❯ /tasks
▸ Tasks
  Backlog (2)
    ID        STATUS     TITLE
    a1b2c3d4  · pending  Refactor the handler package  [Goal] [Sonnet]
    e5f6a7b8  · pending  Add integration tests
  Active (1)
    ID        STATUS     TITLE
    c9d0e1f2  ▶ running  Write the release notes       [Codex]

❯ /tasks run release notes
❯ why is that task taking so long?
● agent
  It's waiting on a worker slot — the pool is full (4/4 running).

❯ /tasks open release notes        # enter that task's thread
❯ add a section about the new TUI  # → posted to the task, not the project
❯ /chat                            # → back to project chat
```

It is also a **CLI**: pass a command as arguments and it runs once, prints the
result and exits — no UI, no alt-screen.

```bash
openvibely-tui tasks
openvibely-tui -project demo tasks run "release notes"
openvibely-tui -project demo chat "why is that task taking so long?"
```

## Install and run

```bash
make build          # → bin/openvibely-tui
./bin/openvibely-tui

# or in one step
make run
```

Requires Go 1.21+ and a running OpenVibely server (default `http://localhost:3001`). The TUI does not install or start the backend for you. If startup reports that the backend is unreachable, start or check your local OpenVibely backend, or point the client at a running server with `-server <url>` or `OPENVIBELY_SERVER_URL`.

### Configuration

Flags override environment variables.

| Flag | Env var | Default | Purpose |
|---|---|---|---|
| `-server` | `OPENVIBELY_SERVER_URL` | `http://localhost:3001` | Backend base URL |
| `-user` | `OPENVIBELY_AUTH_USERNAME` | – | Username, when the server runs with `AUTH_ENABLED=true` |
| `-pass` | `OPENVIBELY_AUTH_PASSWORD` | – | Password, when the server runs with `AUTH_ENABLED=true` |
| `-project` | `OPENVIBELY_PROJECT` | first project | Project to select: name, ID or unique prefix |

```bash
./bin/openvibely-tui -server http://192.168.1.20:3001 -user dubee -pass secret
```

When credentials are supplied the client performs a form login against `/login`
and reuses the `ov_session` cookie for every subsequent request.

## Using the chat

| Input | Effect |
|---|---|
| `some text` | Sent to the project agent (`POST /api/chat/message`, polled until it completes) |
| `/command …` | Runs a command; its output is appended to the transcript |
| `/` then typing | Opens the completion menu — `Tab` completes, `↑`/`↓` choose |

| Key | Action |
|---|---|
| `Enter` | Send / run |
| `Tab` | Complete the highlighted command |
| `↑` / `↓` | Command menu selection, or input history when the menu is closed |
| `PgUp` / `PgDn` | Scroll the transcript |
| `Ctrl+L` | Clear the transcript |
| `Esc` | Close the menu, or clear the input |
| `Ctrl+C` | Quit |

Commands take a resource, an optional action, and arguments:

```
/alerts                       list alerts
/alerts delete a1b2           delete one
/skills add notes | writes release notes
/tasks move Refactor active   move a task between columns
```

Tasks, alerts, skills, models, agents and schedules can be referenced by **ID
prefix or by a substring of their name/title** — `/tasks run refactor` works.
Ambiguous references report the candidates instead of guessing.

## Commands

Every screen in the OpenVibely web UI sidebar has a command.

| Command | Aliases | Actions |
|---|---|---|
| `/tasks` | `task`, `t`, `board` | `list`, `open`, `show`, `new`, `edit`, `run`, `stop`, `delete`, `move`, `order`, `goal`, `reply`, `activate`, `sweep`, `clear` |
| `/schedule` | `schedules` | `list`, `add`, `delete`, `toggle` |
| `/alerts` | `alert` | `list`, `read`, `approve`, `reject`, `dismiss`, `delete`, `read-all`, `clear` |
| `/skills` | `skill` | `list`, `show`, `add`, `edit`, `delete`, `enable`, `disable`, `always` |
| `/agents` | `agent` | `list`, `delete`, `generate`, `metrics` |
| `/models` | `model` | `list`, `default`, `delete`, `capacity` |
| `/workers` | | `show`, `limit <n>`, `project <n>` |
| `/channels` | `integrations` | — |
| `/personality` | | `show`, `set <preset>` |
| `/pulse` | `upcoming` | `show`, `summary` |
| `/reflection` | `history` | `show`, `summary` |
| `/grades` | | — |
| `/insights` | `suggestions` | `show`, `analyze` |
| `/automations` | | — |
| `/analytics` | `stats` | `usage`, `rates`, `agents`, `frequent`, `failures`, `skills`, `trends` |
| `/projects` | | — |
| `/project <name>` | | select the active project |
| `/status` | `health` | connection, auth, worker capacity, stream state |
| `/build` | | trigger an autonomous build |
| `/events` | `stream`, `log` | `on` / `off` — live SSE feed inline |
| `/clear` | | clear the transcript |
| `/help` | `?`, `commands` | `/help <command>` details one |
| `/chat` | `back`, `leave` | return to project chat; `/chat <message>` also sends it |
| `/quit` | `q`, `exit` | |

### Task threads

By default, plain text goes to the **project agent**. To talk to a specific
task instead, open its thread:

```
/tasks open refactor          enter the thread for that task
please also update the docs   → posted as a follow-up on that task
/chat                         return to project chat
```

While a thread is open the header shows `project ▸ task title`, the prompt
placeholder names the task, and every non-command line is posted to
`POST /tasks/:id/thread` rather than to the project chat endpoint. Slash
commands still work normally inside a thread. Switching projects with
`/project` leaves the thread automatically.

### Task detail tabs

`/tasks show <ref>` prints every populated tab. Append a tab name to see just
one:

```
/tasks show refactor thread
/tasks show refactor changes
```

Tabs: `details`, `thread`, `changes`, `schedules`, `chaining`, `attachments`,
`lifecycle`.

### Analytics

`/analytics` renders everything; a section name narrows it. Charts are drawn as
ASCII bars — usage by model with cost and share, success/failure gauges per
period, average execution time by agent and by task, most frequent tasks,
recurring failure patterns, and skill usage with follow-through rates.

## CLI mode

Anything you can type in the chat window can be run as a one-shot command:

```bash
openvibely-tui tasks                              # print the board
openvibely-tui -project demo tasks show refactor  # a task's detail tabs
openvibely-tui -project demo tasks run refactor   # run it
openvibely-tui -project demo alerts               # list alerts
openvibely-tui -project demo analytics usage      # one analytics section
openvibely-tui -project demo chat "ship the docs" # ask the agent, print the reply
openvibely-tui help                               # list every command
openvibely-tui help tasks                         # full syntax of one command
openvibely-tui --help                             # commands + flags
```

The leading `/` is optional, so a line copied from the TUI works as-is
(`openvibely-tui /tasks`). Output is plain text suitable for piping.

### Discovering commands

| Where | Shows |
|---|---|
| `--help` / `-h` | every command with a one-line description, plus the flags |
| `help` (or `/help` in the TUI) | every command with its full action list |
| `help <command>` | the concrete syntax of each of that command's actions |

`help <command>` is the one to reach for — the action *names* alone don't tell
you the argument order:

```
$ openvibely-tui help tasks
  tasks [filter]                             list the board, optionally filtered
  tasks open <task>                          enter the task's thread
  tasks show <task> [tab]                    details, thread, changes, schedules, …
  tasks new <title> [| <prompt>]             create a task
  tasks edit <task> | <title> [| <prompt>]   edit title/prompt
  tasks run|stop|delete <task>               run, cancel or delete
  tasks move <task> <backlog|active|completed>
  tasks order <task> <position>              reorder within its column
  tasks goal <task> <objective>              set a goal ("clear" removes it)
  tasks reply <task> | <message>             post to the task thread
  tasks activate                             activate the whole backlog
  tasks sweep                                sweep finished tasks
  tasks clear <backlog|completed>            clear a column
```

Help is written in the form you invoke it: `/tasks` inside the chat window,
bare `tasks` on the command line.

Notes:

- `-project` accepts a name, ID or unique prefix. An unknown or **ambiguous**
  reference exits non-zero and lists the candidates rather than running against
  the wrong project. Without it the server's first project is used.
- Chat and long-running commands block until the backend finishes, then print
  the result.
- Errors go to stderr with a non-zero exit status; results go to stdout.
- `help` works with no server running.

## How it talks to the backend

The OpenVibely server exposes two kinds of routes, and the client uses both.

**JSON** (documented in the server's `docs/swagger.json`):

| Area | Endpoints |
|---|---|
| Chat | `POST /api/chat/message`, `GET /api/chat/message/:id` |
| Projects | `GET /api/projects` |
| Capacity | `/api/capacity/global`, `/projects`, `/models` |
| Analytics | `/api/analytics/usage`, `success-failure-rates`, `avg-execution-time-by-{task,agent}`, `most-frequent-tasks`, `failed-task-patterns`, `skills` |
| Workflows | `/api/workflows/metrics`, `best-agent`, `cheapest-agent` |
| Autonomous | `POST /api/autonomous/trigger` |
| Lifecycle | `/api/tasks/:id/lifecycle-executions`, `/api/lifecycle-executions/:id/events` |
| Schedules | `POST /api/schedules/:id/toggle` |
| Auth | `POST /login`, `GET /auth/me` |
| Events | `GET /events/live` (SSE) |

**HTML/HTMX** — the task board, alerts, skills, models, agents, schedule,
workers, channels, personality, pulse, reflection, grades, insights and
automations screens are served as templ-rendered fragments with no JSON
equivalent. For these the client:

- sends `HX-Request: true`, so the server returns a fragment and a 2xx status
  instead of a browser redirect;
- reads structured data from the `data-*` attributes the templates already emit
  (`data-task-id`, `data-task-status`, `data-task-category`, `data-alert-id`,
  `data-skill-handle`, `data-model-id`, `data-agent-id`, `data-schedule-id`, …);
- falls back to the rendered text of a known container element for
  prose-oriented screens.

Mutations post to exactly the routes the web UI posts to (e.g.
`POST /tasks/:id/run`, `PATCH /tasks/:id/category`, `DELETE /alerts/:id`,
`POST /skills/:handle/enabled`, `POST /models/:id/set-default`).

Because this layer depends on the server's markup, a template change that
removes a `data-*` attribute will show up as an empty list rather than a crash.

## Reliability

- **Live events** stream from `/events/live` with automatic reconnect and
  exponential backoff (1s → 30s cap); the header shows the stream state.
- **Connection health** is re-checked every 30s; the header switches to
  `● offline` and `/status` explains why.
- **Chat** is asynchronous end to end: the send is accepted with a message ID,
  then polled; server-side queueing behind an active turn is reported inline.
- **Errors** from any command are printed in the transcript rather than
  discarded, including auth failures ("server auth enabled; provide
  credentials") and server error payloads.
- **Shutdown** cancels the SSE context and restores the terminal.

## Layout

```
cmd/tui/main.go              entry point: flags/env, optional login, program lifecycle
internal/client/
  client.go                  base client, auth, projects, chat, capacity
  api.go                     remaining JSON endpoints (analytics, workflows, lifecycle…)
  html.go                    HTML transport: getHTML, doForm, data-* card scraping
  tasks.go                   task board + task detail tabs + task mutations
  resources.go               alerts, skills, models, agents, schedule, workers,
                             channels, personality, pulse, reflection, insights
  htmltext.go                HTML → text helpers
  sse.go                     /events/live stream client
internal/tui/
  model.go                   chat model: transcript, input, history, SSE, polling
  command.go                 command type, registry lookup, completion, arg helpers
  registry.go                every command and the backend calls behind it
  view.go                    chrome + all result renderers (tables, ASCII charts)
  messages.go                async message types
  styles.go                  Lip Gloss styles
```

## Development

```bash
make test         # go test ./...
make test-cover   # with coverage
make vet          # go vet ./...
make build
```

Tests cover the HTML scrapers and JSON client against `httptest` servers, the
chat update loop (history, menus, chat polling, SSE backoff), the renderers,
and an end-to-end dispatch suite asserting that each slash command issues the
expected HTTP method and path.

HTML fixtures mirror the real templ markup (including the kebab menu that
precedes a card's title), because simplified fixtures hide scraping bugs.

To additionally verify the scrapers against a **running** server:

```bash
OPENVIBELY_LIVE=http://localhost:3001 go test ./internal/client -run TestLiveBackend -v
```

That test is skipped unless `OPENVIBELY_LIVE` is set, so ordinary runs stay
hermetic. It asserts that scraped tasks and alerts have real titles rather than
UI chrome text.
