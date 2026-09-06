package client

// This file covers the remainder of the backend's swagger-documented JSON API
// (docs/swagger.json in the openvibely repo): analytics, skill analytics,
// capacity by model, workflow agent metrics, collision detection, lifecycle
// executions, and schedule toggling. Struct fields mirror
// the swagger definitions (handler.*, models.*, repository.*, viewmodels.*).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// --- Analytics (/api/analytics/*) ---

// UsageTotals mirrors models.UsageTotals.
type UsageTotals struct {
	CallCount     int     `json:"call_count"`
	InputTokens   int64   `json:"input_tokens"`
	OutputTokens  int64   `json:"output_tokens"`
	TotalTokens   int64   `json:"total_tokens"`
	CachedInput   int64   `json:"cached_input_tokens"`
	CostUSD       float64 `json:"cost_usd"`
	CostAvailable bool    `json:"cost_available"`
}

// ModelUsagePoint mirrors models.ModelUsagePoint.
type ModelUsagePoint struct {
	Provider    string  `json:"provider"`
	Model       string  `json:"model"`
	CallCount   int     `json:"call_count"`
	TotalTokens int64   `json:"total_tokens"`
	CostUSD     float64 `json:"cost_usd"`
	Percent     float64 `json:"percent"`
}

// AccountLimit mirrors models.AccountLimitView.
type AccountLimit struct {
	Label       string  `json:"label"`
	Status      string  `json:"status"`
	UsedPercent float64 `json:"used_percent"`
	ResetsAt    string  `json:"resets_at"`
}

// AccountUsage mirrors models.AccountUsageView.
type AccountUsage struct {
	Provider      string         `json:"provider"`
	PlanType      string         `json:"plan_type"`
	AccountDetail string         `json:"account_detail"`
	StatusLabel   string         `json:"status_label"`
	PrimaryLimit  *AccountLimit  `json:"primary_limit"`
	Limits        []AccountLimit `json:"limits"`
	Error         string         `json:"error"`
}

// UsageAnalytics mirrors models.AnalyticsUsageViewModel.
type UsageAnalytics struct {
	Totals         UsageTotals       `json:"totals"`
	ModelBreakdown []ModelUsagePoint `json:"model_breakdown"`
	AccountLimits  []AccountUsage    `json:"account_limits"`
	Errors         []string          `json:"errors"`
	LastUpdatedAt  string            `json:"last_updated_at"`
}

// SuccessFailureRate mirrors repository.SuccessFailureRate.
type SuccessFailureRate struct {
	Period       string  `json:"period"`
	SuccessCount int     `json:"successCount"`
	FailureCount int     `json:"failureCount"`
	TotalCount   int     `json:"totalCount"`
	SuccessRate  float64 `json:"successRate"`
}

// AvgExecutionTime mirrors repository.AvgExecutionTime.
type AvgExecutionTime struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	AvgMs float64 `json:"avgMs"`
	MinMs int64   `json:"minMs"`
	MaxMs int64   `json:"maxMs"`
	Count int     `json:"count"`
}

// TaskFrequency mirrors repository.TaskFrequency.
type TaskFrequency struct {
	TaskID         string `json:"taskID"`
	TaskTitle      string `json:"taskTitle"`
	ExecutionCount int    `json:"executionCount"`
	LastExecutedAt string `json:"lastExecutedAt"`
}

// FailedTaskPattern mirrors repository.FailedTaskPattern.
type FailedTaskPattern struct {
	TaskID       string `json:"taskID"`
	TaskTitle    string `json:"taskTitle"`
	FailureCount int    `json:"failureCount"`
	LastError    string `json:"lastError"`
	LastFailedAt string `json:"lastFailedAt"`
}

// analyticsObject is the shared fetch/decode helper for analytics endpoints
// that return a JSON object. segment is the URL path segment after /api/analytics/.
func analyticsObject[T any](ctx context.Context, c *Client, segment, projectID string) (*T, error) {
	var out T
	if err := c.getJSON(ctx, "/api/analytics/"+segment+query("project_id", projectID), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetUsageAnalytics fetches LLM usage/cost analytics.
func (c *Client) GetUsageAnalytics(ctx context.Context, projectID string) (*UsageAnalytics, error) {
	return analyticsObject[UsageAnalytics](ctx, c, "usage", projectID)
}

// analyticsSlice is the shared fetch/decode helper for analytics endpoints that
// return a JSON array. segment is the URL path segment after /api/analytics/.
func analyticsSlice[T any](ctx context.Context, c *Client, segment, projectID string) ([]T, error) {
	var out []T
	err := c.getJSON(ctx, "/api/analytics/"+segment+query("project_id", projectID), &out)
	return out, err
}

// GetSuccessFailureRates fetches execution success/failure rates.
func (c *Client) GetSuccessFailureRates(ctx context.Context, projectID string) ([]SuccessFailureRate, error) {
	return analyticsSlice[SuccessFailureRate](ctx, c, "success-failure-rates", projectID)
}

// GetAvgExecutionTimeByTask fetches per-task average execution times.
func (c *Client) GetAvgExecutionTimeByTask(ctx context.Context, projectID string) ([]AvgExecutionTime, error) {
	return analyticsSlice[AvgExecutionTime](ctx, c, "avg-execution-time-by-task", projectID)
}

// GetAvgExecutionTimeByAgent fetches per-model average execution times.
func (c *Client) GetAvgExecutionTimeByAgent(ctx context.Context, projectID string) ([]AvgExecutionTime, error) {
	return analyticsSlice[AvgExecutionTime](ctx, c, "avg-execution-time-by-agent", projectID)
}

// GetMostFrequentTasks fetches the most frequently executed tasks.
func (c *Client) GetMostFrequentTasks(ctx context.Context, projectID string) ([]TaskFrequency, error) {
	return analyticsSlice[TaskFrequency](ctx, c, "most-frequent-tasks", projectID)
}

// GetFailedTaskPatterns fetches recurring task failure patterns.
func (c *Client) GetFailedTaskPatterns(ctx context.Context, projectID string) ([]FailedTaskPattern, error) {
	return analyticsSlice[FailedTaskPattern](ctx, c, "failed-task-patterns", projectID)
}

// --- Skill analytics (/api/analytics/skills) ---

// SkillMetric mirrors models.SkillAnalyticsSkillMetric.
type SkillMetric struct {
	SkillHandle       string  `json:"skill_handle"`
	SkillScope        string  `json:"skill_scope"`
	ActivityCount     int     `json:"activity_count"`
	SelectedCount     int     `json:"selected_count"`
	LoadedCount       int     `json:"loaded_count"`
	ViewedCount       int     `json:"viewed_count"`
	FollowThroughRate float64 `json:"follow_through_rate"`
	LastActivity      string  `json:"last_activity"`
}

// UnderusedSkill mirrors models.UnderusedSkillMetric.
type UnderusedSkill struct {
	SkillHandle   string `json:"skill_handle"`
	SkillScope    string `json:"skill_scope"`
	Enabled       bool   `json:"enabled"`
	AlwaysUse     bool   `json:"always_use"`
	ActivityCount int    `json:"activity_count"`
	LastActivity  string `json:"last_activity"`
}

// SkillAnalytics mirrors models.SkillAnalyticsDashboard.
type SkillAnalytics struct {
	TopSkills []SkillMetric    `json:"top_skills"`
	Underused []UnderusedSkill `json:"underused"`
}

// GetSkillAnalytics fetches the skill analytics dashboard.
func (c *Client) GetSkillAnalytics(ctx context.Context, projectID string) (*SkillAnalytics, error) {
	return analyticsObject[SkillAnalytics](ctx, c, "skills", projectID)
}

// --- Capacity by model (/api/capacity/models) ---

// ModelCapacity mirrors handler.ModelCapacityResponse.
type ModelCapacity struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Model          string `json:"model"`
	Running        int    `json:"running"`
	MaxWorkers     int    `json:"max_workers"`
	AvailableSlots int    `json:"available_slots"`
	HasCapacity    bool   `json:"has_capacity"`
}

// GetModelCapacities fetches per-model worker capacity.
func (c *Client) GetModelCapacities(ctx context.Context) ([]ModelCapacity, error) {
	var out []ModelCapacity
	err := c.getJSON(ctx, "/api/capacity/models", &out)
	return out, err
}

// --- Workflows (/api/workflows/*) ---

// AgentMetric mirrors models.AgentPerformanceMetric.
type AgentMetric struct {
	ID              string  `json:"id"`
	AgentConfigID   string  `json:"agent_config_id"`
	TaskType        string  `json:"task_type"`
	SuccessCount    int     `json:"success_count"`
	FailureCount    int     `json:"failure_count"`
	AvgDurationMs   int64   `json:"avg_duration_ms"`
	AvgCostCents    int64   `json:"avg_cost_cents"`
	AvgQualityScore float64 `json:"avg_quality_score"`
	LastUpdated     string  `json:"last_updated"`
}

// AgentRecommendation is the /api/workflows/best-agent and cheapest-agent
// response: either {agent, metrics} or {message} when no data exists.
type AgentRecommendation struct {
	Message string          `json:"message,omitempty"`
	Agent   json.RawMessage `json:"agent,omitempty"`
	Metrics *AgentMetric    `json:"metrics,omitempty"`
}

// AgentName extracts a display name from the raw agent payload.
func (r *AgentRecommendation) AgentName() string {
	var a struct {
		Name  string `json:"name"`
		Model string `json:"model"`
	}
	if json.Unmarshal(r.Agent, &a) != nil {
		return ""
	}
	if a.Name != "" {
		return a.Name
	}
	return a.Model
}

// VoteRecord mirrors models.VoteRecord (parallel workflow step voting).
type VoteRecord struct {
	ID              string  `json:"id"`
	StepExecutionID string  `json:"step_execution_id"`
	AgentConfigID   string  `json:"agent_config_id"`
	Vote            string  `json:"vote"`
	Confidence      float64 `json:"confidence"`
	Reasoning       string  `json:"reasoning"`
}

// GetAllAgentMetrics fetches performance metrics for all models.
func (c *Client) GetAllAgentMetrics(ctx context.Context) ([]AgentMetric, error) {
	var out []AgentMetric
	err := c.getJSON(ctx, "/api/workflows/metrics", &out)
	return out, err
}

// agentRecommendation is the shared fetch/decode logic for agent recommendation endpoints.
func (c *Client) agentRecommendation(ctx context.Context, kind, taskType string) (*AgentRecommendation, error) {
	var out AgentRecommendation
	path := "/api/workflows/" + kind
	if taskType != "" {
		path += "?task_type=" + url.QueryEscape(taskType)
	}
	if err := c.getJSON(ctx, path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetBestAgent asks the backend which model performs best for a task type.
func (c *Client) GetBestAgent(ctx context.Context, taskType string) (*AgentRecommendation, error) {
	return c.agentRecommendation(ctx, "best-agent", taskType)
}

// GetCheapestAgent asks for the cheapest model meeting a quality threshold.
func (c *Client) GetCheapestAgent(ctx context.Context, taskType string) (*AgentRecommendation, error) {
	return c.agentRecommendation(ctx, "cheapest-agent", taskType)
}

// GetVoteRecords fetches vote records for a parallel workflow step execution.
func (c *Client) GetVoteRecords(ctx context.Context, stepExecID string) ([]VoteRecord, error) {
	var out []VoteRecord
	err := c.getJSON(ctx, "/api/workflows/votes/"+url.PathEscape(stepExecID), &out)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = make([]VoteRecord, 0)
	}
	return out, nil
}

// --- Lifecycle executions (/api/tasks/:id/lifecycle-executions) ---

// LifecycleExecution mirrors viewmodels.LifecycleExecutionView.
type LifecycleExecution struct {
	ID             string   `json:"id"`
	SkillKey       string   `json:"skill_key"`
	When           string   `json:"when"`
	Status         string   `json:"status"`
	AgentID        string   `json:"agent_id"`
	StartedAt      string   `json:"started_at"`
	CompletedAt    string   `json:"completed_at"`
	Summary        string   `json:"summary"`
	Error          string   `json:"error"`
	SelectedSkills []string `json:"selected_skills"`
}

// LifecycleEvent mirrors viewmodels.LifecycleExecutionEventView.
type LifecycleEvent struct {
	ID        string         `json:"id"`
	Seq       int            `json:"seq"`
	EventType string         `json:"event_type"`
	Payload   map[string]any `json:"payload"`
	CreatedAt string         `json:"created_at"`
}

// ListTaskLifecycleExecutions fetches lifecycle executions for a task using
// the backend's default project scope. Task-detail rendering uses this legacy
// form to preserve its existing fallback behavior.
func (c *Client) ListTaskLifecycleExecutions(ctx context.Context, taskID string) ([]LifecycleExecution, error) {
	return c.listTaskLifecycleExecutions(ctx, taskID, "")
}

// ListTaskLifecycleExecutionsForProject fetches lifecycle executions for a task
// within the explicitly selected project.
func (c *Client) ListTaskLifecycleExecutionsForProject(ctx context.Context, taskID, projectID string) ([]LifecycleExecution, error) {
	return c.listTaskLifecycleExecutions(ctx, taskID, projectID)
}

func (c *Client) listTaskLifecycleExecutions(ctx context.Context, taskID, projectID string) ([]LifecycleExecution, error) {
	var out []LifecycleExecution
	err := c.getJSON(ctx, "/api/tasks/"+url.PathEscape(taskID)+"/lifecycle-executions"+query("project_id", projectID), &out)
	return out, err
}

// GetLifecycleExecutionEvents fetches the trace events of one execution using
// the backend's default project scope.
func (c *Client) GetLifecycleExecutionEvents(ctx context.Context, execID string) ([]LifecycleEvent, error) {
	return c.getLifecycleExecutionEvents(ctx, execID, "")
}

// GetLifecycleExecutionEventsForProject fetches trace events within the
// explicitly selected project.
func (c *Client) GetLifecycleExecutionEventsForProject(ctx context.Context, execID, projectID string) ([]LifecycleEvent, error) {
	return c.getLifecycleExecutionEvents(ctx, execID, projectID)
}

func (c *Client) getLifecycleExecutionEvents(ctx context.Context, execID, projectID string) ([]LifecycleEvent, error) {
	var out []LifecycleEvent
	err := c.getJSON(ctx, "/api/lifecycle-executions/"+url.PathEscape(execID)+"/events"+query("project_id", projectID), &out)
	return out, err
}

// --- Schedules (/api/schedules/:id/toggle) ---

// Schedule mirrors models.Schedule.
type Schedule struct {
	ID         string `json:"id"`
	TaskID     string `json:"task_id"`
	Enabled    bool   `json:"enabled"`
	RepeatType string `json:"repeat_type"`
	RunAt      string `json:"run_at"`
	NextRun    string `json:"next_run"`
	LastRun    string `json:"last_run"`
}

// ToggleSchedule flips a schedule's enabled state.
func (c *Client) ToggleSchedule(ctx context.Context, projectID, id string) (*Schedule, error) {
	var out Schedule
	if err := c.postJSON(ctx, "/api/schedules/"+url.PathEscape(id)+"/toggle"+query("project_id", projectID), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// postJSON issues a POST (optionally with a JSON body) and decodes a JSON
// response into out when out is non-nil. Non-2xx statuses become errors.
func (c *Client) postJSON(ctx context.Context, path string, body, out any) error {
	var rdr *bytes.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(buf)
	} else {
		rdr = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	defer drainAndClose(resp.Body)

	if isAuthResponse(resp) {
		return newAuthRequiredError(http.MethodPost, path, resp)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiError(resp)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding %s response: %w", path, err)
	}
	return nil
}
