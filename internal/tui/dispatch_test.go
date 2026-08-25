package tui

import (
	"net/http"
	"net/http/httptest"
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
		rec.record(r.Method, r.URL.Path)
		_ = r.ParseForm()
		rec.mu.Lock()
		rec.forms = append(rec.forms, r.Method+" "+r.URL.Path+"?"+r.PostForm.Encode())
		rec.mu.Unlock()
		if body, ok := bodies[r.URL.Path]; ok {
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.Method, r.URL.Path)
		if r.Method != http.MethodPost || r.URL.Path != "/projects" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			return
		}
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

	m = runLine(t, m, `/projects create My Project | C:\Users\me\repo`)
	if !rec.saw("POST", "/projects") {
		t.Fatalf("expected project creation request, calls:\n%s", rec.all())
	}
	if m.selectedID != "created-project" || m.selectedName != "My Project" {
		t.Fatalf("created project was not selected: id=%q name=%q", m.selectedID, m.selectedName)
	}
	if !strings.Contains(transcript(m), "active project selected") || !strings.Contains(transcript(m), "My Project") {
		t.Fatalf("creation output did not explain selection:\n%s", transcript(m))
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
		{"/schedule add Refactor 2026-09-01T10:00 seconds", "repeat_type=seconds", "repeat_interval=1"},
		{"/schedule add Refactor 2026-09-01T10:00 seconds 1", "repeat_type=seconds", "repeat_interval=1"},
		{"/schedule add Refactor 2026-09-01T10:00 seconds 30", "repeat_type=seconds", "repeat_interval=30"},
		{"/schedule add Refactor 2026-09-01T10:00 minutes", "repeat_type=minutes", "repeat_interval=1"},
		{"/schedule add Refactor 2026-09-01T10:00 minutes 15", "repeat_type=minutes", "repeat_interval=15"},
		{"/schedule add Refactor 2026-09-01T10:00 hours", "repeat_type=hours", "repeat_interval=1"},
		{"/schedule add Refactor 2026-09-01T10:00 hours 4", "repeat_type=hours", "repeat_interval=4"},
		{"/schedule add Refactor 2026-09-01T10:00 hourly", "repeat_type=hours", "repeat_interval=1"},
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
	m, rec := dispatchModel(t, nil)
	runLine(t, m, "/personality set concise")
	if !rec.saw("POST", "/personality/save") {
		t.Errorf("calls:\n%s", rec.all())
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

func TestAgentsGenerateDelete(t *testing.T) {
	const agentsHTML = `<div data-agent-id="ag-1" data-agent-key="reviewer" data-agent-name="Reviewer"
		data-agent-description="reviews code" data-agent-model="claude" data-agent-scope="project"></div>`

	t.Run("generate", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/agents": agentsHTML})
		m = runLine(t, m, "/agents generate a reviewer agent")
		if !rec.saw("POST", "/agents/generate") {
			t.Errorf("calls:\n%s", rec.all())
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
