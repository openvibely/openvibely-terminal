package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

const maxAutomationDefinitionBytes = 1 << 20

// AutomationDefinition is the complete YAML design used by the web builder.
// YAML is intentionally opaque so edits round-trip fields unknown to this client.
type AutomationDefinition struct {
	AutomationID string `json:"automation_id"`
	ProjectID    string `json:"project_id"`
	YAML         string `json:"-"`
}

// LoadAutomationDefinition loads the authoritative, complete builder YAML.
func (c *Client) LoadAutomationDefinition(ctx context.Context, projectID, automationID string) (*AutomationDefinition, error) {
	projectID, automationID = strings.TrimSpace(projectID), strings.TrimSpace(automationID)
	if projectID == "" {
		return nil, fmt.Errorf("project ID is required")
	}
	if automationID == "" {
		return nil, fmt.Errorf("automation ID is required")
	}
	path := "/automations/" + url.PathEscape(automationID) + "/builder" + query("project_id", projectID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("HX-Request", "true")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer drainAndClose(resp.Body)
	if isReadAuthResponse(resp) {
		return nil, newAuthRequiredError(http.MethodGet, path, resp)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, apiError(resp)
	}
	root, err := html.Parse(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("parsing automation builder: %w", err)
	}
	return parseAutomationDefinition(root, projectID, automationID)
}

func parseAutomationDefinition(root *html.Node, projectID, automationID string) (*AutomationDefinition, error) {
	builder := findByID(root, "automation-builder")
	if builder == nil {
		return nil, fmt.Errorf("automation builder: malformed response")
	}
	textarea := findNode(builder, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "textarea" && attr(n, "name") == "automation_yaml"
	})
	if textarea == nil {
		return nil, fmt.Errorf("automation builder: definition unavailable")
	}
	yaml := rawNodeText(textarea)
	if len(yaml) > maxAutomationDefinitionBytes {
		return nil, fmt.Errorf("automation definition exceeds %d bytes", maxAutomationDefinitionBytes)
	}
	// The web builder's design-form action is the authoritative identity and
	// project-scope evidence. Never substitute caller-supplied IDs when it is
	// absent: a stale or malformed fragment must fail closed before export/save.
	form := findNode(builder, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "form" && attr(n, "id") == "automation-design-form"
	})
	if form == nil || strings.TrimSpace(attr(form, "action")) == "" {
		return nil, fmt.Errorf("automation builder: authoritative target unavailable")
	}
	action, err := url.Parse(attr(form, "action"))
	if err != nil {
		return nil, fmt.Errorf("automation builder: invalid target")
	}
	wantPath := "/automations/" + url.PathEscape(automationID) + "/builder"
	if action.Path != wantPath || action.Query().Get("project_id") != projectID {
		return nil, fmt.Errorf("automation builder: response scope does not match selected automation")
	}
	return &AutomationDefinition{AutomationID: automationID, ProjectID: projectID, YAML: yaml}, nil
}

// UpdateAutomationDefinition previews the full definition and persists it only
// when the backend reports no validation items.
func (c *Client) UpdateAutomationDefinition(ctx context.Context, projectID, automationID, definitionYAML string) error {
	projectID, automationID = strings.TrimSpace(projectID), strings.TrimSpace(automationID)
	if projectID == "" || automationID == "" {
		return fmt.Errorf("project and automation IDs are required")
	}
	if strings.TrimSpace(definitionYAML) == "" {
		return fmt.Errorf("automation definition is empty")
	}
	if len(definitionYAML) > maxAutomationDefinitionBytes {
		return fmt.Errorf("automation definition exceeds %d bytes", maxAutomationDefinitionBytes)
	}
	path := "/automations/" + url.PathEscape(automationID) + "/builder" + query("project_id", projectID)
	form := url.Values{"project_id": {projectID}, "automation_yaml": {definitionYAML}}
	root, err := c.doFormHTML(ctx, http.MethodPost, path, form)
	if err != nil {
		return err
	}
	if _, err := parseAutomationDefinition(root, projectID, automationID); err != nil {
		return err
	}
	if err := automationBuilderError(root); err != nil {
		return err
	}
	form.Set("save_changes", "true")
	savedRoot, err := c.doFormHTML(ctx, http.MethodPost, path, form)
	if err != nil {
		return err
	}
	return automationBuilderError(savedRoot)
}

func automationBuilderError(root *html.Node) error {
	if root == nil {
		return nil
	}
	if alert := findNode(root, func(n *html.Node) bool {
		return n.Type == html.ElementNode && attr(n, "role") == "alert"
	}); alert != nil {
		message := strings.TrimSpace(NodeText(alert))
		if message == "" {
			message = "automation builder rejected the definition"
		}
		return fmt.Errorf("%s", message)
	}
	if validation := findNode(root, func(n *html.Node) bool { return hasHTMLAttr(n, "data-automation-validation-summary") }); validation != nil {
		messages := make([]string, 0)
		walkHTML(validation, func(n *html.Node) {
			if n.Type == html.ElementNode && n.Data == "li" {
				if message := strings.TrimSpace(NodeText(n)); message != "" && len(messages) < 20 {
					messages = append(messages, message)
				}
			}
		})
		if len(messages) == 0 {
			return fmt.Errorf("automation validation failed")
		}
		return fmt.Errorf("automation validation failed: %s", strings.Join(messages, "; "))
	}
	return nil
}

func walkHTML(root *html.Node, visit func(*html.Node)) {
	visit(root)
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		walkHTML(child, visit)
	}
}
