package client

// Task board (kanban) access.
//
// The backend renders /tasks as an HTMX kanban board, so the board is scraped
// from the rendered task cards (each carries data-task-id / data-task-status /
// data-task-category / data-display-order). Mutations reuse the same routes the
// web UI posts to.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/charmbracelet/x/ansi"
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
	Priority     int      `json:"priority,omitempty"`
	DisplayOrder int      `json:"display_order"`
	Badges       []string `json:"badges"` // model, agent, tag, priority, Goal, Chain, Swarm...
	// Attachments is a complete pre-upload snapshot when supplied by the task
	// reference endpoint. Nil means the endpoint did not provide one.
	Attachments []Attachment `json:"attachments,omitempty"`
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

// SwarmTaskForm carries create fields for an autonomous swarm parent task.
type SwarmTaskForm struct {
	Title                   string
	Prompt                  string
	Goal                    string
	Category                string // backlog | active
	Priority                int    // 1-4
	AgentID                 string
	AgentDefinitionID       string
	Tag                     string // "", feature, bug
	MaxWorkers              int
	WorkerIsolation         string // worktree | read_only | shared
	ReviewerEnabled         bool
	MergerEnabled           bool
	AutoMerge               bool
	AutoMergeOnGoalAchieved bool
	MergeTargetBranch       string
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
	v := f.updateValues()
	v.Set("priority", strconv.Itoa(f.Priority))
	v.Set("tag", f.Tag)
	return v
}

func (f TaskForm) updateValues() url.Values {
	v := url.Values{}
	v.Set("title", f.Title)
	v.Set("prompt", f.Prompt)
	if f.Category != "" {
		v.Set("category", f.Category)
	}
	return v
}

func (f SwarmTaskForm) values() url.Values {
	v := url.Values{}
	v.Set("swarm_mode", "true")
	v.Set("title", f.Title)
	v.Set("prompt", f.Prompt)
	v.Set("goal", f.Goal)
	if f.Category != "" {
		v.Set("category", f.Category)
	}
	if f.Priority != 0 {
		v.Set("priority", strconv.Itoa(f.Priority))
	}
	if f.AgentID != "" {
		v.Set("agent_id", f.AgentID)
	}
	if f.AgentDefinitionID != "" {
		v.Set("agent_definition_id", f.AgentDefinitionID)
	}
	v.Set("tag", f.Tag)
	if f.MaxWorkers != 0 {
		v.Set("swarm_max_workers", strconv.Itoa(f.MaxWorkers))
	}
	if f.WorkerIsolation != "" {
		v.Set("swarm_worker_isolation", f.WorkerIsolation)
	}
	v.Set("swarm_reviewer_enabled", strconv.FormatBool(f.ReviewerEnabled))
	v.Set("swarm_merger_enabled", strconv.FormatBool(f.MergerEnabled))
	if f.AutoMerge {
		v.Set("auto_merge", "true")
	}
	if f.AutoMergeOnGoalAchieved {
		v.Set("auto_merge_on_goal_achieved", "true")
	}
	if f.MergeTargetBranch != "" {
		v.Set("merge_target_branch", f.MergeTargetBranch)
	}
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

// ListTaskReferences fetches the complete compact task catalog for one project.
// The reference-catalog route is a JSON-only projection of Task: in addition to
// selector metadata, each task includes its complete attachment snapshot for
// upload duplicate detection. It does not render the full kanban board or controls.
//
// The endpoint accepts either the documented {"tasks": [...]} envelope or a
// bare task array so clients remain compatible with servers that expose the
// same projection as a direct JSON list.
func (c *Client) ListTaskReferences(ctx context.Context, projectID string) ([]Task, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("project ID is required for task references")
	}

	var raw json.RawMessage
	const path = "/api/tasks/reference-catalog"
	if err := c.getJSON(ctx, path+query("project_id", projectID), &raw); err != nil {
		return nil, err
	}

	tasks, err := decodeTaskReferenceCatalog(raw)
	if err != nil {
		return nil, err
	}
	for _, task := range tasks {
		if strings.TrimSpace(task.ID) == "" || strings.TrimSpace(task.ProjectID) == "" || task.ProjectID != projectID {
			return nil, fmt.Errorf("invalid task reference catalog response")
		}
	}
	if tasks == nil {
		tasks = make([]Task, 0)
	}
	return tasks, nil
}

func decodeTaskReferenceCatalog(raw json.RawMessage) ([]Task, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("decoding task reference catalog: empty response")
	}
	if string(trimmed) == "null" {
		return make([]Task, 0), nil
	}
	if trimmed[0] == '[' {
		return decodeTaskReferenceEntries(trimmed)
	}
	if trimmed[0] != '{' {
		return nil, fmt.Errorf("decoding task reference catalog: expected JSON array or object")
	}
	var envelope struct {
		Tasks json.RawMessage `json:"tasks"`
	}
	if err := json.Unmarshal(trimmed, &envelope); err != nil {
		return nil, fmt.Errorf("decoding task reference catalog: %w", err)
	}
	if len(envelope.Tasks) == 0 {
		return nil, fmt.Errorf("decoding task reference catalog: missing tasks")
	}
	if string(bytes.TrimSpace(envelope.Tasks)) == "null" {
		return make([]Task, 0), nil
	}
	return decodeTaskReferenceEntries(envelope.Tasks)
}

type taskReferenceCatalogEntry struct {
	ID           string          `json:"id"`
	ProjectID    string          `json:"project_id"`
	Title        string          `json:"title"`
	Prompt       string          `json:"prompt"`
	Category     string          `json:"category"`
	Status       string          `json:"status"`
	Priority     int             `json:"priority,omitempty"`
	DisplayOrder int             `json:"display_order"`
	Badges       []string        `json:"badges"`
	Attachments  json.RawMessage `json:"attachments"`
}

func decodeTaskReferenceEntries(raw json.RawMessage) ([]Task, error) {
	var entries []taskReferenceCatalogEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("decoding task reference catalog: %w", err)
	}
	tasks := make([]Task, 0, len(entries))
	for _, entry := range entries {
		task := Task{
			ID: entry.ID, ProjectID: entry.ProjectID, Title: entry.Title, Prompt: entry.Prompt,
			Category: entry.Category, Status: entry.Status, Priority: entry.Priority,
			DisplayOrder: entry.DisplayOrder, Badges: entry.Badges,
		}
		var attachments []Attachment
		if len(entry.Attachments) > 0 && json.Unmarshal(entry.Attachments, &attachments) == nil && attachments != nil {
			// A malformed or absent snapshot is deliberately left nil so upload
			// callers use the authoritative pre-upload attachment read.
			task.Attachments = attachments
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
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
		populateExactTaskMetadata(root, &d.Task)
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

const taskBoardPromptPreviewCodePoints = 300

func populateExactTaskMetadata(root *html.Node, task *Task) {
	task.Badges = make([]string, 0)
	seenBadges := make(map[string]bool)
	appendBadge := func(badge string) {
		if badge == "" || seenBadges[badge] {
			return
		}
		seenBadges[badge] = true
		task.Badges = append(task.Badges, badge)
	}

	if selector := findNode(root, func(e *html.Node) bool {
		return nodeHasAttr(e, "data-breadcrumb-selector")
	}); selector != nil {
		if button := findNode(selector, func(e *html.Node) bool {
			return nodeHasAttr(e, "data-breadcrumb-selector-button")
		}); button != nil {
			task.Title = strings.TrimSpace(NodeText(button))
		}
	}
	if task.Title == "" {
		if input := taskDetailNamedControl(root, "input", "title"); input != nil {
			task.Title = strings.TrimSpace(attr(input, "value"))
		}
	}

	if selected := taskDetailSelectedOption(root, "category"); selected != nil {
		task.Category = strings.TrimSpace(attr(selected, "value"))
		if task.Category == "" {
			task.Category = strings.TrimSpace(NodeText(selected))
		}
	}
	if task.Category == "" {
		task.Category = taskDetailMetric(root, "Category:")
	}
	if n := findNode(root, func(e *html.Node) bool { return attr(e, "data-task-status") != "" }); n != nil {
		task.Status = strings.TrimSpace(attr(n, "data-task-status"))
	}
	if n := findNode(root, func(e *html.Node) bool { return attr(e, "data-display-order") != "" }); n != nil {
		task.DisplayOrder, _ = strconv.Atoi(strings.TrimSpace(attr(n, "data-display-order")))
	}

	if prompt := taskDetailNamedControl(root, "textarea", "prompt"); prompt != nil {
		task.Prompt = taskBoardPromptPreview(taskDetailControlText(prompt))
	} else if panel := findByID(root, "task-prompt-panel"); panel != nil {
		if value := findNode(panel, func(e *html.Node) bool {
			return strings.Contains(" "+attr(e, "class")+" ", " textarea ")
		}); value != nil {
			task.Prompt = taskBoardPromptPreview(NodeText(value))
		}
	}

	if taskDetailSwarmChild(root) {
		appendBadge("Chained")
	}
	if taskDetailCheckedControl(root, "chain_enabled") {
		appendBadge("Chain")
	}
	if taskDetailHasGoal(root) {
		appendBadge("Goal")
	}
	if taskDetailHasHeading(root, "Swarm Overview") {
		appendBadge("Swarm")
	}
	for _, badge := range []string{
		taskDetailModelBadge(root),
		taskDetailSelectedLabel(root, "agent_definition_id", "No Agent"),
		taskDetailSelectedLabel(root, "tag", "None"),
		taskDetailSelectedLabel(root, "priority", ""),
	} {
		appendBadge(badge)
	}
}

func taskBoardPromptPreview(value string) string {
	runes := []rune(value)
	if len(runes) > taskBoardPromptPreviewCodePoints {
		runes = runes[:taskBoardPromptPreviewCodePoints]
	}
	return strings.Join(strings.Fields(string(runes)), " ")
}

func taskDetailHasHeading(root *html.Node, text string) bool {
	return findNode(root, func(e *html.Node) bool {
		return e.Data == "h3" && strings.TrimSpace(NodeText(e)) == text
	}) != nil
}

func taskDetailSwarmChild(root *html.Node) bool {
	heading := findNode(root, func(e *html.Node) bool {
		return e.Data == "h3" && strings.TrimSpace(NodeText(e)) == "Swarm Context"
	})
	if heading == nil || heading.Parent == nil || heading.Parent.Parent == nil {
		return false
	}
	return strings.Contains(NodeText(heading.Parent.Parent), "Part of swarm:")
}

func taskDetailCheckedControl(root *html.Node, name string) bool {
	control := findNode(root, func(e *html.Node) bool {
		return (e.Data == "input" || e.Data == "option") && attr(e, "name") == name
	})
	return control != nil && nodeHasAttr(control, "checked")
}

func taskDetailSelectedLabel(root *html.Node, name, emptyLabel string) string {
	selected := taskDetailSelectedOption(root, name)
	if selected == nil || strings.TrimSpace(attr(selected, "value")) == "" {
		return ""
	}
	label := strings.TrimSpace(taskDetailControlText(selected))
	if strings.EqualFold(label, emptyLabel) {
		return ""
	}
	return label
}

func taskDetailModelBadge(root *html.Node) string {
	selectNode := taskDetailNamedControl(root, "select", "agent_id")
	if selectNode == nil {
		return ""
	}
	options := findAll(selectNode, func(e *html.Node) bool { return e.Data == "option" })
	configured := 0
	var selected *html.Node
	for _, option := range options {
		if strings.TrimSpace(attr(option, "value")) != "" {
			configured++
		}
		if nodeHasAttr(option, "selected") {
			selected = option
		}
	}
	if configured == 0 {
		return ""
	}
	if selected != nil && strings.TrimSpace(attr(selected, "value")) != "" {
		return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(taskDetailControlText(selected)), "(Default)"))
	}
	for _, option := range options {
		label := strings.TrimSpace(taskDetailControlText(option))
		if strings.TrimSpace(attr(option, "value")) != "" && strings.HasSuffix(label, "(Default)") {
			return strings.TrimSpace(strings.TrimSuffix(label, "(Default)"))
		}
	}
	return "No Model"
}

func taskDetailControlText(node *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			b.WriteString(current.Data)
			return
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return b.String()
}

func taskDetailNamedControl(root *html.Node, element, name string) *html.Node {
	return findNamedHTMLControl(root, name, element)
}

func taskDetailSelectedOption(root *html.Node, name string) *html.Node {
	return selectedHTMLFormOption(taskDetailNamedControl(root, "select", name), htmlSelectSelectedOption)
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
	return c.addTaskReviewComment(ctx, taskID, "", form)
}

// AddTaskReviewCommentForProject creates an inline review comment within the selected project.
func (c *Client) AddTaskReviewCommentForProject(ctx context.Context, taskID, projectID string, form ReviewCommentForm) ([]ReviewComment, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("project ID is required for task reviews")
	}
	return c.addTaskReviewComment(ctx, taskID, projectID, form)
}

func (c *Client) addTaskReviewComment(ctx context.Context, taskID, projectID string, form ReviewCommentForm) ([]ReviewComment, error) {
	root, err := c.doFormHTML(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/reviews"+query("project_id", projectID), form.values())
	if err != nil {
		return nil, err
	}
	return parseReviewComments(root, taskID), nil
}

// CreateTask creates a task. Category "active" submits it immediately.
func (c *Client) CreateTask(ctx context.Context, projectID string, form TaskForm) error {
	return c.doForm(ctx, http.MethodPost, "/tasks"+query("project_id", projectID), form.values())
}

// CreateSwarmTask creates an autonomous swarm parent task and returns the parent
// parsed from the refreshed board fragment when the backend includes it.
func (c *Client) CreateSwarmTask(ctx context.Context, projectID string, form SwarmTaskForm) (Task, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return Task{}, fmt.Errorf("project ID is required for swarm task creation")
	}
	root, err := c.doFormHTML(ctx, http.MethodPost, "/tasks"+query("project_id", projectID), form.values())
	if err != nil {
		return Task{}, err
	}
	tasks := parseTaskCards(root, projectID)
	for _, task := range tasks {
		if strings.EqualFold(strings.TrimSpace(task.Title), strings.TrimSpace(form.Title)) {
			return task, nil
		}
	}
	if len(tasks) == 1 {
		return tasks[0], nil
	}
	return Task{ProjectID: projectID, Title: form.Title, Prompt: form.Prompt, Category: form.Category}, nil
}

// UpdateTask updates a task's editable fields.
func (c *Client) UpdateTask(ctx context.Context, taskID string, form TaskForm) error {
	return c.doForm(ctx, http.MethodPut, "/tasks/"+url.PathEscape(taskID), form.updateValues())
}

// UpdateTaskForProject updates a task's editable fields in the selected project.
func (c *Client) UpdateTaskForProject(ctx context.Context, taskID, projectID string, form TaskForm) error {
	if err := requireTaskMutationProjectID(projectID); err != nil {
		return err
	}
	return c.doForm(ctx, http.MethodPut, "/tasks/"+url.PathEscape(taskID)+query("project_id", projectID), form.updateValues())
}

// DeleteTask removes a task.
func (c *Client) DeleteTask(ctx context.Context, taskID string) error {
	return c.doForm(ctx, http.MethodDelete, "/tasks/"+url.PathEscape(taskID), nil)
}

// DeleteTaskForProject removes a task from the selected project.
func (c *Client) DeleteTaskForProject(ctx context.Context, taskID, projectID string) error {
	if err := requireTaskMutationProjectID(projectID); err != nil {
		return err
	}
	return c.doForm(ctx, http.MethodDelete, "/tasks/"+url.PathEscape(taskID)+query("project_id", projectID), nil)
}

// RunTask (re-)submits a task for execution.
func (c *Client) RunTask(ctx context.Context, taskID string) error {
	return c.doForm(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/run", nil)
}

// RunTaskForProject (re-)submits a task for execution in the selected project.
func (c *Client) RunTaskForProject(ctx context.Context, taskID, projectID string) error {
	if err := requireTaskMutationProjectID(projectID); err != nil {
		return err
	}
	return c.doForm(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/run"+query("project_id", projectID), nil)
}

// CancelTask stops a queued/running task.
func (c *Client) CancelTask(ctx context.Context, taskID string) error {
	return c.doForm(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/cancel", nil)
}

// CancelTaskForProject stops a queued/running task in the selected project.
func (c *Client) CancelTaskForProject(ctx context.Context, taskID, projectID string) error {
	if err := requireTaskMutationProjectID(projectID); err != nil {
		return err
	}
	return c.doForm(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/cancel"+query("project_id", projectID), nil)
}

// MoveTask moves a task between kanban columns (drag & drop equivalent).
func (c *Client) MoveTask(ctx context.Context, taskID, category string) error {
	v := url.Values{}
	v.Set("category", category)
	return c.doForm(ctx, http.MethodPatch, "/tasks/"+url.PathEscape(taskID)+"/category", v)
}

// MoveTaskForProject moves a task between kanban columns in the selected project.
func (c *Client) MoveTaskForProject(ctx context.Context, taskID, projectID, category string) error {
	if err := requireTaskMutationProjectID(projectID); err != nil {
		return err
	}
	v := url.Values{}
	v.Set("category", category)
	return c.doForm(ctx, http.MethodPatch, "/tasks/"+url.PathEscape(taskID)+"/category"+query("project_id", projectID), v)
}

// ReorderTask moves a task to a new position within its column.
func (c *Client) ReorderTask(ctx context.Context, taskID string, position int) error {
	v := url.Values{}
	v.Set("position", strconv.Itoa(position))
	return c.doForm(ctx, http.MethodPatch, "/tasks/"+url.PathEscape(taskID)+"/reorder", v)
}

// ReorderTaskForProject moves a task to a new position within its column in the selected project.
func (c *Client) ReorderTaskForProject(ctx context.Context, taskID, projectID string, position int) error {
	if err := requireTaskMutationProjectID(projectID); err != nil {
		return err
	}
	v := url.Values{}
	v.Set("position", strconv.Itoa(position))
	return c.doForm(ctx, http.MethodPatch, "/tasks/"+url.PathEscape(taskID)+"/reorder"+query("project_id", projectID), v)
}

func requireTaskMutationProjectID(projectID string) error {
	if strings.TrimSpace(projectID) == "" {
		return fmt.Errorf("project ID is required for task mutations")
	}
	return nil
}

// TaskThreadState is the project-scoped task-thread snapshot needed for guarded
// active-response controls. ActiveTurnID is populated only when the thread
// contains exactly one running execution; callers must not infer a turn from a
// task status or from an arbitrary execution in the thread.
type TaskThreadState struct {
	Body         string
	ActiveTurnID string
}

// TaskThreadPendingInput is the safe terminal projection of one pending task
// thread input. It intentionally contains no form controls, routes, or raw HTML.
type TaskThreadPendingInput struct {
	ID             string `json:"id"`
	TaskID         string `json:"task_id"`
	ProjectID      string `json:"project_id"`
	InputMode      string `json:"input_mode"`
	InputStatus    string `json:"input_status"`
	Preview        string `json:"preview"`
	HasAttachments bool   `json:"has_attachments"`
}

// TaskThreadInputMutation is the canonical identity and resulting state of a
// pending-input mutation.
type TaskThreadInputMutation struct {
	ID          string `json:"id"`
	TaskID      string `json:"task_id"`
	ProjectID   string `json:"project_id"`
	InputMode   string `json:"input_mode"`
	InputStatus string `json:"input_status"`
}

const taskThreadPendingInputPreviewLimit = 240

const taskThreadInputMutationStatusHeader = "X-OpenVibely-Thread-Input-Status"

// GetTaskThreadPendingInputsForProject fetches the task-scoped pending-input
// fragment and parses only its stable semantic row attributes. The selected
// project is sent even though older backends may derive task ownership solely
// from the task path.
func (c *Client) GetTaskThreadPendingInputsForProject(ctx context.Context, taskID, projectID string) ([]TaskThreadPendingInput, error) {
	taskID = strings.TrimSpace(taskID)
	projectID = strings.TrimSpace(projectID)
	if taskID == "" {
		return nil, fmt.Errorf("task ID is required for pending task-thread inputs")
	}
	if projectID == "" {
		return nil, fmt.Errorf("project ID is required for pending task-thread inputs")
	}
	root, err := c.getHTML(ctx, "/tasks/"+url.PathEscape(taskID)+"/thread/pending-inputs"+query("project_id", projectID))
	if err != nil {
		return nil, err
	}
	return parseTaskThreadPendingInputs(root, taskID, projectID)
}

// ListTaskThreadPendingInputsForProject is a descriptive alias used by callers
// that prefer list terminology for read-only collection methods.
func (c *Client) ListTaskThreadPendingInputsForProject(ctx context.Context, taskID, projectID string) ([]TaskThreadPendingInput, error) {
	return c.GetTaskThreadPendingInputsForProject(ctx, taskID, projectID)
}

func parseTaskThreadPendingInputs(root *html.Node, taskID, projectID string) ([]TaskThreadPendingInput, error) {
	if root == nil {
		return make([]TaskThreadPendingInput, 0), nil
	}
	containers := findAll(root, func(n *html.Node) bool {
		return attr(n, "id") == "pending-thread-inputs"
	})
	if len(containers) == 0 {
		return nil, fmt.Errorf("pending task-thread inputs are missing task identity")
	}
	for _, container := range containers {
		scopedTask := strings.TrimSpace(attr(container, "data-task-id"))
		if scopedTask == "" {
			return nil, fmt.Errorf("pending task-thread inputs are missing task identity")
		}
		if scopedTask != taskID {
			return nil, fmt.Errorf("pending task-thread inputs belong to task %q, not requested task %q", scopedTask, taskID)
		}
		if scopedProject := strings.TrimSpace(attr(container, "data-project-id")); scopedProject != "" && scopedProject != projectID {
			return nil, fmt.Errorf("pending task-thread inputs belong to project %q, not selected project", scopedProject)
		}
	}

	rows := findAll(root, func(n *html.Node) bool {
		return strings.TrimSpace(attr(n, "data-thread-input-id")) != ""
	})
	inputs := make([]TaskThreadPendingInput, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		id := strings.TrimSpace(attr(row, "data-thread-input-id"))
		rowTaskID := strings.TrimSpace(attr(row, "data-task-id"))
		if rowTaskID == "" || rowTaskID != taskID {
			return nil, fmt.Errorf("pending input %q is not owned by task %q", id, taskID)
		}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("pending input %q appears more than once", id)
		}
		seen[id] = struct{}{}
		mode := strings.ToLower(strings.TrimSpace(attr(row, "data-input-mode")))
		if mode != "queued" && mode != "steering" {
			return nil, fmt.Errorf("pending input %q has unsupported mode", id)
		}
		inputs = append(inputs, TaskThreadPendingInput{
			ID:             id,
			TaskID:         taskID,
			ProjectID:      projectID,
			InputMode:      mode,
			InputStatus:    "pending",
			Preview:        taskThreadPendingInputPreview(row),
			HasAttachments: taskThreadPendingInputHasAttachments(row),
		})
	}
	return inputs, nil
}

func taskThreadPendingInputPreview(row *html.Node) string {
	// Both current queued and steering templates expose the user content in a
	// descendant with the truncate class. This avoids labels, attachment badges,
	// and button text without depending on visual row layout.
	content := findNode(row, func(n *html.Node) bool {
		return hasClassToken(attr(n, "class"), "truncate")
	})
	preview := ""
	if content != nil {
		preview = nodeConversationText(content)
	}
	if preview == "" {
		text := nodeConversationText(row)
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.EqualFold(line, "steering pending") || strings.HasPrefix(strings.ToLower(line), "will be applied") {
				continue
			}
			preview = line
			break
		}
	}
	preview = ansi.Strip(preview)
	preview = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return -1
		}
		return r
	}, preview)
	preview = strings.Join(strings.Fields(preview), " ")
	runes := []rune(preview)
	if len(runes) > taskThreadPendingInputPreviewLimit {
		return string(runes[:taskThreadPendingInputPreviewLimit-1]) + "…"
	}
	return preview
}

func taskThreadPendingInputHasAttachments(row *html.Node) bool {
	return findNode(row, func(n *html.Node) bool {
		label := strings.ToLower(strings.TrimSpace(attr(n, "aria-label")))
		title := strings.ToLower(strings.TrimSpace(attr(n, "title")))
		return strings.Contains(label, "attachment") || strings.Contains(title, "attachment")
	}) != nil
}

func hasClassToken(classes, want string) bool {
	for _, className := range strings.Fields(classes) {
		if className == want {
			return true
		}
	}
	return false
}

func (c *Client) pendingTaskThreadInput(ctx context.Context, taskID, projectID, inputID string) (TaskThreadPendingInput, error) {
	var zero TaskThreadPendingInput
	inputID = strings.TrimSpace(inputID)
	if inputID == "" {
		return zero, fmt.Errorf("pending input ID is required")
	}
	inputs, err := c.GetTaskThreadPendingInputsForProject(ctx, taskID, projectID)
	if err != nil {
		return zero, err
	}
	var found *TaskThreadPendingInput
	for i := range inputs {
		if inputs[i].ID != inputID {
			continue
		}
		if found != nil {
			return zero, fmt.Errorf("pending input %q is ambiguous", inputID)
		}
		candidate := inputs[i]
		found = &candidate
	}
	if found == nil {
		return zero, fmt.Errorf("pending input %q is missing, stale, or already applied", inputID)
	}
	if found.TaskID != strings.TrimSpace(taskID) || found.ProjectID != strings.TrimSpace(projectID) {
		return zero, fmt.Errorf("pending input %q does not belong to the selected task and project", inputID)
	}
	return *found, nil
}

// CancelTaskThreadInputForProject cancels one currently pending queued or
// steering input after re-reading and validating its task/project relationship.
// It never calls the task cancellation route, so the active execution is not
// cancelled.
func (c *Client) CancelTaskThreadInputForProject(ctx context.Context, taskID, projectID, inputID string) (*TaskThreadInputMutation, error) {
	input, err := c.pendingTaskThreadInput(ctx, taskID, projectID, inputID)
	if err != nil {
		return nil, err
	}
	if input.InputStatus != "pending" || (input.InputMode != "queued" && input.InputMode != "steering") {
		return nil, fmt.Errorf("pending input %q is no longer cancellable", input.ID)
	}
	resp, err := c.doFormResponse(ctx, http.MethodPost, "/thread-inputs/"+url.PathEscape(input.ID)+"/cancel", nil)
	if err != nil {
		return nil, err
	}
	outcome := strings.ToLower(strings.TrimSpace(resp.Header.Get(taskThreadInputMutationStatusHeader)))
	drainAndClose(resp.Body)
	switch outcome {
	case "cancelled":
		return &TaskThreadInputMutation{
			ID:          input.ID,
			TaskID:      input.TaskID,
			ProjectID:   input.ProjectID,
			InputMode:   input.InputMode,
			InputStatus: "cancelled",
		}, nil
	case "not_pending":
		return nil, fmt.Errorf("pending input %q is no longer pending; cancellation was not applied", input.ID)
	default:
		return nil, fmt.Errorf("cancellation response for pending input %q did not report a mutation outcome", input.ID)
	}
}

// SteerTaskThreadQueuedInputForProject converts one queued follow-up to
// steering. The active turn is read immediately before the mutation; the
// backend repeats that identity check atomically and rejects stale races.
func (c *Client) SteerTaskThreadQueuedInputForProject(ctx context.Context, taskID, projectID, inputID string) (*TaskThreadSteerAccepted, error) {
	input, err := c.pendingTaskThreadInput(ctx, taskID, projectID, inputID)
	if err != nil {
		return nil, err
	}
	if input.InputMode != "queued" || input.InputStatus != "pending" {
		return nil, fmt.Errorf("pending input %q is not a queued follow-up", input.ID)
	}
	state, err := c.GetTaskThreadStateForProject(ctx, taskID, projectID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(state.ActiveTurnID) == "" {
		return nil, fmt.Errorf("no active response for queued input %q; it remains queued", input.ID)
	}
	doc, err := c.doFormHTML(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/thread/queued/"+url.PathEscape(input.ID)+"/steer"+query("project_id", projectID), nil)
	if err != nil {
		return nil, err
	}
	var steeringRows int
	for _, node := range findAll(doc, func(n *html.Node) bool {
		return attr(n, "data-input-mode") == "steering" && attr(n, "data-thread-input-id") != ""
	}) {
		steeringRows++
		rowID := strings.TrimSpace(attr(node, "data-thread-input-id"))
		if rowID != input.ID {
			return nil, fmt.Errorf("queued input %q steering response returned foreign input %q", input.ID, rowID)
		}
		rowTask := strings.TrimSpace(attr(node, "data-task-id"))
		if rowTask == "" {
			return nil, fmt.Errorf("queued input %q steering response is missing task identity", input.ID)
		}
		if rowTask != taskID {
			return nil, fmt.Errorf("queued input %q steering response belongs to another task", input.ID)
		}
	}
	if steeringRows != 1 {
		return nil, fmt.Errorf("queued input %q steering response did not confirm exactly one pending steering row", input.ID)
	}
	return &TaskThreadSteerAccepted{PendingInputID: input.ID}, nil
}

// TaskThreadSteerAccepted identifies the pending steering input returned by the
// backend. The ID is empty only when the backend accepted the mutation without
// rendering an input row.
type TaskThreadSteerAccepted struct {
	PendingInputID string `json:"pending_input_id,omitempty"`
}

func (c *Client) getTaskThreadDocument(ctx context.Context, taskID, projectID string) (*html.Node, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("project ID is required for task thread")
	}
	return c.getHTML(ctx, "/tasks/"+url.PathEscape(taskID)+"/thread"+query("project_id", projectID))
}

// GetTaskThreadStateForProject fetches the task conversation and the exact
// active execution identity used to guard steering mutations.
func (c *Client) GetTaskThreadStateForProject(ctx context.Context, taskID, projectID string) (*TaskThreadState, error) {
	root, err := c.getTaskThreadDocument(ctx, taskID, projectID)
	if err != nil {
		return nil, err
	}
	activeNodes := findAll(root, func(n *html.Node) bool {
		return attr(n, "data-execution-pair") == "true" &&
			strings.EqualFold(strings.TrimSpace(attr(n, "data-exec-status")), "running")
	})
	turnIDs := make([]string, 0, len(activeNodes))
	seen := make(map[string]struct{}, len(activeNodes))
	for _, node := range activeNodes {
		id := strings.TrimSpace(attr(node, "data-exec-id"))
		if id == "" {
			return nil, fmt.Errorf("task thread has an active response without a turn ID; refusing to guess which turn to steer")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		turnIDs = append(turnIDs, id)
	}
	if len(turnIDs) > 1 {
		return nil, fmt.Errorf("task thread has multiple active responses; refusing to guess which turn to steer")
	}
	state := &TaskThreadState{Body: strings.TrimSpace(nodeConversationText(root))}
	if len(turnIDs) == 1 {
		state.ActiveTurnID = turnIDs[0]
	}
	return state, nil
}

// SteerTaskThreadForProject queues a steering instruction for the active task
// execution. expectedTurnID must come from GetTaskThreadStateForProject; this
// method never discovers or substitutes a turn on behalf of the caller.
func (c *Client) SteerTaskThreadForProject(ctx context.Context, taskID, projectID, message, expectedTurnID string) (*TaskThreadSteerAccepted, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("project ID is required for task thread steering")
	}
	if strings.TrimSpace(expectedTurnID) == "" {
		return nil, fmt.Errorf("expected turn ID is required for task thread steering")
	}
	form := url.Values{}
	form.Set("message", message)
	form.Set("expected_turn_id", expectedTurnID)
	doc, err := c.doFormHTML(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/thread/steer"+query("project_id", projectID), form)
	if err != nil {
		return nil, err
	}
	accepted := &TaskThreadSteerAccepted{}
	for _, node := range findAll(doc, func(n *html.Node) bool {
		return attr(n, "data-input-mode") == "steering" && attr(n, "data-thread-input-id") != ""
	}) {
		if task := strings.TrimSpace(attr(node, "data-task-id")); task != "" && task != taskID {
			continue
		}
		accepted.PendingInputID = strings.TrimSpace(attr(node, "data-thread-input-id"))
		break
	}
	return accepted, nil
}

// GetTaskThread fetches only the task conversation fragment used by the web
// task view. Interactive controls in that fragment are intentionally excluded
// from terminal output.
func (c *Client) GetTaskThread(ctx context.Context, taskID, projectID string) (string, error) {
	root, err := c.getTaskThreadDocument(ctx, taskID, projectID)
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
	return c.SetTaskGoalForProject(ctx, taskID, "", objective)
}

// SetTaskGoalForProject sets a task's completion goal in the selected project.
func (c *Client) SetTaskGoalForProject(ctx context.Context, taskID, projectID, objective string) error {
	v := url.Values{}
	v.Set("objective", objective)
	v.Set("goal", objective)
	return c.doForm(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/goal"+query("project_id", projectID), v)
}

// ClearTaskGoal removes a task's goal.
func (c *Client) ClearTaskGoal(ctx context.Context, taskID string) error {
	return c.ClearTaskGoalForProject(ctx, taskID, "")
}

// ClearTaskGoalForProject removes a task's goal from the selected project.
func (c *Client) ClearTaskGoalForProject(ctx context.Context, taskID, projectID string) error {
	return c.doForm(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/goal/clear"+query("project_id", projectID), nil)
}

// PauseTaskGoal pauses a task's completion goal.
func (c *Client) PauseTaskGoal(ctx context.Context, taskID string) error {
	return c.PauseTaskGoalForProject(ctx, taskID, "")
}

// PauseTaskGoalForProject pauses a task's completion goal in the selected project.
func (c *Client) PauseTaskGoalForProject(ctx context.Context, taskID, projectID string) error {
	return c.doForm(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/goal/pause"+query("project_id", projectID), nil)
}

// ResumeTaskGoal resumes a task's completion goal.
func (c *Client) ResumeTaskGoal(ctx context.Context, taskID string) error {
	return c.ResumeTaskGoalForProject(ctx, taskID, "")
}

// ResumeTaskGoalForProject resumes a task's completion goal in the selected project.
func (c *Client) ResumeTaskGoalForProject(ctx context.Context, taskID, projectID string) error {
	return c.doForm(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/goal/resume"+query("project_id", projectID), nil)
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
