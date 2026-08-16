package tui

// The slash-command registry: one entry per OpenVibely screen/resource, each
// with the actions the web UI offers for it.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-tui/internal/client"
)

const cmdTimeout = 90 * time.Second

func init() {
	commands = []command{
		tasksCommand(),
		scheduleCommand(),
		alertsCommand(),
		skillsCommand(),
		agentsCommand(),
		modelsCommand(),
		workersCommand(),
		channelsCommand(),
		personalityCommand(),
		pulseCommand(),
		reflectionCommand(),
		gradesCommand(),
		insightsCommand(),
		automationsCommand(),
		analyticsCommand(),
		projectCommand(),
		projectsCommand(),
		statusCommand(),
		buildCommand(),
		eventsCommand(),
		chatCommand(),
		clearCommand(),
		helpCommand(),
		quitCommand(),
	}
}

// page builds a command that renders a read-only backend screen.
func page(name, desc string, fetch func(c *client.Client, ctx context.Context, projectID string) (string, error), aliases ...string) command {
	return command{
		name:    name,
		aliases: aliases,
		desc:    desc,
		run: func(m Model, _ []string) (Model, tea.Cmd) {
			c, pid := m.client, m.selectedID
			return m, run(titleFor(name), cmdTimeout, func(ctx context.Context) (string, error) {
				return fetch(c, ctx, pid)
			})
		},
	}
}

// generateThenFetch consolidates the "optionally run a generate action, then
// always fetch and return the page text" sequence shared by pulseCommand,
// reflectionCommand, and insightsCommand. If currentAction == triggerAction
// the generate func is called first; a generate error short-circuits before
// the fetch.
func generateThenFetch(ctx context.Context, triggerAction, currentAction string, generate func(context.Context) error, fetch func(context.Context) (string, error)) (string, error) {
	if currentAction == triggerAction {
		if err := generate(ctx); err != nil {
			return "", err
		}
	}
	return fetch(ctx)
}

// refreshAndRender consolidates the "act, then reload the list, then format a
// status line followed by the refreshed render" sequence shared by the
// task/alert/skill/agent/model mutation commands. If the refresh fails after
// a successful mutation, the error is swallowed and only the status line is
// returned — the mutation itself already succeeded, and this matches the
// policy every call site used before consolidation.
func refreshAndRender[T any](status string, list func() ([]T, error), render func([]T, string) string) (string, error) {
	items, err := list()
	if err != nil {
		return status, nil
	}
	return status + "\n\n" + render(items, ""), nil
}

func titleFor(name string) string {
	if name == "" {
		return ""
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

// confirmOr gates a destructive command behind a TUI confirmation prompt or
// the CLI --force flag.
//
//   - TUI mode: sets m.pendingConfirmation with displayMsg and returns a nil
//     cmd; the caller must type "yes" and Enter before cmd executes.
//   - CLI mode without --force: returns an errCmd with cliMsg so the process
//     exits nonzero with a clear hint.
//   - CLI mode with --force: returns the original cmd immediately.
func confirmOr(m Model, displayMsg, cliMsg string, cmd tea.Cmd) (Model, tea.Cmd) {
	if cliMode {
		if forceMode {
			return m, cmd
		}
		return m, errCmd(cliMsg)
	}
	// TUI mode: park the command until the user confirms.
	m.busy = false
	m.pendingConfirmation = &pendingCmd{message: displayMsg, cmd: cmd}
	return m, nil
}

// --- tasks ---

func tasksCommand() command {
	actions := []string{"list", "open", "show", "new", "edit", "run", "stop", "delete", "move", "order", "goal", "reply", "activate", "sweep", "clear"}
	return command{
		name:    "tasks",
		aliases: []string{"task", "t", "board"},
		args:    "[filter|id]",
		actions: actions,
		desc:    "the task board and task threads",
		usage: []string{
			"tasks [filter]                             list the board, optionally filtered",
			"tasks open <task>                          enter the task's thread",
			"tasks show <task> [tab]                    details, thread, changes, schedules, chaining, attachments, lifecycle",
			"tasks new <title> [| <prompt>]             create a task",
			"tasks edit <task> | <title> [| <prompt>]   edit title/prompt",
			"tasks run|stop|delete <task>               run, cancel or delete",
			"tasks move <task> <backlog|active|completed>",
			"tasks order <task> <position>              reorder within its column",
			"tasks goal <task> | <objective>            set a goal (\"clear\" removes it)",
			"tasks reply <task> | <message>             post to the task thread",
			"tasks activate                             activate the whole backlog",
			"tasks sweep                                sweep finished tasks",
			"tasks clear <backlog|completed>            clear a column",
		},
		examples: []string{
			`tasks new Fix login bug | Investigate and resolve the OAuth redirect failure`,
			`tasks move "Fix login bug" active`,
			`tasks goal "Fix login bug" | Reproduce on staging then patch the token refresh`,
			`tasks reply "Fix login bug" | PR is up — please review`,
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			mm, cmd, ok := m.needProject()
			if !ok {
				return mm, cmd
			}
			action, rest := splitAction(actions, args)
			c, pid := m.client, m.selectedID
			ref := strings.Join(rest, " ")

			switch action {
			case "", "list":
				return m, run("Tasks", cmdTimeout, func(ctx context.Context) (string, error) {
					tasks, err := c.ListTasks(ctx, pid)
					if err != nil {
						return "", err
					}
					return renderBoard(tasks, ref), nil
				})

			case "open":
				if ref == "" {
					return m, errCmd("usage: /tasks open <id|title>")
				}
				return m, func() tea.Msg {
					ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
					defer cancel()
					t, err := resolveTask(ctx, c, pid, ref)
					if err != nil {
						return threadOpenedMsg{err: err}
					}
					d, err := c.GetTask(ctx, t.ID)
					if err != nil {
						return threadOpenedMsg{err: err}
					}
					return threadOpenedMsg{
						taskID: t.ID,
						title:  firstNonEmpty(d.Task.Title, t.Title, shortID(t.ID)),
						body:   renderThread(d),
					}
				}

			case "show":
				// "/tasks show <ref> <tab>" selects a detail tab; strip it before
				// joining the remaining words into the task reference.
				showRest := rest
				tab := ""
				if n := len(showRest); n > 1 && isDetailTab(showRest[n-1]) {
					tab = showRest[n-1]
					showRest = showRest[:n-1]
				}
				showRef := strings.Join(showRest, " ")
				return m, run("Task", cmdTimeout, func(ctx context.Context) (string, error) {
					t, err := resolveTask(ctx, c, pid, showRef)
					if err != nil {
						return "", err
					}
					d, err := c.GetTask(ctx, t.ID)
					if err != nil {
						return "", err
					}
					return renderTaskDetail(t, d, tab), nil
				})

			case "new":
				if ref == "" {
					return m, errCmd("usage: /tasks new <title> [| <prompt>]")
				}
				title, prompt := splitPipe(ref)
				if title == "" {
					return m, errCmd("usage: /tasks new <title> [| <prompt>]")
				}
				return m, run("Tasks", cmdTimeout, func(ctx context.Context) (string, error) {
					form := client.TaskForm{Title: title, Prompt: prompt, Category: "backlog"}
					if form.Prompt == "" {
						form.Prompt = title
					}
					if err := c.CreateTask(ctx, pid, form); err != nil {
						return "", err
					}
					return refreshAndRender("created "+title,
						func() ([]client.Task, error) { return c.ListTasks(ctx, pid) },
						renderBoard)
				})

			case "edit":
				ref, newTitle := splitPipe(ref)
				if ref == "" || newTitle == "" {
					return m, errCmd("usage: /tasks edit <task> | <new title> [| <new prompt>]")
				}
				title, prompt := splitPipe(newTitle)
				return m, run("Tasks", cmdTimeout, func(ctx context.Context) (string, error) {
					t, err := resolveTask(ctx, c, pid, ref)
					if err != nil {
						return "", err
					}
					if prompt == "" {
						prompt = t.Prompt
					}
					form := client.TaskForm{Title: title, Prompt: prompt, Category: t.Category}
					if err := c.UpdateTask(ctx, t.ID, form); err != nil {
						return "", err
					}
					return refreshAndRender("updated "+title,
						func() ([]client.Task, error) { return c.ListTasks(ctx, pid) },
						renderBoard)
				})

			case "order":
				if len(rest) < 2 {
					return m, errCmd("usage: /tasks order <task> <position>")
				}
				pos := atoiSafe(rest[len(rest)-1])
				if pos < 0 {
					return m, errCmd("position must be a number")
				}
				target := strings.Join(rest[:len(rest)-1], " ")
				return m, run("Tasks", cmdTimeout, func(ctx context.Context) (string, error) {
					t, err := resolveTask(ctx, c, pid, target)
					if err != nil {
						return "", err
					}
					if err := c.ReorderTask(ctx, t.ID, pos); err != nil {
						return "", err
					}
					return refreshAndRender(fmt.Sprintf("moved %s to position %d", t.Title, pos),
						func() ([]client.Task, error) { return c.ListTasks(ctx, pid) },
						renderBoard)
				})

			case "run", "stop", "delete":
				cmd := run("Tasks", cmdTimeout, func(ctx context.Context) (string, error) {
					t, err := resolveTask(ctx, c, pid, ref)
					if err != nil {
						return "", err
					}
					switch action {
					case "run":
						err = c.RunTask(ctx, t.ID)
					case "stop":
						err = c.CancelTask(ctx, t.ID)
					case "delete":
						err = c.DeleteTask(ctx, t.ID)
					}
					if err != nil {
						return "", err
					}
					return refreshAndRender(action+": "+t.Title,
						func() ([]client.Task, error) { return c.ListTasks(ctx, pid) },
						renderBoard)
				})
				if action == "delete" {
					return confirmOr(m,
						fmt.Sprintf("Delete task %q? Type 'yes' to confirm or Esc to cancel.", ref),
						fmt.Sprintf("use --force to confirm deletion of task %q", ref),
						cmd)
				}
				return m, cmd

			case "move":
				if len(rest) < 2 {
					return m, errCmd("usage: /tasks move <task> <backlog|active|completed>")
				}
				category := strings.ToLower(rest[len(rest)-1])
				target := strings.Join(rest[:len(rest)-1], " ")
				return m, run("Tasks", cmdTimeout, func(ctx context.Context) (string, error) {
					t, err := resolveTask(ctx, c, pid, target)
					if err != nil {
						return "", err
					}
					if err := c.MoveTask(ctx, t.ID, category); err != nil {
						return "", err
					}
					return refreshAndRender(t.Title+" → "+category,
						func() ([]client.Task, error) { return c.ListTasks(ctx, pid) },
						renderBoard)
				})

			case "goal":
				if len(rest) < 1 {
					return m, errCmd("usage: /tasks goal <task> | <objective>  (objective \"clear\" removes it)")
				}
				target, objective := splitPipe(strings.Join(rest, " "))
				if objective == "" {
					return m, errCmd("usage: /tasks goal <task> | <objective>  (objective \"clear\" removes it)\nhint: separate the task reference and objective with a | character")
				}
				return m, run("Tasks", cmdTimeout, func(ctx context.Context) (string, error) {
					t, err := resolveTask(ctx, c, pid, target)
					if err != nil {
						return "", err
					}
					if strings.EqualFold(objective, "clear") {
						if err := c.ClearTaskGoal(ctx, t.ID); err != nil {
							return "", err
						}
						return "cleared goal on " + t.Title, nil
					}
					if err := c.SetTaskGoal(ctx, t.ID, objective); err != nil {
						return "", err
					}
					return "goal set on " + t.Title + ": " + objective, nil
				})

			case "reply":
				if len(rest) < 1 {
					return m, errCmd("usage: /tasks reply <task> | <message>")
				}
				target, message := splitPipe(strings.Join(rest, " "))
				if message == "" {
					return m, errCmd("usage: /tasks reply <task> | <message>\nhint: separate the task reference and message with a | character")
				}
				return m, run("Tasks", cmdTimeout, func(ctx context.Context) (string, error) {
					t, err := resolveTask(ctx, c, pid, target)
					if err != nil {
						return "", err
					}
					if err := c.SendTaskThreadMessage(ctx, t.ID, message); err != nil {
						return "", err
					}
					return "sent to thread of " + t.Title, nil
				})

			case "activate":
				return m, run("Tasks", cmdTimeout, func(ctx context.Context) (string, error) {
					if err := c.ActivateBacklog(ctx, pid); err != nil {
						return "", err
					}
					return refreshAndRender("activated the backlog",
						func() ([]client.Task, error) { return c.ListTasks(ctx, pid) },
						renderBoard)
				})

			case "sweep":
				return m, run("Tasks", cmdTimeout, func(ctx context.Context) (string, error) {
					if err := c.SweepCompletedTasks(ctx, pid); err != nil {
						return "", err
					}
					return refreshAndRender("swept finished tasks",
						func() ([]client.Task, error) { return c.ListTasks(ctx, pid) },
						renderBoard)
				})

			case "clear":
				column := "completed"
				if len(rest) > 0 {
					column = strings.ToLower(rest[0])
				}
				if column != "backlog" && column != "completed" {
					return m, errCmd("clear which column? backlog or completed")
				}
				cmd := run("Tasks", cmdTimeout, func(ctx context.Context) (string, error) {
					var err error
					switch column {
					case "backlog":
						err = c.ClearBacklogTasks(ctx, pid)
					case "completed":
						err = c.ClearCompletedTasks(ctx, pid)
					}
					if err != nil {
						return "", err
					}
					return refreshAndRender("cleared "+column,
						func() ([]client.Task, error) { return c.ListTasks(ctx, pid) },
						renderBoard)
				})
				return confirmOr(m,
					fmt.Sprintf("Clear all %s tasks? Type 'yes' to confirm or Esc to cancel.", column),
					fmt.Sprintf("use --force to confirm clearing the %s column", column),
					cmd)
			}
			return m, nil
		},
	}
}

func isDetailTab(s string) bool {
	switch strings.ToLower(s) {
	case "details", "thread", "changes", "schedules", "chaining", "attachments", "lifecycle":
		return true
	}
	return false
}

func resolveTask(ctx context.Context, c *client.Client, projectID, ref string) (client.Task, error) {
	tasks, err := c.ListTasks(ctx, projectID)
	if err != nil {
		return client.Task{}, err
	}
	return matchRef(tasks, ref,
		func(t client.Task) string { return t.ID },
		func(t client.Task) string { return t.Title })
}

// --- schedule ---

func scheduleCommand() command {
	actions := []string{"list", "add", "delete", "toggle"}
	return command{
		name:    "schedule",
		aliases: []string{"schedules"},
		args:    "[args]",
		actions: actions,
		desc:    "scheduled/recurring task runs",
		usage: []string{
			"schedule                                   list schedules",
			"schedule add <task> <2006-01-02T15:04> [once|daily|weekly|monthly|seconds|minutes|hours [interval]]",
			"schedule delete <id>                       remove a schedule",
			"schedule toggle <id>                       enable/disable a schedule",
		},
		examples: []string{
			`schedule add "Daily standup report" 2026-01-20T09:00 daily`,
			`schedule add "Weekly metrics" 2026-01-22T08:00 weekly`,
			`schedule toggle a1b2c3`,
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			mm, cmd, ok := m.needProject()
			if !ok {
				return mm, cmd
			}
			action, rest := splitAction(actions, args)
			c, pid := m.client, m.selectedID

			switch action {
			case "", "list":
				return m, run("Schedule", cmdTimeout, func(ctx context.Context) (string, error) {
					entries, summary, err := c.GetSchedule(ctx, pid)
					if err != nil {
						return "", err
					}
					return renderSchedule(entries, summary), nil
				})
			case "add":
				if len(rest) < 2 {
					return m, errCmd("usage: /schedule add <task> <2006-01-02T15:04> [once|daily|weekly|monthly|seconds|minutes|hours [interval]]")
				}
				repeat := "once"
				interval := 1
				if n, err := strconv.Atoi(rest[len(rest)-1]); err == nil && len(rest) >= 3 && isRepeat(rest[len(rest)-2]) {
					interval = n
					rest = rest[:len(rest)-1]
				}
				if isRepeat(rest[len(rest)-1]) {
					repeat = normalizeRepeat(rest[len(rest)-1])
					rest = rest[:len(rest)-1]
				}
				when := rest[len(rest)-1]
				target := strings.Join(rest[:len(rest)-1], " ")
				return m, run("Schedule", cmdTimeout, func(ctx context.Context) (string, error) {
					t, err := resolveTask(ctx, c, pid, target)
					if err != nil {
						return "", err
					}
					if err := c.CreateSchedule(ctx, t.ID, when, repeat, interval); err != nil {
						return "", err
					}
					entries, summary, _ := c.GetSchedule(ctx, pid)
					return "scheduled " + t.Title + " for " + when + " (" + repeat + ")\n\n" + renderSchedule(entries, summary), nil
				})
			case "delete", "toggle":
				ref := strings.Join(rest, " ")
				cmd := run("Schedule", cmdTimeout, func(ctx context.Context) (string, error) {
					entries, _, err := c.GetSchedule(ctx, pid)
					if err != nil {
						return "", err
					}
					e, err := matchRef(entries, ref,
						func(s client.ScheduleEntry) string { return s.ScheduleID },
						func(s client.ScheduleEntry) string { return s.Text })
					if err != nil {
						return "", err
					}
					if e.ScheduleID == "" {
						return "", fmt.Errorf("that task has no schedule")
					}
					if action == "delete" {
						err = c.DeleteSchedule(ctx, e.ScheduleID)
					} else {
						_, err = c.ToggleSchedule(ctx, e.ScheduleID)
					}
					if err != nil {
						return "", err
					}
					entries, summary, _ := c.GetSchedule(ctx, pid)
					return action + "d schedule\n\n" + renderSchedule(entries, summary), nil
				})
				if action == "delete" {
					return confirmOr(m,
						fmt.Sprintf("Delete schedule %q? Type 'yes' to confirm or Esc to cancel.", ref),
						fmt.Sprintf("use --force to confirm deletion of schedule %q", ref),
						cmd)
				}
				return m, cmd
			}
			return m, nil
		},
	}
}

func isRepeat(s string) bool {
	switch strings.ToLower(s) {
	case "once", "daily", "weekly", "monthly", "hourly", "seconds", "minutes", "hours":
		return true
	}
	return false
}

// normalizeRepeat maps user-facing repeat keywords to backend repeat types,
// keeping "hourly" as an accepted synonym for "hours".
func normalizeRepeat(s string) string {
	r := strings.ToLower(s)
	if r == "hourly" {
		return "hours"
	}
	return r
}

// --- alerts ---

func alertsCommand() command {
	actions := []string{"list", "read", "approve", "reject", "dismiss", "delete", "read-all", "clear"}
	return command{
		name:    "alerts",
		aliases: []string{"alert"},
		args:    "[id]",
		actions: actions,
		desc:    "notifications awaiting review",
		usage: []string{
			"alerts [filter]                            list alerts",
			"alerts read|approve|reject|dismiss <alert>",
			"alerts delete <alert>                      delete one alert",
			"alerts read-all                            mark every alert read",
			"alerts clear                               delete every alert",
		},
		examples: []string{
			`alerts approve "Add retry logic to HTTP client"`,
			`alerts reject "Refactor database layer"`,
			`alerts read-all`,
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			action, rest := splitAction(actions, args)
			c, pid := m.client, m.selectedID
			ref := strings.Join(rest, " ")

			switch action {
			case "", "list":
				return m, run("Alerts", cmdTimeout, func(ctx context.Context) (string, error) {
					alerts, err := c.ListAlerts(ctx, pid)
					if err != nil {
						return "", err
					}
					return renderAlerts(alerts, ref), nil
				})
			case "read-all":
				return m, run("Alerts", cmdTimeout, func(ctx context.Context) (string, error) {
					if err := c.MarkAllAlertsRead(ctx, pid); err != nil {
						return "", err
					}
					return refreshAndRender("marked all read",
						func() ([]client.Alert, error) { return c.ListAlerts(ctx, pid) },
						renderAlerts)
				})
			case "clear":
				cmd := run("Alerts", cmdTimeout, func(ctx context.Context) (string, error) {
					if err := c.DeleteAllAlerts(ctx, pid); err != nil {
						return "", err
					}
					return "deleted all alerts", nil
				})
				return confirmOr(m,
					"Delete ALL alerts? Type 'yes' to confirm or Esc to cancel.",
					"use --force to confirm deleting all alerts",
					cmd)
			default:
				cmd := run("Alerts", cmdTimeout, func(ctx context.Context) (string, error) {
					alerts, err := c.ListAlerts(ctx, pid)
					if err != nil {
						return "", err
					}
					a, err := matchRef(alerts, ref,
						func(a client.Alert) string { return a.ID },
						func(a client.Alert) string { return a.Title + " " + a.Text })
					if err != nil {
						return "", err
					}
					if action == "delete" {
						err = c.DeleteAlert(ctx, a.ID, pid)
					} else {
						err = c.AlertAction(ctx, a.ID, action, pid)
					}
					if err != nil {
						return "", err
					}
					return refreshAndRender(action+": "+a.Title,
						func() ([]client.Alert, error) { return c.ListAlerts(ctx, pid) },
						renderAlerts)
				})
				if action == "delete" {
					return confirmOr(m,
						fmt.Sprintf("Delete alert %q? Type 'yes' to confirm or Esc to cancel.", ref),
						fmt.Sprintf("use --force to confirm deletion of alert %q", ref),
						cmd)
				}
				return m, cmd
			}
		},
	}
}

// --- skills ---

func skillsCommand() command {
	actions := []string{"list", "show", "add", "edit", "delete", "enable", "disable", "always"}
	return command{
		name:    "skills",
		aliases: []string{"skill"},
		args:    "[handle]",
		actions: actions,
		desc:    "reusable skills the agents can load",
		usage: []string{
			"skills [filter]                            list skills",
			"skills show <skill>                        show one skill's body",
			"skills add <name> [| <description>] [| <body>]",
			"skills edit <skill> | <new body>           replace a skill's body",
			"skills delete <skill>                      remove a skill",
			"skills enable|disable <skill>              toggle availability",
			"skills always <skill>                      always load this skill",
		},
		examples: []string{
			`skills add retry-logic | Wrap HTTP calls in exponential backoff`,
			`skills edit retry-logic | Always retry on 429 and 503 with jitter up to 60s`,
			`skills always retry-logic`,
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			action, rest := splitAction(actions, args)
			c, pid := m.client, m.selectedID
			ref := strings.Join(rest, " ")

			switch action {
			case "", "list":
				return m, run("Skills", cmdTimeout, func(ctx context.Context) (string, error) {
					skills, err := c.ListSkills(ctx, pid)
					if err != nil {
						return "", err
					}
					return renderSkills(skills, ref), nil
				})
			case "show":
				return m, run("Skill", cmdTimeout, func(ctx context.Context) (string, error) {
					skills, err := c.ListSkills(ctx, pid)
					if err != nil {
						return "", err
					}
					s, err := matchRef(skills, ref,
						func(s client.Skill) string { return s.Handle },
						func(s client.Skill) string { return s.Name })
					if err != nil {
						return "", err
					}
					return renderSkillDetail(s), nil
				})
			case "add":
				if ref == "" {
					return m, errCmd("usage: /skills add <name> [| <description>] [| <body>]")
				}
				parts := strings.SplitN(ref, "|", 3)
				name := strings.TrimSpace(parts[0])
				desc, body := "", ""
				if len(parts) > 1 {
					desc = strings.TrimSpace(parts[1])
				}
				if len(parts) > 2 {
					body = strings.TrimSpace(parts[2])
				}
				return m, run("Skills", cmdTimeout, func(ctx context.Context) (string, error) {
					if err := c.CreateSkill(ctx, pid, name, desc, body); err != nil {
						return "", err
					}
					return refreshAndRender("created skill "+name,
						func() ([]client.Skill, error) { return c.ListSkills(ctx, pid) },
						renderSkills)
				})
			case "edit":
				handle, body := splitPipe(ref)
				if handle == "" || body == "" {
					return m, errCmd("usage: /skills edit <handle> | <new body>")
				}
				return m, run("Skills", cmdTimeout, func(ctx context.Context) (string, error) {
					if err := c.UpdateSkill(ctx, pid, handle, body); err != nil {
						return "", err
					}
					return "updated skill " + handle, nil
				})
			default:
				cmd := run("Skills", cmdTimeout, func(ctx context.Context) (string, error) {
					skills, err := c.ListSkills(ctx, pid)
					if err != nil {
						return "", err
					}
					s, err := matchRef(skills, ref,
						func(s client.Skill) string { return s.Handle },
						func(s client.Skill) string { return s.Name })
					if err != nil {
						return "", err
					}
					switch action {
					case "delete":
						err = c.DeleteSkill(ctx, pid, s.Handle)
					case "enable":
						err = c.SetSkillEnabled(ctx, pid, s.Handle, true)
					case "disable":
						err = c.SetSkillEnabled(ctx, pid, s.Handle, false)
					case "always":
						err = c.SetSkillAlwaysUse(ctx, pid, s.Handle, !s.AlwaysUse)
					}
					if err != nil {
						return "", err
					}
					return refreshAndRender(action+": "+s.Handle,
						func() ([]client.Skill, error) { return c.ListSkills(ctx, pid) },
						renderSkills)
				})
				if action == "delete" {
					return confirmOr(m,
						fmt.Sprintf("Delete skill %q? Type 'yes' to confirm or Esc to cancel.", ref),
						fmt.Sprintf("use --force to confirm deletion of skill %q", ref),
						cmd)
				}
				return m, cmd
			}
		},
	}
}

// --- agents ---

func agentsCommand() command {
	actions := []string{"list", "delete", "generate", "metrics"}
	return command{
		name:    "agents",
		aliases: []string{"agent"},
		args:    "[name]",
		actions: actions,
		desc:    "agent definitions and their metrics",
		usage: []string{
			"agents [filter]                            list agent definitions",
			"agents generate <description>              create an agent from a description",
			"agents delete <agent>                      remove an agent definition",
			"agents metrics                             per-agent workflow metrics",
		},
		examples: []string{
			`agents generate A code reviewer that checks Go PRs for style and correctness`,
			`agents delete reviewer`,
			`agents metrics`,
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			action, rest := splitAction(actions, args)
			c, pid := m.client, m.selectedID
			ref := strings.Join(rest, " ")

			switch action {
			case "", "list":
				return m, run("Agents", cmdTimeout, func(ctx context.Context) (string, error) {
					agents, err := c.ListAgents(ctx, pid)
					if err != nil {
						return "", err
					}
					return renderAgents(agents, ref), nil
				})
			case "metrics":
				return m, run("Agent metrics", cmdTimeout, func(ctx context.Context) (string, error) {
					metrics, err := c.GetAllAgentMetrics(ctx)
					if err != nil {
						return "", err
					}
					best, _ := c.GetBestAgent(ctx, "")
					cheapest, _ := c.GetCheapestAgent(ctx, "")
					return renderAgentMetrics(metrics, best, cheapest), nil
				})
			case "generate":
				if ref == "" {
					return m, errCmd("usage: /agents generate <description>")
				}
				return m, run("Agents", cmdTimeout, func(ctx context.Context) (string, error) {
					if err := c.GenerateAgent(ctx, pid, ref); err != nil {
						return "", err
					}
					return refreshAndRender("generated an agent from your description",
						func() ([]client.AgentDef, error) { return c.ListAgents(ctx, pid) },
						renderAgents)
				})
			case "delete":
				cmd := run("Agents", cmdTimeout, func(ctx context.Context) (string, error) {
					agents, err := c.ListAgents(ctx, pid)
					if err != nil {
						return "", err
					}
					a, err := matchRef(agents, ref,
						func(a client.AgentDef) string { return a.ID },
						func(a client.AgentDef) string { return a.Name + " " + a.Key })
					if err != nil {
						return "", err
					}
					if err := c.DeleteAgent(ctx, a.ID); err != nil {
						return "", err
					}
					return refreshAndRender("deleted "+a.Name,
						func() ([]client.AgentDef, error) { return c.ListAgents(ctx, pid) },
						renderAgents)
				})
				return confirmOr(m,
					fmt.Sprintf("Delete agent %q? Type 'yes' to confirm or Esc to cancel.", ref),
					fmt.Sprintf("use --force to confirm deletion of agent %q", ref),
					cmd)
			}
			return m, nil
		},
	}
}

// --- models ---

func modelsCommand() command {
	actions := []string{"list", "default", "delete", "capacity"}
	return command{
		name:    "models",
		aliases: []string{"model"},
		args:    "[name]",
		actions: actions,
		desc:    "configured LLM models and capacity",
		usage: []string{
			"models [filter]                            list configured models",
			"models default <model>                     set the default model",
			"models delete <model>                      remove a model",
			"models capacity                            per-model capacity and usage",
		},
		examples: []string{
			`models default gpt-4o`,
			`models capacity`,
			`models delete claude-haiku`,
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			action, rest := splitAction(actions, args)
			c, pid := m.client, m.selectedID
			ref := strings.Join(rest, " ")

			switch action {
			case "", "list":
				return m, run("Models", cmdTimeout, func(ctx context.Context) (string, error) {
					list, err := c.ListModels(ctx, pid)
					if err != nil {
						return "", err
					}
					return renderModels(list, ref), nil
				})
			case "capacity":
				return m, run("Model capacity", cmdTimeout, func(ctx context.Context) (string, error) {
					caps, err := c.GetModelCapacities(ctx)
					if err != nil {
						return "", err
					}
					return renderModelCapacity(caps), nil
				})
			default:
				cmd := run("Models", cmdTimeout, func(ctx context.Context) (string, error) {
					list, err := c.ListModels(ctx, pid)
					if err != nil {
						return "", err
					}
					mo, err := matchRef(list, ref,
						func(x client.LLMModel) string { return x.ID },
						func(x client.LLMModel) string { return x.Name + " " + x.Model })
					if err != nil {
						return "", err
					}
					if action == "default" {
						err = c.SetDefaultModel(ctx, mo.ID)
					} else {
						err = c.DeleteModel(ctx, mo.ID)
					}
					if err != nil {
						return "", err
					}
					return refreshAndRender(action+": "+mo.Name,
						func() ([]client.LLMModel, error) { return c.ListModels(ctx, pid) },
						renderModels)
				})
				if action == "delete" {
					return confirmOr(m,
						fmt.Sprintf("Delete model %q? Type 'yes' to confirm or Esc to cancel.", ref),
						fmt.Sprintf("use --force to confirm deletion of model %q", ref),
						cmd)
				}
				return m, cmd
			}
		},
	}
}

// --- workers ---

func workersCommand() command {
	actions := []string{"show", "limit", "project"}
	return command{
		name:    "workers",
		args:    "[limit <n>|project <n>]",
		actions: actions,
		desc:    "worker pool stats and concurrency caps",
		usage: []string{
			"workers                                    show pool stats and settings",
			"workers limit <n>                          set the global worker cap (0 = unlimited)",
			"workers project <n>                        set this project's worker cap (0 = no limit)",
		},
		examples: []string{
			`workers limit 4`,
			`workers project 2`,
			`workers limit 0`,
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			action, rest := splitAction(actions, args)
			c, pid := m.client, m.selectedID
			if action == "limit" || action == "project" {
				if len(rest) == 0 {
					return m, errCmd("usage: /workers " + action + " <n>")
				}
				n := atoiSafe(rest[0])
				if n < 0 {
					return m, errCmd("worker limit must be a positive number")
				}
				if action == "project" {
					mm, cmd, ok := m.needProject()
					if !ok {
						return mm, cmd
					}
					name := m.selectedName
					return m, run("Workers", cmdTimeout, func(ctx context.Context) (string, error) {
						if err := c.SetProjectWorkerLimit(ctx, pid, n); err != nil {
							return "", err
						}
						if n == 0 {
							return fmt.Sprintf("worker limit for %s removed (unlimited)", name), nil
						}
						return fmt.Sprintf("worker limit for %s set to %d", name, n), nil
					})
				}
				return m, run("Workers", cmdTimeout, func(ctx context.Context) (string, error) {
					if err := c.SetGlobalWorkerLimit(ctx, n); err != nil {
						return "", err
					}
					if n == 0 {
						return "global worker limit set to unlimited", nil
					}
					return fmt.Sprintf("global worker limit set to %d", n), nil
				})
			}
			return m, run("Workers", cmdTimeout, func(ctx context.Context) (string, error) {
				text, err := c.GetWorkerSettings(ctx, pid)
				if err != nil {
					return "", err
				}
				capacity, _ := c.GetGlobalCapacity(ctx)
				return renderWorkers(capacity, text), nil
			})
		},
	}
}

// --- channels / personality ---

func channelsCommand() command {
	actions := []string{"list", "test", "remove"}
	return command{
		name:    "channels",
		aliases: []string{"integrations"},
		args:    "[action] [channel]",
		actions: actions,
		desc:    "integrations: Telegram, Slack, Discord, GitHub, email, webhooks",
		usage: []string{
			"channels                                   list configured integrations",
			"channels test <channel>                    send a test message (telegram, slack, discord, email)",
			"channels remove <channel>                  disconnect an integration (telegram, slack, discord, email)",
			"Note: GitHub and Slack OAuth connect/callback require a browser (known parity gap).",
		},
		examples: []string{
			`channels test telegram`,
			`channels test email`,
			`channels remove discord`,
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			action, rest := splitAction(actions, args)
			c, pid := m.client, m.selectedID
			ref := strings.Join(rest, " ")

			switch action {
			case "", "list":
				return m, run("Channels", cmdTimeout, func(ctx context.Context) (string, error) {
					return c.GetChannels(ctx, pid)
				})
			default:
				return m, run("Channels", cmdTimeout, func(ctx context.Context) (string, error) {
					if ref == "" {
						return "", fmt.Errorf("usage: /channels %s <channel>", action)
					}
					ch, err := matchRef(client.KnownChannels, ref,
						func(ch client.Channel) string { return ch.Type },
						func(ch client.Channel) string { return ch.Name })
					if err != nil {
						return "", err
					}
					if err := c.ChannelAction(ctx, ch.Type, action, pid); err != nil {
						return "", err
					}
					status := action + ": " + ch.Name
					text, err := c.GetChannels(ctx, pid)
					if err != nil {
						return status, nil
					}
					return status + "\n\n" + text, nil
				})
			}
		},
	}
}

func personalityCommand() command {
	actions := []string{"show", "set"}
	return command{
		name:    "personality",
		args:    "[set <preset>]",
		actions: actions,
		desc:    "assistant personality presets",
		usage: []string{
			"personality                                show the current personality",
			"personality set <preset>                   switch personality preset",
		},
		examples: []string{
			`personality`,
			`personality set concise`,
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			action, rest := splitAction(actions, args)
			c, pid := m.client, m.selectedID
			if action == "set" {
				if len(rest) == 0 {
					return m, errCmd("usage: /personality set <preset>")
				}
				preset := strings.Join(rest, " ")
				return m, run("Personality", cmdTimeout, func(ctx context.Context) (string, error) {
					if err := c.SavePersonality(ctx, pid, preset); err != nil {
						return "", err
					}
					return "personality set to " + preset, nil
				})
			}
			return m, run("Personality", cmdTimeout, func(ctx context.Context) (string, error) {
				return c.GetPersonality(ctx, pid)
			})
		},
	}
}

// --- pulse / reflection / grades / insights ---

func pulseCommand() command {
	actions := []string{"show", "summary"}
	return command{
		name:    "pulse",
		aliases: []string{"upcoming"},
		actions: actions,
		desc:    "briefing on upcoming work",
		usage: []string{
			"pulse                                      show the upcoming-work briefing",
			"pulse summary                              regenerate the briefing",
		},
		examples: []string{
			`pulse`,
			`pulse summary`,
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			action, _ := splitAction(actions, args)
			c, pid := m.client, m.selectedID
			return m, run("Pulse", cmdTimeout, func(ctx context.Context) (string, error) {
				return generateThenFetch(ctx, "summary", action,
					func(ctx context.Context) error { return c.GeneratePulseSummary(ctx, pid) },
					func(ctx context.Context) (string, error) { return c.GetPulse(ctx, pid) })
			})
		},
	}
}

func reflectionCommand() command {
	actions := []string{"show", "summary"}
	return command{
		name:    "reflection",
		aliases: []string{"history"},
		actions: actions,
		desc:    "debrief on completed work",
		usage: []string{
			"reflection                                 show the completed-work debrief",
			"reflection summary                         regenerate the debrief",
		},
		examples: []string{
			`reflection`,
			`reflection summary`,
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			action, _ := splitAction(actions, args)
			c, pid := m.client, m.selectedID
			return m, run("Reflection", cmdTimeout, func(ctx context.Context) (string, error) {
				return generateThenFetch(ctx, "summary", action,
					func(ctx context.Context) error { return c.GenerateReflectionSummary(ctx, pid) },
					func(ctx context.Context) (string, error) { return c.GetReflection(ctx, pid) })
			})
		},
	}
}

func gradesCommand() command {
	return command{
		name: "grades",
		desc: "grade the project's ideas/backlog quality",
		run: func(m Model, _ []string) (Model, tea.Cmd) {
			c, pid := m.client, m.selectedID
			return m, run("Grades", cmdTimeout, func(ctx context.Context) (string, error) {
				return c.GradeIdeas(ctx, pid)
			})
		},
	}
}

func insightsCommand() command {
	actions := []string{"show", "analyze"}
	return command{
		name:    "insights",
		aliases: []string{"suggestions"},
		actions: actions,
		desc:    "proactive insights and suggestions",
		usage: []string{
			"insights                                   show current insights",
			"insights analyze                           run a fresh analysis pass",
		},
		examples: []string{
			`insights`,
			`insights analyze`,
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			action, _ := splitAction(actions, args)
			c, pid := m.client, m.selectedID
			return m, run("Insights", cmdTimeout, func(ctx context.Context) (string, error) {
				return generateThenFetch(ctx, "analyze", action,
					func(ctx context.Context) error { return c.RunInsightsAnalysis(ctx, pid) },
					func(ctx context.Context) (string, error) { return c.GetInsights(ctx, pid) })
			})
		},
	}
}

func automationsCommand() command {
	actions := []string{"list", "run-now", "pause", "resume", "delete"}
	return command{
		name:    "automations",
		aliases: []string{"automation"},
		args:    "[filter]",
		actions: actions,
		desc:    "recurring automations and workflow rules",
		usage: []string{
			"automations [filter]                       list automations",
			"automations run-now <automation>           trigger an immediate run",
			"automations pause <automation>              pause an active automation",
			"automations resume <automation>             resume a paused automation",
			"automations delete <automation>             remove an automation",
		},
		examples: []string{
			`automations run-now "Nightly sweep"`,
			`automations pause "Nightly sweep"`,
			`automations resume "Nightly sweep"`,
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			action, rest := splitAction(actions, args)
			c, pid := m.client, m.selectedID
			ref := strings.Join(rest, " ")

			switch action {
			case "", "list":
				return m, run("Automations", cmdTimeout, func(ctx context.Context) (string, error) {
					return c.GetAutomations(ctx, pid)
				})
			default:
				cmd := run("Automations", cmdTimeout, func(ctx context.Context) (string, error) {
					if ref == "" {
						return "", fmt.Errorf("usage: /automations %s <automation>", action)
					}
					automations, err := c.ListAutomations(ctx, pid)
					if err != nil {
						return "", err
					}
					a, err := matchRef(automations, ref,
						func(a client.Automation) string { return a.ID },
						func(a client.Automation) string { return a.Name })
					if err != nil {
						return "", err
					}
					err = c.AutomationAction(ctx, a.ID, action, pid)
					if err != nil {
						return "", err
					}
					status := action + ": " + firstNonEmpty(a.Name, a.ID)
					items, err := c.GetAutomations(ctx, pid)
					if err != nil {
						return status, nil
					}
					return status + "\n\n" + items, nil
				})
				if action == "delete" {
					return confirmOr(m,
						fmt.Sprintf("Delete automation %q? Type 'yes' to confirm or Esc to cancel.", ref),
						fmt.Sprintf("use --force to confirm deletion of automation %q", ref),
						cmd)
				}
				return m, cmd
			}
		},
	}
}

// --- analytics ---

func analyticsCommand() command {
	actions := []string{"usage", "rates", "agents", "frequent", "failures", "skills", "trends"}
	return command{
		name:    "analytics",
		aliases: []string{"stats"},
		actions: actions,
		desc:    "usage, cost, success rates and trends",
		usage: []string{
			"analytics                                  every section",
			"analytics usage                            token usage and cost by model",
			"analytics rates                            success/failure rates",
			"analytics agents                           average execution time by agent",
			"analytics frequent                         most frequent tasks",
			"analytics failures                         failed-task patterns",
			"analytics skills                           skill usage and follow-through",
			"analytics trends                           usage trends over time",
		},
		examples: []string{
			`analytics`,
			`analytics usage`,
			`analytics failures`,
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			action, _ := splitAction(actions, args)
			c, pid := m.client, m.selectedID
			return m, run("Analytics", cmdTimeout, func(ctx context.Context) (string, error) {
				return loadAnalytics(ctx, c, pid, action)
			})
		},
	}
}

// --- projects / status / build / events / misc ---

func projectCommand() command {
	return command{
		name: "project",
		args: "<name>",
		desc: "select the active project",
		run: func(m Model, args []string) (Model, tea.Cmd) {
			m.busy = false
			if len(args) == 0 {
				m.append(entry{role: "result", head: "Projects", text: renderProjects(m.projects, nil, m.selectedID)})
				return m, nil
			}
			name := strings.Join(args, " ")
			if len(m.projects) == 0 {
				return m, m.loadProjects(false, name)
			}
			return m.pickProject(name)
		},
	}
}

func projectsCommand() command {
	return command{
		name: "projects",
		desc: "list projects with running/queued counts",
		run: func(m Model, _ []string) (Model, tea.Cmd) {
			m.busy = false
			return m, m.loadProjects(true, "")
		},
	}
}

func statusCommand() command {
	return command{
		name:    "status",
		aliases: []string{"health"},
		desc:    "connection, auth and capacity",
		run: func(m Model, _ []string) (Model, tea.Cmd) {
			m.busy = false
			m.append(entry{role: "result", head: "Status", text: m.renderStatus()})
			return m, nil
		},
	}
}

func buildCommand() command {
	return command{
		name: "build",
		desc: "trigger an autonomous build for the selected project",
		run: func(m Model, _ []string) (Model, tea.Cmd) {
			mm, cmd, ok := m.needProject()
			if !ok {
				return mm, cmd
			}
			c, pid, name := m.client, m.selectedID, m.selectedName
			return m, run("Build", cmdTimeout, func(ctx context.Context) (string, error) {
				if err := c.TriggerAutonomousBuild(ctx, pid); err != nil {
					return "", err
				}
				return "autonomous build triggered for " + name + " (use /events to watch)", nil
			})
		},
	}
}

func eventsCommand() command {
	return command{
		name:    "events",
		aliases: []string{"stream", "log"},
		args:    "[on|off]",
		desc:    "stream live task/chat events",
		run: func(m Model, args []string) (Model, tea.Cmd) {
			m.busy = false
			on := !m.showEvents
			if len(args) > 0 {
				switch strings.ToLower(args[0]) {
				case "on", "true":
					on = true
				case "off", "false":
					on = false
				}
			}
			m.showEvents = on
			state := "off"
			if on {
				state = "on"
			}
			m.append(entry{role: "system", text: "live events " + state})
			return m, nil
		},
	}
}

// chatCommand returns to the project agent, leaving task-thread mode. With
// arguments it also sends that message to the project agent.
func chatCommand() command {
	return command{
		name:    "chat",
		aliases: []string{"back", "leave"},
		args:    "[message]",
		desc:    "return to project chat, or send a message",
		run: func(m Model, args []string) (Model, tea.Cmd) {
			m.busy = false
			if m.threadID != "" {
				title := m.threadTitle
				m.threadID, m.threadTitle = "", ""
				m.input.Placeholder = defaultPlaceholder
				m.append(entry{role: "system", text: "left thread " + title + " — back to project chat"})
			} else if len(args) == 0 {
				m.append(entry{role: "system", text: "already in project chat"})
			}
			if len(args) == 0 {
				return m, nil
			}
			mm, cmd, ok := m.needProject()
			if !ok {
				return mm, cmd
			}
			m.busy = true
			return m, m.sendChat(m.selectedID, strings.Join(args, " "))
		},
	}
}

func clearCommand() command {
	return command{
		name: "clear",
		desc: "clear the conversation (ctrl+l)",
		run: func(m Model, _ []string) (Model, tea.Cmd) {
			m.busy = false
			m.log = nil
			m.refreshTranscript()
			return m, nil
		},
	}
}

func helpCommand() command {
	return command{
		name:    "help",
		aliases: []string{"?", "commands"},
		args:    "[command]",
		desc:    "list commands, or detail one",
		run: func(m Model, args []string) (Model, tea.Cmd) {
			m.busy = false
			if len(args) > 0 {
				if c := lookupCommand(args[0]); c != nil {
					m.append(entry{role: "result", head: c.summary(), text: renderCommandHelp(*c)})
					return m, nil
				}
				m.append(entry{role: "error", text: "no such command: " + args[0]})
				return m, nil
			}
			m.append(entry{role: "result", head: "Commands", text: renderHelp()})
			return m, nil
		},
	}
}

func quitCommand() command {
	return command{
		name:    "quit",
		aliases: []string{"q", "exit"},
		desc:    "exit the TUI",
		run: func(m Model, _ []string) (Model, tea.Cmd) {
			m.quitting = true
			m.Cleanup()
			return m, tea.Quit
		},
	}
}

// --- helpers ---

// pickProject selects a project by exact name, ID, name prefix or, failing
// those, a unique name substring. An ambiguous reference selects nothing and
// lists the candidates so a partial name never silently picks the wrong
// project (e.g. "openvibely" must not land on "OpenVibely Chrome Plugin").
func (m Model) pickProject(name string) (Model, tea.Cmd) {
	p, err := matchProject(m.projects, name)
	if err != nil {
		m.append(entry{role: "error", text: err.Error()})
		return m, nil
	}
	if m.selectedID != p.ID {
		// A task thread belongs to the old project; leave it.
		m.threadID, m.threadTitle = "", ""
		m.input.Placeholder = defaultPlaceholder
	}
	m.selectedID = p.ID
	m.selectedName = p.Name
	m.append(entry{role: "system", text: "active project: " + p.Name})
	return m, nil
}

// matchProject resolves a project reference, preferring the most specific
// match: exact ID, exact name, then ID/name prefix, then name substring.
func matchProject(projects []client.Project, ref string) (client.Project, error) {
	if strings.TrimSpace(ref) == "" {
		var zero client.Project
		return zero, fmt.Errorf("missing project name")
	}
	return matchRef(projects, ref,
		func(p client.Project) string { return p.ID },
		func(p client.Project) string { return p.Name })
}

// errCmd reports a usage error in the transcript.
func errCmd(msg string) tea.Cmd {
	return func() tea.Msg { return resultMsg{err: fmt.Errorf("%s", msg)} }
}

// splitPipe splits "left | right" on the first pipe.
func splitPipe(s string) (string, string) {
	if i := strings.Index(s, "|"); i >= 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
	}
	return strings.TrimSpace(s), ""
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return -1
		}
		n = n*10 + int(r-'0')
	}
	return n
}
