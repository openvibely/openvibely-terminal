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
# With one backend project, project-scoped commands can omit -project.
openvibely-tui tasks
# With multiple projects, provide a name, full ID, or unique ID prefix.
openvibely-tui -project demo tasks run "release notes"
openvibely-tui -project demo chat "why is that task taking so long?"
# Project-independent commands do not need a project reference.
openvibely-tui projects list
openvibely-tui projects create demo /Users/me/src/demo
```

## Install and run

```bash
make build          # → bin/openvibely-tui
./bin/openvibely-tui

# or in one step
make run
```

Requires Go 1.21+ and a running OpenVibely server (default `http://localhost:3001`). The TUI does not install or start the backend for you. If startup reports that the backend is unreachable, start or check your local OpenVibely backend, or point the client at a running server with `-server <url>` or `OPENVIBELY_SERVER_URL`. If the server is reachable but protected, the TUI shows sign-in guidance and `/login` opens an in-terminal masked login form.

### Configuration

Flags override environment variables.

| Flag | Env var | Default | Purpose |
|---|---|---|---|
| `-server` | `OPENVIBELY_SERVER_URL` | `http://localhost:3001` | Backend base URL |
| `-user` | `OPENVIBELY_AUTH_USERNAME` | – | Username, when the server runs with `AUTH_ENABLED=true` |
| `-pass` | `OPENVIBELY_AUTH_PASSWORD` | – | Password, when the server runs with `AUTH_ENABLED=true` |
| `-project` | `OPENVIBELY_PROJECT` | only project, when there is exactly one | Project to select: name, ID or unique prefix; required for project-scoped CLI commands when several projects exist |

```bash
# Interactive and safest for avoiding shell-history/process-list exposure:
./bin/openvibely-tui
# Then enter /login and type the username and masked password.

# For headless CLI runs, prefer environment variables supplied by your secret manager:
OPENVIBELY_AUTH_USERNAME=dubee OPENVIBELY_AUTH_PASSWORD="$OPENVIBELY_PASSWORD" \
  ./bin/openvibely-tui -server http://192.168.1.20:3001 tasks
```

`-user` and `-pass` remain supported for existing scripts, but command-line
arguments can be visible in shell history or process listings. Do not put a
literal password in a command copied into documentation or shared logs.

When credentials are supplied the client performs a form login against `/login`
and reuses the `ov_session` cookie for every subsequent request. In interactive
mode, a reachable server that returns `302` to `/login` or `401` is shown as
**sign-in required**, not offline; use `/login`, enter the username, then enter
the masked password. A failed attempt returns to the password prompt, and `Esc`
cancels without adding credentials to history or the transcript. After a
successful login, health, project data, the selected project, and the SSE stream
are retried without restarting the TUI.

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
/tasks attachments add Refactor ./request.txt ./trace.json
/tasks attachments delete Refactor att-123
/agents votes parallel-step-exec-123     inspect every agent vote
```

Tasks, alerts, skills, models, agents and schedules can be referenced by **ID
prefix or by a substring of their name/title** — `/tasks run refactor` works.
Ambiguous references report the candidates instead of guessing.

## Commands

Every screen in the OpenVibely web UI sidebar has a command.

| Command | Aliases | Actions |
|---|---|---|
| `/tasks` | `task`, `t`, `board` | `list`, `open`, `show`, `reviews`, `lifecycle`, `logs`, `attachments`, `attach`, `attachment`, `new`, `edit`, `run`, `stop`, `delete`, `move`, `order`, `goal`, `reply`, `activate`, `sweep`, `clear` |
| `/schedule` | `schedules` | `list`, `add`, `delete`, `toggle` |
| `/alerts` | `alert` | `list`, `read`, `approve`, `reject`, `dismiss`, `delete`, `read-all`, `clear` |
| `/skills` | `skill` | `list`, `show`, `add`, `edit`, `delete`, `enable`, `disable`, `always` |
| `/agents` | `agent` | `list`, `delete`, `generate`, `metrics`, `votes` |
| `/models` | `model` | `list`, `default`, `delete`, `capacity` |
| `/workers` | | `show`, `limit <n>`, `project <n>` |
| `/channels` | `integrations` | — |
| `/personality` | | `list`, `show <key|name>`, `add`, `edit`, `set <key|name>`, `delete <key|name>` |
| `/pulse` | `upcoming` | `show`, `summary` |
| `/reflection` | `history` | `show`, `summary` |
| `/grades` | | — |
| `/insights` | `suggestions` | `show`, `analyze` |
| `/automations` | | — |
| `/analytics` | `stats` | `usage`, `rates`, `agents`, `frequent`, `failures`, `skills`, `trends` |
| `/projects` | | `list`, `create <name> <path>` |
| `/project <name>` | | select the active project |
| `/status` | `health` | connection, auth, worker capacity, stream state |
| `/login` | `signin`, `auth` | enter username and masked password; retry the session without restarting |
| `/build` | | trigger an autonomous build |
| `/events` | `stream`, `log` | interactive `on` / `off` display toggle; one-shot `events on` foreground monitor |
| `/clear` | | clear the transcript |
| `/help` | `?`, `commands` | `/help <command>` details one |
| `/chat` | `back`, `leave` | return to project chat; `/chat <message>` also sends it |
| `/quit` | `q`, `exit` | |

The interactive TUI and one-shot CLI have different live-event lifecycles:

- In the interactive TUI, `/events` toggles whether the stream owned by that TUI
  is displayed in the transcript. `/events off` hides those events; it does not
  stop the backend stream.
- In a shell, `events on` (or bare `events`) resolves the selected project,
  opens one project-scoped foreground stream, and writes one plain line per task
  or chat event until the server closes the stream or you press `Ctrl-C`:

```bash
openvibely-tui -project demo events on
```

Use `--json` before the command for newline-delimited JSON with stable event
fields, for example `openvibely-tui --json -project demo events on`. One-shot
`events off` cannot turn off a stream owned by another process; it exits nonzero
and tells you to press `Ctrl-C` in that monitoring process. Use interactive
`/events off` when you only want to hide events in the current TUI.

Create a project from the terminal without opening the browser. The backend owns
and persists the project; after a successful creation it becomes the active
project immediately:

```
/projects create demo /Users/me/src/demo
/projects create My Project | C:\Users\me\src\my-project
```

The pipe form separates the name from the repository path when either contains
spaces. The same syntax works in one-shot mode (`openvibely-tui projects create
...`); add `--json` for a machine-readable created-project record.

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
`lifecycle`. Lazy thread, changes, and lifecycle failures are shown as explicit
errors rather than empty tabs; successfully loaded sections remain visible.

### Task attachments

Attachment reads and mutations use the currently selected project. Upload one or
more local files with the same command in the interactive TUI or one-shot CLI:

```
/tasks attachments add refactor ./request.txt ./trace.json
/tasks attachments list refactor
/tasks attachments delete refactor att-123

openvibely-tui -project demo tasks attachments add refactor ./request.txt ./trace.json
openvibely-tui -project demo tasks attachments list refactor
openvibely-tui -project demo --force tasks attachments delete refactor att-123
```

The refreshed attachment list prints each stable attachment ID, filename, and
size. TUI deletion asks for the normal `yes` confirmation; CLI deletion refuses
to run unless `--force` is supplied. A task-only delete invocation opens the
interactive attachment selector, where the selected file is still confirmed
before deletion. `attach` and `attachment` are aliases for `attachments`.

### Personalities

`/personality` without an action keeps the original rendered settings view. Use
`list` to inspect the backend-backed built-in and custom entries, and `show` to
fetch a complete system prompt:

```
/personality list
/personality show release_coach
/personality add "Release Coach" | Keep release advice practical and safe.
/personality add "Release Coach" | description=safe release guidance | Keep advice practical and safe for production.
/personality edit release_coach | Release Coach | updated description | Keep every release reversible and observable.
/personality set release_coach
/personality delete release_coach
```

The add form without a description is `<name> | <system prompt>`; every character after the first separator, including literal `|` characters and a prompt beginning with `description:`, remains part of the prompt. To provide the optional description, use the explicit `description=<description> | <system prompt>` form. The `description=` marker is case-insensitive and reserved for the marked form; it must include non-empty description and prompt fields, and malformed marked fields are rejected before any backend mutation. Editing a built-in creates or updates its backend override. Deleting that built-in resets it to the built-in default, while deleting a custom entry removes it; both operations require the normal TUI
confirmation or `--force` in CLI mode. Add `--json` to list/show and supported
mutation commands for machine-readable records.


```
/tasks lifecycle refactor
/tasks lifecycle refactor exec-123
openvibely-tui -project demo --json tasks lifecycle refactor exec-123
```

`tasks logs` is an alias for `tasks lifecycle`.

### Parallel workflow votes

Inspect the recorded votes for a completed parallel workflow step after selecting
its project. The action is read-only and takes the backend step-execution ID
literally; it makes one request to the vote-record endpoint and prints each
agent configuration, vote, confidence, and a bounded single-line reasoning
value:

```
/agents votes step-exec-123
openvibely-tui -project demo agents votes step-exec-123
openvibely-tui -project demo --json agents votes step-exec-123
```

The plain view names the step execution and shows an explicit no-records state
when the backend returns an empty collection. `--json` emits only the decoded
vote-record array, including `[]` for an empty result, so it can be consumed by
scripts without a human banner or terminal styling. Unknown IDs, authentication
failures, and backend errors retain the normal non-zero command behavior.

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
openvibely-tui -project demo agents votes step-exec-123 # inspect parallel votes
openvibely-tui -project demo tasks attachments add refactor ./request.txt ./trace.json
openvibely-tui -project demo --force tasks attachments delete refactor att-123
openvibely-tui -project demo alerts               # list alerts
openvibely-tui -project demo analytics usage      # one analytics section
openvibely-tui -project demo chat "ship the docs" # ask the agent, print the reply
openvibely-tui projects create demo /Users/me/src/demo # create; output includes its backend ID
openvibely-tui --json projects create demo /Users/me/src/demo # JSON project record
openvibely-tui help                               # list every command
openvibely-tui help tasks                         # full syntax of one command
openvibely-tui --help                             # commands + flags
```

The leading `/` is optional, so a line copied from the TUI works as-is
(`openvibely-tui /tasks`). Output is plain text suitable for piping.
When the backend has exactly one project, project-scoped commands use it when
`-project` is omitted. When more than one project exists, those commands fail
before making a project request and require `-project <name|id>` (a full ID,
name, or unique ID prefix). `projects list`, `projects create`, `help`, `login`,
and other global commands remain usable without a project reference. In this
implicit single-project mode, human output begins with `project: <name> (project_id=<id>)`; `--json` output uses an envelope with `project_id`,
`project_name`, and `data` so scripts can see the selected scope.

Interactive `projects create` selects the new project immediately. In one-shot CLI
mode, the process ends after creation; the plain result prints the backend project
ID and a copyable next step such as `openvibely-tui -project <ID> tasks`. Use that
ID (or the project name/unique prefix) with `-project` for later commands.

### Discovering commands

| Where | Shows |
|---|---|
| `--help` / `-h` | every command with a one-line description, plus the flags |
| `help` (or `/help` in the TUI) | every command with its full action list |
| `help <command>` | the concrete syntax of each of that command's actions |

`help <command>` is the one to reach for — the action *names* alone don't tell
you the argument order:

```
$ openvibely-tui help projects
  projects [list]                              list projects with running/queued counts
  projects create <name> <path>                create and select a local-path project
  projects create <name> | <path>              use | when the name or path contains spaces

$ openvibely-tui help tasks
  tasks [filter]                             list the board, optionally filtered
  tasks open <task>                          enter the task's thread
  tasks show <task> [tab]                    details, thread, changes, schedules, …
  tasks lifecycle <task> [execution]         list executions or show ordered events
  tasks logs <task> [execution]              alias for lifecycle event logs
  tasks attachments add <task> <file>...      upload local files
  tasks attachments delete <task> <attachment> delete by ID or filename
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

- `-project` accepts a name, full ID or unique prefix. An unknown or **ambiguous**
  reference exits non-zero and lists the candidates rather than running against
  the wrong project. A CLI run may omit it only when there are zero or exactly
  one backend projects; project-scoped commands require `-project <name|id>`
  when multiple projects exist. `projects list`, `projects create`, `help` and
  other global commands do not require it.
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
| Projects | `GET /api/projects`, `POST /projects` (HTMX form) |
| Capacity | `/api/capacity/global`, `/projects`, `/models` |
| Analytics | `/api/analytics/usage`, `success-failure-rates`, `avg-execution-time-by-{task,agent}`, `most-frequent-tasks`, `failed-task-patterns`, `skills` |
| Workflows | `/api/workflows/metrics`, `best-agent`, `cheapest-agent`, `votes/:stepExecID` |
| Autonomous | `POST /api/autonomous/trigger` |
| Lifecycle | `/api/tasks/:id/lifecycle-executions`, `/api/lifecycle-executions/:id/events` |
| Schedules | `POST /api/schedules/:id/toggle` |
| Personality | `POST /personality/custom`, `GET/PUT/DELETE /personality/custom/:key` (JSON custom CRUD); `/personality` remains the scoped HTML list |
| Auth | `POST /login`, `GET /auth/me` |
| Events | `GET /events/live` (SSE) |

**HTML/HTMX** — the task board, alerts, skills, models, agents, schedule,
workers, channels, personality list, pulse, reflection, grades, insights and
automations screens are served as templ-rendered fragments with no JSON list
equivalent. Personality custom detail and CRUD mutations use the JSON routes
listed above. For the HTML screens the client:

- sends `HX-Request: true`, so the server returns a fragment and a 2xx status
  instead of a browser redirect;
- reads structured data from the `data-*` attributes the templates already emit
  (`data-task-id`, `data-task-status`, `data-task-category`, `data-alert-id`,
  `data-skill-handle`, `data-model-id`, `data-agent-id`, `data-schedule-id`, …);
- falls back to the rendered text of a known container element for
  prose-oriented screens.

Mutations post to exactly the routes the web UI posts to (e.g.
`POST /tasks/:id/run`, `PATCH /tasks/:id/category`, `DELETE /alerts/:id`,
`POST /skills/:handle/enabled`, `POST /models/:id/set-default`). Task attachment
uploads use repeated multipart `files` parts at `POST /tasks/:id/attachments`,
and deletion uses `DELETE /attachments/:id`; both carry the selected
`project_id` query.

Because this layer depends on the server's markup, a template change that
removes a `data-*` attribute will show up as an empty list rather than a crash.

## Reliability

- **Live events** in the interactive TUI stream from `/events/live` with automatic
  reconnect and exponential backoff (1s → 30s cap); the header shows the stream
  state. One-shot `events on` is a foreground, single-connection monitor and
  exits on `Ctrl-C` or clean server termination.
- **Connection health** is re-checked every 30s; the header switches to `● offline` for transport failures and to `● sign-in required` for a reachable protected backend. `/status` explains the recovery action.
- **Interactive auth** uses `/login` with a masked password field, reuses the cookie session, and retries health, projects and SSE after success.
- **Chat** is asynchronous end to end: the send is accepted with a message ID,
  then polled; server-side queueing behind an active turn is reported inline.
- **Errors** from any command are printed in the transcript rather than
  discarded; auth failures point to `/login` (or the CLI environment/flags) and
  never print passwords or response bodies.
- **Shutdown** cancels the SSE context and restores the terminal.

## Layout

```
cmd/tui/main.go              entry point: flags/env, optional or interactive login, program lifecycle
internal/client/
  client.go                  base client, auth, projects, chat, capacity
  api.go                     remaining JSON endpoints (analytics, workflows, lifecycle…)
  html.go                    HTML transport: getHTML, doForm, data-* card scraping
  tasks.go                   task board + task detail tabs + attachment client + task mutations
  attachments.go              project-scoped multipart upload, HTML parsing, and deletion
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
