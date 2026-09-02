package tui

// The slash-command registry: one entry per OpenVibely screen/resource, each
// with the actions the web UI offers for it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
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
		memoryCommand(),
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
		loginCommand(),
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

func tasksCommand() command {
	actions := []string{"list", "open", "show", "reviews", "lifecycle", "logs", "attachments", "attach", "attachment", "new", "edit", "run", "stop", "delete", "move", "order", "goal", "reply", "activate", "sweep", "clear"}
	return command{
		name:    "tasks",
		aliases: []string{"task", "t", "board"},
		args:    "[filter|id]",
		actions: actions,
		desc:    "the task board and task threads",
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
				if ref == "" {
					return taskSelector(m, "usage: /tasks open <id|title>", "tasks open", false)
				}
				return m, func() tea.Msg {
					ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
					defer cancel()
					t, err := resolveTask(ctx, c, pid, ref)
					if err != nil {
						return threadOpenedMsg{projectID: pid, err: err}
					}
					d, err := c.GetTaskForProject(ctx, t.ID, pid)
					if err != nil {
						return threadOpenedMsg{projectID: pid, err: err}
					}
					return threadOpenedMsg{
						projectID: pid,
						taskID:    t.ID,
						title:     firstNonEmpty(d.Task.Title, t.Title, shortID(t.ID)),
						body:      renderThread(d),
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
						return fmt.Sprintf("added review comment on %s:%d for %s\n\n%s", location.filePath, location.lineNumber, firstNonEmpty(t.Title, shortID(t.ID)), renderTaskReviews(t, reviews)), nil
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
// renders the execution list or the ordered event trace. A missing execution
// uses the TUI selector when several executions exist; CLI mode lists them so
// its output remains deterministic.
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

		execs, err := c.ListTaskLifecycleExecutionsForProject(ctx, task.ID, projectID)
		if err != nil {
			return resultMsg{title: "Task Lifecycle", err: err}
		}

		if executionRef != "" {
			execution, err := matchLifecycleExecution(execs, executionRef)
			if err != nil {
				return resultMsg{title: "Task Lifecycle", err: err}
			}
			return lifecycleEventsMessage(ctx, c, projectID, task, execution)
		}

		switch len(execs) {
		case 0:
			if jsonMode {
				body, err := marshalJSON(nonNilSlice(execs))
				return resultMsg{title: "Task Lifecycle", body: body, err: err}
			}
			return resultMsg{title: "Task Lifecycle", body: renderLifecycleExecutions(task, execs)}
		case 1:
			return lifecycleEventsMessage(ctx, c, projectID, task, execs[0])
		}

		if cliMode {
			if jsonMode {
				body, err := marshalJSON(execs)
				return resultMsg{title: "Task Lifecycle", body: body, err: err}
			}
			return resultMsg{title: "Task Lifecycle", body: renderLifecycleExecutions(task, execs)}
		}

		items := make([]selectorItem, 0, len(execs))
		for _, execution := range execs {
			label := firstNonEmpty(execution.SkillKey, execution.ID, "(unnamed execution)")
			detail := execution.Status
			if execution.StartedAt != "" {
				detail = strings.TrimSpace(detail + " · " + execution.StartedAt)
			}
			items = append(items, selectorItem{ref: execution.ID, label: label, detail: detail})
		}
		return selectorActiveMsg{
			title:     "Lifecycle Executions",
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
			"omit <task> on add → interactive selector",
			"schedule delete <id>                       remove a schedule",
			"schedule toggle <id>                       enable/disable a schedule",
			"omit <id> on delete/toggle → interactive selector",
		},
		actionUsages: []commandActionUsage{
			{action: "add", args: "<task> <2006-01-02T15:04> [once|daily|weekly|monthly|seconds|minutes|hours [interval]]"},
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
					if jsonMode {
						return marshalJSON(entries)
					}
					return renderSchedule(entries, summary), nil
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
					if err := c.CreateSchedule(ctx, t.ID, when, repeat, interval); err != nil {
						return "", err
					}
					entries, summary, _ := c.GetSchedule(ctx, pid)
					return "scheduled " + t.Title + " for " + when + " (" + repeat + ")\n\n" + renderSchedule(entries, summary), nil
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
										label: firstNonEmpty(e.Text, shortID(e.ScheduleID)),
									}
									item.dispatch = func(m Model) (Model, tea.Cmd) {
										cmd := run("Schedule", cmdTimeout, func(ctx context.Context) (string, error) {
											var err error
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

func alertsCommand() command {
	actions := []string{"list", "show", "read", "approve", "reject", "dismiss", "delete", "read-all", "clear"}
	return command{
		name:    "alerts",
		aliases: []string{"alert"},
		args:    "[id]",
		actions: actions,
		desc:    "notifications awaiting review",
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
											var err error
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
		name:    "skills",
		aliases: []string{"skill"},
		args:    "[handle]",
		actions: actions,
		desc:    "reusable skills the agents can load",
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
		name:    "memory",
		aliases: []string{"memories"},
		args:    "[file|query]",
		actions: actions,
		desc:    "read-only durable memory for the selected project",
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

func agentsCommand() command {
	actions := []string{"list", "delete", "generate", "metrics", "votes"}
	return command{
		name:    "agents",
		aliases: []string{"agent"},
		args:    "[name]",
		actions: actions,
		desc:    "agent definitions, workflow metrics and vote audits",
		usage: []string{
			"agents [filter]                            list agent definitions",
			"agents generate <description>              create an agent from a description",
			"agents delete <agent>                      remove an agent definition (omit <agent> → interactive selector)",
			"agents metrics                             per-agent workflow metrics",
		},
		actionUsages: []commandActionUsage{
			{action: "votes", args: "<step-execution-id>", description: "inspect parallel-step votes"},
		},
		examples: []string{
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
										cmd := run("Agents", cmdTimeout, func(ctx context.Context) (string, error) {
											if err := c.DeleteAgent(ctx, a.ID); err != nil {
												return "", err
											}
											return refreshAndRender("deleted "+a.Name,
												func() ([]client.AgentDef, error) { return c.ListAgents(ctx, pid) },
												renderAgents)
										})
										return confirmOr(m,
											fmt.Sprintf("Delete agent %q? Type 'yes' to confirm or Esc to cancel.", a.ID),
											fmt.Sprintf("use --force to confirm deletion of agent %q", a.ID),
											cmd)
									}
									items = append(items, item)
								}
								return items, nil
							}))
				}
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
		name:    "models",
		aliases: []string{"model"},
		args:    "[name]",
		actions: actions,
		desc:    "configured LLM models, worker capacity and provider health",
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
			mm, cmd, ok := m.needProject()
			if !ok {
				return mm, cmd
			}
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
					if jsonMode {
						return marshalJSON(list)
					}
					return renderModels(list, ref), nil
				})
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
					wg       sync.WaitGroup
					text     string
					textErr  error
					capacity *client.GlobalCapacity
				)
				wg.Add(2)
				go func() { defer wg.Done(); text, textErr = c.GetWorkerSettings(ctx, pid) }()
				go func() { defer wg.Done(); capacity, _ = c.GetGlobalCapacity(ctx) }()
				wg.Wait()
				if textErr != nil {
					return "", textErr
				}
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
			"channels list                              list configured integrations",
			"channels test <channel>                    send a test message (telegram, slack, discord, email)",
			"channels remove <channel>                  disconnect an integration (telegram, slack, discord, email)",
			"omit <channel> on test/remove → interactive selector",
			"Note: GitHub and Slack OAuth connect/callback require a browser (known parity gap).",
		},
		examples: []string{
			`channels test telegram`,
			`channels test email`,
			`channels remove discord`,
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
				return m, run("Channels", cmdTimeout, func(ctx context.Context) (string, error) {
					return c.GetChannels(ctx, pid)
				})
			default:
				if ref == "" {
					return selectorOr(m, fmt.Sprintf("usage: /channels %s <channel>", action),
						selectorFor("Channels", "channels "+action, "no channels available", false,
							func(ctx context.Context) ([]selectorItem, error) {
								items := make([]selectorItem, 0, len(client.KnownChannels))
								for _, ch := range client.KnownChannels {
									items = append(items, selectorItem{ref: ch.Type, label: ch.Name})
								}
								return items, nil
							}))
				}
				cmd := run("Channels", cmdTimeout, func(ctx context.Context) (string, error) {
					ch, err := matchRef(client.KnownChannels, ref,
						func(ch client.Channel) string { return ch.Type },
						func(ch client.Channel) string { return ch.Name })
					if err != nil {
						return "", err
					}
					status := action + ": " + ch.Name
					return actAndReloadText(status,
						func() error { return c.ChannelAction(ctx, ch.Type, action, pid) },
						func() (string, error) { return c.GetChannels(ctx, pid) })
				})
				if action == "remove" {
					return confirmOr(m,
						fmt.Sprintf("Remove channel %q? Type 'yes' to confirm or Esc to cancel.", ref),
						fmt.Sprintf("use --force to confirm removal of channel %q", ref),
						cmd)
				}
				return m, cmd
			}
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
		name:    "personality",
		args:    "[key|name]",
		actions: actions,
		desc:    "built-in and custom assistant personalities",
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
	actions := []string{"list", "show", "open", "run-now", "pause", "resume", "delete"}
	return command{
		name:    "automations",
		aliases: []string{"automation"},
		args:    "[filter]",
		actions: actions,
		desc:    "recurring automations and workflow rules",
		usage: []string{
			"automations [filter]                       list automations",
			"automations show <automation>              show live graph, runtime and resources",
			"automations open <automation>              alias for show",
			"automations run-now <automation>           trigger an immediate run",
			"automations pause <automation>              pause an active automation",
			"automations resume <automation>             resume a paused automation",
			"automations delete <automation>             remove an automation",
			"omit <automation> on show/open/run-now/pause/resume/delete → interactive selector",
		},
		actionUsages: []commandActionUsage{
			{action: "show", args: "<automation>", description: "show live graph, runtime and resources"},
			{action: "open", args: "<automation>", description: "alias for show"},
		},
		examples: []string{
			`automations show "Nightly sweep"`,
			`automations open automation-id`,
			`automations run-now "Nightly sweep"`,
			`automations pause "Nightly sweep"`,
			`automations resume "Nightly sweep"`,
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
					return selectorOr(m, fmt.Sprintf("usage: /automations %s <automation>", action),
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
												func() error { return c.AutomationAction(ctx, a.ID, action, pid) },
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
					if err := c.AutomationAction(ctx, a.ID, action, pid); err != nil {
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

// --- projects / status / build / events / misc ---

func projectCommand() command {
	return command{
		name: "project",
		args: "<name>",
		desc: "select the active project",
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
		desc:    "stream live task/chat events (interactive toggle or CLI foreground monitor)",
		usage: []string{
			"events [on]                                interactive: show events from the TUI stream",
			"events off                                 interactive: hide events; CLI off cannot stop another process",
			"events on                                 one-shot CLI: monitor the selected project until Ctrl-C or EOF",
		},
		examples: []string{
			`events on`,
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			m.busy = false
			if cliMode {
				on, err := parseCLIEventsAction(args)
				if err != nil {
					return m, errCmd(err.Error())
				}
				if !on {
					return m, errCmd(cliEventsOffMessage)
				}
				return m, errCmd("events on must run through the foreground CLI stream; use the openvibely-tui events command")
			}
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
			if len(args) > 0 && m.hasPendingChat() {
				m.append(entry{role: "system", text: chatStillProcessingMessage})
				return m, nil
			}
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
