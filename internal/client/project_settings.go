package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// ProjectAgentOption is an agent/model selectable as a project's default.
type ProjectAgentOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ProjectSettings is the authoritative settings representation exposed by the
// backend's existing-project form.
type ProjectSettings struct {
	ID                          string               `json:"id"`
	Name                        string               `json:"name"`
	Description                 string               `json:"description"`
	RepositorySource            string               `json:"repository_source"`
	RepositoryPath              string               `json:"repository_path"`
	GitHubURL                   string               `json:"github_url"`
	DefaultAgentID              string               `json:"default_agent_id"`
	DefaultAgentName            string               `json:"default_agent_name,omitempty"`
	MaxWorkers                  *int                 `json:"max_workers"`
	LocalRepositoryPathsEnabled bool                 `json:"local_repository_paths_enabled"`
	AvailableAgents             []ProjectAgentOption `json:"-"`
}

// GetProjectSettings reads the backend-owned edit form because /api/projects
// intentionally exposes only list metadata, not the complete settings record.
func (c *Client) GetProjectSettings(ctx context.Context, projectID string) (*ProjectSettings, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("project ID is required")
	}
	root, err := c.getHTML(ctx, "/projects/"+url.PathEscape(projectID)+"/edit")
	if err != nil {
		return nil, err
	}
	return parseProjectSettings(root, projectID)
}

func parseProjectSettings(root *html.Node, projectID string) (*ProjectSettings, error) {
	form := findNode(root, func(n *html.Node) bool {
		return n.Data == "form" && strings.HasPrefix(attr(n, "hx-put"), "/projects/")
	})
	if form == nil {
		return nil, fmt.Errorf("project settings: backend response did not include the edit form")
	}
	setting := &ProjectSettings{ID: projectID, AvailableAgents: make([]ProjectAgentOption, 0)}
	if modal := findByID(root, "edit_project_modal"); modal != nil {
		setting.LocalRepositoryPathsEnabled, _ = strconv.ParseBool(attr(modal, "data-local-repo-path-enabled"))
	}
	setting.Name = projectFieldValue(form, "name")
	setting.Description = projectFieldValue(form, "description")
	setting.RepositorySource = projectFieldValue(form, "repo_source")
	setting.RepositoryPath = projectFieldValue(form, "repo_path")
	setting.GitHubURL = projectFieldValue(form, "repo_url")
	setting.DefaultAgentID = projectFieldValue(form, "default_agent_config_id")
	if raw := strings.TrimSpace(projectFieldValue(form, "max_workers")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return nil, fmt.Errorf("project settings: invalid max_workers %q", raw)
		}
		setting.MaxWorkers = &value
	}
	if setting.Name == "" || (setting.RepositorySource != "local" && setting.RepositorySource != "github") {
		return nil, fmt.Errorf("project settings: backend response omitted required settings")
	}
	selectNode := findProjectField(form, "default_agent_config_id")
	if selectNode != nil {
		for _, option := range findAll(selectNode, func(n *html.Node) bool { return n.Data == "option" }) {
			id := strings.TrimSpace(attr(option, "value"))
			if id == "" {
				continue
			}
			name := strings.TrimSpace(rawNodeText(option))
			if i := strings.Index(name, " ("); i >= 0 {
				name = strings.TrimSpace(name[:i])
			}
			setting.AvailableAgents = append(setting.AvailableAgents, ProjectAgentOption{ID: id, Name: name})
			if id == setting.DefaultAgentID {
				setting.DefaultAgentName = name
			}
		}
	}
	return setting, nil
}

func findProjectField(form *html.Node, name string) *html.Node {
	return findNode(form, func(n *html.Node) bool {
		return (n.Data == "input" || n.Data == "textarea" || n.Data == "select") && attr(n, "name") == name
	})
}

func projectFieldValue(form *html.Node, name string) string {
	n := findProjectField(form, name)
	if n == nil {
		return ""
	}
	switch n.Data {
	case "textarea":
		return rawNodeText(n)
	case "select":
		options := findAll(n, func(option *html.Node) bool { return option.Data == "option" })
		for _, option := range options {
			if hasHTMLAttr(option, "selected") {
				return attr(option, "value")
			}
		}
		if len(options) > 0 {
			return attr(options[0], "value")
		}
	}
	return attr(n, "value")
}

// UpdateProjectSettings submits the complete authoritative form. Callers merge
// partial user edits into a freshly fetched ProjectSettings first.
func (c *Client) UpdateProjectSettings(ctx context.Context, settings ProjectSettings) error {
	if strings.TrimSpace(settings.ID) == "" {
		return fmt.Errorf("project ID is required")
	}
	form := url.Values{}
	form.Set("name", settings.Name)
	form.Set("description", settings.Description)
	form.Set("repo_source", settings.RepositorySource)
	form.Set("repo_path", settings.RepositoryPath)
	form.Set("repo_url", settings.GitHubURL)
	form.Set("default_agent_config_id", settings.DefaultAgentID)
	if settings.MaxWorkers != nil {
		form.Set("max_workers", strconv.Itoa(*settings.MaxWorkers))
	} else {
		form.Set("max_workers", "")
	}
	resp, err := c.doFormResponse(ctx, http.MethodPut, "/projects/"+url.PathEscape(settings.ID), form)
	if err != nil {
		return err
	}
	defer drainAndClose(resp.Body)
	if message := projectToastMessage(resp.Header.Get("HX-Trigger")); message != "" {
		return fmt.Errorf("update project: %s", message)
	}
	return nil
}
