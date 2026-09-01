package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-tui/internal/client"
)

// recorder is a stub backend that records the requests commands make.
type recorder struct {
	mu    sync.Mutex
	calls []string
	// urls keeps the full request URI (with query) so tests can assert which
	// project a command was scoped to.
	urls []string
	body func(path string) string
	// forms captures method, path, and posted form data for POST requests.
	forms []string
}

// sawForm reports whether any recorded form submission contained the given substring.
func (r *recorder) sawForm(substr string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range r.forms {
		if strings.Contains(f, substr) {
			return true
		}
	}
	return false
}

func (r *recorder) record(method, path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, method+" "+path)
}

// recordURL captures the full request URI including its query string.
func (r *recorder) recordURL(method, uri string) {
	r.record(method, strings.SplitN(uri, "?", 2)[0])
	r.mu.Lock()
	defer r.mu.Unlock()
	r.urls = append(r.urls, method+" "+uri)
}

// sawQuery reports whether any request URI contained the given substring.
func (r *recorder) sawQuery(substr string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, u := range r.urls {
		if strings.Contains(u, substr) {
			return true
		}
	}
	return false
}

func (r *recorder) urlsSnapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.urls...)
}

func (r *recorder) saw(method, path string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if c == method+" "+path {
			return true
		}
	}
	return false
}

func (r *recorder) count(method, path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	want := method + " " + path
	count := 0
	for _, c := range r.calls {
		if c == want {
			count++
		}
	}
	return count
}

func (r *recorder) all() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.calls, "\n")
}

// dispatchModel wires a model to a recording server with a project selected.
func dispatchModel(t *testing.T, bodies map[string]string) (Model, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.recordURL(r.Method, r.URL.RequestURI())
		_ = r.ParseForm()
		rec.mu.Lock()
		rec.forms = append(rec.forms, r.Method+" "+r.URL.Path+"?"+r.PostForm.Encode())
		rec.mu.Unlock()
		body, ok := bodies[r.Method+" "+r.URL.Path]
		if !ok {
			body, ok = bodies[r.URL.Path]
		}
		if ok {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(body))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.selectedID = "p1"
	m.selectedName = "demo"
	return m, rec
}

// dispatchVoteModel wires a selected-project model to one vote-record response,
// including non-2xx and malformed response cases.
func dispatchVoteModel(t *testing.T, status int, body string) (Model, *recorder) {
	t.Helper()
	const votePath = "/api/workflows/votes/step-1"
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.recordURL(r.Method, r.URL.RequestURI())
		if r.URL.Path != votePath {
			t.Errorf("unexpected vote path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.WriteHeader(status)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.selectedID = "p1"
	m.selectedName = "demo"
	return m, rec
}

func TestAgentsVotesInteractiveDispatchAndErrors(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		want       []string
		wantAuth   bool
		wantResult bool
	}{
		{
			name:   "success",
			status: http.StatusOK,
			body: `[{
				"id":"vote-b","step_execution_id":"step-1","agent_config_id":"agent-b","vote":"reject","confidence":0.42,"reasoning":"needs more evidence"
			},{
				"id":"vote-a","step_execution_id":"step-1","agent_config_id":"agent-a","vote":"approve","confidence":0.91,"reasoning":"checks passed"
			}]`,
			want:       []string{"Workflow votes", "step execution: step-1", "agent-a", "approve", "0.91", "checks passed", "agent-b", "reject", "0.42", "needs more evidence"},
			wantResult: true,
		},
		{
			name:       "empty",
			status:     http.StatusOK,
			body:       `[]`,
			want:       []string{"Workflow votes", "step execution: step-1", "no vote records available for this step execution"},
			wantResult: true,
		},
		{
			name:   "malformed",
			status: http.StatusOK,
			body:   `not-json`,
			want:   []string{"decoding /api/workflows/votes/step-1 response"},
		},
		{
			name:     "unauthorized",
			status:   http.StatusUnauthorized,
			body:     `{"error":"do not expose this body"}`,
			want:     []string{"requires sign-in", "/login"},
			wantAuth: true,
		},
		{
			name:   "server error",
			status: http.StatusServiceUnavailable,
			body:   `{"error":"vote service unavailable"}`,
			want:   []string{"server error (503): vote service unavailable"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchVoteModel(t, tc.status, tc.body)
			m = runLine(t, m, "/agents votes step-1")
			if got := rec.count("GET", "/api/workflows/votes/step-1"); got != 1 {
				t.Fatalf("vote inspection made %d route requests, want exactly one:\n%s", got, rec.all())
			}
			out := stripANSI(transcript(m))
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q:\n%s", want, out)
				}
			}
			if tc.wantResult && strings.Contains(out, "server error") {
				t.Errorf("successful vote inspection reported an error:\n%s", out)
			}
			if m.authRequired != tc.wantAuth {
				t.Errorf("authRequired = %t, want %t", m.authRequired, tc.wantAuth)
			}
		})
	}
}

func TestAgentsVotesInteractiveRejectsUnresolvedAndUnselectedRequests(t *testing.T) {
	m, rec := dispatchModel(t, nil)
	for _, line := range []string{"/agents votes", "/agents votes step-1 extra"} {
		m = runLine(t, m, line)
	}
	if got := rec.count("GET", "/api/workflows/votes/step-1"); got != 0 {
		t.Fatalf("invalid vote references made %d vote requests, want zero:\n%s", got, rec.all())
	}
	if out := stripANSI(transcript(m)); !strings.Contains(out, "usage: /agents votes <step-execution-id>") {
		t.Fatalf("unresolved vote reference did not report usage:\n%s", out)
	}

	m, rec = dispatchModel(t, nil)
	m.selectedID = ""
	m = runLine(t, m, "/agents votes step-1")
	if got := rec.count("GET", "/api/workflows/votes/step-1"); got != 0 {
		t.Fatalf("unselected vote inspection made %d requests, want zero:\n%s", got, rec.all())
	}
	if out := stripANSI(transcript(m)); !strings.Contains(out, "no project selected") {
		t.Fatalf("unselected vote inspection did not report project guidance:\n%s", out)
	}
}

// confirmDestructive simulates the two-step TUI confirmation flow for
// destructive commands: it types the command (which parks a pendingConfirmation
// instead of executing immediately) and then types "yes" to confirm execution.
func confirmDestructive(t *testing.T, m Model, line string) Model {
	t.Helper()
	m = runLine(t, m, line)     // step 1: sets pendingConfirmation
	return runLine(t, m, "yes") // step 2: confirms and executes
}

// runLine types a command and synchronously executes the command it returns,
// feeding the resulting message back into the model.
func runLine(t *testing.T, m Model, line string) Model {
	t.Helper()
	m, cmd := typeLine(t, m, line)
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			break
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, sub := range batch {
				if sub == nil {
					continue
				}
				if inner := sub(); inner != nil {
					next, _ := m.Update(inner)
					m = next.(Model)
				}
			}
			break
		}
		next, next2 := m.Update(msg)
		m = next.(Model)
		cmd = next2
		// Only follow one level of chaining to avoid polling loops.
		break
	}
	return m
}

func TestProjectsCreateSelectsCreatedProject(t *testing.T) {
	rec := &recorder{}
	requestURI := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.Method, r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/projects":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			if got, want := r.FormValue("name"), "My Project"; got != want {
				t.Errorf("name = %q, want %q", got, want)
			}
			if got, want := r.FormValue("repo_path"), `C:\Users\me\repo`; got != want {
				t.Errorf("repo_path = %q, want %q", got, want)
			}
			w.Header().Set("HX-Redirect", "/tasks?project_id=created-project")
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/events/live":
			select {
			case requestURI <- r.URL.RequestURI():
			default:
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, ": ping\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.selectedID = "old-project"
	m.selectedName = "Old Project"
	m.projects = []client.Project{{ID: "old-project", Name: "Old Project"}}
	m.threadID = "old-task"
	m.threadTitle = "Old Task"
	m.input.Placeholder = "Reply to Old Task (/chat to exit)"

	m = runLine(t, m, `/projects create My Project | C:\Users\me\repo`)
	defer m.Cleanup()
	if !rec.saw("POST", "/projects") {
		t.Fatalf("expected project creation request, calls:\n%s", rec.all())
	}
	if m.selectedID != "created-project" || m.selectedName != "My Project" {
		t.Fatalf("created project was not selected: id=%q name=%q", m.selectedID, m.selectedName)
	}
	if m.threadID != "" || m.threadTitle != "" {
		t.Fatalf("old thread survived project creation: id=%q title=%q", m.threadID, m.threadTitle)
	}
	if m.input.Placeholder != defaultPlaceholder {
		t.Fatalf("placeholder = %q, want default", m.input.Placeholder)
	}
	if !strings.Contains(transcript(m), "active project selected") || !strings.Contains(transcript(m), "My Project") {
		t.Fatalf("creation output did not explain selection:\n%s", transcript(m))
	}
	select {
	case got := <-requestURI:
		if got != "/events/live?project_id=created-project" {
			t.Fatalf("creation SSE request URI = %q, want scoped created project", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for scoped SSE request after project creation")
	}
}

func TestProjectsCreateSameIDPreservesThreadState(t *testing.T) {
	m, _ := dispatchModel(t, nil)
	m.threadID = "old-task"
	m.threadTitle = "Old Task"
	m.input.Placeholder = "Reply to Old Task (/chat to exit)"

	updated, _ := m.Update(projectCreatedMsg{
		project: client.Project{ID: "p1", Name: "Recreated Project", Path: "/tmp/recreated"},
	})
	m = updated.(Model)

	if m.selectedID != "p1" || m.selectedName != "Recreated Project" {
		t.Fatalf("created project was not selected: id=%q name=%q", m.selectedID, m.selectedName)
	}
	if m.threadID != "old-task" || m.threadTitle != "Old Task" {
		t.Fatalf("same-ID creation changed thread: id=%q title=%q", m.threadID, m.threadTitle)
	}
	if got, want := m.input.Placeholder, "Reply to Old Task (/chat to exit)"; got != want {
		t.Fatalf("same-ID creation changed placeholder: %q, want %q", got, want)
	}
	if len(m.projects) != 1 || m.projects[0].ID != "p1" {
		t.Fatalf("created project was not appended: %+v", m.projects)
	}
}

func TestEmptyProjectCommandsOfferCreationGuidance(t *testing.T) {
	for _, line := range []string{"/projects", "/project"} {
		t.Run(line, func(t *testing.T) {
			m, _ := dispatchModel(t, map[string]string{
				"/api/projects":          `{"projects":[]}`,
				"/api/capacity/projects": `[]`,
			})
			m.projects = nil
			m.selectedID = ""
			m.selectedName = ""

			m = runLine(t, m, line)
			out := stripANSI(transcript(m))
			if !strings.Contains(out, "/projects create <name> <path>") {
				t.Fatalf("empty %s state omitted creation guidance:\n%s", line, out)
			}
		})
	}
}

func TestProjectsCreateUsageErrorsDoNotCallBackend(t *testing.T) {
	for _, line := range []string{"/projects create", "/projects create demo"} {
		t.Run(line, func(t *testing.T) {
			m, rec := dispatchModel(t, nil)
			m = runLine(t, m, line)
			if rec.saw("POST", "/projects") {
				t.Fatalf("usage error made a backend request:\n%s", rec.all())
			}
			if !strings.Contains(strings.ToLower(transcript(m)), "usage") {
				t.Fatalf("expected usage error:\n%s", transcript(m))
			}
		})
	}
}

func TestProjectsCreateReportsBackendFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/projects" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"project creation rejected"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m = runLine(t, m, "/projects create demo /tmp/demo")
	if !strings.Contains(transcript(m), "project creation rejected") {
		t.Fatalf("backend error missing from transcript:\n%s", transcript(m))
	}
	if m.selectedID != "" {
		t.Fatalf("failed creation selected project %q", m.selectedID)
	}
}

// Mirrors real card markup: kebab menu first, title in the card's task link.
const taskBoardHTML = `<div>
  <div class="card" data-task-id="t-1" data-task-status="pending" data-task-category="backlog" data-display-order="0">
    <div class="dropdown"><ul><li><button hx-post="/tasks/t-1/run">Run</button></li>
      <li><a hx-get="/tasks/t-1?tab=details&amp;from=tasks">Edit</a></li></ul></div>
    <div class="card-body">
      <a href="/tasks/t-1?from=tasks" title="Refactor the API">Refactor the API</a>
      <p class="line-clamp-2">split handlers</p>
    </div>
  </div>
</div>`

func TestTasksCommandListsRealBoard(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
	m = runLine(t, m, "/tasks")

	if !rec.saw("GET", "/tasks") {
		t.Fatalf("expected a board fetch, calls:\n%s", rec.all())
	}
	out := transcript(m)
	if !strings.Contains(out, "Refactor the API") {
		t.Errorf("board content missing:\n%s", out)
	}
	if strings.Contains(out, "error:") {
		t.Errorf("/tasks should not error:\n%s", out)
	}
}

func TestTasksRunResolvesTaskByTitle(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
	m = runLine(t, m, "/tasks run Refactor")

	if !rec.saw("POST", "/tasks/t-1/run") {
		t.Fatalf("expected a run call, calls:\n%s", rec.all())
	}
	if strings.Contains(transcript(m), "error:") {
		t.Errorf("unexpected error:\n%s", transcript(m))
	}
}

func TestTasksRunRejectsCaseInsensitiveDuplicateExactTitles(t *testing.T) {
	const duplicateTitlesHTML = `<div>
	  <div class="card" data-task-id="t-1" data-task-status="pending" data-task-category="backlog">
	    <a href="/tasks/t-1" title="Deploy">Deploy</a>
	  </div>
	  <div class="card" data-task-id="t-2" data-task-status="pending" data-task-category="backlog">
	    <a href="/tasks/t-2" title="deploy">deploy</a>
	  </div>
	</div>`
	m, rec := dispatchModel(t, map[string]string{"/tasks": duplicateTitlesHTML})
	m = runLine(t, m, "/tasks run DEPLOY")

	if rec.saw("POST", "/tasks/t-1/run") || rec.saw("POST", "/tasks/t-2/run") {
		t.Fatalf("ambiguous exact task title dispatched a run:\n%s", rec.all())
	}
	if !strings.Contains(strings.ToLower(transcript(m)), "ambiguous") {
		t.Fatalf("expected an ambiguous task-title error:\n%s", transcript(m))
	}
}

func TestTasksRunResolvesQuotedTaskTitle(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
	m = runLine(t, m, `/tasks run "Refactor the API"`)

	if rec.count("POST", "/tasks/t-1/run") != 1 {
		t.Fatalf("expected one run call, calls:\n%s", rec.all())
	}
	if strings.Contains(transcript(m), "nothing matches") {
		t.Fatalf("quoted task title should resolve:\n%s", transcript(m))
	}
}

func TestTasksDeleteAndMoveChainArguments(t *testing.T) {
	t.Run("delete", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		confirmDestructive(t, m, "/tasks delete t-1")
		if !rec.saw("DELETE", "/tasks/t-1") {
			t.Errorf("calls:\n%s", rec.all())
		}
	})
	t.Run("move", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		runLine(t, m, "/tasks move Refactor active")
		if !rec.saw("PATCH", "/tasks/t-1/category") {
			t.Errorf("calls:\n%s", rec.all())
		}
	})
	t.Run("show", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		runLine(t, m, "/tasks show t-1")
		if !rec.saw("GET", "/tasks/t-1") {
			t.Errorf("calls:\n%s", rec.all())
		}
	})
	t.Run("new", func(t *testing.T) {
		m, rec := dispatchModel(t, nil)
		runLine(t, m, "/tasks new Ship the release")
		if !rec.saw("POST", "/tasks") {
			t.Errorf("calls:\n%s", rec.all())
		}
	})
	t.Run("show with trailing tab word by id", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		m = runLine(t, m, "/tasks show t-1 thread")
		if !rec.saw("GET", "/tasks/t-1") {
			t.Errorf("calls:\n%s", rec.all())
		}
		if strings.Contains(transcript(m), "nothing matches") {
			t.Errorf("expected task to resolve, got error transcript:\n%s", transcript(m))
		}
	})
	t.Run("show with trailing tab word by multi-word title", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		m = runLine(t, m, "/tasks show Refactor the API thread")
		if !rec.saw("GET", "/tasks/t-1") {
			t.Errorf("calls:\n%s", rec.all())
		}
		out := transcript(m)
		if strings.Contains(out, "nothing matches") {
			t.Errorf("expected task to resolve, got error transcript:\n%s", out)
		}
	})
	for alias, wantPath := range map[string]string{
		"chat":     "/tasks/t-1",
		"diff":     "/tasks/t-1",
		"reviews":  "/tasks/t-1/reviews",
		"schedule": "/tasks/t-1",
		"chain":    "/tasks/t-1",
		"attach":   "/tasks/t-1",
	} {
		t.Run("show accepts tab alias "+alias, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
			m = runLine(t, m, "/tasks show Refactor the API "+alias)
			if !rec.saw("GET", wantPath) {
				t.Errorf("calls:\n%s", rec.all())
			}
			out := transcript(m)
			if strings.Contains(out, "nothing matches") {
				t.Errorf("expected task to resolve, got error transcript:\n%s", out)
			}
		})
	}
}

func TestTasksShowSurfacesLazyFailuresAndPartialOutput(t *testing.T) {
	tests := []struct {
		name        string
		line        string
		failedPath  string
		failedBody  string
		wantFailure string
		wantSuccess string
	}{
		{
			name:        "thread single tab",
			line:        "/tasks show t-1 thread",
			failedPath:  "/tasks/t-1/thread",
			failedBody:  `{"error":"thread backend failed"}`,
			wantFailure: "failed to load thread",
			wantSuccess: "Refactor the API",
		}, {
			name:        "changes full detail",
			line:        "/tasks show t-1",
			failedPath:  "/tasks/t-1/changes",
			failedBody:  `{"error":"changes backend failed"}`,
			wantFailure: "failed to load changes",
			wantSuccess: "thread loaded",
		},
		{
			name:        "lifecycle full detail",
			line:        "/tasks show t-1",
			failedPath:  "/api/tasks/t-1/lifecycle-executions",
			failedBody:  `{"error":"lifecycle backend failed"}`,
			wantFailure: "failed to load lifecycle",
			wantSuccess: "thread loaded",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tc.failedPath {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(tc.failedBody))
					return
				}
				switch r.URL.Path {
				case "/tasks":
					w.Header().Set("Content-Type", "text/html")
					_, _ = w.Write([]byte(taskBoardHTML))
				case "/tasks/t-1":
					w.Header().Set("Content-Type", "text/html")
					_, _ = w.Write([]byte(`<div data-task-status="running" data-task-category="active">
						<h2 class="font-bold">Task</h2>
						<div id="tab-details">details loaded</div>
						<div id="tab-chat"></div>
						<div id="tab-changes"></div>
					</div>`))
				case "/tasks/t-1/thread":
					w.Header().Set("Content-Type", "text/html")
					_, _ = w.Write([]byte(`<div>thread loaded</div>`))
				case "/tasks/t-1/changes":
					w.Header().Set("Content-Type", "text/html")
					_, _ = w.Write([]byte(`<div>changes loaded</div>`))
				case "/api/tasks/t-1/lifecycle-executions":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`[]`))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(srv.Close)

			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			m := New(c)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			m = updated.(Model)
			m.selectedID = "p1"
			m.selectedName = "demo"
			m = runLine(t, m, tc.line)

			out := stripANSI(transcript(m))
			if !strings.Contains(out, tc.wantFailure) {
				t.Fatalf("lazy failure was not visible:\n%s", out)
			}
			if !strings.Contains(out, tc.wantSuccess) {
				t.Fatalf("successful sibling output was lost:\n%s", out)
			}
			if strings.Contains(out, "(empty)") {
				t.Fatalf("failed section was rendered as empty:\n%s", out)
			}
			if !strings.Contains(out, "backend failed") {
				t.Fatalf("backend error detail was not visible:\n%s", out)
			}
		})
	}
}

func TestTasksShowLazyAuthFailureRemainsVisibleAndMarksSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tasks":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(taskBoardHTML))
		case "/tasks/t-1":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div data-task-status="running" data-task-category="active">
				<h2 class="font-bold">Task</h2>
				<div id="tab-details">details loaded</div>
				<div id="tab-chat"></div>
				<div id="tab-changes"></div>
			</div>`))
		case "/tasks/t-1/thread":
			w.Header().Set("Location", "/login?next=%2Ftasks%2Ft-1%2Fthread")
			w.WriteHeader(http.StatusFound)
		case "/tasks/t-1/changes":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div>changes loaded</div>`))
		case "/api/tasks/t-1/lifecycle-executions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.selectedID = "p1"
	m.selectedName = "demo"
	m = runLine(t, m, "/tasks show t-1 thread")

	out := stripANSI(transcript(m))
	if !m.authRequired || m.connected {
		t.Fatalf("lazy auth failure did not update connection state: authRequired=%t connected=%t", m.authRequired, m.connected)
	}
	for _, want := range []string{"failed to load thread", "authentication required", "requires sign-in", "Refactor the API"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "(empty)") {
		t.Fatalf("auth failure was rendered as empty:\n%s", out)
	}
}

func TestTasksShowSuccessfulEmptyLazyFragmentRendersEmptyState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tasks":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(taskBoardHTML))
		case "/tasks/t-1":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div data-task-status="running" data-task-category="active"><h2 class="font-bold">Task</h2><div id="tab-details">details loaded</div><div id="tab-chat"></div><div id="tab-changes"></div></div>`))
		case "/tasks/t-1/thread", "/tasks/t-1/changes":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div></div>`))
		case "/api/tasks/t-1/lifecycle-executions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.selectedID = "p1"
	m.selectedName = "demo"
	m = runLine(t, m, "/tasks show t-1 thread")

	out := stripANSI(transcript(m))
	if !strings.Contains(out, "(empty)") {
		t.Fatalf("successful empty fragment did not render its empty state:\n%s", out)
	}
	if strings.Contains(out, "failed to load") || strings.Contains(out, "error:") {
		t.Fatalf("successful empty fragment was treated as a failure:\n%s", out)
	}
}

func TestTasksLifecycleRendersOrderedEvents(t *testing.T) {
	const executions = `[{"id":"exec-1","skill_key":"router","when":"post_task","status":"completed","started_at":"2026-01-20T10:00:00Z"}]`
	const events = `[
		{"id":"event-2","seq":2,"event_type":"completed","payload":{"message":"second"},"created_at":"2026-01-20T10:00:02Z"},
		{"id":"event-1","seq":1,"event_type":"started","payload":{"message":"first"},"created_at":"2026-01-20T10:00:01Z"}
	]`
	m, rec := dispatchModel(t, map[string]string{
		"/tasks":                                  taskBoardHTML,
		"/api/tasks/t-1/lifecycle-executions":     executions,
		"/api/lifecycle-executions/exec-1/events": events,
	})
	m = runLine(t, m, "/tasks lifecycle Refactor the API")

	if !rec.saw("GET", "/api/tasks/t-1/lifecycle-executions") {
		t.Fatalf("expected lifecycle execution list, calls:\n%s", rec.all())
	}
	if !rec.saw("GET", "/api/lifecycle-executions/exec-1/events") {
		t.Fatalf("expected lifecycle event fetch, calls:\n%s", rec.all())
	}
	out := transcript(m)
	for _, want := range []string{"SEQ", "TIMESTAMP", "EVENT TYPE", "router", "started", "completed", `{"message":"first"}`} {
		if !strings.Contains(out, want) {
			t.Errorf("lifecycle output missing %q:\n%s", want, out)
		}
	}
	if first, second := strings.Index(out, `{"message":"first"}`), strings.Index(out, `{"message":"second"}`); first < 0 || second < 0 || first > second {
		t.Errorf("events were not rendered in sequence order:\n%s", out)
	}
}

func TestTasksLifecycleAliasResolvesExplicitExecution(t *testing.T) {
	const executions = `[{"id":"exec-1","skill_key":"router","status":"running"}]`
	const events = `[{"id":"event-1","seq":1,"event_type":"started","payload":{},"created_at":"2026-01-20T10:00:01Z"}]`
	m, rec := dispatchModel(t, map[string]string{
		"/tasks":                                  taskBoardHTML,
		"/api/tasks/t-1/lifecycle-executions":     executions,
		"/api/lifecycle-executions/exec-1/events": events,
	})
	m = runLine(t, m, "/tasks logs Refactor the API exec-1")
	if !rec.saw("GET", "/api/lifecycle-executions/exec-1/events") {
		t.Fatalf("logs alias did not fetch the explicit execution:\n%s", rec.all())
	}
	if strings.Contains(transcript(m), "nothing matches") {
		t.Fatalf("unexpected resolution error:\n%s", transcript(m))
	}
}

func TestTasksLifecycleNoExecutionsDoesNotFetchEvents(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{
		"/tasks":                              taskBoardHTML,
		"/api/tasks/t-1/lifecycle-executions": "[]",
	})
	m = runLine(t, m, "/tasks lifecycle Refactor")
	if rec.saw("GET", "/api/lifecycle-executions/e-1/events") {
		t.Error("event endpoint must not be fetched when there are no executions")
	}
	if !strings.Contains(transcript(m), "no executions") {
		t.Fatalf("expected empty execution state:\n%s", transcript(m))
	}
}

func TestTasksLifecycleNoEventsRendersEmptyState(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{
		"/tasks":                                  taskBoardHTML,
		"/api/tasks/t-1/lifecycle-executions":     `[{"id":"exec-1","skill_key":"router","status":"completed"}]`,
		"/api/lifecycle-executions/exec-1/events": "[]",
	})
	m = runLine(t, m, "/tasks lifecycle Refactor exec-1")
	if !rec.saw("GET", "/api/lifecycle-executions/exec-1/events") {
		t.Fatalf("expected lifecycle event fetch, calls:\n%s", rec.all())
	}
	if !strings.Contains(transcript(m), "no events for this execution") {
		t.Fatalf("expected empty event state:\n%s", transcript(m))
	}
}

func TestTasksLifecycleMultipleExecutionsOpenSelector(t *testing.T) {
	const executions = `[
		{"id":"exec-1","skill_key":"router","status":"completed","started_at":"2026-01-20T10:00:00Z"},
		{"id":"exec-2","skill_key":"reviewer","status":"failed","started_at":"2026-01-20T11:00:00Z"}
	]`
	m, rec := dispatchModel(t, map[string]string{
		"/tasks":                              taskBoardHTML,
		"/api/tasks/t-1/lifecycle-executions": executions,
	})
	m = runLine(t, m, "/tasks lifecycle Refactor")
	if !m.selectorActive {
		t.Fatalf("expected execution selector:\n%s", transcript(m))
	}
	if m.pendingCommand != "tasks lifecycle t-1" {
		t.Errorf("pending command = %q, want %q", m.pendingCommand, "tasks lifecycle t-1")
	}
	if len(m.selectorItems) != 2 {
		t.Fatalf("selector items = %d, want 2", len(m.selectorItems))
	}
	if rec.saw("GET", "/api/lifecycle-executions/exec-1/events") || rec.saw("GET", "/api/lifecycle-executions/exec-2/events") {
		t.Error("multiple executions must wait for selector choice")
	}
}

func TestTasksLifecycleRejectsAmbiguousTaskAndExecutionRefs(t *testing.T) {
	const ambiguousTasks = `<div>
		<div data-task-id="t-1" data-task-status="pending" data-task-category="backlog"><a href="/tasks/t-1" title="Deploy API">Deploy API</a></div>
		<div data-task-id="t-2" data-task-status="pending" data-task-category="backlog"><a href="/tasks/t-2" title="Deploy Web">Deploy Web</a></div>
	</div>`
	t.Run("task", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/tasks": ambiguousTasks})
		m = runLine(t, m, "/tasks lifecycle Deploy")
		if !strings.Contains(transcript(m), "ambiguous") {
			t.Fatalf("expected ambiguous task error:\n%s", transcript(m))
		}
		if rec.saw("GET", "/api/tasks/t-1/lifecycle-executions") || rec.saw("GET", "/api/tasks/t-2/lifecycle-executions") {
			t.Error("ambiguous task must not fetch lifecycle executions")
		}
	})

	t.Run("execution", func(t *testing.T) {
		const executions = `[{"id":"exec-api","skill_key":"router"},{"id":"exec-agent","skill_key":"reviewer"}]`
		m, rec := dispatchModel(t, map[string]string{
			"/tasks":                              taskBoardHTML,
			"/api/tasks/t-1/lifecycle-executions": executions,
		})
		m = runLine(t, m, "/tasks lifecycle t-1 exec")
		if !strings.Contains(transcript(m), "ambiguous") {
			t.Fatalf("expected ambiguous execution error:\n%s", transcript(m))
		}
		if rec.saw("GET", "/api/lifecycle-executions/exec-api/events") || rec.saw("GET", "/api/lifecycle-executions/exec-agent/events") {
			t.Error("ambiguous execution must not fetch events")
		}
	})
}

func TestTasksLifecycleEventBackendError(t *testing.T) {
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/tasks":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(taskBoardHTML))
		case "/api/tasks/t-1/lifecycle-executions":
			_, _ = w.Write([]byte(`[{"id":"exec-1","skill_key":"router"}]`))
		case "/api/lifecycle-executions/exec-1/events":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"event trace unavailable"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	})
	m = runLine(t, m, "/tasks lifecycle t-1 exec-1")
	if !strings.Contains(transcript(m), "event trace unavailable") {
		t.Fatalf("expected backend event error:\n%s", transcript(m))
	}
}

func TestTasksNewEmptyTitleFromPipeInput(t *testing.T) {
	t.Run("pipe_only_is_rejected", func(t *testing.T) {
		m, rec := dispatchModel(t, nil)
		m = runLine(t, m, "/tasks new | Write tests for the login module")
		out := transcript(m)
		if !strings.Contains(strings.ToLower(out), "usage") {
			t.Errorf("expected usage error, got:\n%s", out)
		}
		if rec.saw("POST", "/tasks") {
			t.Errorf("CreateTask must NOT be called when title is empty, calls:\n%s", rec.all())
		}
	})

	t.Run("title_with_pipe_succeeds", func(t *testing.T) {
		m, rec := dispatchModel(t, nil)
		runLine(t, m, "/tasks new My task | some description")
		if !rec.saw("POST", "/tasks") {
			t.Errorf("expected CreateTask call, calls:\n%s", rec.all())
		}
	})

	t.Run("title_without_pipe_succeeds", func(t *testing.T) {
		m, rec := dispatchModel(t, nil)
		runLine(t, m, "/tasks new My task")
		if !rec.saw("POST", "/tasks") {
			t.Errorf("expected CreateTask call, calls:\n%s", rec.all())
		}
	})
}

func TestSkillsAddEmptyNameFromPipeInput(t *testing.T) {
	const skillsHTML = `<div data-skill-handle="my-skill" data-skill-name="My Skill"
		data-skill-enabled="true" data-skill-always-use="false" data-skill-scope="project"></div>`

	t.Run("pipe_only_is_rejected", func(t *testing.T) {
		m, rec := dispatchModel(t, nil)
		m = runLine(t, m, "/skills add | some description")
		out := transcript(m)
		if !strings.Contains(strings.ToLower(out), "usage") {
			t.Errorf("expected usage error, got:\n%s", out)
		}
		if rec.saw("POST", "/skills") {
			t.Errorf("CreateSkill must NOT be called when name is empty, calls:\n%s", rec.all())
		}
	})

	t.Run("name_with_pipe_succeeds", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/skills": skillsHTML})
		runLine(t, m, "/skills add my-skill | description | body")
		if !rec.saw("POST", "/skills") {
			t.Errorf("expected CreateSkill call, calls:\n%s", rec.all())
		}
	})

	t.Run("name_without_pipe_succeeds", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/skills": skillsHTML})
		runLine(t, m, "/skills add my-skill")
		if !rec.saw("POST", "/skills") {
			t.Errorf("expected CreateSkill call, calls:\n%s", rec.all())
		}
	})
}

func TestAlertsCommandChainsDelete(t *testing.T) {
	const alertsHTML = `<div class="card" data-alert-id="a-1" data-alert-scroll-anchor="a-1"
	  data-search-text="build failed">
	  <p class="font-semibold">Build failed</p>
	</div>`
	m, rec := dispatchModel(t, map[string]string{"/alerts": alertsHTML})

	m = runLine(t, m, "/alerts")
	if !rec.saw("GET", "/alerts") {
		t.Fatalf("expected an alerts fetch:\n%s", rec.all())
	}
	if !strings.Contains(transcript(m), "Build failed") {
		t.Errorf("alerts missing:\n%s", transcript(m))
	}

	m = runLine(t, m, "/alerts delete a-1") // sets pendingConfirmation
	runLine(t, m, "yes")                    // confirms and executes
	if !rec.saw("DELETE", "/alerts/a-1") {
		t.Errorf("expected a delete call:\n%s", rec.all())
	}
}

func TestSkillsCommandAddAndToggle(t *testing.T) {
	const skillsHTML = `<div data-skill-handle="deploy" data-skill-name="Deploy"
		data-skill-enabled="true" data-skill-always-use="false" data-skill-scope="project"></div>`

	t.Run("add", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/skills": skillsHTML})
		runLine(t, m, "/skills add release-notes | writes release notes")
		if !rec.saw("POST", "/skills") {
			t.Errorf("calls:\n%s", rec.all())
		}
	})

	t.Run("disable", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/skills": skillsHTML})
		runLine(t, m, "/skills disable deploy")
		if !rec.saw("POST", "/skills/deploy/enabled") {
			t.Errorf("calls:\n%s", rec.all())
		}
	})

	t.Run("delete", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/skills": skillsHTML})
		confirmDestructive(t, m, "/skills delete deploy")
		if !rec.saw("DELETE", "/skills/deploy") {
			t.Errorf("calls:\n%s", rec.all())
		}
	})
}

func TestAnalyticsInteractiveRequiresProjectBeforeDispatch(t *testing.T) {
	lines := []string{
		"/analytics",
		"/analytics usage",
		"/analytics rates",
		"/analytics agents",
		"/analytics frequent",
		"/analytics failures",
		"/analytics skills",
		"/analytics trends",
	}
	for _, line := range lines {
		t.Run(strings.TrimPrefix(line, "/"), func(t *testing.T) {
			m, rec := dispatchModel(t, nil)
			m.selectedID = ""

			m, cmd := typeLine(t, m, line)
			if cmd != nil {
				// Execute an accidentally returned command so this regression also
				// observes any requests started after dispatch.
				_ = cmd()
			}
			if cmd != nil {
				t.Errorf("unselected analytics command returned pending work")
			}
			if m.busy {
				t.Errorf("unselected analytics command left the model busy")
			}
			for _, uri := range rec.urlsSnapshot() {
				if strings.Contains(uri, "/api/analytics/") {
					t.Errorf("unselected analytics command made request %q", uri)
				}
			}
			if out := stripANSI(transcript(m)); !strings.Contains(out, "no project selected — use /project <name>") {
				t.Fatalf("unselected analytics command did not report project guidance:\n%s", out)
			}
		})
	}
}

func TestAnalyticsInteractiveDispatchPropagatesSelectedProject(t *testing.T) {
	const projectID = "selected-project"
	bodies := map[string]string{
		"/api/analytics/usage":                       `{"totals":{"call_count":1}}`,
		"/api/analytics/success-failure-rates":       `[]`,
		"/api/analytics/avg-execution-time-by-agent": `[]`,
		"/api/analytics/avg-execution-time-by-task":  `[]`,
		"/api/analytics/most-frequent-tasks":         `[]`,
		"/api/analytics/failed-task-patterns":        `[]`,
		"/api/analytics/skills":                      `{}`,
	}
	allPaths := []string{
		"/api/analytics/usage",
		"/api/analytics/success-failure-rates",
		"/api/analytics/avg-execution-time-by-agent",
		"/api/analytics/avg-execution-time-by-task",
		"/api/analytics/most-frequent-tasks",
		"/api/analytics/failed-task-patterns",
		"/api/analytics/skills",
	}
	cases := []struct {
		line  string
		paths []string
	}{
		{line: "/analytics", paths: allPaths},
		{line: "/analytics usage", paths: []string{"/api/analytics/usage"}},
		{line: "/analytics rates", paths: []string{"/api/analytics/success-failure-rates"}},
		{line: "/analytics agents", paths: []string{"/api/analytics/avg-execution-time-by-agent"}},
		{line: "/analytics frequent", paths: []string{"/api/analytics/most-frequent-tasks"}},
		{line: "/analytics failures", paths: []string{"/api/analytics/failed-task-patterns"}},
		{line: "/analytics skills", paths: []string{"/api/analytics/skills"}},
		{line: "/analytics trends", paths: []string{"/api/analytics/avg-execution-time-by-task"}},
	}

	for _, tc := range cases {
		t.Run(strings.TrimPrefix(tc.line, "/"), func(t *testing.T) {
			m, rec := dispatchModel(t, bodies)
			m.selectedID = projectID
			_ = runLine(t, m, tc.line)

			want := make(map[string]int, len(tc.paths))
			for _, path := range tc.paths {
				want[path]++
			}
			got := make(map[string]int, len(tc.paths))
			for _, uri := range rec.urlsSnapshot() {
				parts := strings.SplitN(uri, " ", 2)
				if len(parts) != 2 {
					t.Fatalf("malformed recorded request %q", uri)
				}
				request := strings.SplitN(parts[1], "?", 2)
				if !strings.Contains(parts[1], "project_id="+projectID) {
					t.Errorf("analytics request lost selected project scope: %q", uri)
				}
				got[request[0]]++
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("analytics endpoints = %v, want %v; requests:\n%s", got, want, rec.all())
			}
		})
	}
}

func TestAnalyticsUsageDispatchRendersProviderLimitsAndScope(t *testing.T) {
	const secret = "sk-analytics-account-detail"
	m, rec := dispatchModel(t, map[string]string{
		"/api/analytics/usage": `{
			"totals":{"call_count":8,"input_tokens":1000,"output_tokens":500,"total_tokens":1500,"cost_usd":1.25,"cost_available":true},
			"model_breakdown":[{"model":"claude-sonnet-4","call_count":8,"total_tokens":1500,"cost_usd":1.25,"percent":100}],
			"account_limits":[
				{"provider":"OpenAI","plan_type":"team","status_label":"healthy","account_detail":"` + secret + `",
				 "primary_limit":{"label":"tokens","used_percent":100,"resets_at":"2026-09-01T00:00:00Z"}},
				{"provider":"OpenAI","plan_type":"team","status_label":"healthy",
				 "primary_limit":{"label":"requests","used_percent":42.5,"resets_at":"tomorrow"},
				 "limits":[{"label":"requests","used_percent":42.5,"resets_at":"tomorrow"},{"label":"images","used_percent":12.5,"resets_at":"next week"}]},
				{"provider":"Anthropic","plan_type":"pro","status_label":"healthy",
				 "limits":[{"label":"tokens","used_percent":12,"resets_at":"later"}]},
				{"provider":"Gemini","plan_type":"free","status_label":"blocked","error":"quota service unavailable"}
			]
		}`,
	})
	m = runLine(t, m, "/analytics usage")
	out := stripANSI(transcript(m))

	for _, want := range []string{
		"8 calls", "$1.25", "claude-sonnet-4", "OpenAI", "team", "healthy",
		"tokens", "100.0%", "2026-09-01T00:00:00Z", "requests", "42.5%", "tomorrow",
		"images", "12.5%", "next week", "Anthropic", "pro", "later",
		"Gemini", "free", "blocked", "quota service unavailable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("analytics usage output missing %q:\n%s", want, out)
		}
	}
	if got := strings.Count(out, "    requests"); got != 1 {
		t.Errorf("duplicate primary limit rendered %d times, want once:\n%s", got, out)
	}
	if strings.Contains(out, secret) {
		t.Errorf("account detail leaked into analytics usage output:\n%s", out)
	}
	if !rec.sawQuery("project_id=p1") {
		t.Errorf("analytics usage request lost selected project scope:\n%s", rec.all())
	}
}

func TestScreenCommandsHitTheirEndpoints(t *testing.T) {
	cases := []struct{ line, method, path string }{
		{"/schedule", "GET", "/schedule"},
		{"/models", "GET", "/models"},
		{"/agents", "GET", "/agents"},
		{"/workers", "GET", "/workers"},
		{"/channels", "GET", "/channels"},
		{"/personality", "GET", "/personality"},
		{"/pulse", "GET", "/upcoming"},
		{"/reflection", "GET", "/history"},
		{"/insights", "GET", "/insights"},
		{"/automations", "GET", "/automations"},
		{"/grades", "GET", "/history"},
		{"/build", "POST", "/api/autonomous/trigger"},
		{"/models capacity", "GET", "/api/capacity/models"},
		{"/agents metrics", "GET", "/api/workflows/metrics"},
		{"/analytics usage", "GET", "/api/analytics/usage"},
		{"/analytics rates", "GET", "/api/analytics/success-failure-rates"},
		{"/analytics skills", "GET", "/api/analytics/skills"},
		{"/analytics frequent", "GET", "/api/analytics/most-frequent-tasks"},
		{"/analytics failures", "GET", "/api/analytics/failed-task-patterns"},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			m, rec := dispatchModel(t, nil)
			runLine(t, m, tc.line)
			if !rec.saw(tc.method, tc.path) {
				t.Errorf("%s should call %s %s, calls:\n%s", tc.line, tc.method, tc.path, rec.all())
			}
		})
	}
}

func TestModelsCapacityRendersCapacityOnlyResponse(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{
		"/api/capacity/models": `[{"name":"Sonnet","running":1,"max_workers":4,"available_slots":3}]`,
		"/api/analytics/usage": `{"totals":{"call_count":1}}`,
	})
	m = runLine(t, m, "/models capacity")

	out := stripANSI(transcript(m))
	for _, want := range []string{"Sonnet", "1", "4", "3", "provider limits unavailable", "/analytics usage"} {
		if !strings.Contains(out, want) {
			t.Errorf("capacity-only response missing %q:\n%s", want, out)
		}
	}
	if !rec.saw("GET", "/api/capacity/models") || !rec.saw("GET", "/api/analytics/usage") {
		t.Errorf("expected capacity and best-effort usage requests:\n%s", rec.all())
	}
}

func TestModelsCapacityAccountFetchFailureDoesNotHideCapacity(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.Method, r.URL.Path)
		switch r.URL.Path {
		case "/api/capacity/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"name":"Haiku","running":0,"max_workers":2,"available_slots":2}]`))
		case "/api/analytics/usage":
			http.Error(w, "provider quota service is down", http.StatusServiceUnavailable)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.selectedID = "p1"
	m.selectedName = "demo"

	m = runLine(t, m, "/models capacity")
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "Haiku") || !strings.Contains(out, "provider limits unavailable") {
		t.Errorf("capacity should survive account-fetch failure:\n%s", out)
	}
	if strings.Contains(out, "provider quota service is down") {
		t.Errorf("account-fetch diagnostic should not replace the helpful hint:\n%s", out)
	}
	if !rec.saw("GET", "/api/capacity/models") || !rec.saw("GET", "/api/analytics/usage") {
		t.Errorf("expected both requests:\n%s", rec.all())
	}
}

func TestModelsCapacityRequestsOverlap(t *testing.T) {
	const delay = 150 * time.Millisecond
	var mu sync.Mutex
	active, maxActive := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()
		defer func() {
			mu.Lock()
			active--
			mu.Unlock()
		}()

		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/capacity/models":
			_, _ = w.Write([]byte(`[{"name":"Sonnet","running":1,"max_workers":4,"available_slots":3}]`))
		case "/api/analytics/usage":
			_, _ = w.Write([]byte(`{"account_limits":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.selectedID = "p1"

	start := time.Now()
	m = runLine(t, m, "/models capacity")
	elapsed := time.Since(start)

	mu.Lock()
	gotMaxActive := maxActive
	mu.Unlock()
	if gotMaxActive < 2 {
		t.Errorf("capacity and usage requests did not overlap; max active handlers = %d", gotMaxActive)
	}
	if elapsed >= 275*time.Millisecond {
		t.Errorf("capacity command took %v; expected roughly one %v delay rather than sequential %v", elapsed, delay, 2*delay)
	}
}

func TestModelsCapacityBothSuccessPreservesOutputAndScope(t *testing.T) {
	var mu sync.Mutex
	counts := map[string]int{}
	var usageProjects []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		counts[r.URL.Path]++
		if r.URL.Path == "/api/analytics/usage" {
			usageProjects = append(usageProjects, r.URL.Query().Get("project_id"))
		}
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/capacity/models":
			_, _ = w.Write([]byte(`[
				{"id":"m1","name":"First","model":"first-model","running":1,"max_workers":4,"available_slots":3},
				{"id":"m2","name":"Second","model":"second-model","running":3,"max_workers":4,"available_slots":1}
			]`))
		case "/api/analytics/usage":
			_, _ = w.Write([]byte(`{
				"account_limits":[{
					"provider":"OpenAI",
					"plan_type":"team",
					"status_label":"healthy",
					"account_detail":"sk-private-test-value",
					"primary_limit":{"label":"requests","status":"healthy","used_percent":42.5,"resets_at":"tomorrow"}
				}]
			}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.selectedID = "selected-project"

	m = runLine(t, m, "/models capacity")
	out := stripANSI(transcript(m))

	mu.Lock()
	capCount := counts["/api/capacity/models"]
	usageCount := counts["/api/analytics/usage"]
	gotUsageProjects := append([]string(nil), usageProjects...)
	mu.Unlock()
	if capCount != 1 || usageCount != 1 {
		t.Errorf("expected exactly one request per endpoint, got capacity=%d usage=%d", capCount, usageCount)
	}
	if len(gotUsageProjects) != 1 || gotUsageProjects[0] != "selected-project" {
		t.Errorf("usage project scope = %v, want [selected-project]", gotUsageProjects)
	}

	for _, want := range []string{"First", "Second", "Provider limits", "OpenAI", "team", "healthy", "42.5%", "tomorrow"} {
		if !strings.Contains(out, want) {
			t.Errorf("successful capacity output missing %q:\n%s", want, out)
		}
	}
	first := strings.Index(out, "First")
	second := strings.Index(out, "Second")
	provider := strings.Index(out, "Provider limits")
	if first < 0 || second < 0 || provider < 0 || first >= second || second >= provider {
		t.Errorf("capacity/provider output order changed:\n%s", out)
	}
	if strings.Contains(out, "sk-private-test-value") {
		t.Errorf("provider/account detail leaked into capacity output:\n%s", out)
	}
}

func TestModelsCapacityUsageFailureKeepsCapacityAndFallback(t *testing.T) {
	var mu sync.Mutex
	counts := map[string]int{}
	var usageProjects []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		counts[r.URL.Path]++
		if r.URL.Path == "/api/analytics/usage" {
			usageProjects = append(usageProjects, r.URL.Query().Get("project_id"))
		}
		mu.Unlock()

		switch r.URL.Path {
		case "/api/capacity/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"name":"Haiku","running":0,"max_workers":2,"available_slots":2}]`))
		case "/api/analytics/usage":
			http.Error(w, "provider quota service is down", http.StatusServiceUnavailable)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.selectedID = "selected-project"

	m = runLine(t, m, "/models capacity")
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "Haiku") || !strings.Contains(out, "provider limits unavailable") {
		t.Fatalf("capacity should survive usage failure with fallback:\n%s", out)
	}
	if strings.Contains(out, "error:") {
		t.Errorf("usage failure should not become a command error:\n%s", out)
	}

	mu.Lock()
	capCount := counts["/api/capacity/models"]
	usageCount := counts["/api/analytics/usage"]
	gotUsageProjects := append([]string(nil), usageProjects...)
	mu.Unlock()
	if capCount != 1 || usageCount != 1 {
		t.Errorf("expected exactly one request per endpoint, got capacity=%d usage=%d", capCount, usageCount)
	}
	if len(gotUsageProjects) != 1 || gotUsageProjects[0] != "selected-project" {
		t.Errorf("usage project scope = %v, want [selected-project]", gotUsageProjects)
	}
}

func TestModelsCapacityFailureRemainsFatal(t *testing.T) {
	var mu sync.Mutex
	counts := map[string]int{}
	var usageProjects []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		counts[r.URL.Path]++
		if r.URL.Path == "/api/analytics/usage" {
			usageProjects = append(usageProjects, r.URL.Query().Get("project_id"))
		}
		mu.Unlock()

		switch r.URL.Path {
		case "/api/capacity/models":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"capacity service is down"}`))
		case "/api/analytics/usage":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"account_limits":[{"provider":"OpenAI","status_label":"healthy"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.selectedID = "selected-project"

	m = runLine(t, m, "/models capacity")
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "capacity service is down") {
		t.Fatalf("capacity failure should be returned as a command error:\n%s", out)
	}
	if strings.Contains(out, "Provider limits") {
		t.Errorf("capacity failure should not render a partial capacity result:\n%s", out)
	}

	mu.Lock()
	capCount := counts["/api/capacity/models"]
	usageCount := counts["/api/analytics/usage"]
	gotUsageProjects := append([]string(nil), usageProjects...)
	mu.Unlock()
	if capCount != 1 || usageCount != 1 {
		t.Errorf("expected exactly one request per endpoint, got capacity=%d usage=%d", capCount, usageCount)
	}
	if len(gotUsageProjects) != 1 || gotUsageProjects[0] != "selected-project" {
		t.Errorf("usage project scope = %v, want [selected-project]", gotUsageProjects)
	}
}

func TestFetchModelCapacityWithUsageRespectsCanceledContext(t *testing.T) {
	started := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/capacity/models", "/api/analytics/usage":
			started <- r.URL.Path
			<-r.Context().Done()
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, _, err := fetchModelCapacityWithUsage(ctx, c, "selected-project")
		done <- err
	}()

	seen := map[string]int{}
	for i := 0; i < 2; i++ {
		select {
		case path := <-started:
			seen[path]++
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for both capacity and usage requests")
		}
	}
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled capacity request should remain fatal")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled capacity and usage requests did not return promptly")
	}
	if seen["/api/capacity/models"] != 1 || seen["/api/analytics/usage"] != 1 {
		t.Errorf("started requests = %v, want one request per endpoint", seen)
	}
}

func TestWorkersLimitValidatesArgument(t *testing.T) {
	m, rec := dispatchModel(t, nil)
	m = runLine(t, m, "/workers limit abc")
	if rec.saw("POST", "/workers") {
		t.Error("a non-numeric limit must not be sent")
	}
	if !strings.Contains(transcript(m), "positive number") {
		t.Errorf("expected a validation message:\n%s", transcript(m))
	}

	m2, rec2 := dispatchModel(t, nil)
	runLine(t, m2, "/workers limit 6")
	if !rec2.saw("POST", "/workers") {
		t.Errorf("valid limit should be sent:\n%s", rec2.all())
	}

	m3, rec3 := dispatchModel(t, nil)
	m3 = runLine(t, m3, "/workers limit -1")
	if rec3.saw("POST", "/workers") {
		t.Error("a negative limit must not be sent")
	}
	if !strings.Contains(transcript(m3), "positive number") {
		t.Errorf("expected a validation message:\n%s", transcript(m3))
	}
}

func TestWorkersLimitZeroMeansUnlimited(t *testing.T) {
	m, rec := dispatchModel(t, nil)
	m = runLine(t, m, "/workers limit 0")
	if !rec.saw("POST", "/workers") {
		t.Errorf("zero limit should be sent:\n%s", rec.all())
	}
	if !rec.sawForm("max_workers=0") {
		t.Errorf("expected max_workers=0 in form data:\n%v", rec.forms)
	}
	if !strings.Contains(transcript(m), "unlimited") {
		t.Errorf("expected an unlimited status message:\n%s", transcript(m))
	}
}

func TestCommandsRequiringProjectReportMissingSelection(t *testing.T) {
	m, rec := dispatchModel(t, nil)
	m.selectedID = ""
	m.selectedName = ""

	m = runLine(t, m, "/tasks")
	if !strings.Contains(transcript(m), "no project selected") {
		t.Errorf("expected a project warning:\n%s", transcript(m))
	}
	if rec.saw("GET", "/tasks") {
		t.Error("no request should be made without a project")
	}
}

func TestAutomationsCommandsRequireSelectedProject(t *testing.T) {
	cases := []string{
		"/automations",
		"/automations run-now au-1",
		"/automations pause au-1",
		"/automations resume au-1",
		"/automations delete au-1",
		"/automations delete",
	}
	for _, line := range cases {
		line := line
		t.Run(line, func(t *testing.T) {
			m, rec := dispatchModel(t, nil)
			m.selectedID = ""
			m.selectedName = ""

			m = runLine(t, m, line)
			out := transcript(m)
			if !strings.Contains(out, "no project selected") {
				t.Fatalf("expected no-project error for %s:\n%s", line, out)
			}
			if calls := rec.all(); calls != "" {
				t.Fatalf("%s must not make backend requests:\n%s", line, calls)
			}
			if m.selectorActive {
				t.Errorf("%s must not open the selector", line)
			}
			if m.pendingConfirmation != nil {
				t.Errorf("%s must not set pending confirmation", line)
			}
		})
	}
}

func TestUsageErrorsSurfaceInTranscript(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"database is down"}`))
	}))
	defer srv.Close()

	c, _ := client.New(srv.URL)
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.selectedID = "p1"

	m = runLine(t, m, "/tasks")
	if !strings.Contains(transcript(m), "database is down") {
		t.Errorf("server errors must reach the user:\n%s", transcript(m))
	}
}

func TestTaskEditAndOrder(t *testing.T) {
	t.Run("edit", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		runLine(t, m, "/tasks edit Refactor | Refactor the handlers")
		if !rec.saw("PUT", "/tasks/t-1") {
			t.Errorf("calls:\n%s", rec.all())
		}
	})
	t.Run("order", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		runLine(t, m, "/tasks order Refactor 2")
		if !rec.saw("PATCH", "/tasks/t-1/reorder") {
			t.Errorf("calls:\n%s", rec.all())
		}
	})
	t.Run("goal", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		runLine(t, m, "/tasks goal Refactor | all tests pass")
		if !rec.saw("POST", "/tasks/t-1/goal") {
			t.Errorf("calls:\n%s", rec.all())
		}
	})
	t.Run("reply", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		runLine(t, m, "/tasks reply Refactor | also update the docs")
		if !rec.saw("POST", "/tasks/t-1/thread") {
			t.Errorf("calls:\n%s", rec.all())
		}
	})
}

// TestTasksGoalAndReplyRequirePipe verifies that omitting the | separator in
// /tasks goal and /tasks reply produces a clear error rather than silently
// mis-splitting a multi-word task title.
func TestTasksGoalAndReplyRequirePipe(t *testing.T) {
	t.Run("goal_multiword_no_pipe_is_rejected", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		m = runLine(t, m, "/tasks goal Refactor the API all tests pass")
		// Must not have called the goal endpoint.
		if rec.saw("POST", "/tasks/t-1/goal") {
			t.Errorf("expected no goal POST on pipe-less input, but got one")
		}
		// Must surface a helpful error message.
		out := transcript(m)
		if !strings.Contains(out, "|") {
			t.Errorf("expected error mentioning '|' separator, got:\n%s", out)
		}
	})

	t.Run("reply_multiword_no_pipe_is_rejected", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		m = runLine(t, m, "/tasks reply Refactor the API also update the docs")
		// Must not have called the thread endpoint.
		if rec.saw("POST", "/tasks/t-1/thread") {
			t.Errorf("expected no thread POST on pipe-less input, but got one")
		}
		// Must surface a helpful error message.
		out := transcript(m)
		if !strings.Contains(out, "|") {
			t.Errorf("expected error mentioning '|' separator, got:\n%s", out)
		}
	})

	t.Run("goal_with_pipe_still_works", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		runLine(t, m, "/tasks goal Refactor the API | all tests pass")
		if !rec.saw("POST", "/tasks/t-1/goal") {
			t.Errorf("expected goal POST with pipe syntax, calls:\n%s", rec.all())
		}
	})

	t.Run("reply_with_pipe_still_works", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		runLine(t, m, "/tasks reply Refactor the API | also update the docs")
		if !rec.saw("POST", "/tasks/t-1/thread") {
			t.Errorf("expected thread POST with pipe syntax, calls:\n%s", rec.all())
		}
	})
}

func TestTasksEditEmptyTitleFromDoublePipe(t *testing.T) {
	t.Run("double_pipe_empty_title_is_rejected", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		m = runLine(t, m, "/tasks edit Refactor | | new description")
		out := transcript(m)
		if !strings.Contains(out, "|") {
			t.Errorf("expected usage error containing '|', got:\n%s", out)
		}
		if rec.saw("PUT", "/tasks/t-1") {
			t.Errorf("UpdateTask must NOT be called when title is empty, calls:\n%s", rec.all())
		}
	})

	t.Run("valid_title_and_prompt_succeeds", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		runLine(t, m, "/tasks edit Refactor | New Title | new prompt")
		if !rec.saw("PUT", "/tasks/t-1") {
			t.Errorf("expected UpdateTask call, calls:\n%s", rec.all())
		}
	})

	t.Run("title_only_no_prompt_succeeds", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		runLine(t, m, "/tasks edit Refactor | New Title")
		if !rec.saw("PUT", "/tasks/t-1") {
			t.Errorf("expected UpdateTask call, calls:\n%s", rec.all())
		}
	})
}

func TestWorkersProjectLimit(t *testing.T) {
	m, rec := dispatchModel(t, nil)
	runLine(t, m, "/workers project 3")
	if !rec.saw("POST", "/workers/projects/p1/limit") {
		t.Errorf("calls:\n%s", rec.all())
	}
}

func TestWorkersProjectLimitZeroMeansNoLimit(t *testing.T) {
	m, rec := dispatchModel(t, nil)
	m = runLine(t, m, "/workers project 0")
	if !rec.saw("POST", "/workers/projects/p1/limit") {
		t.Errorf("calls:\n%s", rec.all())
	}
	if !rec.sawForm("max_workers=0") {
		t.Errorf("expected max_workers=0 in form data:\n%v", rec.forms)
	}
	if !strings.Contains(transcript(m), "no limit") && !strings.Contains(transcript(m), "unlimited") {
		t.Errorf("expected a no-limit/unlimited status message:\n%s", transcript(m))
	}
}

func TestWorkersProjectLimitValidatesArgument(t *testing.T) {
	for _, value := range []string{"abc", "-1"} {
		t.Run(value, func(t *testing.T) {
			m, rec := dispatchModel(t, nil)
			m = runLine(t, m, "/workers project "+value)
			if rec.saw("POST", "/workers/projects/p1/limit") {
				t.Errorf("invalid project limit %q must not be sent:\n%s", value, rec.all())
			}
			if !strings.Contains(transcript(m), "positive number") {
				t.Errorf("expected a validation message:\n%s", transcript(m))
			}
		})
	}
}

func TestWorkersLimitOperandValidationDispatch(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantPost  bool
		wantForm  string
		wantError string
	}{
		{name: "empty", input: `""`, wantError: "positive number"},
		{name: "nonnumeric", input: "abc", wantError: "positive number"},
		{name: "negative", input: "-1", wantError: "positive number"},
		{name: "overflow 2^63", input: "9223372036854775808", wantError: "positive number"},
		{name: "overflow max uint64", input: "18446744073709551615", wantError: "positive number"},
		{name: "overflow 2^64", input: "18446744073709551616", wantError: "positive number"},
		{name: "positive", input: "6", wantPost: true, wantForm: "max_workers=6"},
		{name: "zero", input: "0", wantPost: true, wantForm: "max_workers=0"},
		{name: "surplus operand", input: "6 extra", wantError: "usage"},
	}

	for _, action := range []string{"limit", "project"} {
		action := action
		for _, tc := range cases {
			tc := tc
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				m, rec := dispatchModel(t, nil)
				m = runLine(t, m, "/workers "+action+" "+tc.input)

				path := "/workers"
				if action == "project" {
					path = "/workers/projects/p1/limit"
				}
				if tc.wantPost {
					if got := rec.count("POST", path); got != 1 {
						t.Fatalf("valid worker limit should make exactly one POST, got %d:\n%s", got, rec.all())
					}
					if !rec.sawForm(tc.wantForm) {
						t.Fatalf("expected %s in form data:\n%v", tc.wantForm, rec.forms)
					}
					if tc.input == "0" {
						if action == "project" && !strings.Contains(transcript(m), "no limit") && !strings.Contains(transcript(m), "unlimited") {
							t.Errorf("project zero should retain the no-limit message:\n%s", transcript(m))
						}
						if action == "limit" && !strings.Contains(transcript(m), "unlimited") {
							t.Errorf("global zero should retain the unlimited message:\n%s", transcript(m))
						}
					} else if !strings.Contains(transcript(m), "set to 6") {
						t.Errorf("positive worker limit should report its value:\n%s", transcript(m))
					}
					return
				}

				if got := rec.count("POST", path); got != 0 {
					t.Fatalf("invalid worker limit must not make a POST, got %d:\n%s", got, rec.all())
				}
				out := transcript(m)
				wantError := tc.wantError
				if wantError == "usage" {
					wantError = "usage: /workers " + action + " <n>"
				}
				if !strings.Contains(out, wantError) {
					t.Errorf("expected validation text %q:\n%s", wantError, out)
				}
				if strings.Contains(out, "unlimited") || strings.Contains(out, "no limit") || strings.Contains(out, "set to") {
					t.Errorf("invalid worker limit must not report success:\n%s", out)
				}
			})
		}
	}
}

func TestScheduleDirectDispatchResolvesSecondScheduleID(t *testing.T) {
	cases := []struct {
		action string
		method string
		path   string
	}{
		{action: "toggle", method: http.MethodPost, path: "/api/schedules/s-2/toggle"},
		{action: "delete", method: http.MethodDelete, path: "/schedules/s-2"},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/schedule": selScheduleHTML})
			m = runLine(t, m, "/schedule "+tc.action+" s-2")
			if tc.action == "delete" {
				if m.pendingConfirmation == nil {
					t.Fatalf("expected delete confirmation:\n%s", transcript(m))
				}
				m = runLine(t, m, "yes")
			}
			if !rec.saw(tc.method, tc.path) {
				t.Fatalf("expected direct second schedule action %s %s, calls:\n%s", tc.method, tc.path, rec.all())
			}
		})
	}
}

func TestScheduleAddResolvesTask(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
	runLine(t, m, "/schedule add Refactor 2026-09-01T10:00 daily")
	if !rec.saw("POST", "/tasks/t-1/schedule") {
		t.Errorf("calls:\n%s", rec.all())
	}
}

func TestScheduleAddFastRepeatTypes(t *testing.T) {
	cases := []struct {
		line           string
		wantRepeatType string
		wantInterval   string
	}{
		{"/schedule add Refactor 2026-09-01T10:00 once", "repeat_type=once", "repeat_interval=1"},
		{"/schedule add Refactor 2026-09-01T10:00 daily", "repeat_type=daily", "repeat_interval=1"},
		{"/schedule add Refactor 2026-09-01T10:00 weekly", "repeat_type=weekly", "repeat_interval=1"},
		{"/schedule add Refactor 2026-09-01T10:00 monthly", "repeat_type=monthly", "repeat_interval=1"},
		{"/schedule add Refactor 2026-09-01T10:00 seconds", "repeat_type=seconds", "repeat_interval=1"},
		{"/schedule add Refactor 2026-09-01T10:00 seconds 1", "repeat_type=seconds", "repeat_interval=1"},
		{"/schedule add Refactor 2026-09-01T10:00 seconds 30", "repeat_type=seconds", "repeat_interval=30"},
		{"/schedule add Refactor 2026-09-01T10:00 minutes", "repeat_type=minutes", "repeat_interval=1"},
		{"/schedule add Refactor 2026-09-01T10:00 minutes 15", "repeat_type=minutes", "repeat_interval=15"},
		{"/schedule add Refactor 2026-09-01T10:00 hours", "repeat_type=hours", "repeat_interval=1"},
		{"/schedule add Refactor 2026-09-01T10:00 hours 4", "repeat_type=hours", "repeat_interval=4"},
		{"/schedule add Refactor 2026-09-01T10:00 HoUrLy", "repeat_type=hours", "repeat_interval=1"},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
			runLine(t, m, tc.line)
			if !rec.saw("POST", "/tasks/t-1/schedule") {
				t.Errorf("calls:\n%s", rec.all())
			}
			if !rec.sawForm(tc.wantRepeatType) {
				t.Errorf("expected %q in form data:\n%v", tc.wantRepeatType, rec.forms)
			}
			if !rec.sawForm(tc.wantInterval) {
				t.Errorf("expected %q in form data:\n%v", tc.wantInterval, rec.forms)
			}
		})
	}
}

func TestScheduleAddRejectsInvalidRepeatIntervals(t *testing.T) {
	cases := []string{
		"/schedule add Refactor 2026-09-01T10:00 seconds 0",
		"/schedule add Refactor 2026-09-01T10:00 minutes -5",
		"/schedule add Refactor 2026-09-01T10:00 hours 366",
	}
	for _, line := range cases {
		t.Run(line, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
			m = runLine(t, m, line)
			if strings.Contains(rec.all(), "POST ") {
				t.Fatalf("invalid interval should not mutate backend, calls:\n%s", rec.all())
			}
			out := transcript(m)
			if !strings.Contains(out, "repeat interval must be between 1 and 365") {
				t.Errorf("expected interval validation message:\n%s", out)
			}
		})
	}
}

func TestPersonalitySet(t *testing.T) {
	const personalitiesHTML = `<div id="personality-section" data-selected-personality="">
		<div data-personality-key="concise" data-personality-name="Concise" data-personality-description="short answers"
			data-personality-preview="Keep answers short." data-personality-is-preset="true" data-personality-has-custom="false"></div>
	</div>`
	m, rec := dispatchModel(t, map[string]string{"/personality": personalitiesHTML})
	runLine(t, m, "/personality set concise")
	if !rec.saw("POST", "/personality/save") {
		t.Errorf("calls:\n%s", rec.all())
	}
	if !rec.sawQuery("project_id=p1") {
		t.Errorf("set lost project scope:\n%s", rec.all())
	}
}

func TestPersonalityListShowSetAndUnknownAction(t *testing.T) {
	const personalitiesHTML = `<div id="personality-section" data-selected-personality="release_coach">
		<div data-personality-key="" data-personality-name="Base" data-personality-description="Standard tone"
			data-personality-preview="" data-personality-is-preset="true" data-personality-has-custom="false"></div>
		<div data-personality-key="pirate_captain" data-personality-name="Pirate Captain" data-personality-description="pirate style"
			data-personality-preview="Speak like a pirate..." data-personality-is-preset="true" data-personality-has-custom="true"></div>
		<div data-personality-key="release_coach" data-personality-name="Release Coach" data-personality-description="safe releases"
			data-personality-preview="Keep releases safe..." data-personality-is-preset="false" data-personality-has-custom="true"></div>
	</div>`
	const fullPrompt = "This is the complete release coach system prompt for production work."
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.recordURL(r.Method, r.URL.RequestURI())
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/personality":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(personalitiesHTML))
		case r.Method == http.MethodGet && r.URL.Path == "/personality/custom/release_coach":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"id":"cp1","key":"release_coach","name":"Release Coach","description":"safe releases","system_prompt":%q}`, fullPrompt)
		case r.Method == http.MethodPost && r.URL.Path == "/personality/save":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.selectedID = "p1"
	m.selectedName = "demo"

	m = runLine(t, m, "/personality list")
	listOutput := stripANSI(transcript(m))
	for _, want := range []string{"release_coach", "Pirate Captain", "safe releases", "Keep releases safe...", "TYPE", "PROMPT PREVIEW"} {
		if !strings.Contains(listOutput, want) {
			t.Errorf("personality list missing %q:\n%s", want, listOutput)
		}
	}

	m = runLine(t, m, "/personality show Release Coach")
	if !strings.Contains(stripANSI(transcript(m)), fullPrompt) {
		t.Errorf("personality show did not render the full prompt:\n%s", stripANSI(transcript(m)))
	}
	if got := rec.count(http.MethodGet, "/personality/custom/release_coach"); got != 1 {
		t.Errorf("show made %d detail requests, want 1:\n%s", got, rec.all())
	}

	m = runLine(t, m, "/personality set release_coach")
	if !rec.saw(http.MethodPost, "/personality/save") || !rec.sawQuery("project_id=p1") {
		t.Errorf("set did not use the scoped save route:\n%s", rec.all())
	}

	personalityGets := rec.count(http.MethodGet, "/personality")
	m = runLine(t, m, "/personality unsupported")
	if rec.count(http.MethodGet, "/personality") != personalityGets {
		t.Errorf("unsupported action fell through to a read:\n%s", rec.all())
	}
	if !strings.Contains(stripANSI(transcript(m)), "unknown personality action") {
		t.Errorf("unsupported action error missing:\n%s", stripANSI(transcript(m)))
	}
}

func TestPersonalityAmbiguousReferenceDoesNotMutate(t *testing.T) {
	const personalitiesHTML = `<div id="personality-section" data-selected-personality="">
		<div data-personality-key="review_one" data-personality-name="Review One" data-personality-description="one"
			data-personality-preview="one" data-personality-is-preset="false" data-personality-has-custom="true"></div>
		<div data-personality-key="review_two" data-personality-name="Review Two" data-personality-description="two"
			data-personality-preview="two" data-personality-is-preset="false" data-personality-has-custom="true"></div>
	</div>`
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.recordURL(r.Method, r.URL.RequestURI())
		if r.Method == http.MethodGet && r.URL.Path == "/personality" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(personalitiesHTML))
			return
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	m.selectedID = "p1"
	m.selectedName = "demo"
	m = runLine(t, m, "/personality set Review")
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "ambiguous") || !strings.Contains(out, "Review One") || !strings.Contains(out, "Review Two") {
		t.Errorf("ambiguous personality error missing candidates:\n%s", out)
	}
	if rec.count(http.MethodPost, "/personality/save") != 0 {
		t.Error("ambiguous personality reference mutated the active setting")
	}
}

func TestPersonalityAddEditAndDeleteUseBackendContracts(t *testing.T) {
	const personalitiesHTML = `<div id="personality-section" data-selected-personality="release_coach">
		<div data-personality-key="" data-personality-name="Base" data-personality-description="Standard tone"
			data-personality-preview="" data-personality-is-preset="true" data-personality-has-custom="false"></div>
		<div data-personality-key="release_coach" data-personality-name="Release Coach" data-personality-description="safe releases"
			data-personality-preview="Keep releases safe..." data-personality-is-preset="false" data-personality-has-custom="true"></div>
	</div>`
	var addBody, editBody map[string]string
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.recordURL(r.Method, r.URL.RequestURI())
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/personality":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(personalitiesHTML))
		case r.Method == http.MethodPost && r.URL.Path == "/personality/custom":
			if err := json.NewDecoder(r.Body).Decode(&addBody); err != nil {
				t.Errorf("decode add body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":"cp-new","key":"release_coach","name":"Release Coach","description":"safe releases","system_prompt":"Keep releases safe in production deployments."}`)
		case r.Method == http.MethodPut && r.URL.Path == "/personality/custom/release_coach":
			if err := json.NewDecoder(r.Body).Decode(&editBody); err != nil {
				t.Errorf("decode edit body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"id":"cp-new","key":"release_coach","name":"Updated Coach","description":"updated","system_prompt":"Keep every release reversible and observable."}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/personality/custom/release_coach":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	m.selectedID = "p1"
	m.selectedName = "demo"

	m = runLine(t, m, "/personality add Release Coach | Keep releases safe in production deployments.")
	if addBody["description"] != "" {
		t.Errorf("two-field add description = %q, want empty", addBody["description"])
	}
	if addBody["name"] != "Release Coach" || addBody["description"] != "" || addBody["system_prompt"] != "Keep releases safe in production deployments." {
		t.Errorf("add payload = %#v", addBody)
	}
	if !strings.Contains(stripANSI(transcript(m)), "key: release_coach") || !strings.Contains(stripANSI(transcript(m)), "ID: cp-new") {
		t.Errorf("add output did not report key and ID:\n%s", stripANSI(transcript(m)))
	}

	m = runLine(t, m, "/personality edit release_coach | Updated Coach | updated | Keep every release reversible and observable.")
	if editBody["name"] != "Updated Coach" || editBody["description"] != "updated" || editBody["system_prompt"] == "" {
		t.Errorf("edit payload = %#v", editBody)
	}

	beforeDelete := rec.count(http.MethodDelete, "/personality/custom/release_coach")
	m = runLine(t, m, "/personality delete release_coach")
	if rec.count(http.MethodDelete, "/personality/custom/release_coach") != beforeDelete {
		t.Error("delete ran before TUI confirmation")
	}
	m = runLine(t, m, "no")
	if rec.count(http.MethodDelete, "/personality/custom/release_coach") != beforeDelete {
		t.Error("cancelled delete mutated the backend")
	}
	m = confirmDestructive(t, m, "/personality delete release_coach")
	if rec.count(http.MethodDelete, "/personality/custom/release_coach") != beforeDelete+1 {
		t.Errorf("confirmed delete count = %d, want %d:\n%s", rec.count(http.MethodDelete, "/personality/custom/release_coach"), beforeDelete+1, rec.all())
	}
	if !rec.sawQuery("project_id=p1") {
		t.Errorf("personality mutation lost project scope:\n%s", rec.all())
	}
}
func TestPersonalityPipeCharactersArePreservedInAddAndEditPrompts(t *testing.T) {
	const personalitiesHTML = `<div id="personality-section" data-selected-personality="pipe_personality">
		<div data-personality-key="pipe_personality" data-personality-name="Pipe Personality" data-personality-description="test"
			data-personality-preview="prompt" data-personality-is-preset="false" data-personality-has-custom="true"></div>
	</div>`
	addPrompt := "You are precise | preserve this literal marker while helping users."
	editPrompt := "Use calm guidance | keep this marker exactly in the saved prompt."
	var addBody, editBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/personality":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(personalitiesHTML))
		case r.Method == http.MethodPost && r.URL.Path == "/personality/custom":
			if err := json.NewDecoder(r.Body).Decode(&addBody); err != nil {
				t.Errorf("decode add body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":"cp1","key":"pipe_personality","name":"Pipe Personality","system_prompt":"created prompt that is long enough"}`)
		case r.Method == http.MethodPut && r.URL.Path == "/personality/custom/pipe_personality":
			if err := json.NewDecoder(r.Body).Decode(&editBody); err != nil {
				t.Errorf("decode edit body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"id":"cp1","key":"pipe_personality","name":"Pipe Personality","system_prompt":"updated prompt that is long enough"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	m.selectedID = "p1"
	m.selectedName = "demo"

	m = runLine(t, m, "/personality add Pipe Personality | "+addPrompt)
	if got := addBody["system_prompt"]; got != addPrompt {
		t.Fatalf("add prompt = %q, want %q; body=%#v", got, addPrompt, addBody)
	}
	if addBody["description"] != "" {
		t.Fatalf("two-field add description = %q, want empty", addBody["description"])
	}

	m = runLine(t, m, "/personality edit pipe_personality | Pipe Personality | test | "+editPrompt)
	if got := editBody["system_prompt"]; got != editPrompt {
		t.Fatalf("edit prompt = %q, want %q; body=%#v", got, editPrompt, editBody)
	}
}

func TestPersonalityAddLiteralDescriptionPrefixUsesExactTwoFieldPayload(t *testing.T) {
	const personalitiesHTML = `<div id="personality-section" data-selected-personality="">
		<div data-personality-key="release_coach" data-personality-name="Release Coach" data-personality-description=""
			data-personality-preview="prompt" data-personality-is-preset="false" data-personality-has-custom="true"></div>
	</div>`
	const prompt = "description: Explain release risks clearly | preserve every literal | pipe."
	want := map[string]string{
		"name":          "Release Coach",
		"description":   "",
		"system_prompt": prompt,
	}
	var body map[string]string
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.recordURL(r.Method, r.URL.RequestURI())
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/personality":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(personalitiesHTML))
		case r.Method == http.MethodPost && r.URL.Path == "/personality/custom":
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode add body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"id":"cp1","key":"release_coach","name":"Release Coach","description":"","system_prompt":%q}`, prompt)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	m.selectedID = "p1"
	m.selectedName = "demo"

	m = runLine(t, m, "/personality add Release Coach | "+prompt)
	if got := rec.count(http.MethodPost, "/personality/custom"); got != 1 {
		t.Fatalf("interactive add made %d POST requests, want exactly 1:\n%s", got, rec.all())
	}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("interactive add body = %#v, want %#v", body, want)
	}
	if strings.Contains(transcript(m), "error:") {
		t.Fatalf("interactive add reported an error:\n%s", transcript(m))
	}
}

func TestPersonalityAddOptionalDescriptionPreservesPromptPipes(t *testing.T) {
	const personalitiesHTML = `<div id="personality-section" data-selected-personality="">
		<div data-personality-key="existing" data-personality-name="Existing" data-personality-description="existing"
			data-personality-preview="prompt" data-personality-is-preset="false" data-personality-has-custom="true"></div>
	</div>`
	const description = "safe releases for production"
	const prompt = "Keep every release reversible | observable | and easy to explain."
	var body map[string]string
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/personality":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(personalitiesHTML))
		case r.Method == http.MethodPost && r.URL.Path == "/personality/custom":
			posts++
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode add body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":"cp-new","key":"release_coach","name":"Release Coach","description":"safe releases for production","system_prompt":"created prompt that is long enough"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	m.selectedID = "p1"
	m.selectedName = "demo"

	m = runLine(t, m, "/personality add Release Coach | description="+description+" | "+prompt)
	if posts != 1 {
		t.Fatalf("add requests = %d, want 1", posts)
	}
	if body["name"] != "Release Coach" || body["description"] != description || body["system_prompt"] != prompt {
		t.Fatalf("add body = %#v, want description %q and prompt %q", body, description, prompt)
	}
}

func TestPersonalityAddRejectsMalformedOptionalDescription(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/personality/custom" {
			posts++
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	m.selectedID = "p1"
	m.selectedName = "demo"

	for _, line := range []string{
		"/personality add Release Coach",
		"/personality add | Keep releases safe in production deployments.",
		"/personality add Release Coach | description=safe releases",
		"/personality add Release Coach | description= | Keep releases safe in production deployments.",
		"/personality add Release Coach | description=safe releases |",
	} {
		m = runLine(t, m, line)
		if !strings.Contains(transcript(m), "personality add") || !strings.Contains(transcript(m), "description") {
			t.Errorf("malformed add %q did not show explicit usage: %s", line, transcript(m))
		}
	}
	if posts != 0 {
		t.Fatalf("malformed optional-description forms made %d POST requests", posts)
	}
}

func TestPersonalityMalformedAndUnknownReferencesDoNotMutate(t *testing.T) {
	const personalitiesHTML = `<div id="personality-section" data-selected-personality="">
		<div data-personality-key="known" data-personality-name="Known" data-personality-description="known"
			data-personality-preview="known" data-personality-is-preset="false" data-personality-has-custom="true"></div>
	</div>`
	var posts, puts, saves, deletes int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/personality":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(personalitiesHTML))
		case r.Method == http.MethodPost && r.URL.Path == "/personality/custom":
			posts++
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = fmt.Fprint(w, `{"error":"System prompt must be at least 20 characters"}`)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/personality/custom/"):
			puts++
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error":"personality update failed"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/personality/save":
			saves++
			w.WriteHeader(http.StatusBadGateway)
			_, _ = fmt.Fprint(w, `{"error":"personality activation failed"}`)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/personality/custom/"):
			deletes++
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error":"personality deletion failed"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	m.selectedID = "p1"
	m.selectedName = "demo"

	m = runLine(t, m, "/personality add Known | short")
	if posts != 1 || strings.Contains(transcript(m), "created personality") {
		t.Fatalf("validation failure reported success or wrong POST count: posts=%d transcript=%s", posts, transcript(m))
	}
	if !strings.Contains(transcript(m), "System prompt must be at least 20 characters") {
		t.Fatalf("validation error missing from transcript: %s", transcript(m))
	}

	m = runLine(t, m, "/personality edit missing | New Name | desc | A valid prompt that is long enough")
	if puts != 0 || !strings.Contains(transcript(m), "nothing matches") {
		t.Fatalf("unknown edit mutated or lacked error: puts=%d transcript=%s", puts, transcript(m))
	}

	m = runLine(t, m, "/personality set missing")
	if saves != 0 || !strings.Contains(transcript(m), "nothing matches") {
		t.Fatalf("unknown set mutated or lacked error: saves=%d transcript=%s", saves, transcript(m))
	}

	m = runLine(t, m, "/personality set known")
	if saves != 1 || strings.Contains(transcript(m), "personality set to known") {
		t.Fatalf("activation failure reported success or wrong POST count: saves=%d transcript=%s", saves, transcript(m))
	}
	if !strings.Contains(transcript(m), "personality activation failed") {
		t.Fatalf("activation error missing from transcript: %s", transcript(m))
	}

	m = runLine(t, m, "/personality delete known")
	if m.pendingConfirmation == nil || deletes != 0 {
		t.Fatalf("delete should wait for confirmation: pending=%v deletes=%d", m.pendingConfirmation != nil, deletes)
	}
	m = runLine(t, m, "yes")
	if deletes != 1 || strings.Contains(transcript(m), "deleted personality") {
		t.Fatalf("deletion failure reported success or wrong DELETE count: deletes=%d transcript=%s", deletes, transcript(m))
	}
	if !strings.Contains(transcript(m), "personality deletion failed") {
		t.Fatalf("deletion error missing from transcript: %s", transcript(m))
	}
}

func TestPersonalityDeleteConfirmationCancellationDoesNotMutate(t *testing.T) {
	const personalitiesHTML = `<div id="personality-section" data-selected-personality="">
		<div data-personality-key="known" data-personality-name="Known" data-personality-description="known"
			data-personality-preview="known" data-personality-is-preset="false" data-personality-has-custom="true"></div>
	</div>`
	var deletes int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/personality" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(personalitiesHTML))
			return
		}
		if r.Method == http.MethodDelete && r.URL.Path == "/personality/custom/known" {
			deletes++
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	m.selectedID = "p1"
	m.selectedName = "demo"

	m = runLine(t, m, "/personality delete known")
	if m.pendingConfirmation == nil || deletes != 0 {
		t.Fatalf("delete should be parked before confirmation: pending=%v deletes=%d", m.pendingConfirmation != nil, deletes)
	}
	m = runLine(t, m, "no")
	if m.pendingConfirmation != nil || deletes != 0 {
		t.Fatalf("cancelled delete mutated or left confirmation: pending=%v deletes=%d", m.pendingConfirmation != nil, deletes)
	}
}

func TestAlertsBulkActions(t *testing.T) {
	m, rec := dispatchModel(t, nil)
	runLine(t, m, "/alerts read-all")
	if !rec.saw("POST", "/alerts/read-all") {
		t.Errorf("calls:\n%s", rec.all())
	}
}

func TestAlertsCommandsRequireProject(t *testing.T) {
	cases := []struct {
		name              string
		line              string
		checkConfirmation bool
		checkSelector     bool
	}{
		{name: "list", line: "/alerts"},
		{name: "read_all", line: "/alerts read-all"},
		{name: "clear", line: "/alerts clear", checkConfirmation: true},
		{name: "approve", line: "/alerts approve a-1"},
		{name: "reject", line: "/alerts reject a-1"},
		{name: "dismiss", line: "/alerts dismiss a-1"},
		{name: "read", line: "/alerts read a-1"},
		{name: "delete", line: "/alerts delete a-1", checkConfirmation: true},
		{name: "delete_without_ref", line: "/alerts delete", checkConfirmation: true, checkSelector: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, nil)
			m.selectedID = ""
			m.selectedName = ""

			m = runLine(t, m, tc.line)

			if !strings.Contains(transcript(m), "no project selected") {
				t.Errorf("expected no-project error for %s:\n%s", tc.line, transcript(m))
			}
			if calls := rec.all(); calls != "" {
				t.Errorf("%s must not make a backend request without a project:\n%s", tc.line, calls)
			}
			if tc.checkConfirmation && m.pendingConfirmation != nil {
				t.Errorf("%s must not set pendingConfirmation without a project", tc.line)
			}
			if tc.checkSelector && m.selectorActive {
				t.Errorf("%s must not open a selector without a project", tc.line)
			}
		})
	}
}

// After a successful mutation, a failed list refresh must not surface as an
// error — the mutation already succeeded, so the status line alone is shown.
func TestRefreshFailureAfterMutationIsSwallowed(t *testing.T) {
	var taskGETs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/tasks":
			taskGETs++
			if taskGETs > 1 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"refresh failed"}`))
				return
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(taskBoardHTML))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.selectedID = "p1"
	m.selectedName = "demo"

	m = runLine(t, m, "/tasks run Refactor")
	out := transcript(m)
	if !strings.Contains(out, "run: Refactor the API") {
		t.Errorf("expected the status line despite the refresh failure:\n%s", out)
	}
	if strings.Contains(out, "error:") || strings.Contains(out, "refresh failed") {
		t.Errorf("a post-mutation refresh failure must be swallowed, not surfaced:\n%s", out)
	}
}

func TestModelsCommandsRequireSelectedProject(t *testing.T) {
	cases := []string{
		"/models",
		"/models list",
		"/models capacity",
		"/models default Sonnet",
		"/models delete Sonnet",
		"/models default",
		"/models delete",
	}
	for _, line := range cases {
		line := line
		t.Run(line, func(t *testing.T) {
			m, rec := dispatchModel(t, nil)
			m.selectedID = ""
			m.selectedName = ""

			m = runLine(t, m, line)
			out := stripANSI(transcript(m))
			if !strings.Contains(out, "no project selected — use /project <name>") {
				t.Fatalf("expected no-project guidance for %s:\n%s", line, out)
			}
			if calls := rec.all(); calls != "" {
				t.Fatalf("%s made backend requests without a selected project:\n%s", line, calls)
			}
			if m.busy {
				t.Fatalf("%s left the model busy", line)
			}
			if m.selectorActive {
				t.Fatalf("%s opened a selector without a selected project", line)
			}
			if m.pendingConfirmation != nil {
				t.Fatalf("%s opened confirmation without a selected project", line)
			}
		})
	}
}

func TestModelsDefault(t *testing.T) {
	const modelsHTML = `<div data-model-id="m-1" data-model-name="Sonnet"
		data-model-provider="anthropic" data-model-model="claude-sonnet-4"></div>`
	m, rec := dispatchModel(t, map[string]string{"/models": modelsHTML})
	runLine(t, m, "/models default Sonnet")
	if !rec.saw("POST", "/models/m-1/set-default") {
		t.Errorf("calls:\n%s", rec.all())
	}
}

func TestModelsDelete(t *testing.T) {
	const modelsHTML = `<div data-model-id="m-1" data-model-name="Sonnet"
		data-model-provider="anthropic" data-model-model="claude-sonnet-4"></div>`
	m, rec := dispatchModel(t, map[string]string{"/models": modelsHTML})
	confirmDestructive(t, m, "/models delete Sonnet")
	if !rec.saw("DELETE", "/models/m-1") {
		t.Errorf("calls:\n%s", rec.all())
	}
}

func TestModelsDeleteUnauthorizedSkipsSuccessAndReload(t *testing.T) {
	const modelsHTML = `<div data-model-id="m-1" data-model-name="Sonnet"
		data-model-provider="anthropic" data-model-model="claude-sonnet-4"></div>`
	var modelGets, loginRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/models":
			modelGets++
			if modelGets > 1 {
				http.Error(w, "reload failed", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(modelsHTML))
		case r.Method == http.MethodDelete && r.URL.Path == "/models/m-1":
			w.Header().Set("Location", "/login")
			w.WriteHeader(http.StatusTemporaryRedirect)
		case r.URL.Path == "/login":
			loginRequests++
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.selectedID = "p1"
	m.selectedName = "demo"

	m = confirmDestructive(t, m, "/models delete Sonnet")
	out := transcript(m)
	if !strings.Contains(out, "error::") || !strings.Contains(out, "requires sign-in") {
		t.Fatalf("expected sign-in error:\n%s", out)
	}
	if strings.Contains(out, "delete: Sonnet") {
		t.Fatalf("unauthorized delete reported success:\n%s", out)
	}
	if modelGets != 1 {
		t.Errorf("models GETs = %d, want only the reference lookup; reload should not run after failed action", modelGets)
	}
	if loginRequests != 0 {
		t.Errorf("login requests = %d, want 0; redirect was followed", loginRequests)
	}
}

// automationCardHTML mirrors the real automation card markup: the
// delete-menu button carries data-automation-card-delete/data-automation-name,
// and the card's own badges carry the lifecycle state.
func automationCardHTML(id, name, state string) string {
	return `<div class="card" data-automation-url="/automations/` + id + `?project_id=p1"
	  data-search-card data-search-text="` + name + `">
	  <div class="card-body relative">
	    <span class="badge badge-outline badge-sm">` + state + `</span>
	    <button type="button" class="text-error" data-automation-card-delete="` + id + `"
	            data-automation-name="` + name + `"></button>
	  </div>
	</div>`
}

func automationDetailHTML(id, projectID, name string) string {
	return `<div id="automation-live" data-automation-id="` + id + `" data-project-id="` + projectID + `" data-automation-name="` + name + `" data-automation-lifecycle-state="active" data-automation-health-state="healthy" data-automation-version-id="v1" data-automation-version-number="1" data-automation-version-state="published">
		<div data-automation-graph-panel><svg data-automation-canvas><g data-automation-live-node="n1" data-automation-node-key="start" class="automation-graph-node--running"><foreignObject><div><strong>Start</strong><span class="automation-node-state--running">running</span><small>1 running</small></div></foreignObject></g></svg></div>
		<div data-automation-live-metrics data-automation-active-invocations="2" data-automation-active-work-items="3"></div>
		<div data-automation-resources><div data-automation-resource-row data-automation-resource-node-key="start" data-automation-resource-type="repository" data-automation-resource-id="repo1" data-automation-resource-status="ready"></div></div>
		<div data-automation-external-state data-automation-external-status="fresh" data-automation-external-tracked-resources="1"></div>
	</div>`
}

func TestAutomationsShowResolvesReferencesAndLoadsScopedDetail(t *testing.T) {
	automationsHTML := "<div>" +
		automationCardHTML("au-1", "Native SDLC", "active") +
		automationCardHTML("au-2", "GitHub SDLC", "paused") +
		"</div>"

	cases := []struct {
		name string
		line string
		id   string
	}{
		{name: "exact id", line: "/automations show au-1", id: "au-1"},
		{name: "exact name", line: "/automations show Native SDLC", id: "au-1"},
		{name: "unique prefix", line: "/automations show Native", id: "au-1"},
		{name: "unique substring", line: "/automations show GitHub", id: "au-2"},
		{name: "open alias", line: "/automations open au-1", id: "au-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{
				"/automations":      automationsHTML,
				"/automations/au-1": automationDetailHTML("au-1", "p1", "Native SDLC"),
				"/automations/au-2": automationDetailHTML("au-2", "p1", "GitHub SDLC"),
			})
			m = runLine(t, m, tc.line)
			if got := rec.count("GET", "/automations"); got != 1 {
				t.Fatalf("list requests = %d, want 1; calls:\n%s", got, rec.all())
			}
			if got := rec.count("GET", "/automations/"+tc.id); got != 1 {
				t.Fatalf("detail requests = %d, want 1; calls:\n%s", got, rec.all())
			}
			if !rec.sawQuery("GET /automations/" + tc.id + "?project_id=p1") {
				t.Fatalf("detail request lost selected project:\n%s", rec.all())
			}
			out := transcript(m)
			wantName := "Native SDLC"
			if tc.id == "au-2" {
				wantName = "GitHub SDLC"
			}
			for _, want := range []string{wantName, "Graph", "Nodes", "Runtime", "active invocations", "Resources", "External state"} {
				if !strings.Contains(out, want) {
					t.Errorf("detail output missing %q:\n%s", want, out)
				}
			}
			if strings.Contains(out, "error:") {
				t.Errorf("unexpected detail error:\n%s", out)
			}
		})
	}
}

func TestAutomationsShowReferenceFailuresDoNotLoadDetailOrMutate(t *testing.T) {
	automationsHTML := "<div>" +
		automationCardHTML("au-1", "Native SDLC", "active") +
		automationCardHTML("au-2", "GitHub SDLC", "paused") +
		"</div>"
	for _, tc := range []struct {
		name string
		ref  string
		want string
	}{
		{name: "unknown", ref: "missing", want: "error:"},
		{name: "ambiguous substring", ref: "SDLC", want: "ambiguous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{
				"/automations":      automationsHTML,
				"/automations/au-1": automationDetailHTML("au-1", "p1", "Native SDLC"),
			})
			m = runLine(t, m, "/automations show "+tc.ref)
			if rec.count("GET", "/automations/au-1") != 0 || rec.count("GET", "/automations/au-2") != 0 {
				t.Fatalf("reference failure loaded detail:\n%s", rec.all())
			}
			if rec.saw("POST", "/automations/au-1/run-now") || rec.saw("POST", "/automations/au-1/pause") {
				t.Fatalf("reference failure mutated automation:\n%s", rec.all())
			}
			if !strings.Contains(strings.ToLower(transcript(m)), strings.ToLower(tc.want)) {
				t.Fatalf("output = %s, want %q:\n%s", tc.want, tc.want, transcript(m))
			}
		})
	}
}

func TestAutomationsShowDraft404ExplainsUnavailableGraph(t *testing.T) {
	var detailProject string
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/automations":
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprint(w, `<div>`+automationCardHTML("au-draft", "Draft flow", "draft")+`</div>`)
		case r.Method == http.MethodGet && r.URL.Path == "/automations/au-draft":
			detailProject = r.URL.Query().Get("project_id")
			w.WriteHeader(http.StatusNotFound)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{}`)
		}
	})
	m = runLine(t, m, "/automations show Draft")
	out := transcript(m)
	for _, want := range []string{"Automation: Draft flow", "draft automation has no live graph", "nodes: unavailable", "edges: unavailable"} {
		if !strings.Contains(out, want) {
			t.Errorf("draft fallback missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "NODE   STATE") || strings.Contains(out, "error:") {
		t.Errorf("draft fallback claimed graph/error:\n%s", out)
	}
	if detailProject != "p1" {
		t.Errorf("draft detail project_id = %q, want p1", detailProject)
	}
}

func TestAutomationsShowDetailFragmentAndBackendErrorsSurfaceClearly(t *testing.T) {
	cases := []struct {
		name       string
		detailBody string
		status     int
		want       string
	}{
		{name: "malformed fragment", detailBody: `<div data-project-id="p1">missing identity</div>`, status: http.StatusOK, want: "malformed fragment"},
		{name: "backend error", detailBody: `{"error":"detail unavailable"}`, status: http.StatusBadGateway, want: "detail unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/automations":
					w.Header().Set("Content-Type", "text/html")
					_, _ = fmt.Fprint(w, `<div>`+automationCardHTML("au-1", "Native SDLC", "active")+`</div>`)
				case r.Method == http.MethodGet && r.URL.Path == "/automations/au-1":
					w.Header().Set("Content-Type", "text/html")
					w.WriteHeader(tc.status)
					_, _ = fmt.Fprint(w, tc.detailBody)
				default:
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{}`)
				}
			})
			m = runLine(t, m, "/automations show au-1")
			out := transcript(m)
			if !strings.Contains(strings.ToLower(out), strings.ToLower(tc.want)) || !strings.Contains(out, "error:") {
				t.Fatalf("detail failure output = %q, want error containing %q", out, tc.want)
			}
		})
	}
}

func TestAutomationsListUsesStructuredRowsWithNoArguments(t *testing.T) {
	automationsHTML := "<div>" + automationCardHTML("au-1", "Native SDLC", "active") + "</div>"
	m, rec := dispatchModel(t, map[string]string{"/automations": automationsHTML})
	m = runLine(t, m, "/automations")
	if got := strings.Count(rec.all(), "GET /automations"); got != 1 {
		t.Fatalf("expected one automations fetch, got %d:\n%s", got, rec.all())
	}
	out := transcript(m)
	for _, want := range []string{"au-1", "Native SDLC", "active"} {
		if !strings.Contains(out, want) {
			t.Errorf("automations output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "error:") {
		t.Errorf("/automations should not error:\n%s", out)
	}
}

func TestAutomationsListFiltersStructuredRows(t *testing.T) {
	automationsHTML := "<div>" +
		automationCardHTML("au-1", "Native SDLC", "active") +
		automationCardHTML("au-2", "GitHub SDLC", "paused") +
		"</div>"
	m, rec := dispatchModel(t, map[string]string{"/automations": automationsHTML})
	m = runLine(t, m, "/automations GitHub")
	if got := strings.Count(rec.all(), "GET /automations"); got != 1 {
		t.Fatalf("expected one automations fetch, got %d:\n%s", got, rec.all())
	}
	out := transcript(m)
	if strings.Contains(out, "Native SDLC") || !strings.Contains(out, "GitHub SDLC") {
		t.Errorf("filtered automations output:\n%s", out)
	}
}
func TestAutomationsCommandResolvesReferencesAndDispatches(t *testing.T) {
	automationsHTML := "<div>" +
		automationCardHTML("au-1", "Native SDLC", "active") +
		automationCardHTML("au-2", "GitHub SDLC", "paused") +
		"</div>"

	t.Run("exact id match", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/automations": automationsHTML})
		m = runLine(t, m, "/automations pause au-1")
		if !rec.saw("POST", "/automations/au-1/pause") {
			t.Errorf("calls:\n%s", rec.all())
		}
		out := transcript(m)
		if strings.Contains(out, "error:") {
			t.Errorf("unexpected error:\n%s", out)
		}
		if !strings.Contains(out, "pause: Native SDLC") {
			t.Errorf("expected status line:\n%s", out)
		}
		if !strings.Contains(out, "paused") {
			t.Errorf("expected refreshed automations page after action:\n%s", out)
		}
	})

	t.Run("unique name prefix match", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/automations": automationsHTML})
		m = runLine(t, m, "/automations run-now Native")
		if !rec.saw("POST", "/automations/au-1/run-now") {
			t.Errorf("calls:\n%s", rec.all())
		}
	})

	t.Run("unique id prefix match", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/automations": automationsHTML})
		m = runLine(t, m, "/automations resume au-2")
		if !rec.saw("POST", "/automations/au-2/resume") {
			t.Errorf("calls:\n%s", rec.all())
		}
	})

	t.Run("unique substring match", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/automations": automationsHTML})
		confirmDestructive(t, m, "/automations delete GitHub")
		if !rec.saw("POST", "/automations/au-2/delete") {
			t.Errorf("calls:\n%s", rec.all())
		}
	})

	t.Run("ambiguous exact name rejected", func(t *testing.T) {
		ambiguousHTML := "<div>" +
			automationCardHTML("au-3", "Deploy", "active") +
			automationCardHTML("au-4", "deploy", "active") +
			"</div>"
		m, rec := dispatchModel(t, map[string]string{"/automations": ambiguousHTML})
		m = runLine(t, m, "/automations pause DEPLOY")
		if rec.saw("POST", "/automations/au-3/pause") || rec.saw("POST", "/automations/au-4/pause") {
			t.Errorf("ambiguous exact automation name dispatched a pause:\n%s", rec.all())
		}
		if !strings.Contains(strings.ToLower(transcript(m)), "ambiguous") {
			t.Fatalf("expected an ambiguous automation-name error:\n%s", transcript(m))
		}
	})

	t.Run("ambiguous match rejected", func(t *testing.T) {
		ambiguousHTML := "<div>" +
			automationCardHTML("au-3", "SDLC A", "active") +
			automationCardHTML("au-4", "SDLC B", "active") +
			"</div>"
		m, rec := dispatchModel(t, map[string]string{"/automations": ambiguousHTML})
		m = runLine(t, m, "/automations pause SDLC")
		if rec.saw("POST", "/automations/au-3/pause") || rec.saw("POST", "/automations/au-4/pause") {
			t.Errorf("ambiguous reference must not dispatch a mutation:\n%s", rec.all())
		}
		out := transcript(m)
		if !strings.Contains(out, "error:") {
			t.Errorf("expected an ambiguous-match error:\n%s", out)
		}
	})

	t.Run("unknown reference rejected", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/automations": automationsHTML})
		// Step 1: destructive command parks a pendingConfirmation; no backend call yet.
		m = runLine(t, m, "/automations delete nope")
		if rec.saw("POST", "/automations/au-1/delete") || rec.saw("POST", "/automations/au-2/delete") {
			t.Errorf("no backend call must occur before confirmation:\n%s", rec.all())
		}
		// Step 2: confirm with "yes" — the error surfaces during resolution.
		m = runLine(t, m, "yes")
		if rec.saw("POST", "/automations/au-1/delete") || rec.saw("POST", "/automations/au-2/delete") {
			t.Errorf("unknown reference must not dispatch a mutation:\n%s", rec.all())
		}
		if !strings.Contains(transcript(m), "error:") {
			t.Errorf("expected an error for an unknown reference:\n%s", transcript(m))
		}
	})
}

func TestAutomationsReloadFailureAfterActionIsSwallowed(t *testing.T) {
	automationsHTML := "<div>" + automationCardHTML("au-1", "Native SDLC", "active") + "</div>"
	var gets int
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/automations" {
			gets++
			if gets > 1 {
				http.Error(w, "refresh failed", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(automationsHTML))
			return
		}
		if r.Method == "POST" && r.URL.Path == "/automations/au-1/pause" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})

	m = runLine(t, m, "/automations pause au-1")
	out := transcript(m)
	if !strings.Contains(out, "pause: Native SDLC") {
		t.Errorf("expected status line despite refresh failure:\n%s", out)
	}
	if strings.Contains(out, "error:") || strings.Contains(out, "refresh failed") {
		t.Errorf("refresh failure must be swallowed:\n%s", out)
	}
}

// TestAutomationsCommandBackendFailures confirms each new action surfaces a
// non-2xx backend response as an error instead of a false success.
func TestAutomationsCommandBackendFailures(t *testing.T) {
	automationsHTML := "<div>" + automationCardHTML("au-1", "Native SDLC", "active") + "</div>"

	cases := []struct {
		action string
		path   string
	}{
		{"run-now", "/automations/au-1/run-now"},
		{"pause", "/automations/au-1/pause"},
		{"resume", "/automations/au-1/resume"},
		{"delete", "/automations/au-1/delete"},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tc.path {
					http.Error(w, "boom", http.StatusInternalServerError)
					return
				}
				if r.URL.Path == "/automations" {
					w.Header().Set("Content-Type", "text/html")
					_, _ = w.Write([]byte(automationsHTML))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()

			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			m := New(c)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			m = updated.(Model)
			m.selectedID = "p1"
			m.selectedName = "demo"

			m = runLine(t, m, "/automations "+tc.action+" au-1")
			// delete requires explicit confirmation in TUI mode.
			if tc.action == "delete" {
				m = runLine(t, m, "yes")
			}
			out := transcript(m)
			if !strings.Contains(out, "error:") {
				t.Errorf("expected a backend error for %s:\n%s", tc.action, out)
			}
		})
	}
}

func TestTasksActivateSweepClear(t *testing.T) {
	t.Run("activate", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		m = runLine(t, m, "/tasks activate")
		if !rec.saw("POST", "/tasks/backlog/activate") {
			t.Errorf("calls:\n%s", rec.all())
		}
		out := transcript(m)
		if !strings.Contains(out, "activated the backlog") {
			t.Errorf("expected status line:\n%s", out)
		}
		if !strings.Contains(out, "Refactor the API") {
			t.Errorf("expected refreshed board:\n%s", out)
		}
	})
	t.Run("sweep", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		m = runLine(t, m, "/tasks sweep")
		if !rec.saw("POST", "/tasks/move-completed") {
			t.Errorf("calls:\n%s", rec.all())
		}
		out := transcript(m)
		if !strings.Contains(out, "swept finished tasks") {
			t.Errorf("expected status line:\n%s", out)
		}
		if !strings.Contains(out, "Refactor the API") {
			t.Errorf("expected refreshed board:\n%s", out)
		}
	})
	t.Run("clear completed", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		m = runLine(t, m, "/tasks clear completed") // sets pendingConfirmation
		m = runLine(t, m, "yes")                    // confirms and executes
		if !rec.saw("DELETE", "/tasks/completed") {
			t.Errorf("calls:\n%s", rec.all())
		}
		out := transcript(m)
		if !strings.Contains(out, "cleared completed") {
			t.Errorf("expected status line:\n%s", out)
		}
		if !strings.Contains(out, "Refactor the API") {
			t.Errorf("expected refreshed board:\n%s", out)
		}
	})
}

func TestAlertsApproveRejectDismiss(t *testing.T) {
	const alertsHTML = `<div class="card" data-alert-id="a-1" data-alert-scroll-anchor="a-1"
	  data-search-text="build failed">
	  <p class="font-semibold">Build failed</p>
	</div>`
	for _, action := range []string{"approve", "reject", "dismiss"} {
		t.Run(action, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/alerts": alertsHTML})
			m = runLine(t, m, "/alerts "+action+" a-1")
			if !rec.saw("POST", "/alerts/a-1/"+action) {
				t.Errorf("calls:\n%s", rec.all())
			}
			out := transcript(m)
			if !strings.Contains(out, action+": Build failed") {
				t.Errorf("expected status line:\n%s", out)
			}
			if !strings.Contains(out, "Build failed") {
				t.Errorf("expected refreshed alerts:\n%s", out)
			}
		})
	}
}

func TestSkillsEnableAlways(t *testing.T) {
	const skillsHTML = `<div data-skill-handle="deploy" data-skill-name="Deploy"
		data-skill-enabled="true" data-skill-always-use="false" data-skill-scope="project"></div>`

	t.Run("enable", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/skills": skillsHTML})
		m = runLine(t, m, "/skills enable deploy")
		if !rec.saw("POST", "/skills/deploy/enabled") {
			t.Errorf("calls:\n%s", rec.all())
		}
		out := transcript(m)
		if !strings.Contains(out, "enable: deploy") {
			t.Errorf("expected status line:\n%s", out)
		}
		if !strings.Contains(out, "deploy") {
			t.Errorf("expected refreshed skills:\n%s", out)
		}
	})

	t.Run("always", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/skills": skillsHTML})
		m = runLine(t, m, "/skills always deploy")
		if !rec.saw("POST", "/skills/deploy/always_use") {
			t.Errorf("calls:\n%s", rec.all())
		}
		out := transcript(m)
		if !strings.Contains(out, "always: deploy") {
			t.Errorf("expected status line:\n%s", out)
		}
		if !strings.Contains(out, "deploy") {
			t.Errorf("expected refreshed skills:\n%s", out)
		}
	})
}

func TestSkillsAlwaysLoadSetTrueIdempotently(t *testing.T) {
	tests := []struct {
		name         string
		action       string
		initialState bool
		invocations  int
	}{
		{name: "always false-to-true and repeat", action: "always", initialState: false, invocations: 2},
		{name: "always already true", action: "always", initialState: true, invocations: 1},
		{name: "load false-to-true and repeat", action: "load", initialState: false, invocations: 2},
		{name: "load already true", action: "load", initialState: true, invocations: 1},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			alwaysUse := tc.initialState
			var gotAlwaysUse []bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/skills":
					mu.Lock()
					current := alwaysUse
					mu.Unlock()
					w.Header().Set("Content-Type", "text/html")
					_, _ = fmt.Fprintf(w, `<div data-skill-handle="deploy" data-skill-name="Deploy"
						data-skill-enabled="true" data-skill-always-use="%t" data-skill-scope="project"></div>`, current)
				case r.Method == http.MethodPost && r.URL.Path == "/skills/deploy/always_use":
					var payload struct {
						AlwaysUse bool   `json:"always_use"`
						Scope     string `json:"scope"`
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Errorf("decode always-use request: %v", err)
					}
					if payload.Scope != "project" {
						t.Errorf("scope = %q, want project", payload.Scope)
					}
					mu.Lock()
					gotAlwaysUse = append(gotAlwaysUse, payload.AlwaysUse)
					alwaysUse = payload.AlwaysUse
					current := alwaysUse
					mu.Unlock()
					w.Header().Set("Content-Type", "text/html")
					_, _ = fmt.Fprintf(w, `<div data-skill-handle="deploy" data-skill-name="Deploy"
						data-skill-enabled="true" data-skill-always-use="%t" data-skill-scope="project"></div>`, current)
				default:
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{}`))
				}
			}))
			t.Cleanup(srv.Close)

			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			m := New(c)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			m = updated.(Model)
			m.selectedID = "p1"
			m.selectedName = "demo"

			for i := 0; i < tc.invocations; i++ {
				m = runLine(t, m, "/skills "+tc.action+" deploy")
			}

			mu.Lock()
			got := append([]bool(nil), gotAlwaysUse...)
			finalState := alwaysUse
			mu.Unlock()
			want := make([]bool, tc.invocations)
			for i := range want {
				want[i] = true
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("always_use requests = %v, want %v", got, want)
			}
			if !finalState {
				t.Error("repeated state-setting command disabled the skill")
			}
			out := transcript(m)
			if strings.Contains(out, "error:") {
				t.Fatalf("state-setting command reported an error:\n%s", out)
			}
			if count := strings.Count(out, tc.action+": deploy"); count != tc.invocations {
				t.Errorf("success status count = %d, want %d:\n%s", count, tc.invocations, out)
			}
		})
	}
}

// TestSkillsMutationsUseBackendJSONContract runs the TUI skill commands against
// a contract server that rejects form-encoded skill mutations. Reads continue to
// use the rendered HTML route, while every mutation must carry the backend JSON
// payload and preserve the selected project query.
func TestSkillsMutationsUseBackendJSONContract(t *testing.T) {
	const skillsHTML = `<div data-skill-handle="deploy" data-skill-name="Deploy"
		data-skill-description="ship to prod" data-skill-enabled="true" data-skill-always-use="false" data-skill-scope="project"></div>`

	tests := []struct {
		name     string
		line     string
		method   string
		path     string
		wantBody map[string]any
	}{
		{
			name:   "add",
			line:   "/skills add retry-logic | wrap retries | # Retry",
			method: http.MethodPost,
			path:   "/skills",
			wantBody: map[string]any{
				"handle":      "retry-logic",
				"name":        "retry-logic",
				"description": "wrap retries",
				"scope":       "project",
				"body":        "# Retry",
			},
		},
		{
			name:     "edit",
			line:     "/skills edit deploy | new body",
			method:   http.MethodPut,
			path:     "/skills/deploy",
			wantBody: map[string]any{"handle": "deploy", "name": "Deploy", "description": "ship to prod", "scope": "project", "body": "new body", "enabled": true},
		},
		{
			name:     "enable",
			line:     "/skills enable deploy",
			method:   http.MethodPost,
			path:     "/skills/deploy/enabled",
			wantBody: map[string]any{"enabled": true, "scope": "project"},
		},
		{
			name:     "disable",
			line:     "/skills disable deploy",
			method:   http.MethodPost,
			path:     "/skills/deploy/enabled",
			wantBody: map[string]any{"enabled": false, "scope": "project"},
		},
		{
			name:     "always",
			line:     "/skills always deploy",
			method:   http.MethodPost,
			path:     "/skills/deploy/always_use",
			wantBody: map[string]any{"always_use": true, "scope": "project"},
		},
		{
			name:     "load alias",
			line:     "/skills load deploy",
			method:   http.MethodPost,
			path:     "/skills/deploy/always_use",
			wantBody: map[string]any{"always_use": true, "scope": "project"},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath, gotProject, gotContentType, gotHX string
			var gotBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				isSkillMutation :=
					(r.Method == http.MethodPost && r.URL.Path == "/skills") ||
						(r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/skills/")) ||
						(r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/enabled")) ||
						(r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/always_use"))
				if isSkillMutation {
					if r.Header.Get("Content-Type") != "application/json" {
						w.WriteHeader(http.StatusUnsupportedMediaType)
						_, _ = w.Write([]byte(`{"error":"skill mutations require JSON"}`))
						return
					}
					raw, err := io.ReadAll(r.Body)
					if err != nil {
						t.Errorf("read mutation body: %v", err)
					}
					if err := json.Unmarshal(raw, &gotBody); err != nil {
						t.Errorf("decode mutation JSON %q: %v", raw, err)
					}
					gotMethod = r.Method
					gotPath = r.URL.Path
					gotProject = r.URL.Query().Get("project_id")
					gotContentType = r.Header.Get("Content-Type")
					gotHX = r.Header.Get("HX-Request")
					w.Header().Set("Content-Type", "text/html")
					_, _ = w.Write([]byte(skillsHTML))
					return
				}
				if r.Method == http.MethodGet && r.URL.Path == "/skills" {
					w.Header().Set("Content-Type", "text/html")
					_, _ = w.Write([]byte(skillsHTML))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			t.Cleanup(srv.Close)

			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			m := New(c)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			m = updated.(Model)
			m.selectedID = "p1"
			m.selectedName = "demo"
			m = runLine(t, m, tc.line)

			if out := transcript(m); strings.Contains(out, "error:") {
				t.Fatalf("skill mutation was rejected by JSON contract:\n%s", out)
			}
			if gotMethod != tc.method || gotPath != tc.path || gotProject != "p1" {
				t.Fatalf("request = %s %s?project_id=%s, want %s %s?project_id=p1", gotMethod, gotPath, gotProject, tc.method, tc.path)
			}
			if gotContentType != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", gotContentType)
			}
			if gotHX != "true" {
				t.Errorf("HX-Request = %q, want true", gotHX)
			}
			if !reflect.DeepEqual(gotBody, tc.wantBody) {
				t.Errorf("JSON body = %#v, want %#v", gotBody, tc.wantBody)
			}
		})
	}
}

// TestSkillsEditPreservesDisabledState verifies that a body-only edit carries the
// existing disabled state back to the backend instead of implicitly re-enabling it.
func TestSkillsEditPreservesDisabledState(t *testing.T) {
	const skillsHTML = `<div data-skill-handle="deploy" data-skill-name="Deploy"
		data-skill-description="ship to prod" data-skill-enabled="false" data-skill-always-use="false" data-skill-scope="project"></div>`

	var gotBody map[string]any
	var gotContentType string
	var gotMethod, gotPath, gotProject string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/skills":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(skillsHTML))
		case r.Method == http.MethodPut && r.URL.Path == "/skills/deploy":
			if r.Header.Get("Content-Type") != "application/json" {
				w.WriteHeader(http.StatusUnsupportedMediaType)
				_, _ = w.Write([]byte(`{"error":"skill edits require JSON"}`))
				return
			}
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read edit body: %v", err)
			}
			if err := json.Unmarshal(raw, &gotBody); err != nil {
				t.Errorf("decode edit JSON %q: %v", raw, err)
			}
			gotContentType = r.Header.Get("Content-Type")
			gotMethod = r.Method
			gotPath = r.URL.Path
			gotProject = r.URL.Query().Get("project_id")
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(skillsHTML))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.selectedID = "p1"
	m.selectedName = "demo"
	m = runLine(t, m, "/skills edit deploy | new body")

	if out := transcript(m); strings.Contains(out, "error:") {
		t.Fatalf("disabled skill edit was rejected by JSON contract:\n%s", out)
	}
	if gotMethod != http.MethodPut || gotPath != "/skills/deploy" || gotProject != "p1" {
		t.Fatalf("request = %s %s?project_id=%s, want PUT /skills/deploy?project_id=p1", gotMethod, gotPath, gotProject)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	wantBody := map[string]any{
		"handle":      "deploy",
		"name":        "Deploy",
		"description": "ship to prod",
		"scope":       "project",
		"body":        "new body",
		"enabled":     false,
	}
	if !reflect.DeepEqual(gotBody, wantBody) {
		t.Errorf("JSON body = %#v, want %#v", gotBody, wantBody)
	}
}

// TestGlobalSkillMutationsUseResolvedScope verifies that TUI mutations target a
// global skill's root instead of silently sending the project scope.
func TestGlobalSkillMutationsUseResolvedScope(t *testing.T) {
	const skillsHTML = `<div data-skill-handle="global-skill" data-skill-name="Global Skill"
		data-skill-description="global description" data-skill-enabled="true" data-skill-always-use="false" data-skill-scope="global"></div>`

	tests := []struct {
		name     string
		line     string
		method   string
		path     string
		wantBody map[string]any
		confirm  bool
	}{
		{
			name:   "edit",
			line:   "/skills edit global-skill | new body",
			method: http.MethodPut,
			path:   "/skills/global-skill",
			wantBody: map[string]any{
				"handle": "global-skill", "name": "Global Skill", "description": "global description",
				"scope": "global", "body": "new body", "enabled": true,
			},
		},
		{
			name:     "enable",
			line:     "/skills enable global-skill",
			method:   http.MethodPost,
			path:     "/skills/global-skill/enabled",
			wantBody: map[string]any{"enabled": true, "scope": "global"},
		},
		{
			name:     "disable",
			line:     "/skills disable global-skill",
			method:   http.MethodPost,
			path:     "/skills/global-skill/enabled",
			wantBody: map[string]any{"enabled": false, "scope": "global"},
		},
		{
			name:     "always",
			line:     "/skills always global-skill",
			method:   http.MethodPost,
			path:     "/skills/global-skill/always_use",
			wantBody: map[string]any{"always_use": true, "scope": "global"},
		},
		{
			name:     "load alias",
			line:     "/skills load global-skill",
			method:   http.MethodPost,
			path:     "/skills/global-skill/always_use",
			wantBody: map[string]any{"always_use": true, "scope": "global"},
		},
		{
			name:    "delete",
			line:    "/skills delete global-skill",
			method:  http.MethodDelete,
			path:    "/skills/global-skill",
			confirm: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath, gotProject, gotScope, gotContentType string
			var gotBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				isSkillsRead := r.Method == http.MethodGet && r.URL.Path == "/skills"
				if !isSkillsRead {
					gotMethod = r.Method
					gotPath = r.URL.Path
					gotProject = r.URL.Query().Get("project_id")
					gotScope = r.URL.Query().Get("scope")
					gotContentType = r.Header.Get("Content-Type")
				}
				switch {
				case isSkillsRead:
					w.Header().Set("Content-Type", "text/html")
					_, _ = w.Write([]byte(skillsHTML))
				case r.Method == http.MethodDelete && r.URL.Path == "/skills/global-skill":
					if gotScope != "global" {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(`{"error":"global scope required"}`))
						return
					}
					w.Header().Set("Content-Type", "text/html")
					_, _ = w.Write([]byte(skillsHTML))
				default:
					if gotContentType != "application/json" {
						w.WriteHeader(http.StatusUnsupportedMediaType)
						_, _ = w.Write([]byte(`{"error":"skill mutations require JSON"}`))
						return
					}
					raw, err := io.ReadAll(r.Body)
					if err != nil {
						t.Errorf("read mutation body: %v", err)
					}
					if err := json.Unmarshal(raw, &gotBody); err != nil {
						t.Errorf("decode mutation JSON %q: %v", raw, err)
					}
					if gotBody["scope"] != "global" {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(`{"error":"global JSON scope required"}`))
						return
					}
					w.Header().Set("Content-Type", "text/html")
					_, _ = w.Write([]byte(skillsHTML))
				}
			}))
			t.Cleanup(srv.Close)

			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			m := New(c)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			m = updated.(Model)
			m.selectedID = "p1"
			m.selectedName = "demo"
			m = runLine(t, m, tc.line)
			if tc.confirm {
				m = runLine(t, m, "yes")
			}

			if out := transcript(m); strings.Contains(out, "error:") {
				t.Fatalf("global skill mutation was rejected:\n%s", out)
			}
			if gotMethod != tc.method || gotPath != tc.path || gotProject != "p1" {
				t.Fatalf("request = %s %s?project_id=%s&scope=%s, want %s %s?project_id=p1", gotMethod, gotPath, gotProject, gotScope, tc.method, tc.path)
			}
			if tc.wantBody == nil {
				if gotScope != "global" {
					t.Fatalf("scope query = %q, want global", gotScope)
				}
			} else if gotScope != "" {
				t.Errorf("JSON mutation unexpectedly sent scope query %q", gotScope)
			}
			if tc.wantBody != nil {
				if gotContentType != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", gotContentType)
				}
				if !reflect.DeepEqual(gotBody, tc.wantBody) {
					t.Errorf("JSON body = %#v, want %#v", gotBody, tc.wantBody)
				}
			}
		})
	}
}

// TestSkillsCommandsRequireSelectedProject verifies that skill commands fail
// locally before parsing, selectors, confirmations, or backend fallback scope.
func TestSkillsCommandsRequireSelectedProject(t *testing.T) {
	cases := []string{
		"/skills",
		"/skills add new-skill | description | body",
		"/skills show deploy",
		"/skills edit deploy | new body",
		"/skills delete deploy",
		"/skills enable deploy",
		"/skills disable deploy",
		"/skills always deploy",
		"/skills load deploy",
	}
	for _, line := range cases {
		line := line
		t.Run(line, func(t *testing.T) {
			m, rec := dispatchModel(t, nil)
			m.selectedID = ""
			m.selectedName = ""

			m = runLine(t, m, line)
			out := transcript(m)
			if !strings.Contains(out, "no project selected") {
				t.Fatalf("expected no-project error for %s:\n%s", line, out)
			}
			if calls := rec.all(); calls != "" {
				t.Fatalf("%s must not make backend requests:\n%s", line, calls)
			}
			if m.selectorActive {
				t.Errorf("%s must not open the selector", line)
			}
			if m.pendingConfirmation != nil {
				t.Errorf("%s must not set pending confirmation", line)
			}
		})
	}
}
func TestPersonalityCommandsRequireSelectedProject(t *testing.T) {
	cases := []string{
		"/personality",
		"/personality list",
		"/personality show release_coach",
		"/personality add Release Coach | Keep releases safe in production deployments.",
		"/personality edit release_coach | Updated | description | Keep releases safe in production deployments.",
		"/personality set release_coach",
		"/personality delete release_coach",
		"/personality show",
		"/personality delete",
	}
	for _, line := range cases {
		line := line
		t.Run(line, func(t *testing.T) {
			m, rec := dispatchModel(t, nil)
			m.selectedID = ""
			m.selectedName = ""

			m = runLine(t, m, line)
			if !strings.Contains(transcript(m), "no project selected") {
				t.Fatalf("expected no-project error for %s:\n%s", line, transcript(m))
			}
			if calls := rec.all(); calls != "" {
				t.Fatalf("%s made backend requests without a project:\n%s", line, calls)
			}
			if m.selectorActive {
				t.Errorf("%s opened a selector without a project", line)
			}
			if m.pendingConfirmation != nil {
				t.Errorf("%s opened confirmation without a project", line)
			}
		})
	}
}

func TestAgentsCommandsRequireSelectedProject(t *testing.T) {
	cases := []string{
		"/agents",
		"/agents list",
		"/agent",
		"/agents generate Build reviewer",
		"/agent generate Build reviewer",
		"/agents delete Reviewer",
		"/agent delete Reviewer",
		"/agents delete",
		"/agent delete",
	}
	for _, line := range cases {
		line := line
		t.Run(line, func(t *testing.T) {
			m, rec := dispatchModel(t, nil)
			m.selectedID = ""
			m.selectedName = ""

			m = runLine(t, m, line)
			out := stripANSI(transcript(m))
			if !strings.Contains(out, "no project selected — use /project <name>") {
				t.Fatalf("expected no-project guidance for %s:\n%s", line, out)
			}
			if calls := rec.all(); calls != "" {
				t.Fatalf("%s made backend requests without a selected project:\n%s", line, calls)
			}
			if m.busy {
				t.Fatalf("%s left the model busy", line)
			}
			if m.selectorActive {
				t.Fatalf("%s opened a selector without a selected project", line)
			}
			if m.pendingConfirmation != nil {
				t.Fatalf("%s opened confirmation without a selected project", line)
			}
		})
	}
}

func TestAgentsMetricsRemainsGlobalWithoutSelectedProject(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{
		"/api/workflows/metrics":        `[]`,
		"/api/workflows/best-agent":     `{}`,
		"/api/workflows/cheapest-agent": `{}`,
	})
	m.selectedID = ""
	m.selectedName = ""

	m = runLine(t, m, "/agents metrics")
	for _, path := range []string{
		"/api/workflows/metrics",
		"/api/workflows/best-agent",
		"/api/workflows/cheapest-agent",
	} {
		if got := rec.count("GET", path); got != 1 {
			t.Fatalf("global metrics request %s count = %d, want one:\n%s", path, got, rec.all())
		}
	}
	if out := stripANSI(transcript(m)); strings.Contains(out, "no project selected") {
		t.Fatalf("global metrics unexpectedly required a project:\n%s", out)
	}
	if m.busy {
		t.Fatal("global metrics left the model busy")
	}
}

func TestAgentsGenerateDelete(t *testing.T) {
	const agentsHTML = `<div data-agent-id="ag-1" data-agent-key="reviewer" data-agent-name="Reviewer"
		data-agent-description="reviews code" data-agent-model="claude" data-agent-scope="project"></div>`

	t.Run("generate", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/agents": agentsHTML})
		m = runLine(t, m, "/agents generate a reviewer agent")
		if !rec.saw("POST", "/agents/generate") {
			t.Errorf("calls:\n%s", rec.all())
		}
		if !rec.sawQuery("project_id=p1") {
			t.Errorf("generate requests lost selected project scope:\n%s", rec.all())
		}
		out := transcript(m)
		if !strings.Contains(out, "generated an agent from your description") {
			t.Errorf("expected status line:\n%s", out)
		}
		if !strings.Contains(out, "Reviewer") {
			t.Errorf("expected refreshed agents:\n%s", out)
		}
	})

	t.Run("delete", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/agents": agentsHTML})
		m = runLine(t, m, "/agents delete Reviewer") // sets pendingConfirmation
		m = runLine(t, m, "yes")                     // confirms and executes
		if !rec.saw("DELETE", "/agents/ag-1") {
			t.Errorf("calls:\n%s", rec.all())
		}
		out := transcript(m)
		if !strings.Contains(out, "deleted Reviewer") {
			t.Errorf("expected status line:\n%s", out)
		}
	})
}

// TestRefreshFailureAfterMutationIsSwallowedAcrossCommands exercises the
// refresh-swallow policy for alerts, skills, agents, and models (in addition
// to tasks, covered by TestRefreshFailureAfterMutationIsSwallowed) to confirm
// it is applied consistently by the shared refreshAndRender helper.
func TestRefreshFailureAfterMutationIsSwallowedAcrossCommands(t *testing.T) {
	failEndpointAfterFirstGET := func(path, okBody string) http.HandlerFunc {
		var gets int
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" && r.URL.Path == path {
				gets++
				if gets > 1 {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"error":"refresh failed"}`))
					return
				}
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte(okBody))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}
	}

	newModel := func(t *testing.T, handler http.HandlerFunc) Model {
		t.Helper()
		srv := httptest.NewServer(handler)
		t.Cleanup(srv.Close)
		c, err := client.New(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		m := New(c)
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		m = updated.(Model)
		m.selectedID = "p1"
		m.selectedName = "demo"
		return m
	}

	t.Run("alerts", func(t *testing.T) {
		const alertsHTML = `<div class="card" data-alert-id="a-1" data-alert-scroll-anchor="a-1"
		  data-search-text="build failed">
		  <p class="font-semibold">Build failed</p>
		</div>`
		m := newModel(t, failEndpointAfterFirstGET("/alerts", alertsHTML))
		m = runLine(t, m, "/alerts approve a-1")
		out := transcript(m)
		if !strings.Contains(out, "approve: Build failed") {
			t.Errorf("expected status line despite refresh failure:\n%s", out)
		}
		if strings.Contains(out, "refresh failed") {
			t.Errorf("refresh failure must be swallowed:\n%s", out)
		}
	})

	t.Run("skills", func(t *testing.T) {
		const skillsHTML = `<div data-skill-handle="deploy" data-skill-name="Deploy"
			data-skill-enabled="true" data-skill-always-use="false" data-skill-scope="project"></div>`
		m := newModel(t, failEndpointAfterFirstGET("/skills", skillsHTML))
		m = runLine(t, m, "/skills disable deploy")
		out := transcript(m)
		if !strings.Contains(out, "disable: deploy") {
			t.Errorf("expected status line despite refresh failure:\n%s", out)
		}
		if strings.Contains(out, "refresh failed") {
			t.Errorf("refresh failure must be swallowed:\n%s", out)
		}
	})

	t.Run("agents", func(t *testing.T) {
		// Use "agents delete" rather than "generate": delete performs a
		// pre-mutation ListAgents lookup (via matchRef) followed by the
		// post-mutation refreshAndRender refetch, producing two GETs to
		// /agents. This ensures failEndpointAfterFirstGET actually fails
		// the second (refresh) GET and meaningfully exercises the
		// swallow path. "generate" issues only one GET (the refresh
		// itself), so the failure would never trigger.
		const agentsHTML = `<div data-agent-id="ag-1" data-agent-key="reviewer" data-agent-name="Reviewer"
			data-agent-description="reviews code" data-agent-model="claude" data-agent-scope="project"></div>`
		m := newModel(t, failEndpointAfterFirstGET("/agents", agentsHTML))
		m = runLine(t, m, "/agents delete reviewer") // sets pendingConfirmation
		m = runLine(t, m, "yes")                     // confirms and executes
		out := transcript(m)
		if !strings.Contains(out, "deleted Reviewer") {
			t.Errorf("expected status line despite refresh failure:\n%s", out)
		}
		if strings.Contains(out, "refresh failed") {
			t.Errorf("refresh failure must be swallowed:\n%s", out)
		}
	})

	t.Run("models", func(t *testing.T) {
		const modelsHTML = `<div data-model-id="m-1" data-model-name="Sonnet"
			data-model-provider="anthropic" data-model-model="claude-sonnet-4"></div>`
		m := newModel(t, failEndpointAfterFirstGET("/models", modelsHTML))
		m = runLine(t, m, "/models default Sonnet")
		out := transcript(m)
		if !strings.Contains(out, "default: Sonnet") {
			t.Errorf("expected status line despite refresh failure:\n%s", out)
		}
		if strings.Contains(out, "refresh failed") {
			t.Errorf("refresh failure must be swallowed:\n%s", out)
		}
	})
}

// --- channels ---

func TestChannelsCommandsRequireProjectAndPreserveScope(t *testing.T) {
	const channelsPage = `<html><body>Telegram: connected  Slack: disconnected</body></html>`
	cases := []struct {
		name         string
		line         string
		method       string
		path         string
		confirmation bool
	}{
		{name: "bare", line: "/channels", method: "GET", path: "/channels"},
		{name: "list", line: "/channels list", method: "GET", path: "/channels"},
		{name: "test", line: "/channels test telegram", method: "POST", path: "/channels/telegram/test"},
		{name: "remove", line: "/channels remove slack", method: "POST", path: "/channels/slack/disconnect", confirmation: true},
		{name: "integrations alias", line: "/integrations", method: "GET", path: "/channels"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run("no project/"+tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/channels": channelsPage})
			m.selectedID = ""
			m.selectedName = ""

			m = runLine(t, m, tc.line)
			if out := transcript(m); !strings.Contains(out, "no project selected — use /project <name>") {
				t.Fatalf("expected no-project guidance for %s:\n%s", tc.line, out)
			}
			if calls := rec.all(); calls != "" {
				t.Fatalf("%s must not make backend requests:\n%s", tc.line, calls)
			}
			if m.selectorActive {
				t.Fatalf("%s opened a selector without a selected project", tc.line)
			}
			if m.pendingConfirmation != nil {
				t.Fatalf("%s opened a confirmation without a selected project", tc.line)
			}
			if m.busy {
				t.Fatalf("%s left the model busy", tc.line)
			}
		})

		t.Run("selected project/"+tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/channels": channelsPage})
			if tc.confirmation {
				m = confirmDestructive(t, m, tc.line)
			} else {
				m = runLine(t, m, tc.line)
			}

			if !rec.saw(tc.method, tc.path) {
				t.Fatalf("expected %s %s, calls:\n%s", tc.method, tc.path, rec.all())
			}
			rec.mu.Lock()
			urls := append([]string(nil), rec.urls...)
			rec.mu.Unlock()
			for _, uri := range urls {
				if !strings.Contains(uri, "project_id=p1") {
					t.Errorf("%s sent an unscoped request: %s", tc.line, uri)
				}
			}
			if tc.confirmation && m.pendingConfirmation != nil {
				t.Fatalf("%s left a pending confirmation after confirmation", tc.line)
			}
		})
	}
}

// TestChannelsCommandListsPage verifies the no-arg /channels command fetches
// and renders the integrations page unchanged from the read-only behavior.
func TestChannelsCommandListsPage(t *testing.T) {
	const channelsPage = `<html><body>Telegram: connected  Slack: disconnected</body></html>`
	m, rec := dispatchModel(t, map[string]string{"/channels": channelsPage})
	m = runLine(t, m, "/channels")
	if !rec.saw("GET", "/channels") {
		t.Fatalf("expected a channels page fetch, calls:\n%s", rec.all())
	}
	out := transcript(m)
	if !strings.Contains(out, "Telegram") {
		t.Errorf("expected channels page content:\n%s", out)
	}
	if strings.Contains(out, "error:") {
		t.Errorf("/channels should not error:\n%s", out)
	}
}

// TestChannelsTestAndRemove exercises the test and remove actions for all
// manageable channel types. Slack remove must route to /disconnect, not /remove.
func TestChannelsTestAndRemove(t *testing.T) {
	const refreshedChannelsPage = `<html><body>refreshed channels page</body></html>`
	cases := []struct {
		action      string
		channelName string
		wantPath    string
	}{
		{"test", "telegram", "/channels/telegram/test"},
		{"remove", "telegram", "/channels/telegram/remove"},
		{"test", "slack", "/channels/slack/test"},
		{"remove", "slack", "/channels/slack/disconnect"},
		{"test", "discord", "/channels/discord/test"},
		{"remove", "discord", "/channels/discord/remove"},
		{"test", "email", "/channels/email/test"},
		{"remove", "email", "/channels/email/remove"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.action+" "+tc.channelName, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/channels": refreshedChannelsPage})
			if tc.action == "remove" {
				m = confirmDestructive(t, m, "/channels "+tc.action+" "+tc.channelName)
			} else {
				m = runLine(t, m, "/channels "+tc.action+" "+tc.channelName)
			}
			if !rec.saw("POST", tc.wantPath) {
				t.Errorf("expected POST %s, calls:\n%s", tc.wantPath, rec.all())
			}
			out := transcript(m)
			if strings.Contains(out, "error:") {
				t.Errorf("unexpected error for %s %s:\n%s", tc.action, tc.channelName, out)
			}
			// Status line must name the channel (display names are Title-cased in KnownChannels).
			displayName := strings.ToUpper(tc.channelName[:1]) + tc.channelName[1:]
			if !strings.Contains(out, tc.action+": "+displayName) {
				t.Errorf("expected status line %q:\n%s", tc.action+": "+displayName, out)
			}
			if !strings.Contains(out, "refreshed channels page") {
				t.Errorf("expected refreshed channels page after action:\n%s", out)
			}
		})
	}
}

// TestChannelsMatchRefResolution verifies prefix and substring resolution work
// against the fixed channel list, and that ambiguous/unknown refs are rejected.
func TestChannelsMatchRefResolution(t *testing.T) {
	t.Run("unique prefix match", func(t *testing.T) {
		m, rec := dispatchModel(t, nil)
		m = runLine(t, m, "/channels test tele") // prefix of "telegram"
		if !rec.saw("POST", "/channels/telegram/test") {
			t.Errorf("prefix match failed, calls:\n%s", rec.all())
		}
		if strings.Contains(transcript(m), "error:") {
			t.Errorf("unexpected error:\n%s", transcript(m))
		}
	})

	t.Run("unknown reference rejected", func(t *testing.T) {
		m, rec := dispatchModel(t, nil)
		m = runLine(t, m, "/channels test irc")
		// No mutation must be posted.
		for _, method := range []string{"POST"} {
			_ = method
		}
		if rec.saw("POST", "/channels/irc/test") {
			t.Errorf("unknown channel must not be dispatched:\n%s", rec.all())
		}
		if !strings.Contains(transcript(m), "error:") {
			t.Errorf("expected an error for unknown channel:\n%s", transcript(m))
		}
	})
}

func TestChannelsRemoveRequiresConfirmation(t *testing.T) {
	const refreshedChannelsPage = `<html><body>refreshed channels page</body></html>`

	t.Run("slack_no_call_before_confirm", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/channels": refreshedChannelsPage})
		m = runLine(t, m, "/channels remove slack")
		if rec.saw("POST", "/channels/slack/disconnect") {
			t.Errorf("backend must not be called before confirmation:\n%s", rec.all())
		}
		if m.pendingConfirmation == nil {
			t.Error("pendingConfirmation must be set")
		}
	})

	t.Run("slack_called_after_yes_and_refreshes", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/channels": refreshedChannelsPage})
		m = confirmDestructive(t, m, "/channels remove slack")
		if !rec.saw("POST", "/channels/slack/disconnect") {
			t.Errorf("expected Slack disconnect after yes:\n%s", rec.all())
		}
		if !rec.saw("GET", "/channels") {
			t.Errorf("expected channels refresh after remove:\n%s", rec.all())
		}
		out := transcript(m)
		if !strings.Contains(out, "remove: Slack") || !strings.Contains(out, "refreshed channels page") {
			t.Errorf("expected status and refreshed output:\n%s", out)
		}
	})

	t.Run("email_cancelled_after_non_yes", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/channels": refreshedChannelsPage})
		m = runLine(t, m, "/channels remove email")
		m = runLine(t, m, "no")
		if rec.saw("POST", "/channels/email/remove") {
			t.Errorf("backend must not be called after non-yes input:\n%s", rec.all())
		}
		if !strings.Contains(transcript(m), "cancelled") {
			t.Errorf("expected cancellation message:\n%s", transcript(m))
		}
	})

	t.Run("email_cancelled_after_esc", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/channels": refreshedChannelsPage})
		m = runLine(t, m, "/channels remove email")
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = next.(Model)
		if rec.saw("POST", "/channels/email/remove") {
			t.Errorf("backend must not be called after Esc:\n%s", rec.all())
		}
		if !strings.Contains(transcript(m), "cancelled") {
			t.Errorf("expected cancellation message:\n%s", transcript(m))
		}
	})
}

func TestChannelsReloadFailureAfterActionIsSwallowed(t *testing.T) {
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/channels/telegram/test" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
			return
		}
		if r.Method == "GET" && r.URL.Path == "/channels" {
			http.Error(w, "refresh failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})

	m = runLine(t, m, "/channels test telegram")
	out := transcript(m)
	if !strings.Contains(out, "test: Telegram") {
		t.Errorf("expected status line despite refresh failure:\n%s", out)
	}
	if strings.Contains(out, "error:") || strings.Contains(out, "refresh failed") {
		t.Errorf("refresh failure must be swallowed:\n%s", out)
	}
}

// TestChannelsBackendFailureSurfaces ensures a non-2xx backend response for a
// channel action is surfaced as an error instead of a false success.
func TestChannelsBackendFailureSurfaces(t *testing.T) {
	for _, action := range []string{"test", "remove"} {
		action := action
		t.Run(action, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/channels/telegram/") {
					http.Error(w, "boom", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()

			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			m := New(c)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			m = updated.(Model)
			m.selectedID = "p1"
			m.selectedName = "demo"

			if action == "remove" {
				m = confirmDestructive(t, m, "/channels "+action+" telegram")
			} else {
				m = runLine(t, m, "/channels "+action+" telegram")
			}
			out := transcript(m)
			if !strings.Contains(out, "error:") {
				t.Errorf("expected a backend error for %s:\n%s", action, out)
			}
		})
	}
}

// TestChannelsMissingArgOpensSelector verifies that test/remove with no
// channel name open the inline channel selector without posting to the
// backend, and that CLI mode keeps the usage error.
func TestChannelsMissingArgOpensSelector(t *testing.T) {
	for _, action := range []string{"test", "remove"} {
		action := action
		t.Run(action, func(t *testing.T) {
			m, rec := dispatchModel(t, nil)
			m = runLine(t, m, "/channels "+action)
			if rec.saw("POST", "/channels") {
				t.Errorf("must not POST when channel name is missing:\n%s", rec.all())
			}
			if !m.selectorActive {
				t.Errorf("expected selector mode:\n%s", transcript(m))
			}
			if m.pendingCommand != "channels "+action {
				t.Errorf("pendingCommand = %q, want %q", m.pendingCommand, "channels "+action)
			}
			if len(m.selectorItems) != len(client.KnownChannels) {
				t.Errorf("selector items = %d, want %d", len(m.selectorItems), len(client.KnownChannels))
			}
		})
		t.Run(action+"_cli", func(t *testing.T) {
			cliMode = true
			defer func() { cliMode = false }()
			m, rec := dispatchModel(t, nil)
			m = runLine(t, m, "/channels "+action)
			if rec.saw("POST", "/channels") {
				t.Errorf("must not POST when channel name is missing:\n%s", rec.all())
			}
			if !strings.Contains(transcript(m), "error:") {
				t.Errorf("expected a usage error:\n%s", transcript(m))
			}
		})
	}
}

// newModelFromHandler wires a Model to a custom HTTP handler.
func newModelFromHandler(t *testing.T, h http.HandlerFunc) Model {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.selectedID = "p1"
	m.selectedName = "demo"
	return m
}

// TestGenerateThenFetch verifies that pulseCommand, reflectionCommand, and
// insightsCommand delegate to generateThenFetch: the no-action path fetches
// without calling generate, and the trigger-action path calls generate then
// fetch with a generate failure short-circuiting before fetch.
func TestGenerateThenFetch(t *testing.T) {
	// pulse: bare command — only fetch, no generate
	t.Run("pulse show fetches without generate", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/upcoming": "<div>upcoming briefing</div>"})
		m = runLine(t, m, "/pulse")
		if !rec.saw("GET", "/upcoming") {
			t.Errorf("expected GET /upcoming; calls:\n%s", rec.all())
		}
		if rec.saw("POST", "/upcoming/summary") {
			t.Errorf("unexpected POST /upcoming/summary for bare /pulse; calls:\n%s", rec.all())
		}
		if strings.Contains(transcript(m), "error:") {
			t.Errorf("unexpected error:\n%s", transcript(m))
		}
	})

	// pulse: summary — generate then fetch; generate failure short-circuits
	t.Run("pulse summary calls generate then fetch", func(t *testing.T) {
		var generateCalled, fetchCalled bool
		m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			if r.Method == "POST" && r.URL.Path == "/upcoming/summary" {
				generateCalled = true
				_, _ = w.Write([]byte("ok"))
				return
			}
			if r.Method == "GET" && r.URL.Path == "/upcoming" {
				fetchCalled = true
				_, _ = w.Write([]byte("<div>upcoming briefing</div>"))
				return
			}
			_, _ = w.Write([]byte("{}"))
		})
		m = runLine(t, m, "/pulse summary")
		if !generateCalled {
			t.Error("expected GeneratePulseSummary (POST /upcoming/summary) to be called")
		}
		if !fetchCalled {
			t.Error("expected GetPulse (GET /upcoming) to be called after generate")
		}
		if strings.Contains(transcript(m), "error:") {
			t.Errorf("unexpected error:\n%s", transcript(m))
		}
	})

	t.Run("pulse summary generate failure short-circuits", func(t *testing.T) {
		var fetchCalled bool
		m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" && r.URL.Path == "/upcoming/summary" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if r.Method == "GET" && r.URL.Path == "/upcoming" {
				fetchCalled = true
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<div>upcoming briefing</div>"))
		})
		m = runLine(t, m, "/pulse summary")
		if fetchCalled {
			t.Error("fetch (GET /upcoming) must not be called when generate fails")
		}
		if !strings.Contains(transcript(m), "error:") {
			t.Errorf("expected error in transcript:\n%s", transcript(m))
		}
	})

	// reflection: bare command — only fetch, no generate
	t.Run("reflection show fetches without generate", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/history": "<div>history debrief</div>"})
		m = runLine(t, m, "/reflection")
		if !rec.saw("GET", "/history") {
			t.Errorf("expected GET /history; calls:\n%s", rec.all())
		}
		if rec.saw("POST", "/history/summary") {
			t.Errorf("unexpected POST /history/summary for bare /reflection; calls:\n%s", rec.all())
		}
		if strings.Contains(transcript(m), "error:") {
			t.Errorf("unexpected error:\n%s", transcript(m))
		}
	})

	// reflection: summary — generate then fetch; generate failure short-circuits
	t.Run("reflection summary calls generate then fetch", func(t *testing.T) {
		var generateCalled, fetchCalled bool
		m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			if r.Method == "POST" && r.URL.Path == "/history/summary" {
				generateCalled = true
				_, _ = w.Write([]byte("ok"))
				return
			}
			if r.Method == "GET" && r.URL.Path == "/history" {
				fetchCalled = true
				_, _ = w.Write([]byte("<div>history debrief</div>"))
				return
			}
			_, _ = w.Write([]byte("{}"))
		})
		m = runLine(t, m, "/reflection summary")
		if !generateCalled {
			t.Error("expected GenerateReflectionSummary (POST /history/summary) to be called")
		}
		if !fetchCalled {
			t.Error("expected GetReflection (GET /history) to be called after generate")
		}
		if strings.Contains(transcript(m), "error:") {
			t.Errorf("unexpected error:\n%s", transcript(m))
		}
	})

	t.Run("reflection summary generate failure short-circuits", func(t *testing.T) {
		var fetchCalled bool
		m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" && r.URL.Path == "/history/summary" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if r.Method == "GET" && r.URL.Path == "/history" {
				fetchCalled = true
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<div>history debrief</div>"))
		})
		m = runLine(t, m, "/reflection summary")
		if fetchCalled {
			t.Error("fetch (GET /history) must not be called when generate fails")
		}
		if !strings.Contains(transcript(m), "error:") {
			t.Errorf("expected error in transcript:\n%s", transcript(m))
		}
	})

	// insights: bare command — only fetch, no generate
	t.Run("insights show fetches without generate", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/insights": "<div>insights content</div>"})
		m = runLine(t, m, "/insights")
		if !rec.saw("GET", "/insights") {
			t.Errorf("expected GET /insights; calls:\n%s", rec.all())
		}
		if rec.saw("POST", "/insights/analyze") {
			t.Errorf("unexpected POST /insights/analyze for bare /insights; calls:\n%s", rec.all())
		}
		if strings.Contains(transcript(m), "error:") {
			t.Errorf("unexpected error:\n%s", transcript(m))
		}
	})

	// insights: analyze — generate then fetch; generate failure short-circuits
	t.Run("insights analyze calls generate then fetch", func(t *testing.T) {
		var generateCalled, fetchCalled bool
		m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			if r.Method == "POST" && r.URL.Path == "/insights/analyze" {
				generateCalled = true
				_, _ = w.Write([]byte("ok"))
				return
			}
			if r.Method == "GET" && r.URL.Path == "/insights" {
				fetchCalled = true
				_, _ = w.Write([]byte("<div>insights content</div>"))
				return
			}
			_, _ = w.Write([]byte("{}"))
		})
		m = runLine(t, m, "/insights analyze")
		if !generateCalled {
			t.Error("expected RunInsightsAnalysis (POST /insights/analyze) to be called")
		}
		if !fetchCalled {
			t.Error("expected GetInsights (GET /insights) to be called after generate")
		}
		if strings.Contains(transcript(m), "error:") {
			t.Errorf("unexpected error:\n%s", transcript(m))
		}
	})

	t.Run("insights analyze generate failure short-circuits", func(t *testing.T) {
		var fetchCalled bool
		m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" && r.URL.Path == "/insights/analyze" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if r.Method == "GET" && r.URL.Path == "/insights" {
				fetchCalled = true
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<div>insights content</div>"))
		})
		m = runLine(t, m, "/insights analyze")
		if fetchCalled {
			t.Error("fetch (GET /insights) must not be called when generate fails")
		}
		if !strings.Contains(transcript(m), "error:") {
			t.Errorf("expected error in transcript:\n%s", transcript(m))
		}
	})

	// grades: bare command — only GET /history, no POST /history/grade-ideas
	t.Run("grades show fetches without generate", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/history": `<div id="idea-grade-content">grades content</div>`})
		m = runLine(t, m, "/grades")
		if !rec.saw("GET", "/history") {
			t.Errorf("expected GET /history; calls:\n%s", rec.all())
		}
		if rec.saw("POST", "/history/grade-ideas") {
			t.Errorf("unexpected POST /history/grade-ideas for bare /grades; calls:\n%s", rec.all())
		}
		if strings.Contains(transcript(m), "error:") {
			t.Errorf("unexpected error:\n%s", transcript(m))
		}
	})

	// grades run: POST /history/grade-ideas then exactly one GET /history; failure short-circuits
	t.Run("grades run posts then fetches history once", func(t *testing.T) {
		var mu sync.Mutex
		var calls []string
		m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			calls = append(calls, r.Method+" "+r.URL.Path)
			mu.Unlock()

			w.Header().Set("Content-Type", "text/html")
			if r.Method == "POST" && r.URL.Path == "/history/grade-ideas" {
				_, _ = w.Write([]byte("ok"))
				return
			}
			if r.Method == "GET" && r.URL.Path == "/history" {
				_, _ = w.Write([]byte(`<div id="idea-grade-content">grades content</div>`))
				return
			}
			_, _ = w.Write([]byte("{}"))
		})
		m = runLine(t, m, "/grades run")

		mu.Lock()
		postIndex := -1
		getHistoryAfterPost := 0
		for i, call := range calls {
			if call == "POST /history/grade-ideas" && postIndex == -1 {
				postIndex = i
			}
			if postIndex != -1 && i > postIndex && call == "GET /history" {
				getHistoryAfterPost++
			}
		}
		gotCalls := strings.Join(calls, "\n")
		mu.Unlock()

		if postIndex == -1 {
			t.Fatalf("expected GradeIdeas (POST /history/grade-ideas) to be called, calls:\n%s", gotCalls)
		}
		if getHistoryAfterPost != 1 {
			t.Fatalf("expected exactly one GetGrades (GET /history) after grading post, got %d; calls:\n%s", getHistoryAfterPost, gotCalls)
		}
		if strings.Contains(transcript(m), "error:") {
			t.Errorf("unexpected error:\n%s", transcript(m))
		}
	})

	t.Run("grades run generate failure short-circuits", func(t *testing.T) {
		var fetchCalled bool
		m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" && r.URL.Path == "/history/grade-ideas" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if r.Method == "GET" && r.URL.Path == "/history" {
				fetchCalled = true
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div id="idea-grade-content">grades content</div>`))
		})
		m = runLine(t, m, "/grades run")
		if fetchCalled {
			t.Error("fetch (GET /history) must not be called when generate fails")
		}
		if !strings.Contains(transcript(m), "error:") {
			t.Errorf("expected error in transcript:\n%s", transcript(m))
		}
	})
}

func TestProjectScopedBriefingCommandsRequireSelectionAndPreserveScope(t *testing.T) {
	type testCase struct {
		name        string
		line        string
		fetchPath   string
		fetchOutput string
		triggerPath string
	}

	cases := []testCase{
		{name: "pulse", line: "/pulse", fetchPath: "/upcoming", fetchOutput: "pulse output"},
		{name: "pulse summary", line: "/pulse summary", fetchPath: "/upcoming", fetchOutput: "pulse output", triggerPath: "/upcoming/summary"},
		{name: "reflection", line: "/reflection", fetchPath: "/history", fetchOutput: "reflection output"},
		{name: "reflection summary", line: "/reflection summary", fetchPath: "/history", fetchOutput: "reflection output", triggerPath: "/history/summary"},
		{name: "grades", line: "/grades", fetchPath: "/history", fetchOutput: "grades output"},
		{name: "grades run", line: "/grades run", fetchPath: "/history", fetchOutput: "grades output", triggerPath: "/history/grade-ideas"},
		{name: "insights", line: "/insights", fetchPath: "/insights", fetchOutput: "insights output"},
		{name: "insights analyze", line: "/insights analyze", fetchPath: "/insights", fetchOutput: "insights output", triggerPath: "/insights/analyze"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run("no project/"+tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{
				tc.fetchPath: "<div>" + tc.fetchOutput + "</div>",
			})
			m.selectedID = ""
			m.selectedName = ""

			m = runLine(t, m, tc.line)
			if calls := rec.all(); calls != "" {
				t.Fatalf("%s made backend requests without a selected project:\n%s", tc.line, calls)
			}
			out := transcript(m)
			if !strings.Contains(out, "no project selected — use /project <name>") {
				t.Fatalf("%s missing actionable no-project guidance:\n%s", tc.line, out)
			}
			if m.busy {
				t.Fatalf("%s left the model busy", tc.line)
			}
			if m.selectorActive {
				t.Fatalf("%s opened a selector without a selected project", tc.line)
			}
			if m.pendingConfirmation != nil {
				t.Fatalf("%s opened a confirmation without a selected project", tc.line)
			}
		})

		t.Run("selected project/"+tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{
				tc.fetchPath: "<div>" + tc.fetchOutput + "</div>",
			})
			m = runLine(t, m, tc.line)

			if !rec.saw("GET", tc.fetchPath) {
				t.Fatalf("%s did not fetch %s:\n%s", tc.line, tc.fetchPath, rec.all())
			}
			if tc.triggerPath != "" && !rec.saw("POST", tc.triggerPath) {
				t.Fatalf("%s did not trigger %s:\n%s", tc.line, tc.triggerPath, rec.all())
			}
			if tc.triggerPath == "" && strings.Contains(rec.all(), "POST") {
				t.Fatalf("%s unexpectedly triggered a regeneration:\n%s", tc.line, rec.all())
			}
			if !strings.Contains(transcript(m), tc.fetchOutput) {
				t.Fatalf("%s did not preserve fetched output:\n%s", tc.line, transcript(m))
			}
			if m.busy {
				t.Fatalf("%s left the model busy", tc.line)
			}

			rec.mu.Lock()
			urls := append([]string(nil), rec.urls...)
			rec.mu.Unlock()
			for _, url := range urls {
				if !strings.Contains(url, "project_id=p1") {
					t.Errorf("%s sent an unscoped request: %s", tc.line, url)
				}
			}
		})
	}
}

// TestDestructiveCommandsRequireConfirmation verifies VISION.md §"Operator Trust
// Matters": destructive commands in TUI mode must not call the backend until the
// user explicitly types "yes" and presses Enter, and must do nothing if the user
// cancels (types anything else or presses Esc).
func TestDestructiveCommandsRequireConfirmation(t *testing.T) {
	const agentsHTML = `<div data-agent-id="ag-1" data-agent-key="reviewer"
		data-agent-name="Reviewer" data-agent-description="reviews code"
		data-agent-model="claude" data-agent-scope="project"></div>`

	// /tasks delete — no call before confirm, called after "yes", not called after "no".
	t.Run("tasks_delete/no_call_before_confirm", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		m = runLine(t, m, "/tasks delete t-1")
		if rec.saw("DELETE", "/tasks/t-1") {
			t.Error("backend must not be called before confirmation")
		}
		if m.pendingConfirmation == nil {
			t.Error("pendingConfirmation must be set")
		}
	})
	t.Run("tasks_delete/called_after_yes", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		confirmDestructive(t, m, "/tasks delete t-1")
		if !rec.saw("DELETE", "/tasks/t-1") {
			t.Errorf("expected DELETE after yes:\n%s", rec.all())
		}
	})
	t.Run("tasks_delete/not_called_after_no", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		m = runLine(t, m, "/tasks delete t-1")
		m = runLine(t, m, "no")
		if rec.saw("DELETE", "/tasks/t-1") {
			t.Errorf("backend must not be called after 'no':\n%s", rec.all())
		}
		if !strings.Contains(transcript(m), "cancelled") {
			t.Errorf("expected cancellation message:\n%s", transcript(m))
		}
	})
	t.Run("tasks_delete/not_called_after_esc", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		m = runLine(t, m, "/tasks delete t-1")
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = next.(Model)
		if rec.saw("DELETE", "/tasks/t-1") {
			t.Errorf("backend must not be called after Esc:\n%s", rec.all())
		}
		if !strings.Contains(transcript(m), "cancelled") {
			t.Errorf("expected cancellation message:\n%s", transcript(m))
		}
	})

	// /alerts clear — extra-dangerous bulk delete.
	t.Run("alerts_clear/no_call_before_confirm", func(t *testing.T) {
		m, rec := dispatchModel(t, nil)
		m = runLine(t, m, "/alerts clear")
		if rec.saw("DELETE", "/alerts") {
			t.Error("backend must not be called before confirmation")
		}
		if m.pendingConfirmation == nil {
			t.Error("pendingConfirmation must be set for /alerts clear")
		}
	})
	t.Run("alerts_clear/called_after_yes", func(t *testing.T) {
		m, rec := dispatchModel(t, nil)
		confirmDestructive(t, m, "/alerts clear")
		if !rec.saw("DELETE", "/alerts") {
			t.Errorf("expected DELETE /alerts after yes:\n%s", rec.all())
		}
	})
	t.Run("alerts_clear/not_called_after_no", func(t *testing.T) {
		m, rec := dispatchModel(t, nil)
		m = runLine(t, m, "/alerts clear")
		m = runLine(t, m, "no")
		if rec.saw("DELETE", "/alerts") {
			t.Errorf("backend must not be called after 'no':\n%s", rec.all())
		}
		if !strings.Contains(transcript(m), "cancelled") {
			t.Errorf("expected cancellation message:\n%s", transcript(m))
		}
	})

	// /agents delete.
	t.Run("agents_delete/no_call_before_confirm", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/agents": agentsHTML})
		m = runLine(t, m, "/agents delete Reviewer")
		if rec.saw("DELETE", "/agents/ag-1") {
			t.Error("backend must not be called before confirmation")
		}
		if m.pendingConfirmation == nil {
			t.Error("pendingConfirmation must be set for /agents delete")
		}
	})
	t.Run("agents_delete/called_after_yes", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/agents": agentsHTML})
		confirmDestructive(t, m, "/agents delete Reviewer")
		if !rec.saw("DELETE", "/agents/ag-1") {
			t.Errorf("expected DELETE after yes:\n%s", rec.all())
		}
	})
	t.Run("agents_delete/not_called_after_esc", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/agents": agentsHTML})
		m = runLine(t, m, "/agents delete Reviewer")
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = next.(Model)
		if rec.saw("DELETE", "/agents/ag-1") {
			t.Errorf("backend must not be called after Esc:\n%s", rec.all())
		}
		if !strings.Contains(transcript(m), "cancelled") {
			t.Errorf("expected cancellation message:\n%s", transcript(m))
		}
	})

	// Confirm that the prompt message is visible in the view.
	t.Run("confirmation_message_visible_in_view", func(t *testing.T) {
		m, _ := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		m.width, m.height = 120, 40
		m = runLine(t, m, "/tasks delete t-1")
		view := m.View()
		if !strings.Contains(view, "yes") {
			t.Errorf("confirmation prompt should be visible in view:\n%s", view)
		}
	})
}

// TestWorkersShowFetchesConcurrently verifies that the default "workers" show path
// launches GetWorkerSettings and GetGlobalCapacity concurrently instead of sequentially.
func TestWorkersShowFetchesConcurrently(t *testing.T) {
	const delay = 150 * time.Millisecond

	const capacityJSON = `{"total_running":2,"max_workers":8,"queue_size":0,"available_slots":6}`

	// Timing subtest: both handlers sleep delay; total should be < 2*delay.
	t.Run("both fetches run concurrently", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/workers":
				time.Sleep(delay)
				_, _ = w.Write([]byte("<html><body>worker settings page</body></html>"))
			case r.URL.Path == "/api/capacity/global":
				w.Header().Set("Content-Type", "application/json")
				time.Sleep(delay)
				_, _ = w.Write([]byte(capacityJSON))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()

		c, err := client.New(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		m := New(c)
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		m = updated.(Model)
		m.selectedID = "p1"
		m.selectedName = "demo"

		start := time.Now()
		m = runLine(t, m, "/workers")
		elapsed := time.Since(start)

		if elapsed >= 2*delay {
			t.Errorf("workers show took %v, want well under %v (fetches must run concurrently)", elapsed, 2*delay)
		}
		if strings.Contains(transcript(m), "error:") {
			t.Errorf("unexpected error:\n%s", transcript(m))
		}
	})

	// GetWorkerSettings failure propagates as an error.
	t.Run("GetWorkerSettings failure propagates", func(t *testing.T) {
		m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/workers":
				w.WriteHeader(http.StatusInternalServerError)
			case r.URL.Path == "/api/capacity/global":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(capacityJSON))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})
		m = runLine(t, m, "/workers")
		out := transcript(m)
		if !strings.Contains(out, "error:") {
			t.Errorf("expected error when GetWorkerSettings fails; got:\n%s", out)
		}
	})

	// GetGlobalCapacity failure is silently ignored; nil capacity still renders.
	t.Run("GetGlobalCapacity failure ignored", func(t *testing.T) {
		m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/workers":
				_, _ = w.Write([]byte("<html><body>worker settings page</body></html>"))
			case r.URL.Path == "/api/capacity/global":
				w.WriteHeader(http.StatusInternalServerError)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})
		m = runLine(t, m, "/workers")
		out := transcript(m)
		if strings.Contains(out, "error:") {
			t.Errorf("GetGlobalCapacity failure must not surface as an error; got:\n%s", out)
		}
	})
}

// TestAgentsMetricsFetchesConcurrently verifies that the "agents metrics" case
// fires GetAllAgentMetrics, GetBestAgent, and GetCheapestAgent concurrently.
func TestAgentsMetricsFetchesConcurrently(t *testing.T) {
	const delay = 150 * time.Millisecond

	const metricsJSON = `[{"id":"m1","agent_config_id":"ac-1","task_type":"coding","success_count":10,"failure_count":2,"avg_duration_ms":500,"avg_cost_cents":3,"avg_quality_score":0.85}]`
	const recJSON = `{"agent":{"name":"GPT-4"}}`

	// Timing subtest: all three sleep for delay; total should be < 2*delay.
	t.Run("all three fetches run concurrently", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/workflows/metrics":
				time.Sleep(delay)
				_, _ = w.Write([]byte(metricsJSON))
			case "/api/workflows/best-agent":
				time.Sleep(delay)
				_, _ = w.Write([]byte(recJSON))
			case "/api/workflows/cheapest-agent":
				time.Sleep(delay)
				_, _ = w.Write([]byte(recJSON))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()

		c, err := client.New(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		m := New(c)
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		m = updated.(Model)
		m.selectedID = "p1"
		m.selectedName = "demo"

		start := time.Now()
		m = runLine(t, m, "/agents metrics")
		elapsed := time.Since(start)

		if elapsed >= 2*delay {
			t.Errorf("agents metrics took %v, want well under %v (fetches must run concurrently)", elapsed, 2*delay)
		}
		if strings.Contains(transcript(m), "error:") {
			t.Errorf("unexpected error:\n%s", transcript(m))
		}
	})

	// Regression: all 3 healthy → full render with metrics table.
	t.Run("all healthy produces full render", func(t *testing.T) {
		m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/workflows/metrics":
				_, _ = w.Write([]byte(metricsJSON))
			case "/api/workflows/best-agent":
				_, _ = w.Write([]byte(recJSON))
			case "/api/workflows/cheapest-agent":
				_, _ = w.Write([]byte(recJSON))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})
		m = runLine(t, m, "/agents metrics")
		out := transcript(m)
		if strings.Contains(out, "error:") {
			t.Errorf("unexpected error:\n%s", out)
		}
		// renderAgentMetrics produces an AGENT column header
		if !strings.Contains(out, "AGENT") {
			t.Errorf("expected metrics table; got:\n%s", out)
		}
	})

	// Regression: /api/workflows/metrics failure → error returned, no render.
	t.Run("metrics endpoint failure returns error", func(t *testing.T) {
		m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/workflows/metrics":
				w.WriteHeader(http.StatusInternalServerError)
			case "/api/workflows/best-agent":
				_, _ = w.Write([]byte(recJSON))
			case "/api/workflows/cheapest-agent":
				_, _ = w.Write([]byte(recJSON))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})
		m = runLine(t, m, "/agents metrics")
		out := transcript(m)
		if !strings.Contains(out, "error:") {
			t.Errorf("expected error when metrics endpoint fails; got:\n%s", out)
		}
	})

	// Regression: best-agent fails → partial render (nil best recommendation).
	t.Run("best-agent failure renders gracefully", func(t *testing.T) {
		m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/workflows/metrics":
				_, _ = w.Write([]byte(metricsJSON))
			case "/api/workflows/best-agent":
				w.WriteHeader(http.StatusInternalServerError)
			case "/api/workflows/cheapest-agent":
				_, _ = w.Write([]byte(recJSON))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})
		m = runLine(t, m, "/agents metrics")
		out := transcript(m)
		if strings.Contains(out, "error:") {
			t.Errorf("best-agent failure must not surface as an error; got:\n%s", out)
		}
		if !strings.Contains(out, "AGENT") {
			t.Errorf("expected metrics table even with nil best; got:\n%s", out)
		}
	})

	// Regression: cheapest-agent fails → partial render (nil cheapest recommendation).
	t.Run("cheapest-agent failure renders gracefully", func(t *testing.T) {
		m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/workflows/metrics":
				_, _ = w.Write([]byte(metricsJSON))
			case "/api/workflows/best-agent":
				_, _ = w.Write([]byte(recJSON))
			case "/api/workflows/cheapest-agent":
				w.WriteHeader(http.StatusInternalServerError)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})
		m = runLine(t, m, "/agents metrics")
		out := transcript(m)
		if strings.Contains(out, "error:") {
			t.Errorf("cheapest-agent failure must not surface as an error; got:\n%s", out)
		}
		if !strings.Contains(out, "AGENT") {
			t.Errorf("expected metrics table even with nil cheapest; got:\n%s", out)
		}
	})
}

// TestDestructiveEmptyRefEntersSelectorMode verifies that in the TUI a
// destructive command with no target argument no longer errors: with an empty
// backend list it shows the empty-state hint, and it never sets a
// pendingConfirmation or performs the destructive call.
func TestDestructiveEmptyRefEntersSelectorMode(t *testing.T) {
	cases := []struct {
		name     string
		cmd      string
		wantHint string
	}{
		{"tasks_delete", "/tasks delete", "no tasks yet"},
		{"alerts_delete", "/alerts delete", "no pending alerts"},
		{"automations_delete", "/automations delete", "no automations"},
		{"skills_delete", "/skills delete", "no skills yet"},
		{"agents_delete", "/agents delete", "no agent definitions"},
		{"models_delete", "/models delete", "no models configured"},
		{"schedule_delete", "/schedule delete", "nothing scheduled"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, nil)
			m = runLine(t, m, tc.cmd)
			out := transcript(m)
			if !strings.Contains(out, tc.wantHint) {
				t.Errorf("expected empty-state hint %q, got:\n%s", tc.wantHint, out)
			}
			if strings.Contains(strings.ToLower(out), "usage") {
				t.Errorf("must not show a usage error in TUI mode:\n%s", out)
			}
			if m.pendingConfirmation != nil {
				t.Error("pendingConfirmation must NOT be set when ref is empty")
			}
			for _, method := range []string{"DELETE", "POST", "PUT", "PATCH"} {
				rec.mu.Lock()
				for _, c := range rec.calls {
					if strings.HasPrefix(c, method+" ") {
						t.Errorf("no mutating call expected, saw %s", c)
					}
				}
				rec.mu.Unlock()
			}
		})
	}
}

// TestDestructiveEmptyRefCLIStillShowsUsage verifies that headless (CLI) mode
// keeps the original usage error when the ref is missing — there is no
// interactive selector to fall back to.
func TestDestructiveEmptyRefCLIStillShowsUsage(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
	}{
		{"tasks_delete", "/tasks delete"},
		{"alerts_delete", "/alerts delete"},
		{"automations_delete", "/automations delete"},
		{"skills_delete", "/skills delete"},
		{"agents_delete", "/agents delete"},
		{"models_delete", "/models delete"},
		{"schedule_delete", "/schedule delete"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cliMode = true
			defer func() { cliMode = false }()
			m, rec := dispatchModel(t, nil)
			m = runLine(t, m, tc.cmd)
			if out := transcript(m); !strings.Contains(strings.ToLower(out), "usage") {
				t.Errorf("expected usage error in CLI mode, got:\n%s", out)
			}
			rec.mu.Lock()
			nCalls := len(rec.calls)
			rec.mu.Unlock()
			if nCalls != 0 {
				t.Errorf("expected zero backend calls, got %d:\n%s", nCalls, rec.all())
			}
		})
	}
}

// TestSkillsEditResolvesHandleViaMatchRef verifies that /skills edit resolves
// the skill reference through matchRef (exact → prefix → substring) before
// calling UpdateSkill with the canonical handle.
func TestSkillsEditResolvesHandleViaMatchRef(t *testing.T) {
	const skillsHTML = `
<div data-skill-handle="retry-logic" data-skill-name="Retry Logic"
     data-skill-enabled="true" data-skill-always-use="false" data-skill-scope="project"></div>
<div data-skill-handle="rate-limiter" data-skill-name="Rate Limiter"
     data-skill-enabled="true" data-skill-always-use="false" data-skill-scope="project"></div>`

	t.Run("prefix_match", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/skills": skillsHTML})
		m = runLine(t, m, "/skills edit retry | Always retry on 429")
		if !rec.saw("PUT", "/skills/retry-logic") {
			t.Errorf("expected PUT /skills/retry-logic via prefix match; calls:\n%s", rec.all())
		}
		if out := transcript(m); !strings.Contains(out, "updated skill retry-logic") {
			t.Errorf("expected confirmation message; transcript:\n%s", out)
		}
	})

	t.Run("exact_match", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/skills": skillsHTML})
		m = runLine(t, m, "/skills edit retry-logic | new body")
		if !rec.saw("PUT", "/skills/retry-logic") {
			t.Errorf("expected PUT /skills/retry-logic via exact match; calls:\n%s", rec.all())
		}
	})

	t.Run("ambiguous_rejection", func(t *testing.T) {
		// "r" matches both retry-logic and rate-limiter — should produce an error.
		m, rec := dispatchModel(t, map[string]string{"/skills": skillsHTML})
		m = runLine(t, m, "/skills edit r | new body")
		if rec.saw("PUT", "/skills/retry-logic") || rec.saw("PUT", "/skills/rate-limiter") {
			t.Errorf("ambiguous ref must not call UpdateSkill; calls:\n%s", rec.all())
		}
		if out := transcript(m); !strings.Contains(out, "ambiguous") {
			t.Errorf("expected ambiguity error; transcript:\n%s", out)
		}
	})

	t.Run("unknown_ref", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/skills": skillsHTML})
		m = runLine(t, m, "/skills edit nonexistent | new body")
		if rec.saw("PUT", "/skills/nonexistent") {
			t.Errorf("unknown ref must not call UpdateSkill; calls:\n%s", rec.all())
		}
		if out := transcript(m); !strings.Contains(out, "nothing matches") {
			t.Errorf("expected nothing-matches error; transcript:\n%s", out)
		}
	})
}

// TestDestructiveNonEmptyRefStillConfirms verifies that supplying a non-empty
// ref to a destructive command still sets pendingConfirmation (i.e. the
// confirmation prompt is shown as before the fix).
func TestDestructiveNonEmptyRefStillConfirms(t *testing.T) {
	const agentsHTML = `<div data-agent-id="ag-1" data-agent-key="reviewer"
		data-agent-name="Reviewer" data-agent-description="reviews code"
		data-agent-model="claude" data-agent-scope="project"></div>`
	const skillsHTML = `<div data-skill-handle="my-skill" data-skill-name="My Skill"
		data-skill-enabled="true" data-skill-always-use="false" data-skill-scope="project"></div>`

	t.Run("tasks_delete_with_ref", func(t *testing.T) {
		m, _ := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
		m = runLine(t, m, "/tasks delete t-1")
		if m.pendingConfirmation == nil {
			t.Error("pendingConfirmation must be set for /tasks delete <ref>")
		}
	})

	t.Run("agents_delete_with_ref", func(t *testing.T) {
		m, _ := dispatchModel(t, map[string]string{"/agents": agentsHTML})
		m = runLine(t, m, "/agents delete Reviewer")
		if m.pendingConfirmation == nil {
			t.Error("pendingConfirmation must be set for /agents delete <ref>")
		}
	})

	t.Run("skills_delete_with_ref", func(t *testing.T) {
		m, _ := dispatchModel(t, map[string]string{"/skills": skillsHTML})
		m = runLine(t, m, "/skills delete my-skill")
		if m.pendingConfirmation == nil {
			t.Error("pendingConfirmation must be set for /skills delete <ref>")
		}
	})
}

func TestStatusCommandShowsAlertAndTaskCounts(t *testing.T) {
	// An alert with a "pending" badge.
	const alertsHTML = `<div data-alert-id="a-1" data-alert-scroll-anchor="a-1">
	  <p class="font-semibold">Needs approval</p>
	  <span class="badge">pending</span>
	</div>`
	// Two active tasks: one running, one queued.
	const tasksHTML = `<div>
	  <div class="card" data-task-id="t-1" data-task-status="running" data-task-category="active" data-display-order="0">
	    <div class="card-body"><a href="/tasks/t-1" title="Task A">Task A</a></div>
	  </div>
	  <div class="card" data-task-id="t-2" data-task-status="queued" data-task-category="active" data-display-order="1">
	    <div class="card-body"><a href="/tasks/t-2" title="Task B">Task B</a></div>
	  </div>
	</div>`

	m, rec := dispatchModel(t, map[string]string{
		"/alerts": alertsHTML,
		"/tasks":  tasksHTML,
	})

	// First /status call triggers fetchStatusCounts and processes the result
	// (runLine follows one level of chaining, so statusCountsMsg is applied).
	m = runLine(t, m, "/status")

	// Verify the backend was called for both resources.
	if !rec.saw("GET", "/alerts") {
		t.Errorf("expected GET /alerts during status counts fetch:\n%s", rec.all())
	}
	if !rec.saw("GET", "/tasks") {
		t.Errorf("expected GET /tasks during status counts fetch:\n%s", rec.all())
	}

	// Verify model fields were populated.
	if m.pendingAlertCount != 1 {
		t.Errorf("pendingAlertCount = %d, want 1", m.pendingAlertCount)
	}
	if m.activeTaskCount != 2 {
		t.Errorf("activeTaskCount = %d, want 2", m.activeTaskCount)
	}
	if m.queuedTaskCount != 1 {
		t.Errorf("queuedTaskCount = %d, want 1", m.queuedTaskCount)
	}

	// Second /status call renders with the populated counts.
	m = runLine(t, m, "/status")
	tx := transcript(m)

	if !strings.Contains(tx, "pending approvals") {
		t.Errorf("status missing pending approvals row:\n%s", tx)
	}
	if !strings.Contains(tx, "active") {
		t.Errorf("status missing active tasks row:\n%s", tx)
	}
}

func TestStatusCommandSkipsCountFetchWithNoProject(t *testing.T) {
	m, rec := dispatchModel(t, nil)
	m.selectedID = "" // clear project selection
	m.selectedName = ""

	m = runLine(t, m, "/status")

	// With no project selected, fetchStatusCounts must not hit alerts or tasks.
	if rec.saw("GET", "/alerts") {
		t.Error("should not fetch /alerts when no project is selected")
	}
	if rec.saw("GET", "/tasks") {
		t.Error("should not fetch /tasks when no project is selected")
	}
}

const taskReviewHTML = `<div id="review-comments-list" data-task-id="t-1" data-comment-count="1">
	<div class="review-comment-item flex" data-comment-id="rc-1" data-file-path="internal/client/tasks.go" data-line-number="42" data-line-type="new" data-state="open">
		<div><div><span>alice</span><span>·</span><span>internal/client/tasks.go:42</span></div><p>Needs error handling</p></div>
	</div>
</div>`

func TestTasksShowReviewTabListsInlineComments(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{
		"/tasks":             taskBoardHTML,
		"/tasks/t-1/reviews": taskReviewHTML,
	})
	m = runLine(t, m, "/tasks show Refactor the API review")

	if !rec.saw("GET", "/tasks/t-1/reviews") {
		t.Fatalf("expected review fetch, calls:\n%s", rec.all())
	}
	out := transcript(m)
	for _, want := range []string{"internal/client/tasks.go", "42 new", "Needs error handling", "open"} {
		if !strings.Contains(out, want) {
			t.Errorf("review output missing %q:\n%s", want, out)
		}
	}
}

func taskReviewsResultText(m Model) string {
	for i := len(m.log) - 1; i >= 0; i-- {
		if m.log[i].role == "result" {
			return m.log[i].text
		}
	}
	return ""
}

func taskReviewErrorText(m Model) string {
	for i := len(m.log) - 1; i >= 0; i-- {
		if m.log[i].role == "error" {
			return m.log[i].text
		}
	}
	return ""
}

func TestTaskReviewReadPathsHaveEquivalentPlainOutputAndSingleFetch(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{name: "show review tab", line: "/tasks show Refactor the API review"},
		{name: "reviews default list", line: "/tasks reviews Refactor the API"},
		{name: "reviews list subcommand", line: "/tasks reviews list Refactor the API"},
	}

	var want string
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{
				"/tasks":             taskBoardHTML,
				"/tasks/t-1/reviews": taskReviewHTML,
			})
			m = runLine(t, m, tc.line)

			if got := rec.count("GET", "/tasks"); got != 1 {
				t.Fatalf("task resolution should make exactly one board request, got %d:\n%s", got, rec.all())
			}
			if got := rec.count("GET", "/tasks/t-1/reviews"); got != 1 {
				t.Fatalf("review display should make exactly one review request, got %d:\n%s", got, rec.all())
			}
			got := taskReviewsResultText(m)
			if got == "" {
				t.Fatalf("missing task review result:\n%s", transcript(m))
			}
			if want == "" {
				want = got
			} else if got != want {
				t.Errorf("review output differs from the first read path:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func TestTaskReviewReadPathsPreserveEmptyPlainOutput(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{name: "show review tab", line: "/tasks show t-1 review"},
		{name: "reviews default list", line: "/tasks reviews t-1"},
		{name: "reviews list subcommand", line: "/tasks reviews list t-1"},
	}
	const emptyReviewHTML = `<div id="review-comments-list" data-task-id="t-1" data-comment-count="0"></div>`
	const emptyMessage = "no review comments yet — /tasks reviews add <task> <file>:<line> <comment>"

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{
				"/tasks":             taskBoardHTML,
				"/tasks/t-1/reviews": emptyReviewHTML,
			})
			m = runLine(t, m, tc.line)

			if got := rec.count("GET", "/tasks/t-1/reviews"); got != 1 {
				t.Fatalf("empty review display should make exactly one review request, got %d:\n%s", got, rec.all())
			}
			if out := taskReviewsResultText(m); !strings.Contains(out, emptyMessage) {
				t.Fatalf("empty-state message changed or was missing:\n%s", transcript(m))
			}
		})
	}
}

func dispatchModelWithReviewError(t *testing.T) (Model, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.recordURL(r.Method, r.URL.RequestURI())
		switch r.URL.Path {
		case "/tasks":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(taskBoardHTML))
		case "/tasks/t-1/reviews":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"review fetch failed"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.selectedID = "p1"
	m.selectedName = "demo"
	return m, rec
}

func TestTaskReviewReadPathsPropagateFetchErrorsIdentically(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{name: "show review tab", line: "/tasks show t-1 review"},
		{name: "reviews default list", line: "/tasks reviews t-1"},
		{name: "reviews list subcommand", line: "/tasks reviews list t-1"},
	}
	const wantError = "server error (502): review fetch failed"

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModelWithReviewError(t)
			m = runLine(t, m, tc.line)

			if got := rec.count("GET", "/tasks/t-1/reviews"); got != 1 {
				t.Fatalf("review error path should make exactly one review request, got %d:\n%s", got, rec.all())
			}
			if got := taskReviewErrorText(m); got != wantError {
				t.Fatalf("review error = %q, want %q; transcript:\n%s", got, wantError, transcript(m))
			}
		})
	}
}

func TestTasksReviewsListGracefullyHandlesEmptyComments(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{
		"/tasks":             taskBoardHTML,
		"/tasks/t-1/reviews": `<div id="review-comments-list" data-task-id="t-1" data-comment-count="0"></div>`,
	})
	m = runLine(t, m, "/tasks reviews t-1")

	if !rec.saw("GET", "/tasks/t-1/reviews") {
		t.Fatalf("expected review fetch, calls:\n%s", rec.all())
	}
	if out := transcript(m); !strings.Contains(out, "no review comments yet") {
		t.Errorf("expected empty-state message, got:\n%s", out)
	}
}

func TestTasksReviewsAddPostsInlineComment(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{
		"/tasks":             taskBoardHTML,
		"/tasks/t-1/reviews": taskReviewHTML,
	})
	m = runLine(t, m, "/tasks reviews add Refactor the API internal/client/tasks.go:42 Needs error handling")

	if !rec.saw("POST", "/tasks/t-1/reviews") {
		t.Fatalf("expected add review call, calls:\n%s", rec.all())
	}
	for _, want := range []string{"file_path=internal%2Fclient%2Ftasks.go", "line_number=42", "line_type=new", "comment_text=Needs+error+handling"} {
		if !rec.sawForm(want) {
			t.Errorf("posted form missing %q, forms: %v", want, rec.forms)
		}
	}
	out := transcript(m)
	if !strings.Contains(out, "added review comment") || !strings.Contains(out, "Needs error handling") {
		t.Errorf("expected success confirmation and rendered comments, got:\n%s", out)
	}
}

func TestTasksReviewsAddPickerPrefillsAndSubmitsOneComment(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{
		"/tasks":             selTasksHTML,
		"/tasks/t-1/reviews": taskReviewHTML,
	})
	m = runLine(t, m, "/tasks reviews add")
	if !m.selectorActive {
		t.Fatalf("expected task selector, transcript:\n%s", transcript(m))
	}

	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got, want := m.input.Value(), "/tasks reviews add t-1 "; got != want {
		t.Fatalf("prefilled input = %q, want %q", got, want)
	}
	if got := rec.count("GET", "/tasks"); got != 1 {
		t.Fatalf("picker selection must reuse its one task-list lookup, got %d requests; calls:\n%s", got, rec.all())
	}
	if got := rec.count("POST", "/tasks/t-1/reviews"); got != 0 {
		t.Fatalf("picker selection must not submit before operands, got %d POSTs", got)
	}

	m = runLine(t, m, "internal/client/tasks.go:42 Needs error handling")
	if got := rec.count("GET", "/tasks"); got != 1 {
		t.Fatalf("picker submission must not resolve the selected task again, got %d task-list requests; calls:\n%s", got, rec.all())
	}
	if got := rec.count("POST", "/tasks/t-1/reviews"); got != 1 {
		t.Fatalf("expected exactly one review submission, got %d; calls:\n%s", got, rec.all())
	}
	for _, want := range []string{"file_path=internal%2Fclient%2Ftasks.go", "line_number=42", "line_type=new", "comment_text=Needs+error+handling"} {
		if !rec.sawForm(want) {
			t.Errorf("picker submission missing %q, forms: %v", want, rec.forms)
		}
	}
	out := transcript(m)
	if strings.Contains(strings.ToLower(out), "usage") || !strings.Contains(out, "added review comment") || !strings.Contains(out, "Needs error handling") {
		t.Fatalf("picker submission should render refreshed review output without usage error:\n%s", out)
	}
}

func TestTasksReviewsAddIncompleteOperandsKeepUsageError(t *testing.T) {
	for _, line := range []string{
		"/tasks reviews add t-1",
		"/tasks reviews add t-1 internal/client/tasks.go:42",
		"/tasks reviews add t-1 not-a-location Needs error handling",
	} {
		line := line
		t.Run(line, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{
				"/tasks":             taskBoardHTML,
				"/tasks/t-1/reviews": taskReviewHTML,
			})
			m = runLine(t, m, line)
			if m.selectorActive {
				t.Fatalf("incomplete review command must not open selector:\n%s", transcript(m))
			}
			if !strings.Contains(strings.ToLower(transcript(m)), "usage") {
				t.Fatalf("expected usage error for %q:\n%s", line, transcript(m))
			}
			if got := rec.count("POST", "/tasks/t-1/reviews"); got != 0 {
				t.Fatalf("incomplete review command must not mutate, got %d POSTs", got)
			}
		})
	}
}

func TestTasksReviewsAddQuotedTaskTitlePostsOneComment(t *testing.T) {
	board := strings.Replace(taskBoardHTML, "Refactor the API", "Fix login bug", 1)
	m, rec := dispatchModel(t, map[string]string{
		"/tasks":             board,
		"/tasks/t-1/reviews": taskReviewHTML,
	})
	m = runLine(t, m, `/tasks reviews add "Fix login bug" internal/auth.go:42 Handle token refresh errors`)

	if got := rec.count("POST", "/tasks/t-1/reviews"); got != 1 {
		t.Fatalf("expected exactly one add review call, got %d; calls:\n%s", got, rec.all())
	}
	if !rec.sawForm("file_path=internal%2Fauth.go") || !rec.sawForm("line_number=42") || !rec.sawForm("comment_text=Handle+token+refresh+errors") {
		t.Fatalf("quoted review form was not submitted as intended: %v", rec.forms)
	}
	out := transcript(m)
	if strings.Contains(out, "nothing matches") || !strings.Contains(out, "added review comment") || !strings.Contains(out, "Fix login bug") {
		t.Fatalf("quoted task title should resolve and render success:\n%s", out)
	}
}

func TestInteractiveUnmatchedQuoteReportsParseError(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
	m = runLine(t, m, `/tasks run "Refactor the API`)

	out := strings.ToLower(transcript(m))
	if !strings.Contains(out, "parse error") || !strings.Contains(out, "unmatched") || !strings.Contains(out, "quote") {
		t.Fatalf("expected a clear unmatched-quote parse error:\n%s", transcript(m))
	}
	if strings.Contains(out, "nothing matches") || rec.count("POST", "/tasks/t-1/run") != 0 {
		t.Fatalf("malformed input must not reach task lookup or mutation:\n%s\ncalls:\n%s", transcript(m), rec.all())
	}
}
