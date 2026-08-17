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
	m = runLine(t, m, line) // step 1: sets pendingConfirmation
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

func TestAutomationsListIsUnchangedWithNoArguments(t *testing.T) {
	const automationsHTML = `<div>Native SDLC automation</div>`
	m, rec := dispatchModel(t, map[string]string{"/automations": automationsHTML})
	m = runLine(t, m, "/automations")
	if !rec.saw("GET", "/automations") {
		t.Fatalf("expected an automations fetch:\n%s", rec.all())
	}
	if !strings.Contains(transcript(m), "Native SDLC automation") {
		t.Errorf("automations content missing:\n%s", transcript(m))
	}
	if strings.Contains(transcript(m), "error:") {
		t.Errorf("/automations should not error:\n%s", transcript(m))
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
		if strings.Contains(transcript(m), "error:") {
			t.Errorf("unexpected error:\n%s", transcript(m))
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
			m, rec := dispatchModel(t, nil)
			m = runLine(t, m, "/channels "+tc.action+" "+tc.channelName)
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

			m = runLine(t, m, "/channels "+action+" telegram")
			out := transcript(m)
			if !strings.Contains(out, "error:") {
				t.Errorf("expected a backend error for %s:\n%s", action, out)
			}
		})
	}
}

// TestChannelsMissingArgError verifies that test/remove with no channel name
// return a clear usage error without posting to the backend.
func TestChannelsMissingArgError(t *testing.T) {
	for _, action := range []string{"test", "remove"} {
		action := action
		t.Run(action, func(t *testing.T) {
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
