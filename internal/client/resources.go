package client

// Resource access for the OpenVibely screens that the backend renders as HTML:
// alerts, skills, models, agents, schedules, workers, channels, personality,
// pulse (upcoming), reflection (history), insights and grades.
//
// Reads scrape the rendered fragment; writes reuse the routes the web UI posts
// to, sent as HTMX requests so the server answers 2xx instead of redirecting.

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// --- alerts ---

// Alert is one row on the Alerts screen.
type Alert struct {
	ID      string
	Title   string
	Message string
	Text    string   // searchable text (type, state, body)
	Badges  []string // type, decision state, processing state
	Read    bool
}

// ListAlerts scrapes the alerts screen for a project.
//
// An alert row renders its title in a <p class="font-semibold">, its message in
// the following muted paragraph, and marks read rows with an "opacity-60" card
// class.
func (c *Client) ListAlerts(ctx context.Context, projectID string) ([]Alert, error) {
	root, err := c.getHTML(ctx, "/alerts"+query("project_id", projectID))
	if err != nil {
		return nil, err
	}
	nodes := findAll(root, func(e *html.Node) bool { return attr(e, "data-alert-id") != "" })

	seen := map[string]bool{}
	out := make([]Alert, 0, len(nodes))
	for _, n := range nodes {
		id := attr(n, "data-alert-id")
		// Nested action buttons repeat the id but not the row markers.
		if id == "" || seen[id] || attr(n, "data-alert-scroll-anchor") == "" {
			continue
		}
		seen[id] = true

		a := Alert{
			ID:     id,
			Text:   attr(n, "data-search-text"),
			Read:   strings.Contains(attr(n, "class"), "opacity-60"),
			Badges: cardBadges(n),
		}
		if p := findNode(n, func(e *html.Node) bool {
			return e.Data == "p" && strings.Contains(attr(e, "class"), "font-semibold")
		}); p != nil {
			a.Title = strings.TrimSpace(NodeText(p))
		}
		if p := findNode(n, func(e *html.Node) bool {
			cls := attr(e, "class")
			return e.Data == "p" && strings.Contains(cls, "text-sm") && strings.Contains(cls, "opacity-60")
		}); p != nil {
			a.Message = strings.TrimSpace(NodeText(p))
		}
		if a.Title == "" {
			a.Title = firstLine(NodeText(n))
		}
		out = append(out, a)
	}
	return out, nil
}

// AlertAction runs read/approve/reject/dismiss on one alert.
func (c *Client) AlertAction(ctx context.Context, alertID, action, projectID string) error {
	return c.doForm(ctx, http.MethodPost,
		"/alerts/"+url.PathEscape(alertID)+"/"+action+query("project_id", projectID), nil)
}

// DeleteAlert removes one alert.
func (c *Client) DeleteAlert(ctx context.Context, alertID, projectID string) error {
	return c.doForm(ctx, http.MethodDelete,
		"/alerts/"+url.PathEscape(alertID)+query("project_id", projectID), nil)
}

// MarkAllAlertsRead marks every alert read.
func (c *Client) MarkAllAlertsRead(ctx context.Context, projectID string) error {
	return c.doForm(ctx, http.MethodPost, "/alerts/read-all"+query("project_id", projectID), nil)
}

// DeleteAllAlerts clears the alert list.
func (c *Client) DeleteAllAlerts(ctx context.Context, projectID string) error {
	return c.doForm(ctx, http.MethodDelete, "/alerts"+query("project_id", projectID), nil)
}

// --- skills ---

// Skill is one card on the Skills screen.
type Skill struct {
	Handle      string
	Name        string
	Description string
	Scope       string
	Source      string
	Content     string
	Enabled     bool
	AlwaysUse   bool
}

// ListSkills scrapes the skills screen.
func (c *Client) ListSkills(ctx context.Context, projectID string) ([]Skill, error) {
	root, err := c.getHTML(ctx, "/skills"+query("project_id", projectID))
	if err != nil {
		return nil, err
	}
	cards := dedupeCards(scrapeCards(root, "data-skill-handle"), "data-skill-handle")
	out := make([]Skill, 0, len(cards))
	for _, card := range cards {
		out = append(out, Skill{
			Handle:      card.Get("skill-handle"),
			Name:        card.Get("skill-name"),
			Description: card.Get("skill-description"),
			Scope:       card.Get("skill-scope"),
			Source:      card.Get("skill-source"),
			Content:     card.Get("skill-content"),
			Enabled:     card.Bool("skill-enabled"),
			AlwaysUse:   card.Bool("skill-always-use"),
		})
	}
	return out, nil
}

// CreateSkill adds a skill. body is the skill markdown/front-matter document.
func (c *Client) CreateSkill(ctx context.Context, projectID, name, description, body string) error {
	v := url.Values{}
	v.Set("name", name)
	v.Set("description", description)
	v.Set("content", body)
	v.Set("scope", "project")
	return c.doForm(ctx, http.MethodPost, "/skills"+query("project_id", projectID), v)
}

// UpdateSkill replaces a skill's body.
func (c *Client) UpdateSkill(ctx context.Context, projectID, handle, body string) error {
	v := url.Values{}
	v.Set("content", body)
	return c.doForm(ctx, http.MethodPut, "/skills/"+url.PathEscape(handle)+query("project_id", projectID), v)
}

// DeleteSkill removes a skill.
func (c *Client) DeleteSkill(ctx context.Context, projectID, handle string) error {
	return c.doForm(ctx, http.MethodDelete, "/skills/"+url.PathEscape(handle)+query("project_id", projectID), nil)
}

// SetSkillEnabled enables or disables a skill.
func (c *Client) SetSkillEnabled(ctx context.Context, projectID, handle string, enabled bool) error {
	v := url.Values{}
	v.Set("enabled", boolStr(enabled))
	return c.doForm(ctx, http.MethodPost,
		"/skills/"+url.PathEscape(handle)+"/enabled"+query("project_id", projectID), v)
}

// SetSkillAlwaysUse toggles a skill's always-use flag.
func (c *Client) SetSkillAlwaysUse(ctx context.Context, projectID, handle string, always bool) error {
	v := url.Values{}
	v.Set("always_use", boolStr(always))
	return c.doForm(ctx, http.MethodPost,
		"/skills/"+url.PathEscape(handle)+"/always_use"+query("project_id", projectID), v)
}

// --- models ---

// LLMModel is one card on the Models screen.
type LLMModel struct {
	ID       string
	Name     string
	Provider string
	Model    string
	Text     string
}

// ListModels scrapes the models screen.
func (c *Client) ListModels(ctx context.Context, projectID string) ([]LLMModel, error) {
	root, err := c.getHTML(ctx, "/models"+query("project_id", projectID))
	if err != nil {
		return nil, err
	}
	cards := dedupeCards(scrapeCards(root, "data-model-id"), "data-model-id")
	out := make([]LLMModel, 0, len(cards))
	for _, card := range cards {
		if card.Get("model-name") == "" {
			continue
		}
		out = append(out, LLMModel{
			ID:       card.Get("model-id"),
			Name:     card.Get("model-name"),
			Provider: card.Get("model-provider"),
			Model:    card.Get("model-model"),
			Text:     card.Text,
		})
	}
	return out, nil
}

// SetDefaultModel marks a model as the default.
func (c *Client) SetDefaultModel(ctx context.Context, modelID string) error {
	return c.doForm(ctx, http.MethodPost, "/models/"+url.PathEscape(modelID)+"/set-default", nil)
}

// DeleteModel removes a model config.
func (c *Client) DeleteModel(ctx context.Context, modelID string) error {
	return c.doForm(ctx, http.MethodDelete, "/models/"+url.PathEscape(modelID), nil)
}

// --- agents ---

// AgentDef is one card on the Agents screen.
type AgentDef struct {
	ID          string
	Key         string
	Name        string
	Description string
	Model       string
	Scope       string
}

// ListAgents scrapes the agents screen.
func (c *Client) ListAgents(ctx context.Context, projectID string) ([]AgentDef, error) {
	root, err := c.getHTML(ctx, "/agents"+query("project_id", projectID))
	if err != nil {
		return nil, err
	}
	cards := dedupeCards(scrapeCards(root, "data-agent-id"), "data-agent-id")
	out := make([]AgentDef, 0, len(cards))
	for _, card := range cards {
		if card.Get("agent-name") == "" {
			continue
		}
		out = append(out, AgentDef{
			ID:          card.Get("agent-id"),
			Key:         card.Get("agent-key"),
			Name:        card.Get("agent-name"),
			Description: card.Get("agent-description"),
			Model:       card.Get("agent-model"),
			Scope:       card.Get("agent-scope"),
		})
	}
	return out, nil
}

// DeleteAgent removes an agent definition.
func (c *Client) DeleteAgent(ctx context.Context, agentID string) error {
	return c.doForm(ctx, http.MethodDelete, "/agents/"+url.PathEscape(agentID), nil)
}

// GenerateAgent asks the backend to draft an agent from a description.
func (c *Client) GenerateAgent(ctx context.Context, projectID, description string) error {
	v := url.Values{}
	v.Set("description", description)
	return c.doForm(ctx, http.MethodPost, "/agents/generate"+query("project_id", projectID), v)
}

// --- schedule ---

// ScheduleEntry is one scheduled task occurrence.
type ScheduleEntry struct {
	TaskID     string
	ScheduleID string
	Text       string
}

// GetSchedule scrapes the Schedule screen for a project.
func (c *Client) GetSchedule(ctx context.Context, projectID string) ([]ScheduleEntry, string, error) {
	root, err := c.getHTML(ctx, "/schedule"+query("project_id", projectID))
	if err != nil {
		return nil, "", err
	}
	cards := dedupeCards(scrapeCards(root, "data-task-id"), "data-task-id")
	out := make([]ScheduleEntry, 0, len(cards))
	for _, card := range cards {
		out = append(out, ScheduleEntry{
			TaskID:     card.Get("task-id"),
			ScheduleID: card.Get("schedule-id"),
			Text:       strings.TrimSpace(card.Text),
		})
	}
	summary := ""
	if n := findByID(root, "schedule-content"); n != nil {
		summary = NodeText(n)
	}
	return out, summary, nil
}

// CreateSchedule schedules a task. repeat is once/daily/weekly/monthly/hourly.
func (c *Client) CreateSchedule(ctx context.Context, taskID, runAt, repeat string, interval int) error {
	v := url.Values{}
	v.Set("run_at", runAt)
	v.Set("repeat_type", repeat)
	if interval > 0 {
		v.Set("repeat_interval", strconv.Itoa(interval))
	}
	return c.doForm(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/schedule", v)
}

// DeleteSchedule removes a schedule.
func (c *Client) DeleteSchedule(ctx context.Context, scheduleID string) error {
	return c.doForm(ctx, http.MethodDelete, "/schedules/"+url.PathEscape(scheduleID), nil)
}

// --- workers ---

// GetWorkerSettings returns the Workers screen as text.
func (c *Client) GetWorkerSettings(ctx context.Context, projectID string) (string, error) {
	root, err := c.getHTML(ctx, "/workers"+query("project_id", projectID))
	if err != nil {
		return "", err
	}
	return NodeText(root), nil
}

// SetGlobalWorkerLimit updates the global max worker count.
func (c *Client) SetGlobalWorkerLimit(ctx context.Context, limit int) error {
	v := url.Values{}
	v.Set("max_workers", strconv.Itoa(limit))
	return c.doForm(ctx, http.MethodPost, "/workers", v)
}

// SetProjectWorkerLimit updates one project's worker limit.
func (c *Client) SetProjectWorkerLimit(ctx context.Context, projectID string, limit int) error {
	v := url.Values{}
	v.Set("max_workers", strconv.Itoa(limit))
	return c.doForm(ctx, http.MethodPost, "/workers/projects/"+url.PathEscape(projectID)+"/limit", v)
}

// --- channels & personality ---

// GetChannels returns the Channels (integrations) screen as text.
func (c *Client) GetChannels(ctx context.Context, projectID string) (string, error) {
	root, err := c.getHTML(ctx, "/channels"+query("project_id", projectID))
	if err != nil {
		return "", err
	}
	return NodeText(root), nil
}

// GetPersonality returns the Personality screen as text.
func (c *Client) GetPersonality(ctx context.Context, projectID string) (string, error) {
	root, err := c.getHTML(ctx, "/personality"+query("project_id", projectID))
	if err != nil {
		return "", err
	}
	if n := findByID(root, "personality-container"); n != nil {
		return NodeText(n), nil
	}
	return NodeText(root), nil
}

// SavePersonality sets the active personality preset.
func (c *Client) SavePersonality(ctx context.Context, projectID, personality string) error {
	v := url.Values{}
	v.Set("personality", personality)
	return c.doForm(ctx, http.MethodPost, "/personality/save"+query("project_id", projectID), v)
}

// --- pulse / reflection / insights / grades ---

// GetPulse returns the upcoming (Pulse) screen as text.
func (c *Client) GetPulse(ctx context.Context, projectID string) (string, error) {
	return c.pageText(ctx, "/upcoming"+query("project_id", projectID), "upcoming-container")
}

// GeneratePulseSummary asks the backend for a fresh pulse summary.
func (c *Client) GeneratePulseSummary(ctx context.Context, projectID string) error {
	return c.doForm(ctx, http.MethodPost, "/upcoming/summary"+query("project_id", projectID), nil)
}

// GetReflection returns the history (Reflection) screen as text.
func (c *Client) GetReflection(ctx context.Context, projectID string) (string, error) {
	return c.pageText(ctx, "/history"+query("project_id", projectID), "history-container")
}

// GenerateReflectionSummary asks the backend for a fresh reflection summary.
func (c *Client) GenerateReflectionSummary(ctx context.Context, projectID string) error {
	return c.doForm(ctx, http.MethodPost, "/history/summary"+query("project_id", projectID), nil)
}

// GradeIdeas runs idea grading (the Grades view on the history screen).
func (c *Client) GradeIdeas(ctx context.Context, projectID string) (string, error) {
	if err := c.doForm(ctx, http.MethodPost, "/history/grade-ideas"+query("project_id", projectID), nil); err != nil {
		return "", err
	}
	return c.pageText(ctx, "/history"+query("project_id", projectID), "idea-grade-content")
}

// GetInsights returns the proactive insights screen as text.
func (c *Client) GetInsights(ctx context.Context, projectID string) (string, error) {
	return c.pageText(ctx, "/insights"+query("project_id", projectID), "")
}

// RunInsightsAnalysis triggers a new insights analysis run.
func (c *Client) RunInsightsAnalysis(ctx context.Context, projectID string) error {
	return c.doForm(ctx, http.MethodPost, "/insights/analyze"+query("project_id", projectID), nil)
}

// GetAutomations returns the automations screen as text.
func (c *Client) GetAutomations(ctx context.Context, projectID string) (string, error) {
	return c.pageText(ctx, "/automations"+query("project_id", projectID), "")
}

// Automation is one card on the Automations screen.
type Automation struct {
	ID    string
	Name  string
	State string
}

// ListAutomations scrapes the automations screen for a project. Each card
// carries the automation id/name via its delete-menu button's
// data-automation-card-delete/data-automation-name attributes; lifecycle
// state comes from the card's own badge row.
func (c *Client) ListAutomations(ctx context.Context, projectID string) ([]Automation, error) {
	root, err := c.getHTML(ctx, "/automations"+query("project_id", projectID))
	if err != nil {
		return nil, err
	}
	cards := findAll(root, func(e *html.Node) bool { return attr(e, "data-automation-url") != "" })

	out := make([]Automation, 0, len(cards))
	for _, card := range cards {
		btn := findNode(card, func(e *html.Node) bool { return attr(e, "data-automation-card-delete") != "" })
		if btn == nil {
			continue
		}
		a := Automation{
			ID:   attr(btn, "data-automation-card-delete"),
			Name: attr(btn, "data-automation-name"),
		}
		for _, state := range []string{"active", "paused", "draft", "archived"} {
			for _, b := range cardBadges(card) {
				if strings.EqualFold(b, state) {
					a.State = state
				}
			}
		}
		if a.ID != "" {
			out = append(out, a)
		}
	}
	return out, nil
}

// RunAutomationNow triggers an immediate run of one automation.
func (c *Client) RunAutomationNow(ctx context.Context, automationID, projectID string) error {
	return c.doForm(ctx, http.MethodPost,
		"/automations/"+url.PathEscape(automationID)+"/run-now"+query("project_id", projectID), nil)
}

// PauseAutomation pauses one automation.
func (c *Client) PauseAutomation(ctx context.Context, automationID, projectID string) error {
	return c.doForm(ctx, http.MethodPost,
		"/automations/"+url.PathEscape(automationID)+"/pause"+query("project_id", projectID), nil)
}

// ResumeAutomation resumes one paused automation.
func (c *Client) ResumeAutomation(ctx context.Context, automationID, projectID string) error {
	return c.doForm(ctx, http.MethodPost,
		"/automations/"+url.PathEscape(automationID)+"/resume"+query("project_id", projectID), nil)
}

// DeleteAutomation removes one automation.
func (c *Client) DeleteAutomation(ctx context.Context, automationID, projectID string) error {
	return c.doForm(ctx, http.MethodPost,
		"/automations/"+url.PathEscape(automationID)+"/delete"+query("project_id", projectID), nil)
}

// pageText fetches a page and returns the text of elementID (or the whole
// document when elementID is empty or missing).
func (c *Client) pageText(ctx context.Context, path, elementID string) (string, error) {
	root, err := c.getHTML(ctx, path)
	if err != nil {
		return "", err
	}
	if elementID != "" {
		if n := findByID(root, elementID); n != nil {
			return NodeText(n), nil
		}
	}
	return NodeText(root), nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
