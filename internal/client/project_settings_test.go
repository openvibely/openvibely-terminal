package client

import (
	"context"
	"encoding/json"
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

func TestGetProjectSettingsRejectsForeignOrIncompleteAuthoritativeForm(t *testing.T) {
	for _, tc := range []struct {
		name string
		html string
		want string
	}{
		{
			name: "foreign form",
			html: strings.Replace(projectSettingsHTML, `hx-put="/projects/p1"`, `hx-put="/projects/p2"`, 1),
			want: "requested project",
		},
		{
			name: "missing description",
			html: strings.Replace(projectSettingsHTML, `<textarea name="description">old description</textarea>`, ``, 1),
			want: "description",
		},
		{
			name: "duplicate description",
			html: strings.Replace(projectSettingsHTML, `<textarea name="description">old description</textarea>`, `<textarea name="description">old description</textarea><textarea name="description">stale</textarea>`, 1),
			want: "duplicate field description",
		},
		{
			name: "missing worker limit",
			html: strings.Replace(projectSettingsHTML, `<input name="max_workers" value="4">`, ``, 1),
			want: "max_workers",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.html))
			}))
			if _, err := c.GetProjectSettings(context.Background(), "p1"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestGetProjectSettingsHydratesOmittedGitHubPathFromProjectList(t *testing.T) {
	html := strings.Replace(projectSettingsHTML, `<input name="repo_path" value="/managed/old">`, ``, 1)
	html = strings.Replace(html, `data-local-repo-path-enabled="true"`, `data-local-repo-path-enabled="false"`, 1)
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/projects/p1/edit":
			_, _ = w.Write([]byte(html))
		case "/api/projects":
			_, _ = w.Write([]byte(`{"projects":[{"id":"p1","name":"Old Project","path":"/managed/hydrated"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	got, err := c.GetProjectSettings(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.RepositoryPath != "/managed/hydrated" {
		t.Fatalf("repository path = %q", got.RepositoryPath)
	}
}

func TestGetProjectSettingsPreservesParenthesesInAgentNames(t *testing.T) {
	html := strings.Replace(projectSettingsHTML, `Builder (openai/gpt)`, `Builder (fast) (openai/gpt)`, 1)
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(html))
	}))
	got, err := c.GetProjectSettings(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.DefaultAgentName != "Builder (fast)" || got.AvailableAgents[0].Name != "Builder (fast)" {
		t.Fatalf("agents = %#v default=%q", got.AvailableAgents, got.DefaultAgentName)
	}
}

func TestProjectSettingsMaxWorkersRoundTripDistinguishesZeroAndEmpty(t *testing.T) {
	for _, tc := range []struct {
		name     string
		value    string
		want     *int
		wantForm string
	}{
		{name: "explicit zero", value: "0", want: intPointer(0), wantForm: "0"},
		{name: "empty inherits", value: "", want: nil, wantForm: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			html := strings.Replace(projectSettingsHTML, `value="4"`, `value="`+tc.value+`"`, 1)
			var submitted url.Values
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					_, _ = w.Write([]byte(html))
				case http.MethodPut:
					if err := r.ParseForm(); err != nil {
						t.Fatalf("parse form: %v", err)
					}
					submitted = r.PostForm
					w.WriteHeader(http.StatusNoContent)
				default:
					http.NotFound(w, r)
				}
			}))

			got, err := c.GetProjectSettings(context.Background(), "p1")
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == nil {
				if got.MaxWorkers != nil {
					t.Fatalf("max workers = %v, want nil", *got.MaxWorkers)
				}
			} else if got.MaxWorkers == nil || *got.MaxWorkers != *tc.want {
				t.Fatalf("max workers = %v, want %d", got.MaxWorkers, *tc.want)
			}
			if err := c.UpdateProjectSettings(context.Background(), *got); err != nil {
				t.Fatal(err)
			}
			if submitted.Get("max_workers") != tc.wantForm {
				t.Fatalf("submitted max_workers = %q, want %q", submitted.Get("max_workers"), tc.wantForm)
			}
		})
	}
}

func TestProjectSettingsJSONPreservesExplicitZeroAndNull(t *testing.T) {
	zero, err := json.Marshal(ProjectSettings{MaxWorkers: intPointer(0)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(zero), `"max_workers":0`) {
		t.Fatalf("explicit zero JSON = %s", zero)
	}

	nilValue, err := json.Marshal(ProjectSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(nilValue), `"max_workers":null`) {
		t.Fatalf("nil max workers JSON = %s", nilValue)
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
