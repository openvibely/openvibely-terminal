package client

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

const projectSettingsHTML = `<dialog id="edit_project_modal" data-local-repo-path-enabled="true"><form hx-put="/projects/p1">
<input name="name" value="Old Project">
<textarea name="description">old description</textarea>
<select name="repo_source"><option value="local">Local</option><option value="github" selected>GitHub</option></select>
<input name="repo_path" value="/managed/old">
<input name="repo_url" value="https://github.com/acme/old repo">
<select name="default_agent_config_id"><option value="">Global</option><option value="agent-1" selected>Builder (openai/gpt)</option><option value="agent-2">Reviewer</option></select>
<input name="max_workers" value="4">
</form></dialog>`

func TestGetProjectSettingsParsesAuthoritativeEditForm(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/projects/p1/edit" || r.Header.Get("HX-Request") != "true" {
			t.Fatalf("request = %s %s HX=%q", r.Method, r.URL.Path, r.Header.Get("HX-Request"))
		}
		_, _ = w.Write([]byte(projectSettingsHTML))
	}))
	got, err := c.GetProjectSettings(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "p1" || got.Name != "Old Project" || got.Description != "old description" || got.RepositorySource != "github" || got.RepositoryPath != "/managed/old" || got.GitHubURL != "https://github.com/acme/old repo" || got.DefaultAgentID != "agent-1" || got.DefaultAgentName != "Builder" || got.MaxWorkers == nil || *got.MaxWorkers != 4 || !got.LocalRepositoryPathsEnabled {
		t.Fatalf("settings = %#v", got)
	}
	if len(got.AvailableAgents) != 2 || got.AvailableAgents[1].ID != "agent-2" {
		t.Fatalf("agents = %#v", got.AvailableAgents)
	}
}

func TestUpdateProjectSettingsSendsFullAuthoritativeFormAndSurfacesToast(t *testing.T) {
	settings := ProjectSettings{ID: "p /1", Name: "Renamed", Description: "", RepositorySource: "local", RepositoryPath: `\\server\share\repo with spaces`, GitHubURL: "", DefaultAgentID: "", MaxWorkers: intPointer(0)}
	var got url.Values
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.EscapedPath() != "/projects/p%20%2F1" {
			t.Fatalf("request = %s %s (%s)", r.Method, r.URL.Path, r.URL.EscapedPath())
		}
		_ = r.ParseForm()
		got = r.PostForm
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
	}))
	if err := c.UpdateProjectSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"name": "Renamed", "description": "", "repo_source": "local", "repo_path": `\\server\share\repo with spaces`, "repo_url": "", "default_agent_config_id": "", "max_workers": "0"} {
		if got.Get(key) != want {
			t.Errorf("%s = %q, want %q", key, got.Get(key), want)
		}
	}

	c = newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("HX-Trigger", `{"openvibelyToast":{"message":"failed to clone GitHub repository: denied","status":"failed"}}`)
		w.WriteHeader(http.StatusNoContent)
	}))
	if err := c.UpdateProjectSettings(context.Background(), settings); err == nil || !strings.Contains(err.Error(), "failed to clone") {
		t.Fatalf("error = %v", err)
	}
}

func TestProjectSettingsAuthRedirectKeepsTypedGuidance(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/login?next=/projects/p1/edit")
		w.WriteHeader(http.StatusFound)
	}))
	_, err := c.GetProjectSettings(context.Background(), "p1")
	if err == nil || !IsAuthRequired(err) {
		t.Fatalf("error = %v, want typed auth", err)
	}
}

func intPointer(v int) *int { return &v }
