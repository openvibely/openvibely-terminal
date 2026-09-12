# OpenVibely Terminal User Guide

For a quick introduction and installation instructions, see the
[project README](../README.md).

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
openvibely-terminal tasks
# With multiple projects, provide a name, full ID, or unique ID prefix.
openvibely-terminal -project demo tasks run "release notes"
openvibely-terminal -project demo chat "why is that task taking so long?"
# Project-independent commands do not need a project reference.
openvibely-terminal projects list
openvibely-terminal projects create demo /Users/me/src/demo
openvibely-terminal --force projects delete demo
```

## Install and run

```bash
make build          # → bin/openvibely-terminal
./bin/openvibely-terminal

# or in one step
make run
```

Requires Go 1.27.1+ and a running OpenVibely server (default `http://localhost:3001`). The TUI does not install or start the backend for you. If startup reports that the backend is unreachable, use `/setup` in the TUI or `openvibely-terminal setup` in a shell for explicit, read-only recovery instructions. If the server is reachable but protected, the TUI shows sign-in guidance and `/login` opens an in-terminal masked login form.

### Backend setup and recovery

`/setup` and `openvibely-terminal setup` are read-only guides. They do **not** install
OpenVibely, clone repositories, start processes, create projects, authenticate,
or modify files or machine state. They work before a backend, project, or session
exists, and show the same platform-specific commands below for you to choose and
run yourself. The backend's [installation guide](https://docs.openvibely.ai/installation)
is authoritative for installation locations, versions, replacement behavior, and
installed-binary launch details.

**macOS and Linux**

```bash
# Explicitly install the OpenVibely server, if you choose to do so.
curl -fsSL https://openvibely.ai/install.sh | bash -s -- --variant binary

# Or, from an OpenVibely source checkout, start the server.
./start.sh
```

**Windows PowerShell**

```powershell
# Explicitly install the OpenVibely server, if you choose to do so.
& ([scriptblock]::Create((irm https://openvibely.ai/install.ps1))) -Variant binary

# A source checkout can be started from a shell that supports it.
./start.sh
```

After the server starts, verify its health with `/status` in the TUI or
`openvibely-terminal status` in a shell. A local server normally listens at
`http://localhost:3001`.

For a remote backend, check its URL before starting a local server, then pass it
explicitly with `-server <url>` or set `OPENVIBELY_SERVER_URL`. The TUI only uses
that setting to connect; it never changes the setting or starts the remote server.

```bash
openvibely-terminal -server https://openvibely.example status
OPENVIBELY_SERVER_URL=https://openvibely.example openvibely-terminal status
```

```powershell
$env:OPENVIBELY_SERVER_URL = "https://openvibely.example"
openvibely-terminal status
```

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
./bin/openvibely-terminal
# Then enter /login and type the username and masked password.

# For headless CLI runs, prefer environment variables supplied by your secret manager:
OPENVIBELY_AUTH_USERNAME=dubee OPENVIBELY_AUTH_PASSWORD="$OPENVIBELY_PASSWORD" \
  ./bin/openvibely-terminal -server http://192.168.1.20:3001 tasks
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
/alerts list --decision-state pending --processing-state unclaimed
/alerts "approved deployment" # free-text matching, not a workflow predicate
/alerts show a1b2             inspect full body and metadata
/alerts delete a1b2           delete one
/alerts read-bulk a1b2 "Release approval"     mark selected alerts read
/alerts delete-bulk a1b2 "Release approval"   delete selected alerts (confirm)
/projects delete demo                  # type yes to confirm, or press Esc
/skills add notes | writes release notes
/skills load notes
/tasks move Refactor active   move a task between columns
/tasks goal Refactor | all checks pass
/tasks goal pause Refactor
/tasks goal resume Refactor
/tasks steer Refactor | stop and use the new interface
/tasks attachments add Refactor ./request.txt ./trace.json
/tasks attachments delete Refactor att-123
/agents edit reviewer description "Reviews Go and SQL" enabled true
/agents votes parallel-step-exec-123     inspect every agent vote
/memory list                             inspect indexed project memory
/memory show managed_memory.md           read one memory file
/memory search "project scoped"         search memory files
/schedule edit a1b2c3 run-at 2026-01-22T10:30 repeat weekly interval 2 clear-context false
/schedule edit "Daily repeat report" repeat daily interval 5
/schedule edit a1b2c3 repeat hourly
```

`/skills always <skill>` and `/skills load <skill>` are equivalent project-scoped
names for marking a skill for automatic loading. The same syntax is available in
one-shot mode by omitting the leading slash and selecting the project explicitly:

```bash
/skills load retry-logic
openvibely-terminal -project demo skills load retry-logic
```

Tasks, alerts, skills, models, agents and schedules can be referenced by **ID
prefix or by a substring of their name/title** — `/tasks run refactor` works.
Ambiguous references report the candidates instead of guessing.

Project deletion is deliberately destructive: `/projects delete <project>` resolves
an exact ID/name or unique prefix/substring, then asks `Type 'yes' to confirm or
Esc to cancel`. One-shot CLI deletion requires `--force` or `-f`. The backend
protects its default project and remains authoritative for that refusal. Deletion
removes the project and all backend-owned project data; after a successful
deletion, the terminal refreshes the catalog and selects the backend-selected
remaining/default project when available.

`/alerts list [filter] --decision-state <state> [--processing-state <state>]`
uses exact backend workflow predicates on the selected project. Valid decision
states are `pending`, `approved`, `rejected`, and `dismissed`; valid processing
states are `not_applicable`, `unclaimed`, `claimed`,
`implementation_task_linked`, `completed`, and `failed`. For example,
`/alerts list --decision-state pending --processing-state unclaimed` isolates
unclaimed approval work. The optional unflagged `[filter]` continues to be a
separate case-insensitive free-text match against alert content: `/alerts
approved deployment` searches those words and does not select the `approved`
decision state.

`/alerts read-bulk <id|title>...` and `/alerts delete-bulk <id|title>...`
resolve every supplied reference within the selected project before making one
atomic bulk request. Quote multiword titles. Repeating a reference, using an
ambiguous or missing reference, or naming an alert outside the selected project
fails without changing any alert. Read bulk refreshes the alert list; selective
delete asks for `yes` interactively and requires `--force` headlessly. In
`--json` mode the commands emit the returned count as `{"updated":n}` or
`{"deleted":n}`.
## Commands

Every screen in the OpenVibely web UI sidebar has a command.

| Command | Aliases | Actions |
|---|---|---|
| `/tasks` | `task`, `t`, `board` | `list`, `open`, `show`, `reviews`, `lifecycle`, `logs`, `attachments`, `attach`, `attachment`, `new`, `edit`, `run`, `stop`, `delete`, `move`, `order`, `goal` (`set`, `clear`, `pause`, `resume`), `reply`, `steer`, `activate`, `sweep`, `clear` |
| `/schedule` | `schedules` | `list`, `add`, `edit`, `delete`, `toggle` |
| `/alerts` | `alert` | `list`, `show`, `read`, `read-bulk`, `approve`, `reject`, `dismiss`, `delete`, `delete-bulk`, `read-all`, `clear` |
| `/skills` | `skill` | `list`, `show`, `add`, `edit`, `delete`, `enable`, `disable`, `always`, `load` |
| `/memory` | `memories` | `list`, `show`, `search` (read-only project memory) |
| `/agents` | `agent` | `list`, `edit`, `delete`, `generate`, `metrics`, `votes` |
| `/models` | `model` | `list`, `add`, `edit`, `default`, `delete`, `capacity` |
| `/workers` | | `show`, `limit <n>`, `project <n>` |
| `/channels` | `integrations`; deprecated: `webhooks`, `inbound-webhooks` | `list`, `show`, `add`, `connect`, `edit`, `test`, `remove`, `disconnect`; `access <telegram\|slack\|discord\|x\|email\|github> list\|add\|remove`; `webhooks list|show|create|edit|test|rotate|delete` |
| `/personality` | | `list`, `show <key|name>`, `add`, `edit`, `set <key|name>`, `delete <key|name>` |
| `/pulse` | `upcoming` | `show`, `summary` |
| `/reflection` | `history` | `show`, `summary` |
| `/grades` | | `show`, `run` |
| `/insights` | `suggestions` | `show`, `analyze` |
| `/automations` | `automation` | `list`, `show`, `open`, `edit`, `run`, `pause`, `resume`, `delete` |
| `/analytics` | `stats` | `usage`, `rates`, `agents`, `frequent`, `failures`, `skills`, `trends` |
| `/projects` | | `list`, `show <project>`, `create <name> <path>`, `edit <project> [options]`, `delete <project>` |
| `/project <name>` | | select the active project |
| `/status` | `health` | connection, auth, worker capacity, stream state |
| `/setup` | | read-only backend installation, startup, health, and remote-connection guidance |
| `/login` | `signin`, `auth` | enter username and masked password; retry the session without restarting |
| `/events` | `stream`, `log` | interactive `on` / `off` display toggle; one-shot `events on` foreground monitor |
| `/clear` | | clear the transcript |
| `/help` | `?`, `commands` | `/help <command>` details one |
| `/chat` | `back`, `leave` | return to project chat; `/chat <message>` also sends it |
| `/quit` | `q`, `exit` | |

### Channel access

Channel access commands require a selected project. Use `channels access github list|add|remove` to manage the GitHub authorized-actor allowlist. The backend stores this allowlist at system level, while every terminal request still carries the selected `project_id` as request context. GitHub logins are normalized by stripping a leading `@` and lowercasing the login. The display name is optional; quote it as one operand when it contains spaces.

```text
/channels access github list
/channels access github add @Alice
/channels access github add @Alice "Release Reviewer"
/channels access github remove @Alice
```

The same grammar is available in one-shot mode. `--json` returns only safe actor identity fields, and no credentials, OAuth state, tokens, permissions, or unrelated backend form values. Add rejects malformed, duplicate, and surplus operands before the POST. Removal resolves one canonical listed actor before confirmation, captures its backend row ID, revalidates that ID in the selected project, then requires `yes` interactively or `--force` headlessly. Cancellation and missing force perform no DELETE. A successful mutation remains successful if the optional refreshed list cannot be loaded.

```bash
openvibely-terminal -project demo channels access github list
openvibely-terminal -project demo --json channels access github list
openvibely-terminal -project demo channels access github add @Alice "Release Reviewer"
openvibely-terminal -project demo --force channels access github remove @alice
```

The same `list|add|remove` access grammar remains available for Telegram, Slack,
Discord, X, and Email; use `help channels` for provider-specific identity forms.

### Model providers

`/models add` and `/models edit <model>` are shared terminal actions for model
providers. `add` configures `anthropic`, `openai`, and `ollama`; run the bare
add action in the interactive TUI to answer guided prompts. API-key prompts are
masked and never enter the transcript or command history.

`edit` reads the backend-authoritative existing configuration, then applies only
the supplied options: `--name`, `--model`, `--default <true|false>`,
`--max-workers`, `--worker-timeout`, and `--endpoint`. `--endpoint` supports
local Ollama and OpenAI-compatible configurations, must be an absolute HTTP(S)
URL without credentials, query parameters, or fragments, and is checked again
by the backend's provider policy. Unspecified credentials and provider-specific
settings remain unchanged.

```text
/models add
```

One-shot API-key configuration or replacement accepts a secret only through
piped or redirected standard input with `--api-key-stdin`. The key must never be
put in an argument, pasted into a command-history entry, or included in `--json`
output. For an interactive edit, use `--api-key` with no value; it opens the
same masked input safeguard and leaves the saved credential unchanged until a
nonempty replacement is submitted.

```bash
printf '%s' "$OPENAI_API_KEY" | openvibely-terminal models add openai "OpenAI" gpt-4o --api-key-stdin
openvibely-terminal models add ollama "Local Ollama" llama3.1:8b --endpoint http://localhost:11434
openvibely-terminal -project demo models edit "Local Ollama" --model llama3.2 --max-workers 2 --endpoint http://localhost:11434
printf '%s' "$OPENAI_API_KEY" | openvibely-terminal -project demo models edit OpenAI --api-key-stdin
```

For `add`, `--endpoint` is only for Ollama. For `edit`, it additionally supports
saved OpenAI-compatible configurations. It must be an absolute HTTP(S) URL
without credentials, query parameters, or fragments. Omit it on add to use the
backend's local Ollama default. `--oauth` is available for Anthropic and OpenAI.
It saves an OAuth configuration, refreshes the model list, then reports the
backend's authorization status and a browser handoff URL. OAuth is connected
only when that backend status is `connected`; terminal setup alone does not claim
completion.

```bash
openvibely-terminal models add anthropic "Claude OAuth" claude-sonnet-4-6 --oauth
```

After a successful addition, choose it with `/models default <name>` as usual.

### Project memory

Inspect the selected project's canonical, repository-local memory index and topic
files without changing them:

```
/memory list
/memory show managed_memory.md
/memory search "provider architecture"

openvibely-terminal -project demo memory list
openvibely-terminal --json -project demo memory search "provider architecture"
```

`/memories` is a compatibility alias for `/memory`. The terminal reads only
files indexed by `.openvibely/memories/MEMORIES.md`; malformed or missing topic
files are shown as safe warnings. The public backend currently exposes no memory
curation route, so edit/delete is intentionally unavailable here. Corrections
and removals remain owned by the backend Memory Curator lifecycle tools.

The interactive TUI and one-shot CLI have different live-event lifecycles:

- In the interactive TUI, `/events` toggles whether the stream owned by that TUI
  is displayed in the transcript. `/events off` hides those events; it does not
  stop the backend stream.
- In a shell, `events on` (or bare `events`) resolves the selected project,
  opens one project-scoped foreground stream, and writes one plain line per task
  or chat event until the server closes the stream or you press `Ctrl-C`:

```bash
openvibely-terminal -project demo events on
```

Use `--json` before the command for newline-delimited JSON with stable event
fields, for example `openvibely-terminal --json -project demo events on`. One-shot
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
spaces. The same syntax works in one-shot mode (`openvibely-terminal projects create
...`); add `--json` for a machine-readable created-project record.

Inspect or update an existing project's backend-owned settings without opening a
browser:

```bash
/projects show "My Project"
/projects edit "My Project" --name "Renamed Project" --description "Local checkout"
/projects edit "Renamed Project" --repository-path "/Users/me/src/repo with spaces"
/projects edit "Renamed Project" --default-agent Builder --max-workers 4

openvibely-terminal --json projects show "Renamed Project"
openvibely-terminal projects edit "Renamed Project" --max-workers inherit
openvibely-terminal projects edit "Release --name Candidate" '|' --description "Local checkout"
openvibely-terminal -- projects edit --json '|' --description "Project named like a global flag"
openvibely-terminal -f -- projects edit --force '|' \
  --repository-source github --github-url https://github.com/acme/repo
openvibely-terminal --force projects edit "Renamed Project" \
  --repository-source github --github-url https://github.com/acme/repo
```

`projects show` and successful `projects edit` JSON use stable snake-case fields:
`id`, `name`, `description`, `repository_source`, `repository_path`, `github_url`,
`default_agent_id`, optional `default_agent_name`, `max_workers`, and
`local_repository_paths_enabled`. If saving succeeds but that authoritative refresh
fails, JSON instead returns `{"saved":true,"project_id":"…","refresh_error":"saved; authoritative refresh failed"}`.
Edit options omitted from the command retain
the authoritative existing values. `--repository-path` is valid only when the
effective source is `local`, and `--github-url` only when it is `github`; include
`--repository-source` in the same edit when switching modes. If a project name
contains complete option-like words or pairs, put a standalone `|` between the
complete project reference and its edit options, as shown above (quote or escape
it in a shell); equally strong boundaries fail as ambiguous rather than shortening or rebinding the target.
In one-shot mode, if the project name itself is a registered global flag such as
`--json`, `--force`, or `--project`, put the standard outer `--` before `projects`
so the name reaches command parsing: `openvibely-terminal [global flags] -- projects
edit --json '|' --description changed`. Put real global flags, such as `-f`,
before that outer boundary.
`--default-agent inherit` uses the global
default and `--max-workers inherit` (or `0`) removes the project limit. Local
paths may be Unix, Windows drive, UNC, or space-containing paths; quote one shell
argument as shown above.

Changing to a different GitHub repository re-clones into managed storage and can
replace that checkout. Interactive mode displays a destructive warning and
requires typing `yes`; `Esc` cancels. One-shot mode exits nonzero unless
`--force` is supplied. Backend validation remains authoritative for disabled
local paths, worker limits, GitHub URLs/integration, and clone failures. Unknown
or ambiguous project references fail before settings are fetched or mutated.
Protected backends retain the normal typed sign-in guidance: use `/login` in the
TUI, or configured `OPENVIBELY_AUTH_USERNAME` and
`OPENVIBELY_AUTH_PASSWORD` for one-shot commands.

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

### Task goal lifecycle

Completion-goal commands resolve the task in the selected project before changing
anything. Set an objective with the existing pipe form; `clear` removes it.
Pause and resume keep that objective intact while changing only its lifecycle
state:

```
/tasks goal <task> | <objective>
/tasks goal <task> | clear
/tasks goal pause <task>
/tasks goal resume <task>

/tasks goal "Fix login bug" | Reproduce on staging then patch the token refresh
/tasks goal "Fix login bug" | pause
/tasks goal pause "Fix login bug"
/tasks goal resume "Fix login bug"

openvibely-terminal -project demo tasks goal "Fix login bug" '|' "Reproduce on staging then patch the token refresh"
openvibely-terminal -project demo tasks goal pause "Fix login bug"
openvibely-terminal -project demo tasks goal resume "Fix login bug"
```

A pipe always denotes a set-goal request, so `tasks goal <task> | pause` sets
`pause` as the objective rather than pausing the goal. Unknown, ambiguous, or
out-of-project task references fail before any goal mutation. All four actions
use the selected project scope.

### Active task-response steering

Use `steer` to send a correction into the currently running model turn without
starting a second execution:

```
/tasks steer <task> | <message>
openvibely-terminal -project demo tasks steer "Fix login bug" '|' "Stop and use the new interface"
```

The terminal resolves the task from the selected project, reads the thread's
single explicit running execution, and sends its exact execution ID as
`expected_turn_id`. It never guesses a turn. A task with no active response
fails before the steering POST; a stale-turn conflict is a non-zero error and
does not fall back to `tasks reply`. Use `tasks reply` explicitly when you want
the backend to queue a normal follow-up. On success, the TUI shows the pending
steering row in the open task thread when the live stream is connected, and the
CLI acknowledgement includes `status`, `task_id`, `expected_turn_id`, and the
backend's `pending_input_id` when returned with `--json`.

This release intentionally exposes active steering only. Pending-input listing,
cancellation, and redirection of already queued inputs remain available in the
web task thread but do not yet have terminal commands.

### Task attachments

Attachment reads and mutations use the currently selected project. Upload one or
more local files with the same command in the interactive TUI or one-shot CLI:

```
/tasks attachments add refactor ./request.txt ./trace.json
/tasks attachments list refactor
/tasks attachments delete refactor att-123

openvibely-terminal -project demo tasks attachments add refactor ./request.txt ./trace.json
openvibely-terminal -project demo tasks attachments list refactor
openvibely-terminal -project demo --force tasks attachments delete refactor att-123
```

The refreshed attachment list prints each stable attachment ID, filename, and
size. TUI deletion asks for the normal `yes` confirmation; CLI deletion refuses
to run unless `--force` is supplied. A task-only delete invocation opens the
interactive attachment selector, where the selected file is still confirmed
before deletion. `attach` and `attachment` are aliases for `attachments`.

### Channels, integrations, and inbound webhooks

`/channels` provides structured, secret-free management for GitHub, Slack,
Telegram, Discord, X (formerly Twitter), and Email in the selected project. The
list shows only
identity, type, connection state, and safe metadata; browser buttons and raw
backend page text are never printed.

Interactive setup and editing use field-by-field prompts. Credential fields are
masked and never enter command history or the transcript. Omit a channel
reference on `show`, `add`, `connect`, `edit`, `test`, `remove`, or `disconnect`
to open the searchable selector:

```
/channels list
/channels show slack
/channels add telegram
/channels edit email
/channels connect slack
/channels test x
/channels remove discord
```

Headless setup uses explicit integration options; output never echoes option
values. Examples:

```bash
openvibely-terminal -project demo channels add telegram --token "$TELEGRAM_BOT_TOKEN"
openvibely-terminal -project demo channels add github --auth-mode pat --pat "$GITHUB_TOKEN"
openvibely-terminal -project demo channels add x --consumer-key "$X_CONSUMER_KEY" --consumer-secret "$X_CONSUMER_SECRET" --access-token "$X_ACCESS_TOKEN" --access-token-secret "$X_ACCESS_TOKEN_SECRET"
openvibely-terminal -project demo channels edit x --poll-interval 45 --send-responses true
openvibely-terminal -project demo channels edit discord --send-responses false
openvibely-terminal -project demo channels connect slack
openvibely-terminal -project demo channels test telegram
openvibely-terminal -project demo --force channels remove discord
```

Use `help channels` for integration-specific options. Email providers are
`gmail`, `outlook`, `yahoo`, `fastmail`, `icloud`, and `custom`; custom Email
requires IMAP and SMTP hosts. X requires consumer key, consumer secret, access
token, and access token secret; its poll interval is 15 to 300 seconds. GitHub
does not expose a test route. GitHub and
Slack `connect` print a project-scoped local backend URL to open in a browser
for OAuth, without exposing OAuth state. Their `disconnect` action clears active
credentials while retaining other configuration, so interactive disconnect and
removal both require typing `yes`; headless use requires `--force` (or `-f`).
Slack removal retains the required safe mapping to `/channels/slack/disconnect`;
other remove actions delete that integration's stored configuration. Interactive
GitHub App setup accepts pasted multiline PEM keys and reconstructs line breaks
flattened by terminal input.

Authorized inbound access is managed independently from channel credentials and
outbound message targets. Use `channels access x list|add|remove` for the
project-scoped X mention-author workflow. The selected project's access rows are
secret-free in both plain and `--json` output. Telegram accepts a numeric user ID or username,
Slack accepts a Slack user ID, Discord accepts only a numeric user ID, X accepts a
numeric X user ID and an optional username, and Email addresses are normalized
before they are added:

```
/channels access telegram list
/channels access telegram add @release_user "Release User"
/channels access slack add U12345678 "Slack User"
/channels access discord add 123456789012345678
/channels access x list
/channels access x add 123456789 @release_user
/channels access x remove @release_user
/channels access email add Person@Example.COM "Person"
/channels access email remove person@example.com
openvibely-terminal -project demo --json channels access slack list
openvibely-terminal -project demo --force channels access email remove person@example.com
```

`remove` resolves one listed identity before it prompts, captures that row ID,
and then requires `yes` in the TUI or `--force`/`-f` in one-shot CLI mode.
Unknown, ambiguous, duplicate, foreign, malformed, and surplus references are
rejected before a deletion request is sent. X authorization records expose only
their canonical record ID, selected project ID, numeric X user ID, and optional
username. GitHub actors use a system-level allowlist with the selected project
ID carried as request context; their output exposes only the canonical record ID,
login, and optional display name.

Inbound webhooks use the nested `/channels webhooks` registry:

```
/channels webhooks list
/channels webhooks show "PagerDuty alerts"
/channels webhooks create "PagerDuty alerts" --priority 3 --agents triage-agent
/channels webhooks edit pager --enabled false
/channels webhooks test pager
/channels webhooks rotate pager
/channels webhooks delete pager
```

Webhook `create` and `edit` accept `--name`, `--enabled`, `--priority` (or
`--default-priority`), `--system-instructions`, `--title-template`,
`--prompt-template`, and comma-separated `--agents` (or `--agent-ids`). Edit
preserves omitted fields. Interactive webhook secret rotation and deletion
require typing `yes`; headless mode requires `--force`. Rotation is the only
command that returns a new secret. `/webhooks` and `/inbound-webhooks` remain
hidden deprecated aliases with identical project scope, selectors, errors, and
confirmation behavior.

### Automations

`/automations` (also `/automation`) exposes the selected project's recurring
automation inspection, graph editing, and lifecycle controls. The supported actions are
`list`, `show`, `open`, `edit`, `run`, `pause`, `resume`, and `delete`; `show` is
the canonical detail action and `open` is a compatibility alias with identical
selection and output. `run-now` remains accepted as a compatibility alias for `run`.
`show` renders the saved graph topology, node and transition status/config summaries,
runtime totals, resources, external state, and explicit unavailable or empty sections.
`edit` consumes the backend builder's complete YAML definition, previews it through
the backend validator, and saves only a valid changed definition. Automation creation
is not exposed by this command and remains handled elsewhere.

In the interactive TUI:

```
/automations list
/automations show "Nightly sweep"
/automations open automation-id
/automations edit "Nightly sweep"        # opens the multiline terminal editor; Ctrl+S saves, Esc cancels
/automations edit "Nightly sweep" --export automation.yaml
/automations edit "Nightly sweep" --file automation.yaml
/automations run "Nightly sweep"
/automations pause "Nightly sweep"
/automations resume "Nightly sweep"
/automations delete "Nightly sweep"
```

The same controls work as one-shot CLI commands. Put global flags before the
command:

```bash
openvibely-terminal -project demo automations list
openvibely-terminal -project demo automations show "Nightly sweep"
openvibely-terminal -project demo automations open automation-id
openvibely-terminal -project demo automations edit "Nightly sweep" --export automation.yaml
openvibely-terminal -project demo automations edit "Nightly sweep" --file automation.yaml
openvibely-terminal -project demo automations run "Nightly sweep"
openvibely-terminal -project demo automations pause "Nightly sweep"
openvibely-terminal -project demo automations resume "Nightly sweep"
openvibely-terminal -project demo --force automations delete "Nightly sweep"
```

Interactive deletion requires typing `yes` to confirm, or `Esc` to cancel.
One-shot CLI deletion requires `--force` or its `-f` shorthand; without it the
command exits without deleting anything. Automation references resolve by ID,
ID prefix, or name, and all actions use the selected project. In interactive mode,
`edit <automation>` opens the complete definition in a multiline terminal editor;
`Ctrl+S` previews backend validation and saves, while `Esc` cancels without mutation.
For deterministic or external editing, `--export` creates a new owner-only YAML file
and never overwrites an existing path. Modify that complete definition, then use
`--file` to preview backend validation and save. Unchanged files, invalid definitions,
canceled selectors/editors, and failed previews perform no save.

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
openvibely-terminal -project demo --json tasks lifecycle refactor exec-123
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
openvibely-terminal -project demo agents votes step-exec-123
openvibely-terminal -project demo --json agents votes step-exec-123
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
openvibely-terminal tasks                              # print the board
openvibely-terminal -project demo tasks show refactor  # a task's detail tabs
openvibely-terminal -project demo tasks run refactor   # run it
openvibely-terminal -project demo tasks goal refactor '|' "all checks pass"
openvibely-terminal -project demo tasks goal pause refactor
openvibely-terminal -project demo tasks goal resume refactor
openvibely-terminal -project demo tasks steer refactor '|' "stop and use the new interface"
openvibely-terminal -project demo agents votes step-exec-123 # inspect parallel votes
openvibely-terminal -project demo tasks attachments add refactor ./request.txt ./trace.json
openvibely-terminal -project demo --force tasks attachments delete refactor att-123
openvibely-terminal -project demo alerts               # list alerts
openvibely-terminal -project demo alerts list --decision-state pending --processing-state unclaimed
openvibely-terminal -project demo alerts "approved deployment" # free-text alert search
openvibely-terminal -project demo alerts read-bulk a1b2 "Release approval"
openvibely-terminal -project demo --force alerts delete-bulk a1b2 "Release approval"
openvibely-terminal -project demo analytics usage      # one analytics section
openvibely-terminal -project demo chat "ship the docs" # ask the agent, print the reply
openvibely-terminal projects create demo /Users/me/src/demo # create; output includes its backend ID
openvibely-terminal --json projects create demo /Users/me/src/demo # JSON project record
openvibely-terminal projects show demo                  # authoritative project settings
openvibely-terminal projects edit demo --description "Local checkout" --max-workers 4
openvibely-terminal --force projects edit demo --repository-source github --github-url https://github.com/acme/demo
openvibely-terminal help                               # list every command
openvibely-terminal help tasks                         # full syntax of one command
openvibely-terminal --help                             # commands + flags
```

The leading `/` is optional, so a line copied from the TUI works as-is
(`openvibely-terminal /tasks`). Output is plain text suitable for piping. One-shot
`chat <message>` and `tasks reply <task> | <message>` write model output as it
arrives and remain attached through completion; Ctrl-C cancels the active
stream. With `--json`, these two commands emit newline-delimited records with
`type`, `project_id`, `exec_id`, byte `offset`, and delta or terminal fields.
List/show JSON shapes are unchanged.
When the backend has exactly one project, project-scoped commands use it when
`-project` is omitted. When more than one project exists, those commands fail
before making a project request and require `-project <name|id>` (a full ID,
name, or unique ID prefix). `projects list`, `projects show`, `projects create`,
`projects edit`, `help`, `login`,
and other global commands remain usable without a project reference. In this
implicit single-project mode, human output begins with `project: <name> (project_id=<id>)`; `--json` output uses an envelope with `project_id`,
`project_name`, and `data` so scripts can see the selected scope.

Interactive `projects create` selects the new project immediately. In one-shot CLI
mode, the process ends after creation; the plain result prints the backend project
ID and a copyable next step such as `openvibely-terminal -project <ID> tasks`. Use that
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
$ openvibely-terminal help projects
  projects [list]                              list projects with running/queued counts
  projects show <project>                     show authoritative project settings
  projects create <name> <path>                create and select a local-path project
  projects create <name> | <path>              use | when the name or path contains spaces
  projects edit <project> [options]            update only explicitly supplied settings
    --name <name> --description <text>
    --repository-source <local|github> --repository-path <path> --github-url <url>
    --default-agent <name|id|inherit> --max-workers <n|inherit>

$ openvibely-terminal help tasks
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
  tasks goal <task> | <objective>            set a goal ("clear" removes it)
  tasks goal pause <task>                    pause a goal without changing its objective
  tasks goal resume <task>                   resume a paused goal
  tasks reply <task> | <message>             post to the task thread
  tasks steer <task> | <message>             steer the active response
  tasks activate                             activate the whole backlog
  tasks sweep                                sweep finished tasks
  tasks clear <backlog|completed>            clear a column
$ openvibely-terminal help channels
  channels list                              list safe channel identity and connection state
  channels show <channel>                    show safe channel details
  channels add <type> <options>              configure a new channel
  channels connect <github|slack>            show the browser OAuth URL
  channels edit <channel> <options>          update channel settings
  channels test <channel>                    test Slack, Telegram, Discord, X, or Email
  channels remove <channel>                  remove configuration; Slack disconnects safely (confirmation required)
  channels disconnect <github|slack>         clear connection credentials but keep other settings (confirmation required)
  channels access <telegram|slack|discord|x|email|github> <list|add|remove> [identity] [display name]
                                            manage authorized inbound access identities
  channels access x list                    list project-scoped X mention authors
  channels access x add <numeric ID> [@username]
                                            authorize one X user ID with optional username
  channels access x remove <ID|numeric ID|@username>
                                            remove one listed X mention author
  channels webhooks list                     list inbound webhooks
  channels webhooks show <webhook>           show secret-free webhook detail
  channels webhooks create <name> [options]  create an inbound webhook
  channels webhooks edit <webhook> <options> edit only specified configuration
  channels webhooks test <webhook>           create a synthetic test task
  channels webhooks rotate <webhook>         rotate its secret (confirmation required)
  channels webhooks delete <webhook>         delete a webhook (confirmation required)
```

Help is written in the form you invoke it: `/tasks` inside the chat window,
bare `tasks` on the command line.

Notes:

- `-project` accepts a name, full ID or unique prefix. An unknown or **ambiguous**
  reference exits non-zero and lists the candidates rather than running against
  the wrong project. A CLI run may omit it only when there are zero or exactly
  one backend projects; project-scoped commands require `-project <name|id>`
  when multiple projects exist. Project-reference `projects` commands and
  other global commands do not require it.
- Chat and long-running commands block until the backend finishes, then print
  the result.
- Errors go to stderr with a non-zero exit status; results go to stdout. `status`
  still prints available global health, auth, and capacity rows when project
  discovery fails, marks the result partial, and exits non-zero. With multiple
  projects it does not choose one implicitly or fetch project-scoped counts.
- `help` works with no server running.

## How it talks to the backend

The OpenVibely server exposes two kinds of routes, and the client uses both.

**JSON** (documented in the server's `docs/swagger.json`):

| Area | Endpoints |
|---|---|
| Chat | `POST /api/chat/message`, `GET /api/chat/message/:id` |
| Projects | `GET /api/projects`, `GET /projects/:id/edit`, `POST /projects`, `PUT /projects/:id` (HTMX forms) |
| Capacity | `/api/capacity/global`, `/projects`, `/models` |
| Analytics | `/api/analytics/usage`, `success-failure-rates`, `avg-execution-time-by-{task,agent}`, `most-frequent-tasks`, `failed-task-patterns`, `skills` |
| Workflows | `/api/workflows/metrics`, `best-agent`, `cheapest-agent`, `votes/:stepExecID` |
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
cmd/openvibely-terminal/main.go              entry point: flags/env, optional or interactive login, program lifecycle
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
internal/terminal/
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

Routine runs of the GitHub Actions test workflow use Go's successful-test cache.
For a fresh execution, manually dispatch the workflow with its `uncached` input,
or run the equivalent local coverage command:

```bash
go test ./... -count=1 -timeout 120s -coverpkg=./... -coverprofile=coverage.txt
go tool cover -func=coverage.txt
```

Tests cover the HTML scrapers and JSON client against `httptest` servers, the
chat update loop (history, menus, chat polling, SSE backoff), the renderers,
and an end-to-end dispatch suite asserting that each slash command issues the
expected HTTP method and path.

HTML fixtures mirror the real templ markup (including the kebab menu that
precedes a card's title), because simplified fixtures hide scraping bugs.

To additionally verify the scrapers against a **running** server:

```bash
OPENVIBELY_LIVE=http://localhost:3001 go test ./internal/client -run TestLiveBackend -count=1 -v
```

That test is skipped unless `OPENVIBELY_LIVE` is set, so ordinary runs stay
hermetic. It asserts that scraped tasks and alerts have real titles rather than
UI chrome text.
