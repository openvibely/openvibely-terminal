package tui

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-tui/internal/client"
)

const projectEditFixture = `<dialog id="edit_project_modal" data-local-repo-path-enabled="true"><form hx-put="/projects/p1"><input name="name" value="Alpha Project"><textarea name="description">kept</textarea><select name="repo_source"><option value="local" selected>Local</option><option value="github">GitHub</option></select><input name="repo_path" value="/tmp/alpha path"><input name="repo_url" value=""><select name="default_agent_config_id"><option value="" selected>Global</option><option value="agent-1">Builder</option></select><input name="max_workers" value="3"></form></dialog>`

func TestProjectsShowAndEditPreserveOmittedSettingsAndSameIDContext(t *testing.T) {
	var putForm url.Values
	saved := false
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/projects/p1/edit":
			html := projectEditFixture
			if saved {
				html = strings.Replace(html, `value="Alpha Project"`, `value="Renamed"`, 1)
			}
			_, _ = io.WriteString(w, html)
		case r.Method == http.MethodPut && r.URL.Path == "/projects/p1":
			_ = r.ParseForm()
			putForm = r.PostForm
			saved = true
			w.Header().Set("HX-Refresh", "true")
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			_, _ = io.WriteString(w, `{"projects":[{"id":"p1","name":"Renamed","path":"/tmp/alpha path"}]}`)
		default:
			http.NotFound(w, r)
		}
	})
	m.projects = []client.Project{{ID: "p1", Name: "Alpha Project", Path: "/tmp/alpha path"}}
	m.setActiveProject(m.projects[0])
	m.threadID, m.threadTitle, m.input.Placeholder = "task-1", "Task One", "Reply to Task One..."

	m = runLine(t, m, `/projects show "Alpha Project"`)
	if out := stripANSI(transcript(m)); !strings.Contains(out, "Repository source") || !strings.Contains(out, "/tmp/alpha path") {
		t.Fatalf("show output = %s", out)
	}
	m = runLine(t, m, `/projects edit "Alpha Project" --name Renamed`)
	if putForm == nil {
		t.Fatal("edit did not PUT")
	}
	for key, want := range map[string]string{"name": "Renamed", "description": "kept", "repo_source": "local", "repo_path": "/tmp/alpha path", "default_agent_config_id": "", "max_workers": "3"} {
		if putForm.Get(key) != want {
			t.Errorf("%s = %q, want %q", key, putForm.Get(key), want)
		}
	}
	if m.selectedID != "p1" || m.selectedName != "Renamed" || m.threadID != "task-1" || m.input.Placeholder != "Reply to Task One..." {
		t.Fatalf("same-ID context lost: selected=%s/%s thread=%s placeholder=%q", m.selectedID, m.selectedName, m.threadID, m.input.Placeholder)
	}
}

func TestProjectsEditValidatesBeforeMutationAndResolvesCanonicalReferences(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{`/projects edit missing --name nope`, "nothing matches"},
		{`/projects edit Dup --name nope`, "ambiguous"},
		{`/projects edit p1 --max-workers -1`, "0 or a positive whole number"},
		{`/projects edit p1 --default-agent absent`, "unknown default agent"},
		{`/projects edit p1 --repository-source svn`, "local or github"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			puts, edits := 0, 0
			m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/edit") {
					edits++
					_, _ = io.WriteString(w, projectEditFixture)
					return
				}
				if r.Method == http.MethodPut {
					puts++
				}
			})
			m.projects = []client.Project{{ID: "p1", Name: "Duplicate One"}, {ID: "p2", Name: "Duplicate Two"}}
			m.projectsLoaded = true
			m = runLine(t, m, tc.line)
			if puts != 0 {
				t.Fatalf("PUTs = %d", puts)
			}
			if (strings.Contains(tc.line, "missing") || strings.Contains(tc.line, "Dup")) && edits != 0 {
				t.Fatalf("reference failure made %d detail requests", edits)
			}
			if out := stripANSI(transcript(m)); !strings.Contains(out, tc.want) {
				t.Fatalf("want %q in %s", tc.want, out)
			}
		})
	}
}

func TestProjectSettingsReferenceErrorsAreTerminalSafe(t *testing.T) {
	ref := "Dup\x1b"
	projects := []client.Project{
		{ID: "p1", Name: ref + "[31m\a\nOne"},
		{ID: "p2", Name: ref + "]0;owned\a\rTwo"},
	}
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

func TestApplyProjectEditsCoversAllSettingsAndPathForms(t *testing.T) {
	base := client.ProjectSettings{
		ID: "p1", Name: "Keep", Description: "keep", RepositorySource: "local",
		RepositoryPath: "/keep", DefaultAgentID: "agent-1", DefaultAgentName: "Builder",
		MaxWorkers: intPointerForTUI(2), LocalRepositoryPathsEnabled: true,
		AvailableAgents: []client.ProjectAgentOption{{ID: "agent-1", Name: "Builder"}, {ID: "agent-2", Name: "Reviewer Agent"}},
	}
	for _, path := range []string{"/Users/me/repo with spaces", `C:\Users\me\repo with spaces`, `\\server\share\repo with spaces`} {
		t.Run(path, func(t *testing.T) {
			name, description, source, githubURL, agent, workers := "Renamed Project", "", "github", "https://github.com/acme/repo", "Reviewer Agent", "0"
			updated, err := applyProjectEdits(base, projectEditValues{Name: &name, Description: &description, RepositorySource: &source, RepositoryPath: &path, GitHubURL: &githubURL, DefaultAgent: &agent, MaxWorkers: &workers})
			if err != nil {
				t.Fatal(err)
			}
			if updated.Name != name || updated.Description != "" || updated.RepositoryPath != path || updated.GitHubURL != githubURL || updated.DefaultAgentID != "agent-2" || updated.MaxWorkers == nil || *updated.MaxWorkers != 0 {
				t.Fatalf("updated = %#v", updated)
			}
		})
	}
	unchanged, err := applyProjectEdits(base, projectEditValues{})
	if err != nil || unchanged.Name != base.Name || unchanged.Description != base.Description || unchanged.RepositoryPath != base.RepositoryPath || unchanged.DefaultAgentID != base.DefaultAgentID || unchanged.MaxWorkers == nil || *unchanged.MaxWorkers != 2 {
		t.Fatalf("omitted values changed: %#v, err=%v", unchanged, err)
	}
}

func intPointerForTUI(value int) *int { return &value }

func TestProjectRepositoryReplacementRiskMatchesBackendRepositoryIdentity(t *testing.T) {
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

func TestProjectsEditRepositoryReplacementRequiresConfirmationAndCanCancel(t *testing.T) {
	puts := 0
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, projectEditFixture)
			return
		}
		puts++
		w.Header().Set("HX-Refresh", "true")
	})
	m.projects = []client.Project{{ID: "p1", Name: "Alpha Project"}}
	m.projectsLoaded = true
	m = runLine(t, m, `/projects edit p1 --repository-source github --github-url https://github.com/acme/new`)
	if m.pendingConfirmation == nil || puts != 0 || !strings.Contains(m.pendingConfirmation.message, "re-clone") || !strings.Contains(m.pendingConfirmation.message, "replace") {
		t.Fatalf("confirmation=%v puts=%d", m.pendingConfirmation, puts)
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.pendingConfirmation != nil || puts != 0 {
		t.Fatalf("cancel mutated: confirmation=%v puts=%d", m.pendingConfirmation, puts)
	}
	m = runLine(t, m, `/projects edit p1 --repository-source github --github-url https://github.com/acme/new`)
	if m.pendingConfirmation == nil || puts != 0 {
		t.Fatalf("second edit did not wait for confirmation: pending=%v puts=%d", m.pendingConfirmation != nil, puts)
	}
	m = runLine(t, m, "yes")
	if m.pendingConfirmation != nil || puts != 1 {
		t.Fatalf("confirmed edit did not mutate exactly once: pending=%v puts=%d", m.pendingConfirmation != nil, puts)
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

func TestCLIProjectsEditJSONAndForceParity(t *testing.T) {
	puts := 0
	saved := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/projects":
			_, _ = io.WriteString(w, `{"projects":[{"id":"p1","name":"Alpha Project","path":"/tmp/alpha path"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/projects/p1/edit":
			html := projectEditFixture
			if saved {
				html = strings.Replace(html, `<option value="local" selected>`, `<option value="local">`, 1)
				html = strings.Replace(html, `<option value="github">`, `<option value="github" selected>`, 1)
				html = strings.Replace(html, `<input name="repo_url" value="">`, `<input name="repo_url" value="https://github.com/acme/new">`, 1)
			}
			_, _ = io.WriteString(w, html)
		case r.Method == http.MethodPut && r.URL.Path == "/projects/p1":
			puts++
			saved = true
			w.Header().Set("HX-Refresh", "true")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL)
	var out bytes.Buffer
	args := []string{"projects", "edit", "p1", "--repository-source", "github", "--github-url", "https://github.com/acme/new"}
	if err := RunCLI(c, &out, "", args, false, true); err == nil || !strings.Contains(err.Error(), "--force") || puts != 0 {
		t.Fatalf("unforced err=%v puts=%d", err, puts)
	}
	out.Reset()
	if err := RunCLI(c, &out, "", args, true, true); err != nil {
		t.Fatal(err)
	}
	var got client.ProjectSettings
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &got); err != nil {
		t.Fatalf("json=%q err=%v", out.String(), err)
	}
	if puts != 1 || got.ID != "p1" || got.RepositorySource != "github" || got.GitHubURL != "https://github.com/acme/new" {
		t.Fatalf("puts=%d settings=%#v", puts, got)
	}
}
