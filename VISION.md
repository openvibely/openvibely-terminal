# OpenVibely TUI Vision

OpenVibely TUI exists to make the full OpenVibely control plane available from
a terminal.

The long-term goal is a local-first terminal UI and CLI that can operate any
OpenVibely backend, whether that backend is running on the same machine, on a
remote VPS, or inside a development environment that the CLI bootstraps on
demand. It should run well on all major operating systems: macOS, Linux, and
Windows.

The backend owns orchestration, persistence, agents, tasks, schedules,
automations, memory, skills, approvals, integrations, analytics, and model
execution. The TUI/CLI is the operator's front end: fast, scriptable,
inspectable, and comfortable for people who live in terminals.

The TUI should let a user command OpenVibely without opening a browser.

It should also make OpenVibely feel discoverable. A new user should not need to
memorize the backend's product model or command list before getting value. The
client should guide them toward the best OpenVibely features at the moment they
become relevant.

## Relationship To OpenVibely

This project is not a separate product with a separate source of truth. It is a
terminal client for OpenVibely.

The product vision for the backend lives in:

`/Users/dubee/go/src/github.com/openvibely/openvibely/VISION.md`

Future work in this repository should read that vision as the upstream product
charter. When the backend adds a capability, this client should expose it in a
terminal-native form. When there is tension between local terminal ergonomics
and backend behavior, the backend remains authoritative for data and lifecycle
semantics, while the TUI chooses the clearest terminal interaction model.

The TUI/CLI should communicate with OpenVibely through the public HTTP, JSON,
SSE, and documented API routes provided by the backend. It should not reach
into backend databases, mutate backend files directly, or duplicate backend
business logic except for client-side formatting, command parsing, reference
resolution, and lightweight workflow convenience.

## The Product We Are Building

OpenVibely TUI should become the terminal operations console for OpenVibely.

That means:

- A user can connect to any reachable OpenVibely server and operate every
  project resource from the terminal.
- A user can run it with no arguments as an interactive TUI, or pass arguments
  to run the same command once as a scriptable CLI.
- A user can host OpenVibely on a remote VPS and run this client locally against
  that remote server.
- A user can run both backend and client locally during development, demos, or
  personal use.
- If the backend is not installed or not running, the CLI can guide or perform
  a bootstrap path that installs, configures, starts, and verifies a compatible
  backend.
- A new user can be guided from first launch to useful work through friendly
  setup, project selection, suggested next actions, examples, and contextual
  help.
- Every backend feature has a terminal representation that feels natural for a
  TUI/CLI rather than a literal browser clone.
- Long-running work, live events, approvals, diffs, logs, schedules, task
  threads, automation state, analytics, memories, skills, model capacity, and
  project status are visible and actionable from the terminal.

The ideal experience is simple:

1. Run `openvibely-tui`.
2. Connect to an existing backend, or bootstrap one if needed.
3. Select or create a project.
4. Get a short, useful set of suggested next actions based on the backend state.
5. Describe work in chat, inspect what OpenVibely creates, and steer the
   resulting tasks, agents, automations, schedules, and approvals.
6. Drop into one-shot CLI commands whenever output should be piped, scripted,
   logged, or used in shell workflows.

## Principles

### The Backend Is The Source Of Truth

OpenVibely owns durable state and execution. The TUI/CLI should keep client
state shallow and recoverable: selected project, task focus, command history,
session settings, cached display data, and local bootstrap configuration.

If a feature requires durable product state, add or use a backend API instead
of inventing a local-only store.

### Terminal First, Not Browser Copy

The client should expose the same product capabilities as the web UI, but in
terminal-native ways:

- Slash commands for direct action.
- Plain text output suitable for pipes and logs.
- Compact tables, trees, timelines, status lines, and ASCII charts.
- Searchable references by ID prefix, title, name, handle, or unique substring.
- Keyboard-first navigation in the TUI.
- Live SSE event streaming in the transcript. In interactive mode `/events`
  controls display for the TUI-owned stream; in CLI mode `events on` owns one
  foreground project-scoped stream, emits line-oriented output, and remains
  active until `Ctrl-C` or clean termination. CLI `events off` must fail clearly
  rather than pretending to control another process.
- Clear non-zero exits and stderr messages in CLI mode.
- Stable, parseable output modes when useful for automation.

Do not recreate browser layouts inside the terminal. Translate the workflow,
not the pixels.

### Friendly By Default

The TUI/CLI should feel approachable, intuitave, and powerful without becoming
shallow. It should help users discover what OpenVibely can do, explain errors
in terms of the next useful action, and make common workflows obvious.

Friendly does not mean hiding power. It means progressive disclosure:

- First-run setup should explain what is needed and offer a clear path forward.
- Empty states should suggest useful next commands.
- Help should include examples, not only syntax.
- Command completions should expose the most relevant resources and actions.
- Errors should say what failed, why it likely failed, and what to try next.
- Status views should answer "what should I pay attention to?" instead of only
  dumping raw fields.
- The TUI should guide users toward high-leverage OpenVibely features such as
  tasks, automations, schedules, approvals, insights, memory, skills, analytics,
  and live events when those features match the current project state.
- Advanced features should be discoverable from simple commands like `/help`,
  `/status`, `/next`, `/suggest`, or equivalent future commands.

The client should be comfortable for experienced terminal users while still
being kind to someone launching OpenVibely for the first time.

### One Command Surface

The interactive TUI and one-shot CLI should share the same command registry,
argument behavior, help text, resource resolution, and backend client methods.

Anything typed as `/tasks run refactor` in the TUI should be invokable as
`openvibely-tui tasks run refactor` in shell mode. New capabilities should be
added once and exposed in both places unless there is a strong reason not to.

### Chat Is Still The Control Plane

The default TUI should remain one continuous project conversation. Plain text
goes to the project agent unless a task thread is focused. Commands produce
visible transcript entries. A user should be able to move between project chat,
task threads, approvals, task details, automation state, analytics, and live
events without losing conversational context.

### Complete Backend Coverage

The client should steadily converge on full coverage of OpenVibely's public
API. Future agents improving this repository should compare the backend API,
web UI behavior, and backend vision against this client and close meaningful
gaps.

Coverage includes, but is not limited to:

- Projects and project selection.
- Project chat and task threads.
- Tasks, task details, changes, schedules, chaining, attachments, lifecycle,
  execution, and review state.
- Alerts, approvals, dismissals, and review decisions.
- Agents, models, provider configuration visibility, capacity, and worker
  limits.
- Skills, memory, personality, reflection, pulse, grades, and insights.
- Schedules and recurring work.
- Automations and automation graph state.
- Channels and integrations.
- Analytics, costs, failures, trends, quality signals, and usage.
- Live events and operational health.
- Backend bootstrap, installation, update, start, stop, and diagnostics.

### Remote And Local Are Both First-Class

The client must work cleanly against:

- A local backend at the default development URL.
- A backend running elsewhere on the user's machine.
- A remote backend on a VPS or private network.
- An authenticated backend.
- A newly bootstrapped backend started by the CLI.

Configuration should be explicit, scriptable, and easy to inspect. Flags should
override environment variables. Environment variables should be documented.
Credentials and tokens should be handled carefully and never printed by
default.

### Cross-Platform Is Required

The client should support macOS, Linux, and Windows as first-class targets.

Future work should avoid assumptions that only hold on one platform. Prefer Go
standard-library behavior and portable process, path, terminal, and filesystem
handling. Where platform-specific behavior is unavoidable, isolate it behind
small adapters and test the expected behavior.

Cross-platform support includes:

- Building release binaries for macOS, Linux, and Windows.
- Handling Windows paths, shells, terminals, process signals, and executable
  names correctly.
- Avoiding hardcoded Unix commands in core flows.
- Keeping bootstrap logic aware of platform differences.
- Documenting install and update paths for each major operating system.
- Preserving useful behavior in terminals with different color, width, Unicode,
  and keyboard support.

### Bootstrap Should Be Helpful And Respectful

When no backend is reachable, the client should help the user get one.

A good bootstrap experience can:

- Detect whether an OpenVibely backend is installed locally.
- Detect common development checkouts.
- Offer to clone or install the backend when missing.
- Check required tools and ports.
- Create or reuse a local configuration.
- Start the backend in a supervised way.
- Wait for health checks.
- Explain what happened and how to stop or reconnect later.

Bootstrap should avoid surprise destructive behavior. It should be explicit
about paths, ports, commands, credentials, and persisted state.

### Operator Trust Matters

The terminal is often used during high-context engineering work. The client
should be boring in the best sense: predictable, clear, and recoverable.

Commands should fail loudly enough to be useful and quietly enough to preserve
flow. Ambiguous resource references should list candidates instead of guessing.
Long-running operations should show progress or status. Dangerous actions
should require confirmation in TUI mode and offer explicit force flags in CLI
mode when appropriate.

### Scriptability Is A Product Feature

The CLI should be useful in shell scripts, CI jobs, cron tasks, local aliases,
and other automation.

As the project grows, prefer:

- Plain text defaults for humans.
- Optional JSON output for machines.
- Non-zero exit statuses for failures.
- Stable command syntax.
- Clear separation between stdout results and stderr diagnostics.
- Timeouts and cancellation where operations can block.

## Recursive Self-Improvement Guidance

This repository is intended to be improved by future LLM agents. Agents should
use this file as a north star and make concrete, tested progress toward it.

When improving the project recursively:

1. Read the backend vision first.
2. Inspect the backend API, swagger documentation, routes, handlers, and web UI
   behavior relevant to the area being improved.
3. Compare backend capabilities to the TUI/CLI command surface.
4. Pick a small, valuable gap that moves this client closer to full terminal
   coverage.
5. Implement the backend client call, command behavior, TUI rendering, CLI
   output, help text, and tests together.
6. Preserve the shared TUI/CLI command registry.
7. Prefer terminal-native workflows over browser-shaped screens.
8. Keep durable state in the backend.
9. Add or update README documentation when user-facing behavior changes.
10. Run focused tests and fix regressions before moving on.

Good recursive improvements include:

- Adding missing commands for backend resources.
- Improving task, automation, lifecycle, diff, log, and approval inspection.
- Adding JSON output for scriptability.
- Improving remote connection setup and diagnostics.
- Adding backend bootstrap and health workflows.
- Adding first-run onboarding, contextual suggestions, and friendlier empty
  states.
- Improving cross-platform behavior, packaging, and installation.
- Making command help more complete and accurate.
- Improving resource reference resolution and ambiguity messages.
- Rendering dense operational data more clearly in tables, trees, timelines, or
  charts.
- Adding tests around client API calls, command parsing, CLI output, and TUI
  rendering.

Poor recursive improvements include:

- Building a parallel backend inside the TUI.
- Storing product data locally because an API is missing.
- Adding large abstractions before there is repeated local pressure.
- Making visual terminal flourishes more important than operational clarity.
- Assuming every user already knows which OpenVibely feature to use next.
- Adding platform-specific shortcuts that break macOS, Linux, or Windows.
- Changing command syntax without preserving compatibility.
- Treating bootstrap as permission to run destructive install or cleanup
  commands without user intent.
- Adding untested behavior across broad command surfaces.

## What We Will Not Optimize For

OpenVibely TUI should not become:

- A replacement for the OpenVibely backend.
- A second source of truth for projects, tasks, agents, automations, memory, or
  skills.
- A browser UI squeezed into terminal dimensions.
- A novelty TUI where styling matters more than speed, clarity, and command
  ergonomics.
- A local-only tool that assumes the backend is on the same machine.
- A remote-only tool that makes local development awkward.
- A Unix-only tool that neglects Windows or platform-specific installation
  needs.
- A command collection with inconsistent syntax, help, output, or error
  behavior.
- An unsafe bootstrapper that hides installation, credential, path, or process
  decisions.

## Direction

Near-term work should deepen the loop:

1. Connect to a backend or bootstrap one.
2. Select a project.
3. Guide the user toward the most useful next action.
4. Chat with the project agent.
5. Turn goals into tasks, schedules, automations, and approvals.
6. Inspect details, logs, diffs, lifecycle evidence, and live events.
7. Review and steer execution from task threads and commands.
8. Use analytics, pulse, reflection, insights, memory, and skills to improve the
   next run.
9. Script repeatable operations through the CLI.

The north star is full OpenVibely power from a local terminal: remote-capable,
scriptable, inspectable, friendly, cross-platform, and faithful to the backend.
