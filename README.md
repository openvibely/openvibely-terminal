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
openvibely-terminal -project demo chat "summarize the current project"
openvibely-terminal --json -project demo automations list
```

Project-scoped commands automatically use the backend's only project. When the
backend has multiple projects, select one with `-project <name|id>`.

Common commands include:

| Command | Purpose |
|---|---|
| `/tasks` | Inspect and manage tasks and task threads |
| `/alerts` | Review and act on alerts |
| `/automations` | Inspect, edit, and control automations |
| `/schedule` | Manage task schedules |
| `/agents`, `/models`, `/workers` | Inspect execution resources |
| `/channels` | Manage integrations and inbound webhooks |
| `/projects`, `/project` | Manage or select projects |
| `/analytics` | View usage and execution statistics |
| `/status`, `/setup`, `/login` | Check and recover connectivity |
| `/help <command>` | Show complete command syntax |

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
