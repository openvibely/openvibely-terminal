package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ProjectAgentOption is an agent/model selectable as a project's default.
type ProjectAgentOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ProjectSettings is the backend-owned settings record used by project show and
// edit commands. AvailableAgents and LocalRepositoryPathsEnabled are read-only
// metadata used to validate terminal edits and are returned by the settings API.
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

type projectSettingsResponse struct {
	ProjectSettings
	AvailableAgents []ProjectAgentOption `json:"available_agents"`
}

// GetProjectSettings reads the stable JSON project settings contract. The
// browser edit page is intentionally not used by terminal clients.
func (c *Client) GetProjectSettings(ctx context.Context, projectID string) (*ProjectSettings, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("project ID is required")
	}

	path := "/api/projects/" + url.PathEscape(projectID) + "/settings"
	var fields map[string]json.RawMessage
	if err := c.getJSON(ctx, path, &fields); err != nil {
		return nil, err
	}
	for _, name := range []string{
		"id", "name", "description", "repository_source", "repository_path", "github_url",
		"default_agent_id", "max_workers", "local_repository_paths_enabled", "available_agents",
	} {
		if _, ok := fields[name]; !ok {
			return nil, fmt.Errorf("project settings: backend response omitted required field %s", name)
		}
	}

	var response projectSettingsResponse
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("project settings: encode response: %w", err)
	}
	if err := json.Unmarshal(encoded, &response); err != nil {
		return nil, fmt.Errorf("project settings: decode response: %w", err)
	}
	settings := response.ProjectSettings
	settings.AvailableAgents = response.AvailableAgents
	if settings.DefaultAgentName == "" {
		for _, agent := range settings.AvailableAgents {
			if agent.ID == settings.DefaultAgentID {
				settings.DefaultAgentName = agent.Name
				break
			}
		}
	}
	if settings.ID != projectID {
		return nil, fmt.Errorf("project settings: backend returned project ID %q for requested project %q", settings.ID, projectID)
	}
	if strings.TrimSpace(settings.Name) == "" {
		return nil, fmt.Errorf("project settings: backend response omitted required settings name")
	}
	if settings.RepositorySource != "local" && settings.RepositorySource != "github" {
		return nil, fmt.Errorf("project settings: backend returned invalid repository source %q", settings.RepositorySource)
	}
	if settings.MaxWorkers != nil && *settings.MaxWorkers < 0 {
		return nil, fmt.Errorf("project settings: backend returned invalid max_workers %d", *settings.MaxWorkers)
	}
	return &settings, nil
}

type projectSettingsUpdateRequest struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	RepositorySource string `json:"repository_source"`
	RepositoryPath   string `json:"repository_path"`
	GitHubURL        string `json:"github_url"`
	DefaultAgentID   string `json:"default_agent_id"`
	MaxWorkers       *int   `json:"max_workers"`
}

// UpdateProjectSettings submits the complete editable settings record. Callers
// merge partial user edits into a freshly fetched ProjectSettings first. The
// backend remains responsible for authorization and business-rule validation.
func (c *Client) UpdateProjectSettings(ctx context.Context, settings ProjectSettings) error {
	projectID := strings.TrimSpace(settings.ID)
	if projectID == "" {
		return fmt.Errorf("project ID is required")
	}
	request := projectSettingsUpdateRequest{
		Name:             settings.Name,
		Description:      settings.Description,
		RepositorySource: settings.RepositorySource,
		RepositoryPath:   settings.RepositoryPath,
		GitHubURL:        settings.GitHubURL,
		DefaultAgentID:   settings.DefaultAgentID,
		MaxWorkers:       settings.MaxWorkers,
	}
	return c.putJSON(ctx, "/api/projects/"+url.PathEscape(projectID)+"/settings", request, nil)
}
