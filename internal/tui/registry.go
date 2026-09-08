package tui

// The slash-command registry: one entry per OpenVibely screen/resource, each
// with the actions the web UI offers for it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
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
		memoryCommand(),
		agentsCommand(),
		modelsCommand(),
		workersCommand(),
		channelsCommand(),
		webhooksCommand(),
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
		loginCommand(),
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

// actAndReloadText consolidates the "act, then reload a preformatted text
// page" sequence. If the reload fails after a successful action, the error is
// swallowed and only the status line is returned.
func actAndReloadText(status string, act func() error, reload func() (string, error)) (string, error) {
	if err := act(); err != nil {
		return "", err
	}
	text, err := reload()
	if err != nil {
		return status, nil
	}
	return status + "\n\n" + text, nil
}

// scheduleMutationOutput returns the confirmed schedule mutation status and,
// when available, the refreshed schedule page. A failed refresh is not an
// action failure: the mutation already succeeded, so return only its status
// instead of rendering nil entries as an authoritative empty schedule.
func scheduleMutationOutput(status string, reload func() ([]client.ScheduleEntry, string, error)) (string, error) {
	entries, summary, err := reload()
	if err != nil {
		return status, nil
	}
	return status + "\n\n" + renderSchedule(entries, summary), nil
}

// taskReviewsOutput fetches and formats the read-only review view for a task.
func taskReviewsOutput(ctx context.Context, c *client.Client, t client.Task) (string, error) {
	reviews, err := c.ListTaskReviews(ctx, t.ID)
	if err != nil {
		return "", err
	}
	if jsonMode {
		return marshalJSON(reviews)
	}
	return renderTaskReviews(t, reviews), nil
}

func taskReviewsOutputForProject(ctx context.Context, c *client.Client, t client.Task, projectID string) (string, error) {
	reviews, err := c.ListTaskReviewsForProject(ctx, t.ID, projectID)
	if err != nil {
		return "", err
	}
	if jsonMode {
		return marshalJSON(reviews)
	}
	return renderTaskReviews(t, reviews), nil
}

// marshalJSON marshals v to a JSON string. When jsonMode is false it is never
// called; callers should guard with `if jsonMode { ... }`.
func marshalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("json: %w", err)
	}
	return string(b), nil
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
	m.pendingConfirmation = &pendingCmd{
		message: displayMsg,
		cmd:     withMessageGeneration(cmd, sessionGenerationOf(m), projectGenerationOf(m)),
	}
	return m, nil
}

// selectorOr routes a ref-less command invocation: in the TUI it opens the
// inline interactive selector; in CLI mode (no interactivity) it keeps the
// original usage error.
func selectorOr(m Model, usage string, sel tea.Cmd) (Model, tea.Cmd) {
	if cliMode {
		return m, errCmd(usage)
	}
	return m, sel
}

// --- tasks ---

// taskSelectorItems converts tasks into selector rows.
func taskSelectorItems(tasks []client.Task) []selectorItem {
	items := make([]selectorItem, 0, len(tasks))
	for _, task := range tasks {
		task := task
		detail := task.Category
		if task.Status != "" {
			detail += " · " + task.Status
		}
		items = append(items, selectorItem{
			ref:          task.ID,
			label:        firstNonEmpty(task.Title, shortID(task.ID)),
			detail:       strings.Trim(detail, " ·"),
			resolvedTask: &task,
		})
	}
	return items
}

// taskSelector opens the inline task picker for a ref-less tasks subcommand.
func taskSelector(m Model, usage, command string, prefill bool) (Model, tea.Cmd) {
	prefillSuffix := ""
	if prefill {
		prefillSuffix = " | "
	}
	return taskSelectorWithSuffix(m, usage, command, prefillSuffix)
}

func taskSelectorWithSuffix(m Model, usage, command, prefillSuffix string) (Model, tea.Cmd) {
	c, pid := m.client, m.selectedID
	return selectorOr(m, usage, selectorForWithSuffix("Tasks", command,
		taskEmptyStateHint, prefillSuffix,
		func(ctx context.Context) ([]selectorItem, error) {
			tasks, err := c.ListTasks(ctx, pid)
			if err != nil {
				return nil, err
			}
			return taskSelectorItems(tasks), nil
		}))
}

func taskDetailCompletionValues() []string {
	var values []string
	for _, tab := range client.TaskDetailTabs() {
		values = append(values, tab.Name)
		for _, alias := range tab.Aliases {
			// An alias that only extends its canonical name makes every useful
			// canonical prefix ambiguous (for example review/reviews).
			if !strings.HasPrefix(alias, tab.Name) {
				values = append(values, alias)
			}
		}
	}
	return values
}

func tasksCommand() command {
	actions := []string{"list", "open", "show", "reviews", "lifecycle", "logs", "attachments", "attach", "attachment", "new", "edit", "run", "stop", "delete", "move", "order", "goal", "reply", "activate", "sweep", "clear"}
	return command{
		name:    "tasks",
		aliases: []string{"task", "t", "board"},
		args:    "[filter|id]",
		actions: actions,
		completions: []commandCompletion{
			{after: []string{"reviews"}, values: []string{"list", "add"}},
			{after: []string{"attachments"}, values: []string{"add", "upload", "list", "show", "delete", "remove"}},
			{after: []string{"attach"}, values: []string{"add", "upload", "list", "show", "delete", "remove"}},
			{after: []string{"attachment"}, values: []string{"add", "upload", "list", "show", "delete", "remove"}},
			{after: []string{"move", "**"}, partialAfter: completionAfterQuotedOperand, values: []string{"backlog", "active", "completed"}},
			{after: []string{"clear"}, values: []string{"backlog", "completed"}},
			{after: []string{"show", "**"}, partialAfter: completionAfterQuotedOperand, values: taskDetailCompletionValues()},
		},
		selectorPaths: [][]string{
			{"open"}, {"show"}, {"reviews"}, {"reviews", "list"}, {"reviews", "add"},
			{"lifecycle"}, {"logs"}, {"edit"}, {"run"}, {"stop"}, {"delete"}, {"move"},
			{"order"}, {"goal"}, {"reply"}, {"attachments"}, {"attachments", "add"},
			{"attachments", "upload"}, {"attachments", "list"}, {"attachments", "show"},
			{"attachments", "delete"}, {"attachments", "remove"},
			{"attach"}, {"attach", "add"}, {"attach", "upload"}, {"attach", "list"}, {"attach", "show"}, {"attach", "delete"}, {"attach", "remove"},
			{"attachment"}, {"attachment", "add"}, {"attachment", "upload"}, {"attachment", "list"}, {"attachment", "show"}, {"attachment", "delete"}, {"attachment", "remove"},
		},
		desc: "the task board and task threads",
		usage: []string{
			"tasks [filter]                             list the board, optionally filtered",
			"tasks open <task>                          enter the task's thread",
			"omit <task> on open/show/reviews/edit/run/stop/delete/move/order/goal/reply/lifecycle/logs → interactive selector",
			"tasks show <task> [tab]                    " + detailTabUsageList(),
			"tasks reviews [list] <task>                list inline review comments",
			"tasks reviews add <task> <file>:<line> <comment>",
			"tasks attachments add <task> <file>...      upload local files",
			"tasks attachments delete <task> <attachment> delete by ID or filename",
			"tasks attach ...                            alias for attachments",
			"tasks lifecycle <task> [execution]         list executions or show ordered events",
			"tasks logs <task> [execution]              alias for lifecycle event logs",
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
		actionUsages: []commandActionUsage{
			{action: "reviews add", args: "<task> <file>:<line> <comment>"},
			{action: "attachments add", args: "<task> <file>...", description: "upload local files"},
			{action: "attachments delete", args: "<task> <attachment>", description: "delete by ID or filename"},
			{action: "new", args: "<title> [| <prompt>]", description: "create a task"},
			{action: "edit", args: "<task> | <title> [| <prompt>]", description: "edit title/prompt"},
		},
		examples: []string{
			`tasks new Fix login bug | Investigate and resolve the OAuth redirect failure`,
			`tasks move "Fix login bug" active`,
			`tasks goal "Fix login bug" | Reproduce on staging then patch the token refresh`,
			`tasks show "Fix login bug" review`,
			`tasks reviews add "Fix login bug" internal/auth.go:42 Handle token refresh errors`,
			`tasks attachments add "Fix login bug" ./fixtures/request.txt ./fixtures/trace.json`,
			`tasks attachments delete "Fix login bug" request.txt`,
			`tasks lifecycle "Fix login bug"`,
			`tasks logs "Fix login bug" execution-id`,
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
					if jsonMode {
						return marshalJSON(tasks)
					}
					return renderBoard(tasks, ref), nil
				})

			case "open":
				m.threadOpenRequestID++
				m.threadRefreshRequestID++
				m.threadReplyPendingRequestID = 0
				if ref == "" {
					return taskSelector(m, "usage: /tasks open <id|title>", "tasks open", false)
				}
				requestID := m.threadOpenRequestID
				return m, func() tea.Msg {
					ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
					defer cancel()
					t, err := resolveTask(ctx, c, pid, ref)
					if err != nil {
						return threadOpenedMsg{requestID: requestID, projectID: pid, err: err}
					}
					body, err := c.GetTaskThread(ctx, t.ID, pid)
					if err != nil {
						return threadOpenedMsg{requestID: requestID, projectID: pid, err: err}
					}
					if body == "" {
						body = dimStyle.Render("(no messages yet)")
					}
					return threadOpenedMsg{
						requestID: requestID,
						projectID: pid,
						taskID:    t.ID,
						title:     firstNonEmpty(t.Title, shortID(t.ID)),
						status:    t.Status,
						body:      body,
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
				if showRef == "" {
					return taskSelector(m, "usage: /tasks show <id|title> [tab]", "tasks show", false)
				}
				return m, run("Task", cmdTimeout, func(ctx context.Context) (string, error) {
					if isCanonicalFullTaskID(showRef) {
						if isReviewTab(tab) || jsonMode {
							d, err := c.GetTaskMetadataForProjectExact(ctx, showRef, pid)
							if err != nil {
								return "", err
							}
							if isReviewTab(tab) {
								return taskReviewsOutputForProject(ctx, c, d.Task, pid)
							}
							return marshalJSON(d.Task)
						}

						d, err := c.GetTaskForProjectExact(ctx, showRef, pid)
						if err != nil {
							if d == nil {
								return "", err
							}
							return renderTaskDetail(d.Task, d, tab), err
						}
						return renderTaskDetail(d.Task, d, tab), nil
					}

					t, err := resolveTask(ctx, c, pid, showRef)
					if err != nil {
						return "", err
					}
					if isReviewTab(tab) {
						return taskReviewsOutput(ctx, c, t)
					}
					if jsonMode {
						return marshalJSON(t)
					}
					d, err := c.GetTaskForProject(ctx, t.ID, pid)
					if err != nil {
						if d == nil {
							return "", err
						}
						// Lazy tab failures return a useful partial detail alongside
						// the error. Render it so successful sections remain visible;
						// the model still receives the typed error for auth/transport
						// state handling and a non-zero CLI result.
						return renderTaskDetail(t, d, tab), err
					}
					return renderTaskDetail(t, d, tab), nil
				})

			case "reviews":
				if len(rest) == 0 {
					return taskSelector(m, "usage: /tasks reviews <task>", "tasks reviews", false)
				}
				reviewAction := "list"
				reviewRest := rest
				if strings.EqualFold(reviewRest[0], "list") {
					reviewRest = reviewRest[1:]
				} else if strings.EqualFold(reviewRest[0], "add") {
					reviewAction = "add"
					reviewRest = reviewRest[1:]
				}
				switch reviewAction {
				case "list":
					reviewRef := strings.Join(reviewRest, " ")
					if reviewRef == "" {
						return taskSelector(m, "usage: /tasks reviews <task>", "tasks reviews", false)
					}
					return m, run("Task Reviews", cmdTimeout, func(ctx context.Context) (string, error) {
						t, err := resolveTask(ctx, c, pid, reviewRef)
						if err != nil {
							return "", err
						}
						return taskReviewsOutput(ctx, c, t)
					})
				case "add":
					if len(reviewRest) == 0 {
						return taskSelectorWithSuffix(m, commandUsage("tasks", "reviews add"), "tasks reviews add", " ")
					}
					if len(reviewRest) < 3 || len(reviewLocationCandidates(reviewRest)) == 0 {
						return m, errCmd(commandUsage("tasks", "reviews add"))
					}
					return m, run("Task Reviews", cmdTimeout, func(ctx context.Context) (string, error) {
						tasks, err := m.reviewTaskCandidates(ctx, c, pid)
						if err != nil {
							return "", err
						}
						t, location, err := resolveReviewLocation(tasks, reviewRest)
						if err != nil {
							return "", err
						}
						commentText := strings.TrimSpace(strings.Join(reviewRest[location.index+1:], " "))
						if commentText == "" {
							return "", fmt.Errorf("%s", commandUsage("tasks", "reviews add"))
						}
						form := client.ReviewCommentForm{FilePath: location.filePath, LineNumber: location.lineNumber, LineType: "new", CommentText: commentText}
						reviews, err := c.AddTaskReviewComment(ctx, t.ID, form)
						if err != nil {
							return "", err
						}
						added := addedReviewComment(reviews, form)
						if jsonMode {
							return marshalJSON(added)
						}
						return fmt.Sprintf("added review comment on %s:%d for %s\n\n%s", sanitizeAutomationDetailText(location.filePath), location.lineNumber, sanitizeAutomationDetailText(firstNonEmpty(t.Title, shortID(t.ID))), renderTaskReviews(t, reviews)), nil
					})
				}

			case "attachments", "attach", "attachment":
				return taskAttachmentsCommand(m, c, pid, rest)

			case "lifecycle", "logs":
				if len(rest) == 0 {
					return taskSelector(m, "usage: /tasks "+action+" <task> [execution]", "tasks "+action, false)
				}
				return m, lifecycleCommand(c, pid, action, rest)

			case "new":
				if ref == "" {
					return m, errCmd(commandUsage("tasks", "new"))
				}
				title, prompt := splitPipe(ref)
				if title == "" {
					return m, errCmd(commandUsage("tasks", "new"))
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
				if ref == "" {
					return taskSelector(m, commandUsage("tasks", "edit"), "tasks edit", true)
				}
				if newTitle == "" {
					return m, errCmd(commandUsage("tasks", "edit"))
				}
				title, prompt := splitPipe(newTitle)
				if title == "" {
					return m, errCmd(commandUsage("tasks", "edit"))
				}
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
				if len(rest) == 0 {
					return taskSelectorWithSuffix(m, "usage: /tasks order <task> <position>", "tasks order", " ")
				}
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
				if ref == "" {
					return taskSelector(m, "usage: /tasks "+action+" <task>", "tasks "+action, false)
				}
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
				if len(rest) == 0 {
					return taskSelectorWithSuffix(m, "usage: /tasks move <task> <backlog|active|completed>", "tasks move", " ")
				}
				if len(rest) < 2 {
					if !cliMode {
						return optionSelector(m, "Task column", "tasks move "+strings.Join(rest, " "),
							"usage: /tasks move <task> <backlog|active|completed>", registryCompletionValues("tasks", "move", rest[0]))
					}
					return m, errCmd("usage: /tasks move <task> <backlog|active|completed>")
				}
				category := strings.ToLower(rest[len(rest)-1])
				if category != "backlog" && category != "active" && category != "completed" {
					if !cliMode {
						pendingRest := rest
						if len(matchingActions(registryCompletionValues("tasks", append([]string{"move"}, rest[:len(rest)-1]...)...), category)) > 0 {
							pendingRest = rest[:len(rest)-1]
						}
						return optionSelector(m, "Task column", "tasks move "+strings.Join(pendingRest, " "),
							"usage: /tasks move <task> <backlog|active|completed>", registryCompletionValues("tasks", append([]string{"move"}, pendingRest...)...))
					}
					return m, errCmd("usage: /tasks move <task> <backlog|active|completed>")
				}
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
					return taskSelector(m, "usage: /tasks goal <task> | <objective>  (objective \"clear\" removes it)", "tasks goal", true)
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
					return taskSelector(m, "usage: /tasks reply <task> | <message>", "tasks reply", true)
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
				if len(rest) == 0 && !cliMode {
					return optionSelector(m, "Task column", "tasks clear", "clear which column? backlog or completed",
						registryCompletionValues("tasks", "clear"))
				}
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

func detailTabNames() []string {
	tabs := client.TaskDetailTabs()
	names := make([]string, 0, len(tabs))
	for _, tab := range tabs {
		names = append(names, tab.Name)
	}
	return names
}

func detailTabUsageList() string {
	return strings.Join(detailTabNames(), ", ")
}

func detailTabHintList() string {
	return strings.Join(detailTabNames(), "|")
}

func isDetailTab(s string) bool {
	_, ok := client.TaskDetailTabByName(s)
	return ok
}

func isReviewTab(s string) bool {
	tab, ok := client.TaskDetailTabByName(s)
	return ok && tab.Name == "review"
}

type reviewLocation struct {
	index      int
	filePath   string
	lineNumber int
}

func reviewLocationCandidates(args []string) []reviewLocation {
	var candidates []reviewLocation
	for i := 1; i < len(args)-1; i++ {
		filePath, lineNumber, ok := parseReviewLocation(args[i])
		if !ok || strings.TrimSpace(strings.Join(args[i+1:], " ")) == "" {
			continue
		}
		candidates = append(candidates, reviewLocation{index: i, filePath: filePath, lineNumber: lineNumber})
	}
	return candidates
}

// resolveReviewLocation finds the longest task-reference prefix that precedes a
// valid file:line operand. Resolving all candidates is important because an
// unquoted task title can itself contain a location-shaped token. A later
// ambiguous candidate is retained as an error rather than allowing a shorter
// prefix to select a potentially wrong task and location.
func resolveReviewLocation(tasks []client.Task, args []string) (client.Task, reviewLocation, error) {
	var zero client.Task
	var empty reviewLocation
	candidates := reviewLocationCandidates(args)
	if len(candidates) == 0 {
		return zero, empty, fmt.Errorf("missing task reference, review location, or comment")
	}

	var bestTask client.Task
	var bestLocation reviewLocation
	bestIndex := -1
	var bestAmbiguous error
	bestAmbiguousIndex := -1
	var bestOtherErr error
	bestOtherErrIndex := -1
	for _, location := range candidates {
		ref := strings.Join(args[:location.index], " ")
		task, err := matchRef(tasks, ref,
			func(t client.Task) string { return t.ID },
			func(t client.Task) string { return t.Title })
		if err == nil {
			if location.index > bestIndex {
				bestTask = task
				bestLocation = location
				bestIndex = location.index
			}
			continue
		}
		if strings.Contains(err.Error(), "ambiguous") {
			if location.index > bestAmbiguousIndex {
				bestAmbiguous = err
				bestAmbiguousIndex = location.index
			}
			continue
		}
		if location.index > bestOtherErrIndex {
			bestOtherErr = err
			bestOtherErrIndex = location.index
		}
	}

	if bestAmbiguousIndex > bestIndex {
		return zero, empty, bestAmbiguous
	}
	if bestIndex >= 0 {
		return bestTask, bestLocation, nil
	}
	if bestAmbiguous != nil {
		return zero, empty, bestAmbiguous
	}
	if bestOtherErr != nil {
		return zero, empty, bestOtherErr
	}
	return zero, empty, fmt.Errorf("nothing matches a task reference")
}

func parseReviewLocation(s string) (string, int, bool) {
	idx := strings.LastIndex(s, ":")
	if idx <= 0 || idx == len(s)-1 {
		return "", 0, false
	}
	lineNumber, err := strconv.Atoi(s[idx+1:])
	if err != nil || lineNumber <= 0 {
		return "", 0, false
	}
	return strings.TrimSpace(s[:idx]), lineNumber, strings.TrimSpace(s[:idx]) != ""
}

func addedReviewComment(reviews []client.ReviewComment, form client.ReviewCommentForm) client.ReviewComment {
	for i := len(reviews) - 1; i >= 0; i-- {
		r := reviews[i]
		if r.FilePath == form.FilePath && r.LineNumber == form.LineNumber && r.CommentText == form.CommentText {
			return r
		}
	}
	if len(reviews) > 0 {
		return reviews[len(reviews)-1]
	}
	return client.ReviewComment{
		FilePath:    form.FilePath,
		LineNumber:  form.LineNumber,
		LineType:    form.LineType,
		CommentText: form.CommentText,
	}
}

func isCanonicalFullTaskID(ref string) bool {
	if len(ref) != 32 {
		return false
	}
	for i := 0; i < len(ref); i++ {
		if (ref[i] < '0' || ref[i] > '9') && (ref[i] < 'a' || ref[i] > 'f') {
			return false
		}
	}
	return true
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

func resolvePersonality(ctx context.Context, c *client.Client, projectID, ref string) (client.Personality, error) {
	personalities, err := c.ListPersonalities(ctx, projectID)
	if err != nil {
		return client.Personality{}, err
	}
	return matchRef(personalities, ref,
		func(p client.Personality) string { return p.Key },
		func(p client.Personality) string { return p.Name })
}

func (m Model) reviewTaskCandidates(ctx context.Context, c *client.Client, projectID string) ([]client.Task, error) {
	if m.reviewPrefillTask != nil &&
		m.reviewPrefillProjectID == projectID &&
		strings.EqualFold(strings.TrimSpace(m.reviewPrefillTask.ID), strings.TrimSpace(m.reviewPrefillTaskRef)) {
		return []client.Task{*m.reviewPrefillTask}, nil
	}
	return c.ListTasks(ctx, projectID)
}

func taskAttachmentsCommand(m Model, c *client.Client, projectID string, args []string) (Model, tea.Cmd) {
	usageAdd := commandUsage("tasks", "attachments add")
	usageDelete := commandUsage("tasks", "attachments delete")
	if len(args) == 0 {
		return taskSelectorWithSuffix(m, usageAdd, "tasks attachments add", " ")
	}

	action := strings.ToLower(args[0])
	rest := args[1:]
	switch action {
	case "add", "upload":
		return taskAttachmentsAddCommand(m, c, projectID, rest, usageAdd)
	case "delete", "remove":
		return taskAttachmentsDeleteCommand(m, c, projectID, rest, usageDelete)
	case "list", "show":
		return taskAttachmentsListCommand(m, c, projectID, rest)
	default:
		// Keep the short form useful for one-shot commands while documenting the
		// explicit "attachments add" form: /tasks attachments <task> <file>...
		return taskAttachmentsAddCommand(m, c, projectID, args, usageAdd)
	}
}

func taskAttachmentsAddCommand(m Model, c *client.Client, projectID string, args []string, usage string) (Model, tea.Cmd) {
	if len(args) == 0 {
		return taskSelectorWithSuffix(m, usage, "tasks attachments add", " ")
	}
	if len(args) < 2 {
		return m, errCmd(usage)
	}

	return m, run("Task Attachments", cmdTimeout, func(ctx context.Context) (string, error) {
		tasks, err := c.ListTasks(ctx, projectID)
		if err != nil {
			return "", err
		}
		task, filePaths, err := resolveTaskWithOperands(tasks, args, 1)
		if err != nil {
			return "", err
		}
		attachments, err := c.AddTaskAttachments(ctx, task.ID, projectID, filePaths)
		if err != nil {
			return "", err
		}
		if jsonMode {
			return marshalJSON(attachments)
		}
		return fmt.Sprintf("uploaded %d attachment(s) to %s\n\n%s", len(filePaths), firstNonEmpty(task.Title, task.ID), renderTaskAttachments(attachments)), nil
	})
}

func taskAttachmentsDeleteCommand(m Model, c *client.Client, projectID string, args []string, usage string) (Model, tea.Cmd) {
	if len(args) == 0 {
		return taskSelectorWithSuffix(m, usage, "tasks attachments delete", " ")
	}
	if len(args) == 1 {
		return taskAttachmentSelector(m, c, projectID, args[0], usage)
	}
	if !cliMode {
		return m, resolveTaskAttachmentTarget(c, projectID, args)
	}

	cmd := run("Task Attachments", cmdTimeout, func(ctx context.Context) (string, error) {
		task, attachment, err := lookupTaskAttachmentTarget(ctx, c, projectID, args)
		if err != nil {
			return "", err
		}
		return deleteTaskAttachmentResult(ctx, c, projectID, task, attachment)
	})
	attachmentDisplay := strings.TrimSpace(args[len(args)-1])
	taskDisplay := strings.TrimSpace(strings.Join(args[:len(args)-1], " "))
	return confirmOr(m,
		fmt.Sprintf("Delete attachment %q from task %q? Type 'yes' to confirm or Esc to cancel.", attachmentDisplay, taskDisplay),
		fmt.Sprintf("use --force to confirm deletion of attachment %q", attachmentDisplay),
		cmd)
}

func lookupTaskAttachmentTarget(ctx context.Context, c *client.Client, projectID string, args []string) (client.Task, client.Attachment, error) {
	var zeroTask client.Task
	var zeroAttachment client.Attachment
	tasks, err := c.ListTasks(ctx, projectID)
	if err != nil {
		return zeroTask, zeroAttachment, err
	}
	task, attachmentRefParts, err := resolveTaskWithOperands(tasks, args, 1)
	if err != nil {
		return task, zeroAttachment, err
	}
	attachmentRef := strings.TrimSpace(strings.Join(attachmentRefParts, " "))
	if attachmentRef == "" {
		return task, zeroAttachment, fmt.Errorf("missing attachment ID or filename")
	}
	attachments, err := c.ListTaskAttachments(ctx, task.ID, projectID)
	if err != nil {
		return task, zeroAttachment, err
	}
	attachment, err := matchRef(attachments, attachmentRef,
		func(a client.Attachment) string { return a.ID },
		func(a client.Attachment) string { return a.FileName })
	return task, attachment, err
}

func resolveTaskAttachmentTarget(c *client.Client, projectID string, args []string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		defer cancel()
		task, attachment, err := lookupTaskAttachmentTarget(ctx, c, projectID, args)
		return attachmentDeleteTargetMsg{
			projectID:  projectID,
			task:       task,
			attachment: attachment,
			err:        err,
		}
	}
}

func taskAttachmentsListCommand(m Model, c *client.Client, projectID string, args []string) (Model, tea.Cmd) {
	if len(args) == 0 {
		listUsage := commandUsage("tasks", "attachments list")
		return taskSelectorWithSuffix(m, listUsage, "tasks attachments list", " ")
	}
	return m, run("Task Attachments", cmdTimeout, func(ctx context.Context) (string, error) {
		task, err := resolveTask(ctx, c, projectID, strings.Join(args, " "))
		if err != nil {
			return "", err
		}
		attachments, err := c.ListTaskAttachments(ctx, task.ID, projectID)
		if err != nil {
			return "", err
		}
		if jsonMode {
			return marshalJSON(attachments)
		}
		return renderTaskAttachments(attachments), nil
	})
}

func taskAttachmentSelector(m Model, c *client.Client, projectID, taskRef, usage string) (Model, tea.Cmd) {
	if cliMode {
		return m, errCmd(usage)
	}
	command := "tasks attachments delete " + taskRef
	return m, selectorFor("Attachments", command, attachmentEmptyStateHint, false,
		func(ctx context.Context) ([]selectorItem, error) {
			task, err := resolveTask(ctx, c, projectID, taskRef)
			if err != nil {
				return nil, err
			}
			attachments, err := c.ListTaskAttachments(ctx, task.ID, projectID)
			if err != nil {
				return nil, err
			}
			items := make([]selectorItem, 0, len(attachments))
			for _, attachment := range attachments {
				attachment := attachment
				item := selectorItem{
					ref:    attachment.ID,
					label:  firstNonEmpty(attachment.FileName, attachment.ID),
					detail: attachmentSizeText(attachment.FileSize),
				}
				item.dispatch = func(mm Model) (Model, tea.Cmd) {
					cmd := run("Task Attachments", cmdTimeout, func(ctx context.Context) (string, error) {
						return deleteTaskAttachmentResult(ctx, c, projectID, task, attachment)
					})
					return confirmOr(mm,
						fmt.Sprintf("Delete attachment %q from task %q? Type 'yes' to confirm or Esc to cancel.", attachment.FileName, firstNonEmpty(task.Title, task.ID)),
						fmt.Sprintf("use --force to confirm deletion of attachment %q", attachment.FileName),
						cmd)
				}
				items = append(items, item)
			}
			return items, nil
		})
}

func deleteTaskAttachmentResult(ctx context.Context, c *client.Client, projectID string, task client.Task, attachment client.Attachment) (string, error) {
	remaining, err := c.DeleteTaskAttachment(ctx, attachment.ID, projectID)
	if err != nil {
		return "", err
	}
	if jsonMode {
		return marshalJSON(remaining)
	}
	return fmt.Sprintf("deleted attachment %q from %s\n\n%s", attachment.FileName, firstNonEmpty(task.Title, task.ID), renderTaskAttachments(remaining)), nil
}

// resolveTaskWithOperands finds the longest task-reference prefix that matches
// a task, leaving the remaining operands for files or attachment selectors.
// This preserves multi-word task titles while keeping file names and IDs as
// ordinary trailing arguments.
func resolveTaskWithOperands(tasks []client.Task, args []string, trailing int) (client.Task, []string, error) {
	var zero client.Task
	if len(args) <= trailing {
		return zero, nil, fmt.Errorf("missing task reference or attachment operand")
	}
	maxRefWords := len(args) - trailing
	var matched client.Task
	matchedAt := 0
	for end := 1; end <= maxRefWords; end++ {
		task, err := matchRef(tasks, strings.Join(args[:end], " "),
			func(t client.Task) string { return t.ID },
			func(t client.Task) string { return t.Title })
		if err == nil {
			matched = task
			matchedAt = end
		}
	}
	if matchedAt == 0 {
		task, err := matchRef(tasks, strings.Join(args[:maxRefWords], " "),
			func(t client.Task) string { return t.ID },
			func(t client.Task) string { return t.Title })
		return task, nil, err
	}
	return matched, args[matchedAt:], nil
}

// lifecycleCommand resolves a task and its optional execution, then either
// renders the execution page or, only for an explicit execution reference, the
// ordered event trace. Interactive pages with several executions use the
// execution selector; CLI mode always renders the page deterministically.
func lifecycleCommand(c *client.Client, projectID, action string, args []string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		defer cancel()

		tasks, err := c.ListTasks(ctx, projectID)
		if err != nil {
			return resultMsg{title: "Task Lifecycle", err: err}
		}
		task, executionRef, err := resolveLifecycleRefs(tasks, args)
		if err != nil {
			return resultMsg{title: "Task Lifecycle", err: err}
		}

		page, err := c.ListTaskLifecycleExecutionPageForProject(ctx, task.ID, projectID)
		if err != nil {
			return resultMsg{title: "Task Lifecycle", err: err}
		}
		execs := page.Items

		if executionRef != "" {
			execution, err := matchLifecycleExecution(execs, executionRef)
			if err != nil {
				return resultMsg{title: "Task Lifecycle", err: err}
			}
			return lifecycleEventsMessage(ctx, c, projectID, task, execution)
		}

		if jsonMode {
			body, err := marshalJSON(page)
			return resultMsg{title: "Task Lifecycle", body: body, err: err}
		}
		if cliMode || len(execs) <= 1 {
			return resultMsg{title: "Task Lifecycle", body: renderLifecycleExecutionPage(task, page)}
		}

		items := make([]selectorItem, 0, len(execs))
		for _, execution := range execs {
			label := sanitizeAutomationDetailText(firstNonEmpty(execution.SkillKey, execution.ID, "(unnamed execution)"))
			detail := sanitizeAutomationDetailText(execution.Status)
			if execution.StartedAt != "" {
				detail = strings.TrimSpace(detail + " · " + sanitizeAutomationDetailText(execution.StartedAt))
			}
			items = append(items, selectorItem{ref: execution.ID, label: truncate(label, 64), detail: truncate(detail, 96)})
		}
		title := "Lifecycle Executions"
		if page.HasMore {
			title += " (more available)"
		}
		return selectorActiveMsg{
			title:     title,
			command:   "tasks " + action + " " + task.ID,
			emptyHint: "no lifecycle executions for " + firstNonEmpty(task.Title, task.ID),
			items:     items,
		}
	}
}

func lifecycleEventsMessage(ctx context.Context, c *client.Client, projectID string, task client.Task, execution client.LifecycleExecution) tea.Msg {
	events, err := c.GetLifecycleExecutionEventsForProject(ctx, execution.ID, projectID)
	if err != nil {
		return resultMsg{title: "Task Lifecycle", err: err}
	}
	if jsonMode {
		body, err := marshalJSON(nonNilSlice(events))
		return resultMsg{title: "Task Lifecycle", body: body, err: err}
	}
	return resultMsg{title: "Task Lifecycle", body: renderLifecycleEvents(task, execution, events)}
}

func resolveLifecycleRefs(tasks []client.Task, args []string) (client.Task, string, error) {
	if len(args) == 0 {
		return client.Task{}, "", fmt.Errorf("usage: /tasks lifecycle <task> [execution]")
	}

	fullRef := strings.Join(args, " ")
	task, fullErr := matchRef(tasks, fullRef,
		func(t client.Task) string { return t.ID },
		func(t client.Task) string { return t.Title })
	if fullErr == nil {
		return task, "", nil
	}
	if strings.Contains(fullErr.Error(), "ambiguous") {
		return client.Task{}, "", fullErr
	}

	var ambiguous error
	for i := len(args) - 1; i > 0; i-- {
		taskRef := strings.Join(args[:i], " ")
		task, err := matchRef(tasks, taskRef,
			func(t client.Task) string { return t.ID },
			func(t client.Task) string { return t.Title })
		if err == nil {
			return task, strings.Join(args[i:], " "), nil
		}
		if ambiguous == nil && strings.Contains(err.Error(), "ambiguous") {
			ambiguous = err
		}
	}
	if ambiguous != nil {
		return client.Task{}, "", ambiguous
	}
	return client.Task{}, "", fullErr
}

func matchLifecycleExecution(execs []client.LifecycleExecution, ref string) (client.LifecycleExecution, error) {
	return matchRef(execs, ref,
		func(e client.LifecycleExecution) string { return e.ID },
		func(e client.LifecycleExecution) string { return e.SkillKey })
}

func nonNilSlice[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}

// --- schedule ---

func parseScheduleEdit(args []string) (string, client.ScheduleUpdate, error) {
	usage := commandUsage("schedule", "edit")
	optionAt := -1
	for i := 1; i < len(args); i++ {
		if isScheduleEditSetting(args[i]) {
			optionAt = i
			break
		}
	}
	if optionAt < 1 {
		return "", client.ScheduleUpdate{}, fmt.Errorf("%s", usage)
	}
	update, _, err := parseScheduleEditOptions(args[optionAt:], usage)
	if err != nil {
		return "", client.ScheduleUpdate{}, err
	}
	return strings.Join(args[:optionAt], " "), update, nil
}

func validateScheduleArgs(args []string) error {
	action, rest := splitAction([]string{"list", "show", "open", "add", "edit", "delete", "toggle"}, args)
	if action == "" && len(args) > 0 || action == "list" && len(rest) > 0 {
		return fmt.Errorf("usage: /schedule [list|show|open|add|edit|delete|toggle]")
	}
	if (action == "show" || action == "open") && len(rest) == 0 {
		return fmt.Errorf("%s", commandUsage("schedule", action))
	}
	if action == "edit" {
		_, _, err := parseScheduleEdit(rest)
		return err
	}
	return nil
}

func isScheduleEditSetting(value string) bool {
	switch strings.ToLower(value) {
	case "run-at", "repeat", "interval", "clear-context":
		return true
	default:
		return false
	}
}

func parseScheduleEditOptions(args []string, usage string) (client.ScheduleUpdate, int, error) {
	var update client.ScheduleUpdate
	seen := make(map[string]bool)
	parsedPairs := 0
	for i := 0; i < len(args); i += 2 {
		if i+1 >= len(args) {
			return client.ScheduleUpdate{}, parsedPairs, fmt.Errorf("%s", usage)
		}
		key, value := strings.ToLower(args[i]), args[i+1]
		if seen[key] || !isScheduleEditSetting(key) {
			return client.ScheduleUpdate{}, parsedPairs, fmt.Errorf("%s", usage)
		}
		seen[key] = true
		switch key {
		case "run-at":
			if _, err := time.Parse("2006-01-02T15:04", value); err != nil {
				return client.ScheduleUpdate{}, parsedPairs, fmt.Errorf("run time must use 2006-01-02T15:04")
			}
			update.RunAt = &value
		case "repeat":
			value = strings.ToLower(value)
			if !isRepeat(value) {
				return client.ScheduleUpdate{}, parsedPairs, fmt.Errorf("unknown repeat type %q", value)
			}
			value = client.NormalizeScheduleRepeat(value)
			update.RepeatType = &value
		case "interval":
			interval, err := strconv.Atoi(value)
			if err != nil || interval < 1 || interval > 365 {
				return client.ScheduleUpdate{}, parsedPairs, fmt.Errorf("repeat interval must be between 1 and 365")
			}
			update.RepeatInterval = &interval
		case "clear-context":
			var clear bool
			switch strings.ToLower(value) {
			case "true":
				clear = true
			case "false":
				clear = false
			default:
				return client.ScheduleUpdate{}, parsedPairs, fmt.Errorf("clear-context must be true or false")
			}
			update.ClearContextOnStart = &clear
		}
		parsedPairs++
	}
	return update, parsedPairs, nil
}

func applyScheduleUpdate(config client.ScheduleConfig, update client.ScheduleUpdate) client.ScheduleConfig {
	if update.RunAt != nil {
		config.RunAt = *update.RunAt
	}
	if update.RepeatType != nil {
		config.RepeatType = *update.RepeatType
	}
	if update.RepeatInterval != nil {
		config.RepeatInterval = *update.RepeatInterval
	}
	if update.ClearContextOnStart != nil {
		config.ClearContextOnStart = *update.ClearContextOnStart
	}
	return config
}

func getBoundScheduleTask(ctx context.Context, c *client.Client, projectID, taskID string) (*client.Task, error) {
	if taskID == "" {
		return nil, nil
	}
	detail, err := c.GetTaskMetadataForProjectExact(ctx, taskID, projectID)
	if err != nil {
		if client.IsNotFoundError(err) {
			return nil, nil
		}
		return nil, err
	}
	return &detail.Task, nil
}

func scheduleInspectionCommand(c *client.Client, projectID string, entry client.ScheduleEntry) tea.Cmd {
	return run("Schedule", cmdTimeout, func(ctx context.Context) (string, error) {
		boundTask, err := getBoundScheduleTask(ctx, c, projectID, entry.TaskID)
		if err != nil {
			return "", err
		}
		if jsonMode {
			return marshalJSON(scheduleInspection{Schedule: entry, Task: boundTask})
		}
		return renderScheduleInspection(entry, boundTask), nil
	})
}

func scheduleCommand() command {
	actions := []string{"list", "show", "open", "add", "edit", "delete", "toggle"}
	return command{
		name:         "schedule",
		aliases:      []string{"schedules"},
		args:         "[args]",
		actions:      actions,
		validateArgs: validateScheduleArgs,
		completions: []commandCompletion{
			{after: []string{"add", "*", "**"}, partialAfter: completionAfterScheduleTimestamp, values: []string{"once", "daily", "weekly", "monthly", "seconds", "minutes", "hours"}},
			{after: []string{"edit", "*"}, values: []string{"run-at", "repeat", "interval", "clear-context"}},
			{after: []string{"edit", "*", "repeat"}, values: []string{"once", "daily", "weekly", "monthly", "hourly", "seconds", "minutes", "hours"}},
			{after: []string{"edit", "*", "clear-context"}, values: []string{"true", "false"}},
		},
		selectorPaths: [][]string{{"show"}, {"open"}, {"add"}, {"edit"}, {"delete"}, {"toggle"}},
		desc:          "scheduled/recurring task runs",
		usage: []string{
			"schedule                                   list schedules",
			"schedule show <id|name>                    inspect a schedule and its bound task",
			"schedule open <id|name>                    compatibility alias for show",
			"omit <id|name> on show/open → interactive selector",
			"omit <task> on add → interactive selector",
			"schedule delete <id>                       remove a schedule",
			"schedule edit <id> <setting> <value> [...] update a schedule",
			"schedule toggle <id>                       enable/disable a schedule",
			"omit <id> on edit/delete/toggle → interactive selector",
		},
		actionUsages: []commandActionUsage{
			{action: "show", args: "<id|name>", description: "inspect a schedule and its bound task"},
			{action: "open", args: "<id|name>", description: "compatibility alias for show"},
			{action: "add", args: "<task> <2006-01-02T15:04> [once|daily|weekly|monthly|seconds|minutes|hours [interval]]"},
			{action: "edit", args: "<id> [run-at <2006-01-02T15:04>] [repeat <once|daily|weekly|monthly|hourly|seconds|minutes|hours>] [interval <1..365>] [clear-context <true|false>]"},
		},
		examples: []string{
			`schedule add "Daily standup report" 2026-01-20T09:00 daily`,
			`schedule add "Weekly metrics" 2026-01-22T08:00 weekly`,
			`schedule edit a1b2c3 run-at 2026-01-22T10:30 repeat weekly interval 2 clear-context false`,
			`schedule toggle a1b2c3`,
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			mm, cmd, ok := m.needProject()
			if !ok {
				return mm, cmd
			}
			action, rest := splitAction(actions, args)
			c, pid := m.client, m.selectedID
			if (action == "" && len(rest) > 0) || (action == "list" && len(rest) > 0) {
				return m, errCmd("usage: /schedule [list|show|open|add|edit|delete|toggle]")
			}

			switch action {
			case "", "list":
				return m, run("Schedule", cmdTimeout, func(ctx context.Context) (string, error) {
					entries, summary, err := c.GetSchedule(ctx, pid)
					if err != nil {
						return "", err
					}
					if jsonMode {
						return marshalJSON(entries)
					}
					return renderSchedule(entries, summary), nil
				})
			case "show", "open":
				ref := strings.Join(rest, " ")
				if ref == "" {
					return selectorOr(m, commandUsage("schedule", action),
						selectorFor("Schedule", "schedule "+action, scheduleEmptyStateHint, false, func(ctx context.Context) ([]selectorItem, error) {
							entries, _, err := c.GetSchedule(ctx, pid)
							if err != nil {
								return nil, err
							}
							items := make([]selectorItem, 0, len(entries))
							for _, entry := range entries {
								if entry.ScheduleID == "" {
									continue
								}
								entry := entry
								items = append(items, selectorItem{
									ref:    entry.ScheduleID,
									label:  firstNonEmpty(entry.Name, entry.Text, shortID(entry.ScheduleID)),
									detail: "task " + firstNonEmpty(shortID(entry.TaskID), "unavailable"),
									dispatch: func(m Model) (Model, tea.Cmd) {
										m.busy = true
										return m, scheduleInspectionCommand(c, pid, entry)
									},
								})
							}
							return items, nil
						}))
				}
				return m, run("Schedule", cmdTimeout, func(ctx context.Context) (string, error) {
					entries, _, err := c.GetSchedule(ctx, pid)
					if err != nil {
						return "", err
					}
					entry, err := matchRefWithDisplay(entries, ref,
						func(s client.ScheduleEntry) string { return s.ScheduleID },
						func(s client.ScheduleEntry) string { return s.Name },
						sanitizeAutomationDetailText)
					if err != nil {
						return "", err
					}
					boundTask, err := getBoundScheduleTask(ctx, c, pid, entry.TaskID)
					if err != nil {
						return "", err
					}
					if jsonMode {
						return marshalJSON(scheduleInspection{Schedule: entry, Task: boundTask})
					}
					return renderScheduleInspection(entry, boundTask), nil
				})
			case "add":
				if len(rest) == 0 {
					return taskSelectorWithSuffix(m, commandUsage("schedule", "add"), "schedule add", " ")
				}
				if len(rest) < 2 {
					return m, errCmd(commandUsage("schedule", "add"))
				}
				repeat := "once"
				interval := 1
				if n, err := strconv.Atoi(rest[len(rest)-1]); err == nil && len(rest) >= 3 && isRepeat(rest[len(rest)-2]) {
					interval = n
					rest = rest[:len(rest)-1]
				}
				if interval < 1 || interval > 365 {
					return m, errCmd("repeat interval must be between 1 and 365")
				}
				if isRepeat(rest[len(rest)-1]) {
					repeat = client.NormalizeScheduleRepeat(strings.ToLower(rest[len(rest)-1]))
					rest = rest[:len(rest)-1]
				}
				when := rest[len(rest)-1]
				target := strings.Join(rest[:len(rest)-1], " ")
				return m, run("Schedule", cmdTimeout, func(ctx context.Context) (string, error) {
					t, err := resolveTask(ctx, c, pid, target)
					if err != nil {
						return "", err
					}
					if err := c.CreateSchedule(ctx, pid, t.ID, when, repeat, interval); err != nil {
						return "", err
					}
					return scheduleMutationOutput("scheduled "+t.Title+" for "+when+" ("+repeat+")",
						func() ([]client.ScheduleEntry, string, error) { return c.GetSchedule(ctx, pid) })
				})
			case "edit":
				if len(rest) == 0 {
					return selectorOr(m, commandUsage("schedule", "edit"),
						selectorForWithSuffix("Schedule", "schedule edit", scheduleEmptyStateHint, " ", func(ctx context.Context) ([]selectorItem, error) {
							entries, _, err := c.GetSchedule(ctx, pid)
							if err != nil {
								return nil, err
							}
							items := make([]selectorItem, 0, len(entries))
							for _, e := range entries {
								if e.ScheduleID != "" {
									items = append(items, selectorItem{ref: e.ScheduleID, label: firstNonEmpty(e.Name, e.Text, shortID(e.ScheduleID))})
								}
							}
							return items, nil
						}))
				}
				ref, update, err := parseScheduleEdit(rest)
				if err != nil {
					return m, errCmd(err.Error())
				}
				return m, run("Schedule", cmdTimeout, func(ctx context.Context) (string, error) {
					entries, _, err := c.GetSchedule(ctx, pid)
					if err != nil {
						return "", err
					}
					entry, err := matchRef(entries, ref,
						func(s client.ScheduleEntry) string { return s.ScheduleID },
						func(s client.ScheduleEntry) string { return s.Name })
					if err != nil {
						return "", err
					}
					if entry.ScheduleID == "" || entry.TaskID == "" {
						return "", fmt.Errorf("that task has no schedule")
					}
					config, err := c.GetTaskSchedule(ctx, pid, entry.TaskID, entry.ScheduleID)
					if err != nil {
						return "", err
					}
					if err := c.UpdateSchedule(ctx, config, update); err != nil {
						return "", err
					}
					updated := applyScheduleUpdate(config, update)
					if jsonMode {
						_, _, _ = c.GetSchedule(ctx, pid)
						return marshalJSON(updated)
					}
					return scheduleMutationOutput("updated schedule "+entry.ScheduleID,
						func() ([]client.ScheduleEntry, string, error) { return c.GetSchedule(ctx, pid) })
				})
			case "delete", "toggle":
				ref := strings.Join(rest, " ")
				if ref == "" {
					return selectorOr(m, "usage: /schedule "+action+" <id>",
						selectorFor("Schedule", "schedule "+action,
							scheduleEmptyStateHint, false, func(ctx context.Context) ([]selectorItem, error) {
								entries, _, err := c.GetSchedule(ctx, pid)
								if err != nil {
									return nil, err
								}
								items := make([]selectorItem, 0, len(entries))
								for _, e := range entries {
									if e.ScheduleID == "" {
										continue
									}
									e := e
									item := selectorItem{
										ref:   e.ScheduleID,
										label: firstNonEmpty(e.Name, e.Text, shortID(e.ScheduleID)),
									}
									item.dispatch = func(m Model) (Model, tea.Cmd) {
										cmd := run("Schedule", cmdTimeout, func(ctx context.Context) (string, error) {
											var err error
											if action == "delete" {
												err = c.DeleteSchedule(ctx, pid, e.ScheduleID)
											} else {
												_, err = c.ToggleSchedule(ctx, pid, e.ScheduleID)
											}
											if err != nil {
												return "", err
											}
											return scheduleMutationOutput(action+"d schedule",
												func() ([]client.ScheduleEntry, string, error) { return c.GetSchedule(ctx, pid) })
										})
										if action == "delete" {
											return confirmOr(m,
												fmt.Sprintf("Delete schedule %q? Type 'yes' to confirm or Esc to cancel.", e.ScheduleID),
												fmt.Sprintf("use --force to confirm deletion of schedule %q", e.ScheduleID),
												cmd)
										}
										m.busy = true
										return m, cmd
									}
									items = append(items, item)
								}
								return items, nil
							}))
				}
				cmd := run("Schedule", cmdTimeout, func(ctx context.Context) (string, error) {
					entries, _, err := c.GetSchedule(ctx, pid)
					if err != nil {
						return "", err
					}
					e, err := matchRef(entries, ref,
						func(s client.ScheduleEntry) string { return s.ScheduleID },
						func(s client.ScheduleEntry) string { return s.Name })
					if err != nil {
						return "", err
					}
					if e.ScheduleID == "" {
						return "", fmt.Errorf("that task has no schedule")
					}
					if action == "delete" {
						err = c.DeleteSchedule(ctx, pid, e.ScheduleID)
					} else {
						_, err = c.ToggleSchedule(ctx, pid, e.ScheduleID)
					}
					if err != nil {
						return "", err
					}
					return scheduleMutationOutput(action+"d schedule",
						func() ([]client.ScheduleEntry, string, error) { return c.GetSchedule(ctx, pid) })
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

// --- alerts ---

func alertInspectionOutput(ctx context.Context, c *client.Client, projectID string, alert client.Alert) (string, error) {
	detail, err := c.GetAlertDetail(ctx, alert.ID, projectID)
	if err != nil {
		return "", err
	}
	inspection := client.AlertInspection{
		Summary: alertSummaryFor(alert, projectID),
		Detail:  *detail,
	}
	if jsonMode {
		return marshalJSON(inspection)
	}
	return renderAlertInspection(inspection), nil
}

func alertDeleteOutput(status string, alerts []client.Alert) (string, error) {
	if jsonMode {
		return marshalJSON(alerts)
	}
	return status + "\n\n" + renderAlerts(alerts, ""), nil
}

func alertsCommand() command {
	actions := []string{"list", "show", "read", "approve", "reject", "dismiss", "delete", "read-all", "clear"}
	return command{
		name:          "alerts",
		aliases:       []string{"alert"},
		args:          "[id]",
		actions:       actions,
		selectorPaths: [][]string{{"show"}, {"read"}, {"approve"}, {"reject"}, {"dismiss"}, {"delete"}},
		desc:          "notifications awaiting review",
		usage: []string{
			"alerts [filter]                            list alerts",
			"alerts show <alert>                         inspect full body and metadata",
			"alerts read|approve|reject|dismiss <alert>",
			"alerts delete <alert>                      delete one alert",
			"omit <alert> on show/read/approve/reject/dismiss/delete → interactive selector",
			"alerts read-all                            mark every alert read",
			"alerts clear                               delete every alert",
		},
		actionUsages: []commandActionUsage{
			{action: "show", args: "<id|title>", description: "inspect full alert context"},
		},
		examples: []string{
			`alerts show "Add retry logic to HTTP client"`,
			`alerts approve "Add retry logic to HTTP client"`,
			`alerts reject "Refactor database layer"`,
			`alerts read-all`,
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
				return m, run("Alerts", cmdTimeout, func(ctx context.Context) (string, error) {
					alerts, err := c.ListAlerts(ctx, pid)
					if err != nil {
						return "", err
					}
					if jsonMode {
						return marshalJSON(alerts)
					}
					return renderAlerts(alerts, ref), nil
				})
			case "show":
				if ref == "" {
					return selectorOr(m, commandUsage("alerts", "show"),
						selectorFor("Alerts", "alerts show", "no alerts in the current project", false,
							func(ctx context.Context) ([]selectorItem, error) {
								alerts, err := c.ListAlerts(ctx, pid)
								if err != nil {
									return nil, err
								}
								items := make([]selectorItem, 0, len(alerts))
								for _, alert := range alerts {
									a := alert
									item := selectorItem{
										ref:    a.ID,
										label:  firstNonEmpty(a.Title, a.Message, a.Text, shortID(a.ID)),
										detail: strings.Join(a.Badges, " "),
									}
									item.dispatch = func(m Model) (Model, tea.Cmd) {
										m.busy = true
										return m, run("Alert", cmdTimeout, func(ctx context.Context) (string, error) {
											return alertInspectionOutput(ctx, c, pid, a)
										})
									}
									items = append(items, item)
								}
								return items, nil
							}))
				}
				return m, run("Alert", cmdTimeout, func(ctx context.Context) (string, error) {
					alerts, err := c.ListAlerts(ctx, pid)
					if err != nil {
						return "", err
					}
					a, err := matchRef(alerts, ref,
						func(a client.Alert) string { return a.ID },
						func(a client.Alert) string { return a.Title })
					if err != nil {
						return "", err
					}
					return alertInspectionOutput(ctx, c, pid, a)
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
				if ref == "" {
					return selectorOr(m, "usage: /alerts "+action+" <alert>",
						selectorFor("Alerts", "alerts "+action,
							"no pending alerts in the current project", false,
							func(ctx context.Context) ([]selectorItem, error) {
								alerts, err := c.ListAlerts(ctx, pid)
								if err != nil {
									return nil, err
								}
								items := make([]selectorItem, 0, len(alerts))
								for _, a := range alerts {
									a := a
									item := selectorItem{
										ref:    a.ID,
										label:  firstNonEmpty(a.Title, a.Message, a.Text, shortID(a.ID)),
										detail: strings.Join(a.Badges, " "),
									}
									item.dispatch = func(m Model) (Model, tea.Cmd) {
										cmd := run("Alerts", cmdTimeout, func(ctx context.Context) (string, error) {
											if action == "delete" {
												alerts, err := c.DeleteAlertAndList(ctx, a.ID, pid)
												if err != nil {
													return "", err
												}
												return alertDeleteOutput("delete: "+a.Title, alerts)
											}
											if err := c.AlertAction(ctx, a.ID, action, pid); err != nil {
												return "", err
											}
											return refreshAndRender(action+": "+a.Title,
												func() ([]client.Alert, error) { return c.ListAlerts(ctx, pid) },
												renderAlerts)
										})
										if action == "delete" {
											return confirmOr(m,
												fmt.Sprintf("Delete alert %q? Type 'yes' to confirm or Esc to cancel.", a.ID),
												fmt.Sprintf("use --force to confirm deletion of alert %q", a.ID),
												cmd)
										}
										m.busy = true
										return m, cmd
									}
									items = append(items, item)
								}
								return items, nil
							}))
				}
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
						refreshed, err := c.DeleteAlertAndList(ctx, a.ID, pid)
						if err != nil {
							return "", err
						}
						return alertDeleteOutput("delete: "+a.Title, refreshed)
					}
					if err := c.AlertAction(ctx, a.ID, action, pid); err != nil {
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

// skillSelector opens the inline skill picker for a ref-less skills subcommand.
func skillSelector(m Model, usage, command string, prefill bool) (Model, tea.Cmd) {
	c, pid := m.client, m.selectedID
	return selectorOr(m, usage, selectorFor("Skills", command,
		skillEmptyStateHint, prefill,
		func(ctx context.Context) ([]selectorItem, error) {
			skills, err := c.ListSkills(ctx, pid)
			if err != nil {
				return nil, err
			}
			items := make([]selectorItem, 0, len(skills))
			for _, s := range skills {
				items = append(items, selectorItem{
					ref:    s.Handle,
					label:  firstNonEmpty(s.Name, s.Handle),
					detail: truncate(s.Description, 40),
				})
			}
			return items, nil
		}))
}

func skillsCommand() command {
	actions := []string{"list", "show", "add", "edit", "delete", "enable", "disable", "always", "load"}
	return command{
		name:          "skills",
		aliases:       []string{"skill"},
		args:          "[handle]",
		actions:       actions,
		selectorPaths: [][]string{{"show"}, {"edit"}, {"delete"}, {"enable"}, {"disable"}, {"always"}, {"load"}},
		desc:          "reusable skills the agents can load",
		usage: []string{
			"skills [filter]                            list skills",
			"skills show <skill>                        show one skill's body",
			"skills delete <skill>                      remove a skill",
			"skills enable|disable <skill>              toggle availability",
			"skills always|load <skill>                 always load this skill",
			"omit <skill> on show/edit/delete/enable/disable/always/load → interactive selector",
		},
		actionUsages: []commandActionUsage{
			{action: "add", args: "<name> [| <description>] [| <body>]"},
			{action: "edit", args: "<skill> | <new body>", description: "replace a skill's body"},
		},
		examples: []string{
			`skills add retry-logic | Wrap HTTP calls in exponential backoff`,
			`skills edit retry-logic | Always retry on 429 and 503 with jitter up to 60s`,
			`skills load retry-logic`,
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
				return m, run("Skills", cmdTimeout, func(ctx context.Context) (string, error) {
					skills, err := c.ListSkills(ctx, pid)
					if err != nil {
						return "", err
					}
					if jsonMode {
						return marshalJSON(skills)
					}
					return renderSkills(skills, ref), nil
				})
			case "show":
				if ref == "" {
					return skillSelector(m, "usage: /skills show <skill>", "skills show", false)
				}
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
					return m, errCmd(commandUsage("skills", "add"))
				}
				parts := strings.SplitN(ref, "|", 3)
				name := strings.TrimSpace(parts[0])
				if name == "" {
					return m, errCmd(commandUsage("skills", "add"))
				}
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
				ref2, body := splitPipe(ref)
				if ref2 == "" {
					return skillSelector(m, commandUsage("skills", "edit"), "skills edit", true)
				}
				if body == "" {
					return m, errCmd(commandUsage("skills", "edit"))
				}
				return m, run("Skills", cmdTimeout, func(ctx context.Context) (string, error) {
					skills, err := c.ListSkills(ctx, pid)
					if err != nil {
						return "", err
					}
					s, err := matchRef(skills, ref2,
						func(s client.Skill) string { return s.Handle },
						func(s client.Skill) string { return s.Name })
					if err != nil {
						return "", err
					}
					if err := c.UpdateSkill(ctx, pid, s.Handle, s.Scope, s.Name, s.Description, s.Enabled, body); err != nil {
						return "", err
					}
					return "updated skill " + s.Handle, nil
				})
			default:
				if ref == "" {
					return skillSelector(m, "usage: /skills "+action+" <skill>", "skills "+action, false)
				}
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
						err = c.DeleteSkill(ctx, pid, s.Handle, s.Scope)
					case "enable":
						err = c.SetSkillEnabled(ctx, pid, s.Handle, s.Scope, true)
					case "disable":
						err = c.SetSkillEnabled(ctx, pid, s.Handle, s.Scope, false)
					case "always", "load":
						err = c.SetSkillAlwaysUse(ctx, pid, s.Handle, s.Scope, true)
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

// --- memory ---

func memorySelector(m Model, usage string) (Model, tea.Cmd) {
	project := m.selectedProject()
	return selectorOr(m, usage, selectorForWithWarnings("Memory", "memory show",
		"no indexed project memory — MEMORIES.md has no topic files",
		func(ctx context.Context) ([]selectorItem, []string, error) {
			list, err := m.client.ListMemories(ctx, project)
			items := make([]selectorItem, 0, len(list.Memories))
			for _, memory := range list.Memories {
				label := firstNonEmpty(memory.Title, memory.File)
				detail := memory.File
				if memory.Summary != "" {
					detail += " · " + truncate(memory.Summary, 50)
				}
				items = append(items, selectorItem{ref: memory.File, label: label, detail: detail})
			}
			return items, list.Warnings, err
		}))
}

func memoryCommand() command {
	actions := []string{"list", "show", "search"}
	return command{
		name:          "memory",
		aliases:       []string{"memories"},
		args:          "[file|query]",
		actions:       actions,
		selectorPaths: [][]string{{"show"}},
		desc:          "read-only durable memory for the selected project",
		usage: []string{
			"memory [filter]                            list indexed memory files",
			"memory is read-only; curation remains owned by the backend lifecycle tools",
		},
		actionUsages: []commandActionUsage{
			{action: "list", args: "[filter]", description: "list indexed memory files"},
			{action: "show", args: "<file|title>", description: "show one indexed memory file"},
			{action: "search", args: "<query>", description: "search indexed files and bodies"},
		},
		examples: []string{`memory list`,
			`memory show managed_memory.md`,
			`memory search "selected project"`,
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			mm, cmd, ok := m.needProject()
			if !ok {
				return mm, cmd
			}
			action, rest := splitAction(actions, args)
			c, project := m.client, m.selectedProject()
			ref := strings.Join(rest, " ")

			switch action {
			case "", "list":
				return m, run("Memory", cmdTimeout, func(ctx context.Context) (string, error) {
					list, err := c.ListMemories(ctx, project)
					list = filterMemoryList(list, ref)
					if jsonMode {
						body, marshalErr := marshalJSON(list)
						if marshalErr != nil {
							return "", marshalErr
						}
						return body, err
					}
					return renderMemoryListForFilter(list, ref), err
				})
			case "show":
				if ref == "" {
					return memorySelector(m, commandUsage("memory", "show"))
				}
				return m, run("Memory", cmdTimeout, func(ctx context.Context) (string, error) {
					document, err := c.ShowMemory(ctx, project, ref)
					if jsonMode {
						body, marshalErr := marshalJSON(document)
						if marshalErr != nil {
							return "", marshalErr
						}
						return body, err
					}
					body := renderMemoryDocument(document)
					return body, err
				})
			case "search":
				if strings.TrimSpace(ref) == "" {
					return m, errCmd(commandUsage("memory", "search"))
				}
				return m, run("Memory Search", cmdTimeout, func(ctx context.Context) (string, error) {
					result, err := c.SearchMemories(ctx, project, ref)
					if jsonMode {
						body, marshalErr := marshalJSON(result)
						if marshalErr != nil {
							return "", marshalErr
						}
						return body, err
					}
					return renderMemorySearch(result), err
				})
			default:
				return m, errCmd(commandUsage("memory", action))
			}
		},
	}
}

func filterMemoryList(list client.MemoryList, filter string) client.MemoryList {
	filtered := client.MemoryList{
		Memories: make([]client.Memory, 0, len(list.Memories)),
		Warnings: append(make([]string, 0, len(list.Warnings)), list.Warnings...),
	}
	for _, memory := range list.Memories {
		if filterMatch(filter, memory.File, memory.Title, memory.Summary) {
			filtered.Memories = append(filtered.Memories, memory)
		}
	}
	return filtered
}

// --- agents ---

type agentEdit struct {
	Name                *string
	Description         *string
	SystemPrompt        *string
	Model               *string
	Key                 *string
	Scope               *string
	Enabled             *bool
	SelectableAsPrimary *bool
}

func isAgentEditField(value string) bool {
	switch strings.ToLower(value) {
	case "name", "description", "system-prompt", "model", "key", "scope", "enabled", "selectable":
		return true
	default:
		return false
	}
}

func parseAgentEdit(args []string) (string, agentEdit, error) {
	usage := commandUsage("agents", "edit")
	optionAt := -1
	for i := 1; i < len(args); i++ {
		if isAgentEditField(args[i]) {
			optionAt = i
			break
		}
	}
	if optionAt < 1 {
		return "", agentEdit{}, fmt.Errorf("%s", usage)
	}
	var update agentEdit
	seen := map[string]bool{}
	for i := optionAt; i < len(args); i += 2 {
		if i+1 >= len(args) || !isAgentEditField(args[i]) || seen[strings.ToLower(args[i])] {
			return "", agentEdit{}, fmt.Errorf("%s", usage)
		}
		key, value := strings.ToLower(args[i]), args[i+1]
		seen[key] = true
		switch key {
		case "name":
			if strings.TrimSpace(value) == "" {
				return "", agentEdit{}, errors.New("agent name cannot be empty")
			}
			update.Name = &value
		case "description":
			update.Description = &value
		case "system-prompt":
			update.SystemPrompt = &value
		case "model":
			if strings.TrimSpace(value) == "" {
				return "", agentEdit{}, errors.New("agent model cannot be empty")
			}
			update.Model = &value
		case "key":
			if strings.TrimSpace(value) == "" {
				return "", agentEdit{}, errors.New("agent key cannot be empty")
			}
			update.Key = &value
		case "scope":
			value = strings.ToLower(value)
			if value != "global" && value != "project" {
				return "", agentEdit{}, errors.New("agent scope must be global or project")
			}
			update.Scope = &value
		case "enabled", "selectable":
			var parsed bool
			switch strings.ToLower(value) {
			case "true":
				parsed = true
			case "false":
				parsed = false
			default:
				return "", agentEdit{}, fmt.Errorf("agent %s must be true or false", key)
			}
			if key == "enabled" {
				update.Enabled = &parsed
			} else {
				update.SelectableAsPrimary = &parsed
			}
		}
	}
	return strings.Join(args[:optionAt], " "), update, nil
}

func matchAgentRef(agents []client.AgentDef, ref string) (client.AgentDef, error) {
	var zero client.AgentDef
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return zero, errors.New("missing id or name")
	}
	lower := strings.ToLower(ref)
	ambiguous := func(hits []client.AgentDef) error {
		names := make([]string, 0, len(hits))
		for _, hit := range hits {
			names = append(names, sanitizeAutomationDetailText(firstNonEmpty(hit.Name, hit.Key, hit.ID)))
		}
		return fmt.Errorf("%q is ambiguous: %s — use the full name or ID", sanitizeAutomationDetailText(ref), strings.Join(names, ", "))
	}
	matchTier := func(matches func(client.AgentDef) bool) (client.AgentDef, bool, error) {
		hits := make([]client.AgentDef, 0)
		for _, agent := range agents {
			if matches(agent) {
				hits = append(hits, agent)
			}
		}
		switch len(hits) {
		case 0:
			return zero, false, nil
		case 1:
			return hits[0], true, nil
		default:
			return zero, true, ambiguous(hits)
		}
	}
	for _, tier := range []func(client.AgentDef) bool{
		func(agent client.AgentDef) bool { return strings.EqualFold(agent.ID, ref) },
		func(agent client.AgentDef) bool { return strings.EqualFold(agent.Name, ref) },
		func(agent client.AgentDef) bool { return strings.EqualFold(agent.Key, ref) },
		func(agent client.AgentDef) bool {
			return strings.HasPrefix(strings.ToLower(agent.ID), lower) ||
				strings.HasPrefix(strings.ToLower(agent.Name), lower) ||
				strings.HasPrefix(strings.ToLower(agent.Key), lower)
		},
		func(agent client.AgentDef) bool {
			return strings.Contains(strings.ToLower(agent.Name), lower) ||
				strings.Contains(strings.ToLower(agent.Key), lower)
		},
	} {
		if agent, matched, err := matchTier(tier); matched {
			return agent, err
		}
	}
	return zero, fmt.Errorf("nothing matches %q", ref)
}

type agentEditPartialResult struct {
	Saved        bool   `json:"saved"`
	RefreshError string `json:"refresh_error"`
}

func validateAgentModel(models []client.LLMModel, value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.EqualFold(value, "inherit") {
		return "inherit", nil
	}
	for _, model := range models {
		if model.Model == value {
			return value, nil
		}
	}
	return "", fmt.Errorf("unknown agent model %q; use inherit or an exact configured model value", value)
}

func applyAgentEdit(agent client.AgentDefinition, update agentEdit) client.AgentDefinition {
	if update.Name != nil {
		agent.Name = *update.Name
	}
	if update.Description != nil {
		agent.Description = *update.Description
	}
	if update.SystemPrompt != nil {
		agent.SystemPrompt = *update.SystemPrompt
	}
	if update.Model != nil {
		agent.Model = *update.Model
	}
	if update.Key != nil {
		agent.Key = *update.Key
	}
	if update.Scope != nil {
		agent.Scope = *update.Scope
	}
	if update.Enabled != nil {
		agent.Enabled = *update.Enabled
	}
	if update.SelectableAsPrimary != nil {
		agent.SelectableAsPrimary = *update.SelectableAsPrimary
	}
	return agent
}

func validateAgentArgs(args []string) error {
	action, rest := splitAction([]string{"list", "edit", "delete", "generate", "metrics", "votes"}, args)
	if action == "edit" && len(rest) > 0 {
		_, _, err := parseAgentEdit(rest)
		return err
	}
	return nil
}

func resolveAgentDeletion(c *client.Client, projectID, ref string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		defer cancel()
		agents, err := c.ListAgents(ctx, projectID)
		if err != nil {
			return agentDeleteTargetMsg{projectID: projectID, err: err}
		}
		agent, err := matchAgentRef(agents, ref)
		return agentDeleteTargetMsg{projectID: projectID, agent: agent, err: err}
	}
}

func confirmAgentDeletion(m Model, projectID string, agent client.AgentDef) (Model, tea.Cmd) {
	name := sanitizeAutomationDetailText(firstNonEmpty(agent.Name, agent.Key, agent.ID))
	c := m.client
	cmd := run("Agents", cmdTimeout, func(ctx context.Context) (string, error) {
		if err := c.DeleteAgent(ctx, agent.ID); err != nil {
			return "", err
		}
		return refreshAndRender("deleted "+name,
			func() ([]client.AgentDef, error) { return c.ListAgents(ctx, projectID) },
			renderAgents)
	})
	return confirmOr(m,
		fmt.Sprintf("Delete agent %q? Type 'yes' to confirm or Esc to cancel.", name),
		fmt.Sprintf("use --force to confirm deletion of agent %q", name),
		cmd)
}

func agentsCommand() command {
	actions := []string{"list", "edit", "delete", "generate", "metrics", "votes"}
	return command{
		name:         "agents",
		aliases:      []string{"agent"},
		args:         "[name]",
		actions:      actions,
		validateArgs: validateAgentArgs,
		completions: []commandCompletion{
			{after: []string{"edit", "*"}, values: []string{"name", "description", "system-prompt", "model", "key", "scope", "enabled", "selectable"}},
			{after: []string{"edit", "*", "scope"}, values: []string{"global", "project"}},
			{after: []string{"edit", "*", "enabled"}, values: []string{"true", "false"}},
			{after: []string{"edit", "*", "selectable"}, values: []string{"true", "false"}},
		},
		selectorPaths: [][]string{{"edit"}, {"delete"}},
		desc:          "agent definitions, workflow metrics and vote audits",
		usage: []string{
			"agents [filter]                            list agent definitions",
			"agents generate <description>              create an agent from a description",
			"agents delete <agent>                      remove an agent definition (omit <agent> → interactive selector)",
			"agents metrics                             per-agent workflow metrics",
		},
		actionUsages: []commandActionUsage{
			{action: "edit", args: "<agent> <field> <value> [...]", description: "edit name, prompt, model, identity, scope, or state"},
			{action: "votes", args: "<step-execution-id>", description: "inspect parallel-step votes"},
		},
		examples: []string{
			`agents edit reviewer description "Reviews Go changes" enabled true`,
			`agents generate A code reviewer that checks Go PRs for style and correctness`,
			`agents delete reviewer`,
			`agents metrics`,
			`agents votes step-exec-123`,
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			if len(args) == 0 || !strings.EqualFold(args[0], "metrics") {
				mm, cmd, ok := m.needProject()
				if !ok {
					return mm, cmd
				}
				m = mm
			}
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
					if jsonMode {
						return marshalJSON(agents)
					}
					return renderAgents(agents, ref), nil
				})
			case "metrics":
				return m, run("Agent metrics", cmdTimeout, func(ctx context.Context) (string, error) {
					var (
						metrics    []client.AgentMetric
						best       *client.AgentRecommendation
						cheapest   *client.AgentRecommendation
						metricsErr error
					)
					var wg sync.WaitGroup
					wg.Add(3)
					go func() { defer wg.Done(); metrics, metricsErr = c.GetAllAgentMetrics(ctx) }()
					go func() { defer wg.Done(); best, _ = c.GetBestAgent(ctx, "") }()
					go func() { defer wg.Done(); cheapest, _ = c.GetCheapestAgent(ctx, "") }()
					wg.Wait()
					if metricsErr != nil {
						return "", metricsErr
					}
					return renderAgentMetrics(metrics, best, cheapest), nil
				})
			case "votes":
				if len(rest) != 1 || strings.TrimSpace(rest[0]) == "" {
					return m, errCmd(commandUsage("agents", "votes"))
				}
				stepExecID := strings.TrimSpace(rest[0])
				return m, run("Workflow votes", cmdTimeout, func(ctx context.Context) (string, error) {
					records, err := c.GetVoteRecords(ctx, stepExecID)
					if err != nil {
						return "", err
					}
					if jsonMode {
						return marshalJSON(records)
					}
					return renderVoteRecords(stepExecID, records), nil
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
			case "edit":
				if len(rest) == 0 {
					return selectorOr(m, commandUsage("agents", "edit"),
						selectorForWithSuffix("Agents", "agents edit",
							"no agent definitions — /agents generate <description> creates one", " ",
							func(ctx context.Context) ([]selectorItem, error) {
								agents, err := c.ListAgents(ctx, pid)
								if err != nil {
									return nil, err
								}
								items := make([]selectorItem, 0, len(agents))
								for _, a := range agents {
									items = append(items, selectorItem{ref: a.ID, label: firstNonEmpty(a.Name, a.Key, shortID(a.ID)), detail: truncate(a.Description, 40)})
								}
								return items, nil
							}))
				}
				ref, edit, err := parseAgentEdit(rest)
				if err != nil {
					return m, errCmd(err.Error())
				}
				return m, run("Agents", cmdTimeout, func(ctx context.Context) (string, error) {
					agents, err := c.ListAgents(ctx, pid)
					if err != nil {
						return "", err
					}
					matched, err := matchAgentRef(agents, ref)
					if err != nil {
						return "", err
					}
					if edit.Model != nil {
						if strings.EqualFold(strings.TrimSpace(*edit.Model), "inherit") {
							model := "inherit"
							edit.Model = &model
						} else {
							models, err := c.ListModels(ctx, pid)
							if err != nil {
								return "", fmt.Errorf("validating agent model: %w", err)
							}
							model, err := validateAgentModel(models, *edit.Model)
							if err != nil {
								return "", err
							}
							edit.Model = &model
						}
					}
					definition, err := c.GetAgent(ctx, pid, matched.ID)
					if err != nil {
						return "", err
					}
					definition = applyAgentEdit(definition, edit)
					if err := c.UpdateAgent(ctx, pid, definition); err != nil {
						return "", err
					}
					if jsonMode {
						persisted, refreshErr := c.GetAgent(ctx, pid, matched.ID)
						if refreshErr != nil {
							return marshalJSON(agentEditPartialResult{
								Saved:        true,
								RefreshError: "saved; authoritative refresh failed",
							})
						}
						return marshalJSON(persisted)
					}
					refreshed, refreshErr := c.ListAgents(ctx, pid)
					statusName := firstNonEmpty(definition.Name, definition.Key, definition.ID)
					for _, agent := range refreshed {
						if agent.ID == matched.ID {
							statusName = firstNonEmpty(agent.Name, agent.Key, agent.ID)
							break
						}
					}
					status := "updated agent " + sanitizeAutomationDetailText(statusName)
					if refreshErr != nil {
						return status + " (saved; refresh failed)", nil
					}
					return status + "\n\n" + renderAgents(refreshed, ""), nil
				})
			case "delete":
				if ref == "" {
					return selectorOr(m, "usage: /agents delete <agent>",
						selectorFor("Agents", "agents delete",
							"no agent definitions — /agents generate <description> creates one", false,
							func(ctx context.Context) ([]selectorItem, error) {
								agents, err := c.ListAgents(ctx, pid)
								if err != nil {
									return nil, err
								}
								items := make([]selectorItem, 0, len(agents))
								for _, a := range agents {
									a := a
									item := selectorItem{
										ref:    a.ID,
										label:  firstNonEmpty(a.Name, a.Key, shortID(a.ID)),
										detail: truncate(a.Description, 40),
									}
									item.dispatch = func(m Model) (Model, tea.Cmd) {
										return confirmAgentDeletion(m, pid, a)
									}
									items = append(items, item)
								}
								return items, nil
							}))
				}
				return m, resolveAgentDeletion(c, pid, ref)
			}
			return m, nil
		},
	}
}

// --- models ---

// fetchModelCapacityWithUsage fetches model capacity and optional provider
// usage independently. Capacity is the primary result; usage failures leave
// the usage value nil so the renderer can show its existing fallback.
func fetchModelCapacityWithUsage(ctx context.Context, c *client.Client, projectID string) ([]client.ModelCapacity, *client.UsageAnalytics, error) {
	var (
		caps   []client.ModelCapacity
		capErr error
		usage  *client.UsageAnalytics
	)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		caps, capErr = c.GetModelCapacities(ctx)
	}()
	go func() {
		defer wg.Done()
		usage, _ = c.GetUsageAnalytics(ctx, projectID)
	}()
	wg.Wait()

	if capErr != nil {
		return nil, nil, capErr
	}
	return caps, usage, nil
}

func modelsCommand() command {
	actions := []string{"list", "default", "delete", "capacity"}
	return command{
		name:          "models",
		aliases:       []string{"model"},
		args:          "[name]",
		actions:       actions,
		selectorPaths: [][]string{{"default"}, {"delete"}},
		desc:          "configured LLM models, worker capacity and provider health",
		usage: []string{
			"models [filter]                            list configured models",
			"models default <model>                     set the default model",
			"models delete <model>                      remove a model",
			"omit <model> on default/delete → interactive selector",
			"models capacity                            worker capacity plus provider/account-limit health (see analytics usage)",
		},
		examples: []string{
			`models default gpt-4o`,
			`models capacity`,
			`models delete claude-haiku`,
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			action, rest := splitAction(actions, args)
			c := m.client
			ref := strings.Join(rest, " ")

			if action == "" || action == "list" {
				return m, run("Models", cmdTimeout, func(ctx context.Context) (string, error) {
					list, err := c.ListModels(ctx, "")
					if err != nil {
						return "", err
					}
					if jsonMode {
						return marshalJSON(list)
					}
					return renderModels(list, ref), nil
				})
			}

			mm, cmd, ok := m.needProject()
			if !ok {
				return mm, cmd
			}
			pid := m.selectedID
			executeResolvedAction := func(ctx context.Context, mo client.LLMModel, action string) (string, error) {
				var err error
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
			}

			switch action {
			case "capacity":
				return m, run("Model capacity", cmdTimeout, func(ctx context.Context) (string, error) {
					caps, usage, err := fetchModelCapacityWithUsage(ctx, c, pid)
					if err != nil {
						return "", err
					}
					return renderModelCapacityWithUsage(caps, usage), nil
				})
			default:
				if ref == "" {
					return selectorOr(m, "usage: /models "+action+" <model>",
						selectorFor("Models", "models "+action,
							"no models configured — add a model via the web UI or API", false,
							func(ctx context.Context) ([]selectorItem, error) {
								list, err := c.ListModels(ctx, pid)
								if err != nil {
									return nil, err
								}
								items := make([]selectorItem, 0, len(list))
								for _, mo := range list {
									mo := mo
									item := selectorItem{
										ref:    mo.ID,
										label:  firstNonEmpty(mo.Name, mo.Model, shortID(mo.ID)),
										detail: strings.TrimSpace(mo.Provider + " " + mo.Model),
									}
									item.dispatch = func(m Model) (Model, tea.Cmd) {
										cmd := run("Models", cmdTimeout, func(ctx context.Context) (string, error) {
											return executeResolvedAction(ctx, mo, action)
										})
										if action == "delete" {
											return confirmOr(m,
												fmt.Sprintf("Delete model %q? Type 'yes' to confirm or Esc to cancel.", mo.ID),
												fmt.Sprintf("use --force to confirm deletion of model %q", mo.ID),
												cmd)
										}
										m.busy = true
										return m, cmd
									}
									items = append(items, item)
								}
								return items, nil
							}))
				}
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
					return executeResolvedAction(ctx, mo, action)
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
		aliases: []string{"works"},
		args:    "[show|limit <n>|project <n>]",
		actions: actions,
		completions: []commandCompletion{
			{after: []string{"limit"}, values: []string{"0", "1", "2", "4", "8", "16", "32"}},
			{after: []string{"project"}, values: []string{"0", "1", "2", "4", "8", "16", "32"}},
		},
		desc: "worker pool stats and concurrency caps",
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
			if action == "project" && len(rest) == 0 && !cliMode {
				mm, cmd, ok := m.needProject()
				if !ok {
					return mm, cmd
				}
				m = mm
			}
			if (action == "limit" || action == "project") && len(rest) == 0 && !cliMode {
				return optionSelector(m, "Worker limit", "workers "+action,
					fmt.Sprintf("usage: /workers %s <n>", action), registryCompletionValues("workers", action))
			}
			if action == "limit" || action == "project" {
				n, err := parseWorkerLimit(action, rest)
				if err != nil {
					return m, errCmd(err.Error())
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
				var (
					wg          sync.WaitGroup
					capacity    *client.GlobalCapacity
					capacityErr error
					projects    []client.ProjectCapacity
					projectsErr error
					models      []client.ModelCapacity
					modelsErr   error
				)
				wg.Add(3)
				go func() { defer wg.Done(); capacity, capacityErr = c.GetGlobalCapacity(ctx) }()
				go func() { defer wg.Done(); projects, projectsErr = c.GetProjectCapacities(ctx) }()
				go func() { defer wg.Done(); models, modelsErr = c.GetModelCapacities(ctx) }()
				wg.Wait()
				if capacityErr != nil {
					return "", capacityErr
				}
				warnings := make([]string, 0, 2)
				if projectsErr != nil {
					projects = []client.ProjectCapacity{}
					warnings = append(warnings, "project worker capacity unavailable")
				}
				if modelsErr != nil {
					models = []client.ModelCapacity{}
					warnings = append(warnings, "model worker capacity unavailable")
				}
				overview := newWorkersOverview(capacity, projects, models, warnings, modelsErr == nil)
				if jsonMode {
					return marshalJSON(overview)
				}
				return renderWorkers(overview), nil
			})
		},
	}
}

// --- channels / personality ---

type channelWizardStep struct {
	field       string
	label       string
	secret      bool
	required    bool
	placeholder string
}

type channelWizardState struct {
	action        string
	channel       client.Channel
	form          url.Values
	steps         []channelWizardStep
	index         int
	restorePrompt string
	restoreHint   string
}

func channelWizardSteps(action, channelType string) []channelWizardStep {
	required := action == "add"
	switch channelType {
	case "telegram":
		return []channelWizardStep{{"token", "Telegram bot token", true, required, "required for a new bot"}, {"telegram_rich_messages_v2", "Rich messages (true/false)", false, false, "true"}}
	case "github":
		return []channelWizardStep{{"github_auth_mode", "Authentication mode (pat/app)", false, false, "pat"}, {"github_pat", "Personal access token (blank for app mode)", true, false, "blank keeps the existing token"}, {"github_app_id", "GitHub App ID", false, false, "blank keeps the current value"}, {"github_app_slug", "GitHub App slug", false, false, "blank keeps the current value"}, {"github_app_private_key", "GitHub App private key", true, false, "blank keeps the existing key"}, {"github_api_endpoint", "GitHub API endpoint", false, false, "https://api.github.com"}}
	case "slack":
		return []channelWizardStep{{"slack_client_id", "Slack client ID", false, required, "required for new Slack setup"}, {"slack_client_secret", "Slack client secret", true, required, "blank keeps the existing secret"}, {"slack_app_token", "Slack app-level token", true, required, "xapp-..."}, {"slack_bot_token_mode", "Bot token mode (oauth/manual)", false, false, "oauth"}, {"slack_bot_token", "Manual bot token (optional)", true, false, "xoxb-...; blank keeps existing"}, {"slack_send_responses", "Send task responses (true/false)", false, false, "true"}}
	case "discord":
		return []channelWizardStep{{"discord_bot_token", "Discord bot token", true, required, "blank keeps the existing token"}, {"discord_send_responses", "Send task responses (true/false)", false, false, "true"}}
	case "email":
		return []channelWizardStep{{"email_provider", "Email provider (gmail/outlook/custom)", false, required, "gmail"}, {"email_address", "Email address", false, required, "bot@example.com"}, {"email_password", "Email app password", true, required, "blank keeps the existing password"}, {"email_imap_host", "IMAP host (custom provider)", false, false, "blank for provider default"}, {"email_imap_port", "IMAP port (custom provider)", false, false, "993"}, {"email_smtp_host", "SMTP host (custom provider)", false, false, "blank for provider default"}, {"email_smtp_port", "SMTP port (custom provider)", false, false, "587"}, {"email_poll_interval_seconds", "Poll interval seconds", false, false, "15"}, {"email_send_responses", "Send responses (true/false)", false, false, "true"}, {"email_skip_attachments", "Skip attachments (true/false)", false, false, "false"}, {"email_mark_existing_seen_on_start", "Mark existing messages seen (true/false)", false, false, "true"}}
	}
	return nil
}

func channelWizardSecretField(field string) bool {
	return strings.Contains(field, "token") || strings.Contains(field, "secret") || strings.Contains(field, "password") || strings.Contains(field, "private_key")
}

func redactChannelCommandSecrets(commandLine string) string {
	tokens, err := tokenizeCommandTokens(commandLine)
	if err != nil || len(tokens) < 2 {
		lower := strings.ToLower(commandLine)
		if strings.Contains(lower, "/channels ") || strings.Contains(lower, "/integrations ") {
			for _, option := range []string{"--token", "--pat", "--private-key", "--client-secret", "--app-token", "--bot-token", "--password"} {
				if strings.Contains(lower, option) {
					return "/channels <redacted sensitive options>"
				}
			}
		}
		return commandLine
	}
	root := strings.TrimPrefix(strings.ToLower(tokens[0].value), "/")
	if (root != "channels" && root != "integrations") || (tokens[1].value != "add" && tokens[1].value != "edit") {
		return commandLine
	}
	secretOptions := map[string]bool{"--token": true, "--pat": true, "--private-key": true, "--client-secret": true, "--app-token": true, "--bot-token": true, "--password": true}
	parts := make([]string, 0, len(tokens))
	for i := 0; i < len(tokens); i++ {
		parts = append(parts, tokens[i].value)
		if secretOptions[strings.ToLower(tokens[i].value)] && i+1 < len(tokens) {
			i++
			parts = append(parts, "<redacted>")
		}
	}
	return strings.Join(parts, " ")
}

func (m Model) beginChannelWizard(action string, channel client.Channel) (Model, tea.Cmd) {
	form := channel.EditableSettings()
	if action == "add" {
		form = make(url.Values)
	}
	defaults := map[string]string{"telegram_rich_messages_v2": "true", "github_auth_mode": "pat", "slack_bot_token_mode": "oauth", "slack_send_responses": "true", "discord_send_responses": "true", "email_provider": "gmail", "email_imap_port": "993", "email_smtp_port": "587", "email_poll_interval_seconds": "15", "email_send_responses": "true", "email_skip_attachments": "false", "email_mark_existing_seen_on_start": "true"}
	for key, value := range defaults {
		if form.Get(key) == "" {
			form.Set(key, value)
		}
	}
	m.channelWizard = &channelWizardState{action: action, channel: channel, form: form, steps: channelWizardSteps(action, channel.Type), restorePrompt: m.input.Prompt, restoreHint: m.input.Placeholder}
	m.menu = nil
	m.input.SetValue("")
	m.append(entry{role: "system", text: action + " " + channel.Name + ": enter each field; leave optional fields blank to preserve/default them. Esc cancels."})
	m.setChannelWizardPrompt()
	return m, nil
}

func (m *Model) setChannelWizardPrompt() {
	wizard := m.channelWizard
	if wizard == nil || wizard.index >= len(wizard.steps) {
		return
	}
	step := wizard.steps[wizard.index]
	m.input.SetValue("")
	m.input.Prompt = step.label + ": "
	m.input.Placeholder = step.placeholder
	m.input.EchoMode = textinput.EchoNormal
	if step.secret {
		m.input.EchoMode = textinput.EchoPassword
	}
	m.input.Focus()
}

func (m *Model) resetChannelWizard() {
	if m.channelWizard == nil {
		return
	}
	m.input.SetValue("")
	m.input.Prompt = m.channelWizard.restorePrompt
	m.input.Placeholder = m.channelWizard.restoreHint
	m.input.EchoMode = textinput.EchoNormal
	m.channelWizard = nil
	m.input.Focus()
}

func validateChannelWizardValue(step channelWizardStep, value string) error {
	if strings.HasSuffix(step.field, "send_responses") || step.field == "telegram_rich_messages_v2" || step.field == "email_skip_attachments" || step.field == "email_mark_existing_seen_on_start" {
		if _, err := strconv.ParseBool(value); err != nil {
			return errors.New("value must be true or false")
		}
	}
	if step.field == "github_auth_mode" && value != "pat" && value != "app" {
		return errors.New("authentication mode must be pat or app")
	}
	if step.field == "slack_bot_token_mode" && value != "oauth" && value != "manual" {
		return errors.New("bot token mode must be oauth or manual")
	}
	if step.field == "email_imap_port" || step.field == "email_smtp_port" {
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return errors.New("port must be from 1 to 65535")
		}
	}
	if step.field == "email_poll_interval_seconds" {
		interval, err := strconv.Atoi(value)
		if err != nil || interval < 5 {
			return errors.New("poll interval must be at least 5 seconds")
		}
	}
	return nil
}

func (m Model) handleChannelWizardKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "ctrl+d":
		m.quitting = true
		m.Cleanup()
		return m, tea.Quit
	case "esc":
		m.resetChannelWizard()
		m.append(entry{role: "system", text: "channel setup cancelled"})
		return m, nil
	case "enter":
		wizard := m.channelWizard
		step := wizard.steps[wizard.index]
		value := strings.TrimSpace(m.input.Value())
		m.input.SetValue("")
		if value == "" && step.required && wizard.form.Get(step.field) == "" {
			m.append(entry{role: "error", text: step.label + " is required"})
			m.setChannelWizardPrompt()
			return m, nil
		}
		if value != "" {
			if err := validateChannelWizardValue(step, value); err != nil {
				m.append(entry{role: "error", text: err.Error()})
				m.setChannelWizardPrompt()
				return m, nil
			}
			wizard.form.Set(step.field, value)
		}
		wizard.index++
		if wizard.index < len(wizard.steps) {
			m.setChannelWizardPrompt()
			return m, nil
		}
		action, channel, form, projectID := wizard.action, wizard.channel, wizard.form, m.selectedID
		m.resetChannelWizard()
		m.busy = true
		c := m.client
		return m, run("Channels", cmdTimeout, func(ctx context.Context) (string, error) {
			defer func() {
				for field := range form {
					if channelWizardSecretField(field) {
						form.Set(field, "")
					}
				}
			}()
			var err error
			if action == "edit" {
				err = c.UpdateChannel(ctx, channel.Type, projectID, form)
			} else {
				err = c.ConfigureChannel(ctx, channel.Type, projectID, form)
			}
			if err != nil {
				return "", err
			}
			channels, err := c.ListChannels(ctx, projectID)
			status := action + "ed " + channel.Name
			if err != nil {
				return status, nil
			}
			return status + "\n\n" + renderChannels(channels), nil
		})
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func matchChannelRef(ref string) (client.Channel, error) {
	channel, err := matchRef(client.KnownChannels, ref,
		func(ch client.Channel) string { return ch.Type },
		func(ch client.Channel) string { return ch.Name })
	if err != nil {
		return client.Channel{}, errors.New(sanitizeAutomationDetailText(err.Error()))
	}
	return channel, nil
}

var channelOptionFields = map[string]string{
	"--token": "token", "--rich-messages": "telegram_rich_messages_v2",
	"--auth-mode": "github_auth_mode", "--pat": "github_pat", "--app-id": "github_app_id",
	"--app-slug": "github_app_slug", "--private-key": "github_app_private_key", "--api-endpoint": "github_api_endpoint",
	"--client-id": "slack_client_id", "--client-secret": "slack_client_secret", "--app-token": "slack_app_token",
	"--bot-token-mode": "slack_bot_token_mode", "--bot-token": "bot_token",
	"--send-responses": "send_responses", "--provider": "email_provider", "--address": "email_address",
	"--password": "email_password", "--imap-host": "email_imap_host", "--imap-port": "email_imap_port",
	"--smtp-host": "email_smtp_host", "--smtp-port": "email_smtp_port", "--poll-interval": "email_poll_interval_seconds",
	"--skip-attachments": "email_skip_attachments", "--mark-existing-seen": "email_mark_existing_seen_on_start",
}

var channelAllowedFields = map[string]map[string]string{
	"telegram": {"token": "token", "telegram_rich_messages_v2": "telegram_rich_messages_v2"},
	"github":   {"github_auth_mode": "github_auth_mode", "github_pat": "github_pat", "github_app_id": "github_app_id", "github_app_slug": "github_app_slug", "github_app_private_key": "github_app_private_key", "github_api_endpoint": "github_api_endpoint"},
	"slack":    {"slack_client_id": "slack_client_id", "slack_client_secret": "slack_client_secret", "slack_app_token": "slack_app_token", "slack_bot_token_mode": "slack_bot_token_mode", "bot_token": "slack_bot_token", "send_responses": "slack_send_responses"},
	"discord":  {"bot_token": "discord_bot_token", "send_responses": "discord_send_responses"},
	"email":    {"email_provider": "email_provider", "email_address": "email_address", "email_password": "email_password", "email_imap_host": "email_imap_host", "email_imap_port": "email_imap_port", "email_smtp_host": "email_smtp_host", "email_smtp_port": "email_smtp_port", "email_poll_interval_seconds": "email_poll_interval_seconds", "send_responses": "email_send_responses", "email_skip_attachments": "email_skip_attachments", "email_mark_existing_seen_on_start": "email_mark_existing_seen_on_start"},
}

func parseChannelMutationArgs(action string, args []string) (client.Channel, map[string]string, error) {
	if len(args) < 2 {
		return client.Channel{}, nil, errors.New(commandUsage("channels", action))
	}
	ch, err := matchChannelRef(args[1])
	if err != nil {
		return client.Channel{}, nil, err
	}
	if len(args) == 2 {
		return client.Channel{}, nil, errors.New(commandUsage("channels", action))
	}
	values := make(map[string]string)
	for i := 2; i < len(args); i += 2 {
		if i+1 >= len(args) {
			return client.Channel{}, nil, fmt.Errorf("%s requires a value", sanitizeAutomationDetailText(args[i]))
		}
		field, ok := channelOptionFields[strings.ToLower(args[i])]
		if !ok {
			return client.Channel{}, nil, fmt.Errorf("unknown channel option %q", sanitizeAutomationDetailText(args[i]))
		}
		backendField, ok := channelAllowedFields[ch.Type][field]
		if !ok {
			return client.Channel{}, nil, fmt.Errorf("%s is not supported for %s", sanitizeAutomationDetailText(args[i]), ch.Name)
		}
		if _, duplicate := values[backendField]; duplicate {
			return client.Channel{}, nil, fmt.Errorf("duplicate channel option %q", sanitizeAutomationDetailText(args[i]))
		}
		value := strings.TrimSpace(args[i+1])
		if value == "" {
			return client.Channel{}, nil, fmt.Errorf("%s cannot be empty", sanitizeAutomationDetailText(args[i]))
		}
		if strings.HasSuffix(backendField, "send_responses") || backendField == "telegram_rich_messages_v2" || backendField == "email_skip_attachments" || backendField == "email_mark_existing_seen_on_start" {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return client.Channel{}, nil, fmt.Errorf("%s must be true or false", sanitizeAutomationDetailText(args[i]))
			}
			value = strconv.FormatBool(parsed)
		}
		if backendField == "github_auth_mode" && value != "pat" && value != "app" {
			return client.Channel{}, nil, errors.New("--auth-mode must be pat or app")
		}
		if backendField == "slack_bot_token_mode" && value != "oauth" && value != "manual" {
			return client.Channel{}, nil, errors.New("--bot-token-mode must be oauth or manual")
		}
		if backendField == "email_imap_port" || backendField == "email_smtp_port" {
			port, err := strconv.Atoi(value)
			if err != nil || port < 1 || port > 65535 {
				return client.Channel{}, nil, fmt.Errorf("%s must be a port from 1 to 65535", sanitizeAutomationDetailText(args[i]))
			}
		}
		if backendField == "email_poll_interval_seconds" {
			interval, err := strconv.Atoi(value)
			if err != nil || interval < 5 {
				return client.Channel{}, nil, errors.New("--poll-interval must be at least 5 seconds")
			}
		}
		values[backendField] = value
	}
	if action == "add" {
		required := map[string][]string{
			"telegram": {"token"}, "discord": {"discord_bot_token"},
			"email": {"email_provider", "email_address", "email_password"},
			"slack": {"slack_client_id", "slack_client_secret", "slack_app_token"},
		}
		for _, field := range required[ch.Type] {
			if values[field] == "" {
				return client.Channel{}, nil, fmt.Errorf("adding %s requires %s", ch.Name, strings.ReplaceAll(field, "_", "-"))
			}
		}
		if ch.Type == "github" {
			mode := values["github_auth_mode"]
			if mode == "" {
				mode = "pat"
				values["github_auth_mode"] = mode
			}
			if mode == "pat" && values["github_pat"] == "" {
				return client.Channel{}, nil, errors.New("adding GitHub in PAT mode requires --pat")
			}
			if mode == "app" && (values["github_app_id"] == "" || values["github_app_slug"] == "" || values["github_app_private_key"] == "") {
				return client.Channel{}, nil, errors.New("adding GitHub in app mode requires --app-id, --app-slug, and --private-key")
			}
		}
	}
	return ch, values, nil
}

func validateChannelsArgs(args []string) error {
	if len(args) == 0 {
		return nil
	}
	action := strings.ToLower(args[0])
	switch action {
	case "list":
		if len(args) == 1 {
			return nil
		}
	case "show", "connect", "test", "remove", "disconnect":
		if len(args) == 1 {
			return nil
		}
		ch, err := matchChannelRef(strings.Join(args[1:], " "))
		if err != nil {
			return err
		}
		if action == "test" && ch.Type == "github" {
			return errors.New("GitHub does not expose a connection test")
		}
		if action == "connect" && ch.Type != "github" && ch.Type != "slack" {
			return fmt.Errorf("%s does not support browser connection", ch.Name)
		}
		return nil
	case "add", "edit":
		if len(args) == 1 {
			return nil
		}
		if !cliMode && len(args) == 2 {
			_, err := matchChannelRef(args[1])
			return err
		}
		if !cliMode {
			return errors.New("interactive channel configuration uses masked prompts; omit options")
		}
		_, _, err := parseChannelMutationArgs(action, args)
		return err
	}
	return errors.New(commandUsage("channels", ""))
}

func renderChannels(channels []client.Channel) string {
	if len(channels) == 0 {
		return "No channel integrations are available."
	}
	var b strings.Builder
	b.WriteString("TYPE       NAME          CONNECTION\n")
	for _, ch := range channels {
		status := sanitizeAutomationDetailText(ch.Status)
		if status == "" {
			status = "unknown"
		}
		metadata := ""
		if ch.Address != "" {
			metadata = " · " + sanitizeAutomationDetailText(ch.Address)
		}
		fmt.Fprintf(&b, "%-10s %-13s %s%s\n", sanitizeAutomationDetailText(ch.Type), sanitizeAutomationDetailText(ch.Name), status, metadata)
	}
	return strings.TrimRight(b.String(), "\n")
}

func channelSelector(action string, suffix string, allowed func(client.Channel) bool) tea.Cmd {
	return selectorForWithSuffix("Channels", "channels "+action, "no matching channels available", suffix, func(ctx context.Context) ([]selectorItem, error) {
		items := make([]selectorItem, 0, len(client.KnownChannels))
		for _, ch := range client.KnownChannels {
			if allowed == nil || allowed(ch) {
				items = append(items, selectorItem{ref: ch.Type, label: ch.Name})
			}
		}
		return items, nil
	})
}

func channelsCommand() command {
	actions := []string{"list", "show", "add", "connect", "edit", "test", "remove", "disconnect"}
	return command{
		name: "channels", aliases: []string{"integrations"}, args: "[action] [channel]", actions: actions,
		selectorPaths: [][]string{{"show"}, {"add"}, {"connect"}, {"edit"}, {"test"}, {"remove"}, {"disconnect"}},
		completions: []commandCompletion{
			{after: []string{"add", "*", "**"}, values: []string{"--token", "--rich-messages", "--auth-mode", "--pat", "--app-id", "--app-slug", "--private-key", "--api-endpoint", "--client-id", "--client-secret", "--app-token", "--bot-token-mode", "--bot-token", "--send-responses", "--provider", "--address", "--password", "--imap-host", "--imap-port", "--smtp-host", "--smtp-port", "--poll-interval", "--skip-attachments", "--mark-existing-seen"}},
			{after: []string{"edit", "*", "**"}, values: []string{"--token", "--rich-messages", "--auth-mode", "--pat", "--app-id", "--app-slug", "--private-key", "--api-endpoint", "--client-id", "--client-secret", "--app-token", "--bot-token-mode", "--bot-token", "--send-responses", "--provider", "--address", "--password", "--imap-host", "--imap-port", "--smtp-host", "--smtp-port", "--poll-interval", "--skip-attachments", "--mark-existing-seen"}},
			{after: []string{"add", "*", "--auth-mode"}, values: []string{"pat", "app"}},
			{after: []string{"edit", "*", "--auth-mode"}, values: []string{"pat", "app"}},
			{after: []string{"add", "*", "--bot-token-mode"}, values: []string{"oauth", "manual"}},
			{after: []string{"edit", "*", "--bot-token-mode"}, values: []string{"oauth", "manual"}},
		},
		desc: "manage GitHub, Slack, Telegram, Discord, and Email integrations",
		actionUsages: []commandActionUsage{
			{action: "", args: "[list|show|add|connect|edit|test|remove|disconnect]"},
			{action: "list", description: "list safe channel identity and connection state"},
			{action: "show", args: "<channel>", description: "show safe channel details"},
			{action: "add", args: "<type> <options>", description: "configure a new channel"},
			{action: "connect", args: "<github|slack>", description: "show the browser OAuth URL"},
			{action: "edit", args: "<channel> <options>", description: "update channel settings"},
			{action: "test", args: "<channel>", description: "test Slack, Telegram, Discord, or Email"},
			{action: "remove", args: "<channel>", description: "disconnect/remove a channel (confirmation required)"},
			{action: "disconnect", args: "<channel>", description: "alias for remove"},
		},
		usage: []string{
			"options: --token, --rich-messages, --auth-mode, --pat, --app-id, --app-slug, --private-key, --api-endpoint",
			"         --client-id, --client-secret, --app-token, --bot-token-mode, --bot-token, --send-responses",
			"         --provider, --address, --password, --imap-host, --imap-port, --smtp-host, --smtp-port, --poll-interval",
			"Secret options are accepted headlessly but are never echoed; prefer an interactive masked terminal when available.",
		},
		examples:     []string{"channels show slack", "channels connect slack", "channels test telegram", "channels remove discord"},
		validateArgs: validateChannelsArgs,
		run: func(m Model, args []string) (Model, tea.Cmd) {
			mm, cmd, ok := m.needProject()
			if !ok {
				return mm, cmd
			}
			if err := validateChannelsArgs(args); err != nil {
				return m, errCmd(err.Error())
			}
			action, rest := splitAction(actions, args)
			c, pid := m.client, m.selectedID
			if action == "" || action == "list" {
				return m, run("Channels", cmdTimeout, func(ctx context.Context) (string, error) {
					channels, err := c.ListChannels(ctx, pid)
					if err != nil {
						return "", err
					}
					if jsonMode {
						return marshalJSON(channels)
					}
					return renderChannels(channels), nil
				})
			}
			if len(rest) == 0 {
				suffix := ""
				allowed := func(ch client.Channel) bool { return true }
				if action == "connect" {
					allowed = func(ch client.Channel) bool { return ch.Type == "github" || ch.Type == "slack" }
				}
				if action == "test" {
					allowed = func(ch client.Channel) bool { return ch.Type != "github" }
				}
				return selectorOr(m, commandUsage("channels", action), channelSelector(action, suffix, allowed))
			}
			if action == "add" || action == "edit" {
				if !cliMode {
					ch, _ := matchChannelRef(strings.Join(rest, " "))
					if action == "add" {
						return m.beginChannelWizard(action, ch)
					}
					m.busy = true
					sessionGeneration, projectGeneration := m.sessionGeneration, m.projectGeneration
					return m, func() tea.Msg {
						ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
						defer cancel()
						current, err := c.GetChannel(ctx, pid, ch.Type)
						msg := channelWizardStartMsg{sessionGeneration: sessionGeneration, projectGeneration: projectGeneration, projectID: pid, action: action, err: err}
						if current != nil {
							msg.channel = *current
						}
						return msg
					}
				}
				ch, updates, _ := parseChannelMutationArgs(action, args)
				return m, run("Channels", cmdTimeout, func(ctx context.Context) (string, error) {
					form := mapToValues(updates)
					var err error
					if action == "edit" {
						err = c.UpdateChannel(ctx, ch.Type, pid, form)
					} else {
						err = c.ConfigureChannel(ctx, ch.Type, pid, form)
					}
					if err != nil {
						return "", err
					}
					channels, err := c.ListChannels(ctx, pid)
					status := action + "ed " + ch.Name
					if err != nil {
						return status, nil
					}
					return status + "\n\n" + renderChannels(channels), nil
				})
			}
			ch, _ := matchChannelRef(strings.Join(rest, " "))
			if action == "show" {
				return m, run("Channels", cmdTimeout, func(ctx context.Context) (string, error) {
					current, err := c.GetChannel(ctx, pid, ch.Type)
					if err != nil {
						return "", err
					}
					if jsonMode {
						return marshalJSON(current)
					}
					return renderChannels([]client.Channel{*current}), nil
				})
			}
			if action == "connect" {
				safeURL, err := c.ChannelConnectURL(ch.Type, pid)
				if err != nil {
					return m, errCmd(err.Error())
				}
				return m, run("Channels", cmdTimeout, func(context.Context) (string, error) {
					return "Open this URL in a browser to connect " + ch.Name + ":\n" + safeURL, nil
				})
			}
			backendAction := action
			if backendAction == "disconnect" {
				backendAction = "remove"
			}
			runAction := run("Channels", cmdTimeout, func(ctx context.Context) (string, error) {
				if err := c.ChannelAction(ctx, ch.Type, backendAction, pid); err != nil {
					return "", err
				}
				channels, err := c.ListChannels(ctx, pid)
				status := backendAction + ": " + ch.Name
				if err != nil {
					return status, nil
				}
				return status + "\n\n" + renderChannels(channels), nil
			})
			if backendAction == "remove" {
				return confirmOr(m, fmt.Sprintf("Remove channel %q? Type 'yes' to confirm or Esc to cancel.", ch.Name), fmt.Sprintf("use --force to confirm removal of channel %q", ch.Name), runAction)
			}
			return m, runAction
		},
	}
}

func mapToValues(values map[string]string) url.Values {
	form := make(url.Values)
	for key, value := range values {
		form.Set(key, value)
	}
	return form
}

// webhookActionJSON is the stable machine-readable acknowledgement for delete.
type webhookActionJSON struct {
	Action string `json:"action"`
	ID     string `json:"id"`
	Name   string `json:"name"`
}

var webhookOptionNames = map[string]string{
	"--name": "name", "--enabled": "enabled", "--priority": "priority", "--default-priority": "priority",
	"--system-instructions": "system_instructions", "--title-template": "title_template", "--prompt-template": "prompt_template",
	"--agents": "agent_ids", "--agent-ids": "agent_ids",
}

func webhookOptionBoundary(args []string) int {
	for i, arg := range args {
		if strings.HasPrefix(arg, "--") {
			return i
		}
	}
	return len(args)
}

func parseWebhookOptions(args []string) (map[string]string, error) {
	values := make(map[string]string)
	for len(args) > 0 {
		key, ok := webhookOptionNames[strings.ToLower(args[0])]
		if !ok {
			return nil, fmt.Errorf("unknown webhook option %q", args[0])
		}
		if len(args) < 2 || strings.HasPrefix(args[1], "--") {
			return nil, fmt.Errorf("webhook option %s requires a value", args[0])
		}
		if _, duplicate := values[key]; duplicate {
			return nil, fmt.Errorf("webhook option %s was provided more than once", args[0])
		}
		values[key] = args[1]
		args = args[2:]
	}
	if enabled, ok := values["enabled"]; ok {
		if enabled != "true" && enabled != "false" {
			return nil, errors.New("webhook enabled must be true or false")
		}
	}
	if priority, ok := values["priority"]; ok {
		n, err := strconv.Atoi(priority)
		if err != nil || n < 1 || n > 4 {
			return nil, errors.New("webhook priority must be between 1 and 4")
		}
	}
	return values, nil
}

func applyWebhookOptions(webhook *client.Webhook, values map[string]string) {
	if value, ok := values["name"]; ok {
		webhook.Name = strings.TrimSpace(value)
	}
	if value, ok := values["enabled"]; ok {
		webhook.Enabled, _ = strconv.ParseBool(value)
	}
	if value, ok := values["priority"]; ok {
		webhook.DefaultPriority, _ = strconv.Atoi(value)
	}
	if value, ok := values["system_instructions"]; ok {
		webhook.SystemInstructions = value
	}
	if value, ok := values["title_template"]; ok {
		webhook.TitleTemplate = value
	}
	if value, ok := values["prompt_template"]; ok {
		webhook.PromptTemplate = value
	}
	if value, ok := values["agent_ids"]; ok {
		webhook.AgentIDs = make([]string, 0)
		for _, id := range strings.Split(value, ",") {
			if id = strings.TrimSpace(id); id != "" {
				webhook.AgentIDs = append(webhook.AgentIDs, id)
			}
		}
	}
}

func validateWebhooksArgs(args []string) error {
	if len(args) == 0 {
		return nil
	}
	action, rest := strings.ToLower(args[0]), args[1:]
	switch action {
	case "list":
		if len(rest) != 0 {
			return errors.New(commandUsage("webhooks", "list"))
		}
		return nil
	case "show", "test", "rotate", "delete":
		return nil
	case "create", "edit":
		boundary := webhookOptionBoundary(rest)
		if _, err := parseWebhookOptions(rest[boundary:]); err != nil {
			return err
		}
		if action == "create" && strings.TrimSpace(strings.Join(rest[:boundary], " ")) == "" {
			return errors.New(commandUsage("webhooks", "create"))
		}
		if action == "edit" && boundary == len(rest) && len(rest) > 0 {
			return errors.New(commandUsage("webhooks", "edit"))
		}
		return nil
	default:
		return errors.New(commandUsage("webhooks", ""))
	}
}

func resolveWebhook(ctx context.Context, c *client.Client, projectID, ref string) (client.Webhook, error) {
	webhooks, err := c.ListWebhooks(ctx, projectID)
	if err != nil {
		return client.Webhook{}, err
	}
	return matchRefWithDisplay(webhooks, ref,
		func(w client.Webhook) string { return w.ID },
		func(w client.Webhook) string { return w.Name },
		sanitizeAutomationDetailText)
}

func resolveWebhookMutation(c *client.Client, projectID, action, ref string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		defer cancel()
		webhook, err := resolveWebhook(ctx, c, projectID, ref)
		return webhookMutationTargetMsg{projectID: projectID, action: action, webhook: webhook, err: err}
	}
}

func confirmWebhookMutation(m Model, projectID, action string, webhook client.Webhook) (Model, tea.Cmd) {
	name := sanitizeAutomationDetailText(firstNonEmpty(webhook.Name, webhook.ID))
	cmd := run("Webhooks", cmdTimeout, func(ctx context.Context) (string, error) {
		switch action {
		case "rotate":
			rotation, err := m.client.RotateWebhookSecret(ctx, projectID, webhook.ID)
			if err != nil {
				return "", err
			}
			if jsonMode {
				return marshalJSON(rotation)
			}
			return fmt.Sprintf("rotated secret for %s\nNew secret: %s", name, sanitizeAutomationDetailText(rotation.Secret)), nil
		case "delete":
			if err := m.client.DeleteWebhook(ctx, projectID, webhook.ID); err != nil {
				return "", err
			}
			if jsonMode {
				return marshalJSON(webhookActionJSON{Action: "delete", ID: webhook.ID, Name: webhook.Name})
			}
			return "deleted webhook: " + name, nil
		default:
			return "", errors.New(commandUsage("webhooks", action))
		}
	})
	return confirmOr(m,
		fmt.Sprintf("%s webhook %q? Type 'yes' to confirm or Esc to cancel.", titleFor(action), name),
		fmt.Sprintf("use --force to confirm %s of webhook %q", action, name),
		cmd)
}

func webhookSelector(m Model, action string, prefill bool) (Model, tea.Cmd) {
	c, projectID := m.client, m.selectedID
	prefillSuffix := ""
	if prefill {
		prefillSuffix = " "
	}
	return selectorOr(m, commandUsage("webhooks", action), selectorForWithSuffix("Webhooks", "webhooks "+action, "no inbound webhooks configured", prefillSuffix, func(ctx context.Context) ([]selectorItem, error) {
		webhooks, err := c.ListWebhooks(ctx, projectID)
		if err != nil {
			return nil, err
		}
		items := make([]selectorItem, 0, len(webhooks))
		for _, webhook := range webhooks {
			items = append(items, selectorItem{ref: webhook.ID, label: webhook.Name, detail: webhook.Path})
		}
		return items, nil
	}))
}

func renderWebhooks(webhooks []client.Webhook) string {
	if len(webhooks) == 0 {
		return "no inbound webhooks configured"
	}
	var b strings.Builder
	b.WriteString("Inbound webhooks\n")
	for _, webhook := range webhooks {
		state := "disabled"
		if webhook.Enabled {
			state = "enabled"
		}
		fmt.Fprintf(&b, "  %s  %s  %s  priority %d\n    %s\n", sanitizeAutomationDetailText(webhook.Name), sanitizeAutomationDetailText(shortID(webhook.ID)), state, webhook.DefaultPriority, sanitizeAutomationDetailText(webhook.Path))
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderWebhookDetail(webhook client.Webhook) string {
	safe := sanitizeAutomationDetailText
	state := "disabled"
	if webhook.Enabled {
		state = "enabled"
	}
	return fmt.Sprintf("Webhook: %s\nID: %s\nProject: %s\nState: %s\nURL: %s\nPath: %s\nPriority: %d\nAgents: %s\nSystem instructions: %s\nTitle template: %s\nPrompt template: %s", safe(webhook.Name), safe(webhook.ID), safe(webhook.ProjectID), state, safe(webhook.URL), safe(webhook.Path), webhook.DefaultPriority, safe(strings.Join(webhook.AgentIDs, ", ")), safe(webhook.SystemInstructions), safe(webhook.TitleTemplate), safe(webhook.PromptTemplate))
}

func webhooksCommand() command {
	actions := []string{"list", "show", "create", "edit", "test", "rotate", "delete"}
	return command{
		name: "webhooks", aliases: []string{"inbound-webhooks"}, args: "[action] [webhook]", actions: actions,
		selectorPaths: [][]string{{"show"}, {"edit"}, {"test"}, {"rotate"}, {"delete"}}, desc: "project-scoped inbound webhook endpoints",
		actionUsages: []commandActionUsage{
			{action: "", args: "[list|show <webhook>|create <name> [options]|edit <webhook> <options>|test <webhook>|rotate <webhook>|delete <webhook>]"},
			{action: "list", description: "list inbound webhooks"}, {action: "show", args: "<webhook>", description: "show secret-free webhook detail"},
			{action: "create", args: "<name> [options]", description: "create an inbound webhook"}, {action: "edit", args: "<webhook> <options>", description: "edit only specified configuration"},
			{action: "test", args: "<webhook>", description: "create a synthetic test task"}, {action: "rotate", args: "<webhook>", description: "rotate the webhook secret (confirmation required)"},
			{action: "delete", args: "<webhook>", description: "delete a webhook (confirmation required)"},
		},
		usage:    []string{"options: --name, --enabled, --priority, --system-instructions, --title-template, --prompt-template, --agents", "omit <webhook> on show/edit/test/rotate/delete → interactive selector"},
		examples: []string{`webhooks create "PagerDuty alerts" --priority 3`, `webhooks edit pager --enabled false`, `webhooks test pager`, `webhooks rotate pager`}, validateArgs: validateWebhooksArgs,
		run: func(m Model, args []string) (Model, tea.Cmd) {
			mm, noProject, ok := m.needProject()
			if !ok {
				return mm, noProject
			}
			if err := validateWebhooksArgs(args); err != nil {
				return m, errCmd(err.Error())
			}
			action, rest := splitAction(actions, args)
			c, projectID := m.client, m.selectedID
			if action == "" || action == "list" {
				return m, run("Webhooks", cmdTimeout, func(ctx context.Context) (string, error) {
					webhooks, err := c.ListWebhooks(ctx, projectID)
					if err != nil {
						return "", err
					}
					if jsonMode {
						return marshalJSON(webhooks)
					}
					return renderWebhooks(webhooks), nil
				})
			}
			boundary := len(rest)
			options := map[string]string(nil)
			if action == "create" || action == "edit" {
				boundary = webhookOptionBoundary(rest)
				options, _ = parseWebhookOptions(rest[boundary:])
			}
			ref := strings.TrimSpace(strings.Join(rest[:boundary], " "))
			if ref == "" && action != "create" {
				return webhookSelector(m, action, action == "edit")
			}
			if action == "create" {
				return m, run("Webhooks", cmdTimeout, func(ctx context.Context) (string, error) {
					webhook := client.Webhook{ProjectID: projectID, Name: ref, Enabled: true, DefaultPriority: 2, AgentIDs: make([]string, 0)}
					applyWebhookOptions(&webhook, options)
					created, err := c.CreateWebhook(ctx, projectID, webhook)
					if err != nil {
						return "", err
					}
					if jsonMode {
						return marshalJSON(created)
					}
					return "created webhook\n\n" + renderWebhookDetail(*created), nil
				})
			}
			if action == "rotate" || action == "delete" {
				return m, resolveWebhookMutation(c, projectID, action, ref)
			}
			cmd := run("Webhooks", cmdTimeout, func(ctx context.Context) (string, error) {
				webhook, err := resolveWebhook(ctx, c, projectID, ref)
				if err != nil {
					return "", err
				}
				switch action {
				case "show":
					detail, err := c.GetWebhook(ctx, projectID, webhook.ID)
					if err != nil {
						return "", err
					}
					if jsonMode {
						return marshalJSON(detail)
					}
					return renderWebhookDetail(*detail), nil
				case "edit":
					detail, err := c.GetWebhook(ctx, projectID, webhook.ID)
					if err != nil {
						return "", err
					}
					applyWebhookOptions(detail, options)
					updated, err := c.UpdateWebhook(ctx, projectID, *detail)
					if err != nil {
						return "", err
					}
					if jsonMode {
						return marshalJSON(updated)
					}
					return "updated webhook\n\n" + renderWebhookDetail(*updated), nil
				case "test":
					result, err := c.TestWebhook(ctx, projectID, webhook.ID)
					if err != nil {
						return "", err
					}
					if jsonMode {
						return marshalJSON(result)
					}
					return fmt.Sprintf("test task created: %s", sanitizeAutomationDetailText(result.TaskID)), nil
				}
				return "", errors.New(commandUsage("webhooks", action))
			})
			return m, cmd
		},
	}
}

// personalityActionJSON is the stable machine-readable record used for
// personality mutations whose backend response has no resource body.
type personalityActionJSON struct {
	Action string `json:"action"`
	ID     string `json:"id,omitempty"`
	Key    string `json:"key"`
	Name   string `json:"name"`
	Active bool   `json:"active,omitempty"`
}

func personalitySelector(m Model, usage, command, action string, prefill bool) (Model, tea.Cmd) {
	c, pid := m.client, m.selectedID
	prefillSuffix := ""
	if prefill {
		prefillSuffix = " | "
	}
	return selectorOr(m, usage, selectorForWithSuffix("Personality", command,
		"no personalities available — /personality add <name> | <system prompt> creates one",
		prefillSuffix,
		func(ctx context.Context) ([]selectorItem, error) {
			personalities, err := c.ListPersonalities(ctx, pid)
			if err != nil {
				return nil, err
			}
			items := make([]selectorItem, 0, len(personalities))
			for _, personality := range personalities {
				personality := personality
				// Base can be selected or shown, but it is not an editable or
				// deletable custom key.
				if personality.Key == "" && (action == "edit" || action == "delete") {
					continue
				}
				ref := personality.Key
				if ref == "" {
					ref = personality.Name
				}
				kind := personalityKind(personality)
				item := selectorItem{
					ref:    ref,
					label:  firstNonEmpty(personality.Name, ref),
					detail: strings.TrimSpace(kind + " · " + truncate(personality.Description, 40)),
				}
				if !prefill {
					item.dispatch = func(m Model) (Model, tea.Cmd) {
						switch action {
						case "show":
							return m, personalityShowCommand(c, pid, personality)
						case "set":
							return m, personalitySetCommand(c, pid, personality)
						case "delete":
							cmd := personalityDeleteCommand(c, pid, personality)
							return confirmOr(m,
								fmt.Sprintf("Delete personality %q? Type 'yes' to confirm or Esc to cancel.", personality.Name),
								fmt.Sprintf("use --force to confirm deletion of personality %q", personality.Name),
								cmd)
						}
						return m, nil
					}
				}
				items = append(items, item)
			}
			return items, nil
		}))
}

func personalityShowResult(ctx context.Context, c *client.Client, projectID string, personality client.Personality) (string, error) {
	detail := personality
	if personality.Key != "" {
		loaded, err := c.GetCustomPersonality(ctx, projectID, personality.Key)
		if err != nil {
			return "", err
		}
		detail = *loaded
	}
	// The detail route returns the resource fields but not the list-only state
	// markers, so retain the resolved entry metadata for rendering and JSON
	// consumers.
	detail.IsPreset = personality.IsPreset
	detail.HasCustom = personality.HasCustom
	detail.Active = personality.Active
	if jsonMode {
		return marshalJSON(detail)
	}
	return renderPersonalityDetail(detail), nil
}

func personalityShowCommand(c *client.Client, projectID string, personality client.Personality) tea.Cmd {
	return run("Personality", cmdTimeout, func(ctx context.Context) (string, error) {
		return personalityShowResult(ctx, c, projectID, personality)
	})
}

func personalitySetResult(ctx context.Context, c *client.Client, projectID string, personality client.Personality) (string, error) {
	if err := c.SavePersonality(ctx, projectID, personality.Key); err != nil {
		return "", err
	}
	if jsonMode {
		return marshalJSON(personalityActionJSON{
			Action: "set",
			ID:     personality.ID,
			Key:    personality.Key,
			Name:   personality.Name,
			Active: true,
		})
	}
	return "personality set to " + firstNonEmpty(personality.Key, personality.Name), nil
}

func personalitySetCommand(c *client.Client, projectID string, personality client.Personality) tea.Cmd {
	return run("Personality", cmdTimeout, func(ctx context.Context) (string, error) {
		return personalitySetResult(ctx, c, projectID, personality)
	})
}

func personalityDeleteResult(ctx context.Context, c *client.Client, projectID string, personality client.Personality) (string, error) {
	if err := c.DeleteCustomPersonality(ctx, projectID, personality.Key); err != nil {
		return "", err
	}
	if jsonMode {
		return marshalJSON(personalityActionJSON{
			Action: "delete",
			ID:     personality.ID,
			Key:    personality.Key,
			Name:   personality.Name,
		})
	}
	return refreshAndRender("deleted personality "+firstNonEmpty(personality.Key, personality.Name),
		func() ([]client.Personality, error) { return c.ListPersonalities(ctx, projectID) },
		renderPersonalities)
}

func personalityDeleteCommand(c *client.Client, projectID string, personality client.Personality) tea.Cmd {
	return run("Personality", cmdTimeout, func(ctx context.Context) (string, error) {
		return personalityDeleteResult(ctx, c, projectID, personality)
	})
}

func personalityCommand() command {
	actions := []string{"list", "show", "add", "edit", "set", "delete"}
	return command{
		name:          "personality",
		args:          "[key|name]",
		actions:       actions,
		selectorPaths: [][]string{{"show"}, {"edit"}, {"set"}, {"delete"}},
		desc:          "built-in and custom assistant personalities",
		usage: []string{
			"personality                                show the current personality",
			"personality list                           list built-in and custom personalities",
			"personality show <key|name>                 show the full system prompt",
			"personality add <name> | <system prompt>   create a custom personality",
			"personality add <name> | description=<description> | <system prompt>",
			"personality edit <key|name> | <name> | <description> | <system prompt>",
			"personality set <key|name>                  activate a personality",
			"personality delete <key|name>               delete a custom or reset an override",
			"omit <key|name> on show/edit/set/delete → interactive selector",
		},
		actionUsages: []commandActionUsage{
			{action: "show", args: "<key|name>"},
			{action: "add", args: `<name> | <system prompt> [or: <name> | description=<description> | <system prompt>]`},
			{action: "edit", args: "<key|name> | <name> | <description> | <system prompt>"},
			{action: "set", args: "<key|name>"},
			{action: "delete", args: "<key|name>"},
		},
		examples: []string{
			`personality list`,
			`personality add "Release Coach" | Keep advice practical and focused on shipping safely.`,
			`personality add "Release Coach" | description=safe release guidance | Keep advice practical, focused, and safe for production releases.`,
			`personality edit release_coach | Release Coach | pragmatic release guidance | Keep advice practical, focused, and safe for production releases.`,
			`personality set release_coach`,
			`personality delete release_coach`,
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			mm, cmd, ok := m.needProject()
			if !ok {
				return mm, cmd
			}
			action, rest := splitAction(actions, args)
			if action == "" && len(rest) > 0 {
				return m, errCmd(fmt.Sprintf("unknown personality action %q — use /personality list|show|add|edit|set|delete", rest[0]))
			}
			c, pid := m.client, m.selectedID
			ref := strings.Join(rest, " ")

			switch action {
			case "":
				// Keep the original no-argument command as the rendered current
				// personality page rather than changing it into a list operation.
				return m, run("Personality", cmdTimeout, func(ctx context.Context) (string, error) {
					return c.GetPersonality(ctx, pid)
				})
			case "list":
				return m, run("Personalities", cmdTimeout, func(ctx context.Context) (string, error) {
					personalities, err := c.ListPersonalities(ctx, pid)
					if err != nil {
						return "", err
					}
					if jsonMode {
						return marshalJSON(personalities)
					}
					return renderPersonalities(personalities, ref), nil
				})
			case "show":
				if ref == "" {
					return personalitySelector(m, commandUsage("personality", "show"), "personality show", "show", false)
				}
				return m, run("Personality", cmdTimeout, func(ctx context.Context) (string, error) {
					personality, err := resolvePersonality(ctx, c, pid, ref)
					if err != nil {
						return "", err
					}
					return personalityShowResult(ctx, c, pid, personality)
				})
			case "add":
				name, description, prompt, err := parsePersonalityAdd(ref)
				if err != nil {
					return m, errCmd(commandUsage("personality", "add") + ": " + err.Error())
				}
				return m, run("Personality", cmdTimeout, func(ctx context.Context) (string, error) {
					created, err := c.CreateCustomPersonality(ctx, pid, name, description, prompt)
					if err != nil {
						return "", err
					}
					if jsonMode {
						return marshalJSON(created)
					}
					status := fmt.Sprintf("created personality %q\nkey: %s\nID: %s", created.Name, created.Key, created.ID)
					return refreshAndRender(status,
						func() ([]client.Personality, error) { return c.ListPersonalities(ctx, pid) },
						renderPersonalities)
				})
			case "edit":
				if strings.TrimSpace(ref) == "" {
					return personalitySelector(m, commandUsage("personality", "edit"), "personality edit", "edit", true)
				}
				parts := strings.SplitN(ref, "|", 4)
				if len(parts) != 4 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" || strings.TrimSpace(parts[3]) == "" {
					return m, errCmd(commandUsage("personality", "edit"))
				}
				ref2 := strings.TrimSpace(parts[0])
				name := strings.TrimSpace(parts[1])
				description := strings.TrimSpace(parts[2])
				prompt := strings.TrimSpace(parts[3])
				return m, run("Personality", cmdTimeout, func(ctx context.Context) (string, error) {
					personality, err := resolvePersonality(ctx, c, pid, ref2)
					if err != nil {
						return "", err
					}
					if personality.Key == "" {
						return "", fmt.Errorf("base personality cannot be edited")
					}
					updated, err := c.UpdateCustomPersonality(ctx, pid, personality.Key, name, description, prompt)
					if err != nil {
						return "", err
					}
					updated.IsPreset = personality.IsPreset
					updated.HasCustom = true
					updated.Active = personality.Active
					if jsonMode {
						return marshalJSON(updated)
					}
					return "updated personality " + personality.Key, nil
				})
			case "set":
				if ref == "" {
					return personalitySelector(m, commandUsage("personality", "set"), "personality set", "set", false)
				}
				return m, run("Personality", cmdTimeout, func(ctx context.Context) (string, error) {
					personality, err := resolvePersonality(ctx, c, pid, ref)
					if err != nil {
						return "", err
					}
					return personalitySetResult(ctx, c, pid, personality)
				})
			case "delete":
				if ref == "" {
					return personalitySelector(m, commandUsage("personality", "delete"), "personality delete", "delete", false)
				}
				cmd := run("Personality", cmdTimeout, func(ctx context.Context) (string, error) {
					personality, err := resolvePersonality(ctx, c, pid, ref)
					if err != nil {
						return "", err
					}
					if personality.Key == "" {
						return "", fmt.Errorf("base personality cannot be deleted; use set Base to reset it")
					}
					return personalityDeleteResult(ctx, c, pid, personality)
				})
				return confirmOr(m,
					fmt.Sprintf("Delete personality %q? Type 'yes' to confirm or Esc to cancel.", ref),
					fmt.Sprintf("use --force to confirm deletion of personality %q", ref),
					cmd)
			}
			return m, nil
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
			mm, cmd, ok := m.needProject()
			if !ok {
				return mm, cmd
			}
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
			mm, cmd, ok := m.needProject()
			if !ok {
				return mm, cmd
			}
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
	actions := []string{"show", "run"}
	return command{
		name:    "grades",
		actions: actions,
		desc:    "grade the project's ideas/backlog quality",
		usage: []string{
			"grades                                     show the current idea grades",
			"grades run                                 run a fresh grading pass",
		},
		examples: []string{`grades`, `grades run`},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			mm, cmd, ok := m.needProject()
			if !ok {
				return mm, cmd
			}
			action, _ := splitAction(actions, args)
			c, pid := m.client, m.selectedID
			return m, run("Grades", cmdTimeout, func(ctx context.Context) (string, error) {
				return generateThenFetch(ctx, "run", action,
					func(ctx context.Context) error { return c.GradeIdeas(ctx, pid) },
					func(ctx context.Context) (string, error) { return c.GetGrades(ctx, pid) })
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
			mm, cmd, ok := m.needProject()
			if !ok {
				return mm, cmd
			}
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
	actions := []string{"list", "show", "open", "run", "pause", "resume", "delete"}
	return command{
		name:          "automations",
		aliases:       []string{"automation"},
		args:          "[filter]",
		actions:       actions,
		selectorPaths: [][]string{{"show"}, {"open"}, {"run"}, {"run-now"}, {"pause"}, {"resume"}, {"delete"}},
		desc:          "recurring automations and workflow rules",
		usage: []string{
			"automations [filter]                       list automations",
			"automations show <automation>              show live graph, runtime and resources",
			"automations open <automation>              alias for show",
			"automations run <automation>               trigger an immediate run",
			"automations pause <automation>              pause an active automation",
			"automations resume <automation>             resume a paused automation",
			"automations delete <automation>             remove an automation (interactive: type 'yes'; CLI: use --force/-f before the command)",
			"automations run-now <automation>           compatibility alias for run",
			"omit <automation> on show/open/run/pause/resume/delete → interactive selector",
		},
		actionUsages: []commandActionUsage{
			{action: "show", args: "<automation>", description: "show live graph, runtime and resources"},
			{action: "open", args: "<automation>", description: "alias for show"},
		},
		examples: []string{
			`automations list`,
			`automations show "Nightly sweep"`,
			`automations open automation-id`,
			`automations run "Nightly sweep"`,
			`automations pause "Nightly sweep"`,
			`automations resume "Nightly sweep"`,
			`automations delete "Nightly sweep"`,
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			mm, cmd, ok := m.needProject()
			if !ok {
				return mm, cmd
			}
			action, rest := splitAction(actions, args)
			if action == "" && len(args) > 0 && strings.EqualFold(args[0], "run-now") {
				action, rest = "run", args[1:]
			}
			c, pid := m.client, m.selectedID
			ref := strings.Join(rest, " ")
			backendAction := action
			if backendAction == "run" {
				backendAction = "run-now"
			}

			switch action {
			case "", "list":
				return m, run("Automations", cmdTimeout, func(ctx context.Context) (string, error) {
					automations, err := c.ListAutomations(ctx, pid)
					if err != nil {
						return "", err
					}
					if jsonMode {
						return marshalJSON(automations)
					}
					return renderAutomations(automations, ref), nil
				})

			case "show", "open":
				if ref == "" {
					usage := commandUsage("automations", action)
					return selectorOr(m, usage,
						selectorFor("Automations", "automations "+action,
							automationEmptyStateHint, false,
							func(ctx context.Context) ([]selectorItem, error) {
								automations, err := c.ListAutomations(ctx, pid)
								if err != nil {
									return nil, err
								}
								items := make([]selectorItem, 0, len(automations))
								for _, a := range automations {
									a := a
									item := selectorItem{
										ref:    a.ID,
										label:  firstNonEmpty(a.Name, shortID(a.ID)),
										detail: a.State,
									}
									item.dispatch = func(m Model) (Model, tea.Cmd) {
										cmd := run("Automation", cmdTimeout, func(ctx context.Context) (string, error) {
											return loadAutomationDetail(ctx, c, pid, a)
										})
										m.busy = true
										return m, cmd
									}
									items = append(items, item)
								}
								return items, nil
							}))
				}
				return m, run("Automation", cmdTimeout, func(ctx context.Context) (string, error) {
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
					return loadAutomationDetail(ctx, c, pid, a)
				})

			default:
				if ref == "" {
					return selectorOr(m, fmt.Sprintf("usage: %sautomations %s <automation>", cmdPrefix, action),
						selectorFor("Automations", "automations "+action,
							automationEmptyStateHint, false,
							func(ctx context.Context) ([]selectorItem, error) {
								automations, err := c.ListAutomations(ctx, pid)
								if err != nil {
									return nil, err
								}
								items := make([]selectorItem, 0, len(automations))
								for _, a := range automations {
									a := a
									item := selectorItem{
										ref:    a.ID,
										label:  firstNonEmpty(a.Name, shortID(a.ID)),
										detail: a.State,
									}
									item.dispatch = func(m Model) (Model, tea.Cmd) {
										cmd := run("Automations", cmdTimeout, func(ctx context.Context) (string, error) {
											status := action + ": " + firstNonEmpty(a.Name, a.ID)
											return actAndReloadText(status,
												func() error { return c.AutomationAction(ctx, a.ID, backendAction, pid) },
												func() (string, error) { return c.GetAutomations(ctx, pid) })
										})
										if action == "delete" {
											return confirmOr(m,
												fmt.Sprintf("Delete automation %q? Type 'yes' to confirm or Esc to cancel.", a.ID),
												fmt.Sprintf("use --force to confirm deletion of automation %q", a.ID),
												cmd)
										}
										m.busy = true
										return m, cmd
									}
									items = append(items, item)
								}
								return items, nil
							}))
				}
				cmd := run("Automations", cmdTimeout, func(ctx context.Context) (string, error) {
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
					status := action + ": " + firstNonEmpty(a.Name, a.ID)
					if err := c.AutomationAction(ctx, a.ID, backendAction, pid); err != nil {
						return "", err
					}
					return refreshAndRender(status,
						func() ([]client.Automation, error) { return c.ListAutomations(ctx, pid) },
						func(automations []client.Automation, _ string) string {
							return renderAutomations(automations, "")
						})
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

func loadAutomationDetail(ctx context.Context, c *client.Client, projectID string, automation client.Automation) (string, error) {
	detail, err := c.GetAutomationDetail(ctx, projectID, automation.ID)
	if err != nil {
		// The backend deliberately has no live graph for draft automations and
		// may answer that route with 404. Preserve the resolved card identity and
		// say exactly what is unavailable instead of presenting it as a loaded
		// empty graph.
		if errors.Is(err, client.ErrAutomationNotFound) && strings.EqualFold(automation.State, "draft") {
			draft := automationDraftDetail(projectID, automation)
			detail = &draft
			if jsonMode {
				return marshalJSON(detail)
			}
			return renderAutomationDetail(*detail), nil
		}
		return "", err
	}
	if detail == nil {
		return "", fmt.Errorf("automation detail: backend returned no detail")
	}
	if jsonMode {
		return marshalJSON(detail)
	}
	return renderAutomationDetail(*detail), nil
}

func automationDraftDetail(projectID string, automation client.Automation) client.AutomationDetail {
	return client.AutomationDetail{
		Automation: client.AutomationMetadata{
			ID:             automation.ID,
			ProjectID:      projectID,
			Name:           automation.Name,
			LifecycleState: firstNonEmpty(automation.State, "draft"),
		},
		Nodes:                  make([]client.AutomationLiveNode, 0),
		Edges:                  make([]client.AutomationLiveEdge, 0),
		Resources:              make([]client.AutomationResourceSummary, 0),
		GraphAvailable:         false,
		NodesAvailable:         false,
		EdgesAvailable:         false,
		CountsAvailable:        false,
		ResourcesAvailable:     false,
		ExternalStateAvailable: false,
		Warnings:               []string{"live graph unavailable: automation is draft"},
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
			var cmd tea.Cmd
			m, cmd, ok := m.needProject()
			if !ok {
				return m, cmd
			}
			action, _ := splitAction(actions, args)
			c, pid := m.client, m.selectedID
			return m, run("Analytics", cmdTimeout, func(ctx context.Context) (string, error) {
				return loadAnalytics(ctx, c, pid, action)
			})
		},
	}
}

// --- projects / status / events / misc ---

func projectCommand() command {
	return command{
		name:          "project",
		args:          "<name>",
		selectorPaths: [][]string{{}},
		desc:          "select the active project",
		run: func(m Model, args []string) (Model, tea.Cmd) {
			m.busy = false
			if len(args) == 0 {
				if !m.projectsLoaded {
					var cmd tea.Cmd
					m, cmd = m.beginProjectLoadWithSSE(true, "", !cliMode)
					return m, cmd
				}
				if !cliMode && len(m.projects) > 1 {
					projects := m.projects
					return m, selectorFor("Projects", "project", "no projects", false,
						func(context.Context) ([]selectorItem, error) {
							items := make([]selectorItem, 0, len(projects))
							for _, p := range projects {
								items = append(items, selectorItem{
									ref:    p.ID,
									label:  p.Name,
									detail: truncate(p.Path, 40),
								})
							}
							return items, nil
						})
				}
				m.append(entry{role: "result", head: "Projects", text: renderProjects(m.projects, nil, m.selectedID)})
				return m, nil
			}
			name := strings.Join(args, " ")
			if len(m.projects) == 0 {
				var cmd tea.Cmd
				m, cmd = m.beginProjectLoadWithSSE(false, name, true)
				return m, cmd
			}
			return m.pickProject(name)
		},
	}
}

func projectsCommand() command {
	actions := []string{"list", "create"}
	return command{
		name:    "projects",
		actions: actions,
		desc:    "list or create backend-owned projects",
		usage: []string{
			"projects [list]                              list projects with running/queued counts",
			"projects create <name> <path>                create and select a local-path project",
			"projects create <name> | <path>              use | when the name or path contains spaces",
		},
		examples: []string{
			`projects create demo /Users/me/src/demo`,
			`projects create My Project | C:\Users\me\src\my-project`,
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			m.busy = false
			action, rest := splitAction(actions, args)
			if action == "create" {
				name, path, ok := parseProjectCreateArgs(rest)
				if !ok {
					return m, errCmd(projectCreateUsage())
				}
				m.busy = true
				requestID := nextProjectRequestID()
				m.projectRequestID = requestID
				startSSE := !cliMode
				c := m.client
				return m, func() tea.Msg {
					ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
					defer cancel()
					project, err := c.CreateProject(ctx, name, path)
					if err != nil {
						return projectCreatedMsg{requestID: requestID, startSSE: startSSE, err: err}
					}
					return projectCreatedMsg{requestID: requestID, startSSE: startSSE, project: *project}
				}
			}
			if jsonMode {
				c := m.client
				return m, run("Projects", cmdTimeout, func(ctx context.Context) (string, error) {
					projects, err := c.ListProjects(ctx)
					if err != nil {
						return "", err
					}
					return marshalJSON(projects)
				})
			}
			var cmd tea.Cmd
			m, cmd = m.beginProjectLoadWithSSE(true, "", !cliMode)
			return m, cmd
		},
	}
}

func projectCreateUsage() string {
	return fmt.Sprintf("usage: %sprojects create <name> <path> (or <name> | <path> when either contains spaces)", cmdPrefix)
}
func parseProjectCreateArgs(args []string) (string, string, bool) {
	if len(args) == 0 {
		return "", "", false
	}
	joined := strings.TrimSpace(strings.Join(args, " "))
	if strings.Contains(joined, "|") {
		name, path := splitPipe(joined)
		if name == "" || path == "" {
			return "", "", false
		}
		return name, path, true
	}
	if len(args) < 2 {
		return "", "", false
	}

	// Find an absolute-looking path first so names such as "My Project" can
	// still be entered without a pipe. This deliberately avoids filepath.IsAbs:
	// the command must preserve Windows paths even when running on Unix.
	for i := 1; i < len(args); i++ {
		if looksLikeProjectPath(args[i]) {
			name := strings.TrimSpace(strings.Join(args[:i], " "))
			path := strings.TrimSpace(strings.Join(args[i:], " "))
			if name == "" || path == "" {
				return "", "", false
			}
			return name, path, true
		}
	}

	name := strings.TrimSpace(args[0])
	path := strings.TrimSpace(strings.Join(args[1:], " "))
	if name == "" || path == "" {
		return "", "", false
	}
	return name, path, true
}

func looksLikeProjectPath(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\\`) ||
		strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
		return true
	}
	return len(value) >= 3 &&
		((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) &&
		value[1] == ':' && (value[2] == '/' || value[2] == '\\')
}

func loginCommand() command {
	return command{
		name:    "login",
		aliases: []string{"signin", "auth"},
		desc:    "sign in to a backend that requires authentication",
		usage: []string{
			"login                                      enter username and masked password",
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			if cliMode {
				return m, errCmd("interactive sign-in is available in the TUI; use -user/-pass or OPENVIBELY_AUTH_USERNAME/OPENVIBELY_AUTH_PASSWORD for CLI runs")
			}
			if len(args) > 0 {
				return m, errCmd("usage: /login")
			}
			return m.beginLogin()
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
			// RunCLI prefetches counts before rendering and drains returned
			// commands after dispatch. The one-shot output is already rendered,
			// so only interactive mode needs the follow-up refresh.
			if cliMode {
				return m, nil
			}
			return m, m.fetchStatusCounts()
		},
	}
}

func eventsCommand() command {
	return command{
		name:        "events",
		aliases:     []string{"stream", "log"},
		args:        "[on|off]",
		completions: []commandCompletion{{values: []string{"on", "off", "true", "false"}}},
		desc:        "stream live task/chat events (interactive toggle or CLI foreground monitor)",
		usage: []string{
			"events [on]                                interactive: show events from the TUI stream",
			"events off                                 interactive: hide events; CLI off cannot stop another process",
			"events on                                 one-shot CLI: monitor the selected project until Ctrl-C or EOF",
		},
		examples: []string{
			`events on`,
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			if cliMode {
				m.busy = false
				on, err := parseCLIEventsAction(args)
				if err != nil {
					return m, errCmd(err.Error())
				}
				if !on {
					return m, errCmd(cliEventsOffMessage)
				}
				return m, errCmd("events on must run through the foreground CLI stream; use the openvibely-tui events command")
			}
			if len(args) > 1 {
				return m, errCmd("usage: /events [on|off]")
			}
			on := !m.showEvents
			if len(args) == 1 {
				switch strings.ToLower(args[0]) {
				case "on", "true":
					on = true
				case "off", "false":
					on = false
				default:
					return m, errCmd("usage: /events [on|off]")
				}
			}
			m.busy = false
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
			// Even before a thread has finished opening, /chat owns the user's
			// navigation intent and invalidates delayed open/live-refresh results.
			m.threadOpenRequestID++
			m.threadRefreshRequestID++
			m.threadReplyPendingRequestID = 0
			if len(args) > 0 && m.hasPendingChat() {
				m.append(entry{role: "system", text: chatStillProcessingMessage})
				return m, nil
			}
			m.busy = false
			if m.threadID != "" {
				title := m.threadTitle
				m.threadID, m.threadTitle, m.threadStatus = "", "", ""
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
			submissionID, ok := m.beginChatSubmission(m.selectedID)
			if !ok {
				return m, nil
			}
			return m, m.sendChat(m.selectedID, strings.Join(args, " "), submissionID)
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
	// A successful explicit selection supersedes every older project request,
	// including a creation or list response that is still in flight.
	m.projectRequestID = nextProjectRequestID()
	shouldReconnect := m.sseCancel != nil || m.sseRetryAfterProject
	m.setActiveProject(p)
	m.append(entry{role: "system", text: "active project: " + p.Name})
	// If SSE is active, reconnect with the new project ID so the server
	// delivers only this project's events.
	if !m.authRequired && shouldReconnect {
		m.sseRetryAfterProject = false
		return m, m.connectSSE()
	}
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

// parsePersonalityAdd parses the two unambiguous add forms:
//
//	name | system prompt
//	name | description=<description> | system prompt
//
// The first form treats every character after the first pipe as prompt text.
// The second form recognizes the explicit description marker and treats only
// its first following pipe as structural, so prompt pipes remain intact.
func parsePersonalityAdd(s string) (name, description, prompt string, err error) {
	name, tail := splitPipe(s)
	if name == "" || tail == "" {
		return "", "", "", fmt.Errorf("name and system prompt are required")
	}

	const marker = "description="
	if strings.HasPrefix(strings.ToLower(tail), marker) {
		description, prompt = splitPipe(strings.TrimSpace(tail[len(marker):]))
		if description == "" || prompt == "" {
			return "", "", "", fmt.Errorf("optional description uses description=<description> | <system prompt> and requires non-empty description and system prompt")
		}
		return name, description, prompt, nil
	}

	return name, "", tail, nil
}

// splitPipe splits "left | right" on the first pipe.
func splitPipe(s string) (string, string) {
	if i := strings.Index(s, "|"); i >= 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
	}
	return strings.TrimSpace(s), ""
}

// parseWorkerLimit validates the sole numeric operand used by the global and
// project worker-limit commands. Zero is valid and means unlimited; malformed,
// negative, overflowing, and surplus operands are rejected before any request.
func parseWorkerLimit(action string, args []string) (int, error) {
	if len(args) != 1 {
		return 0, fmt.Errorf("usage: /workers %s <n>", action)
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n < 0 {
		return 0, fmt.Errorf("worker limit must be a positive number")
	}
	return n, nil
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
