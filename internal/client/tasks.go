package client

// Task board (kanban) access.
//
// The backend renders /tasks as an HTMX kanban board, so the board is scraped
// from the rendered task cards (each carries data-task-id / data-task-status /
// data-task-category / data-display-order). Mutations reuse the same routes the
// web UI posts to.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/net/html"
)

// Task is one card on the kanban board.
type Task struct {
	ID           string
	ProjectID    string
	Title        string
	Prompt       string
	Category     string // backlog | active | completed | scheduled
	Status       string // pending | queued | running | completed | failed | cancelled | blocked
	DisplayOrder int
	Badges       []string // model, agent, tag, priority, Goal, Chain, Swarm...
}

// TaskDetail is the task detail page split into its tabs.
type TaskDetail struct {
	Task     Task
	Details  string // Details tab (prompt, goal, metrics, executions)
	Thread   string // Thread tab (conversation)
	Changes  string // Changes tab (diff summary)
	Schedule string // Schedules tab
	Chaining string // Chaining tab
	Attach   string // Attachments tab
	Life     string // Lifecycle tab
}

// TabText returns one detail tab's text by name.
func (d TaskDetail) TabText(tab string) string {
	switch strings.ToLower(tab) {
	case "thread", "chat":
		return d.Thread
	case "changes", "diff":
		return d.Changes
	case "schedules", "schedule":
		return d.Schedule
	case "chaining", "chain":
		return d.Chaining
	case "attachments", "attach":
		return d.Attach
	case "lifecycle":
		return d.Life
	default:
		return d.Details
	}
}

// TaskForm carries create/update fields for a task.
type TaskForm struct {
	Title    string
	Prompt   string
	Category string // backlog | active
	Priority int    // 0-4
	Tag      string // "", feature, bug
}

func (f TaskForm) values() url.Values {
	v := url.Values{}
	v.Set("title", f.Title)
	v.Set("prompt", f.Prompt)
	if f.Category != "" {
		v.Set("category", f.Category)
	}
	v.Set("priority", strconv.Itoa(f.Priority))
	v.Set("tag", f.Tag)
	return v
}

// ListTasks scrapes the kanban board for a project. An empty projectID lets
// the backend fall back to the first project.
func (c *Client) ListTasks(ctx context.Context, projectID string) ([]Task, error) {
	root, err := c.getHTML(ctx, "/tasks"+query("project_id", projectID))
	if err != nil {
		return nil, err
	}
	return parseTaskCards(root, projectID), nil
}

// parseTaskCards extracts tasks from rendered kanban cards.
//
// A card's text begins with its kebab-menu items ("Run", "Cancel", "Edit"), so
// the title must come from the card's own task link rather than the first line
// of text. The template renders it as:
//
//	<a href="/tasks/<id>?from=tasks" title="<title>">{ task.Title }</a>
//	<p class="... line-clamp-2 ...">{ task.Prompt }</p>
//	<span class="badge ...">Goal</span> ...
func parseTaskCards(root *html.Node, projectID string) []Task {
	nodes := findAll(root, func(e *html.Node) bool { return attr(e, "data-task-id") != "" })

	seen := map[string]bool{}
	tasks := make([]Task, 0, len(nodes))
	for _, n := range nodes {
		id := attr(n, "data-task-id")
		category := attr(n, "data-task-category")
		if id == "" || category == "" || seen[id] {
			continue // nested control or duplicate, not a card root
		}
		seen[id] = true

		order, _ := strconv.Atoi(attr(n, "data-display-order"))
		tasks = append(tasks, Task{
			ID:           id,
			ProjectID:    projectID,
			Title:        cardTitle(n, id),
			Prompt:       cardPrompt(n),
			Category:     category,
			Status:       attr(n, "data-task-status"),
			DisplayOrder: order,
			Badges:       cardBadges(n),
		})
	}
	return tasks
}

// cardTitle returns the task title from the card's own detail link.
//
// Several links on a card point at /tasks/<id>: the kebab menu's "Edit" entry
// (…?tab=details&from=tasks) and the title link (…?from=tasks). Only the title
// link carries a title attribute holding the full task title, so that is
// required first; the remaining links are ignored rather than yielding "Edit".
func cardTitle(card *html.Node, id string) string {
	want := "/tasks/" + id
	targetsTask := func(e *html.Node) bool {
		if e.Data != "a" {
			return false
		}
		href := attr(e, "href")
		if href == "" {
			href = attr(e, "hx-get")
		}
		// Must target this task, not a swarm child.
		return href == want || strings.HasPrefix(href, want+"?")
	}

	// Preferred: the link whose title attribute holds the full task title.
	if link := findNode(card, func(e *html.Node) bool {
		return targetsTask(e) && strings.TrimSpace(attr(e, "title")) != ""
	}); link != nil {
		return strings.TrimSpace(attr(link, "title"))
	}

	// Fallback: a task link that is not a tab deep-link (i.e. not the kebab
	// menu's Edit entry), using its text.
	if link := findNode(card, func(e *html.Node) bool {
		return targetsTask(e) && !strings.Contains(attr(e, "href")+attr(e, "hx-get"), "tab=")
	}); link != nil {
		return firstLine(NodeText(link))
	}
	return ""
}

// cardPrompt returns the prompt preview paragraph of a task card.
func cardPrompt(card *html.Node) string {
	p := findNode(card, func(e *html.Node) bool {
		return e.Data == "p" && strings.Contains(attr(e, "class"), "line-clamp")
	})
	if p == nil {
		return ""
	}
	return strings.TrimSpace(NodeText(p))
}

// cardBadges collects the badge labels shown on a card (Goal, Chain, Swarm,
// model, agent, tag, priority).
func cardBadges(card *html.Node) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range findAll(card, func(e *html.Node) bool {
		return e.Data == "span" && strings.Contains(attr(e, "class"), "badge")
	}) {
		label := strings.TrimSpace(NodeText(n))
		if label == "" || seen[label] {
			continue
		}
		seen[label] = true
		out = append(out, label)
	}
	return out
}

// GetTask fetches the task detail page and extracts each tab's content.
func (c *Client) GetTask(ctx context.Context, taskID string) (*TaskDetail, error) {
	root, err := c.getHTML(ctx, "/tasks/"+url.PathEscape(taskID))
	if err != nil {
		return nil, err
	}
	d := &TaskDetail{Task: Task{ID: taskID}}

	// The page heading carries the real title.
	if h := findNode(root, func(e *html.Node) bool {
		return e.Data == "h2" && strings.Contains(attr(e, "class"), "font-bold")
	}); h != nil {
		d.Task.Title = strings.TrimSpace(NodeText(h))
	}
	if n := findNode(root, func(e *html.Node) bool { return attr(e, "data-task-status") != "" }); n != nil {
		d.Task.Status = attr(n, "data-task-status")
	}
	if n := findNode(root, func(e *html.Node) bool { return attr(e, "data-task-category") != "" }); n != nil {
		d.Task.Category = attr(n, "data-task-category")
	}

	// Tab panels are rendered into the page as #tab-<name> containers; the
	// thread and changes panels defer their content to a lazy hx-get fragment.
	for _, s := range []struct {
		id  string
		dst *string
	}{
		{"tab-details", &d.Details},
		{"tab-chat", &d.Thread},
		{"tab-changes", &d.Changes},
		{"tab-schedules", &d.Schedule},
		{"tab-chaining", &d.Chaining},
		{"tab-attachments", &d.Attach},
		{"tab-lifecycle", &d.Life},
	} {
		if n := findByID(root, s.id); n != nil {
			*s.dst = NodeText(n)
		}
	}
	if d.Details == "" {
		if n := findByID(root, "task-detail-view"); n != nil {
			d.Details = NodeText(n)
		}
	}

	// Thread and changes load asynchronously in the browser, so their panels
	// are empty placeholders in the initial page; fetch the real fragments.
	// Lifecycle executions (when needed) are independent of both, so all three
	// fetches run concurrently rather than sequentially.
	var wg sync.WaitGroup
	var threadNode, changesNode *html.Node
	var threadErr, changesErr error

	wg.Add(1)
	go func() {
		defer wg.Done()
		threadNode, threadErr = c.getHTML(ctx, "/tasks/"+url.PathEscape(taskID)+"/thread")
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		changesNode, changesErr = c.getHTML(ctx, "/tasks/"+url.PathEscape(taskID)+"/changes")
	}()

	needLife := d.Life == ""
	var execs []LifecycleExecution
	var lifeErr error
	if needLife {
		wg.Add(1)
		go func() {
			defer wg.Done()
			execs, lifeErr = c.ListTaskLifecycleExecutions(ctx, taskID)
		}()
	}

	wg.Wait()

	if threadErr == nil {
		if text := NodeText(threadNode); text != "" {
			d.Thread = text
		}
	}
	if changesErr == nil {
		if text := NodeText(changesNode); text != "" {
			d.Changes = text
		}
	}
	if needLife && lifeErr == nil && len(execs) > 0 {
		var b strings.Builder
		for _, e := range execs {
			fmt.Fprintf(&b, "%s  %s  %s  %s\n", e.When, e.SkillKey, e.Status, e.StartedAt)
		}
		d.Life = b.String()
	}
	return d, nil
}

// CreateTask creates a task. Category "active" submits it immediately.
func (c *Client) CreateTask(ctx context.Context, projectID string, form TaskForm) error {
	return c.doForm(ctx, http.MethodPost, "/tasks"+query("project_id", projectID), form.values())
}

// UpdateTask updates a task's editable fields.
func (c *Client) UpdateTask(ctx context.Context, taskID string, form TaskForm) error {
	return c.doForm(ctx, http.MethodPut, "/tasks/"+url.PathEscape(taskID), form.values())
}

// DeleteTask removes a task.
func (c *Client) DeleteTask(ctx context.Context, taskID string) error {
	return c.doForm(ctx, http.MethodDelete, "/tasks/"+url.PathEscape(taskID), nil)
}

// RunTask (re-)submits a task for execution.
func (c *Client) RunTask(ctx context.Context, taskID string) error {
	return c.doForm(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/run", nil)
}

// CancelTask stops a queued/running task.
func (c *Client) CancelTask(ctx context.Context, taskID string) error {
	return c.doForm(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/cancel", nil)
}

// MoveTask moves a task between kanban columns (drag & drop equivalent).
func (c *Client) MoveTask(ctx context.Context, taskID, category string) error {
	v := url.Values{}
	v.Set("category", category)
	return c.doForm(ctx, http.MethodPatch, "/tasks/"+url.PathEscape(taskID)+"/category", v)
}

// ReorderTask moves a task to a new position within its column.
func (c *Client) ReorderTask(ctx context.Context, taskID string, position int) error {
	v := url.Values{}
	v.Set("position", strconv.Itoa(position))
	return c.doForm(ctx, http.MethodPatch, "/tasks/"+url.PathEscape(taskID)+"/reorder", v)
}

// SendTaskThreadMessage posts a follow-up message into a task's thread.
func (c *Client) SendTaskThreadMessage(ctx context.Context, taskID, message string) error {
	v := url.Values{}
	v.Set("message", message)
	return c.doForm(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/thread", v)
}

// SetTaskGoal sets a task's completion goal.
func (c *Client) SetTaskGoal(ctx context.Context, taskID, objective string) error {
	v := url.Values{}
	v.Set("objective", objective)
	v.Set("goal", objective)
	return c.doForm(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/goal", v)
}

// ClearTaskGoal removes a task's goal.
func (c *Client) ClearTaskGoal(ctx context.Context, taskID string) error {
	return c.doForm(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/goal/clear", nil)
}

// SweepCompletedTasks moves finished active tasks into the completed column.
func (c *Client) SweepCompletedTasks(ctx context.Context, projectID string) error {
	return c.doForm(ctx, http.MethodPost, "/tasks/move-completed"+query("project_id", projectID), nil)
}

// ClearCompletedTasks deletes every completed task.
func (c *Client) ClearCompletedTasks(ctx context.Context, projectID string) error {
	return c.doForm(ctx, http.MethodDelete, "/tasks/completed"+query("project_id", projectID), nil)
}

// ClearBacklogTasks deletes every backlog task.
func (c *Client) ClearBacklogTasks(ctx context.Context, projectID string) error {
	return c.doForm(ctx, http.MethodDelete, "/tasks/backlog"+query("project_id", projectID), nil)
}

// ActivateBacklog moves every backlog task to active (starting execution).
func (c *Client) ActivateBacklog(ctx context.Context, projectID string) error {
	return c.doForm(ctx, http.MethodPost, "/tasks/backlog/activate"+query("project_id", projectID), nil)
}
