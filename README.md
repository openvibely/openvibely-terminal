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

The TUI does not install or start the backend. If it cannot connect, run `/setup`
in the TUI or `openvibely-terminal setup` in a shell for read-only recovery guidance.
Use `/login` when the backend requires authentication.

## Usage

Interactive input is either a chat message or a slash command:

```text
summarize the current project
/tasks
/tasks show refactor
/tasks goal refactor | all tests pass
/tasks goal pause refactor
/tasks goal resume refactor
/automations show "Nightly sweep"
/status
/help tasks
```

Press `Tab` after typing `/` to complete commands. Use `↑` and `↓` to select a
completion, `PgUp` and `PgDn` to scroll, and `Ctrl+C` to quit.

For one-shot CLI use, omit the leading slash:

```bash
openvibely-terminal tasks
openvibely-terminal -project demo tasks show refactor
openvibely-terminal -project demo tasks goal pause refactor
openvibely-terminal -project demo chat "summarize the current project"
openvibely-terminal --json -project demo automations list
```

Project-scoped commands automatically use the backend's only project. When the
backend has multiple projects, select one with `-project <name|id>`.

Common commands include:

| Command | Purpose |
|---|---|
| `/tasks` | Inspect and manage tasks, threads, and completion goals. See the task-goal lifecycle examples below. |
| `/alerts` | Review alerts, including selected bulk read/delete actions |
| `/automations` | Inspect, edit, and control automations |
| `/schedule` | Manage task schedules |
| `/agents`, `/models`, `/workers` | Inspect execution resources; `/models add` configures providers and `/models edit` safely updates existing configurations |
| `/channels` | Manage integrations, inbound webhooks, and project-scoped Telegram, Slack, Discord, X, and Email authorized access with `channels access <provider> list\|add\|remove` |
| `/projects`, `/project` | Manage or select projects |
| `/analytics` | View usage and execution statistics |
| `/status`, `/setup`, `/login` | Check and recover connectivity |
| `/help <command>` | Show complete command syntax |

Use `help channels` for integration-specific options. X mention access is project-scoped and uses `channels access x list|add|remove`; add accepts a numeric X user ID plus an optional username. The username is normalized by removing a leading `@`:

```text
channels access x list
channels access x add 123456789 @release_user
channels access x remove @release_user
openvibely-terminal -project demo --json channels access x list
openvibely-terminal -project demo --force channels access x remove 123456789
```


```text
tasks goal <task> | <objective>
tasks goal <task> | clear
tasks goal pause <task>
tasks goal resume <task>
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
`/models add` collects new configuration details and masks API-key input. Edits
first read the backend-authoritative configuration, apply only named options,
and retain credentials and provider-specific settings that were not changed.

In one-shot mode, API keys are accepted only from piped or redirected standard
input with `--api-key-stdin`; never place a key in an argument, shell history,
or an example. In the TUI, append `--api-key` to `models edit` to open a masked
replacement prompt.

```bash
# API-key provider: the key is read from standard input, not argv.
printf '%s' "$OPENAI_API_KEY" | openvibely-terminal models add openai "OpenAI" gpt-4o --api-key-stdin

# Local Ollama: the endpoint is optional and defaults to localhost in the backend.
openvibely-terminal models add ollama "Local Ollama" llama3.1:8b --endpoint http://localhost:11434

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

See the [development section](docs/user-guide.md#development) for coverage and
live-backend test instructions.
