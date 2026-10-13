package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

func intPointerForTUI(value int) *int { return &value }

func projectSettingsFixture() client.ProjectSettings {
	return client.ProjectSettings{
		ID: "p1", Name: "Alpha Project", Description: "kept", RepositorySource: "local",
		RepositoryPath: "/tmp/alpha path", DefaultAgentID: "agent-1", DefaultAgentName: "Builder",
		MaxWorkers: intPointerForTUI(3), LocalRepositoryPathsEnabled: true,
		AvailableAgents: []client.ProjectAgentOption{{ID: "agent-1", Name: "Builder"}, {ID: "agent-2", Name: "Reviewer Agent"}},
	}
}

type projectSettingsUpdateBody struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	RepositorySource string `json:"repository_source"`
	RepositoryPath   string `json:"repository_path"`
	GitHubURL        string `json:"github_url"`
	DefaultAgentID   string `json:"default_agent_id"`
	MaxWorkers       *int   `json:"max_workers"`
}

func writeProjectSettings(w http.ResponseWriter, settings client.ProjectSettings) {
	w.Header().Set("Content-Type", "application/json")
	payload := map[string]any{
		"id":                             settings.ID,
		"name":                           settings.Name,
		"description":                    settings.Description,
		"repository_source":              settings.RepositorySource,
		"repository_path":                settings.RepositoryPath,
		"github_url":                     settings.GitHubURL,
		"default_agent_id":               settings.DefaultAgentID,
		"default_agent_name":             settings.DefaultAgentName,
		"max_workers":                    settings.MaxWorkers,
		"local_repository_paths_enabled": settings.LocalRepositoryPathsEnabled,
		"available_agents":               settings.AvailableAgents,
	}
	_ = json.NewEncoder(w).Encode(payload)
}

func decodeProjectSettingsUpdate(t *testing.T, r *http.Request) projectSettingsUpdateBody {
	t.Helper()
	if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("HX-Request") != "" {
		t.Fatalf("unexpected update headers: %#v", r.Header)
	}
	var body projectSettingsUpdateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decode settings update: %v", err)
	}
	return body
}

func applyProjectSettingsUpdate(settings *client.ProjectSettings, body projectSettingsUpdateBody) {
	settings.Name = body.Name
	settings.Description = body.Description
	settings.RepositorySource = body.RepositorySource
	settings.RepositoryPath = body.RepositoryPath
	settings.GitHubURL = body.GitHubURL
	settings.DefaultAgentID = body.DefaultAgentID
	settings.MaxWorkers = body.MaxWorkers
	for _, agent := range settings.AvailableAgents {
		if agent.ID == settings.DefaultAgentID {
			settings.DefaultAgentName = agent.Name
		}
	}
}

func TestRenderProjectSettingsPreservesWorkerLimitStates(t *testing.T) {
	for _, tc := range []struct {
		name string
		max  *int
		want string
		no   string
	}{
		{name: "unset inherits", want: "Max workers: inherit", no: "Max workers: No limit"},
		{name: "explicit zero has no limit", max: intPointerForTUI(0), want: "Max workers: No limit", no: "Max workers: inherit"},
		{name: "positive is numeric", max: intPointerForTUI(7), want: "Max workers: 7", no: "Max workers: No limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := renderProjectSettings(client.ProjectSettings{ID: "p1", MaxWorkers: tc.max})
			if !strings.Contains(out, tc.want) || strings.Contains(out, tc.no) {
				t.Fatalf("rendered settings = %q, want %q and not %q", out, tc.want, tc.no)
			}
		})
	}
}

func TestProjectsShowAndEditUseJSONSettingsContract(t *testing.T) {
	settings := projectSettingsFixture()
	var gets, puts, browserRequests int
	var submitted projectSettingsUpdateBody
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/projects/p1/settings" && r.Method == http.MethodGet:
			gets++
			writeProjectSettings(w, settings)
		case r.URL.Path == "/api/projects/p1/settings" && r.Method == http.MethodPut:
			puts++
			submitted = decodeProjectSettingsUpdate(t, r)
			applyProjectSettingsUpdate(&settings, submitted)
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/projects/p1/edit":
			browserRequests++
			_, _ = io.WriteString(w, `<html><form name="browser form changed"></form></html>`)
		default:
			http.NotFound(w, r)
		}
	})
	m.projects = []client.Project{{ID: "p1", Name: "Alpha Project", Path: "/tmp/alpha path"}}
	m.setActiveProject(m.projects[0])
	m.threadID, m.threadTitle, m.input.Placeholder = "task-1", "Task One", "Reply to Task One..."

	m = runLine(t, m, `/projects show "Alpha Project"`)
	if out := stripANSI(transcript(m)); !strings.Contains(out, "Repository source: local") || !strings.Contains(out, "/tmp/alpha path") {
		t.Fatalf("show output = %s", out)
	}
	m = runLine(t, m, `/projects edit "Alpha Project" --name Renamed`)
	if puts != 1 || gets != 3 {
		t.Fatalf("GETs=%d PUTs=%d, want show GET plus pre/post-save GET and one PUT", gets, puts)
	}
	if submitted.Name != "Renamed" || submitted.Description != "kept" || submitted.RepositorySource != "local" || submitted.RepositoryPath != "/tmp/alpha path" || submitted.DefaultAgentID != "agent-1" || submitted.MaxWorkers == nil || *submitted.MaxWorkers != 3 {
		t.Fatalf("submitted settings = %#v", submitted)
	}
	if browserRequests != 0 {
		t.Fatalf("browser edit requests = %d", browserRequests)
	}
	if m.selectedID != "p1" || m.selectedName != "Renamed" || m.threadID != "task-1" || m.input.Placeholder != "Reply to Task One..." {
		t.Fatalf("same-ID context lost: selected=%s/%s thread=%s placeholder=%q", m.selectedID, m.selectedName, m.threadID, m.input.Placeholder)
	}
	if out := transcript(m); !strings.Contains(out, "updated project Renamed") {
		t.Fatalf("missing authoritative post-save result: %q", out)
	}
}

func TestProjectsEditUpdatesSupportedSettingsAndPreservesWhitespaceAndWorkerZero(t *testing.T) {
	settings := projectSettingsFixture()
	settings.RepositorySource = "github"
	settings.GitHubURL = "https://github.com/acme/alpha"
	var submitted projectSettingsUpdateBody
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeProjectSettings(w, settings)
		case http.MethodPut:
			submitted = decodeProjectSettingsUpdate(t, r)
			applyProjectSettingsUpdate(&settings, submitted)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	m.projects = []client.Project{{ID: "p1", Name: "Alpha Project", Path: "/tmp/alpha path"}}
	m.projectsLoaded = true
	m = runLine(t, m, `/projects edit p1 --name "  Renamed  " --description "  preserved  " --repository-source github --github-url https://github.com/acme/alpha --default-agent "Reviewer Agent" --max-workers 0`)
	if submitted.Name != "  Renamed  " || submitted.Description != "  preserved  " || submitted.RepositorySource != "github" || submitted.GitHubURL != "https://github.com/acme/alpha" || submitted.DefaultAgentID != "agent-2" || submitted.MaxWorkers == nil || *submitted.MaxWorkers != 0 {
		t.Fatalf("submitted settings = %#v", submitted)
	}
	if out := stripANSI(transcript(m)); !strings.Contains(out, "Max workers: No limit") {
		t.Fatalf("success transcript = %q", out)
	}
}

func TestProjectSettingsRejectInvalidResponseAndInvalidUserSettingsBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name, line, response, want string
	}{
		{name: "missing field", line: `/projects show p1`, response: `{"id":"p1","name":"Alpha"}`, want: "omitted required field description"},
		{name: "bad repository source", line: `/projects show p1`, response: `{"id":"p1","name":"Alpha","description":"","repository_source":"svn","repository_path":"","github_url":"","default_agent_id":"","max_workers":null,"local_repository_paths_enabled":true,"available_agents":[]}`, want: "invalid repository source"},
		{name: "negative workers", line: `/projects edit p1 --name Renamed`, response: `{"id":"p1","name":"Alpha","description":"","repository_source":"local","repository_path":"/tmp/repo","github_url":"","default_agent_id":"","max_workers":-1,"local_repository_paths_enabled":true,"available_agents":[]}`, want: "invalid max_workers"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			puts := 0
			m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					puts++
					return
				}
				_, _ = io.WriteString(w, tc.response)
			})
			m.projects = []client.Project{{ID: "p1", Name: "Alpha"}}
			m.projectsLoaded = true
			m = runLine(t, m, tc.line)
			if puts != 0 || !strings.Contains(transcript(m), tc.want) {
				t.Fatalf("PUTs=%d output=%q, want %q", puts, transcript(m), tc.want)
			}
		})
	}

	for _, tc := range []struct{ line, want string }{
		{`/projects edit p1 --max-workers -1`, "0 or a positive whole number"},
		{`/projects edit p1 --default-agent absent`, "unknown default agent"},
		{`/projects edit p1 --repository-source svn`, "local or github"},
	} {
		puts := 0
		m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				writeProjectSettings(w, projectSettingsFixture())
			} else if r.Method == http.MethodPut {
				puts++
			}
		})
		m.projects, m.projectsLoaded = []client.Project{{ID: "p1", Name: "Alpha Project"}}, true
		m = runLine(t, m, tc.line)
		if puts != 0 || !strings.Contains(stripANSI(transcript(m)), tc.want) {
			t.Fatalf("line=%q PUTs=%d output=%q", tc.line, puts, transcript(m))
		}
	}
}

func TestProjectSettingsKeepsLocalPathRestrictionAndBackendValidation(t *testing.T) {
	settings := projectSettingsFixture()
	settings.LocalRepositoryPathsEnabled = false
	puts := 0
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeProjectSettings(w, settings)
			return
		}
		puts++
	})
	m.projects, m.projectsLoaded = []client.Project{{ID: "p1", Name: "Alpha Project"}}, true
	m = runLine(t, m, `/projects edit p1 --repository-path /tmp/replacement`)
	if puts != 0 || !strings.Contains(transcript(m), "local repository paths are disabled") {
		t.Fatalf("PUTs=%d output=%q", puts, transcript(m))
	}

	settings = projectSettingsFixture()
	m = newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeProjectSettings(w, settings)
			return
		}
		puts++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"Max concurrent workers cannot exceed the global worker limit of 8"}`)
	})
	m.projects, m.projectsLoaded = []client.Project{{ID: "p1", Name: "Alpha Project"}}, true
	m = runLine(t, m, `/projects edit p1 --max-workers 99`)
	if puts != 1 || !strings.Contains(transcript(m), "cannot exceed the global worker limit of 8") {
		t.Fatalf("PUTs=%d output=%q", puts, transcript(m))
	}
}

func TestProjectRepositoryReplacementRequiresConfirmationAndForce(t *testing.T) {
	settings := projectSettingsFixture()
	var puts int
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeProjectSettings(w, settings)
			return
		}
		puts++
		w.WriteHeader(http.StatusNoContent)
	})
	m.projects, m.projectsLoaded = []client.Project{{ID: "p1", Name: "Alpha Project"}}, true
	m = runLine(t, m, `/projects edit p1 --repository-source github --github-url https://github.com/acme/new`)
	if m.pendingConfirmation == nil || puts != 0 || !strings.Contains(m.pendingConfirmation.message, "re-clone") || !strings.Contains(m.pendingConfirmation.message, "replace") {
		t.Fatalf("confirmation=%v puts=%d", m.pendingConfirmation, puts)
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.pendingConfirmation != nil || puts != 0 {
		t.Fatalf("cancel mutated: confirmation=%v puts=%d", m.pendingConfirmation, puts)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/projects" {
			_, _ = io.WriteString(w, `{"projects":[{"id":"p1","name":"Alpha Project","path":"/tmp/alpha path"}]}`)
			return
		}
		if r.Method == http.MethodGet {
			writeProjectSettings(w, settings)
			return
		}
		puts++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	c, _ := client.New(server.URL)
	args := []string{"projects", "edit", "p1", "--repository-source", "github", "--github-url", "https://github.com/acme/new"}
	if err := RunCLI(c, io.Discard, "", args, false, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("unforced CLI error = %v", err)
	}
	if puts != 0 {
		t.Fatalf("unforced PUTs=%d", puts)
	}
	if err := RunCLI(c, io.Discard, "", args, true, false); err != nil {
		t.Fatal(err)
	}
	if puts != 1 {
		t.Fatalf("forced PUTs=%d", puts)
	}
}

func TestProjectsEditPostSaveRefreshFailureReportsSavedAndRetainsRemoteAuth(t *testing.T) {
	for _, tc := range []struct {
		name, statusBody string
		status           int
		login            bool
	}{
		{name: "backend refresh failure", status: http.StatusInternalServerError, statusBody: `{"error":"temporary unavailable"}`},
		{name: "remote authorization expires", status: http.StatusFound, login: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			puts, gets := 0, 0
			m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					gets++
					if gets == 1 {
						writeProjectSettings(w, projectSettingsFixture())
						return
					}
					if tc.login {
						w.Header().Set("Location", "/login?next=/api/projects/p1/settings")
						w.WriteHeader(http.StatusFound)
						return
					}
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.statusBody)
					return
				}
				puts++
				w.WriteHeader(http.StatusNoContent)
			})
			m.projects, m.projectsLoaded = []client.Project{{ID: "p1", Name: "Alpha Project"}}, true
			m = runLine(t, m, `/projects edit p1 --description changed`)
			if puts != 1 || gets != 2 {
				t.Fatalf("PUTs=%d GETs=%d", puts, gets)
			}
			if tc.login {
				if !m.authRequired || !strings.Contains(transcript(m), "requires sign-in") {
					t.Fatalf("typed remote auth recovery lost: auth=%v transcript=%q", m.authRequired, transcript(m))
				}
			} else if !strings.Contains(transcript(m), "saved; authoritative refresh failed") {
				t.Fatalf("post-save failure status missing: %q", transcript(m))
			}
		})
	}
}

func TestProjectSettingsJSONOutputIncludesUpdatedAuthoritativeSettings(t *testing.T) {
	settings := projectSettingsFixture()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/projects" {
			_, _ = io.WriteString(w, `{"projects":[{"id":"p1","name":"Alpha Project","path":"/tmp/alpha path"}]}`)
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeProjectSettings(w, settings)
		case http.MethodPut:
			var body projectSettingsUpdateBody
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			applyProjectSettingsUpdate(&settings, body)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c, _ := client.New(server.URL)
	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"projects", "edit", "p1", "--name", "Renamed"}, false, true); err != nil {
		t.Fatal(err)
	}
	var got client.ProjectSettings
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &got); err != nil {
		t.Fatalf("json=%q err=%v", out.String(), err)
	}
	if got.ID != "p1" || got.Name != "Renamed" || got.RepositoryPath != "/tmp/alpha path" || got.MaxWorkers == nil || *got.MaxWorkers != 3 {
		t.Fatalf("authoritative JSON settings = %#v", got)
	}
}

func TestTerminalSafeProjectSettingsErrorPreservesAuthAndTransportTypes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/login")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	c, _ := client.New(server.URL)
	_, authErr := c.GetProjectSettings(context.Background(), "p1")
	if safe := terminalSafeProjectSettingsError(authErr); !client.IsAuthRequired(safe) {
		t.Fatalf("auth type lost: %v", safe)
	}
	if safe := terminalSafeProjectSettingsError(context.DeadlineExceeded); !client.IsTransportError(safe) {
		t.Fatalf("transport type lost: %v", safe)
	}
}

func TestProjectsShowSanitizesJSONBackendErrors(t *testing.T) {
	unsafe := "denied\x1b[31m\x1b]0;owned\a\nretry\rnow\a"
	m := newModelFromHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": unsafe})
	})
	m.projects, m.projectsLoaded = []client.Project{{ID: "p1", Name: "Alpha Project"}}, true
	m = runLine(t, m, `/projects show p1`)
	out := transcript(m)
	if !strings.Contains(out, "denied retry now") {
		t.Fatalf("safe error text missing: %q", out)
	}
	for _, bad := range []string{"\x1b", "\a", "\nretry", "\r"} {
		if strings.Contains(out, bad) {
			t.Fatalf("unsafe error output: %q", out)
		}
	}
}

func TestApplyProjectEditsCoversSupportedFieldsAndLocalPaths(t *testing.T) {
	base := projectSettingsFixture()
	for _, path := range []string{"/Users/me/repo with spaces", `C:\Users\me\repo with spaces`, `\\server\share\repo with spaces`} {
		t.Run(path, func(t *testing.T) {
			source := "local"
			updated, err := applyProjectEdits(base, projectEditValues{RepositorySource: &source, RepositoryPath: &path})
			if err != nil || updated.RepositorySource != "local" || updated.RepositoryPath != path {
				t.Fatalf("updated=%#v err=%v", updated, err)
			}
		})
	}
	name, description, source, githubURL, agent, workers := "Renamed", "", "github", "https://github.com/acme/repo", "Reviewer Agent", "0"
	updated, err := applyProjectEdits(base, projectEditValues{Name: &name, Description: &description, RepositorySource: &source, GitHubURL: &githubURL, DefaultAgent: &agent, MaxWorkers: &workers})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != name || updated.Description != "" || updated.RepositorySource != "github" || updated.GitHubURL != githubURL || updated.DefaultAgentID != "agent-2" || updated.MaxWorkers == nil || *updated.MaxWorkers != 0 {
		t.Fatalf("updated = %#v", updated)
	}
	inherited := "inherit"
	updated, err = applyProjectEdits(base, projectEditValues{MaxWorkers: &inherited})
	if err != nil || updated.MaxWorkers != nil {
		t.Fatalf("inherit settings=%#v err=%v", updated, err)
	}
}

func TestApplyProjectEditsRejectsSourceIncompatibleAndDisabledPathEdits(t *testing.T) {
	local := client.ProjectSettings{RepositorySource: "local", LocalRepositoryPathsEnabled: true}
	github := client.ProjectSettings{RepositorySource: "github", LocalRepositoryPathsEnabled: true}
	disabled := local
	disabled.LocalRepositoryPathsEnabled = false
	path, githubURL := "/tmp/repo", "https://github.com/acme/repo"
	for _, tc := range []struct {
		name     string
		settings client.ProjectSettings
		edits    projectEditValues
		want     string
	}{
		{name: "GitHub URL for local source", settings: local, edits: projectEditValues{GitHubURL: &githubURL}, want: "requires repository source github"},
		{name: "local path for GitHub source", settings: github, edits: projectEditValues{RepositoryPath: &path}, want: "requires repository source local"},
		{name: "disabled local paths", settings: disabled, edits: projectEditValues{RepositoryPath: &path}, want: "local repository paths are disabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := applyProjectEdits(tc.settings, tc.edits); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestProjectRepositoryReplacementRiskUsesCanonicalRepositoryIdentity(t *testing.T) {
	current := client.ProjectSettings{RepositorySource: "github", GitHubURL: "https://github.com/Acme/Repo.git"}
	for _, equivalent := range []string{"https://github.com/acme/repo", "git@github.com:acme/repo.git", "acme/repo"} {
		updated := current
		updated.GitHubURL = equivalent
		if projectRepositoryReplacementRisk(current, updated) {
			t.Errorf("equivalent repository %q warned about replacement", equivalent)
		}
	}
	updated := current
	updated.GitHubURL = "https://github.com/acme/other"
	if !projectRepositoryReplacementRisk(current, updated) {
		t.Error("changed repository did not warn about replacement")
	}
}

func TestParseProjectEditArgsKeepsExplicitBoundaryAndLiteralValues(t *testing.T) {
	projects := []client.Project{{ID: "option-name", Name: "--name"}, {ID: "p1", Name: "Alpha"}}
	for _, tc := range []struct {
		args               []string
		wantID, wantRef    string
		wantName, wantDesc string
	}{
		{args: []string{"--name", "|", "--description", "changed"}, wantID: "option-name", wantRef: "--name", wantDesc: "changed"},
		{args: []string{"p1", "--description", "|", "--name", "Changed"}, wantID: "p1", wantRef: "p1", wantName: "Changed", wantDesc: "|"},
	} {
		project, ref, edits, err := parseProjectEditArgs(projects, tc.args)
		if err != nil || project.ID != tc.wantID || ref != tc.wantRef || projectEditValue(edits.Name) != tc.wantName || projectEditValue(edits.Description) != tc.wantDesc {
			t.Fatalf("parse(%v) = %s/%q/name=%q desc=%q err=%v", tc.args, project.ID, ref, projectEditValue(edits.Name), projectEditValue(edits.Description), err)
		}
	}
}

func projectEditValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func TestProjectEditReferenceErrorIsSafe(t *testing.T) {
	ref := "Dup\x1b"
	projects := []client.Project{{ID: "p1", Name: ref + "[31m\a\nOne"}, {ID: "p2", Name: ref + "]0;owned\a\rTwo"}}
	_, err := matchProject(projects, ref)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous project reference, got %v", err)
	}
	for _, control := range []string{"\x1b", "\a", "\n", "\r"} {
		if strings.Contains(err.Error(), control) {
			t.Fatalf("unsafe error %q", err)
		}
	}
}

func TestStaleProjectEditCannotReplaceNewProjectContext(t *testing.T) {
	m := Model{projects: []client.Project{{ID: "p1", Name: "One"}, {ID: "p2", Name: "Two"}}, selectedID: "p2", selectedName: "Two", projectGeneration: 2, sessionGeneration: 1}
	updated, _ := m.Update(projectUpdatedMsg{sessionGeneration: 1, projectGeneration: 1, projectID: "p1", saved: true, settings: &client.ProjectSettings{ID: "p1", Name: "Stale rename"}})
	m = updated.(Model)
	if m.selectedID != "p2" || m.selectedName != "Two" || m.projects[0].Name != "One" {
		t.Fatalf("stale edit changed state: selected=%s/%s projects=%#v", m.selectedID, m.selectedName, m.projects)
	}
}

func TestProjectSettingsBackendErrorSanitizesControlCharacters(t *testing.T) {
	unsafe := "clone denied\x1b[31m\nretry\rnow\a"
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeProjectSettings(w, projectSettingsFixture())
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": unsafe})
	})
	m.projects, m.projectsLoaded = []client.Project{{ID: "p1", Name: "Alpha Project"}}, true
	m = runLine(t, m, `/projects edit p1 --name Renamed`)
	out := transcript(m)
	if !strings.Contains(out, "clone denied retry now") {
		t.Fatalf("safe error text missing: %q", out)
	}
	for _, bad := range []string{"\x1b", "\nretry", "\r", "\a"} {
		if strings.Contains(out, bad) {
			t.Fatalf("unsafe backend diagnostic %q", out)
		}
	}
}

func TestCLIProjectSettingsAuthRedirectIsReportedAsAuthorizationFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Location", "/login?next="+r.URL.Path)
			w.WriteHeader(http.StatusFound)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	c, _ := client.New(server.URL)
	err := RunCLI(c, io.Discard, "", []string{"projects", "show", "p1"}, false, false)
	if err == nil || !strings.Contains(err.Error(), "requires sign-in") || !strings.Contains(err.Error(), "/login") {
		t.Fatalf("remote auth guidance = %v", err)
	}
}

func TestProjectsEditMaxWorkerZeroJSONPostSave(t *testing.T) {
	settings := projectSettingsFixture()
	settings.MaxWorkers = nil
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/projects" {
			_, _ = io.WriteString(w, `{"projects":[{"id":"p1","name":"Alpha Project","path":"/tmp/alpha path"}]}`)
			return
		}
		if r.Method == http.MethodGet {
			writeProjectSettings(w, settings)
			return
		}
		body := decodeProjectSettingsUpdate(t, r)
		if body.MaxWorkers == nil || *body.MaxWorkers != 0 {
			t.Fatalf("submitted max_workers = %v, want explicit zero", body.MaxWorkers)
		}
		applyProjectSettingsUpdate(&settings, body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	c, _ := client.New(server.URL)
	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"projects", "edit", "p1", "--max-workers", "0"}, false, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Max workers: No limit") {
		t.Fatalf("output=%q", out.String())
	}
}
