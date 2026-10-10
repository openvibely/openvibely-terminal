package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// TaskWorktreeInfo is the project-scoped branch integration snapshot returned
// by the backend's task worktree panel.
type TaskWorktreeInfo struct {
	TaskID            string `json:"task_id"`
	ProjectID         string `json:"project_id"`
	Branch            string `json:"branch,omitempty"`
	TargetBranch      string `json:"target_branch,omitempty"`
	IntegrationStatus string `json:"integration_status"`
}

// TaskWorktreeActionResult describes the backend's merge or rebase result and
// the branch state refreshed after the action.
type TaskWorktreeActionResult struct {
	Status   string            `json:"status"` // succeeded | ineligible | conflict | failed
	Action   string            `json:"action"`
	Strategy string            `json:"strategy,omitempty"`
	Message  string            `json:"message,omitempty"`
	Worktree *TaskWorktreeInfo `json:"worktree,omitempty"`
}

// TaskWorktreeActionError carries the backend outcome while preserving the
// current branch snapshot for callers that need to exit non-zero.
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
	state := "branch state unavailable"
	if e.Result.Worktree != nil {
		worktree := e.Result.Worktree
		state = fmt.Sprintf("branch %s, target %s, integration %s",
			firstWorktreeValue(worktree.Branch), firstWorktreeValue(worktree.TargetBranch), firstWorktreeValue(worktree.IntegrationStatus))
	}
	return fmt.Sprintf("%s: %s (%s)", e.Result.Status, message, state)
}

func (e *TaskWorktreeActionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
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
	return info, nil
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
	if info.Branch == "" {
		info.IntegrationStatus = "none"
	} else if info.IntegrationStatus == "" {
		info.IntegrationStatus = "unknown"
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
		if result.Status == "succeeded" {
			result.Status = "failed"
			result.Message = "backend accepted the action, but the current branch state could not be refreshed"
		}
		return result, &TaskWorktreeActionError{Result: result, Cause: responseErr, RefreshErr: refreshErr}
	}
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
