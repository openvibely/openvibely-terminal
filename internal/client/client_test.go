package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestStreamEvents(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events/live" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, ": ping\n\n")
		flusher.Flush()
		fmt.Fprint(w, "event: task_status_changed\n")
		fmt.Fprint(w, `data: {"type":"task_status_changed","task_id":"t1","status":"running"}`+"\n\n")
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
		if err == nil {
			t.Error("expected stream-closed error")
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

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
