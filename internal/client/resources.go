package client

// Resource access for the OpenVibely screens that the backend renders as HTML:
// alerts, skills, models, agents, schedules, workers, channels, personality,
// pulse (upcoming), reflection (history), insights and grades.
//
// Reads scrape the rendered fragment; writes reuse the routes the web UI posts
// to, sent as HTMX requests so the server answers 2xx instead of redirecting.

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
	"time"
	"unicode"

	"golang.org/x/net/html"
)

// --- alerts ---

// Alert is one row on the Alerts screen.
type Alert struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Message string   `json:"message"`
	Text    string   `json:"text"`   // searchable text (type, state, body)
	Badges  []string `json:"badges"` // type, decision state, processing state
	Read    bool     `json:"read"`

	// The web alert card exposes these bounded summary values through badges and
	// semantic classes. Keep them out of the legacy list JSON shape; show/inspect
	// copies them into AlertSummary, where the fields have stable JSON names.
	ProjectID       string `json:"-"`
	Scope           string `json:"-"`
	Type            string `json:"-"`
	Severity        string `json:"-"`
	Source          string `json:"-"`
	DecisionState   string `json:"-"`
	ProcessingState string `json:"-"`
}

// AlertSummary is the bounded identity and workflow state shown alongside an
// alert's full detail. It is separate from Alert so the existing list JSON
// contract remains unchanged.
type AlertSummary struct {
	ID              string   `json:"id"`
	ProjectID       string   `json:"project_id"`
	Scope           string   `json:"scope"`
	Type            string   `json:"type"`
	Severity        string   `json:"severity"`
	Title           string   `json:"title"`
	Message         string   `json:"message"`
	Source          string   `json:"source"`
	DecisionState   string   `json:"decision_state"`
	ProcessingState string   `json:"processing_state"`
	Text            string   `json:"text"`
	Badges          []string `json:"badges"`
	Read            bool     `json:"read"`
}

// AlertDetail is the on-demand project-scoped detail fragment for one alert.
// Metadata is always initialized to an empty map when the backend has no
// structured metadata, so JSON callers receive {} rather than null.
type AlertDetail struct {
	Body     string         `json:"body"`
	Metadata map[string]any `json:"metadata"`
}

// AlertInspection combines the bounded alert summary with its on-demand full
// detail for the read-only show/inspect command.
type AlertInspection struct {
	Summary AlertSummary `json:"summary"`
	Detail  AlertDetail  `json:"detail"`
}

// ListAlerts scrapes the alerts screen for a project.
//
// An alert row renders its title in a <p class="font-semibold">, its message in
// the following muted paragraph, and marks read rows with an "opacity-60" card
// class.
func (c *Client) ListAlerts(ctx context.Context, projectID string) ([]Alert, error) {
	pages, err := c.getCardPages(ctx, "/alerts"+query("project_id", projectID))
	if err != nil {
		return nil, err
	}
	return aggregateAlertPages(pages, projectID), nil
}

// FindAlertByID incrementally traverses alert cards and stops after the first
// page containing id. On a completed miss, alerts contains the fully aggregated
// collection so callers can preserve non-ID matching without another traversal.
func (c *Client) FindAlertByID(ctx context.Context, id, projectID string) (Alert, []Alert, bool, error) {
	path := "/alerts" + query("project_id", projectID)
	root, hasMore, err := c.getHTMLPage(ctx, path)
	if err != nil {
		return Alert{}, nil, false, err
	}
	var found Alert
	pages, matched, err := c.getCardPagesFromInitialUntil(ctx, path, root, hasMore, func(root *html.Node) bool {
		for _, alert := range parseAlerts(root, projectID) {
			if alert.ID == id {
				found = alert
				return true
			}
		}
		return false
	})
	if err != nil {
		return Alert{}, nil, false, err
	}
	if matched {
		return found, nil, true, nil
	}
	return Alert{}, aggregateAlertPages(pages, projectID), false, nil
}

func aggregateAlertPages(pages []htmlPage, projectID string) []Alert {
	seen := make(map[string]bool)
	alerts := make([]Alert, 0)
	for _, page := range pages {
		for _, alert := range parseAlerts(page.root, projectID) {
			if seen[alert.ID] {
				continue
			}
			seen[alert.ID] = true
			alerts = append(alerts, alert)
		}
	}
	return alerts
}

func parseAlerts(root *html.Node, projectID string) []Alert {
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

		badges := cardBadges(n)
		a := Alert{
			ID:              id,
			ProjectID:       projectID,
			Scope:           firstAlertAttribute(n, "data-alert-scope"),
			Type:            firstAlertAttribute(n, "data-alert-type"),
			Severity:        firstAlertAttribute(n, "data-alert-severity"),
			Source:          firstAlertAttribute(n, "data-alert-source"),
			DecisionState:   firstAlertAttribute(n, "data-alert-decision-state", "data-alert-decision"),
			ProcessingState: firstAlertAttribute(n, "data-alert-processing-state", "data-alert-processing"),
			Text:            attr(n, "data-search-text"),
			Read:            strings.Contains(attr(n, "class"), "opacity-60"),
			Badges:          badges,
		}
		if a.Scope == "" {
			a.Scope = "project"
		}
		if a.Type == "" {
			a.Type = alertTypeFromBadges(badges)
		}
		if a.DecisionState == "" {
			a.DecisionState = alertDecisionFromBadges(badges)
		}
		if a.ProcessingState == "" {
			a.ProcessingState = alertProcessingFromBadges(badges)
		}
		if a.Severity == "" {
			a.Severity = alertSeverityFromCard(n)
		}
		if a.Type == "" || a.DecisionState == "" || a.ProcessingState == "" || a.Severity == "" {
			// data-search-text is a stable bounded summary marker on the backend
			// card. It also keeps parsing compatible with compact fixtures that do
			// not include all of the visual badge/icon markup.
			searchType, searchSeverity, searchDecision, searchProcessing := alertValuesFromSearchText(a.Text)
			if a.Type == "" {
				a.Type = searchType
			}
			if a.Severity == "" {
				a.Severity = searchSeverity
			}
			if a.DecisionState == "" {
				a.DecisionState = searchDecision
			}
			if a.ProcessingState == "" {
				a.ProcessingState = searchProcessing
			}
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
	return out
}

func firstAlertAttribute(node *html.Node, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(attr(node, name)); value != "" {
			return value
		}
	}
	return ""
}

func alertTypeFromBadges(badges []string) string {
	for _, badge := range badges {
		value := strings.ToLower(strings.TrimSpace(badge))
		switch value {
		case "task_failed", "task_needs_followup", "custom":
			return value
		}
	}
	for _, badge := range badges {
		value := strings.ToLower(strings.TrimSpace(badge))
		if value == "" || isAlertStateValue(value) || value == "project scoped" || value == "operational" {
			continue
		}
		return value
	}
	return ""
}

func alertDecisionFromBadges(badges []string) string {
	for _, badge := range badges {
		value := strings.ToLower(strings.TrimSpace(badge))
		switch value {
		case "not_required", "pending", "approved", "rejected", "dismissed":
			return value
		case "operational":
			return "not_required"
		}
	}
	return ""
}

func alertProcessingFromBadges(badges []string) string {
	for _, badge := range badges {
		value := strings.ToLower(strings.TrimSpace(badge))
		switch value {
		case "not_applicable", "unclaimed", "claimed", "implementation_task_linked", "completed", "failed":
			return value
		}
	}
	return ""
}

func isAlertStateValue(value string) bool {
	switch value {
	case "not_required", "pending", "approved", "rejected", "dismissed",
		"not_applicable", "unclaimed", "claimed", "implementation_task_linked", "completed", "failed",
		"info", "warning", "error":
		return true
	default:
		return false
	}
}

func alertSeverityFromCard(card *html.Node) string {
	if value := firstAlertAttribute(card, "data-alert-severity"); value != "" {
		return strings.ToLower(value)
	}
	icon := findNode(card, func(node *html.Node) bool {
		return node.Data == "svg" && (strings.Contains(attr(node, "class"), "text-error") ||
			strings.Contains(attr(node, "class"), "text-warning") ||
			strings.Contains(attr(node, "class"), "text-info"))
	})
	if icon == nil {
		return ""
	}
	class := attr(icon, "class")
	switch {
	case strings.Contains(class, "text-error"):
		return "error"
	case strings.Contains(class, "text-warning"):
		return "warning"
	case strings.Contains(class, "text-info"):
		return "info"
	default:
		return ""
	}
}

func alertValuesFromSearchText(text string) (typ, severity, decision, processing string) {
	for _, value := range strings.Fields(strings.ToLower(text)) {
		switch value {
		case "task_failed", "task_needs_followup", "custom":
			typ = firstAlertValue(typ, value)
		case "info", "warning", "error":
			severity = firstAlertValue(severity, value)
		case "not_required", "pending", "approved", "rejected", "dismissed":
			decision = firstAlertValue(decision, value)
		case "not_applicable", "unclaimed", "claimed", "implementation_task_linked", "completed", "failed":
			processing = firstAlertValue(processing, value)
		}
	}
	return typ, severity, decision, processing
}

func firstAlertValue(current, value string) string {
	if current != "" {
		return current
	}
	return value
}

// GetAlertDetail fetches the full detail fragment for one alert in the selected
// project. The endpoint is read-only and intentionally makes no list or
// mutation request.
func (c *Client) GetAlertDetail(ctx context.Context, alertID, projectID string) (*AlertDetail, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("project ID is required for alert details")
	}
	path := "/alerts/" + url.PathEscape(alertID) + "/details" + query("project_id", projectID)
	root, err := c.getHTML(ctx, path)
	if err != nil {
		return nil, err
	}
	detail, err := parseAlertDetail(root)
	if err != nil {
		return nil, err
	}
	return &detail, nil
}

func parseAlertDetail(root *html.Node) (AlertDetail, error) {
	detail := AlertDetail{Metadata: make(map[string]any)}
	if root == nil {
		return detail, nil
	}

	scope := findNode(root, func(node *html.Node) bool {
		return hasHTMLAttr(node, "data-alert-detail-loaded")
	})
	if scope == nil {
		scope = root
	}
	if body := findNode(scope, func(node *html.Node) bool {
		return hasHTMLAttr(node, "data-alert-markdown") && hasHTMLAttr(node, "data-raw-content")
	}); body != nil {
		detail.Body = attr(body, "data-raw-content")
	}

	metadataText := ""
	if metadata := findNode(scope, func(node *html.Node) bool {
		return hasHTMLAttr(node, "data-alert-metadata")
	}); metadata != nil {
		metadataText = attr(metadata, "data-alert-metadata")
		if metadataText == "" {
			metadataText = rawNodeText(metadata)
		}
	} else if metadata := findNode(scope, func(node *html.Node) bool {
		return node.Data == "pre" && !hasHTMLAttr(node, "data-alert-copy-text")
	}); metadata != nil {
		metadataText = rawNodeText(metadata)
	}
	if strings.TrimSpace(metadataText) == "" {
		return detail, nil
	}

	var metadata map[string]any
	if err := json.Unmarshal([]byte(metadataText), &metadata); err != nil {
		return detail, fmt.Errorf("decoding alert detail metadata: %w", err)
	}
	if metadata != nil {
		detail.Metadata = metadata
	}
	return detail, nil
}

func rawNodeText(node *html.Node) string {
	if node == nil {
		return ""
	}
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

// AlertAction runs read/approve/reject/dismiss on one alert.
func (c *Client) AlertAction(ctx context.Context, alertID, action, projectID string) error {
	return c.doForm(ctx, http.MethodPost,
		"/alerts/"+url.PathEscape(alertID)+"/"+action+query("project_id", projectID), nil)
}

// MarkAlertsReadBulk marks the supplied project-scoped alerts read and returns
// the backend's affected count. IDs are trimmed and deduplicated before the
// atomic request is sent.
func (c *Client) MarkAlertsReadBulk(ctx context.Context, projectID string, ids []string) (int, error) {
	var response struct {
		Updated int `json:"updated"`
	}
	if err := c.alertBulkMutation(ctx, http.MethodPost, "/alerts/read-bulk", projectID, ids, &response); err != nil {
		return 0, err
	}
	return response.Updated, nil
}

// DeleteAlertsBulk removes the supplied project-scoped alerts and returns the
// backend's affected count. IDs are trimmed and deduplicated before the atomic
// request is sent.
func (c *Client) DeleteAlertsBulk(ctx context.Context, projectID string, ids []string) (int, error) {
	var response struct {
		Deleted int `json:"deleted"`
	}
	if err := c.alertBulkMutation(ctx, http.MethodDelete, "/alerts/bulk", projectID, ids, &response); err != nil {
		return 0, err
	}
	return response.Deleted, nil
}

func (c *Client) alertBulkMutation(ctx context.Context, method, path, projectID string, ids []string, response any) error {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return fmt.Errorf("project ID is required for bulk alert mutations")
	}
	ids, err := dedupeAlertIDs(ids)
	if err != nil {
		return err
	}
	resp, err := c.doJSONResponse(ctx, method, path+query("project_id", projectID), struct {
		IDs []string `json:"ids"`
	}{IDs: ids})
	if err != nil {
		return err
	}
	defer drainAndClose(resp.Body)
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(response); err != nil {
		return fmt.Errorf("decoding %s response: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return fmt.Errorf("decoding %s response: trailing JSON data: %w", path, err)
	}
	return nil
}

func dedupeAlertIDs(ids []string) ([]string, error) {
	seen := make(map[string]struct{}, len(ids))
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, fmt.Errorf("alert IDs must not be empty")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return nil, fmt.Errorf("at least one alert ID is required")
	}
	return unique, nil
}

// DeleteAlert removes one alert.
func (c *Client) DeleteAlert(ctx context.Context, alertID, projectID string) error {
	_, err := c.DeleteAlertAndList(ctx, alertID, projectID)
	return err
}

// DeleteAlertAndList removes one alert and parses the refreshed alert list from
// the backend's HTMX response, following any card-page continuations without
// repeating the mutation or reloading its first page.
func (c *Client) DeleteAlertAndList(ctx context.Context, alertID, projectID string) ([]Alert, error) {
	root, err := c.doFormHTML(ctx, http.MethodDelete,
		"/alerts/"+url.PathEscape(alertID)+query("project_id", projectID), nil)
	if err != nil {
		return nil, err
	}
	paginationRoot := findNode(root, func(n *html.Node) bool {
		return hasHTMLAttr(n, "data-card-pagination-root")
	})
	hasMore := paginationRoot != nil && attr(paginationRoot, "data-card-pagination-has-more") == "true"
	pages, err := c.getCardPagesFromInitial(ctx, "/alerts"+query("project_id", projectID), root, hasMore)
	if err != nil {
		return nil, err
	}
	return aggregateAlertPages(pages, projectID), nil
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
	Handle      string `json:"handle"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Scope       string `json:"scope"`
	Source      string `json:"source"`
	Content     string `json:"content"`
	Enabled     bool   `json:"enabled"`
	AlwaysUse   bool   `json:"always_use"`
}

// ListSkills scrapes metadata-only cards from the skills screen. Skill bodies are
// deliberately deferred to GetSkillDetail so ordinary list and picker reads do
// not retain instruction documents.
func (c *Client) ListSkills(ctx context.Context, projectID string) ([]Skill, error) {
	pages, err := c.getCardPages(ctx, "/skills"+query("project_id", projectID))
	if err != nil {
		return nil, err
	}
	cards := make([]Card, 0)
	for _, page := range pages {
		cards = append(cards, dedupedCardsWithoutText(page.root, "data-skill-handle")...)
	}
	seen := make(map[string]bool)
	out := make([]Skill, 0, len(cards))
	for _, card := range cards {
		if seen[card.Get("skill-handle")] {
			continue
		}
		seen[card.Get("skill-handle")] = true
		out = append(out, Skill{
			Handle:      card.Get("skill-handle"),
			Name:        card.Get("skill-name"),
			Description: card.Get("skill-description"),
			Scope:       card.Get("skill-scope"),
			Source:      card.Get("skill-source"),
			Enabled:     card.Bool("skill-enabled"),
			AlwaysUse:   card.Bool("skill-always-use"),
		})
	}
	return out, nil
}

// GetSkillDetail fetches one selected skill's full instruction document from
// the backend's scoped detail endpoint.
func (c *Client) GetSkillDetail(ctx context.Context, projectID, handle, scope string) (Skill, error) {
	var skill Skill
	path := "/skills/" + url.PathEscape(handle) + "/details" + query("project_id", projectID, "scope", scope)
	if err := c.getJSON(ctx, path, &skill); err != nil {
		return Skill{}, err
	}
	if skill.Handle != handle || skill.Scope != scope {
		return Skill{}, fmt.Errorf("mismatched skill detail for %q", handle)
	}
	return skill, nil
}

// ListSkillsWithContent retains the full-content JSON list contract by loading
// the instruction body for each summary after the paginated catalog resolves.
// Ordinary terminal lists and selectors must use ListSkills instead.
func (c *Client) ListSkillsWithContent(ctx context.Context, projectID string) ([]Skill, error) {
	skills, err := c.ListSkills(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for i, skill := range skills {
		detail, err := c.GetSkillDetail(ctx, projectID, skill.Handle, skill.Scope)
		if err != nil {
			return nil, err
		}
		skills[i] = detail
	}
	return skills, nil
}

// CreateSkill adds a skill. body is the skill markdown/front-matter document.
func (c *Client) CreateSkill(ctx context.Context, projectID, name, description, body string) error {
	payload := struct {
		Handle      string `json:"handle"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Scope       string `json:"scope"`
		Body        string `json:"body"`
	}{
		Handle:      name,
		Name:        name,
		Description: description,
		Scope:       "project",
		Body:        body,
	}
	return c.doJSON(ctx, http.MethodPost, "/skills"+query("project_id", projectID), payload)
}

// UpdateSkill replaces a skill's body while preserving its display metadata and enabled state.
func (c *Client) UpdateSkill(ctx context.Context, projectID, handle, scope, name, description string, enabled bool, body string) error {
	payload := struct {
		Handle      string `json:"handle"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Scope       string `json:"scope"`
		Body        string `json:"body"`
		Enabled     bool   `json:"enabled"`
	}{
		Handle:      handle,
		Name:        name,
		Description: description,
		Scope:       scope,
		Body:        body,
		Enabled:     enabled,
	}
	return c.doJSON(ctx, http.MethodPut, "/skills/"+url.PathEscape(handle)+query("project_id", projectID), payload)
}

// DeleteSkill removes a skill.
func (c *Client) DeleteSkill(ctx context.Context, projectID, handle, scope string) error {
	return c.doForm(ctx, http.MethodDelete,
		"/skills/"+url.PathEscape(handle)+query("project_id", projectID, "scope", scope), nil)
}

// SetSkillEnabled enables or disables a skill.
func (c *Client) SetSkillEnabled(ctx context.Context, projectID, handle, scope string, enabled bool) error {
	payload := struct {
		Enabled bool   `json:"enabled"`
		Scope   string `json:"scope"`
	}{
		Enabled: enabled,
		Scope:   scope,
	}
	return c.doJSON(ctx, http.MethodPost,
		"/skills/"+url.PathEscape(handle)+"/enabled"+query("project_id", projectID), payload)
}

// SetSkillAlwaysUse sets a skill's always-use flag.
func (c *Client) SetSkillAlwaysUse(ctx context.Context, projectID, handle, scope string, always bool) error {
	payload := struct {
		AlwaysUse bool   `json:"always_use"`
		Scope     string `json:"scope"`
	}{
		AlwaysUse: always,
		Scope:     scope,
	}
	return c.doJSON(ctx, http.MethodPost,
		"/skills/"+url.PathEscape(handle)+"/always_use"+query("project_id", projectID), payload)
}

// ModelCreateRequest is the supported terminal subset of the backend's
// browser model-create form. APIKey is deliberately excluded from every JSON
// representation and is only sent in the form body.
type ModelCreateRequest struct {
	Name          string
	Provider      string
	Model         string
	APIKey        string
	OAuth         bool
	OllamaBaseURL string
}

// ModelOAuthStatus is the backend-confirmed authorization state for an
// OAuth-configured model.
type ModelOAuthStatus struct {
	Status string `json:"status"`
}

// ModelEditDetails is the safe, authoritative state needed to edit an existing
// model configuration. Credentials and provider-specific secret values are held
// privately so callers cannot accidentally render or marshal them.
type ModelEditDetails struct {
	ID                    string
	Name                  string
	Provider              string
	Model                 string
	ReasoningEffort       string
	Temperature           float64
	IsDefault             bool
	AuthMethod            string
	MaxWorkers            int
	WorkerTimeout         int
	OllamaBaseURL         string
	BaseURL               string
	Transport             string
	PresetSlug            string
	ModelsURL             string
	AuthHeaderName        string
	AuthHeaderValuePrefix string
	AutoStartTasks        bool
	DefaultMaxTokens      int

	hasDefaultMaxTokens bool
	secrets             modelEditSecrets
}

// ModelEditRequest contains only the terminal-supported explicit edits. Nil
// fields leave the authoritative value untouched.
type ModelEditRequest struct {
	Name          *string
	Model         *string
	IsDefault     *bool
	MaxWorkers    *int
	WorkerTimeout *int
	Endpoint      *string
}

type modelEditDetailsPayload struct {
	ID                    string  `json:"id"`
	Name                  string  `json:"name"`
	Provider              string  `json:"provider"`
	Model                 string  `json:"model"`
	ReasoningEffort       string  `json:"reasoning_effort"`
	Temperature           float64 `json:"temperature"`
	IsDefault             bool    `json:"is_default"`
	APIKey                string  `json:"api_key"`
	AuthMethod            string  `json:"auth_method"`
	MaxWorkers            int     `json:"max_workers"`
	WorkerTimeout         int     `json:"worker_timeout"`
	OAuthClientID         string  `json:"oauth_client_id"`
	OAuthClientSecret     string  `json:"oauth_client_secret"`
	OAuthAuthorizeURL     string  `json:"oauth_authorize_url"`
	OAuthTokenURL         string  `json:"oauth_token_url"`
	OAuthScopes           string  `json:"oauth_scopes"`
	OllamaBaseURL         string  `json:"ollama_base_url"`
	BaseURL               string  `json:"base_url"`
	Transport             string  `json:"transport"`
	PresetSlug            string  `json:"preset_slug"`
	ModelsURL             string  `json:"models_url"`
	AuthHeaderName        string  `json:"auth_header_name"`
	AuthHeaderValuePrefix string  `json:"auth_header_value_prefix"`
	ExtraHeadersJSON      string  `json:"extra_headers_json"`
	ExtraBodyJSON         string  `json:"extra_body_json"`
	CustomAuthConfigJSON  string  `json:"custom_auth_config_json"`
	MixtureConfigJSON     string  `json:"mixture_config_json"`
	AutoStartTasks        bool    `json:"auto_start_tasks"`
	DefaultMaxTokens      *int    `json:"default_max_tokens"`
}

type modelEditSecrets struct {
	apiKey               string
	oauthClientID        string
	oauthClientSecret    string
	oauthAuthorizeURL    string
	oauthTokenURL        string
	oauthScopes          string
	extraHeadersJSON     string
	extraBodyJSON        string
	customAuthConfigJSON string
	mixtureConfigJSON    string
}

// modelCustomAuthConfig mirrors the backend's saved OpenAI-compatible
// authentication settings. It is deliberately private because several fields
// may contain credentials. The edit client needs it only to retain settings the
// browser form otherwise replaces wholesale.
type modelCustomAuthConfig struct {
	RefreshURL              string            `json:"refresh_url,omitempty"`
	PKCE                    bool              `json:"pkce"`
	TokenRequestFormat      string            `json:"token_request_format,omitempty"`
	AccessTokenField        string            `json:"access_token_field,omitempty"`
	RefreshTokenField       string            `json:"refresh_token_field,omitempty"`
	ExpiresInField          string            `json:"expires_in_field,omitempty"`
	AuthorizationMode       string            `json:"authorization_mode,omitempty"`
	AccessTokenHeader       string            `json:"access_token_header,omitempty"`
	AccessTokenPrefix       string            `json:"access_token_prefix,omitempty"`
	UserAgent               string            `json:"user_agent,omitempty"`
	StaticHeaders           map[string]string `json:"static_headers,omitempty"`
	ProfileURL              string            `json:"profile_url,omitempty"`
	ProfileInstancePath     string            `json:"profile_instance_path,omitempty"`
	ProfileTeamPath         string            `json:"profile_team_path,omitempty"`
	InstanceHeader          string            `json:"instance_header,omitempty"`
	TeamHeader              string            `json:"team_header,omitempty"`
	SigningSecret           string            `json:"signing_secret,omitempty"`
	TimestampHeader         string            `json:"timestamp_header,omitempty"`
	SignatureHeader         string            `json:"signature_header,omitempty"`
	ModelsArrayPath         string            `json:"models_array_path,omitempty"`
	ModelIDField            string            `json:"model_id_field,omitempty"`
	AuthorizationParameters map[string]string `json:"authorization_parameters,omitempty"`
	StandardTokenFields     bool              `json:"standard_token_fields"`
	CallbackParameter       string            `json:"callback_parameter,omitempty"`
	LocalCallbackHost       string            `json:"local_callback_host,omitempty"`
	LocalCallbackPath       string            `json:"local_callback_path,omitempty"`
	AllowPrivateEndpoints   bool              `json:"allow_private_endpoints"`
	TokenHeaders            map[string]string `json:"token_headers,omitempty"`
	RefreshRequestFormat    string            `json:"refresh_request_format,omitempty"`
	RefreshParameters       map[string]string `json:"refresh_parameters,omitempty"`
	RefreshHeaders          map[string]string `json:"refresh_headers,omitempty"`
	RefreshIncludeGrantType bool              `json:"refresh_include_grant_type"`
	RefreshIncludeClient    bool              `json:"refresh_include_client"`
}

// GetModelEditDetails reads the backend's authoritative edit state. The route
// is intentionally separate from model cards because cards omit credentials and
// provider-specific configuration.
func (c *Client) GetModelEditDetails(ctx context.Context, projectID, modelID string) (ModelEditDetails, error) {
	var payload modelEditDetailsPayload
	path := "/models/" + url.PathEscape(modelID) + "/edit-details" + query("project_id", projectID)
	if err := c.getJSON(ctx, path, &payload); err != nil {
		return ModelEditDetails{}, err
	}
	if strings.TrimSpace(payload.ID) == "" || payload.ID != modelID {
		return ModelEditDetails{}, fmt.Errorf("model edit details did not match requested model")
	}
	return ModelEditDetails{
		ID:                    payload.ID,
		Name:                  payload.Name,
		Provider:              payload.Provider,
		Model:                 payload.Model,
		ReasoningEffort:       payload.ReasoningEffort,
		Temperature:           payload.Temperature,
		IsDefault:             payload.IsDefault,
		AuthMethod:            payload.AuthMethod,
		MaxWorkers:            payload.MaxWorkers,
		WorkerTimeout:         payload.WorkerTimeout,
		OllamaBaseURL:         payload.OllamaBaseURL,
		BaseURL:               payload.BaseURL,
		Transport:             payload.Transport,
		PresetSlug:            payload.PresetSlug,
		ModelsURL:             payload.ModelsURL,
		AuthHeaderName:        payload.AuthHeaderName,
		AuthHeaderValuePrefix: payload.AuthHeaderValuePrefix,
		AutoStartTasks:        payload.AutoStartTasks,
		DefaultMaxTokens:      derefModelEditInt(payload.DefaultMaxTokens),
		hasDefaultMaxTokens:   payload.DefaultMaxTokens != nil,
		secrets: modelEditSecrets{
			apiKey:               payload.APIKey,
			oauthClientID:        payload.OAuthClientID,
			oauthClientSecret:    payload.OAuthClientSecret,
			oauthAuthorizeURL:    payload.OAuthAuthorizeURL,
			oauthTokenURL:        payload.OAuthTokenURL,
			oauthScopes:          payload.OAuthScopes,
			extraHeadersJSON:     payload.ExtraHeadersJSON,
			extraBodyJSON:        payload.ExtraBodyJSON,
			customAuthConfigJSON: payload.CustomAuthConfigJSON,
			mixtureConfigJSON:    payload.MixtureConfigJSON,
		},
	}, nil
}

func derefModelEditInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

// UpdateModel merges explicit terminal edits into authoritative details and
// submits the backend's browser-form update contract. apiKey is intentionally
// separate from ModelEditRequest so normal edit values cannot accidentally carry
// a credential into a printable request value.
func (c *Client) UpdateModel(ctx context.Context, projectID string, details ModelEditDetails, update ModelEditRequest, apiKey string) error {
	if strings.TrimSpace(details.ID) == "" {
		return errors.New("model ID is required")
	}
	if update.Name != nil {
		details.Name = strings.TrimSpace(*update.Name)
	}
	if update.Model != nil {
		details.Model = strings.TrimSpace(*update.Model)
	}
	if update.IsDefault != nil {
		details.IsDefault = *update.IsDefault
	}
	if update.MaxWorkers != nil {
		if *update.MaxWorkers < 0 {
			return errors.New("max workers must be nonnegative")
		}
		details.MaxWorkers = *update.MaxWorkers
	}
	if update.WorkerTimeout != nil {
		if *update.WorkerTimeout < 0 {
			return errors.New("worker timeout must be nonnegative")
		}
		details.WorkerTimeout = *update.WorkerTimeout
	}
	if update.Endpoint != nil {
		endpoint, err := validateModelEditEndpoint(*update.Endpoint)
		if err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(details.Provider)) {
		case "ollama":
			details.OllamaBaseURL = endpoint
		case "openai_compatible":
			details.BaseURL = endpoint
		default:
			return errors.New("--endpoint is supported only for Ollama or OpenAI-compatible models")
		}
	}
	name, err := validateModelEditName(details.Name)
	if err != nil {
		return err
	}
	details.Name = name
	provider := strings.ToLower(strings.TrimSpace(details.Provider))
	if provider == "openai" {
		model, err := validateOpenAIModelEdit(details.Model)
		if err != nil {
			return err
		}
		details.Model = model
	}
	if provider == "openai_compatible" && !details.hasDefaultMaxTokens {
		return errors.New("backend did not return default max tokens; cannot safely edit this OpenAI-compatible model")
	}
	if apiKey != "" && !modelAllowsAPIKeyReplacement(details) {
		return errors.New("API-key replacement is supported only for API-key model configurations")
	}

	form, err := modelEditForm(details, apiKey)
	if err != nil {
		return redactModelEditError(err, modelEditSensitiveValues(details, apiKey)...)
	}
	err = c.doForm(ctx, http.MethodPut, "/models/"+url.PathEscape(details.ID)+query("project_id", projectID), form)
	return redactModelEditError(err, modelEditSensitiveValues(details, apiKey)...)
}

func modelAllowsAPIKeyReplacement(details ModelEditDetails) bool {
	switch strings.ToLower(strings.TrimSpace(details.Provider)) {
	case "anthropic", "openai", "openai_compatible":
		return !strings.EqualFold(strings.TrimSpace(details.AuthMethod), "oauth")
	default:
		return false
	}
}

func validateModelEditName(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("model name is required")
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return "", errors.New("model name must not contain terminal control characters")
		}
	}
	return strings.TrimSpace(value), nil
}

func validateOpenAIModelEdit(value string) (string, error) {
	model := strings.TrimSpace(value)
	switch model {
	case "gpt-6-astra",
		"gpt-5.6-sol",
		"gpt-5.6-terra",
		"gpt-5.6-luna",
		"gpt-5.5",
		"gpt-5.5-pro",
		"gpt-5.4",
		"gpt-5.4-mini",
		"gpt-5.3-codex",
		"gpt-5.3-codex-spark",
		"gpt-5.2-codex",
		"gpt-5.1-codex-max",
		"gpt-5.1-codex",
		"gpt-5.1-codex-mini",
		"gpt-5-codex",
		"gpt-5-codex-mini":
		return model, nil
	default:
		return "", errors.New("unsupported OpenAI model")
	}
}

func validateModelEditEndpoint(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("--endpoint must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("--endpoint must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	return value, nil
}

func modelEditForm(details ModelEditDetails, apiKey string) (url.Values, error) {
	form := url.Values{}
	form.Set("name", details.Name)
	form.Set("provider", details.Provider)
	form.Set("model", details.Model)
	form.Set("reasoning_effort", details.ReasoningEffort)
	form.Set("temperature", strconv.FormatFloat(details.Temperature, 'f', -1, 64))
	form.Set("model_max_workers", strconv.Itoa(details.MaxWorkers))
	form.Set("worker_timeout", strconv.Itoa(details.WorkerTimeout))
	if details.IsDefault {
		form.Set("is_default", "on")
	}
	if details.AutoStartTasks {
		form.Set("auto_start_tasks", "on")
	}

	provider := strings.ToLower(strings.TrimSpace(details.Provider))
	oauth := strings.EqualFold(strings.TrimSpace(details.AuthMethod), "oauth")
	switch provider {
	case "anthropic":
		if oauth {
			form.Set("anthropic_auth_type", "oauth")
			form.Set("auth_method", "oauth")
		} else {
			form.Set("anthropic_auth_type", "api_key")
		}
	case "openai":
		if oauth {
			form.Set("openai_auth_type", "oauth")
			form.Set("auth_method", "oauth")
		} else {
			form.Set("openai_auth_type", "api_key")
		}
	case "ollama":
		form.Set("ollama_base_url", details.OllamaBaseURL)
	case "mixture":
		form.Set("mixture_config_json", details.secrets.mixtureConfigJSON)
	case "openai_compatible":
		form.Set("base_url", details.BaseURL)
		form.Set("transport", details.Transport)
		form.Set("preset_slug", details.PresetSlug)
		form.Set("models_url", details.ModelsURL)
		form.Set("auth_header_name", details.AuthHeaderName)
		form.Set("auth_header_value_prefix", details.AuthHeaderValuePrefix)
		form.Set("default_max_tokens", strconv.Itoa(details.DefaultMaxTokens))
		if oauth {
			form.Set("custom_auth_method", "oauth")
			form.Set("auth_method", "oauth")
		}
		if err := appendModelCustomAuthForm(form, details.secrets.customAuthConfigJSON); err != nil {
			return nil, err
		}
	}
	if apiKey != "" {
		form.Set("api_key", apiKey)
	}
	return form, nil
}

func appendModelCustomAuthForm(form url.Values, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var cfg modelCustomAuthConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return fmt.Errorf("invalid saved provider-specific authentication configuration: %w", err)
	}
	form.Set("custom_refresh_url", cfg.RefreshURL)
	form.Set("custom_token_request_format", cfg.TokenRequestFormat)
	form.Set("custom_access_token_field", cfg.AccessTokenField)
	form.Set("custom_refresh_token_field", cfg.RefreshTokenField)
	form.Set("custom_expires_in_field", cfg.ExpiresInField)
	form.Set("custom_authorization_mode", cfg.AuthorizationMode)
	form.Set("custom_access_token_header", cfg.AccessTokenHeader)
	form.Set("custom_access_token_prefix", cfg.AccessTokenPrefix)
	form.Set("custom_user_agent", cfg.UserAgent)
	form.Set("custom_profile_url", cfg.ProfileURL)
	form.Set("custom_profile_instance_path", cfg.ProfileInstancePath)
	form.Set("custom_profile_team_path", cfg.ProfileTeamPath)
	form.Set("custom_instance_header", cfg.InstanceHeader)
	form.Set("custom_team_header", cfg.TeamHeader)
	form.Set("custom_signing_secret", cfg.SigningSecret)
	form.Set("custom_timestamp_header", cfg.TimestampHeader)
	form.Set("custom_signature_header", cfg.SignatureHeader)
	form.Set("custom_models_array_path", cfg.ModelsArrayPath)
	form.Set("custom_model_id_field", cfg.ModelIDField)
	form.Set("custom_callback_parameter", cfg.CallbackParameter)
	form.Set("custom_local_callback_host", cfg.LocalCallbackHost)
	form.Set("custom_local_callback_path", cfg.LocalCallbackPath)
	form.Set("custom_refresh_request_format", cfg.RefreshRequestFormat)
	if cfg.PKCE {
		form.Set("custom_oauth_pkce", "on")
	}
	if cfg.StandardTokenFields {
		form.Set("custom_standard_token_fields", "on")
	}
	if cfg.AllowPrivateEndpoints {
		form.Set("custom_allow_private_endpoints", "on")
	}
	if cfg.RefreshIncludeGrantType {
		form.Set("custom_refresh_include_grant_type", "on")
	}
	if cfg.RefreshIncludeClient {
		form.Set("custom_refresh_include_client", "on")
	}
	for _, item := range []struct {
		name  string
		value map[string]string
	}{
		{"custom_static_headers_json", cfg.StaticHeaders},
		{"custom_authorization_parameters_json", cfg.AuthorizationParameters},
		{"custom_token_headers_json", cfg.TokenHeaders},
		{"custom_refresh_parameters_json", cfg.RefreshParameters},
		{"custom_refresh_headers_json", cfg.RefreshHeaders},
	} {
		if item.value == nil {
			continue
		}
		encoded, err := json.Marshal(item.value)
		if err != nil {
			return fmt.Errorf("encoding saved provider-specific authentication configuration: %w", err)
		}
		form.Set(item.name, string(encoded))
	}
	return nil
}

func modelEditSensitiveValues(details ModelEditDetails, apiKey string) []string {
	values := []string{
		apiKey,
		details.secrets.apiKey,
		details.secrets.oauthClientID,
		details.secrets.oauthClientSecret,
		details.secrets.oauthAuthorizeURL,
		details.secrets.oauthTokenURL,
		details.secrets.oauthScopes,
		details.secrets.extraHeadersJSON,
		details.secrets.extraBodyJSON,
		details.secrets.customAuthConfigJSON,
		details.secrets.mixtureConfigJSON,
	}
	var cfg modelCustomAuthConfig
	if json.Unmarshal([]byte(details.secrets.customAuthConfigJSON), &cfg) == nil {
		values = append(values, cfg.SigningSecret)
		for _, group := range []map[string]string{cfg.StaticHeaders, cfg.AuthorizationParameters, cfg.TokenHeaders, cfg.RefreshParameters, cfg.RefreshHeaders} {
			for _, value := range group {
				values = append(values, value)
			}
		}
	}
	return values
}

// CreateModel creates a model through the same form contract used by the
// backend Models screen. Server-side normalization and validation remain
// authoritative.
func (c *Client) CreateModel(ctx context.Context, projectID string, request ModelCreateRequest) error {
	form := url.Values{}
	form.Set("name", request.Name)
	form.Set("provider", request.Provider)
	form.Set("model", request.Model)
	switch request.Provider {
	case "anthropic":
		if request.OAuth {
			form.Set("anthropic_auth_type", "oauth")
			form.Set("auth_method", "oauth")
		} else {
			form.Set("anthropic_auth_type", "api_key")
			form.Set("api_key", request.APIKey)
		}
	case "openai":
		if request.OAuth {
			form.Set("openai_auth_type", "oauth")
			form.Set("auth_method", "oauth")
		} else {
			form.Set("openai_auth_type", "api_key")
			form.Set("api_key", request.APIKey)
		}
	case "ollama":
		if request.OllamaBaseURL != "" {
			form.Set("ollama_base_url", request.OllamaBaseURL)
		}
	}
	err := c.doForm(ctx, http.MethodPost, "/models"+query("project_id", projectID), form)
	return redactModelCreateError(err, request.APIKey)
}

// GetModelOAuthStatus returns the backend-confirmed OAuth status. A model is
// connected only when this endpoint reports "connected".
func (c *Client) GetModelOAuthStatus(ctx context.Context, modelID string) (*ModelOAuthStatus, error) {
	var out ModelOAuthStatus
	if err := c.getJSON(ctx, "/models/"+url.PathEscape(modelID)+"/oauth/status", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// redactModelCreateError ensures an unexpected backend validation response can
// never reflect a submitted API key into terminal output.
func redactModelCreateError(err error, secret string) error {
	return redactModelMutationError(err, secret)
}

func redactModelEditError(err error, secrets ...string) error {
	return redactModelMutationError(err, secrets...)
}

func redactModelMutationError(err error, secrets ...string) error {
	if err == nil || IsAuthRequired(err) {
		return err
	}
	message := err.Error()
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		for _, value := range []string{secret, url.QueryEscape(secret)} {
			if value != "" {
				message = strings.ReplaceAll(message, value, "[redacted]")
			}
		}
	}
	if message == err.Error() {
		return err
	}
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) {
		redacted := statusErr.Message
		for _, secret := range secrets {
			if secret == "" {
				continue
			}
			for _, value := range []string{secret, url.QueryEscape(secret)} {
				if value != "" {
					redacted = strings.ReplaceAll(redacted, value, "[redacted]")
				}
			}
		}
		return &HTTPStatusError{StatusCode: statusErr.StatusCode, Message: redacted}
	}
	return errors.New(message)
}

// --- models ---

// LLMModel is one card on the Models screen.
type LLMModel struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Text     string `json:"text"`
}

// ListModels scrapes the models screen.
func (c *Client) ListModels(ctx context.Context, projectID string) ([]LLMModel, error) {
	pages, err := c.getCardPages(ctx, "/models"+query("project_id", projectID))
	if err != nil {
		return nil, err
	}
	cards := make([]Card, 0)
	for _, page := range pages {
		cards = append(cards, dedupedCards(page.root, "data-model-id")...)
	}
	seen := make(map[string]bool)
	out := make([]LLMModel, 0, len(cards))
	for _, card := range cards {
		if card.Get("model-name") == "" || seen[card.Get("model-id")] {
			continue
		}
		seen[card.Get("model-id")] = true
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
	ID          string `json:"id"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Model       string `json:"model"`
	Scope       string `json:"scope"`
}

// AgentDefinition is the authoritative agent edit representation returned by
// GET /agents/:id/json. Fields not exposed by the terminal editor remain in
// this value and are submitted unchanged by UpdateAgent.
type AgentDefinition struct {
	ID                  string                  `json:"id"`
	Name                string                  `json:"name"`
	Description         string                  `json:"description"`
	SystemPrompt        string                  `json:"system_prompt"`
	Model               string                  `json:"model"`
	Tools               []string                `json:"tools"`
	ToolConfig          AgentToolConfig         `json:"tool_config"`
	Plugins             []string                `json:"plugins"`
	MCPServers          []MCPServerConfig       `json:"mcp_servers"`
	SystemKind          string                  `json:"system_kind,omitempty"`
	Skills              []AgentSkillConfig      `json:"skills"`
	Key                 string                  `json:"key,omitempty"`
	Scope               string                  `json:"scope,omitempty"`
	ProjectID           string                  `json:"project_id,omitempty"`
	SelectableAsPrimary bool                    `json:"selectable_as_primary"`
	Enabled             bool                    `json:"enabled"`
	PermissionDefaults  AgentPermissionDefaults `json:"permission_defaults,omitempty"`
	ModelDefaults       AgentModelDefaults      `json:"model_defaults,omitempty"`
	CreatedBy           string                  `json:"created_by,omitempty"`
	GeneratedStatus     string                  `json:"generated_status,omitempty"`
	AbsorbedInto        string                  `json:"absorbed_into,omitempty"`
	SourceRefs          []string                `json:"source_refs,omitempty"`
	ArchivedAt          *time.Time              `json:"archived_at,omitempty"`
	CreatedAt           time.Time               `json:"created_at"`
	UpdatedAt           time.Time               `json:"updated_at"`
	LifecycleHooks      []AgentLifecycleHook    `json:"lifecycle_hooks,omitempty"`
}

type MCPServerConfig struct {
	Name    string            `json:"name"`
	Type    string            `json:"type,omitempty"`
	Command []string          `json:"command,omitempty"`
	URL     string            `json:"url,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type AgentSkillConfig struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Tools       string `json:"tools,omitempty"`
	Content     string `json:"content"`
}

type ScopedFilesConfig struct {
	Directory   string   `json:"directory"`
	Permissions []string `json:"permissions"`
}

type AgentToolConfig struct {
	ScopedFiles            []ScopedFilesConfig `json:"scoped_files,omitempty"`
	SkipDefaultTools       bool                `json:"skip_default_tools,omitempty"`
	DisableRuntimeWorktree bool                `json:"disable_runtime_worktree,omitempty"`
}

type AgentPermissionDefaults struct {
	ReadTaskPrompt       bool `json:"read_task_prompt,omitempty"`
	ReadTaskExecution    bool `json:"read_task_execution,omitempty"`
	ReadProjectMemory    bool `json:"read_project_memory,omitempty"`
	WriteProjectMemory   bool `json:"write_project_memory,omitempty"`
	ReadAgents           bool `json:"read_agents,omitempty"`
	WriteAgents          bool `json:"write_agents,omitempty"`
	ReadSkills           bool `json:"read_skills,omitempty"`
	WriteSkills          bool `json:"write_skills,omitempty"`
	ReadRepositoryFiles  bool `json:"read_repository_files,omitempty"`
	WriteRepositoryFiles bool `json:"write_repository_files,omitempty"`
	UseShellOrTools      bool `json:"use_shell_or_tools,omitempty"`
}

type AgentModelDefaults struct {
	Model       string  `json:"model,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
	MaxTokens   int     `json:"max_tokens,omitempty"`
}

type AgentLifecycleHook struct {
	ID              string `json:"id,omitempty"`
	AgentID         string `json:"agent_id,omitempty"`
	When            string `json:"when"`
	SkillKey        string `json:"skill_key"`
	PromptOverride  string `json:"prompt_override,omitempty"`
	OutputContract  string `json:"output_contract,omitempty"`
	Blocking        bool   `json:"blocking"`
	Enabled         bool   `json:"enabled"`
	PermissionsJSON string `json:"permissions_json,omitempty"`
	RunPolicyJSON   string `json:"run_policy_json,omitempty"`
	ScheduleJSON    string `json:"schedule_json,omitempty"`
	PayloadJSON     string `json:"payload_json,omitempty"`
}

// ListAgents scrapes the agents screen.
func (c *Client) ListAgents(ctx context.Context, projectID string) ([]AgentDef, error) {
	pages, err := c.getCardPages(ctx, "/agents"+query("project_id", projectID))
	if err != nil {
		return nil, err
	}
	cards := make([]Card, 0)
	for _, page := range pages {
		cards = append(cards, dedupedCardsWithoutText(page.root, "data-agent-id")...)
	}
	seen := make(map[string]bool)
	out := make([]AgentDef, 0, len(cards))
	for _, card := range cards {
		if card.Get("agent-name") == "" || seen[card.Get("agent-id")] {
			continue
		}
		seen[card.Get("agent-id")] = true
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

// GetAgent loads the authoritative definition and lifecycle associations for an
// edit. Both reads are explicitly scoped to the selected project.
func (c *Client) GetAgent(ctx context.Context, projectID, agentID string) (AgentDefinition, error) {
	path := "/agents/" + url.PathEscape(agentID)
	var agent AgentDefinition
	if err := c.getJSON(ctx, path+"/json"+query("project_id", projectID), &agent); err != nil {
		return AgentDefinition{}, err
	}
	if strings.EqualFold(agent.Scope, "project") && strings.TrimSpace(agent.ProjectID) != "" && agent.ProjectID != projectID {
		return AgentDefinition{}, fmt.Errorf("agent %q belongs to a different project", firstNonEmptyString(agent.Name, agent.ID))
	}
	if strings.EqualFold(agent.GeneratedStatus, "protected") {
		return AgentDefinition{}, fmt.Errorf("protected system agent %q is read-only", firstNonEmptyString(agent.Name, agent.ID))
	}
	var hooks []AgentLifecycleHook
	if err := c.getJSON(ctx, path+"/lifecycle-hooks"+query("project_id", projectID), &hooks); err != nil {
		return AgentDefinition{}, fmt.Errorf("loading agent lifecycle hooks: %w", err)
	}
	if hooks == nil {
		hooks = []AgentLifecycleHook{}
	}
	agent.LifecycleHooks = hooks
	return agent, nil
}

// UpdateAgent submits the backend dialog's replacement contract for agent
// fields. Values that are false, empty, or absent in the authoritative
// definition are encoded explicitly so an edit cannot reset unrelated
// configuration. Lifecycle hooks are intentionally omitted: the backend PUT
// preserves them when absent, while its form decoder cannot round-trip every
// field returned by the lifecycle endpoint.
func (c *Client) UpdateAgent(ctx context.Context, projectID string, agent AgentDefinition) error {
	if strings.EqualFold(agent.Scope, "project") && strings.TrimSpace(agent.ProjectID) != "" && agent.ProjectID != projectID {
		return fmt.Errorf("agent %q belongs to a different project", firstNonEmptyString(agent.Name, agent.ID))
	}
	if strings.EqualFold(agent.GeneratedStatus, "protected") {
		return fmt.Errorf("protected system agent %q is read-only", firstNonEmptyString(agent.Name, agent.ID))
	}
	marshal := func(name string, value any) (string, error) {
		data, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("encoding agent %s: %w", name, err)
		}
		return string(data), nil
	}
	form := url.Values{}
	form.Set("name", agent.Name)
	form.Set("description", agent.Description)
	form.Set("system_prompt", agent.SystemPrompt)
	form.Set("model", agent.Model)
	form.Set("key", agent.Key)
	form.Set("scope", agent.Scope)
	form.Set("project_id", agent.ProjectID)
	form.Set("selectable_as_primary", strconv.FormatBool(agent.SelectableAsPrimary))
	form.Set("enabled", strconv.FormatBool(agent.Enabled))
	values := []struct {
		name  string
		value any
	}{
		{"tools_json", agent.Tools},
		{"tool_config_json", agent.ToolConfig},
		{"plugins_json", agent.Plugins},
		{"skills_json", agent.Skills},
		{"mcp_servers_json", agent.MCPServers},
		{"permission_defaults_json", agent.PermissionDefaults},
		{"source_refs_json", agent.SourceRefs},
	}
	for _, value := range values {
		encoded, err := marshal(value.name, value.value)
		if err != nil {
			return err
		}
		form.Set(value.name, encoded)
	}
	return c.doForm(ctx, http.MethodPut, "/agents/"+url.PathEscape(agent.ID)+query("project_id", projectID), form)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "agent"
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
	TaskID     string `json:"task_id"`
	ScheduleID string `json:"schedule_id"`
	Name       string `json:"-"`
	Text       string `json:"text"`
}

// ScheduleConfig is the editable state of one existing schedule.
type ScheduleConfig struct {
	ID                  string `json:"id"`
	TaskID              string `json:"task_id"`
	ProjectID           string `json:"project_id"`
	RunAt               string `json:"run_at"`
	RepeatType          string `json:"repeat_type"`
	RepeatInterval      int    `json:"repeat_interval"`
	ClearContextOnStart bool   `json:"clear_context_on_start"`
}

// ScheduleUpdate contains optional changes to an existing schedule.
type ScheduleUpdate struct {
	RunAt               *string
	RepeatType          *string
	RepeatInterval      *int
	ClearContextOnStart *bool
}

func scheduleCardName(node *html.Node) string {
	title := findNode(node, func(n *html.Node) bool {
		if n.Type != html.ElementNode {
			return false
		}
		for _, class := range strings.Fields(attr(n, "class")) {
			if class == "font-semibold" {
				return true
			}
		}
		return false
	})
	if title == nil {
		return ""
	}
	return strings.TrimSpace(NodeText(title))
}

// GetSchedule scrapes the Schedule screen for a project.
func (c *Client) GetSchedule(ctx context.Context, projectID string) ([]ScheduleEntry, string, error) {
	root, err := c.getHTML(ctx, "/schedule"+query("project_id", projectID))
	if err != nil {
		return nil, "", err
	}
	cards := dedupeScrapedCards(scrapeCardNodes(root, "data-schedule-id"), "data-schedule-id")
	out := make([]ScheduleEntry, 0, len(cards))
	for _, card := range cards {
		text := strings.TrimSpace(cardNodeText(card.node))
		name := scheduleCardName(card.node)
		if name == "" {
			name = text
		}
		out = append(out, ScheduleEntry{
			TaskID:     card.attrs["data-task-id"],
			ScheduleID: card.attrs["data-schedule-id"],
			Name:       name,
			Text:       text,
		})
	}
	summary := ""
	if n := findByID(root, "schedule-content"); n != nil {
		summary = NodeText(n)
	}
	return out, summary, nil
}

// NormalizeScheduleRepeat converts the user-facing "hourly" schedule alias
// to the backend's "hours" repeat type. Other repeat values are unchanged.
func NormalizeScheduleRepeat(repeat string) string {
	if repeat == "hourly" {
		return "hours"
	}
	return repeat
}

func validScheduleRepeat(repeat string) bool {
	switch NormalizeScheduleRepeat(strings.ToLower(repeat)) {
	case "once", "daily", "weekly", "monthly", "seconds", "minutes", "hours":
		return true
	default:
		return false
	}
}

// GetTaskSchedule loads the current editable values for one schedule from its
// task's project-scoped schedule tab.
func (c *Client) GetTaskSchedule(ctx context.Context, projectID, taskID, scheduleID string) (ScheduleConfig, error) {
	root, err := c.getHTML(ctx, "/tasks/"+url.PathEscape(taskID)+query("tab", "schedules", "project_id", projectID))
	if err != nil {
		return ScheduleConfig{}, err
	}
	containers := findAll(root, func(n *html.Node) bool {
		return attr(n, "data-schedule-id") == scheduleID
	})
	var form *html.Node
	for _, container := range containers {
		form = findNode(container, func(n *html.Node) bool {
			return n.Type == html.ElementNode && n.Data == "form" && (attr(n, "hx-put") != "" || attr(n, "action") != "")
		})
		if form != nil {
			break
		}
	}
	if form == nil {
		return ScheduleConfig{}, fmt.Errorf("schedule %q was not found in task %q", scheduleID, taskID)
	}
	updateTarget := attr(form, "hx-put")
	if updateTarget == "" {
		updateTarget = attr(form, "action")
	}
	updateURL, err := url.Parse(updateTarget)
	if err != nil {
		return ScheduleConfig{}, fmt.Errorf("invalid schedule update form: %w", err)
	}
	if got := updateURL.Query().Get("project_id"); got != projectID {
		return ScheduleConfig{}, fmt.Errorf("schedule %q belongs to project %q, not selected project %q", scheduleID, got, projectID)
	}
	if updateURL.Path != "/schedules/"+scheduleID {
		return ScheduleConfig{}, fmt.Errorf("schedule update form identity does not match %q", scheduleID)
	}
	inputValue := func(name string) string {
		n := findNode(form, func(n *html.Node) bool { return attr(n, "name") == name && attr(n, "type") != "hidden" })
		return attr(n, "value")
	}
	selectValue := func(name string) string {
		selectNode := findNode(form, func(n *html.Node) bool { return n.Data == "select" && attr(n, "name") == name })
		if selectNode == nil {
			return ""
		}
		selected := findNode(selectNode, func(n *html.Node) bool { return n.Data == "option" && hasHTMLAttr(n, "selected") })
		return attr(selected, "value")
	}
	interval, err := strconv.Atoi(inputValue("repeat_interval"))
	if err != nil {
		return ScheduleConfig{}, fmt.Errorf("invalid schedule repeat interval")
	}
	checkedClear := findNode(form, func(n *html.Node) bool {
		return attr(n, "name") == "clear_context_on_start" && attr(n, "type") == "checkbox" && hasHTMLAttr(n, "checked")
	}) != nil
	config := ScheduleConfig{ID: scheduleID, TaskID: taskID, ProjectID: projectID, RunAt: inputValue("run_at"), RepeatType: selectValue("repeat_type"), RepeatInterval: interval, ClearContextOnStart: checkedClear}
	if err := validateScheduleValues(config.RunAt, config.RepeatType, config.RepeatInterval); err != nil {
		return ScheduleConfig{}, fmt.Errorf("invalid existing schedule: %w", err)
	}
	return config, nil
}

func validateScheduleValues(runAt, repeat string, interval int) error {
	if _, err := time.Parse("2006-01-02T15:04", runAt); err != nil {
		return fmt.Errorf("run time must use 2006-01-02T15:04")
	}
	if !validScheduleRepeat(repeat) {
		return fmt.Errorf("unknown repeat type %q", repeat)
	}
	if interval < 1 || interval > 365 {
		return fmt.Errorf("repeat interval must be between 1 and 365")
	}
	return nil
}

// UpdateSchedule applies optional changes while retaining all omitted values.
func (c *Client) UpdateSchedule(ctx context.Context, current ScheduleConfig, update ScheduleUpdate) error {
	runAt, repeat, interval := current.RunAt, current.RepeatType, current.RepeatInterval
	if update.RunAt != nil {
		runAt = *update.RunAt
	}
	if update.RepeatType != nil {
		repeat = NormalizeScheduleRepeat(strings.ToLower(*update.RepeatType))
	}
	if update.RepeatInterval != nil {
		interval = *update.RepeatInterval
	}
	if err := validateScheduleValues(runAt, repeat, interval); err != nil {
		return err
	}
	if current.ID == "" || current.ProjectID == "" {
		return fmt.Errorf("schedule and project IDs are required")
	}
	v := url.Values{}
	v.Set("run_at", runAt)
	v.Set("repeat_type", repeat)
	v.Set("repeat_interval", strconv.Itoa(interval))
	clearContextOnStart := current.ClearContextOnStart
	if update.ClearContextOnStart != nil {
		clearContextOnStart = *update.ClearContextOnStart
	}
	v.Set("clear_context_on_start", strconv.FormatBool(clearContextOnStart))
	return c.doForm(ctx, http.MethodPut, "/schedules/"+url.PathEscape(current.ID)+query("project_id", current.ProjectID), v)
}

// CreateSchedule schedules a task. repeat is
// once/daily/weekly/monthly/seconds/minutes/hours/hourly. The user-facing
// "hourly" keyword is translated to the backend's "hours" repeat_type, since
// the backend has no "hourly" value.
func (c *Client) CreateSchedule(ctx context.Context, projectID, taskID, runAt, repeat string, interval int) error {
	if interval < 1 || interval > 365 {
		return fmt.Errorf("repeat interval must be between 1 and 365")
	}
	repeat = NormalizeScheduleRepeat(repeat)
	v := url.Values{}
	v.Set("run_at", runAt)
	v.Set("repeat_type", repeat)
	v.Set("repeat_interval", strconv.Itoa(interval))
	return c.doForm(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/schedule"+query("project_id", projectID), v)
}

// DeleteSchedule removes a schedule.
func (c *Client) DeleteSchedule(ctx context.Context, projectID, scheduleID string) error {
	return c.doForm(ctx, http.MethodDelete, "/schedules/"+url.PathEscape(scheduleID)+query("project_id", projectID), nil)
}

// --- workers ---

// GetWorkerSettings returns the Workers screen as text.
func (c *Client) GetWorkerSettings(ctx context.Context, projectID string) (string, error) {
	return c.pageText(ctx, "/workers"+query("project_id", projectID), "")
}

// setWorkerLimit posts a max_workers value to a worker-limit endpoint.
func (c *Client) setWorkerLimit(ctx context.Context, path string, limit int) error {
	v := url.Values{}
	v.Set("max_workers", strconv.Itoa(limit))
	return c.doForm(ctx, http.MethodPost, path, v)
}

// SetGlobalWorkerLimit updates the global max worker count.
func (c *Client) SetGlobalWorkerLimit(ctx context.Context, limit int) error {
	return c.setWorkerLimit(ctx, "/workers", limit)
}

// SetProjectWorkerLimit updates one project's worker limit.
func (c *Client) SetProjectWorkerLimit(ctx context.Context, projectID string, limit int) error {
	return c.setWorkerLimit(ctx, "/workers/projects/"+url.PathEscape(projectID)+"/limit", limit)
}

// --- channels & personality ---

// Channel is one project-scoped integration from the Channels screen. Only
// terminal-safe identity, state, and operational metadata are exported. Secret
// values scraped for backend-required preservation are kept in unexported fields.
type Channel struct {
	Type       string `json:"type"`
	Name       string `json:"name"`
	Configured bool   `json:"configured"`
	Connected  bool   `json:"connected"`
	Running    bool   `json:"running"`
	Status     string `json:"status"`
	Address    string `json:"address,omitempty"`
	Provider   string `json:"provider,omitempty"`

	configuration url.Values
}

// KnownChannels is the fixed set of backend-supported channel integrations.
var KnownChannels = []Channel{
	{Type: "github", Name: "GitHub"},
	{Type: "slack", Name: "Slack"},
	{Type: "telegram", Name: "Telegram Bot"},
	{Type: "discord", Name: "Discord"},
	{Type: "x", Name: "X (formerly Twitter)"},
	{Type: "email", Name: "Email"},
}

func knownChannel(channelType string) (Channel, bool) {
	for _, channel := range KnownChannels {
		if channel.Type == strings.ToLower(strings.TrimSpace(channelType)) {
			return channel, true
		}
	}
	return Channel{}, false
}

func channelFormValue(root *html.Node, name string) string {
	n := findNode(root, func(n *html.Node) bool {
		return n.Type == html.ElementNode && attr(n, "name") == name
	})
	if n == nil {
		return ""
	}
	if n.Data == "select" {
		selected := findNode(n, func(option *html.Node) bool {
			return option.Type == html.ElementNode && option.Data == "option" && hasHTMLAttr(option, "selected")
		})
		if selected != nil {
			return attr(selected, "value")
		}
	}
	if n.Data == "textarea" {
		return strings.TrimSpace(NodeText(n))
	}
	return strings.TrimSpace(attr(n, "value"))
}

func channelFormChecked(root *html.Node, name string) (bool, bool) {
	n := findNode(root, func(n *html.Node) bool {
		return n.Type == html.ElementNode && attr(n, "name") == name
	})
	if n == nil {
		return false, false
	}
	return hasHTMLAttr(n, "checked"), true
}

var channelFormFields = map[string][]string{
	"github":   {"github_auth_mode", "github_pat", "github_app_id", "github_app_slug", "github_app_private_key", "github_api_endpoint"},
	"slack":    {"slack_client_id", "slack_client_secret", "slack_app_token", "slack_bot_token_mode", "slack_bot_token", "slack_send_responses"},
	"telegram": {"telegram_rich_messages_v2"},
	"discord":  {"discord_bot_token", "discord_send_responses"},
	"x":        {"x_poll_interval_seconds", "x_send_responses"},
	"email":    {"email_provider", "email_address", "email_password", "email_imap_host", "email_imap_port", "email_smtp_host", "email_smtp_port", "email_poll_interval_seconds", "email_send_responses", "email_skip_attachments", "email_mark_existing_seen_on_start"},
}

var channelBooleanFormFields = map[string]bool{
	"slack_send_responses": true, "telegram_rich_messages_v2": true,
	"discord_send_responses": true, "x_send_responses": true, "email_send_responses": true,
	"email_skip_attachments": true, "email_mark_existing_seen_on_start": true,
}

func channelConfiguration(root *html.Node, channelType string, card Card) url.Values {
	values := make(url.Values)
	for _, name := range channelFormFields[channelType] {
		if channelBooleanFormFields[name] {
			if checked, found := channelFormChecked(root, name); found {
				values.Set(name, strconv.FormatBool(checked))
			}
			continue
		}
		if value := channelFormValue(root, name); value != "" {
			values.Set(name, value)
		}
	}
	if channelType == "telegram" {
		values.Set("token", card.Get("channel-token"))
	}
	return values
}

func cloneChannelConfiguration(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, entries := range values {
		cloned[key] = append([]string(nil), entries...)
	}
	return cloned
}

var channelEditableFormFields = map[string]bool{
	"github_auth_mode": true, "github_app_id": true, "github_app_slug": true, "github_api_endpoint": true,
	"slack_client_id": true, "slack_bot_token_mode": true, "slack_send_responses": true,
	"telegram_rich_messages_v2": true, "discord_send_responses": true,
	"x_poll_interval_seconds": true, "x_send_responses": true,
	"email_provider": true, "email_address": true, "email_imap_host": true, "email_imap_port": true,
	"email_smtp_host": true, "email_smtp_port": true, "email_poll_interval_seconds": true,
	"email_send_responses": true, "email_skip_attachments": true, "email_mark_existing_seen_on_start": true,
}

// EditableSettings returns only allowlisted non-secret configuration values
// suitable for interactive defaults and transition comparison. Credential
// values remain private to the client.
func (c Channel) EditableSettings() url.Values {
	settings := make(url.Values)
	for key, entries := range c.configuration {
		if !channelEditableFormFields[key] {
			continue
		}
		settings[key] = append([]string(nil), entries...)
	}
	return settings
}

func channelBadgeStatus(root *html.Node, channelType string) string {
	card := findNode(root, func(n *html.Node) bool {
		return n.Type == html.ElementNode && attr(n, "data-channel-type") == channelType
	})
	if card == nil {
		return ""
	}
	badges := findAll(card, func(n *html.Node) bool {
		if n.Type != html.ElementNode {
			return false
		}
		for _, class := range strings.Fields(attr(n, "class")) {
			if class == "badge" {
				return true
			}
		}
		return false
	})
	for _, badge := range badges {
		switch strings.ToLower(strings.TrimSpace(NodeText(badge))) {
		case "connected":
			return "connected"
		case "running", "gateway running":
			return "running"
		case "configured":
			return "configured"
		case "gateway offline", "configured, polling offline", "not running":
			return "configured, offline"
		case "not configured":
			return "not configured"
		case "not connected":
			return "not connected"
		}
	}
	return ""
}

func channelCardStatus(root *html.Node, card Card, channelType, displayName string) string {
	if status := channelBadgeStatus(root, channelType); status != "" {
		return status
	}
	status := strings.ToLower(strings.TrimSpace(card.Get("search-text")))
	if strings.HasPrefix(status, strings.ToLower(displayName)) {
		status = strings.TrimSpace(status[len(displayName):])
	}
	status += "\n" + strings.ToLower(card.Text)
	switch {
	case strings.Contains(status, "not configured"):
		return "not configured"
	case strings.Contains(status, "not connected"):
		return "not connected"
	case strings.Contains(status, "gateway offline") || strings.Contains(status, "not running"):
		return "configured, offline"
	case strings.Contains(status, "connected"):
		return "connected"
	case strings.Contains(status, "gateway running") || strings.Contains(status, "running"):
		return "running"
	case strings.Contains(status, "configured") && strings.Contains(status, "offline"):
		return "configured, offline"
	case strings.Contains(status, "configured"):
		return "configured"
	default:
		return "unknown"
	}
}

// ListChannels parses only stable channel cards and allowlisted metadata. It
// deliberately does not return the page text, controls, backend errors, or any
// credential-bearing form values.
func (c *Client) ListChannels(ctx context.Context, projectID string) ([]Channel, error) {
	root, err := c.getHTML(ctx, "/channels"+query("project_id", projectID))
	if err != nil {
		return nil, safeChannelError(err)
	}
	cards := scrapeCards(root, "data-channel-type")
	out := make([]Channel, 0, len(KnownChannels))
	seen := make(map[string]bool)
	for _, card := range cards {
		base, ok := knownChannel(card.Get("channel-type"))
		if !ok || seen[base.Type] {
			continue
		}
		seen[base.Type] = true
		base.Status = channelCardStatus(root, card, base.Type, base.Name)
		base.configuration = channelConfiguration(root, base.Type, card)
		lowerStatus := strings.ToLower(base.Status)
		base.Connected = strings.Contains(lowerStatus, "connected") && !strings.Contains(lowerStatus, "not connected")
		base.Running = card.Bool("channel-running") || strings.Contains(lowerStatus, "running")
		base.Configured = base.Connected || base.Running || (strings.Contains(lowerStatus, "configured") && !strings.Contains(lowerStatus, "not configured"))
		if (base.Type == "github" || base.Type == "slack") && base.Status == "not connected" {
			base.Configured = true
		}
		if base.Type == "telegram" {
			base.Configured = base.Running || strings.TrimSpace(base.configuration.Get("token")) != ""
		}
		if base.Type == "email" {
			base.Address = base.configuration.Get("email_address")
			base.Provider = base.configuration.Get("email_provider")
			base.Configured = base.Configured || base.Address != ""
		}
		if base.Running {
			base.Status = "running"
		} else if base.Connected {
			base.Status = "connected"
		} else if base.Configured && base.Status == "unknown" {
			base.Status = "configured"
		}
		out = append(out, base)
	}
	return out, nil
}

// GetChannel resolves an exact integration type from the selected project.
func (c *Client) GetChannel(ctx context.Context, projectID, channelType string) (*Channel, error) {
	channels, err := c.ListChannels(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for i := range channels {
		if channels[i].Type == strings.ToLower(strings.TrimSpace(channelType)) {
			return &channels[i], nil
		}
	}
	return nil, fmt.Errorf("channel %q is not available", channelType)
}

func safeChannelError(err error) error {
	if err == nil || IsAuthRequired(err) || IsTransportError(err) {
		return err
	}
	return errors.New("channel request failed")
}

func (c *Client) doSafeChannelForm(ctx context.Context, path string, form url.Values) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.Header.Set("HX-Request", "true")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	defer drainAndClose(resp.Body)
	if isAuthResponse(resp) {
		return newAuthRequiredError(http.MethodPost, path, resp)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("channel request failed")
	}
	return nil
}

// ConfigureChannel submits one backend-compatible channel configuration form.
// The caller owns field validation; this method owns exact route and scope.
func (c *Client) ConfigureChannel(ctx context.Context, channelType, projectID string, form url.Values) error {
	channelType = strings.ToLower(strings.TrimSpace(channelType))
	if _, ok := knownChannel(channelType); !ok {
		return fmt.Errorf("unsupported channel type %q", channelType)
	}
	path := "/channels/" + url.PathEscape(channelType)
	if channelType != "telegram" {
		path += "/configure"
	}
	return c.doSafeChannelForm(ctx, path+query("project_id", projectID), form)
}

// UpdateChannel performs an authoritative read-modify-write entirely inside
// the client so omitted settings, including credentials, remain unchanged
// without exposing them to command or rendering layers.
func (c *Client) UpdateChannel(ctx context.Context, channelType, projectID string, updates url.Values) error {
	current, err := c.GetChannel(ctx, projectID, channelType)
	if err != nil {
		return err
	}
	return c.UpdateChannelFromCurrent(ctx, *current, projectID, updates)
}

// UpdateChannelFromCurrent merges updates into a channel snapshot returned by
// GetChannel. Callers that need to validate changes against authoritative state
// can reuse that same snapshot for the mutation instead of fetching it twice.
func (c *Client) UpdateChannelFromCurrent(ctx context.Context, current Channel, projectID string, updates url.Values) error {
	if _, ok := knownChannel(current.Type); !ok {
		return fmt.Errorf("unsupported channel type %q", current.Type)
	}
	form := cloneChannelConfiguration(current.configuration)
	for key, entries := range updates {
		form[key] = append([]string(nil), entries...)
	}
	defer func() {
		for key := range form {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "private_key") {
				form.Set(key, "")
			}
		}
	}()
	return c.ConfigureChannel(ctx, current.Type, projectID, form)
}

// ChannelConnectURL returns the safe local backend URL a browser should open
// to begin OAuth. Any user-info, existing query, or fragment in the configured
// server URL is deliberately discarded.
func (c *Client) ChannelConnectURL(channelType, projectID string) (string, error) {
	channelType = strings.ToLower(strings.TrimSpace(channelType))
	if channelType != "github" && channelType != "slack" {
		return "", fmt.Errorf("%s does not support browser connection", channelType)
	}
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return "", errors.New("invalid server URL")
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	u.Path = strings.TrimRight(u.Path, "/") + "/channels/" + channelType + "/connect"
	u.RawPath = ""
	q := url.Values{}
	if strings.TrimSpace(projectID) != "" {
		q.Set("project_id", projectID)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// GetChannels returns only messaging integrations from the shared Channels
// screen. Modern pages expose all configured integrations as data-channel-type
// cards and identify the separately managed webhook list with webhook-card-list.
// Rendering only non-webhook cards avoids leaking webhook controls, pagination,
// empty states, modals, or scripts that live elsewhere in the shared page.
func (c *Client) GetChannels(ctx context.Context, projectID string) (string, error) {
	root, err := c.getHTML(ctx, "/channels"+query("project_id", projectID))
	if err != nil {
		return "", err
	}
	if container := findByID(root, "channels-container"); container != nil {
		if findByID(container, "webhook-card-list") != nil {
			parts := make([]string, 0)
			for _, card := range findAll(container, func(n *html.Node) bool {
				return hasHTMLAttr(n, "data-channel-type") && !strings.EqualFold(attr(n, "data-channel-type"), "webhook")
			}) {
				if text := strings.TrimSpace(NodeText(card)); text != "" {
					parts = append(parts, text)
				}
			}
			if len(parts) == 0 {
				return "no messaging integrations configured", nil
			}
			return strings.Join(parts, "\n\n"), nil
		}
		root = container
	}
	// Card-only fragments predate the modern webhook list marker. Keep webhook
	// cards out while preserving their surrounding legacy prose as before.
	for _, card := range findAll(root, func(n *html.Node) bool { return hasHTMLAttr(n, "data-webhook-id") }) {
		if card.Parent != nil {
			card.Parent.RemoveChild(card)
		}
	}
	return strings.TrimSpace(NodeText(root)), nil
}

// Webhook is the terminal-safe representation of one project-scoped inbound
// webhook. Backend secrets are deliberately not represented: list/detail JSON,
// diagnostics, and formatted values therefore cannot disclose them by default.
type Webhook struct {
	ID                 string   `json:"id"`
	ProjectID          string   `json:"project_id"`
	Name               string   `json:"name"`
	Enabled            bool     `json:"enabled"`
	Path               string   `json:"path"`
	URL                string   `json:"url"`
	SystemInstructions string   `json:"system_instructions,omitempty"`
	TitleTemplate      string   `json:"title_template,omitempty"`
	PromptTemplate     string   `json:"prompt_template,omitempty"`
	DefaultPriority    int      `json:"default_priority"`
	AgentIDs           []string `json:"agent_ids"`
}

// WebhookTestResult is returned after creating the synthetic webhook test task.
type WebhookTestResult struct {
	TaskID string `json:"task_id"`
}

// WebhookSecretRotation is returned only by the explicit, confirmation-gated
// secret rotation action. It must never be included in ordinary webhook output.
type WebhookSecretRotation struct {
	Secret string `json:"secret"`
}

func (c *Client) webhookLocation(pathToken string) (string, string) {
	path := "/webhooks/inbound/" + url.PathEscape(pathToken)
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return path, path
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	u.Path = path
	u.RawPath = ""
	return path, u.String()
}

func (c *Client) webhooksFromPages(pages []htmlPage, projectID string) []Webhook {
	out := make([]Webhook, 0)
	seen := make(map[string]struct{})
	for _, page := range pages {
		for _, card := range scrapeCards(page.root, "data-webhook-id") {
			id := strings.TrimSpace(card.Get("webhook-id"))
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			path, endpointURL := c.webhookLocation(card.Get("webhook-token"))
			out = append(out, Webhook{
				ID:              id,
				ProjectID:       projectID,
				Name:            card.Get("webhook-name"),
				Enabled:         card.Bool("webhook-enabled"),
				Path:            path,
				URL:             endpointURL,
				DefaultPriority: card.Int("webhook-default-priority"),
				AgentIDs:        make([]string, 0),
			})
		}
	}
	return out
}

// ListWebhooks returns every inbound webhook card for exactly one project.
func (c *Client) ListWebhooks(ctx context.Context, projectID string) ([]Webhook, error) {
	pages, err := c.getCardPages(ctx, "/channels"+query("project_id", projectID))
	if err != nil {
		return nil, err
	}
	return c.webhooksFromPages(pages, projectID), nil
}

// GetWebhook loads complete editable configuration while discarding the secret
// returned by the backend detail route.
func (c *Client) GetWebhook(ctx context.Context, projectID, id string) (*Webhook, error) {
	var raw struct {
		ID                 string   `json:"id"`
		ProjectID          string   `json:"project_id"`
		Name               string   `json:"name"`
		Enabled            bool     `json:"enabled"`
		PathToken          string   `json:"path_token"`
		SystemInstructions string   `json:"system_instructions"`
		TitleTemplate      string   `json:"title_template"`
		PromptTemplate     string   `json:"prompt_template"`
		DefaultPriority    int      `json:"default_priority"`
		AgentIDs           []string `json:"agent_ids"`
	}
	path := "/channels/webhooks/" + url.PathEscape(id) + query("project_id", projectID)
	if err := c.getJSON(ctx, path, &raw); err != nil {
		return nil, err
	}
	if raw.ProjectID != projectID {
		return nil, fmt.Errorf("webhook %q does not belong to selected project", id)
	}
	endpointPath, endpointURL := c.webhookLocation(raw.PathToken)
	return &Webhook{
		ID: raw.ID, ProjectID: raw.ProjectID, Name: raw.Name, Enabled: raw.Enabled,
		Path: endpointPath, URL: endpointURL, SystemInstructions: raw.SystemInstructions,
		TitleTemplate: raw.TitleTemplate, PromptTemplate: raw.PromptTemplate,
		DefaultPriority: raw.DefaultPriority, AgentIDs: nonNilStrings(raw.AgentIDs),
	}, nil
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return make([]string, 0)
	}
	return values
}

func webhookForm(webhook Webhook) url.Values {
	form := url.Values{}
	form.Set("name", webhook.Name)
	form.Set("enabled", strconv.FormatBool(webhook.Enabled))
	form.Set("system_instructions", webhook.SystemInstructions)
	form.Set("title_template", webhook.TitleTemplate)
	form.Set("prompt_template", webhook.PromptTemplate)
	form.Set("default_priority", strconv.Itoa(webhook.DefaultPriority))
	form.Set("agent_ids", strings.Join(webhook.AgentIDs, ","))
	return form
}

// CreateWebhook creates one webhook and returns its secret-free detail.
func (c *Client) CreateWebhook(ctx context.Context, projectID string, webhook Webhook) (*Webhook, error) {
	var created struct {
		ID        string `json:"id"`
		ProjectID string `json:"project_id"`
	}
	if err := c.doWebhookJSONForm(ctx, http.MethodPost, "/channels/webhooks"+query("project_id", projectID), webhookForm(webhook), &created); err != nil {
		return nil, err
	}
	if created.ProjectID != projectID {
		return nil, fmt.Errorf("created webhook does not belong to selected project")
	}
	return c.GetWebhook(ctx, projectID, created.ID)
}

// UpdateWebhook replaces the backend form while callers preserve omitted fields
// by starting from GetWebhook's complete configuration.
func (c *Client) UpdateWebhook(ctx context.Context, projectID string, webhook Webhook) (*Webhook, error) {
	if webhook.ProjectID != projectID {
		return nil, fmt.Errorf("webhook %q does not belong to selected project", webhook.ID)
	}
	path := "/channels/webhooks/" + url.PathEscape(webhook.ID) + query("project_id", projectID)
	if err := c.doForm(ctx, http.MethodPut, path, webhookForm(webhook)); err != nil {
		return nil, err
	}
	return c.GetWebhook(ctx, projectID, webhook.ID)
}

func (c *Client) doWebhookJSONForm(ctx context.Context, method, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer drainAndClose(resp.Body)
	if isAuthResponse(resp) {
		return newAuthRequiredError(method, path, resp)
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

// TestWebhook creates a synthetic task and returns its backend task ID.
func (c *Client) TestWebhook(ctx context.Context, projectID, id string) (*WebhookTestResult, error) {
	var out WebhookTestResult
	path := "/channels/webhooks/" + url.PathEscape(id) + "/test" + query("project_id", projectID)
	if err := c.doWebhookJSONForm(ctx, http.MethodPost, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RotateWebhookSecret rotates a secret only through the explicit caller action.
func (c *Client) RotateWebhookSecret(ctx context.Context, projectID, id string) (*WebhookSecretRotation, error) {
	var out WebhookSecretRotation
	path := "/channels/webhooks/" + url.PathEscape(id) + "/rotate-secret" + query("project_id", projectID)
	if err := c.doWebhookJSONForm(ctx, http.MethodPost, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteWebhook deletes one webhook in the selected project.
func (c *Client) DeleteWebhook(ctx context.Context, projectID, id string) error {
	return c.doForm(ctx, http.MethodDelete, "/channels/webhooks/"+url.PathEscape(id)+query("project_id", projectID), nil)
}

func (c *Client) doSafeChannelTest(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("HX-Request", "true")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	defer drainAndClose(resp.Body)
	if isAuthResponse(resp) {
		return newAuthRequiredError(http.MethodPost, path, resp)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("channel test failed")
	}
	root, err := html.Parse(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return errors.New("channel test failed")
	}
	feedback := ""
	for _, node := range findAll(root, func(n *html.Node) bool { return n.Type == html.ElementNode }) {
		for _, class := range strings.Fields(attr(node, "class")) {
			if class == "text-error" {
				return errors.New("channel test failed")
			}
			if class == "text-success" {
				feedback = class
			}
		}
	}
	if feedback == "" {
		return errors.New("channel test failed")
	}
	return nil
}

// ChannelAction runs a supported operational action on a channel integration.
// Slack remove intentionally maps to disconnect to preserve the established safe
// terminal behavior. Explicit disconnect is supported only by GitHub and Slack.
func (c *Client) ChannelAction(ctx context.Context, channelType, action, projectID string) error {
	channelType = strings.ToLower(strings.TrimSpace(channelType))
	if _, ok := knownChannel(channelType); !ok {
		return fmt.Errorf("unsupported channel type %q", channelType)
	}
	if action == "test" {
		if channelType == "github" {
			return errors.New("GitHub does not expose a connection test")
		}
		return c.doSafeChannelTest(ctx, "/channels/"+url.PathEscape(channelType)+"/test"+query("project_id", projectID))
	}
	if action == "disconnect" {
		if channelType != "github" && channelType != "slack" {
			return fmt.Errorf("%s does not support disconnect", channelType)
		}
		return c.doSafeChannelForm(ctx, "/channels/"+url.PathEscape(channelType)+"/disconnect"+query("project_id", projectID), nil)
	}
	if action != "remove" {
		return fmt.Errorf("unsupported channel action %q", action)
	}
	verb := "remove"
	if channelType == "slack" {
		verb = "disconnect"
	}
	return c.doSafeChannelForm(ctx,
		"/channels/"+url.PathEscape(channelType)+"/"+verb+query("project_id", projectID), nil)
}

// Personality is one built-in or custom entry on the Personality screen. The
// list endpoint returns a bounded prompt preview; GetCustomPersonality returns
// the complete system prompt for a selected key.
type Personality struct {
	ID                  string `json:"id,omitempty"`
	Name                string `json:"name"`
	Key                 string `json:"key"`
	Description         string `json:"description"`
	SystemPrompt        string `json:"system_prompt,omitempty"`
	SystemPromptPreview string `json:"system_prompt_preview"`
	IsPreset            bool   `json:"is_preset"`
	HasCustom           bool   `json:"has_custom"`
	Active              bool   `json:"active"`
}

// CustomPersonality is retained as a descriptive alias for detail responses;
// list entries can represent either a built-in preset or a custom personality.
type CustomPersonality = Personality

// ListPersonalities scrapes the scoped Personality screen. The backend renders
// built-in presets and non-preset custom entries as structured cards, including
// effective metadata for built-in overrides.
func (c *Client) ListPersonalities(ctx context.Context, projectID string) ([]Personality, error) {
	pages, err := c.getCardPages(ctx, "/personality"+query("project_id", projectID))
	if err != nil {
		return nil, err
	}

	selectedKey := ""
	sectionFound := false
	if section := findByID(pages[0].root, "personality-section"); section != nil {
		sectionFound = true
		selectedKey = attr(section, "data-selected-personality")
	}

	cards := make([]*html.Node, 0)
	for _, page := range pages {
		cards = append(cards, findAll(page.root, func(n *html.Node) bool {
			// data-personality-key is intentionally checked for presence because the
			// Base card carries the empty key as an explicit attribute.
			return hasHTMLAttr(n, "data-personality-key") &&
				attr(n, "data-personality-is-preset") != ""
		})...)
	}
	seen := make(map[string]bool)
	out := make([]Personality, 0, len(cards))
	for _, card := range cards {
		identity := attr(card, "data-personality-key")
		if seen[identity] {
			continue
		}
		seen[identity] = true
		p := Personality{
			ID:                  attr(card, "data-personality-id"),
			Name:                strings.TrimSpace(attr(card, "data-personality-name")),
			Key:                 attr(card, "data-personality-key"),
			Description:         strings.TrimSpace(attr(card, "data-personality-description")),
			SystemPromptPreview: strings.TrimSpace(attr(card, "data-personality-preview")),
			IsPreset:            attr(card, "data-personality-is-preset") == "true",
			HasCustom:           attr(card, "data-personality-has-custom") == "true",
		}
		if p.Name == "" {
			if p.Key != "" {
				p.Name = p.Key
			} else {
				p.Name = "Base"
			}
		}
		p.Active = sectionFound && p.Key == selectedKey
		out = append(out, p)
	}
	return out, nil
}

// GetCustomPersonality returns the complete detail for a custom key or a
// built-in preset. The backend uses the same route for both and returns an
// effective custom override when one exists.
func (c *Client) GetCustomPersonality(ctx context.Context, projectID, key string) (*Personality, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("personality key is required")
	}
	var out Personality
	if err := c.getJSON(ctx, "/personality/custom/"+url.PathEscape(key)+query("project_id", projectID), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type customPersonalityPayload struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	SystemPrompt string `json:"system_prompt"`
}

func (c *Client) decodePersonalityMutation(ctx context.Context, method, path string, payload customPersonalityPayload, expectedStatus int) (*Personality, error) {
	resp, err := c.doJSONResponse(ctx, method, path, payload)
	if err != nil {
		return nil, err
	}
	defer drainAndClose(resp.Body)
	if resp.StatusCode != expectedStatus {
		return nil, fmt.Errorf("%s %s: unexpected status %d", method, path, resp.StatusCode)
	}
	var out Personality
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding %s response: %w", path, err)
	}
	return &out, nil
}

// CreateCustomPersonality persists a custom personality. The backend derives
// the stable key from the name and returns the created record, including ID.
func (c *Client) CreateCustomPersonality(ctx context.Context, projectID, name, description, systemPrompt string) (*Personality, error) {
	return c.decodePersonalityMutation(ctx, http.MethodPost,
		"/personality/custom"+query("project_id", projectID),
		customPersonalityPayload{
			Name:         name,
			Description:  description,
			SystemPrompt: systemPrompt,
		}, http.StatusCreated)
}

// UpdateCustomPersonality updates a custom personality or creates a built-in
// override when key names a preset without an existing custom row.
func (c *Client) UpdateCustomPersonality(ctx context.Context, projectID, key, name, description, systemPrompt string) (*Personality, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("personality key is required")
	}
	return c.decodePersonalityMutation(ctx, http.MethodPut,
		"/personality/custom/"+url.PathEscape(key)+query("project_id", projectID),
		customPersonalityPayload{
			Name:         name,
			Description:  description,
			SystemPrompt: systemPrompt,
		}, http.StatusOK)
}

// DeleteCustomPersonality removes a custom personality or resets a built-in
// override. The backend also clears the active setting when this key is active.
func (c *Client) DeleteCustomPersonality(ctx context.Context, projectID, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("personality key is required")
	}
	return c.doForm(ctx, http.MethodDelete,
		"/personality/custom/"+url.PathEscape(key)+query("project_id", projectID), nil)
}

// GetPersonality returns the Personality screen as text.
func (c *Client) GetPersonality(ctx context.Context, projectID string) (string, error) {
	return c.paginatedPageText(ctx, "/personality"+query("project_id", projectID), "personality-container")
}

// SavePersonality sets the active personality preset or custom key.
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

// GetGrades returns the current idea grades without triggering a new grading run.
func (c *Client) GetGrades(ctx context.Context, projectID string) (string, error) {
	return c.pageText(ctx, "/history"+query("project_id", projectID), "idea-grade-content")
}

// GradeIdeas triggers a fresh grading pass (the Grades view on the history screen).
func (c *Client) GradeIdeas(ctx context.Context, projectID string) error {
	return c.doForm(ctx, http.MethodPost, "/history/grade-ideas"+query("project_id", projectID), nil)
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
	return c.paginatedPageText(ctx, "/automations"+query("project_id", projectID), "")
}

// Automation is one card on the Automations screen.
type Automation struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

// ListAutomations scrapes the automations screen for a project. Each card
// carries the automation id/name via its delete-menu button's
// data-automation-card-delete/data-automation-name attributes; lifecycle
// state comes from the card's own badge row.
func (c *Client) ListAutomations(ctx context.Context, projectID string) ([]Automation, error) {
	pages, err := c.getCardPages(ctx, "/automations"+query("project_id", projectID))
	if err != nil {
		return nil, err
	}
	out := make([]Automation, 0)
	seen := make(map[string]bool)
	for _, page := range pages {
		for _, automation := range parseAutomations(page.root) {
			if seen[automation.ID] {
				continue
			}
			seen[automation.ID] = true
			out = append(out, automation)
		}
	}
	return out, nil
}

func parseAutomations(root *html.Node) []Automation {
	out := make([]Automation, 0)
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && attr(n, "data-automation-url") != "" {
			if a := parseAutomationCard(n); a.ID != "" {
				out = append(out, a)
			}
			return
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return out
}

func parseAutomationCard(card *html.Node) Automation {
	var a Automation
	stateRank := -1
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if id := attr(n, "data-automation-card-delete"); id != "" && a.ID == "" {
				a.ID = id
				a.Name = attr(n, "data-automation-name")
			}
			if n.Data == "span" && strings.Contains(attr(n, "class"), "badge") {
				state := strings.ToLower(automationBadgeText(n))
				rank := automationStateRank(state)
				if rank > stateRank {
					a.State = state
					stateRank = rank
				}
				return
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(card)
	return a
}

func automationStateRank(state string) int {
	switch state {
	case "active":
		return 0
	case "paused":
		return 1
	case "draft":
		return 2
	case "archived":
		return 3
	default:
		return -1
	}
}

func automationBadgeText(n *html.Node) string {
	var state string
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			text := strings.ToLower(strings.TrimSpace(node.Data))
			if automationStateRank(text) >= 0 {
				state = text
			}
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return state
}

// AutomationAction runs run-now/pause/resume/delete on one automation.
func (c *Client) AutomationAction(ctx context.Context, automationID, action, projectID string) error {
	return c.doForm(ctx, http.MethodPost,
		"/automations/"+url.PathEscape(automationID)+"/"+action+query("project_id", projectID), nil)
}

func (c *Client) paginatedPageText(ctx context.Context, path, elementID string) (string, error) {
	pages, err := c.getCardPages(ctx, path)
	if err != nil {
		return "", err
	}

	firstRoot := pages[0].root
	paginationRoot := findNode(firstRoot, func(n *html.Node) bool {
		return hasHTMLAttr(n, "data-card-pagination-root")
	})
	selector, keyAttr := "", ""
	if paginationRoot != nil {
		selector = attr(paginationRoot, "data-card-pagination-card-selector")
		keyAttr = attr(paginationRoot, "data-card-pagination-key")
	}
	seen := make(map[string]struct{})
	for _, card := range paginationCardNodes(firstRoot, selector) {
		if key := attr(card, keyAttr); key != "" {
			seen[key] = struct{}{}
		}
	}

	firstNode := firstRoot
	if elementID != "" {
		if selected := findByID(firstRoot, elementID); selected != nil {
			firstNode = selected
		}
	}
	parts := make([]string, 0, len(pages))
	if text := strings.TrimSpace(NodeText(firstNode)); text != "" {
		parts = append(parts, text)
	}
	for _, page := range pages[1:] {
		for _, card := range paginationCardNodes(page.root, selector) {
			key := attr(card, keyAttr)
			if key != "" {
				if _, duplicate := seen[key]; duplicate {
					continue
				}
				seen[key] = struct{}{}
			}
			if text := strings.TrimSpace(NodeText(card)); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n"), nil
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
