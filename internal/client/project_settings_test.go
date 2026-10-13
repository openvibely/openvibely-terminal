package client

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const projectSettingsJSON = `{"id":"p1","name":"Old Project","description":"old description","repository_source":"github","repository_path":"/managed/old","github_url":"https://github.com/acme/old repo","default_agent_id":"agent-1","default_agent_name":"Builder","max_workers":4,"local_repository_paths_enabled":true,"available_agents":[{"id":"agent-1","name":"Builder"},{"id":"agent-2","name":"Reviewer"}]}`

func TestGetProjectSettingsReadsStableJSONContract(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/projects/p1/settings" || r.Header.Get("Accept") != "application/json" {
			t.Fatalf("request = %s %s Accept=%q", r.Method, r.URL.Path, r.Header.Get("Accept"))
		}
		_, _ = w.Write([]byte(projectSettingsJSON))
	}))
	got, err := c.GetProjectSettings(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "p1" || got.Name != "Old Project" || got.Description != "old description" || got.RepositorySource != "github" || got.RepositoryPath != "/managed/old" || got.GitHubURL != "https://github.com/acme/old repo" || got.DefaultAgentID != "agent-1" || got.DefaultAgentName != "Builder" || got.MaxWorkers == nil || *got.MaxWorkers != 4 || !got.LocalRepositoryPathsEnabled {
		t.Fatalf("settings = %#v", got)
	}
	if len(got.AvailableAgents) != 2 || got.AvailableAgents[1] != (ProjectAgentOption{ID: "agent-2", Name: "Reviewer"}) {
		t.Fatalf("agents = %#v", got.AvailableAgents)
	}
}

func TestGetProjectSettingsUsesAvailableAgentNameWhenDisplayNameIsOmitted(t *testing.T) {
	body := strings.Replace(projectSettingsJSON, `"default_agent_name":"Builder",`, "", 1)
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
	got, err := c.GetProjectSettings(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.DefaultAgentName != "Builder" {
		t.Fatalf("default agent name = %q, want agent option name", got.DefaultAgentName)
	}
}

func TestGetProjectSettingsRejectsInvalidOrIncompleteContract(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{name: "missing field", body: `{"id":"p1","name":"Alpha"}`, want: "description"},
		{name: "wrong project", body: strings.Replace(projectSettingsJSON, `"id":"p1"`, `"id":"p2"`, 1), want: "returned project ID"},
		{name: "empty name", body: strings.Replace(projectSettingsJSON, `"name":"Old Project"`, `"name":"  "`, 1), want: "settings name"},
		{name: "invalid source", body: strings.Replace(projectSettingsJSON, `"repository_source":"github"`, `"repository_source":"svn"`, 1), want: "invalid repository source"},
		{name: "negative workers", body: strings.Replace(projectSettingsJSON, `"max_workers":4`, `"max_workers":-1`, 1), want: "invalid max_workers"},
		{name: "invalid json", body: `{`, want: "decoding"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			if _, err := c.GetProjectSettings(context.Background(), "p1"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestGetProjectSettingsPreservesExplicitZeroAndNullWorkerLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want *int
	}{
		{name: "zero", body: strings.Replace(projectSettingsJSON, `"max_workers":4`, `"max_workers":0`, 1), want: intPointer(0)},
		{name: "inherit", body: strings.Replace(projectSettingsJSON, `"max_workers":4`, `"max_workers":null`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			got, err := c.GetProjectSettings(context.Background(), "p1")
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == nil && got.MaxWorkers != nil || tc.want != nil && (got.MaxWorkers == nil || *got.MaxWorkers != *tc.want) {
				t.Fatalf("max_workers = %v, want %v", got.MaxWorkers, tc.want)
			}
		})
	}
}

func TestUpdateProjectSettingsSendsEditableJSONAndSurfacesValidationErrors(t *testing.T) {
	settings := ProjectSettings{ID: "p /1", Name: "Renamed", Description: "", RepositorySource: "local", RepositoryPath: `\\server\share\repo with spaces`, DefaultAgentID: "", MaxWorkers: intPointer(0), LocalRepositoryPathsEnabled: true, AvailableAgents: []ProjectAgentOption{{ID: "ignored", Name: "metadata"}}}
	var got map[string]json.RawMessage
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.EscapedPath() != "/api/projects/p%20%2F1/settings" {
			t.Fatalf("request = %s %s (%s)", r.Method, r.URL.Path, r.URL.EscapedPath())
		}
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" || r.Header.Get("HX-Request") != "" {
			t.Fatalf("headers = %#v", r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	if err := c.UpdateProjectSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	var decoded projectSettingsUpdateRequest
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Name != "Renamed" || decoded.Description != "" || decoded.RepositorySource != "local" || decoded.RepositoryPath != settings.RepositoryPath || decoded.GitHubURL != "" || decoded.DefaultAgentID != "" || decoded.MaxWorkers == nil || *decoded.MaxWorkers != 0 {
		t.Fatalf("request = %#v", decoded)
	}
	for _, readonly := range []string{"id", "local_repository_paths_enabled", "available_agents", "default_agent_name"} {
		if _, ok := got[readonly]; ok {
			t.Errorf("update sent read-only field %q", readonly)
		}
	}

	c = newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"repository path is not allowed"}`))
	}))
	if err := c.UpdateProjectSettings(context.Background(), settings); err == nil || !strings.Contains(err.Error(), "repository path is not allowed") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestProjectSettingsJSONPreservesExplicitZeroAndNull(t *testing.T) {
	zero, err := json.Marshal(projectSettingsUpdateRequest{MaxWorkers: intPointer(0)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(zero), `"max_workers":0`) {
		t.Fatalf("explicit zero JSON = %s", zero)
	}
	nilValue, err := json.Marshal(projectSettingsUpdateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(nilValue), `"max_workers":null`) {
		t.Fatalf("nil max workers JSON = %s", nilValue)
	}
}

func TestProjectSettingsRemoteAuthRedirectRetainsTypedGuidance(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", "/login?next=/api/projects/p1/settings")
				w.WriteHeader(http.StatusFound)
			}))
			var err error
			if method == http.MethodGet {
				_, err = c.GetProjectSettings(context.Background(), "p1")
			} else {
				err = c.UpdateProjectSettings(context.Background(), ProjectSettings{ID: "p1"})
			}
			if err == nil || !IsAuthRequired(err) {
				t.Fatalf("error = %v, want typed auth", err)
			}
		})
	}
}

func TestProjectSettingsRejectsEmptyProjectID(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected request") }))
	if _, err := c.GetProjectSettings(context.Background(), " "); err == nil || !strings.Contains(err.Error(), "project ID is required") {
		t.Fatalf("GET error = %v", err)
	}
	if err := c.UpdateProjectSettings(context.Background(), ProjectSettings{}); err == nil || !strings.Contains(err.Error(), "project ID is required") {
		t.Fatalf("PUT error = %v", err)
	}
}

func intPointer(v int) *int { return &v }
