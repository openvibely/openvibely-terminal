# OpenVibely Terminal

A terminal client for the [OpenVibely](https://github.com/openvibely/openvibely)
backend, built with [Bubble Tea](https://github.com/charmbracelet/bubbletea),
Bubbles, and Lip Gloss.

OpenVibely Terminal puts chat and project commands in one transcript. Type a message
to talk to the project agent, or start a line with `/` to inspect and manage
tasks, alerts, automations, schedules, integrations, and other project resources.
The same commands also work as one-shot shell commands.

```text
❯ /tasks
▸ Tasks
  Backlog (1)
    ID        STATUS     TITLE
    a1b2c3d4  · pending  Refactor the handler package

❯ /tasks run refactor
❯ why is that task taking so long?
● agent
  It's waiting on a worker slot.
```

## Getting started

Requires Go 1.27.1+ and a running OpenVibely backend. The default backend URL is
`http://localhost:3001`.

```bash
make build
./bin/openvibely-terminal

# Or build and run in one step.
make run
```

Bare `/setup` and `openvibely-terminal setup` are read-only recovery guides. If you want the terminal to inspect or start a local backend, opt in explicitly with `/setup check`, `/setup start`, or `/setup bootstrap --install`; start/bootstrap disclose effects and require confirmation in the TUI or `--force` in CLI mode. Use `/login` when the backend requires authentication.

Stop a setup-started local backend manually after its health check succeeds: it continues running independently, and the terminal does not track a backend PID, so stop only the OpenVibely process you recognize. On macOS/Linux, identify the local listener with `lsof -nP -iTCP:3001 -sTCP:LISTEN`, then stop that PID with `kill <pid>`; on Windows, use Task Manager or `netstat -ano | findstr :3001` followed by `taskkill /PID <pid>`. Reconnect to a running backend with `/status` or `openvibely-terminal -server http://localhost:3001 status`. Update a local backend by stopping it first, then running the documented installer again or `openvibely-terminal --force setup bootstrap --install`, which downloads over HTTPS, allows installer file changes, starts again, and waits for health.

## Usage

Interactive input is either a chat message or a slash command:

```text
summarize the current project
/tasks
/tasks show refactor
/tasks show refactor review
/tasks reviews add refactor internal/auth.go:42 Handle token refresh errors
/tasks swarm --category active Coordinate release | Split validation across workers
/tasks goal refactor | all tests pass
/tasks goal pause refactor
/tasks goal resume refactor
/tasks steer refactor | stop and use the new interface
/tasks worktree refactor
/tasks pull-request refactor
/automations show "Nightly sweep"
/automations create --file automation.yaml
/personality delete-bulk old_one "Old Two"
/projects delete demo
/status
/help tasks
```

Press `Tab` after typing `/` to complete commands. Use `↑` and `↓` to select a
completion, `PgUp` and `PgDn` to scroll, and `Ctrl+C` to quit.

For one-shot CLI use, omit the leading slash:

```bash
openvibely-terminal tasks
openvibely-terminal -project demo tasks show refactor review
openvibely-terminal -project demo tasks worktree refactor
openvibely-terminal -project demo tasks pull-request refactor
openvibely-terminal -project demo --json tasks pull-request refactor
openvibely-terminal -project demo tasks reviews refactor
openvibely-terminal -project demo tasks swarm --category active "Coordinate release" '|' "Split validation across workers"
openvibely-terminal -project demo tasks goal pause refactor
openvibely-terminal -project demo tasks steer refactor '|' "stop and use the new interface"
openvibely-terminal -project demo chat "summarize the current project"
openvibely-terminal -project demo automations create --file automation.yaml
openvibely-terminal -project demo personality add "Release Coach" --file coach.md
openvibely-terminal -project demo personality add "Release Coach" description=safe releases --file coach.md
openvibely-terminal -project demo personality edit release_coach --file coach.md
openvibely-terminal --force projects delete demo
openvibely-terminal -project demo --force personality delete-bulk old_one "Old Two"
openvibely-terminal --json -project demo automations list
```

`/personality edit <key>` opens a multiline TUI editor prefilled with the current
system prompt; `Ctrl+S` saves and `Esc` cancels. The TUI also keeps the existing
four-field pipe edit form. CLI edits use `personality edit <key> --file <path>`
to replace only the prompt, preserving name and description. Add personalities
from exact prompt file contents with `personality add <name> --file <path>`; add
`description=<text>` before `--file` to set a description. `--json` on a file edit
emits the updated personality record.

Project-scoped commands automatically use the backend's only project. When the
backend has multiple projects, select one with `-project <name|id>`. Use
`tasks swarm [options] <title> | <prompt>` to create an autonomous swarm parent;
`--category active` starts the planner immediately, while backlog swarm parents
start planning after they become Active.

`projects delete <project>` requires the standard interactive confirmation (`Type 'yes' to confirm or Esc to cancel`). One-shot CLI deletion requires `--force` (or `-f`), and the backend remains authoritative for refusing deletion of the default project. Deletion removes the project and its backend-owned project data; after success, the terminal refreshes the catalog and selects the backend-selected remaining/default project when available.

Create projects from either a local checkout or a GitHub repository:

```text
/projects create demo /Users/me/src/demo
/projects github-create "My GitHub Project" https://github.com/acme/demo
```

The local form is `projects create <name> <path>`; use `|` when an unquoted local name or path contains spaces, for example `/projects create My Project | C:\Users\me\src\my-project`. Quote or escape `|` in a one-shot shell command so it is passed as an argument. Append the local-only `--create-directory` option to ask the backend to create a missing checkout folder, such as `/projects create demo /Users/me/src/new-project --create-directory`. If a literal path ends with that option text, quote the whole path so it remains one operand. The backend validates the path and reports filesystem errors; a failed creation does not leave a project record. The GitHub form is `projects github-create <name> <url>` and accepts `|` before the URL when the project name contains spaces. The backend clones the GitHub repository and handles authentication, so GitHub-backed creation also works when local repository paths are disabled in the environment. After either form succeeds, the backend-assigned project ID is selected in the TUI and included in plain output or the `--json` project record.

Common commands include:

| Command | Purpose |
|---|---|
| `/tasks` | Inspect and manage tasks, threads, inline review comments, completion goals, worktrees, and task pull requests. See the task-goal lifecycle examples below. |
| `/alerts` | Review alerts, including selected bulk read/delete actions |
| `/skills` | Manage reusable skills; `skills always <skill>` and `skills load <skill>` are equivalent names for automatic loading |
| `/automations` | Inspect, edit, and control automations |
| `/schedule` | Manage task schedules |
| `/personality` | Manage built-in and custom personalities; bulk deletion is limited to inactive non-preset custom entries |
| `/agents`, `/models`, `/workers` | Inspect execution resources; `/workers` shows a single snapshot and `/workers watch` keeps capacity current every 3s until Esc |
| `/channels` | Manage integrations, inbound webhooks, saved outbound targets, and project-scoped Telegram, Slack, Discord, X, Email, and GitHub authorized access with `channels access <provider> list\|add\|remove` and `channels targets list\|show\|add\|edit\|test\|remove\|policy` |
| `/projects`, `/project` | Manage or select projects |
| `/analytics` | View usage and execution statistics |
| `/pulse` | View the upcoming-work briefing; `openvibely-terminal -project <project> --json pulse` emits structured running, queued, blocked, scheduled, and summary fields for scripts |
| `/status`, `/setup`, `/login` | Check and recover connectivity |
| `/server [url]` | Show the backend URL, or switch local/remote servers without restarting |
| `/help <command>` | Show complete command syntax |

Use `/tasks pull-request <ref>` or `openvibely-terminal -project <project> tasks pull-request <ref>` to ask the backend to create or reuse a task branch pull request. The command reports its number, URL, and created/reused status; add `--json` for structured output. `/tasks worktree <ref>` includes any PR link recorded for the task.

Use `help workers` for worker-capacity options. `/workers` and `/workers show` render one stable snapshot for scripts; `/workers watch` starts a live terminal view that refreshes global, project, and dedicated model worker values every 3 seconds and stops on Esc. Headless scripts can keep the stable snapshot path:

```text
/workers watch
openvibely-terminal workers show
```

Use `help channels` for integration-specific options. Channel access management is scoped to the selected project in every request. Telegram, Slack, Discord, X, Email, and GitHub expose `channels access <provider> list|add|remove`; GitHub actors are a system-level allowlist viewed with the selected project as request context. GitHub logins are normalized by removing a leading `@` and lowercasing them. A display name is optional and must be one quoted operand when it contains spaces:

```text
channels access github list
channels access github add @Alice
channels access github add @Alice "Release Reviewer"
channels access github remove @Alice
openvibely-terminal -project demo --json channels access github list
openvibely-terminal -project demo --force channels access github remove @alice
```

X mention access remains project-scoped and uses a numeric X user ID plus an optional username:

```text
channels access x list
channels access x add 123456789 @release_user
channels access x remove @release_user
openvibely-terminal -project demo --json channels access x list
openvibely-terminal -project demo --force channels access x remove 123456789
```

```text
channels targets list
channels targets add slack C123 --kind channel --name ops --thread-id 42 --home
channels targets add slack U123 --kind user --name "Release reviewer"
channels targets add telegram -1001234567890 --kind chat --name alerts --topic-id 7
channels targets add email person@example.com --name client --default-subject Deploy
channels targets add discord 123456789012345678 --kind user --name reviewer
channels targets add x me --kind account --name release
channels targets edit ops --default-subject Deployments
channels targets test ops
channels targets test draft email person@example.com --name draft --default-subject Preview
channels targets policy on
channels targets remove ops
```

The same registry is available to one-shot CLI callers:

```bash
openvibely-terminal -project demo channels targets list
openvibely-terminal -project demo --json channels targets list
openvibely-terminal -project demo channels targets add slack C123 --kind channel --name ops --thread-id 42 --home
openvibely-terminal -project demo channels targets add discord 123456789012345678 --kind user --name reviewer
openvibely-terminal -project demo channels targets edit ops --thread-id 99
openvibely-terminal -project demo channels targets test ops
openvibely-terminal -project demo channels targets test draft email person@example.com --name draft
openvibely-terminal -project demo channels targets policy off
openvibely-terminal -project demo --force channels targets remove ops
```

`targets` also accepts `outbound-targets`. Saved target list/show output is
project-scoped. JSON includes each canonical `id` alongside platform, target
kind, name, destination, thread/topic, Home state, and default subject; project
IDs, credentials, and hidden backend fields remain omitted. Plain output stays
compact and does not show IDs. JSON list output is `[]` when no targets are
saved. Slack and Discord support user-DM targets with `--kind user`; Slack
channels, Telegram chats/topics, Email addresses, Discord channels, and X
accounts use the corresponding backend forms and normalization. The backend
remains authoritative for normalization and duplicate validation. Saved and
draft tests report only `sent` or `failed`, without requiring credentials in the
terminal or printing backend HTML.

Read an ID from list or show JSON and pass it as the target reference for a
later edit or remove. IDs let scripts select one destination when different
platforms use the same mutable name:

```bash
TARGET_ID=$(openvibely-terminal -project demo --json channels targets list | jq -r '.[] | select(.platform == "email" and .name == "shared") | .id')
openvibely-terminal -project demo --json channels targets edit "$TARGET_ID" --default-subject Deployments
openvibely-terminal -project demo --force --json channels targets remove "$TARGET_ID"
```

`targets policy on|off` reads or changes the selected project's
explicit-unsaved-target setting. Removing by ID or name resolves only within
the selected project's results, then requires `yes` in the TUI or `--force` in
CLI mode. Missing, foreign, ambiguous, or stale references are rejected without
submitting a replacement form.

`channels webhooks` manages project-scoped inbound webhooks. Single deletion remains
available as `channels webhooks delete <webhook>`; delete several with
`channels webhooks delete-bulk <webhook>...`. References may be IDs, names, or
unique matches, but every reference is resolved in the selected project before
any deletion. The TUI bulk selector uses Space to select, filtering to narrow
visible rows, Enter to review the selected names/count, and
`Type 'yes' to confirm or Esc to cancel`. Headless bulk deletion requires
`--force` (or `-f`), and `--json` returns a stable object such as
`{"action":"delete-bulk","deleted":2}`. Unknown, ambiguous, duplicate,
foreign, or stale references make no deletion request; webhook secrets are
never included in list, detail, or bulk output. The deprecated `webhooks` and
`inbound-webhooks` aliases support the same lifecycle.

```text
tasks goal <task> | <objective>
tasks goal <task> | clear
tasks goal pause <task>
tasks goal resume <task>
```

Steer only an actively running task response with the guarded form below. The
terminal first reads the selected project's thread to obtain its exact active
turn ID, then sends that ID with the steering request. If the response is no
longer active or the turn changed, the command fails without sending a normal
follow-up. Use `tasks reply` explicitly when a queued follow-up is intended.
Pending task-thread inputs can be inspected and managed without exposing backend
controls or secrets:

```text
tasks inputs <task>
tasks inputs cancel <task> <input-id>       # asks for yes in the TUI
tasks inputs steer <task> <input-id>        # queued only; active turn required
openvibely-terminal -project demo --json tasks inputs <task>
openvibely-terminal -project demo --force tasks inputs cancel <task> <input-id>
```

Input listings distinguish queued follow-ups from pending steering and include a
canonical input ID, task/project identity, safe text preview, and attachment
presence. `pending` and `pending-inputs` are aliases for `inputs`; `cancel-input`
and `steer-queued` are shorthand mutation forms. `--json` emits stable fields and
`[]` when empty. Cancellation never cancels the active task execution; TUI
cancellation requires typing `yes` (or `Esc` to cancel), one-shot cancellation
requires `--force`, and the client reports an error unless the backend confirms
that the cancellation mutation was applied. Queued-to-steering conversion
requires the matching active turn and a confirmed steering response, rejecting
missing, stale, ambiguous, foreign-project, and no-active-turn references without
mutation.

```text
/tasks steer <task> | <message>
openvibely-terminal -project demo tasks steer <task> '|' <message>
```

Mark or remove selected alerts by supplying one or more IDs or quoted titles. To
inspect an exact workflow queue, add `--decision-state` and optionally
`--processing-state` after `alerts list`; these are backend predicates, while
unflagged alert terms remain free-text matching. Decision states are `pending`,
`approved`, `rejected`, and `dismissed`. Processing states are `not_applicable`,
`unclaimed`, `claimed`, `implementation_task_linked`, `completed`, and `failed`.
Bulk removal follows the normal confirmation safety rule and needs `--force` in
one-shot CLI mode:

```bash
/alerts list --decision-state pending --processing-state unclaimed
/alerts "approved deployment"                    # free-text, not a state predicate
openvibely-terminal -project demo alerts list --decision-state approved
/alerts read-bulk a1b2 "Release approval"
/alerts delete-bulk a1b2 "Release approval"
openvibely-terminal -project demo --force alerts delete-bulk a1b2 "Release approval"
```

## Model providers

Use shared `models add` and `models edit <model>` actions in the TUI or CLI.
`/models add` collects new configuration details for Anthropic, OpenAI, Ollama,
and focused OpenAI-compatible Chat Completions configs while masking API-key
input. Edits first read the backend-authoritative configuration, apply only named
options, and retain credentials and provider-specific settings that were not
changed.

In one-shot mode, API keys are accepted only from piped or redirected standard
input with `--api-key-stdin`; never place a key in an argument, shell history,
or an example. In the TUI, append `--api-key` to `models edit` to open a masked
replacement prompt.

```bash
# API-key provider: the key is read from standard input, not argv.
printf '%s' "$OPENAI_API_KEY" | openvibely-terminal models add openai "OpenAI" gpt-4o --api-key-stdin

# Local Ollama: the endpoint is optional and defaults to localhost in the backend.
openvibely-terminal models add ollama "Local Ollama" llama3.1:8b --endpoint http://localhost:11434

# OpenAI-compatible Chat Completions: endpoint and default token limit are required.
openvibely-terminal models add openai_compatible "Local vLLM" llama-3.1 --endpoint http://127.0.0.1:8000/v1 --default-max-tokens 4096
printf '%s' "$OPENROUTER_API_KEY" | openvibely-terminal models add openai_compatible "OpenRouter" openai/gpt-4o --endpoint https://openrouter.ai/api/v1 --default-max-tokens 4096 --api-key-stdin

# Existing model: only supplied options change; unchanged credentials are retained.
openvibely-terminal -project demo models edit "Local Ollama" --model llama3.2 --max-workers 2 --endpoint http://localhost:11434

# Replace an existing API key without placing it in argv.
printf '%s' "$OPENAI_API_KEY" | openvibely-terminal -project demo models edit OpenAI --api-key-stdin

# OAuth saves the configuration, then reports the backend authorization status and browser handoff.
openvibely-terminal models add anthropic "Claude OAuth" claude-sonnet-4-6 --oauth
```

OAuth is not terminal-only completion: open the reported authorization URL in a
browser, complete the provider flow, and rely on the backend-confirmed status
before treating the model as connected.

## Configuration

Flags override environment variables.

| Flag | Environment variable | Default |
|---|---|---|
| `-server` | `OPENVIBELY_SERVER_URL` | `http://localhost:3001` |
| `-user` | `OPENVIBELY_AUTH_USERNAME` | — |
| `-pass` | `OPENVIBELY_AUTH_PASSWORD` | — |
| `-project` | `OPENVIBELY_PROJECT` | The only project, when exactly one exists |

`-server` / `OPENVIBELY_SERVER_URL` choose the backend at startup. In the TUI,
`/server` shows the current backend and `/server <url>` switches to another
local or remote server without restarting:

```text
/server
/server https://openvibely.example.com
/server http://localhost:3001
/server https://protected.example.com --login
```

The new URL is checked (health, session, and project list) before the current
connection is replaced. On success, projects reload and the transcript, task
thread, selections, pending chat, and live event stream from the previous
backend are cleared, even when both servers use the same project IDs. A
malformed, unreachable, or unhealthy URL leaves the current connection in
place. A server that requires sign-in is only switched to with `--login`, which
opens the masked `/login` form. URLs with credentials, queries, or fragments
are rejected and never echoed. One-shot CLI runs keep using `-server` or
`OPENVIBELY_SERVER_URL`.

Prefer `/login` for interactive authentication and environment variables from a
secret manager for headless use. Literal passwords in command-line arguments may
be exposed through shell history or process listings.

## Documentation

The [user guide](docs/user-guide.md) covers backend recovery, authentication,
every command area, CLI and JSON behavior, architecture, and development.
The built-in `help <command>` output is the authoritative reference for current
command syntax.

## Development

```bash
make test
make vet
make build
```

Routine local validation from a clean checkout should use the default hermetic test
suite:

```bash
go test ./...
go test ./internal/client ./internal/terminal -count=1
```

Measurement-heavy evidence tests are opt-in so normal runs do not spend seconds in
artificial sleeps or repeated samples. Run them explicitly when updating the
bounded-list or status-latency performance contracts:

```bash
OPENVIBELY_PERF_EVIDENCE=1 go test ./internal/client ./internal/terminal -run 'TestList(Alerts|Automations)BoundedLargeCatalogLatencyAndAllocations|TestCLIStatus(LatencyOverlapsBalancedAndSlowDiscovery|UsesTwoDelayedRequestWaves|DeadlineAfterProjectSelectionCancelsEveryStartedRequest)|TestWebhooksCanonicalIDCatalogPerformanceEvidence|TestRenderLifecycleEventsBackendShapedMeasurements' -count=1 -v
```

The filtered skills JSON small-set latency and allocation evidence uses paired
samples. Its serial-versus-bounded benchmark uses ten fixed repetitions per case:

```bash
OPENVIBELY_PERF_EVIDENCE=1 go test ./internal/client ./internal/terminal -run '^Test(LoadSkillDetailsSmallSetAllocationEvidence|FilteredSkillsSmallSetLatencyEvidence)$' -count=1 -v
go test ./internal/terminal -run '^$' -bench '^BenchmarkListSkillsWithFilteredContentSerialVsBounded$' -benchtime=10x -benchmem -count=1 -v
```

See the [development section](docs/user-guide.md#development) for coverage and
live-backend test instructions.
