package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// TaskPullRequestInfo describes a pull request recorded for a task. The HTML
// task Changes fragment exposes the persisted PR link; its presence confirms
// that the backend has a recorded PR, but does not claim a live GitHub state.
type TaskPullRequestInfo struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"` // recorded
}

// TaskPullRequestResult reports a task pull request created or reused by the
// backend-owned worktree endpoint.
type TaskPullRequestResult struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Status string `json:"status"` // created | reused
}

// TaskWorktreeInfo is the project-scoped branch integration snapshot returned
// by the backend's task worktree panel, with any recorded pull request link.
type TaskWorktreeInfo struct {
	TaskID            string               `json:"task_id"`
	ProjectID         string               `json:"project_id"`
	Branch            string               `json:"branch,omitempty"`
	TargetBranch      string               `json:"target_branch,omitempty"`
	IntegrationStatus string               `json:"integration_status"`
	PullRequest       *TaskPullRequestInfo `json:"pull_request,omitempty"`
}

// TaskWorktreeActionResult separates the backend's merge or rebase outcome from
// the follow-up refresh status and any branch state read after the action.
type TaskWorktreeActionResult struct {
	Status        string            `json:"status"` // operation status: succeeded | ineligible | conflict | failed
	Action        string            `json:"action"`
	Strategy      string            `json:"strategy,omitempty"`
	Message       string            `json:"message,omitempty"`
	RefreshStatus string            `json:"refresh_status"` // succeeded | failed
	RefreshError  string            `json:"refresh_error,omitempty"`
	Worktree      *TaskWorktreeInfo `json:"worktree,omitempty"`
}

// TaskWorktreeActionError carries mutation and refresh failures separately while
// preserving any branch snapshot available to callers.
type TaskWorktreeActionError struct {
	Result     TaskWorktreeActionResult
	Cause      error
	RefreshErr error
}

func (e *TaskWorktreeActionError) Error() string {
	if e == nil {
		return "task worktree action failed"
	}
	message := strings.TrimSpace(e.Result.Message)
	if message == "" && e.Cause != nil {
		message = e.Cause.Error()
	}
	if message == "" {
		message = "operation did not complete"
	}
	outcome := fmt.Sprintf("%s: %s", e.Result.Status, message)
	if e.Result.Status == "succeeded" {
		outcome = "worktree action " + outcome
	}
	if e.RefreshErr != nil {
		outcome += "; refresh failed: " + e.RefreshErr.Error()
	}
	state := "branch state unavailable"
	if e.Result.Worktree != nil {
		worktree := e.Result.Worktree
		state = fmt.Sprintf("branch %s, target %s, integration %s",
			firstWorktreeValue(worktree.Branch), firstWorktreeValue(worktree.TargetBranch), firstWorktreeValue(worktree.IntegrationStatus))
	}
	return fmt.Sprintf("%s (%s)", outcome, state)
}

func (e *TaskWorktreeActionError) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.Cause == nil {
		return e.RefreshErr
	}
	if e.RefreshErr == nil {
		return e.Cause
	}
	return errors.Join(e.Cause, e.RefreshErr)
}

func firstWorktreeValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

// GetTaskWorktreeInfoForProject reads a task's backend-owned worktree panel.
// It first resolves the exact task through the selected project's reference
// catalog because the legacy HTML route itself is keyed only by task ID.
func (c *Client) GetTaskWorktreeInfoForProject(ctx context.Context, taskID, projectID string) (TaskWorktreeInfo, error) {
	taskID = strings.TrimSpace(taskID)
	projectID = strings.TrimSpace(projectID)
	if taskID == "" {
		return TaskWorktreeInfo{}, fmt.Errorf("task ID is required for worktree inspection")
	}
	if projectID == "" {
		return TaskWorktreeInfo{}, fmt.Errorf("project ID is required for worktree inspection")
	}
	if _, err := c.resolveTaskReferenceForProject(ctx, taskID, projectID); err != nil {
		return TaskWorktreeInfo{}, err
	}
	root, err := c.getHTML(ctx, "/tasks/"+url.PathEscape(taskID)+"/worktree"+query("project_id", projectID))
	if err != nil {
		return TaskWorktreeInfo{}, err
	}
	info := parseTaskWorktreeInfo(root, taskID, projectID)
	changes, err := c.getHTML(ctx, "/tasks/"+url.PathEscape(taskID)+"/changes"+query("project_id", projectID))
	if err != nil {
		return info, fmt.Errorf("load task pull request state: %w", err)
	}
	info.PullRequest = parseTaskPullRequestInfo(changes)
	return info, nil
}

func parseTaskPullRequestInfo(root *html.Node) *TaskPullRequestInfo {
	for _, node := range findAll(root, func(node *html.Node) bool {
		return node.Type == html.ElementNode && node.Data == "a" && strings.HasPrefix(strings.TrimSpace(NodeText(node)), "View PR #")
	}) {
		label := strings.TrimSpace(NodeText(node))
		number, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(label, "View PR #")))
		if err != nil || number < 1 {
			continue
		}
		link, err := url.Parse(strings.TrimSpace(attr(node, "href")))
		if err != nil || (link.Scheme != "https" && link.Scheme != "http") || link.Host == "" || link.User != nil {
			continue
		}
		state := strings.TrimSpace(attr(node, "data-pr-state"))
		if state == "" {
			state = strings.TrimSpace(attr(node, "data-pull-request-state"))
		}
		if state == "" {
			state = "recorded"
		}
		return &TaskPullRequestInfo{Number: number, URL: link.String(), State: state}
	}
	return nil
}

// CreateTaskPullRequestForProject asks the backend to publish or reuse a task
// worktree pull request. The server owns repository validation and GitHub API
// access; the client only submits the selected project scope and validates the
// successful HTMX toast and refreshed PR link.
func (c *Client) CreateTaskPullRequestForProject(ctx context.Context, taskID, projectID string) (TaskPullRequestResult, error) {
	taskID = strings.TrimSpace(taskID)
	projectID = strings.TrimSpace(projectID)
	if taskID == "" {
		return TaskPullRequestResult{}, fmt.Errorf("task ID is required for pull request creation")
	}
	if projectID == "" {
		return TaskPullRequestResult{}, fmt.Errorf("project ID is required for pull request creation")
	}
	if _, err := c.resolveTaskReferenceForProject(ctx, taskID, projectID); err != nil {
		return TaskPullRequestResult{}, err
	}

	path := "/tasks/" + url.PathEscape(taskID) + "/worktree/pull-request" + query("project_id", projectID)
	req, err := c.newRequest(ctx, http.MethodPost, path, nil)
	if err != nil {
		return TaskPullRequestResult{}, err
	}
	req.Header.Set("HX-Request", "true")
	req.Header.Set("Accept", "text/html, application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return TaskPullRequestResult{}, fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	if isAuthResponse(resp) {
		return TaskPullRequestResult{}, newAuthRequiredError(http.MethodPost, path, resp)
	}

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if readErr != nil {
		return TaskPullRequestResult{}, fmt.Errorf("read task pull request response: %w", readErr)
	}
	toastStatus, toastMessage, toastErr := taskPullRequestToast(resp.Header.Get("HX-Trigger"))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if toastErr == nil && toastMessage != "" {
			return TaskPullRequestResult{}, fmt.Errorf("create task pull request: %s", toastMessage)
		}
		return TaskPullRequestResult{}, &HTTPStatusError{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(body))}
	}
	if toastErr != nil {
		return TaskPullRequestResult{}, fmt.Errorf("create task pull request: backend returned no valid success notification: %w", toastErr)
	}
	if !strings.EqualFold(toastStatus, "success") {
		if toastMessage == "" {
			toastMessage = "backend did not confirm pull request creation"
		}
		return TaskPullRequestResult{}, fmt.Errorf("create task pull request: %s", toastMessage)
	}

	status, number, err := parseTaskPullRequestOutcome(toastMessage)
	if err != nil {
		return TaskPullRequestResult{}, err
	}
	changes, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return TaskPullRequestResult{}, fmt.Errorf("parse refreshed task changes: %w", err)
	}
	pullRequest := parseTaskPullRequestInfo(changes)
	if pullRequest == nil {
		return TaskPullRequestResult{}, fmt.Errorf("backend confirmed pull request #%d, but the refreshed task changes did not include its link", number)
	}
	if pullRequest.Number != number {
		return TaskPullRequestResult{}, fmt.Errorf("backend confirmed pull request #%d, but the refreshed task changes linked pull request #%d", number, pullRequest.Number)
	}
	return TaskPullRequestResult{Number: number, URL: pullRequest.URL, Status: status}, nil
}

func taskPullRequestToast(raw string) (status, message string, err error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return "", "", fmt.Errorf("invalid HTMX notification")
	}
	rawToast, ok := payload["openvibelyToast"]
	if !ok {
		return "", "", fmt.Errorf("missing openvibelyToast notification")
	}
	var toast struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rawToast, &toast); err != nil {
		return "", "", fmt.Errorf("invalid openvibelyToast notification")
	}
	return strings.TrimSpace(toast.Status), strings.TrimSpace(toast.Message), nil
}

func parseTaskPullRequestOutcome(message string) (status string, number int, err error) {
	lower := strings.ToLower(strings.TrimSpace(message))
	switch {
	case strings.HasPrefix(lower, "github pr created (#"):
		status = "created"
	case strings.HasPrefix(lower, "github pr already exists (#"), strings.HasPrefix(lower, "github pr reused (#"):
		status = "reused"
	default:
		return "", 0, fmt.Errorf("create task pull request: backend returned an unrecognized success notification %q", message)
	}
	message = strings.TrimSpace(message)
	end := strings.LastIndex(message, ")")
	hash := strings.LastIndex(message, "#")
	if hash < 0 || end <= hash {
		return "", 0, fmt.Errorf("create task pull request: backend returned an invalid pull request number")
	}
	numberText := strings.TrimSpace(message[hash+1 : end])
	number, err = strconv.Atoi(numberText)
	if err != nil || number < 1 {
		return "", 0, fmt.Errorf("create task pull request: backend returned an invalid pull request number")
	}
	return status, number, nil
}

func (c *Client) resolveTaskReferenceForProject(ctx context.Context, taskID, projectID string) (Task, error) {
	tasks, err := c.ListTaskReferences(ctx, projectID)
	if err != nil {
		return Task{}, err
	}
	for _, task := range tasks {
		if task.ID == taskID {
			if task.ProjectID != projectID {
				return Task{}, fmt.Errorf("task %q was not found in selected project", taskID)
			}
			return task, nil
		}
	}
	return Task{}, fmt.Errorf("task %q was not found in selected project", taskID)
}

func parseTaskWorktreeInfo(root *html.Node, taskID, projectID string) TaskWorktreeInfo {
	info := TaskWorktreeInfo{TaskID: taskID, ProjectID: projectID}
	panel := findByID(root, "worktree-info-panel")
	if panel == nil {
		info.IntegrationStatus = "unknown"
		return info
	}
	info.Branch = worktreeLabelValue(panel, "Branch:")
	info.TargetBranch = worktreeLabelValue(panel, "Target:")
	info.IntegrationStatus = parseWorktreeIntegrationStatus(panel)
	if info.IntegrationStatus == "" {
		if info.Branch == "" {
			info.IntegrationStatus = "none"
		} else {
			info.IntegrationStatus = "unknown"
		}
	}
	return info
}

func populateTaskWorktreeMetadata(root *html.Node, task *Task) {
	if task == nil {
		return
	}
	info := parseTaskWorktreeInfo(root, task.ID, task.ProjectID)
	if info.Branch != "" {
		task.WorktreeBranch = info.Branch
	}
	if info.TargetBranch != "" {
		task.MergeTargetBranch = info.TargetBranch
	}
	if info.IntegrationStatus != "unknown" {
		task.IntegrationStatus = info.IntegrationStatus
	}
}

func worktreeLabelValue(root *html.Node, label string) string {
	labelNode := findNode(root, func(node *html.Node) bool {
		return node.Type == html.ElementNode && strings.TrimSpace(NodeText(node)) == label
	})
	if labelNode == nil {
		return ""
	}
	for sibling := labelNode.NextSibling; sibling != nil; sibling = sibling.NextSibling {
		if sibling.Type != html.ElementNode {
			continue
		}
		if sibling.Data == "code" {
			return strings.TrimSpace(NodeText(sibling))
		}
		if value := findNode(sibling, func(node *html.Node) bool { return node.Data == "code" }); value != nil {
			return strings.TrimSpace(NodeText(value))
		}
	}
	return ""
}

func parseWorktreeIntegrationStatus(root *html.Node) string {
	for _, node := range findAll(root, func(node *html.Node) bool {
		return node.Data == "span" && strings.Contains(" "+attr(node, "class")+" ", " badge ")
	}) {
		value := strings.ToLower(strings.TrimSpace(NodeText(node)))
		switch value {
		case "pending", "pending merge":
			return "pending"
		case "merged":
			return "merged"
		case "failed", "merge failed":
			return "failed"
		case "conflict", "conflicts", "merge conflict":
			return "conflict"
		}
	}
	return ""
}

// ApplyTaskWorktreeActionForProject asks the backend to merge or rebase a
// project-owned task branch, then reads the worktree panel again so failures and
// successes report the branch state that actually exists after the operation.
func (c *Client) ApplyTaskWorktreeActionForProject(ctx context.Context, taskID, projectID, action, strategy string) (TaskWorktreeActionResult, error) {
	taskID = strings.TrimSpace(taskID)
	projectID = strings.TrimSpace(projectID)
	action = strings.ToLower(strings.TrimSpace(action))
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if taskID == "" {
		return TaskWorktreeActionResult{}, fmt.Errorf("task ID is required for worktree integration")
	}
	if projectID == "" {
		return TaskWorktreeActionResult{}, fmt.Errorf("project ID is required for worktree integration")
	}
	switch action {
	case "merge":
		if strategy != "merge" && strategy != "ff" && strategy != "squash" {
			return TaskWorktreeActionResult{}, fmt.Errorf("invalid merge strategy %q; supported strategies: merge, ff, squash", strategy)
		}
	case "rebase":
		if strategy != "" {
			return TaskWorktreeActionResult{}, fmt.Errorf("rebase does not accept a merge strategy")
		}
	default:
		return TaskWorktreeActionResult{}, fmt.Errorf("invalid worktree action %q; supported actions: merge, rebase", action)
	}
	if _, err := c.resolveTaskReferenceForProject(ctx, taskID, projectID); err != nil {
		return TaskWorktreeActionResult{}, err
	}

	form := url.Values{
		"project_id":   {projectID},
		"merge_source": {"task_card"},
	}
	if action == "merge" {
		form.Set("merge_type", strategy)
	}
	response, responseErr := c.taskWorktreeMutation(ctx, taskID, action, form)
	result := TaskWorktreeActionResult{Action: action, Strategy: strategy}
	if responseErr != nil {
		result.Status = classifyTaskWorktreeActionError(responseErr)
		result.Message = taskWorktreeErrorMessage(responseErr)
	} else {
		result.Status, result.Message = taskWorktreeResponseOutcome(response)
	}

	info, refreshErr := c.GetTaskWorktreeInfoForProject(ctx, taskID, projectID)
	if refreshErr != nil {
		result.RefreshStatus = "failed"
		result.RefreshError = refreshErr.Error()
		if info.TaskID != "" {
			result.Worktree = &info
		}
		return result, &TaskWorktreeActionError{Result: result, Cause: responseErr, RefreshErr: refreshErr}
	}
	result.RefreshStatus = "succeeded"
	result.Worktree = &info
	if result.Status == "" {
		result.Status = "succeeded"
	}
	if result.Status != "succeeded" {
		return result, &TaskWorktreeActionError{Result: result, Cause: responseErr}
	}
	return result, nil
}

func (c *Client) taskWorktreeMutation(ctx context.Context, taskID, action string, form url.Values) (*http.Response, error) {
	body := strings.NewReader(form.Encode())
	path := "/tasks/" + url.PathEscape(taskID) + "/worktree/" + action
	req, err := c.newRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("Accept", "text/html, application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", path, err)
	}
	if isAuthResponse(resp) {
		drainAndClose(resp.Body)
		return nil, newAuthRequiredError(http.MethodPost, path, resp)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return nil, &HTTPStatusError{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(message))}
}

func taskWorktreeResponseOutcome(resp *http.Response) (string, string) {
	if resp == nil {
		return "failed", "backend returned no worktree action response"
	}
	defer drainAndClose(resp.Body)
	for _, header := range []string{"HX-Trigger", "HX-Trigger-After-Settle", "HX-Trigger-After-Swap"} {
		if value := strings.TrimSpace(resp.Header.Get(header)); value != "" {
			var payload map[string]json.RawMessage
			var toast struct {
				Message string `json:"message"`
				Status  string `json:"status"`
			}
			if json.Unmarshal([]byte(value), &payload) == nil {
				if raw := payload["openvibelyToast"]; len(raw) > 0 && json.Unmarshal(raw, &toast) == nil {
					message := strings.TrimSpace(toast.Message)
					status := strings.ToLower(strings.TrimSpace(toast.Status))
					if status == "completed" || status == "success" {
						return "succeeded", message
					}
					if status == "failed" || status == "error" {
						return classifyWorktreeMessage(message), message
					}
				}
			}
		}
	}
	return "succeeded", ""
}

func classifyTaskWorktreeActionError(err error) string {
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) {
		return classifyWorktreeStatus(statusErr.StatusCode, statusErr.Message)
	}
	return "failed"
}

func classifyWorktreeStatus(code int, message string) string {
	lower := strings.ToLower(message)
	if strings.Contains(lower, "conflict") || strings.Contains(lower, "already active") {
		return "conflict"
	}
	if code == http.StatusConflict || strings.Contains(lower, "not eligible") || strings.Contains(lower, "not currently eligible") || strings.Contains(lower, "not available for the current branch") {
		return "ineligible"
	}
	return "failed"
}

func classifyWorktreeMessage(message string) string {
	lower := strings.ToLower(message)
	if strings.Contains(lower, "conflict") || strings.Contains(lower, "resolve or abort") {
		return "conflict"
	}
	if strings.Contains(lower, "eligible") || strings.Contains(lower, "not available") || strings.Contains(lower, "locked") || strings.Contains(lower, "no mergeable") {
		return "ineligible"
	}
	return "failed"
}

func taskWorktreeErrorMessage(err error) string {
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) && strings.TrimSpace(statusErr.Message) != "" {
		return strings.TrimSpace(statusErr.Message)
	}
	return err.Error()
}
