package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

func TestProjectsEditResolvesNamesContainingOptionLikeTokens(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want string
	}{
		{name: "quoted option name", line: `/projects edit "--name" --description changed`, want: "--name"},
		{name: "option in name", line: `/projects edit "Alpha --description token" --name changed`, want: "Alpha --description token"},
		{name: "complete option pair in name", line: `/projects edit Alpha --name Beta | --description changed`, want: "Alpha --name Beta"},
		{name: "explicit separator", line: `/projects edit --name | --description changed`, want: "--name"},
		{name: "global json flag name", line: `/projects edit --json | --description changed`, want: "--json"},
		{name: "global force flag name", line: `/projects edit --force | --description changed`, want: "--force"},
		{name: "global project flag name", line: `/projects edit --project | --description changed`, want: "--project"},
		{name: "literal separator value", line: `/projects edit target-id --description |`, want: "Target"},
		{name: "literal separator value before another option", line: `/projects edit target-id --description | --name Changed`, want: "Changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const projectID = "target-id"
			puts := 0
			m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/projects/"+projectID+"/edit":
					fixture := strings.ReplaceAll(projectEditFixture, "/projects/p1", "/projects/"+projectID)
					fixture = strings.Replace(fixture, "Alpha Project", tc.want, 1)
					_, _ = io.WriteString(w, fixture)
				case r.Method == http.MethodPut && r.URL.Path == "/projects/"+projectID:
					puts++
					w.Header().Set("HX-Refresh", "true")
				default:
					http.NotFound(w, r)
				}
			})
			m.projects = []client.Project{{ID: "other-id", Name: "Alpha"}, {ID: projectID, Name: tc.want}}
			m.projectsLoaded = true
			m = runLine(t, m, tc.line)
			if puts != 1 {
				t.Fatalf("PUTs = %d, transcript=%q", puts, transcript(m))
			}
		})
	}
}

func TestCLIProjectsEditResolvesNamesContainingOptionLikeTokens(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "option name", args: []string{"projects", "edit", "--name", "--description", "changed"}, want: "--name"},
		{name: "global json flag name after outer boundary", args: []string{"projects", "edit", "--json", "|", "--description", "changed"}, want: "--json"},
		{name: "global force flag name after outer boundary", args: []string{"projects", "edit", "--force", "|", "--description", "changed"}, want: "--force"},
		{name: "global project flag name after outer boundary", args: []string{"projects", "edit", "--project", "|", "--description", "changed"}, want: "--project"},
		{name: "complete option pair in name", args: []string{"projects", "edit", "Alpha", "--name", "Beta", "|", "--description", "changed"}, want: "Alpha --name Beta"},
		{name: "explicit separator", args: []string{"projects", "edit", "--name", "|", "--description", "changed"}, want: "--name"},
		{name: "literal separator value", args: []string{"projects", "edit", "target-id", "--description", "|"}, want: "Target"},
		{name: "literal separator value before another option", args: []string{"projects", "edit", "target-id", "--description", "|", "--name", "Changed"}, want: "Changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const projectID = "target-id"
			puts := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/api/projects":
					_, _ = fmt.Fprintf(w, `{"projects":[{"id":"other-id","name":"Alpha"},{"id":"%s","name":%q}]}`, projectID, tc.want)
				case r.Method == http.MethodGet && r.URL.Path == "/projects/"+projectID+"/edit":
					fixture := strings.ReplaceAll(projectEditFixture, "/projects/p1", "/projects/"+projectID)
					fixture = strings.Replace(fixture, "Alpha Project", tc.want, 1)
					_, _ = io.WriteString(w, fixture)
				case r.Method == http.MethodPut && r.URL.Path == "/projects/"+projectID:
					puts++
					w.Header().Set("HX-Refresh", "true")
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			c, _ := client.New(srv.URL)
			if err := RunCLI(c, io.Discard, "", tc.args, false, false); err != nil {
				t.Fatal(err)
			}
			if puts != 1 {
				t.Fatalf("PUTs = %d", puts)
			}
		})
	}
}

func TestProjectsEditOptionBoundaryAmbiguityDoesNotRebind(t *testing.T) {
	projects := []client.Project{{ID: "short-id", Name: "Alpha"}, {ID: "long-id", Name: "Alpha --name Beta"}}
	for _, headless := range []bool{false, true} {
		name := "interactive"
		if headless {
			name = "headless"
		}
		t.Run(name, func(t *testing.T) {
			detailRequests, puts := 0, 0
			handler := func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/projects" {
					_, _ = io.WriteString(w, `{"projects":[{"id":"short-id","name":"Alpha"},{"id":"long-id","name":"Alpha --name Beta"}]}`)
					return
				}
				if r.Method == http.MethodGet {
					detailRequests++
				}
				if r.Method == http.MethodPut {
					puts++
				}
				http.NotFound(w, r)
			}
			if headless {
				srv := httptest.NewServer(http.HandlerFunc(handler))
				defer srv.Close()
				c, _ := client.New(srv.URL)
				err := RunCLI(c, io.Discard, "", []string{"projects", "edit", "Alpha", "--name", "Beta", "--description", "changed"}, false, false)
				if err == nil || !strings.Contains(err.Error(), "ambiguous across option boundaries") {
					t.Fatalf("error = %v", err)
				}
			} else {
				m := newModelFromHandler(t, handler)
				m.projects, m.projectsLoaded = projects, true
				m = runLine(t, m, `/projects edit Alpha --name Beta --description changed`)
				if out := transcript(m); !strings.Contains(out, "ambiguous across option boundaries") {
					t.Fatalf("transcript = %q", out)
				}
			}
			if detailRequests != 0 || puts != 0 {
				t.Fatalf("detail requests=%d PUTs=%d", detailRequests, puts)
			}
		})
	}
}

func TestProjectsEditLongBoundaryAmbiguityOutranksShortProject(t *testing.T) {
	requests := 0
	m := newModelFromHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusNotFound)
	})
	m.projects = []client.Project{
		{ID: "short-id", Name: "Alpha"},
		{ID: "long-1", Name: "Alpha --name Beta"},
		{ID: "long-2", Name: "Alpha --name Beta"},
	}
	m.projectsLoaded = true
	m = runLine(t, m, `/projects edit Alpha --name Beta --description changed`)
	if out := transcript(m); !strings.Contains(out, `"Alpha --name Beta" is ambiguous`) {
		t.Fatalf("stronger ambiguity lost: %q", out)
	}
	if requests != 0 {
		t.Fatalf("requests = %d", requests)
	}
}

func TestTerminalSafeProjectSettingsErrorPreservesAuthAndTransportTypes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/login")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL)
	_, authErr := c.GetProjectSettings(context.Background(), "p1")
	if safe := terminalSafeProjectSettingsError(authErr); !client.IsAuthRequired(safe) {
		t.Fatalf("auth type lost: %v", safe)
	}
	if safe := terminalSafeProjectSettingsError(context.DeadlineExceeded); !client.IsTransportError(safe) {
		t.Fatalf("transport type lost: %v", safe)
	}
}

func TestProjectsShowErrorsAreTerminalSafeInteractiveAndHeadless(t *testing.T) {
	unsafe := "denied\x1b[31m\x1b]0;owned\a\nretry\rnow\a"
	for _, tc := range []struct {
		name   string
		want   string
		server func(http.ResponseWriter)
	}{
		{name: "backend", want: "denied retry now", server: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": unsafe})
		}},
		{name: "parser", want: "invalid local repository path setting", server: func(w http.ResponseWriter) {
			fixture := strings.Replace(projectEditFixture, `data-local-repo-path-enabled="true"`, `data-local-repo-path-enabled="denied&#x1b;[31m&#x1b;]0;owned&#x7;&#xa;retry&#xd;now&#x7;"`, 1)
			_, _ = io.WriteString(w, fixture)
		}},
	} {
		t.Run(tc.name+" interactive", func(t *testing.T) {
			m := newModelFromHandler(t, func(w http.ResponseWriter, _ *http.Request) { tc.server(w) })
			m.projects = []client.Project{{ID: "p1", Name: "Alpha Project"}}
			m.projectsLoaded = true
			m = runLine(t, m, `/projects show p1`)
			assertTerminalSafeProjectShowError(t, transcript(m), tc.want)
		})
		t.Run(tc.name+" headless", func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/projects" {
					_, _ = io.WriteString(w, `{"projects":[{"id":"p1","name":"Alpha Project"}]}`)
					return
				}
				tc.server(w)
			}))
			defer srv.Close()
			c, _ := client.New(srv.URL)
			err := RunCLI(c, io.Discard, "", []string{"projects", "show", "p1"}, false, false)
			if err == nil {
				t.Fatal("expected show failure")
			}
			assertTerminalSafeProjectShowError(t, err.Error(), tc.want)
		})
	}
}

func assertTerminalSafeProjectShowError(t *testing.T, output, want string) {
	t.Helper()
	if !strings.Contains(output, want) {
		t.Fatalf("safe error text %q missing: %q", want, output)
	}
	for _, unsafe := range []string{"\x1b", "\a", "\nretry", "\r"} {
		if strings.Contains(output, unsafe) {
			t.Fatalf("unsafe show error output: %q", output)
		}
	}
}

func TestProjectsEditRejectsIncompleteFormBeforeMutation(t *testing.T) {
	puts := 0
	incomplete := strings.Replace(projectEditFixture, `<textarea name="description">kept</textarea>`, ``, 1)
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts++
		}
		_, _ = io.WriteString(w, incomplete)
	})
	m.projects = []client.Project{{ID: "p1", Name: "Alpha Project"}}
	m.projectsLoaded = true
	m = runLine(t, m, `/projects edit p1 --name Renamed`)
	if puts != 0 {
		t.Fatalf("PUTs = %d", puts)
	}
	if out := transcript(m); !strings.Contains(out, "required field description") {
		t.Fatalf("missing strict form error: %q", out)
	}
}

func TestProjectsEditRefreshesGitHubPathOmittedFromDisabledLocalForm(t *testing.T) {
	githubForm := strings.Replace(projectEditFixture, `data-local-repo-path-enabled="true"`, `data-local-repo-path-enabled="false"`, 1)
	githubForm = strings.Replace(githubForm, `<option value="local" selected>Local</option><option value="github">GitHub</option>`, `<option value="github" selected>GitHub</option>`, 1)
	githubForm = strings.Replace(githubForm, `<input name="repo_path" value="/tmp/alpha path">`, ``, 1)
	githubForm = strings.Replace(githubForm, `<input name="repo_url" value="">`, `<input name="repo_url" value="https://github.com/acme/repo">`, 1)
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/projects/p1/edit":
			_, _ = io.WriteString(w, githubForm)
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			_, _ = io.WriteString(w, `{"projects":[{"id":"p1","name":"Alpha Project","path":"/managed/github-checkout"}]}`)
		case r.Method == http.MethodPut && r.URL.Path == "/projects/p1":
			w.Header().Set("HX-Refresh", "true")
		default:
			http.NotFound(w, r)
		}
	})
	m.projects = []client.Project{{ID: "p1", Name: "Alpha Project", Path: "/managed/old"}}
	m.setActiveProject(m.projects[0])
	m = runLine(t, m, `/projects edit p1 --description updated`)
	if m.projects[0].Path != "/managed/github-checkout" {
		t.Fatalf("project path = %q", m.projects[0].Path)
	}
	if out := transcript(m); !strings.Contains(out, "/managed/github-checkout") {
		t.Fatalf("refreshed path missing from output: %q", out)
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
			source := "local"
			updated, err := applyProjectEdits(base, projectEditValues{RepositorySource: &source, RepositoryPath: &path})
			if err != nil {
				t.Fatal(err)
			}
			if updated.RepositorySource != "local" || updated.RepositoryPath != path {
				t.Fatalf("updated = %#v", updated)
			}
		})
	}
	name, description, source, githubURL, agent, workers := "Renamed Project", "", "github", "https://github.com/acme/repo", "Reviewer Agent", "0"
	updated, err := applyProjectEdits(base, projectEditValues{Name: &name, Description: &description, RepositorySource: &source, GitHubURL: &githubURL, DefaultAgent: &agent, MaxWorkers: &workers})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != name || updated.Description != "" || updated.RepositorySource != "github" || updated.GitHubURL != githubURL || updated.DefaultAgentID != "agent-2" || updated.MaxWorkers == nil || *updated.MaxWorkers != 0 {
		t.Fatalf("updated = %#v", updated)
	}
	unchanged, err := applyProjectEdits(base, projectEditValues{})
	if err != nil || unchanged.Name != base.Name || unchanged.Description != base.Description || unchanged.RepositoryPath != base.RepositoryPath || unchanged.DefaultAgentID != base.DefaultAgentID || unchanged.MaxWorkers == nil || *unchanged.MaxWorkers != 2 {
		t.Fatalf("omitted values changed: %#v, err=%v", unchanged, err)
	}
}

func TestApplyProjectEditsRejectsSourceIncompatibleRepositoryOptions(t *testing.T) {
	local := client.ProjectSettings{RepositorySource: "local", LocalRepositoryPathsEnabled: true}
	github := client.ProjectSettings{RepositorySource: "github", LocalRepositoryPathsEnabled: true}
	path, githubURL := "/tmp/repo", "https://github.com/acme/repo"
	for _, tc := range []struct {
		name     string
		settings client.ProjectSettings
		edits    projectEditValues
		want     string
	}{
		{name: "github URL for local source", settings: local, edits: projectEditValues{GitHubURL: &githubURL}, want: "requires repository source github"},
		{name: "local path for GitHub source", settings: github, edits: projectEditValues{RepositoryPath: &path}, want: "requires repository source local"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := applyProjectEdits(tc.settings, tc.edits); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestApplyProjectEditsMatchesRawAgentNamesButSanitizesDiagnostics(t *testing.T) {
	rawName := "Build\x1b[31m\nFast (mode)"
	settings := client.ProjectSettings{
		AvailableAgents: []client.ProjectAgentOption{
			{ID: "a1", Name: rawName},
			{ID: "a2", Name: "Build\x1b[31m\rOther"},
		},
	}
	updated, err := applyProjectEdits(settings, projectEditValues{DefaultAgent: &rawName})
	if err != nil || updated.DefaultAgentID != "a1" {
		t.Fatalf("raw exact match failed: updated=%#v err=%v", updated, err)
	}
	ref := "Build\x1b[31m"
	_, err = applyProjectEdits(settings, projectEditValues{DefaultAgent: &ref})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguity, got %v", err)
	}
	for _, unsafe := range []string{"\x1b", "\n", "\r"} {
		if strings.Contains(err.Error(), unsafe) {
			t.Fatalf("unsafe diagnostic %q", err)
		}
	}
}

func TestProjectsEditSanitizesBackendToastFailure(t *testing.T) {
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = io.WriteString(w, projectEditFixture)
		case http.MethodPut:
			w.Header().Set("HX-Trigger", `{"openvibelyToast":{"message":"clone denied\u001b[31m\nretry\rnow\u0007","status":"failed"}}`)
			w.WriteHeader(http.StatusNoContent)
		}
	})
	m.projects = []client.Project{{ID: "p1", Name: "Alpha Project"}}
	m.projectsLoaded = true
	m = runLine(t, m, `/projects edit p1 --name Renamed`)
	out := transcript(m)
	if !strings.Contains(out, "clone denied retry now") {
		t.Fatalf("safe error text missing: %q", out)
	}
	for _, unsafe := range []string{"\x1b", "\nretry", "\r", "\a"} {
		if strings.Contains(out, unsafe) {
			t.Fatalf("unsafe backend diagnostic %q", out)
		}
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
