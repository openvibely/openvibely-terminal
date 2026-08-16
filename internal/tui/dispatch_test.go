package tui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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
		runLine(t, m, "/tasks delete t-1")
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

	runLine(t, m, "/alerts delete a-1")
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
		runLine(t, m, "/skills delete deploy")
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
		{"/grades", "POST", "/history/grade-ideas"},
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

func TestWorkersProjectLimit(t *testing.T) {
	m, rec := dispatchModel(t, nil)
	runLine(t, m, "/workers project 3")
	if !rec.saw("POST", "/workers/projects/p1/limit") {
		t.Errorf("calls:\n%s", rec.all())
	}
}

func TestScheduleAddResolvesTask(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
	runLine(t, m, "/schedule add Refactor 2026-09-01T10:00 daily")
	if !rec.saw("POST", "/tasks/t-1/schedule") {
		t.Errorf("calls:\n%s", rec.all())
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
