package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

// OutboundTarget is the terminal-safe view of one saved send_message
// destination. ID and ProjectID are retained for scoped reference resolution
// but are intentionally omitted from all JSON output.
type OutboundTarget struct {
	ID             string `json:"-"`
	ProjectID      string `json:"-"`
	Platform       string `json:"platform"`
	TargetKind     string `json:"target_kind"`
	Name           string `json:"name,omitempty"`
	Destination    string `json:"destination"`
	TargetID       string `json:"-"`
	ThreadID       string `json:"thread_id,omitempty"`
	Home           bool   `json:"home"`
	DefaultSubject string `json:"default_subject,omitempty"`
}

// OutboundTargetsPage combines the selected project's saved destinations with
// the explicit-unsaved-target policy rendered by the same backend fragment.
type OutboundTargetsPage struct {
	Targets                       []OutboundTarget `json:"targets"`
	ExplicitUnsavedTargetsAllowed bool             `json:"explicit_unsaved_targets_allowed"`
}

// GetOutboundTargets fetches and parses the project-scoped saved destination
// fragment. Only the semantic outbound-target section is inspected; channel
// credentials, controls, scripts, and raw HTML never enter the model.
func (c *Client) GetOutboundTargets(ctx context.Context, projectID string) (OutboundTargetsPage, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return OutboundTargetsPage{Targets: make([]OutboundTarget, 0)}, errors.New("project ID is required for outbound targets")
	}
	root, err := c.getHTML(ctx, "/channels/outbound-targets"+query("project_id", projectID))
	if err != nil {
		return OutboundTargetsPage{Targets: make([]OutboundTarget, 0)}, err
	}
	return parseOutboundTargetsPage(root, projectID)
}

// ListOutboundTargets returns only the selected project's saved destinations.
func (c *Client) ListOutboundTargets(ctx context.Context, projectID string) ([]OutboundTarget, error) {
	page, err := c.GetOutboundTargets(ctx, projectID)
	if err != nil {
		return make([]OutboundTarget, 0), err
	}
	return page.Targets, nil
}

// GetOutboundTargetPolicy reads only the selected project's explicit-target
// policy form. It deliberately avoids the saved-destination table so policy
// display stays independent from target row count and row parsing.
func (c *Client) GetOutboundTargetPolicy(ctx context.Context, projectID string) (bool, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return false, errors.New("project ID is required for outbound target policy")
	}
	root, err := c.getHTML(ctx, "/channels/send-message-explicit-targets"+query("project_id", projectID))
	if err != nil {
		return false, err
	}
	return parseOutboundTargetPolicy(root, projectID)
}

// SetOutboundTargetPolicy updates only the selected project's explicit-target
// policy. Saved destinations are not loaded or reposted.
func (c *Client) SetOutboundTargetPolicy(ctx context.Context, projectID string, allowed bool) error {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return errors.New("project ID is required for outbound target policy")
	}
	form := url.Values{}
	form.Set("project_id", projectID)
	if allowed {
		form.Set("enabled", "true")
	}
	resp, err := c.doFormResponse(ctx, http.MethodPost, "/channels/send-message-explicit-targets", form)
	if err != nil {
		return err
	}
	defer drainAndClose(resp.Body)
	root, parseErr := html.Parse(resp.Body)
	if parseErr != nil {
		return fmt.Errorf("decoding outbound target policy response: %w", parseErr)
	}
	if strings.Contains(resp.Header.Get("HX-Trigger"), "outbound-targets-save-error") {
		if message := outboundTargetSaveMessage(root); message != "" {
			return errors.New(message)
		}
		return errors.New("failed to save outbound target policy")
	}
	return nil
}

// SaveOutboundTargets submits the same replacement form used by the web UI.
// Empty IDs are intentionally preserved for new rows so the backend allocates
// canonical IDs; existing IDs are sent unchanged for edits and removals.
func (c *Client) SaveOutboundTargets(ctx context.Context, projectID string, targets []OutboundTarget, explicitAllowed bool) error {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return errors.New("project ID is required for outbound targets")
	}
	form := url.Values{}
	form.Set("project_id", projectID)
	for _, target := range targets {
		form.Add("target_row_id", strings.TrimSpace(target.ID))
		form.Add("target_platform", strings.TrimSpace(target.Platform))
		form.Add("target_kind", strings.TrimSpace(target.TargetKind))
		form.Add("target_name", strings.TrimSpace(target.Name))
		form.Add("target_target_id", outboundTargetDestination(target))
		form.Add("target_thread_id", strings.TrimSpace(target.ThreadID))
		form.Add("target_is_home", strconv.FormatBool(target.Home))
		form.Add("target_default_subject", strings.TrimSpace(target.DefaultSubject))
	}
	if explicitAllowed {
		form.Set("enabled", "true")
	}
	resp, err := c.doFormResponse(ctx, http.MethodPost, "/channels/send-message-explicit-targets", form)
	if err != nil {
		return err
	}
	defer drainAndClose(resp.Body)
	root, parseErr := html.Parse(resp.Body)
	if parseErr != nil {
		return fmt.Errorf("decoding outbound target save response: %w", parseErr)
	}
	if strings.Contains(resp.Header.Get("HX-Trigger"), "outbound-targets-save-error") {
		if message := outboundTargetSaveMessage(root); message != "" {
			return errors.New(message)
		}
		return errors.New("failed to save outbound targets")
	}
	return nil
}

// TestOutboundTarget tests one saved destination and returns the backend's
// semantic Sent/Failed result. A failed provider send is not a transport error.
func (c *Client) TestOutboundTarget(ctx context.Context, projectID, id string) (bool, error) {
	projectID = strings.TrimSpace(projectID)
	id = strings.TrimSpace(id)
	if projectID == "" {
		return false, errors.New("project ID is required for outbound target tests")
	}
	if id == "" {
		return false, errors.New("outbound target ID is required")
	}
	resp, err := c.doFormResponse(ctx, http.MethodPost,
		"/channels/outbound-targets/"+url.PathEscape(id)+"/test"+query("project_id", projectID), nil)
	if err != nil {
		return false, err
	}
	defer drainAndClose(resp.Body)
	return parseOutboundTargetTestResponse(resp.Body)
}

// TestOutboundTargetDraft tests an unsaved destination through the backend's
// draft route. It deliberately does not persist the target.
func (c *Client) TestOutboundTargetDraft(ctx context.Context, projectID string, target OutboundTarget) (bool, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return false, errors.New("project ID is required for outbound target tests")
	}
	form := url.Values{}
	form.Set("project_id", projectID)
	form.Set("target_platform", strings.TrimSpace(target.Platform))
	form.Set("target_kind", strings.TrimSpace(target.TargetKind))
	form.Set("target_name", strings.TrimSpace(target.Name))
	form.Set("target_target_id", outboundTargetDestination(target))
	form.Set("target_thread_id", strings.TrimSpace(target.ThreadID))
	form.Set("target_is_home", strconv.FormatBool(target.Home))
	form.Set("target_default_subject", strings.TrimSpace(target.DefaultSubject))
	resp, err := c.doFormResponse(ctx, http.MethodPost, "/channels/outbound-targets/test-draft", form)
	if err != nil {
		return false, err
	}
	defer drainAndClose(resp.Body)
	return parseOutboundTargetTestResponse(resp.Body)
}

func outboundTargetDestination(target OutboundTarget) string {
	if strings.TrimSpace(target.Destination) != "" {
		return strings.TrimSpace(target.Destination)
	}
	return strings.TrimSpace(target.TargetID)
}

func parseOutboundTargetPolicy(root *html.Node, projectID string) (bool, error) {
	projectID = strings.TrimSpace(projectID)
	section := findByID(root, "outbound-targets-section")
	if section != nil && strings.TrimSpace(attr(section, "data-project-id")) != projectID {
		return false, errors.New("outbound target policy response does not belong to selected project")
	}
	policyForm := findByID(root, "outbound-targets-policy-form")
	if policyForm == nil {
		return false, errors.New("outbound target policy response is malformed")
	}
	projectInputs := findAll(policyForm, func(node *html.Node) bool {
		return node.Data == "input" && attr(node, "name") == "project_id"
	})
	if len(projectInputs) != 1 || strings.TrimSpace(attr(projectInputs[0], "value")) != projectID {
		return false, errors.New("outbound target policy response has invalid project scope")
	}
	if enabled := findNode(policyForm, func(node *html.Node) bool {
		return node.Data == "input" && attr(node, "name") == "enabled" && attr(node, "type") == "checkbox"
	}); enabled != nil {
		return hasHTMLAttr(enabled, "checked"), nil
	}
	return false, nil
}

func parseOutboundTargetsPage(root *html.Node, projectID string) (OutboundTargetsPage, error) {
	page := OutboundTargetsPage{Targets: make([]OutboundTarget, 0)}
	section := findByID(root, "outbound-targets-section")
	if section == nil {
		return page, errors.New("outbound target response is malformed")
	}
	if strings.TrimSpace(attr(section, "data-project-id")) != projectID {
		return page, errors.New("outbound target response does not belong to selected project")
	}
	projectInputs := findAll(section, func(node *html.Node) bool {
		return node.Data == "input" && attr(node, "name") == "project_id"
	})
	if len(projectInputs) != 1 || strings.TrimSpace(attr(projectInputs[0], "value")) != projectID {
		return page, errors.New("outbound target response has invalid project scope")
	}
	if enabled := findNode(section, func(node *html.Node) bool {
		return node.Data == "input" && attr(node, "name") == "enabled" && attr(node, "type") == "checkbox"
	}); enabled != nil {
		page.ExplicitUnsavedTargetsAllowed = hasHTMLAttr(enabled, "checked")
	}

	groups := findAll(section, func(node *html.Node) bool {
		return node.Data != "tr" && hasHTMLAttr(node, "data-outbound-target-draft-key") &&
			findNode(node, func(child *html.Node) bool {
				return child.Data == "input" && attr(child, "name") == "target_target_id"
			}) != nil
	})
	rows := findAll(section, func(node *html.Node) bool {
		return node.Data == "tr" && hasHTMLAttr(node, "data-outbound-target-draft-key")
	})
	if len(groups) != len(rows) {
		return page, errors.New("outbound target response has mismatched target rows")
	}
	groupByID := make(map[string]*html.Node, len(groups))
	for _, group := range groups {
		id := strings.TrimSpace(attr(group, "data-outbound-target-draft-key"))
		if id == "" {
			return page, errors.New("outbound target response has an empty target ID")
		}
		if _, exists := groupByID[id]; exists {
			return page, errors.New("outbound target response has duplicate target IDs")
		}
		groupByID[id] = group
	}
	rowIDs := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		id := strings.TrimSpace(attr(row, "data-outbound-target-draft-key"))
		if id == "" {
			return page, errors.New("outbound target response has an empty target row ID")
		}
		if _, exists := rowIDs[id]; exists {
			return page, errors.New("outbound target response has duplicate target rows")
		}
		rowIDs[id] = struct{}{}
		if _, exists := groupByID[id]; !exists {
			return page, errors.New("outbound target response is missing target fields")
		}
	}

	seenDestinations := make(map[string]struct{}, len(groups))
	seenNames := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		id := strings.TrimSpace(attr(group, "data-outbound-target-draft-key"))
		rowID, err := requiredOutboundTargetField(group, "target_row_id")
		if err != nil {
			return page, err
		}
		if strings.TrimSpace(rowID) != id {
			return page, fmt.Errorf("outbound target %q has mismatched row ID", safeOutboundTargetText(id))
		}
		platform, err := requiredOutboundTargetField(group, "target_platform")
		if err != nil {
			return page, err
		}
		kind, err := requiredOutboundTargetField(group, "target_kind")
		if err != nil {
			return page, err
		}
		destination, err := requiredOutboundTargetField(group, "target_target_id")
		if err != nil {
			return page, err
		}
		threadID, err := requiredOutboundTargetField(group, "target_thread_id")
		if err != nil {
			return page, err
		}
		home, err := requiredOutboundTargetField(group, "target_is_home")
		if err != nil {
			return page, err
		}
		subject, err := requiredOutboundTargetField(group, "target_default_subject")
		if err != nil {
			return page, err
		}
		name, err := requiredOutboundTargetField(group, "target_name")
		if err != nil {
			return page, err
		}
		isHome, parseErr := strconv.ParseBool(home)
		if parseErr != nil {
			return page, fmt.Errorf("outbound target %q has invalid home state", safeOutboundTargetText(id))
		}
		platform = strings.ToLower(strings.TrimSpace(platform))
		kind = strings.ToLower(strings.TrimSpace(kind))
		name = strings.TrimSpace(name)
		destination = strings.TrimSpace(destination)
		threadID = strings.TrimSpace(threadID)
		subject = strings.TrimSpace(subject)
		if platform == "" || kind == "" || destination == "" {
			return page, fmt.Errorf("outbound target %q is missing required fields", safeOutboundTargetText(id))
		}
		if !supportedOutboundTargetPlatform(platform) {
			return page, fmt.Errorf("outbound target %q has unsupported platform", safeOutboundTargetText(id))
		}
		nameKey := platform + "\x00" + strings.ToLower(name)
		if name != "" {
			if _, exists := seenNames[nameKey]; exists {
				return page, errors.New("outbound target response has duplicate target names")
			}
			seenNames[nameKey] = struct{}{}
		}
		destinationKey := platform + "\x00" + kind + "\x00" + destination + "\x00" + threadID
		if _, exists := seenDestinations[destinationKey]; exists {
			return page, errors.New("outbound target response has duplicate destinations")
		}
		seenDestinations[destinationKey] = struct{}{}
		page.Targets = append(page.Targets, OutboundTarget{
			ID: id, ProjectID: projectID, Platform: platform, TargetKind: kind,
			Name: name, Destination: destination, TargetID: destination,
			ThreadID: threadID, Home: isHome, DefaultSubject: subject,
		})
	}
	return page, nil
}

func requiredOutboundTargetField(group *html.Node, name string) (string, error) {
	fields := findAll(group, func(node *html.Node) bool {
		return node.Data == "input" && attr(node, "name") == name
	})
	if len(fields) != 1 {
		return "", fmt.Errorf("outbound target response has invalid %s field", name)
	}
	return attr(fields[0], "value"), nil
}

func supportedOutboundTargetPlatform(platform string) bool {
	switch platform {
	case "slack", "telegram", "email", "discord", "x":
		return true
	default:
		return false
	}
}

func outboundTargetSaveMessage(root *html.Node) string {
	for _, node := range findAll(root, func(node *html.Node) bool {
		return node.Data == "div" && (hasClass(node, "alert-error") || hasClass(node, "alert-success"))
	}) {
		if message := safeOutboundTargetText(NodeText(node)); message != "" {
			return message
		}
	}
	return ""
}

func parseOutboundTargetTestResponse(body interface{ Read([]byte) (int, error) }) (bool, error) {
	root, err := html.Parse(body)
	if err != nil {
		return false, errors.New("outbound target test response was malformed")
	}
	if findNode(root, func(node *html.Node) bool { return hasClass(node, "text-error") }) != nil {
		return false, nil
	}
	if findNode(root, func(node *html.Node) bool { return hasClass(node, "text-success") }) != nil {
		return true, nil
	}
	return false, errors.New("outbound target test response was malformed")
}

func hasClass(node *html.Node, class string) bool {
	for _, value := range strings.Fields(attr(node, "class")) {
		if value == class {
			return true
		}
	}
	return false
}

func safeOutboundTargetText(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			if r == '\n' || r == '\r' || r == '\t' {
				b.WriteByte(' ')
			}
			continue
		}
		b.WriteRune(r)
		if b.Len() >= 512 {
			break
		}
	}
	return strings.TrimSpace(b.String())
}
