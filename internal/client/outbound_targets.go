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
	_, _, err := c.saveOutboundTargets(ctx, projectID, targets, explicitAllowed, false)
	return err
}

// SaveOutboundTargetsWithResult submits the replacement form and returns the
// canonical saved-target page when the server includes one in its response.
// A response without that page remains a successful save for compatibility;
// callers that need persisted fields can then fetch the list.
func (c *Client) SaveOutboundTargetsWithResult(ctx context.Context, projectID string, targets []OutboundTarget, explicitAllowed bool) (OutboundTargetsPage, bool, error) {
	return c.saveOutboundTargets(ctx, projectID, targets, explicitAllowed, true)
}

func (c *Client) saveOutboundTargets(ctx context.Context, projectID string, targets []OutboundTarget, explicitAllowed, wantCanonical bool) (OutboundTargetsPage, bool, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return OutboundTargetsPage{Targets: make([]OutboundTarget, 0)}, false, errors.New("project ID is required for outbound targets")
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
		return OutboundTargetsPage{Targets: make([]OutboundTarget, 0)}, false, err
	}
	defer drainAndClose(resp.Body)
	root, parseErr := html.Parse(resp.Body)
	if parseErr != nil {
		return OutboundTargetsPage{Targets: make([]OutboundTarget, 0)}, false, fmt.Errorf("decoding outbound target save response: %w", parseErr)
	}
	if strings.Contains(resp.Header.Get("HX-Trigger"), "outbound-targets-save-error") {
		if message := outboundTargetSaveMessage(root); message != "" {
			return OutboundTargetsPage{Targets: make([]OutboundTarget, 0)}, false, errors.New(message)
		}
		return OutboundTargetsPage{Targets: make([]OutboundTarget, 0)}, false, errors.New("failed to save outbound targets")
	}
	if !wantCanonical {
		return OutboundTargetsPage{Targets: make([]OutboundTarget, 0)}, false, nil
	}
	if findByID(root, "outbound-targets-section") != nil {
		page, err := parseOutboundTargetsPage(root, projectID)
		if err != nil {
			return OutboundTargetsPage{Targets: make([]OutboundTarget, 0)}, false, fmt.Errorf("decoding canonical outbound target save response: %w", err)
		}
		return page, true, nil
	}
	if findByID(root, "outbound-target-saved-target") != nil {
		target, err := parseCanonicalSavedOutboundTarget(root, projectID)
		if err != nil {
			return OutboundTargetsPage{Targets: make([]OutboundTarget, 0)}, false, fmt.Errorf("decoding canonical outbound target save response: %w", err)
		}
		return OutboundTargetsPage{Targets: []OutboundTarget{target}}, true, nil
	}
	return OutboundTargetsPage{Targets: make([]OutboundTarget, 0)}, false, nil
}

// parseCanonicalSavedOutboundTarget accepts the narrow save reply wrapper and
// reads only the same safe semantic fields used by the outbound-target list
// parser. Other response fields, including credentials, are ignored.
func parseCanonicalSavedOutboundTarget(root *html.Node, projectID string) (OutboundTarget, error) {
	wrappers := findAll(root, func(node *html.Node) bool {
		return node.Data == "div" && attr(node, "id") == "outbound-target-saved-target"
	})
	if len(wrappers) != 1 {
		return OutboundTarget{}, errors.New("outbound target save response has invalid canonical target wrapper")
	}
	wrapper := wrappers[0]
	if strings.TrimSpace(attr(wrapper, "data-project-id")) != projectID {
		return OutboundTarget{}, errors.New("outbound target save response does not belong to selected project")
	}
	id, err := requiredOutboundTargetField(wrapper, "target_row_id")
	if err != nil {
		return OutboundTarget{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return OutboundTarget{}, errors.New("outbound target save response has an empty target ID")
	}
	target, err := convertOutboundTargetFields(wrapper, id, projectID, "outbound target save response")
	if err != nil {
		return OutboundTarget{}, err
	}
	return target, nil
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
		target, err := convertOutboundTargetFields(group, id, projectID, fmt.Sprintf("outbound target %q", safeOutboundTargetText(id)))
		if err != nil {
			return page, err
		}
		nameKey := target.Platform + "\x00" + strings.ToLower(target.Name)
		if target.Name != "" {
			if _, exists := seenNames[nameKey]; exists {
				return page, errors.New("outbound target response has duplicate target names")
			}
			seenNames[nameKey] = struct{}{}
		}
		destinationKey := target.Platform + "\x00" + target.TargetKind + "\x00" + target.Destination + "\x00" + target.ThreadID
		if _, exists := seenDestinations[destinationKey]; exists {
			return page, errors.New("outbound target response has duplicate destinations")
		}
		seenDestinations[destinationKey] = struct{}{}
		page.Targets = append(page.Targets, target)
	}
	return page, nil
}

func convertOutboundTargetFields(fields *html.Node, id, projectID, label string) (OutboundTarget, error) {
	platform, err := requiredOutboundTargetField(fields, "target_platform")
	if err != nil {
		return OutboundTarget{}, err
	}
	kind, err := requiredOutboundTargetField(fields, "target_kind")
	if err != nil {
		return OutboundTarget{}, err
	}
	name, err := requiredOutboundTargetField(fields, "target_name")
	if err != nil {
		return OutboundTarget{}, err
	}
	destination, err := requiredOutboundTargetField(fields, "target_target_id")
	if err != nil {
		return OutboundTarget{}, err
	}
	threadID, err := requiredOutboundTargetField(fields, "target_thread_id")
	if err != nil {
		return OutboundTarget{}, err
	}
	home, err := requiredOutboundTargetField(fields, "target_is_home")
	if err != nil {
		return OutboundTarget{}, err
	}
	subject, err := requiredOutboundTargetField(fields, "target_default_subject")
	if err != nil {
		return OutboundTarget{}, err
	}
	isHome, err := strconv.ParseBool(home)
	if err != nil {
		return OutboundTarget{}, fmt.Errorf("%s has invalid home state", label)
	}
	platform = strings.ToLower(strings.TrimSpace(platform))
	kind = strings.ToLower(strings.TrimSpace(kind))
	name = strings.TrimSpace(name)
	destination = strings.TrimSpace(destination)
	threadID = strings.TrimSpace(threadID)
	subject = strings.TrimSpace(subject)
	if platform == "" || kind == "" || destination == "" {
		return OutboundTarget{}, fmt.Errorf("%s is missing required fields", label)
	}
	if !IsSupportedOutboundTargetPlatform(platform) {
		return OutboundTarget{}, fmt.Errorf("%s has unsupported platform", label)
	}
	return OutboundTarget{
		ID: strings.TrimSpace(id), ProjectID: projectID, Platform: platform, TargetKind: kind,
		Name: name, Destination: destination, TargetID: destination, ThreadID: threadID,
		Home: isHome, DefaultSubject: subject,
	}, nil
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

// IsSupportedOutboundTargetPlatform reports whether platform is supported by
// outbound target parsing and terminal validation. It accepts case and
// surrounding whitespace differences.
func IsSupportedOutboundTargetPlatform(platform string) bool {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "slack", "telegram", "email", "discord", "x":
		return true
	default:
		return false
	}
}

func outboundTargetSaveMessage(root *html.Node) string {
	for _, node := range findAll(root, func(node *html.Node) bool {
		return node.Data == "div" && (hasHTMLClassToken(attr(node, "class"), "alert-error") || hasHTMLClassToken(attr(node, "class"), "alert-success"))
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
	if findNode(root, func(node *html.Node) bool { return hasHTMLClassToken(attr(node, "class"), "text-error") }) != nil {
		return false, nil
	}
	if findNode(root, func(node *html.Node) bool { return hasHTMLClassToken(attr(node, "class"), "text-success") }) != nil {
		return true, nil
	}
	return false, errors.New("outbound target test response was malformed")
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
