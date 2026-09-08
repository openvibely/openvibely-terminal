package client

// Task board (kanban) access.
//
// The backend renders /tasks as an HTMX kanban board, so the board is scraped
// from the rendered task cards (each carries data-task-id / data-task-status /
// data-task-category / data-display-order). Mutations reuse the same routes the
// web UI posts to.

import (
	"context"
	"encoding/json"
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
	ID           string   `json:"id"`
	ProjectID    string   `json:"project_id"`
	Title        string   `json:"title"`
	Prompt       string   `json:"prompt"`
	Category     string   `json:"category"` // backlog | active | completed | scheduled
	Status       string   `json:"status"`   // pending | queued | running | completed | failed | cancelled | blocked
	DisplayOrder int      `json:"display_order"`
	Badges       []string `json:"badges"` // model, agent, tag, priority, Goal, Chain, Swarm...
}

// TaskDetail is the task detail page split into its tabs.
type TaskDetail struct {
	Task     Task
	Details  string // Details tab (prompt, goal, metrics, executions)
	Thread   string // Thread tab (conversation)
	Changes  string // Changes tab (diff summary)
	Review   string // Review tab (inline code review comments)
	Schedule string // Schedules tab
	Chaining string // Chaining tab
	Attach   string // Attachments tab
	// Attachments contains the structured attachment records parsed from the
	// attachment controls in the task detail page. Attach remains the rendered
	// tab text so existing task-detail output is unchanged.
	Attachments []Attachment `json:"attachments,omitempty"`
	Life        string       // Lifecycle tab

	// loadErrors is populated only when an independent lazy tab request fails.
	// It is intentionally private; callers use TabError so normal tab text and
	// JSON compatibility remain unchanged.
	loadErrors *TaskDetailLoadError
}

// TaskDetailLoadError reports failures from independent lazy task-detail
// requests. The returned TaskDetail is still useful and contains every
// successfully loaded section. Unwrap preserves the original errors so
// errors.Is/errors.As continue to recognize authentication and transport
// failures.
type TaskDetailLoadError struct {
	Thread    error
	Changes   error
	Lifecycle error
}

func (e *TaskDetailLoadError) Error() string {
	if e == nil {
		return "task detail load failed"
	}
	failures := make([]string, 0, 3)
	for _, failure := range []struct {
		name string
		err  error
	}{
		{name: "thread", err: e.Thread},
		{name: "changes", err: e.Changes},
		{name: "lifecycle", err: e.Lifecycle},
	} {
		if failure.err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", failure.name, failure.err))
		}
	}
	if len(failures) == 0 {
		return "task detail load failed"
	}
	return "task detail load failed: " + strings.Join(failures, "; ")
}

// Unwrap exposes every failed lazy request to the standard errors package.
func (e *TaskDetailLoadError) Unwrap() []error {
	if e == nil {
		return nil
	}
	errs := make([]error, 0, 3)
	for _, err := range []error{e.Thread, e.Changes, e.Lifecycle} {
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

func (e *TaskDetailLoadError) hasErrors() bool {
	return e != nil && (e.Thread != nil || e.Changes != nil || e.Lifecycle != nil)
}

// TaskDetailTab describes one task detail tab across command parsing, display,
// backend panel extraction, aliases, and TaskDetail field access.
type TaskDetailTab struct {
	Name     string
	Label    string
	PanelIDs []string
	Aliases  []string
	field    func(*TaskDetail) *string
}

var taskDetailTabs = []TaskDetailTab{
	{Name: "details", Label: "Details", PanelIDs: []string{"tab-details"}, field: func(d *TaskDetail) *string { return &d.Details }},
	{Name: "thread", Label: "Thread", PanelIDs: []string{"tab-chat"}, Aliases: []string{"chat"}, field: func(d *TaskDetail) *string { return &d.Thread }},
	{Name: "changes", Label: "Changes", PanelIDs: []string{"tab-changes"}, Aliases: []string{"diff"}, field: func(d *TaskDetail) *string { return &d.Changes }},
	{Name: "review", Label: "Review", PanelIDs: []string{"tab-review", "tab-reviews"}, Aliases: []string{"reviews"}, field: func(d *TaskDetail) *string { return &d.Review }},
	{Name: "schedules", Label: "Schedules", PanelIDs: []string{"tab-schedules"}, Aliases: []string{"schedule"}, field: func(d *TaskDetail) *string { return &d.Schedule }},
	{Name: "chaining", Label: "Chaining", PanelIDs: []string{"tab-chaining"}, Aliases: []string{"chain"}, field: func(d *TaskDetail) *string { return &d.Chaining }},
	{Name: "attachments", Label: "Attachments", PanelIDs: []string{"tab-attachments"}, Aliases: []string{"attach"}, field: func(d *TaskDetail) *string { return &d.Attach }},
	{Name: "lifecycle", Label: "Lifecycle", PanelIDs: []string{"tab-lifecycle"}, field: func(d *TaskDetail) *string { return &d.Life }},
}

// TaskDetailTabs returns the supported task detail tabs in display order.
func TaskDetailTabs() []TaskDetailTab {
	return append([]TaskDetailTab(nil), taskDetailTabs...)
}

// TaskDetailTabByName resolves a canonical tab name or alias.
func TaskDetailTabByName(name string) (TaskDetailTab, bool) {
	for _, tab := range taskDetailTabs {
		if tab.Matches(name) {
			return tab, true
		}
	}
	return TaskDetailTab{}, false
}

// Matches reports whether name is this tab's canonical name or an alias.
func (tab TaskDetailTab) Matches(name string) bool {
	name = strings.ToLower(name)
	if name == tab.Name {
		return true
	}
	for _, alias := range tab.Aliases {
		if name == alias {
			return true
		}
	}
	return false
}

// Text returns this tab's TaskDetail field.
func (tab TaskDetailTab) Text(d *TaskDetail) string {
	if d == nil || tab.field == nil {
		return ""
	}
	return *tab.field(d)
}

func (tab TaskDetailTab) setText(d *TaskDetail, text string) {
	if d == nil || tab.field == nil {
		return
	}
	*tab.field(d) = text
}

// TabText returns one detail tab's text by name.
func (d TaskDetail) TabText(tab string) string {
	if meta, ok := TaskDetailTabByName(tab); ok {
		return meta.Text(&d)
	}
	return d.Details
}

// TabError returns the lazy-load error for one detail tab, resolving aliases in
// the same way as TabText. A nil error means the tab loaded successfully or its
// intentionally empty response was valid.
func (d TaskDetail) TabError(tab string) error {
	if d.loadErrors == nil {
		return nil
	}
	meta, ok := TaskDetailTabByName(tab)
	if !ok {
		return nil
	}
	switch meta.Name {
	case "thread":
		return d.loadErrors.Thread
	case "changes":
		return d.loadErrors.Changes
	case "lifecycle":
		return d.loadErrors.Lifecycle
	default:
		return nil
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

// ReviewComment is one inline code review comment attached to a task diff line.
type ReviewComment struct {
	ID          string `json:"id"`
	TaskID      string `json:"task_id"`
	FilePath    string `json:"file_path"`
	LineNumber  int    `json:"line_number"`
	LineType    string `json:"line_type,omitempty"`
	CommentText string `json:"comment_text"`
	ReviewedBy  string `json:"reviewed_by,omitempty"`
	State       string `json:"state,omitempty"`
	Resolved    bool   `json:"resolved,omitempty"`
}

// ReviewCommentForm carries fields for creating an inline review comment.
type ReviewCommentForm struct {
	FilePath    string
	LineNumber  int
	LineType    string
	CommentText string
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

func (f ReviewCommentForm) values() url.Values {
	v := url.Values{}
	v.Set("file_path", f.FilePath)
	v.Set("line_number", strconv.Itoa(f.LineNumber))
	if f.LineType != "" {
		v.Set("line_type", f.LineType)
	}
	v.Set("comment_text", f.CommentText)
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

// reviewCommentText preserves the author's formatting while ignoring HTML that
// cannot contribute visible review text. Unlike NodeText, it deliberately keeps
// text-node whitespace and emits one newline for each explicit br boundary.
func reviewCommentText(n *html.Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		switch node.Type {
		case html.TextNode:
			b.WriteString(node.Data)
			return
		case html.ElementNode:
			if skippedTags[node.Data] {
				return
			}
			if node.Data == "br" {
				b.WriteByte('\n')
				return
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return strings.TrimSpace(b.String())
}

// parseReviewComments extracts review comments from the backend's HTMX fragment.
func parseReviewComments(root *html.Node, taskID string) []ReviewComment {
	nodes := findAll(root, func(e *html.Node) bool {
		return strings.Contains(" "+attr(e, "class")+" ", " review-comment-item ")
	})
	comments := make([]ReviewComment, 0, len(nodes))
	for _, n := range nodes {
		lineNumber, _ := strconv.Atoi(attr(n, "data-line-number"))
		text := ""
		if p := findNode(n, func(e *html.Node) bool { return e.Data == "p" }); p != nil {
			text = reviewCommentText(p)
		}
		reviewedBy := attr(n, "data-reviewed-by")
		if reviewedBy == "" {
			if span := findNode(n, func(e *html.Node) bool { return e.Data == "span" }); span != nil {
				reviewedBy = strings.TrimSpace(NodeText(span))
			}
		}
		reviewTaskID := attr(n, "data-task-id")
		if reviewTaskID == "" {
			reviewTaskID = taskID
		}
		state := attr(n, "data-state")
		if state == "" {
			state = attr(n, "data-review-state")
		}
		comments = append(comments, ReviewComment{
			ID:          attr(n, "data-comment-id"),
			TaskID:      reviewTaskID,
			FilePath:    attr(n, "data-file-path"),
			LineNumber:  lineNumber,
			LineType:    attr(n, "data-line-type"),
			CommentText: text,
			ReviewedBy:  reviewedBy,
			State:       state,
			Resolved:    attr(n, "data-resolved") == "true" || attr(n, "data-resolved") == "1",
		})
	}
	return comments
}

// GetTask fetches the task detail page and extracts each tab's content.
func (c *Client) GetTask(ctx context.Context, taskID string) (*TaskDetail, error) {
	return c.getTask(ctx, taskID, "", false, true)
}

// GetTaskForProject fetches a task detail page within the selected project.
// Task attachments are embedded in this page, so the project query is part of
// the attachment read contract as well as the mutation contract.
func (c *Client) GetTaskForProject(ctx context.Context, taskID, projectID string) (*TaskDetail, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("project ID is required for task details")
	}
	return c.getTask(ctx, taskID, projectID, false, true)
}

// GetTaskForProjectExact fetches task detail after requiring the response's
// task and project metadata to exactly match the requested identity. It is for
// callers that already hold a canonical full task ID and therefore must not
// discover the task through the board first.
func (c *Client) GetTaskForProjectExact(ctx context.Context, taskID, projectID string) (*TaskDetail, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("project ID is required for task details")
	}
	return c.getTask(ctx, taskID, projectID, true, true)
}

// GetTaskMetadataForProjectExact validates and returns only metadata embedded
// in the initial task detail page. It avoids loading lazy tabs when the caller
// only needs task identity, such as JSON summary or review rendering.
func (c *Client) GetTaskMetadataForProjectExact(ctx context.Context, taskID, projectID string) (*TaskDetail, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("project ID is required for task details")
	}
	return c.getTask(ctx, taskID, projectID, true, false)
}

func (c *Client) getTask(ctx context.Context, taskID, projectID string, exact, loadLazy bool) (*TaskDetail, error) {
	root, err := c.getHTML(ctx, "/tasks/"+url.PathEscape(taskID)+query("project_id", projectID))
	if err != nil {
		return nil, err
	}
	if exact && (taskDetailTaskID(root) != taskID || taskDetailProjectID(root) != projectID) {
		return nil, fmt.Errorf("task %q was not found in selected project", taskID)
	}
	d := &TaskDetail{Task: Task{ID: taskID, ProjectID: projectID}, Attachments: make([]Attachment, 0)}
	if exact {
		metadata, metadataErr := c.getExactTaskMetadata(ctx, taskID, projectID)
		if metadataErr != nil {
			return nil, metadataErr
		}
		d.Task = metadata.Task
		populateExactTaskMetadata(root, &d.Task, metadata)
	}

	// The legacy detail page heading carries the real title.
	if d.Task.Title == "" {
		if h := findNode(root, func(e *html.Node) bool {
			return e.Data == "h2" && strings.Contains(attr(e, "class"), "font-bold")
		}); h != nil {
			d.Task.Title = strings.TrimSpace(NodeText(h))
		}
	}
	if d.Task.Status == "" {
		if n := findNode(root, func(e *html.Node) bool { return attr(e, "data-task-status") != "" }); n != nil {
			d.Task.Status = attr(n, "data-task-status")
		}
	}
	if d.Task.Category == "" {
		if n := findNode(root, func(e *html.Node) bool { return attr(e, "data-task-category") != "" }); n != nil {
			d.Task.Category = attr(n, "data-task-category")
		}
	}

	// Tab panels are rendered into the page as #tab-<name> containers; the
	// thread and changes panels defer their content to a lazy hx-get fragment.
	for _, tab := range taskDetailTabs {
		for _, panelID := range tab.PanelIDs {
			if n := findByID(root, panelID); n != nil {
				tab.setText(d, NodeText(n))
				break
			}
		}
	}
	if projectID != "" {
		d.Attachments, err = parseTaskAttachmentsForProject(root, taskID, projectID)
		if err != nil {
			return nil, err
		}
	}
	// Keep the legacy GetTask path text-compatible without exposing structured
	// attachment records when no selected project was supplied. Callers that
	// need attachment IDs or sizes must use GetTaskForProject.
	if d.Details == "" {
		if n := findByID(root, "task-detail-view"); n != nil {
			d.Details = NodeText(n)
		}
	}
	if exact && d.Task.Prompt == "" {
		if prompt := findByID(root, "task-prompt-panel"); prompt != nil {
			if value := findNode(prompt, func(e *html.Node) bool {
				return strings.Contains(" "+attr(e, "class")+" ", " textarea ")
			}); value != nil {
				d.Task.Prompt = strings.TrimSpace(NodeText(value))
			}
		}
	}
	if !loadLazy {
		return d, nil
	}

	// Thread and changes load asynchronously in the browser, so their panels
	// are empty placeholders in the initial page; fetch the real fragments.
	// Lifecycle executions (when needed) are independent of both, so all three
	// fetches run concurrently rather than sequentially.
	var wg sync.WaitGroup
	var threadNode, changesNode *html.Node
	var threadErr, changesErr error

	threadPath := "/tasks/" + url.PathEscape(taskID) + "/thread"
	changesPath := "/tasks/" + url.PathEscape(taskID) + "/changes"
	if exact {
		threadPath += query("project_id", projectID)
		changesPath += query("project_id", projectID)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		threadNode, threadErr = c.getHTML(ctx, threadPath)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		changesNode, changesErr = c.getHTML(ctx, changesPath)
	}()

	needLife := d.Life == ""
	var execs []LifecycleExecution
	var lifeErr error
	if needLife {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if projectID != "" {
				execs, lifeErr = c.ListTaskLifecycleExecutionsForProject(ctx, taskID, projectID)
				return
			}
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

	loadErr := &TaskDetailLoadError{
		Thread:  threadErr,
		Changes: changesErr,
	}
	if needLife {
		loadErr.Lifecycle = lifeErr
	}
	if loadErr.hasErrors() {
		d.loadErrors = loadErr
		return d, loadErr
	}
	return d, nil
}

type exactTaskMetadata struct {
	Task
	ParentTaskID      *string `json:"parent_task_id"`
	ChainConfig       string  `json:"chain_config"`
	SwarmRole         string  `json:"swarm_role"`
	HasGoal           bool    `json:"has_goal"`
	AgentID           *string `json:"agent_id"`
	AgentDefinitionID *string `json:"agent_definition_id"`
}

func (c *Client) getExactTaskMetadata(ctx context.Context, taskID, projectID string) (*exactTaskMetadata, error) {
	var metadata exactTaskMetadata
	path := "/api/tasks/" + url.PathEscape(taskID) + "/swarm" + query("project_id", projectID)
	if err := c.getJSON(ctx, path, &metadata); err != nil {
		return nil, err
	}
	if metadata.ID != taskID || metadata.ProjectID != projectID {
		return nil, fmt.Errorf("task %q was not found in selected project", taskID)
	}
	return &metadata, nil
}

func populateExactTaskMetadata(root *html.Node, task *Task, metadata *exactTaskMetadata) {
	task.Badges = make([]string, 0)

	if task.Title == "" {
		if selector := findNode(root, func(e *html.Node) bool {
			return nodeHasAttr(e, "data-breadcrumb-selector")
		}); selector != nil {
			if button := findNode(selector, func(e *html.Node) bool {
				return nodeHasAttr(e, "data-breadcrumb-selector-button")
			}); button != nil {
				task.Title = strings.TrimSpace(NodeText(button))
			}
		}
	}
	if task.Title == "" {
		if input := taskDetailNamedControl(root, "input", "title"); input != nil {
			task.Title = strings.TrimSpace(attr(input, "value"))
		}
	}

	if task.Category == "" {
		if selected := taskDetailSelectedOption(root, "category"); selected != nil {
			task.Category = strings.TrimSpace(attr(selected, "value"))
			if task.Category == "" {
				task.Category = strings.TrimSpace(NodeText(selected))
			}
		}
	}
	if task.Category == "" {
		task.Category = taskDetailMetric(root, "Category:")
	}
	if task.Status == "" {
		if n := findNode(root, func(e *html.Node) bool { return attr(e, "data-task-status") != "" }); n != nil {
			task.Status = strings.TrimSpace(attr(n, "data-task-status"))
		}
	}

	if metadata.ParentTaskID != nil {
		task.Badges = append(task.Badges, "Chained")
	}
	if taskChainEnabled(metadata.ChainConfig) {
		task.Badges = append(task.Badges, "Chain")
	}
	if metadata.HasGoal || taskDetailHasGoal(root) {
		task.Badges = append(task.Badges, "Goal")
	}
	if metadata.SwarmRole == "parent" {
		task.Badges = append(task.Badges, "Swarm")
	}
	for _, badge := range []string{
		taskDetailMetric(root, "Model:"),
		taskDetailMetric(root, "Agent:"),
		taskDetailMetric(root, "Tag:"),
		taskDetailMetric(root, "Priority:"),
	} {
		badge = strings.TrimSpace(badge)
		if badge == "" || strings.EqualFold(badge, "none") || strings.EqualFold(badge, "no agent") {
			continue
		}
		task.Badges = append(task.Badges, badge)
	}
}

func taskDetailNamedControl(root *html.Node, element, name string) *html.Node {
	return findNode(root, func(e *html.Node) bool {
		return e.Data == element && attr(e, "name") == name
	})
}

func taskDetailSelectedOption(root *html.Node, name string) *html.Node {
	selectNode := taskDetailNamedControl(root, "select", name)
	if selectNode == nil {
		return nil
	}
	return findNode(selectNode, func(e *html.Node) bool {
		return e.Data == "option" && nodeHasAttr(e, "selected")
	})
}

func nodeHasAttr(node *html.Node, name string) bool {
	if node == nil {
		return false
	}
	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return true
		}
	}
	return false
}

func taskDetailHasGoal(root *html.Node) bool {
	panel := findByID(root, "task-goal-panel")
	return panel != nil && !strings.Contains(strings.ToLower(NodeText(panel)), "no goal set")
}

func taskChainEnabled(raw string) bool {
	var config struct {
		Enabled bool `json:"enabled"`
	}
	return json.Unmarshal([]byte(raw), &config) == nil && config.Enabled
}

func taskDetailMetric(root *html.Node, label string) string {
	labelNode := findNode(root, func(e *html.Node) bool {
		return e.Data == "span" && strings.TrimSpace(NodeText(e)) == label
	})
	if labelNode == nil || labelNode.Parent == nil {
		return ""
	}
	if value := findNode(labelNode.Parent, func(e *html.Node) bool {
		return e != labelNode && e.Data == "span" && strings.Contains(" "+attr(e, "class")+" ", " badge ")
	}); value != nil {
		return strings.TrimSpace(NodeText(value))
	}
	return ""
}

func taskDetailTaskID(root *html.Node) string {
	if root == nil {
		return ""
	}
	if n := findNode(root, func(e *html.Node) bool {
		return strings.TrimSpace(attr(e, "data-task-id")) != "" && strings.TrimSpace(attr(e, "data-project-id")) != ""
	}); n != nil {
		return strings.TrimSpace(attr(n, "data-task-id"))
	}
	if n := findNode(root, func(e *html.Node) bool { return strings.TrimSpace(attr(e, "data-task-id")) != "" }); n != nil {
		return strings.TrimSpace(attr(n, "data-task-id"))
	}
	return ""
}

// ListTaskReviews fetches inline review comments for a task.
func (c *Client) ListTaskReviews(ctx context.Context, taskID string) ([]ReviewComment, error) {
	return c.listTaskReviews(ctx, taskID, "")
}

// ListTaskReviewsForProject fetches review comments within the selected project.
func (c *Client) ListTaskReviewsForProject(ctx context.Context, taskID, projectID string) ([]ReviewComment, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("project ID is required for task reviews")
	}
	return c.listTaskReviews(ctx, taskID, projectID)
}

func (c *Client) listTaskReviews(ctx context.Context, taskID, projectID string) ([]ReviewComment, error) {
	root, err := c.getHTML(ctx, "/tasks/"+url.PathEscape(taskID)+"/reviews"+query("project_id", projectID))
	if err != nil {
		return nil, err
	}
	return parseReviewComments(root, taskID), nil
}

// AddTaskReviewComment creates an inline review comment and returns the updated list.
func (c *Client) AddTaskReviewComment(ctx context.Context, taskID string, form ReviewCommentForm) ([]ReviewComment, error) {
	root, err := c.doFormHTML(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/reviews", form.values())
	if err != nil {
		return nil, err
	}
	return parseReviewComments(root, taskID), nil
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

// GetTaskThread fetches only the task conversation fragment used by the web
// task view. Interactive controls in that fragment are intentionally excluded
// from terminal output.
func (c *Client) GetTaskThread(ctx context.Context, taskID, projectID string) (string, error) {
	if strings.TrimSpace(projectID) == "" {
		return "", fmt.Errorf("project ID is required for task thread")
	}
	root, err := c.getHTML(ctx, "/tasks/"+url.PathEscape(taskID)+"/thread"+query("project_id", projectID))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(nodeConversationText(root)), nil
}

func nodeConversationText(root *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "form", "button":
				return
			}
			if skippedTags[n.Data] {
				return
			}
			if blockTags[n.Data] {
				b.WriteByte('\n')
			}
		}
		if n.Type == html.TextNode {
			if text := strings.Join(strings.Fields(n.Data), " "); text != "" {
				b.WriteString(text)
				b.WriteByte(' ')
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if n.Type == html.ElementNode && blockTags[n.Data] {
			b.WriteByte('\n')
		}
	}
	walk(root)
	return tidyText(b.String())
}

// TaskFollowupAccepted identifies the execution started by a thread message or
// the queued input that will later be promoted to an execution. Both IDs are
// empty when the backend accepted a swarm-parent orchestration follow-up that
// does not map the submission to one directly streamable execution.
type TaskFollowupAccepted struct {
	ExecID         string `json:"exec_id,omitempty"`
	PendingInputID string `json:"pending_input_id,omitempty"`
	Queued         bool   `json:"queued"`
}

// SendTaskThreadMessageForProject posts a follow-up and retains the backend's
// structured HTML identity so headless callers can attach to its output stream.
func (c *Client) SendTaskThreadMessageForProject(ctx context.Context, taskID, projectID, message string) (*TaskFollowupAccepted, error) {
	v := url.Values{}
	v.Set("message", message)
	doc, err := c.doFormHTML(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/thread"+query("project_id", projectID), v)
	if err != nil {
		return nil, err
	}
	accepted := &TaskFollowupAccepted{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if accepted.ExecID == "" {
			if execID := attr(n, "data-exec-id"); execID != "" && attr(n, "data-execution-pair") == "true" {
				accepted.ExecID = execID
			}
		}
		if accepted.PendingInputID == "" {
			if inputID := attr(n, "data-thread-input-id"); inputID != "" && attr(n, "data-input-mode") == "queued" {
				accepted.PendingInputID = inputID
				accepted.Queued = true
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return accepted, nil
}

// SendTaskThreadMessage posts a follow-up message into a task's thread.
func (c *Client) SendTaskThreadMessage(ctx context.Context, taskID, message string) error {
	_, err := c.SendTaskThreadMessageForProject(ctx, taskID, "", message)
	return err
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
