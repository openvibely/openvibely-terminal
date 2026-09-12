package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNewNormalizesURL(t *testing.T) {
	c, err := New("localhost:3001/")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, want := c.BaseURL(), "http://localhost:3001"; got != want {
		t.Errorf("BaseURL = %q, want %q", got, want)
	}

	if _, err := New("   "); err == nil {
		t.Error("expected error for empty URL")
	}
}

func TestNewNormalizesMixedCaseHTTPSSchemeForRequests(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/projects"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte(`{"projects":[]}`))
	}))
	defer srv.Close()

	mixedCaseURL := "hTtPs" + strings.TrimPrefix(srv.URL, "https")
	c, err := New(mixedCaseURL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.http = srv.Client()
	if got, want := c.BaseURL(), srv.URL; got != want {
		t.Fatalf("BaseURL = %q, want %q", got, want)
	}
	if _, err := c.ListProjects(context.Background()); err != nil {
		t.Fatalf("ListProjects through mixed-case HTTPS server URL: %v", err)
	}
}

func TestMalformedConfiguredServerURLsAreInvalidNotTransportFailures(t *testing.T) {
	for _, server := range []string{
		"https://ops.example/%zz",
		"https://[::1",
		"https://ops.example:99999",
	} {
		t.Run(server, func(t *testing.T) {
			c, err := New(server)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, err = c.GetGlobalCapacity(context.Background())
			if err == nil {
				t.Fatal("expected malformed configured server URL error")
			}
			if !IsInvalidServerURL(err) {
				t.Fatalf("error = %T %v, want invalid server URL classification", err, err)
			}
			if IsTransportError(err) || IsReachableError(err) || IsAuthRequired(err) {
				t.Fatalf("error = %v was assigned another connectivity classification", err)
			}
			if got, want := err.Error(), "invalid configured server URL"; got != want {
				t.Fatalf("error = %q, want %q", got, want)
			}
		})
	}
}

func TestListProjects(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"projects": []Project{{ID: "p1", Name: "Demo", Path: "/tmp/demo"}},
		})
	}))

	projects, err := c.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != "p1" || projects[0].Name != "Demo" {
		t.Errorf("unexpected projects: %+v", projects)
	}
}

func TestDeleteProjectUsesExactIDAndReturnsBackendSelection(t *testing.T) {
	var requests int
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodDelete || r.URL.Path != "/projects/project-delete" {
			t.Fatalf("request = %s %s, want DELETE /projects/project-delete", r.Method, r.URL.Path)
		}
		if r.Header.Get("HX-Request") != "true" {
			t.Fatal("DELETE request missing HX-Request header")
		}
		if got := r.Header.Get("Accept"); got != "text/html, application/json" {
			t.Fatalf("Accept = %q, want HTMX mutation accept header", got)
		}
		w.Header().Set("HX-Redirect", "/tasks?project_id=remaining-project")
		w.WriteHeader(http.StatusOK)
	}))

	selected, err := c.DeleteProjectWithSelection(context.Background(), "project-delete")
	if err != nil {
		t.Fatalf("DeleteProjectWithSelection: %v", err)
	}
	if selected != "remaining-project" {
		t.Fatalf("backend-selected project = %q, want remaining-project", selected)
	}
	if requests != 1 {
		t.Fatalf("DELETE requests = %d, want one", requests)
	}
}

func TestDeleteProjectAcceptsMissingSelectionHintAndLocation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		location string
		want     string
		htmx     bool
	}{
		{name: "missing hint", location: "", want: ""},
		{name: "ordinary redirect", location: "/tasks?project_id=location-project", want: "location-project"},
		{name: "unexpected redirect path is advisory", location: "/projects?project_id=ignored-project", want: ""},
		{name: "malformed redirect is advisory", location: "/tasks/%zz", want: "", htmx: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || r.URL.Path != "/projects/p1" {
					t.Fatalf("request = %s %s, want DELETE /projects/p1", r.Method, r.URL.Path)
				}
				if tc.location != "" {
					if tc.htmx {
						w.Header().Set("HX-Redirect", tc.location)
					} else {
						w.Header().Set("Location", tc.location)
					}
				}
				if tc.htmx {
					w.WriteHeader(http.StatusOK)
				} else {
					w.WriteHeader(http.StatusSeeOther)
				}
			}))

			selected, err := c.DeleteProjectWithSelection(context.Background(), "p1")
			if err != nil {
				t.Fatalf("DeleteProjectWithSelection: %v", err)
			}
			if selected != tc.want {
				t.Fatalf("backend-selected project = %q, want %q", selected, tc.want)
			}
		})
	}
}

func TestDeleteProjectValidationAndBackendDefaultProtection(t *testing.T) {
	requests := 0
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/projects/default" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"cannot delete the default project"}`)
	}))

	if err := c.DeleteProject(context.Background(), " "); err == nil || err.Error() != "project ID is required" {
		t.Fatalf("blank project ID error = %v, want local validation", err)
	}
	if requests != 0 {
		t.Fatalf("blank project ID made %d requests", requests)
	}
	if err := c.DeleteProject(context.Background(), "default"); err == nil || !strings.Contains(err.Error(), "cannot delete the default project") {
		t.Fatalf("default deletion error = %v", err)
	} else if !IsReachableError(err) || IsTransportError(err) || IsAuthRequired(err) {
		t.Fatalf("default deletion error classification = %v", err)
	}
	if requests != 1 {
		t.Fatalf("default deletion requests = %d, want one", requests)
	}
}

func TestDeleteProjectPreservesTransportClassification(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	baseURL := srv.URL
	srv.Close()

	c, err := New(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteProject(context.Background(), "p1"); err == nil || !IsTransportError(err) {
		t.Fatalf("closed-server delete error = %v, want transport error", err)
	}
}

func TestCreateProject(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/projects" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("HX-Request") != "true" {
			t.Errorf("missing HX-Request header")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got, want := r.FormValue("name"), "Terminal Project"; got != want {
			t.Errorf("name = %q, want %q", got, want)
		}
		if got, want := r.FormValue("repo_source"), "local"; got != want {
			t.Errorf("repo_source = %q, want %q", got, want)
		}
		if got, want := r.FormValue("repo_path"), `/Users/dev/terminal project`; got != want {
			t.Errorf("repo_path = %q, want %q", got, want)
		}
		w.Header().Set("HX-Redirect", "/tasks?project_id=created-1")
		w.WriteHeader(http.StatusNoContent)
	}))

	project, err := c.CreateProject(context.Background(), "  Terminal Project  ", "  /Users/dev/terminal project  ")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if project.ID != "created-1" || project.Name != "Terminal Project" || project.Path != "/Users/dev/terminal project" {
		t.Fatalf("unexpected project: %+v", project)
	}
}

func TestCreateProjectAcceptsLocationRedirect(t *testing.T) {
	var projectRequests, redirectedRequests int
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/projects":
			projectRequests++
			if r.Method != http.MethodPost {
				t.Errorf("project request method = %s, want POST", r.Method)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			if got, want := r.FormValue("name"), "Location Project"; got != want {
				t.Errorf("name = %q, want %q", got, want)
			}
			if got, want := r.FormValue("repo_path"), "/tmp/location-project"; got != want {
				t.Errorf("repo_path = %q, want %q", got, want)
			}
			w.Header().Set("Location", "/tasks?project_id=created-by-location")
			w.WriteHeader(http.StatusFound)
		case "/tasks":
			redirectedRequests++
			t.Errorf("project creation redirect was followed")
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	project, err := c.CreateProject(context.Background(), "  Location Project  ", "  /tmp/location-project  ")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if project == nil {
		t.Fatal("CreateProject returned nil project")
	}
	if project.ID != "created-by-location" || project.Name != "Location Project" || project.Path != "/tmp/location-project" {
		t.Fatalf("unexpected project: %+v", project)
	}
	if projectRequests != 1 || redirectedRequests != 0 {
		t.Fatalf("project requests = %d, followed redirects = %d", projectRequests, redirectedRequests)
	}
}

func TestCreateProjectRejectsInvalidRedirects(t *testing.T) {
	for _, tc := range []struct {
		name     string
		location string
		want     string
	}{
		{name: "missing location", location: "", want: "did not include a project ID"},
		{name: "missing ID", location: "/tasks", want: "did not include a project ID"},
		{name: "empty ID", location: "/tasks?project_id=   ", want: "did not include a project ID"},
		{name: "malformed ID", location: "/tasks?project_id=%zz", want: "did not include a project ID"},
		{name: "unrelated target", location: "/after-mutation?project_id=wrong-target", want: "unexpected backend redirect path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/projects" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				if tc.location != "" {
					w.Header().Set("Location", tc.location)
				}
				w.WriteHeader(http.StatusFound)
			}))

			project, err := c.CreateProject(context.Background(), "Project", "/tmp/project")
			if project != nil {
				t.Fatalf("invalid redirect returned project: %+v", project)
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("CreateProject error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestCreateProjectValidatesNameAndPathBeforeHTTP(t *testing.T) {
	requests := 0
	c := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{name: "", path: "/tmp/repo", want: "project name is required"},
		{name: "Project", path: "", want: "project path is required"},
		{name: "   ", path: " /tmp/repo ", want: "project name is required"},
		{name: "Project", path: "   ", want: "project path is required"},
	} {
		if _, err := c.CreateProject(context.Background(), tc.name, tc.path); err == nil || err.Error() != tc.want {
			t.Errorf("CreateProject(%q, %q) error = %v, want %q", tc.name, tc.path, err, tc.want)
		}
	}
	if requests != 0 {
		t.Errorf("validation made %d HTTP requests", requests)
	}
}

func TestCreateProjectBackendError(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/projects" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"project already exists"}`))
	}))

	_, err := c.CreateProject(context.Background(), "Project", "/tmp/repo")
	if err == nil || !strings.Contains(err.Error(), "project already exists") {
		t.Fatalf("CreateProject error = %v", err)
	}
}

func TestCreateProjectHTMXBackendError(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/projects" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("HX-Trigger", `{"openvibelyToast":{"message":"Local repository paths are disabled in this environment","status":"failed"}}`)
		w.WriteHeader(http.StatusNoContent)
	}))

	_, err := c.CreateProject(context.Background(), "Project", "/tmp/repo")
	if err == nil || !strings.Contains(err.Error(), "Local repository paths are disabled in this environment") {
		t.Fatalf("CreateProject error = %v", err)
	}
}

func TestCreateProjectReusesAuthenticatedSession(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			if r.FormValue("username") != "admin" || r.FormValue("password") != "secret" {
				t.Fatalf("unexpected login form: %v", r.Form)
			}
			http.SetCookie(w, &http.Cookie{Name: "ov_session", Value: "session-token"})
			w.Header().Set("Location", "/")
			w.WriteHeader(http.StatusFound)
		case "/projects":
			cookie, err := r.Cookie("ov_session")
			if err != nil || cookie.Value != "session-token" {
				t.Errorf("project request did not reuse login cookie: %v %v", cookie, err)
				w.Header().Set("Location", "/login")
				w.WriteHeader(http.StatusFound)
				return
			}
			w.Header().Set("HX-Redirect", "/tasks?project_id=authenticated-project")
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))

	if err := c.Login(context.Background(), "admin", "secret"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	project, err := c.CreateProject(context.Background(), "Authenticated Project", "C:\\Users\\dev\\repo")
	if err != nil {
		t.Fatalf("CreateProject after Login: %v", err)
	}
	if project.ID != "authenticated-project" {
		t.Errorf("project ID = %q", project.ID)
	}
}

func TestCreateProjectRejectsUnauthorizedRedirect(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/projects" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Location", "/login?next=%2Fprojects")
		w.WriteHeader(http.StatusFound)
	}))

	_, err := c.CreateProject(context.Background(), "Project", "/tmp/repo")
	if err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("CreateProject error = %v, want unauthorized", err)
	}
}

func TestCreateProjectNearLoginRedirectsAreNotAuthentication(t *testing.T) {
	for _, location := range []string{"/login-help", "/login2", "/after-mutation"} {
		location := location
		t.Run(location, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/projects" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Location", location)
				w.WriteHeader(http.StatusFound)
			}))

			_, err := c.CreateProject(context.Background(), "Project", "/tmp/repo")
			if err == nil || IsAuthRequired(err) {
				t.Fatalf("redirect %q error = %v, want ordinary mutation error", location, err)
			}
		})
	}
}

func TestSendChatMessageNearLoginRedirectsAreNotAuthentication(t *testing.T) {
	for _, location := range []string{"/login-help", "/login2"} {
		location := location
		t.Run(location, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", location)
				w.WriteHeader(http.StatusFound)
			}))

			_, err := c.SendChatMessage(context.Background(), "p1", "hello")
			if err == nil || IsAuthRequired(err) {
				t.Fatalf("redirect %q error = %v, want ordinary mutation error", location, err)
			}
		})
	}
}

func TestLoginNearLoginRedirectIsNotCredentialFailure(t *testing.T) {
	for _, location := range []string{"/login-help", "/login2"} {
		location := location
		t.Run(location, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", location)
				w.WriteHeader(http.StatusFound)
			}))

			err := c.Login(context.Background(), "admin", "secret")
			if err == nil || strings.Contains(err.Error(), "invalid credentials") {
				t.Fatalf("redirect %q was not reported as an ordinary login error: %v", location, err)
			}
			if IsAuthRequired(err) {
				t.Fatalf("redirect %q was classified as authentication: %v", location, err)
			}
		})
	}
}

func TestCreateProjectPreservesPlatformRepositoryPaths(t *testing.T) {
	for _, path := range []string{
		`/Users/dev/work tree`,
		`C:\Users\dev\work tree`,
		`\\server\share\work tree`,
	} {
		t.Run(path, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Fatalf("ParseForm: %v", err)
				}
				if got := r.FormValue("repo_path"); got != path {
					t.Errorf("repo_path = %q, want %q", got, path)
				}
				w.Header().Set("HX-Redirect", "/tasks?project_id=path-project")
				w.WriteHeader(http.StatusNoContent)
			}))
			project, err := c.CreateProject(context.Background(), "Path Project", path)
			if err != nil {
				t.Fatalf("CreateProject: %v", err)
			}
			if project.Path != path {
				t.Errorf("project path = %q, want %q", project.Path, path)
			}
		})
	}
}

func TestProjectStatusCountsUseCompactProjectScopedJSON(t *testing.T) {
	var requests []string
	var totalBytes int
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		if got := r.URL.Query().Get("project_id"); got != "project-2" {
			t.Fatalf("project_id = %q, want project-2", got)
		}
		var body string
		switch r.URL.Path {
		case "/api/alerts/pending-count":
			body = `{"count":17}`
		case "/api/tasks/status-counts":
			body = `{"active_tasks":23,"queued_tasks":11}`
		default:
			t.Fatalf("unexpected status-count path %q", r.URL.Path)
		}
		totalBytes += len(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))

	pending, err := c.GetPendingAlertCount(context.Background(), "project-2")
	if err != nil {
		t.Fatalf("GetPendingAlertCount: %v", err)
	}
	tasks, err := c.GetTaskStatusCounts(context.Background(), "project-2")
	if err != nil {
		t.Fatalf("GetTaskStatusCounts: %v", err)
	}
	if pending != 17 || tasks.ActiveTasks != 23 || tasks.QueuedTasks != 11 {
		t.Fatalf("compact counts = %d/%+v, want 17/{active_tasks:23 queued_tasks:11}", pending, tasks)
	}
	if len(requests) != 2 || totalBytes >= 1024 {
		t.Fatalf("compact requests/response bytes = %d/%d, want two small JSON responses: %v", len(requests), totalBytes, requests)
	}
}

func TestProjectStatusCountsDoNotAcceptAnUnscopedProject(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unscoped count request %s", r.URL.RequestURI())
	}))
	if _, err := c.GetPendingAlertCount(context.Background(), ""); err == nil {
		t.Fatal("GetPendingAlertCount accepted an empty project ID")
	}
	if _, err := c.GetTaskStatusCounts(context.Background(), " "); err == nil {
		t.Fatal("GetTaskStatusCounts accepted a blank project ID")
	}
}

func TestProjectStatusCountsPreserveAuthTransportAndCancellationErrors(t *testing.T) {
	tests := []struct {
		name  string
		setup func(http.ResponseWriter, *http.Request)
		call  func(*Client, context.Context) error
		check func(error) bool
	}{
		{
			name:  "authentication",
			setup: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
			call: func(c *Client, ctx context.Context) error {
				_, err := c.GetPendingAlertCount(ctx, "project-2")
				return err
			},
			check: IsAuthRequired,
		},
		{
			name: "cancellation",
			setup: func(w http.ResponseWriter, r *http.Request) {
				<-r.Context().Done()
			},
			call: func(c *Client, ctx context.Context) error {
				_, err := c.GetTaskStatusCounts(ctx, "project-2")
				return err
			},
			check: func(err error) bool { return errors.Is(err, context.Canceled) },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(tc.setup))
			ctx := context.Background()
			if tc.name == "cancellation" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			err := tc.call(c, ctx)
			if err == nil || !tc.check(err) {
				t.Fatalf("error = %v, classification check failed", err)
			}
		})
	}
}

func TestProjectStatusCountsPreserveTransportErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	baseURL := srv.URL
	srv.Close()

	c, err := New(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetPendingAlertCount(context.Background(), "project-2"); err == nil || !IsTransportError(err) {
		t.Fatalf("closed-server count error = %v, want transport error", err)
	}
}

func TestGetGlobalCapacity(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/capacity/global" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(GlobalCapacity{MaxWorkers: 5, TotalRunning: 2, AvailableSlots: 3, HasCapacity: true})
	}))

	cap, err := c.GetGlobalCapacity(context.Background())
	if err != nil {
		t.Fatalf("GetGlobalCapacity: %v", err)
	}
	if cap.MaxWorkers != 5 || cap.AvailableSlots != 3 {
		t.Errorf("unexpected capacity: %+v", cap)
	}
}

func TestReadErrorsClassifyReachableFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "http 500", body: `{"error":"capacity service failed"}`},
		{name: "malformed json", body: `{"has_capacity":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/capacity/global" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.name == "http 500" {
					w.WriteHeader(http.StatusInternalServerError)
				}
				_, _ = w.Write([]byte(tc.body))
			}))

			_, err := c.GetGlobalCapacity(context.Background())
			if err == nil {
				t.Fatal("expected health request error")
			}
			if !IsReachableError(err) {
				t.Fatalf("error = %v, want reachable backend classification", err)
			}
			if IsTransportError(err) || IsAuthRequired(err) {
				t.Fatalf("error = %v was classified as transport or authentication", err)
			}
		})
	}
}

func TestNotFoundErrorClassificationIsStatusSpecific(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   bool
	}{
		{status: http.StatusNotFound, want: true},
		{status: http.StatusInternalServerError, want: false},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":"request failed"}`))
			}))
			_, err := c.GetGlobalCapacity(context.Background())
			if err == nil {
				t.Fatal("expected status error")
			}
			if got := IsNotFoundError(fmt.Errorf("wrapped: %w", err)); got != tc.want {
				t.Fatalf("IsNotFoundError(%v) = %t, want %t", err, got, tc.want)
			}
		})
	}
}

func TestSendChatMessage(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat/message" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if r.FormValue("message") != "hello" || r.FormValue("project_id") != "p1" {
			t.Errorf("unexpected form: %v", r.Form)
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(ChatAccepted{MessageID: "exec1", Status: "processing"})
	}))

	accepted, err := c.SendChatMessage(context.Background(), "p1", "hello")
	if err != nil {
		t.Fatalf("SendChatMessage: %v", err)
	}
	if accepted.MessageID != "exec1" {
		t.Errorf("unexpected accepted: %+v", accepted)
	}
}

func TestSendChatMessageNonLoginRedirectIsNotAuthentication(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat/message" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Location", "/after-mutation")
		w.WriteHeader(http.StatusFound)
	}))

	_, err := c.SendChatMessage(context.Background(), "p1", "hello")
	if err == nil {
		t.Fatal("expected non-login redirect error")
	}
	if IsAuthRequired(err) {
		t.Fatalf("ordinary non-login redirect was classified as authentication: %v", err)
	}
}

func TestSendChatMessageServerError(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "project not found"})
	}))

	_, err := c.SendChatMessage(context.Background(), "missing", "hello")
	if err == nil {
		t.Fatal("expected error")
	}
	if want := "project not found"; !contains(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err.Error(), want)
	}
}

func TestGetChatStatus(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat/message/exec1" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(ChatStatus{MessageID: "exec1", Status: "completed", Response: "hi there"})
	}))

	status, err := c.GetChatStatus(context.Background(), "exec1")
	if err != nil {
		t.Fatalf("GetChatStatus: %v", err)
	}
	if status.Status != "completed" || status.Response != "hi there" {
		t.Errorf("unexpected status: %+v", status)
	}
}

func TestLoginSuccessAndFailure(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.FormValue("username") == "admin" && r.FormValue("password") == "secret" {
			http.SetCookie(w, &http.Cookie{Name: "ov_session", Value: "tok"})
			w.Header().Set("Location", "/")
			w.WriteHeader(http.StatusFound)
			return
		}
		w.Header().Set("Location", "/login?next=%2F")
		w.WriteHeader(http.StatusFound)
	}))

	if err := c.Login(context.Background(), "admin", "secret"); err != nil {
		t.Errorf("Login success case: %v", err)
	}
	if err := c.Login(context.Background(), "admin", "wrong"); err == nil {
		t.Error("expected login failure")
	}
}

func TestAuthMe(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(AuthStatus{Authenticated: true, Username: "admin"})
	}))

	auth, err := c.AuthMe(context.Background())
	if err != nil {
		t.Fatalf("AuthMe: %v", err)
	}
	if !auth.Authenticated || auth.Username != "admin" {
		t.Errorf("unexpected auth: %+v", auth)
	}
}

func TestStreamChatOutputParsesChunksAndTerminalEvents(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events/chat/exec-1" || r.URL.Query().Get("offset") != "7" {
			t.Fatalf("unexpected stream request: %s", r.URL.RequestURI())
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": keepalive\n\n")
		fmt.Fprint(w, "event: chunk  \ndata: hello\n: still the same frame\ndata:  world\n\n")
		fmt.Fprint(w, "event: done\ndata: completed\n\n")
		fmt.Fprint(w, "data: tail\ndata:  exact\n\n")
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, errs := c.StreamChatOutput(ctx, "exec-1", 7)
	if got := <-events; got.Name != "chunk" || got.Data != "hello\n world" {
		t.Fatalf("chunk = %#v", got)
	}
	if got := <-events; got.Name != "done" || got.Data != "completed" {
		t.Fatalf("done = %#v", got)
	}
	if got := <-events; got.Name != "" || got.Data != "tail\n exact" {
		t.Fatalf("reset unnamed chunk = %#v", got)
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("unexpected stream error: %v", err)
		}
	}
}

func TestSSEStreamsReportOversizedFrames(t *testing.T) {
	const oversizedDataBytes = 1024*1024 + 1
	for _, tc := range []struct {
		name        string
		path        string
		errorPrefix string
		stream      func(context.Context, *Client) (<-chan error, func())
	}{
		{
			name:        "chat output",
			path:        "/events/chat/exec",
			errorPrefix: "chat output stream read:",
			stream: func(ctx context.Context, c *Client) (<-chan error, func()) {
				events, errs := c.StreamChatOutput(ctx, "exec", 0)
				return errs, func() {
					for range events {
					}
				}
			},
		},
		{
			name:        "live events",
			path:        "/events/live",
			errorPrefix: "event stream read:",
			stream: func(ctx context.Context, c *Client) (<-chan error, func()) {
				events, errs := c.StreamEvents(ctx, "")
				return errs, func() {
					for range events {
					}
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Fatalf("path = %q, want %q", r.URL.Path, tc.path)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: "+strings.Repeat("x", oversizedDataBytes)+"\n\n")
			}))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			errs, drainEvents := tc.stream(ctx, c)
			drainEvents()
			err := <-errs
			if err == nil || !strings.Contains(err.Error(), tc.errorPrefix) {
				t.Fatalf("error = %v, want prefix %q", err, tc.errorPrefix)
			}
		})
	}
}

func TestStreamExecutionResumesFromByteOffsetAndParsesTerminalEvents(t *testing.T) {
	var gotOffset string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events/chat/exec-1" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		gotOffset = r.URL.Query().Get("offset")
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: 世界\n\n")
		flusher.Flush()
		fmt.Fprint(w, "event: done\ndata: completed\n\n")
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	events, errs := c.StreamExecution(context.Background(), "exec-1", 5)
	var got []ExecutionEvent
	for event := range events {
		got = append(got, event)
	}
	if err := <-errs; err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if gotOffset != "5" {
		t.Fatalf("offset = %q, want 5", gotOffset)
	}
	if len(got) != 2 || got[0].Type != ExecutionDelta || got[0].Data != "世界" || got[0].Offset != 11 || got[1].Type != ExecutionDone || got[1].Data != "completed" {
		t.Fatalf("events = %#v", got)
	}
}

func TestStreamExecutionReportsDisconnectCancellationAndAuthentication(t *testing.T) {
	t.Run("disconnect", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: partial\n\n")
		}))
		defer srv.Close()
		c, _ := New(srv.URL)
		events, errs := c.StreamExecution(context.Background(), "exec", 0)
		for range events {
		}
		if err := <-errs; !errors.Is(err, ErrEventStreamClosed) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		started := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			close(started)
			<-r.Context().Done()
		}))
		defer srv.Close()
		c, _ := New(srv.URL)
		ctx, cancel := context.WithCancel(context.Background())
		events, errs := c.StreamExecution(ctx, "exec", 0)
		<-started
		cancel()
		for range events {
		}
		if err := <-errs; err != nil {
			t.Fatalf("cancel error = %v", err)
		}
	})

	t.Run("auth", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", "/login")
			w.WriteHeader(http.StatusFound)
		}))
		defer srv.Close()
		c, _ := New(srv.URL)
		events, errs := c.StreamExecution(context.Background(), "exec", 0)
		for range events {
		}
		if err := <-errs; !IsAuthRequired(err) {
			t.Fatalf("error = %v, want auth required", err)
		}
	})
}

func TestStreamEvents(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events/live" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, ": ping\n\n")
		flusher.Flush()
		fmt.Fprint(w, "event: task_status_changed  \n")
		fmt.Fprint(w, `data:  {"type":"task_status_changed",`+"\n")
		fmt.Fprint(w, `data:  "task_id":"t1","status":"running"}  `+"\n\n")
		flusher.Flush()
		fmt.Fprint(w, `data: {"type":"chat_new_message","project_id":"p1","exec_id":"e1"}`+"\n\n")
		flusher.Flush()
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, errs := c.StreamEvents(ctx, "")

	ev1, ok := <-events
	if !ok {
		t.Fatal("expected first event")
	}
	if ev1.Name != "task_status_changed" {
		t.Errorf("event name = %q", ev1.Name)
	}
	var te TaskEvent
	if err := json.Unmarshal(ev1.Data, &te); err != nil || te.TaskID != "t1" || te.Status != "running" {
		t.Errorf("unexpected task event: %+v err=%v", te, err)
	}

	ev2, ok := <-events
	if !ok {
		t.Fatal("expected second event")
	}
	var ce ChatEvent
	if err := json.Unmarshal(ev2.Data, &ce); err != nil || ce.ExecID != "e1" {
		t.Errorf("unexpected chat event: %+v err=%v", ce, err)
	}

	// Server closes the stream: expect a terminal error reporting closure.
	select {
	case err := <-errs:
		if !errors.Is(err, ErrEventStreamClosed) {
			t.Errorf("stream error = %v, want ErrEventStreamClosed", err)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for stream end")
	}
}

func TestStreamEventsCancellation(t *testing.T) {
	blockCh := make(chan struct{})
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-blockCh
	}))
	defer close(blockCh)

	ctx, cancel := context.WithCancel(context.Background())
	events, errs := c.StreamEvents(ctx, "")
	time.Sleep(50 * time.Millisecond)
	cancel()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-events:
			if !ok {
				events = nil
			}
		case err, ok := <-errs:
			if !ok {
				return // clean shutdown, no terminal error
			}
			if err != nil {
				t.Errorf("expected nil error on cancellation, got %v", err)
			}
		case <-deadline:
			t.Fatal("stream did not shut down after cancellation")
		}
		if events == nil {
			return
		}
	}
}

func TestReadRequestsClassifyUnauthorizedHealthAndProjects(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusUnauthorized} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if status == http.StatusFound {
					w.Header().Set("Location", "/login?next="+r.URL.Path)
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte("password=do-not-render-this"))
			}))

			for name, call := range map[string]func() error{
				"health":   func() error { _, err := c.GetGlobalCapacity(context.Background()); return err },
				"projects": func() error { _, err := c.ListProjects(context.Background()); return err },
			} {
				t.Run(name, func(t *testing.T) {
					err := call()
					if err == nil {
						t.Fatal("expected authentication error")
					}
					if !IsAuthRequired(err) || !errors.Is(err, ErrAuthRequired) {
						t.Fatalf("error = %T %v, want auth-required", err, err)
					}
					var authErr *AuthRequiredError
					if !errors.As(err, &authErr) || authErr.StatusCode != status {
						t.Fatalf("error = %T %+v, want status %d", err, authErr, status)
					}
					if strings.Contains(err.Error(), "do-not-render-this") {
						t.Fatalf("auth error exposed response body: %v", err)
					}
				})
			}
		})
	}
}

func TestLoginFailureDoesNotExposeResponseBody(t *testing.T) {
	const secret = "not-a-login-secret"
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("password=" + secret))
	}))

	err := c.Login(context.Background(), "admin", secret)
	if err == nil || !strings.Contains(err.Error(), "invalid credentials") {
		t.Fatalf("Login error = %v, want generic invalid-credentials error", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Login error exposed password: %v", err)
	}
}

func TestLoginTransportFailureIsClassifiedWithoutCredentials(t *testing.T) {
	const secret = "transport-secret-that-must-not-appear"
	c, err := New("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}

	err = c.Login(context.Background(), "admin", secret)
	if err == nil || !IsLoginTransportError(err) {
		t.Fatalf("Login error = %v, want login transport error", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Login transport error exposed password: %v", err)
	}
}

func TestStreamEventsClassifiesUnauthorizedResponses(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusUnauthorized} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if status == http.StatusFound {
					w.Header().Set("Location", "/login")
				}
				w.WriteHeader(status)
			}))
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			events, errs := c.StreamEvents(ctx, "p1")

			select {
			case err := <-errs:
				if err == nil || !IsAuthRequired(err) {
					t.Fatalf("stream error = %v, want auth-required", err)
				}
			case <-ctx.Done():
				t.Fatal("timed out waiting for unauthorized stream response")
			}
			select {
			case _, ok := <-events:
				if ok {
					t.Fatal("unauthorized stream emitted an event")
				}
			case <-ctx.Done():
				t.Fatal("timed out waiting for stream close")
			}
		})
	}
}

// BenchmarkCompactStatusCountRetrieval records the allocation profile for the
// compact status contract at the same 100/1,000/5,000 card-equivalent workload
// sizes used by the status collection regression. The response is deliberately
// constant-size at every size; the benchmark must not scale with card history.
func BenchmarkCompactStatusCountRetrieval(b *testing.B) {
	for _, cards := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprintf("%d-card-equivalent", cards), func(b *testing.B) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/alerts/pending-count":
					_, _ = io.WriteString(w, `{"count":17}`)
				case "/api/tasks/status-counts":
					_, _ = io.WriteString(w, `{"active_tasks":23,"queued_tasks":11}`)
				default:
					b.Errorf("unexpected path %q", r.URL.Path)
				}
			}))
			defer srv.Close()
			c, err := New(srv.URL)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := c.GetPendingAlertCount(context.Background(), "project-2"); err != nil {
					b.Fatal(err)
				}
				if _, err := c.GetTaskStatusCounts(context.Background(), "project-2"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
