package terminal

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-terminal/internal/client"
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
		if strings.HasPrefix(r.URL.Path, "/channels/") && strings.HasSuffix(r.URL.Path, "/test") {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div class="text-success"><span>Connection successful!</span></div>`))
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

func TestEventsInteractiveDispatchValidatesOperands(t *testing.T) {
	cases := []struct {
		name       string
		line       string
		valid      bool
		wantForOff bool
		wantForOn  bool
	}{
		{name: "bare toggles", line: "/events", valid: true, wantForOff: true, wantForOn: false},
		{name: "on enables", line: "/events on", valid: true, wantForOff: true, wantForOn: true},
		{name: "off disables", line: "/events off", valid: true, wantForOff: false, wantForOn: false},
		{name: "true enables", line: "/events true", valid: true, wantForOff: true, wantForOn: true},
		{name: "false disables", line: "/events false", valid: true, wantForOff: false, wantForOn: false},
		{name: "unknown mode", line: "/events typo"},
		{name: "extra operand", line: "/events off extra"},
		{name: "extra operand after unknown", line: "/events typo extra"},
	}

	for _, initial := range []bool{false, true} {
		for _, tc := range cases {
			initial, tc := initial, tc
			t.Run(fmt.Sprintf("initial_%t/%s", initial, tc.name), func(t *testing.T) {
				m, rec := dispatchModel(t, nil)
				m.showEvents = initial
				m.busy = true
				sseCanceled := false
				m.sseCancel = func() { sseCanceled = true }
				beforeLogLen := len(m.log)
				beforeGeneration := m.sseGeneration

				m, cmd := typeLine(t, m, tc.line)
				if tc.valid {
					if cmd != nil {
						t.Fatal("valid interactive /events should not return a command")
					}
				} else {
					if cmd == nil {
						t.Fatal("invalid interactive /events should return a usage message command")
					}
					if !m.busy {
						t.Fatal("invalid command changed busy state during dispatch")
					}
					next, followup := m.Update(cmd())
					m = next.(Model)
					if followup != nil {
						t.Fatal("invalid interactive /events returned an unexpected follow-up command")
					}
				}
				if got := rec.all(); got != "" {
					t.Fatalf("interactive /events made backend requests:\n%s", got)
				}
				if m.sseGeneration != beforeGeneration || m.sseCancel == nil || sseCanceled {
					t.Fatal("interactive /events changed SSE lifecycle state")
				}

				out := stripANSI(transcript(m))
				if !tc.valid {
					if m.showEvents != initial {
						t.Fatalf("showEvents = %t, want unchanged %t", m.showEvents, initial)
					}
					if !strings.Contains(out, "usage: /events [on|off]") {
						t.Fatalf("canonical usage missing:\n%s", out)
					}
					for _, success := range []string{"live events on", "live events off"} {
						if strings.Contains(out, success) {
							t.Fatalf("invalid command reported success %q:\n%s", success, out)
						}
					}
					return
				}

				want := tc.wantForOff
				if initial {
					want = tc.wantForOn
				}
				if m.showEvents != want {
					t.Fatalf("showEvents = %t, want %t", m.showEvents, want)
				}
				if len(m.log) != beforeLogLen+2 {
					t.Fatalf("valid command appended %d entries, want command and result", len(m.log)-beforeLogLen)
				}
				wantText := "live events off"
				if want {
					wantText = "live events on"
				}
				if !strings.Contains(out, wantText) || strings.Contains(out, "usage: /events") {
					t.Fatalf("valid command output mismatch:\n%s", out)
				}
			})
		}
	}
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

// runLineWithFollowUp executes the one additional command that a resolver
// returns after feeding its target message back through the model.
func runLineWithFollowUp(t *testing.T, m Model, line string) Model {
	t.Helper()
	m, cmd := typeLine(t, m, line)
	if cmd == nil {
		return m
	}
	msg := cmd()
	if msg == nil {
		return m
	}
	next, followUp := m.Update(msg)
	m = next.(Model)
	if followUp == nil {
		return m
	}
	result := followUp()
	if result == nil {
		return m
	}
	next, _ = m.Update(result)
	return next.(Model)
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
			w.Header().Set("Location", "/tasks?project_id=created-project")
			w.WriteHeader(http.StatusFound)
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

func canonicalTaskDetailHTMLWithModel(taskID, projectID, prompt, modelOptions string) string {
	return `<div id="task-detail-content" class="h-full flex flex-col">
		<span class="hidden" aria-hidden="true" data-openvibely-page-title="Exact task - OpenVibely"></span>
		<a id="task-back-btn" data-project-id="` + projectID + `">Tasks</a>
		<div data-breadcrumb-selector data-searchable-selector data-searchable-selector-kind="Task">
			<button id="task-resource-selector-button" data-breadcrumb-selector-button><span>Exact task</span></button>
		</div>
		<div id="tab-details">
			<div id="task-detail-view">
				<div id="task-detail-metrics" data-task-status="running">
					<div><span>Category:</span><span class="badge">active</span></div>
					<div><span>Status:</span><span class="badge">Running</span></div>
					<div><span>Tag:</span><span class="badge">Bug</span></div>
					<div><span>Priority:</span><span class="badge">High</span></div>
					<div><span>Model:</span><span class="badge">Claude Sonnet</span></div>
					<div><span>Agent:</span><span class="badge">Planner</span></div>
				</div>
				<div class="card"><h3>Swarm Overview</h3></div>
				<div id="task-prompt-panel"><div>Prompt</div><div class="textarea">` + prompt + `</div></div>
				<div id="task-goal-panel" data-task-id="` + taskID + `"><div>Goal</div><span class="badge">Active</span></div>
			</div>
			<form id="edit-task-form-` + taskID + `">
				<input name="title" value="Exact task"><select name="category"><option value="active" selected>active</option></select>
				<select name="priority"><option value="3" selected>High</option></select>
				<select name="tag"><option value="bug" selected>Bug</option></select>
				<textarea name="prompt">` + prompt + `</textarea>
				<select name="agent_id">` + modelOptions + `</select>
				<select name="agent_definition_id"><option value="">No Agent</option><option value="planner" selected>Planner</option></select>
			</form>
			<form><input type="checkbox" name="chain_enabled" checked></form>
		</div>
		<div id="tab-chat"></div><div id="tab-changes"></div><div id="tab-lifecycle">life loaded</div>
	</div>`
}

func canonicalTaskDetailHTML(taskID, projectID string) string {
	return canonicalTaskDetailHTMLWithModel(taskID, projectID, "Implement exact lookup", `<option value="">Use Default Model</option><option value="model-1" selected>Claude Sonnet (Default)</option>`)
}

func TestTasksShowCanonicalFullIDBypassesBoard(t *testing.T) {
	const taskID = "0123456789abcdef0123456789abcdef"
	m, rec := dispatchModel(t, map[string]string{
		"/tasks/" + taskID:              canonicalTaskDetailHTML(taskID, "p1"),
		"/tasks/" + taskID + "/thread":  `<div>thread loaded</div>`,
		"/tasks/" + taskID + "/changes": `<div>changes loaded</div>`,
	})
	m = runLine(t, m, "/tasks show "+taskID+" changes")

	if got := rec.count("GET", "/tasks"); got != 0 {
		t.Fatalf("board requests = %d, want 0; calls:\n%s", got, rec.all())
	}
	if got := rec.count("GET", "/tasks/"+taskID); got != 1 {
		t.Fatalf("detail requests = %d, want 1; calls:\n%s", got, rec.all())
	}
	if got := rec.count("GET", "/api/tasks/"+taskID+"/swarm"); got != 0 {
		t.Fatalf("global metadata requests = %d, want 0; calls:\n%s", got, rec.all())
	}
	for _, path := range []string{"/tasks/" + taskID, "/tasks/" + taskID + "/thread", "/tasks/" + taskID + "/changes"} {
		if !rec.sawQuery("GET " + path + "?project_id=p1") {
			t.Errorf("request %s was not project scoped: %v", path, rec.urlsSnapshot())
		}
	}
	out := stripANSI(transcript(m))
	for _, want := range []string{"Exact task", "changes loaded"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "error:") {
		t.Fatalf("unexpected error:\n%s", out)
	}
}

func TestTasksShowCanonicalFullIDJSONAndReviewBypassBoard(t *testing.T) {
	const taskID = "0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name string
		line string
		json bool
		want []string
	}{
		{
			name: "json", line: "/tasks show " + taskID, json: true,
			want: []string{
				`"id":"` + taskID + `"`, `"project_id":"p1"`, `"title":"Exact task"`,
				`"prompt":"Implement exact lookup"`, `"category":"active"`, `"status":"running"`,
				`"display_order":0`, `"badges":["Chain","Goal","Swarm","Claude Sonnet","Planner","Bug","High"]`,
			},
		},
		{name: "review", line: "/tasks show " + taskID + " review", want: []string{"Exact task", "no review comments"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{
				"/tasks/" + taskID:              canonicalTaskDetailHTML(taskID, "p1"),
				"/tasks/" + taskID + "/reviews": `<div></div>`,
			})
			previousJSON := jsonMode
			jsonMode = tc.json
			defer func() { jsonMode = previousJSON }()
			m = runLine(t, m, tc.line)
			if rec.count("GET", "/tasks") != 0 || rec.count("GET", "/api/tasks/"+taskID+"/swarm") != 0 || rec.count("GET", "/tasks/"+taskID+"/thread") != 0 || rec.count("GET", "/tasks/"+taskID+"/changes") != 0 {
				t.Fatalf("metadata-only show made board, global metadata, or lazy requests:\n%s", rec.all())
			}
			if tc.name == "review" && !rec.sawQuery("GET /tasks/"+taskID+"/reviews?project_id=p1") {
				t.Fatalf("review request was not project scoped: %v", rec.urlsSnapshot())
			}
			out := stripANSI(transcript(m))
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q:\n%s", want, transcript(m))
				}
			}
		})
	}
}

func TestTasksShowCanonicalFullIDModelBadgesMatchBoard(t *testing.T) {
	const taskID = "0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name         string
		modelOptions string
		wantBadge    string
		unwanted     string
	}{
		{
			name:         "default model uses configured default name",
			modelOptions: `<option value="" selected>Use Default Model</option><option value="model-1">Claude Sonnet (Default)</option>`,
			wantBadge:    `"badges":["Chain","Goal","Swarm","Claude Sonnet","Planner","Bug","High"]`,
			unwanted:     "Default model",
		},
		{
			name:         "no configured models has no model badge",
			modelOptions: `<option value="" selected>Use Default Model</option>`,
			wantBadge:    `"badges":["Chain","Goal","Swarm","Planner","Bug","High"]`,
			unwanted:     "Claude Sonnet",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{
				"/tasks/" + taskID: canonicalTaskDetailHTMLWithModel(taskID, "p1", "Implement exact lookup", tc.modelOptions),
			})
			previousJSON := jsonMode
			jsonMode = true
			defer func() { jsonMode = previousJSON }()
			m = runLine(t, m, "/tasks show "+taskID)
			out := stripANSI(transcript(m))
			if !strings.Contains(out, tc.wantBadge) || strings.Contains(out, tc.unwanted) {
				t.Fatalf("unexpected model badges:\n%s", out)
			}
			if rec.count("GET", "/tasks") != 0 || rec.count("GET", "/api/tasks/"+taskID+"/swarm") != 0 {
				t.Fatalf("model badge lookup made prohibited requests:\n%s", rec.all())
			}
		})
	}
}

func TestTasksShowCanonicalFullIDJSONTruncatesPromptToBoardPreview(t *testing.T) {
	const taskID = "0123456789abcdef0123456789abcdef"
	prompt := strings.Repeat("界", 300) + "TAIL"
	m, rec := dispatchModel(t, map[string]string{
		"/tasks/" + taskID: canonicalTaskDetailHTMLWithModel(taskID, "p1", prompt, `<option value="" selected>Use Default Model</option>`),
	})
	previousJSON := jsonMode
	jsonMode = true
	defer func() { jsonMode = previousJSON }()
	m = runLine(t, m, "/tasks show "+taskID)
	out := stripANSI(transcript(m))
	if !strings.Contains(out, `"prompt":"`+strings.Repeat("界", 300)+`"`) || strings.Contains(out, "TAIL") {
		t.Fatalf("prompt does not match 300-code-point board preview:\n%s", out)
	}
	if rec.count("GET", "/tasks") != 0 || rec.count("GET", "/api/tasks/"+taskID+"/swarm") != 0 {
		t.Fatalf("prompt lookup made prohibited requests:\n%s", rec.all())
	}
}

func TestTasksShowCanonicalFullIDPreservesAuthFailure(t *testing.T) {
	const taskID = "0123456789abcdef0123456789abcdef"
	var boardRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tasks" {
			boardRequests.Add(1)
		}
		w.Header().Set("Location", "/login?next=%2Ftasks%2F"+taskID)
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	m.selectedID, m.selectedName = "p1", "demo"
	m = runLine(t, m, "/tasks show "+taskID)
	if !m.authRequired || m.connected {
		t.Fatalf("auth state = required %t connected %t", m.authRequired, m.connected)
	}
	if boardRequests.Load() != 0 {
		t.Fatalf("auth failure fell back to board")
	}
	if !strings.Contains(stripANSI(transcript(m)), "requires sign-in") {
		t.Fatalf("auth error missing:\n%s", transcript(m))
	}
}

func TestTasksShowCanonicalFullIDLargeBoardPerformance(t *testing.T) {
	const taskID = "0123456789abcdef0123456789abcdef"
	var board strings.Builder
	board.Grow(600000)
	for i := 0; i < 2500; i++ {
		id := fmt.Sprintf("%032x", i+1)
		title := fmt.Sprintf("Large board task %04d", i)
		if i == 2499 {
			id, title = taskID, "Target large task"
		}
		fmt.Fprintf(&board, `<div data-task-id="%s" data-task-status="pending" data-task-category="backlog"><a href="/tasks/%s" title="%s">%s</a><p class="line-clamp-2">prompt</p></div>`, id, id, title, title)
	}

	var boardRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tasks":
			boardRequests.Add(1)
			time.Sleep(20 * time.Millisecond)
			_, _ = io.WriteString(w, board.String())
		case "/tasks/" + taskID:
			_, _ = io.WriteString(w, canonicalTaskDetailHTML(taskID, "p1"))
		case "/tasks/" + taskID + "/thread":
			_, _ = io.WriteString(w, `<div>thread</div>`)
		case "/tasks/" + taskID + "/changes":
			_, _ = io.WriteString(w, `<div>changes</div>`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	runShow := func(ref string) {
		m := New(c)
		m.selectedID, m.selectedName = "p1", "demo"
		_ = runLine(t, m, "/tasks show "+ref)
	}
	measure := func(ref string, runs int) (time.Duration, int32) {
		before := boardRequests.Load()
		start := time.Now()
		for i := 0; i < runs; i++ {
			runShow(ref)
		}
		return time.Since(start), boardRequests.Load() - before
	}

	const runs = 3
	directTime, directBoards := measure(taskID, runs)
	boardTime, fuzzyBoards := measure("Target large task", runs)
	if directBoards != 0 || fuzzyBoards != runs {
		t.Fatalf("board request evidence: direct=%d fuzzy=%d, want 0 and %d", directBoards, fuzzyBoards, runs)
	}
	if directTime*5 > boardTime*4 {
		t.Fatalf("large-board timing improved less than 20%%: direct=%v board=%v", directTime, boardTime)
	}

	directAllocs := testing.AllocsPerRun(3, func() { runShow(taskID) })
	boardAllocs := testing.AllocsPerRun(3, func() { runShow("Target large task") })
	if directAllocs*5 > boardAllocs*4 {
		t.Fatalf("large-board allocations improved less than 20%%: direct=%.0f board=%.0f", directAllocs, boardAllocs)
	}
	t.Logf("large-board evidence: board requests %d -> %d; elapsed %v -> %v; allocations %.0f -> %.0f", fuzzyBoards, directBoards, boardTime, directTime, boardAllocs, directAllocs)
}

func TestTasksShowCanonicalFullIDRejectsForeignMetadataWithoutFallback(t *testing.T) {
	const taskID = "0123456789abcdef0123456789abcdef"
	m, rec := dispatchModel(t, map[string]string{
		"/tasks":           `<div data-task-id="` + taskID + `" data-task-category="active"><a href="/tasks/` + taskID + `" title="Board secret">Board secret</a></div>`,
		"/tasks/" + taskID: `<div data-task-id="` + taskID + `" data-project-id="p2"><h2 class="font-bold">Foreign secret</h2></div>`,
	})
	m = runLine(t, m, "/tasks show "+taskID)

	if rec.count("GET", "/tasks") != 0 || rec.count("GET", "/tasks/"+taskID+"/thread") != 0 || rec.count("GET", "/tasks/"+taskID+"/changes") != 0 {
		t.Fatalf("foreign detail triggered fallback or lazy requests:\n%s", rec.all())
	}
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "not found in selected project") {
		t.Fatalf("missing scoped not-found error:\n%s", out)
	}
	for _, leaked := range []string{"Foreign secret", "Board secret", "p2"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("output leaked %q:\n%s", leaked, out)
		}
	}
}

func TestTasksShowUnknownCanonicalFullIDDoesNotScanBoard(t *testing.T) {
	const taskID = "0123456789abcdef0123456789abcdef"
	var boardRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tasks" {
			boardRequests.Add(1)
			_, _ = io.WriteString(w, `<div>board secret</div>`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"task not found"}`)
	}))
	defer srv.Close()
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	m.selectedID, m.selectedName = "p1", "demo"
	m = runLine(t, m, "/tasks show "+taskID)
	if boardRequests.Load() != 0 {
		t.Fatal("unknown canonical ID fell back to the board")
	}
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "task not found") || strings.Contains(out, "board secret") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

func TestTasksShowNoncanonicalReferencesKeepBoardResolution(t *testing.T) {
	const taskID = "0123456789abcdef0123456789abcdef"
	board := `<div data-task-id="` + taskID + `" data-task-status="running" data-task-category="active"><a href="/tasks/` + taskID + `" title="Exact task">Exact task</a></div>`
	for _, ref := range []string{"Exact task", taskID[:12], strings.ToUpper(taskID)} {
		t.Run(ref, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{
				"/tasks":                        board,
				"/tasks/" + taskID:              canonicalTaskDetailHTML(taskID, "p1"),
				"/tasks/" + taskID + "/thread":  `<div>thread</div>`,
				"/tasks/" + taskID + "/changes": `<div>changes</div>`,
			})
			m = runLine(t, m, "/tasks show "+ref)
			if rec.count("GET", "/tasks") != 1 {
				t.Fatalf("noncanonical ref %q did not retain board matching:\n%s", ref, rec.all())
			}
			if strings.Contains(stripANSI(transcript(m)), "error:") {
				t.Fatalf("noncanonical ref failed:\n%s", transcript(m))
			}
		})
	}
}

func TestTasksRunCanonicalFullIDStillUsesBoard(t *testing.T) {
	const taskID = "0123456789abcdef0123456789abcdef"
	board := `<div data-task-id="` + taskID + `" data-task-status="pending" data-task-category="backlog"><a href="/tasks/` + taskID + `" title="Exact task">Exact task</a></div>`
	m, rec := dispatchModel(t, map[string]string{"/tasks": board})
	m = runLine(t, m, "/tasks run "+taskID)
	if rec.count("GET", "/tasks") != 2 || rec.count("POST", "/tasks/"+taskID+"/run") != 1 {
		t.Fatalf("non-show action changed behavior:\n%s", rec.all())
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

func TestTasksLifecycleRendersOrderedEventsFromCurrentPageResponse(t *testing.T) {
	const executions = `{"items":[{"id":"exec-1","skill_key":"router","when":"post_task","status":"completed","started_at":"2026-01-20T10:00:00Z"}],"has_more":false}`
	const events = `[
		{"id":"event-2","seq":2,"event_type":"completed","payload":{"message":"second"},"created_at":"2026-01-20T10:00:02Z"},
		{"id":"event-1","seq":1,"event_type":"started","payload":{"message":"first"},"created_at":"2026-01-20T10:00:01Z"}
	]`
	m, rec := dispatchModel(t, map[string]string{
		"/tasks":                                  taskBoardHTML,
		"/api/tasks/t-1/lifecycle-executions":     executions,
		"/api/lifecycle-executions/exec-1/events": events,
	})
	m = runLine(t, m, "/tasks lifecycle Refactor the API exec-1")

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

func TestAlertsShowLoadsFullDetailWithoutMutation(t *testing.T) {
	const alertsHTML = `<div class="card" data-alert-id="a-1" data-alert-scroll-anchor="a-1"
		data-search-text="review pending unclaimed" data-alert-type="custom"
		data-alert-severity="warning" data-alert-decision-state="pending"
		data-alert-processing-state="unclaimed">
		<svg class="h-5 w-5 text-warning"></svg>
		<p class="font-semibold">Review request</p>
		<p class="text-sm opacity-60 mt-1">Approval is needed</p>
	</div>`
	const detailHTML = `<div data-alert-detail-loaded>
		<div data-alert-markdown data-raw-content="# Review request&#10;&#10;First line&#10;Second line"></div>
		<pre>{
  "owner": "release team",
  "attempt": 2
}</pre>
	</div>`
	m, rec := dispatchModel(t, map[string]string{
		"/alerts":             alertsHTML,
		"/alerts/a-1/details": detailHTML,
	})

	m = runLine(t, m, "/alerts show a-1")
	out := transcript(m)
	plain := stripANSI(out)
	for _, want := range []string{
		"Review request",
		"id: a-1",
		"type: custom · severity: warning",
		"decision: pending · processing: unclaimed",
		"# Review request\n\nFirst line\nSecond line",
		`"owner": "release team"`,
		`"attempt": 2`,
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("alerts show output missing %q:\n%s", want, plain)
		}
	}
	if !rec.sawQuery("GET /alerts?project_id=p1") || !rec.sawQuery("GET /alerts/a-1/details?project_id=p1") {
		t.Fatalf("alerts show requests were not project-scoped:\n%s", strings.Join(rec.urlsSnapshot(), "\n"))
	}
	if got := rec.count("GET", "/alerts/a-1/details"); got != 1 {
		t.Fatalf("detail requests = %d, want one:\n%s", got, rec.all())
	}
	if rec.count("POST", "/alerts/a-1/approve") != 0 || rec.count("POST", "/alerts/a-1/read") != 0 || rec.count("DELETE", "/alerts/a-1") != 0 {
		t.Fatalf("alerts show made a mutation:\n%s", rec.all())
	}
}

func TestAlertActionsExactTitleBeatsLongerTitlePrefix(t *testing.T) {
	const alertsHTML = `<div data-alert-id="a-deploy" data-alert-scroll-anchor="a-deploy" data-search-text="deploy exact body"><p class="font-semibold">Deploy</p></div>
		<div data-alert-id="a-deploy-service" data-alert-scroll-anchor="a-deploy-service" data-search-text="deploy service body"><p class="font-semibold">Deploy service</p></div>`

	actions := []struct {
		action string
		method string
		path   string
	}{
		{action: "read", method: http.MethodPost, path: "/alerts/a-deploy/read"},
		{action: "approve", method: http.MethodPost, path: "/alerts/a-deploy/approve"},
		{action: "reject", method: http.MethodPost, path: "/alerts/a-deploy/reject"},
		{action: "dismiss", method: http.MethodPost, path: "/alerts/a-deploy/dismiss"},
		{action: "delete", method: http.MethodDelete, path: "/alerts/a-deploy"},
	}
	for _, tc := range actions {
		t.Run(tc.action, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{
				"/alerts":                 alertsHTML,
				"DELETE /alerts/a-deploy": alertsHTML,
			})

			if tc.action == "delete" {
				m = confirmDestructive(t, m, "/alerts delete dEpLoY")
			} else {
				m = runLine(t, m, "/alerts "+tc.action+" dEpLoY")
			}

			if !rec.saw(tc.method, tc.path) {
				t.Fatalf("%s did not act on the exact title:\n%s\n%s", tc.action, rec.all(), transcript(m))
			}
			if rec.saw(http.MethodPost, "/alerts/a-deploy-service/"+tc.action) || rec.saw(http.MethodDelete, "/alerts/a-deploy-service") {
				t.Fatalf("%s acted on the longer prefix candidate:\n%s", tc.action, rec.all())
			}
		})
	}
}

func TestAlertActionsUseUniqueSearchTextFallback(t *testing.T) {
	const alertsHTML = `<div data-alert-id="a-deploy" data-alert-scroll-anchor="a-deploy" data-search-text="release plan unique"><p class="font-semibold">Deploy</p></div>
		<div data-alert-id="a-deploy-service" data-alert-scroll-anchor="a-deploy-service" data-search-text="service rollout"><p class="font-semibold">Deploy service</p></div>`

	for _, action := range []string{"read", "approve", "reject", "dismiss", "delete"} {
		t.Run(action, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{
				"/alerts":                 alertsHTML,
				"DELETE /alerts/a-deploy": alertsHTML,
			})
			if action == "delete" {
				m = confirmDestructive(t, m, "/alerts delete ReLeAsE PlAn UnIqUe")
			} else {
				m = runLine(t, m, "/alerts "+action+" ReLeAsE PlAn UnIqUe")
			}

			method, path := http.MethodPost, "/alerts/a-deploy/"+action
			if action == "delete" {
				method, path = http.MethodDelete, "/alerts/a-deploy"
			}
			if !rec.saw(method, path) {
				t.Fatalf("%s did not use the unique search-text fallback:\n%s\n%s", action, rec.all(), transcript(m))
			}
		})
	}
}

func TestAlertActionResolutionFailuresNeverMutate(t *testing.T) {
	const duplicateTitles = `<div data-alert-id="a-one" data-alert-scroll-anchor="a-one" data-search-text="first"><p class="font-semibold">Duplicate</p></div>
		<div data-alert-id="a-two" data-alert-scroll-anchor="a-two" data-search-text="second"><p class="font-semibold">duplicate</p></div>`
	const ambiguousSearchText = `<div data-alert-id="a-deploy" data-alert-scroll-anchor="a-deploy" data-search-text="shared release note"><p class="font-semibold">Deploy</p></div>
		<div data-alert-id="a-deploy-service" data-alert-scroll-anchor="a-deploy-service" data-search-text="shared release note"><p class="font-semibold">Deploy service</p></div>`

	failures := []struct {
		name   string
		alerts string
		ref    string
		want   string
	}{
		{name: "missing", alerts: duplicateTitles, ref: "missing", want: "nothing matches"},
		{name: "duplicate title", alerts: duplicateTitles, ref: "duplicate", want: "ambiguous"},
		{name: "ambiguous search text", alerts: ambiguousSearchText, ref: "shared release note", want: "ambiguous"},
	}
	for _, failure := range failures {
		for _, action := range []string{"read", "approve", "reject", "dismiss", "delete"} {
			t.Run(failure.name+"/"+action, func(t *testing.T) {
				m, rec := dispatchModel(t, map[string]string{"/alerts": failure.alerts})
				line := "/alerts " + action + " " + failure.ref
				if action == "delete" {
					m = confirmDestructive(t, m, line)
				} else {
					m = runLine(t, m, line)
				}

				for _, call := range strings.Split(rec.all(), "\n") {
					if strings.HasPrefix(call, "POST /alerts/") || strings.HasPrefix(call, "DELETE /alerts/") {
						t.Fatalf("%s %q made a mutation request: %s", action, failure.ref, call)
					}
				}
				if out := strings.ToLower(stripANSI(transcript(m))); !strings.Contains(out, failure.want) {
					t.Fatalf("%s %q did not report %q:\n%s", action, failure.ref, failure.want, out)
				}
			})
		}
	}
}

func TestAlertsDeleteResolvesTypedTargetBeforeConfirmation(t *testing.T) {
	const originalAlert = `<div data-alert-id="a-original" data-alert-scroll-anchor="a-original"><p class="font-semibold">Deploy original</p></div>`
	const replacementAlert = `<div data-alert-id="a-rebound" data-alert-scroll-anchor="a-rebound"><p class="font-semibold">Deploy replacement</p></div>`
	const duplicateAlerts = `<div data-alert-id="a-1" data-alert-scroll-anchor="a-1"><p class="font-semibold">Duplicate</p></div>
		<div data-alert-id="a-2" data-alert-scroll-anchor="a-2"><p class="font-semibold">Duplicate</p></div>`
	const partialAlert = `<div data-alert-id="a-partial" data-alert-scroll-anchor="a-partial"><p class="font-semibold">Deploy production</p></div>`

	type testServer struct {
		m            Model
		setList      func(string)
		listRequests *int
		deletes      *[]string
	}
	newTestServer := func(t *testing.T, initialList string) testServer {
		t.Helper()
		list := initialList
		listRequests := 0
		deletes := []string(nil)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/alerts":
				listRequests++
				_, _ = io.WriteString(w, list)
			case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/alerts/"):
				deletes = append(deletes, r.URL.Path)
				_, _ = io.WriteString(w, `<div></div>`)
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
		m.selectedID, m.selectedName = "p1", "demo"
		return testServer{
			m:            m,
			setList:      func(next string) { list = next },
			listRequests: &listRequests,
			deletes:      &deletes,
		}
	}

	t.Run("unknown does not prompt", func(t *testing.T) {
		ts := newTestServer(t, originalAlert)
		m := runLine(t, ts.m, "/alerts delete missing")
		if m.pendingConfirmation != nil || *ts.listRequests != 1 || len(*ts.deletes) != 0 {
			t.Fatalf("unknown delete state: pending=%v lists=%d deletes=%v", m.pendingConfirmation != nil, *ts.listRequests, *ts.deletes)
		}
		if out := stripANSI(transcript(m)); !strings.Contains(out, `nothing matches "missing"`) {
			t.Fatalf("unknown delete did not report resolution error:\n%s", out)
		}
	})

	t.Run("ambiguous does not prompt", func(t *testing.T) {
		ts := newTestServer(t, duplicateAlerts)
		m := runLine(t, ts.m, "/alerts delete Duplicate")
		if m.pendingConfirmation != nil || *ts.listRequests != 1 || len(*ts.deletes) != 0 {
			t.Fatalf("ambiguous delete state: pending=%v lists=%d deletes=%v", m.pendingConfirmation != nil, *ts.listRequests, *ts.deletes)
		}
		if out := stripANSI(transcript(m)); !strings.Contains(out, `"Duplicate" is ambiguous:`) {
			t.Fatalf("ambiguous delete did not report resolution error:\n%s", out)
		}
	})

	t.Run("unique partial uses canonical prompt", func(t *testing.T) {
		ts := newTestServer(t, partialAlert)
		m := runLine(t, ts.m, "/alerts delete product")
		if m.pendingConfirmation == nil || *ts.listRequests != 1 || len(*ts.deletes) != 0 {
			t.Fatalf("partial delete state: pending=%v lists=%d deletes=%v", m.pendingConfirmation != nil, *ts.listRequests, *ts.deletes)
		}
		if prompt := m.pendingConfirmation.message; !strings.Contains(prompt, `"Deploy production"`) {
			t.Fatalf("partial delete prompt = %q, want canonical title", prompt)
		}
		m = runLine(t, m, "no")
		if m.pendingConfirmation != nil || *ts.listRequests != 1 || len(*ts.deletes) != 0 {
			t.Fatalf("cancelled partial delete state: pending=%v lists=%d deletes=%v", m.pendingConfirmation != nil, *ts.listRequests, *ts.deletes)
		}
	})

	t.Run("confirmation captures the resolved ID", func(t *testing.T) {
		ts := newTestServer(t, originalAlert)
		m := runLine(t, ts.m, "/alerts delete deploy")
		if m.pendingConfirmation == nil || *ts.listRequests != 1 {
			t.Fatalf("original alert was not resolved before confirmation: pending=%v lists=%d", m.pendingConfirmation != nil, *ts.listRequests)
		}
		if prompt := m.pendingConfirmation.message; !strings.Contains(prompt, `"Deploy original"`) {
			t.Fatalf("confirmation prompt = %q, want original canonical title", prompt)
		}
		ts.setList(replacementAlert)
		m = runLine(t, m, "yes")
		if *ts.listRequests != 1 || len(*ts.deletes) != 1 || (*ts.deletes)[0] != "/alerts/a-original" {
			t.Fatalf("confirmed delete rebound after list changed: lists=%d deletes=%v", *ts.listRequests, *ts.deletes)
		}
	})
}

func TestAlertActionPresentationStripsTerminalControls(t *testing.T) {
	assertSingleLineSafe := func(t *testing.T, value string) {
		t.Helper()
		for _, unsafe := range []string{"\x1b", "\n", "\r", "\x00"} {
			if strings.Contains(value, unsafe) {
				t.Fatalf("unsafe alert presentation contains %q: %q", unsafe, value)
			}
		}
		if !strings.Contains(value, "Deploy production") {
			t.Fatalf("alert presentation omitted readable title: %q", value)
		}
	}

	t.Run("action success", func(t *testing.T) {
		const hostileTitle = "\x1b[31mDeploy\nproduction\r"
		const alertsHTML = `<div data-alert-id="a-1" data-alert-scroll-anchor="a-1"><p class="font-semibold">` + hostileTitle + `</p></div>`
		listRequests := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/alerts":
				listRequests++
				if listRequests == 1 {
					w.Header().Set("Content-Type", "text/html")
					_, _ = io.WriteString(w, alertsHTML)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"error":"refresh unavailable"}`)
			case r.Method == http.MethodPost && r.URL.Path == "/alerts/a-1/approve":
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
		m.selectedID, m.selectedName = "p1", "demo"
		m = runLine(t, m, "/alerts approve a-1")
		if len(m.log) == 0 {
			t.Fatal("approve produced no transcript entries")
		}
		assertSingleLineSafe(t, m.log[len(m.log)-1].text)
	})

	t.Run("typed delete confirmation", func(t *testing.T) {
		m, _ := dispatchModel(t, nil)
		next, cmd := m.Update(alertDeleteTargetMsg{
			projectID: "p1",
			alert: client.Alert{
				ID:    "a-1",
				Title: "\x1b[31mDeploy\nproduction\r\x00",
			},
		})
		if cmd != nil {
			t.Fatal("resolved delete target unexpectedly returned follow-up work")
		}
		m = next.(Model)
		if m.pendingConfirmation == nil {
			t.Fatal("resolved delete target did not open confirmation")
		}
		assertSingleLineSafe(t, m.pendingConfirmation.message)
	})
}

func TestAlertsDeleteConfirmationResolutionFailureAndRefresh(t *testing.T) {
	const initialAlerts = `<div data-alert-id="a-1" data-alert-scroll-anchor="a-1" data-search-text="build failed"><p class="font-semibold">Build failed</p></div>
		<div data-alert-id="a-2" data-alert-scroll-anchor="a-2" data-search-text="duplicate one"><p class="font-semibold">Duplicate</p></div>
		<div data-alert-id="a-3" data-alert-scroll-anchor="a-3" data-search-text="duplicate two"><p class="font-semibold">Duplicate</p></div>`
	const refreshedAlerts = `<div data-alert-id="a-2" data-alert-scroll-anchor="a-2" data-search-text="duplicate one"><p class="font-semibold">Remaining alert</p></div>`
	var mu sync.Mutex
	var gets, deletes int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/alerts":
			gets++
			_, _ = io.WriteString(w, initialAlerts)
		case r.Method == http.MethodDelete && r.URL.Path == "/alerts/a-1":
			deletes++
			if r.URL.Query().Get("project_id") != "p1" || r.Header.Get("HX-Request") != "true" {
				t.Errorf("delete request lost project scope or HTMX header: %s", r.URL.RequestURI())
			}
			_, _ = io.WriteString(w, refreshedAlerts)
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
	m.selectedID, m.selectedName = "p1", "demo"

	m = runLine(t, m, "/alerts delete a-1")
	if m.pendingConfirmation == nil || gets != 1 || deletes != 0 {
		t.Fatalf("delete was not resolved before confirmation: pending=%v gets=%d deletes=%d", m.pendingConfirmation != nil, gets, deletes)
	}
	m = runLine(t, m, "no")
	if m.pendingConfirmation != nil || gets != 1 || deletes != 0 {
		t.Fatalf("cancelled delete performed work: pending=%v gets=%d deletes=%d", m.pendingConfirmation != nil, gets, deletes)
	}

	m = runLine(t, m, "/alerts delete missing")
	m = runLine(t, m, "/alerts delete Duplicate")
	if deletes != 0 {
		t.Fatalf("missing or ambiguous references deleted an alert: deletes=%d", deletes)
	}
	plain := stripANSI(transcript(m))
	if !strings.Contains(plain, `nothing matches "missing"`) || !strings.Contains(plain, `"Duplicate" is ambiguous:`) {
		t.Fatalf("resolution errors not reported:\n%s", plain)
	}

	beforeSuccessGets := gets
	m = confirmDestructive(t, m, "/alerts delete a-1")
	if deletes != 1 {
		t.Fatalf("confirmed delete count = %d, want 1", deletes)
	}
	if gets != beforeSuccessGets+1 {
		t.Fatalf("successful delete made %d list GETs, want one reference-resolution GET and no post-delete GET", gets-beforeSuccessGets)
	}
	plain = stripANSI(transcript(m))
	if !strings.Contains(plain, "delete: Build failed") || !strings.Contains(plain, "Remaining alert") || strings.Contains(plain, "delete: Build failed\n\nAlerts\n3 unread") {
		t.Fatalf("delete did not render the backend-refreshed list:\n%s", plain)
	}
}

func TestAlertsDeleteReportsBackendFailure(t *testing.T) {
	const alertsHTML = `<div data-alert-id="a-1" data-alert-scroll-anchor="a-1" data-search-text="build"><p class="font-semibold">Build failed</p></div>`
	var deletes int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/alerts":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, alertsHTML)
		case r.Method == http.MethodDelete && r.URL.Path == "/alerts/a-1":
			deletes++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":"alert deletion unavailable"}`)
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
	m.selectedID, m.selectedName = "p1", "demo"
	m = confirmDestructive(t, m, "/alerts delete a-1")
	if deletes != 1 {
		t.Fatalf("delete requests = %d, want 1", deletes)
	}
	if out := stripANSI(transcript(m)); !strings.Contains(out, "alert deletion unavailable") || strings.Contains(out, "delete: Build failed") {
		t.Fatalf("backend deletion failure was not reported correctly:\n%s", out)
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
		{"/workers", "GET", "/api/capacity/global"},
		{"/channels", "GET", "/channels"},
		{"/personality", "GET", "/personality"},
		{"/pulse", "GET", "/upcoming"},
		{"/reflection", "GET", "/history"},
		{"/insights", "GET", "/insights"},
		{"/automations", "GET", "/automations"},
		{"/grades", "GET", "/history"},
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
		"/automations run au-1",
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

func TestTaskGoalLifecycleUsesScopedRoutesAndKeepsPauseAsObjective(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantURL  string
		wantOut  string
		wantForm string
	}{
		{name: "set", line: "/tasks goal Refactor | all tests pass", wantURL: "POST /tasks/t-1/goal?project_id=p1", wantOut: "goal set on Refactor the API: all tests pass", wantForm: "objective=all+tests+pass"},
		{name: "clear", line: "/tasks goal Refactor | clear", wantURL: "POST /tasks/t-1/goal/clear?project_id=p1", wantOut: "cleared goal on Refactor the API"},
		{name: "pause", line: "/tasks goal pause Refactor", wantURL: "POST /tasks/t-1/goal/pause?project_id=p1", wantOut: "paused goal on Refactor the API"},
		{name: "resume", line: "/tasks goal resume Refactor", wantURL: "POST /tasks/t-1/goal/resume?project_id=p1", wantOut: "resumed goal on Refactor the API"},
		{name: "pause objective", line: "/tasks goal Refactor | pause", wantURL: "POST /tasks/t-1/goal?project_id=p1", wantOut: "goal set on Refactor the API: pause", wantForm: "objective=pause"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
			m = runLine(t, m, tc.line)
			if !rec.sawQuery(tc.wantURL) {
				t.Fatalf("requests = %q, want %q", rec.urlsSnapshot(), tc.wantURL)
			}
			if tc.wantForm != "" && !rec.sawForm(tc.wantForm) {
				t.Fatalf("forms = %q, want %q", rec.forms, tc.wantForm)
			}
			if out := transcript(m); !strings.Contains(out, tc.wantOut) {
				t.Fatalf("output missing %q:\n%s", tc.wantOut, out)
			}
		})
	}
}

func TestTaskGoalLifecycleRejectsUnresolvableReferencesBeforeMutation(t *testing.T) {
	const duplicateTitlesHTML = `<div>
		<div class="card" data-task-id="t-1" data-task-status="pending" data-task-category="backlog"><a href="/tasks/t-1" title="Deploy">Deploy</a></div>
		<div class="card" data-task-id="t-2" data-task-status="pending" data-task-category="backlog"><a href="/tasks/t-2" title="deploy">deploy</a></div>
	</div>`

	for _, tc := range []struct {
		name  string
		board string
		line  string
		want  string
	}{
		{name: "missing", board: taskBoardHTML, line: "/tasks goal pause foreign-task", want: "nothing matches"},
		{name: "ambiguous", board: duplicateTitlesHTML, line: "/tasks goal resume DEPLOY", want: "ambiguous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/tasks": tc.board})
			m = runLine(t, m, tc.line)
			if rec.saw("POST", "/tasks/t-1/goal/pause") || rec.saw("POST", "/tasks/t-1/goal/resume") || rec.saw("POST", "/tasks/t-2/goal/pause") || rec.saw("POST", "/tasks/t-2/goal/resume") {
				t.Fatalf("unresolvable reference mutated a goal:\n%s", rec.all())
			}
			if out := strings.ToLower(transcript(m)); !strings.Contains(out, tc.want) {
				t.Fatalf("output missing %q:\n%s", tc.want, out)
			}
		})
	}
}

func TestTaskGoalLifecycleFailureDoesNotClaimSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tasks":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(taskBoardHTML))
		case "/tasks/t-1/goal/pause":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"pause denied"}`))
		default:
			http.NotFound(w, r)
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
	m = runLine(t, m, "/tasks goal pause Refactor")
	out := transcript(m)
	if !strings.Contains(out, "pause denied") {
		t.Fatalf("backend failure was not shown:\n%s", out)
	}
	if strings.Contains(out, "paused goal on") {
		t.Fatalf("backend failure claimed success:\n%s", out)
	}
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

func TestWorkersProjectLimitRequiresProject(t *testing.T) {
	m, rec := dispatchModel(t, nil)
	m.selectedID = ""
	m.selectedName = ""

	m = runLine(t, m, "/works project 3")
	if out := stripANSI(transcript(m)); !strings.Contains(out, "no project selected") {
		t.Fatalf("missing project-limit selection error:\n%s", out)
	}
	if calls := rec.all(); calls != "" {
		t.Fatalf("project limit made requests without a selected project:\n%s", calls)
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

func TestScheduleMutationsUseExactSelectedProjectForDirectAndPickerPaths(t *testing.T) {
	const projectB = "project B&mode=terminal"
	const encodedScope = "project_id=project+B%26mode%3Dterminal"
	cases := []struct {
		name        string
		line        string
		method      string
		path        string
		needsTasks  bool
		destructive bool
		picker      bool
	}{
		{name: "add direct", line: "/schedule add Refactor 2026-09-01T10:00 daily", method: http.MethodPost, path: "/tasks/t-1/schedule", needsTasks: true},
		{name: "toggle direct", line: "/schedule toggle s-1", method: http.MethodPost, path: "/api/schedules/s-1/toggle"},
		{name: "delete direct", line: "/schedule delete s-1", method: http.MethodDelete, path: "/schedules/s-1", destructive: true},
		{name: "toggle picker", line: "/schedule toggle", method: http.MethodPost, path: "/api/schedules/s-1/toggle", picker: true},
		{name: "delete picker", line: "/schedule delete", method: http.MethodDelete, path: "/schedules/s-1", destructive: true, picker: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mutations int
			m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case tc.needsTasks && r.Method == http.MethodGet && r.URL.Path == "/tasks":
					if got := r.URL.Query().Get("project_id"); got != projectB {
						t.Errorf("task resolution project_id = %q, want %q", got, projectB)
					}
					w.Header().Set("Content-Type", "text/html")
					_, _ = w.Write([]byte(taskBoardHTML))
				case r.Method == http.MethodGet && r.URL.Path == "/schedule":
					if got := r.URL.Query().Get("project_id"); got != projectB {
						t.Errorf("schedule read project_id = %q, want %q", got, projectB)
					}
					w.Header().Set("Content-Type", "text/html")
					_, _ = w.Write([]byte(selScheduleHTML))
				case r.Method == tc.method && r.URL.Path == tc.path:
					mutations++
					if got := r.URL.Query().Get("project_id"); got != projectB {
						http.Error(w, "schedule belongs to another project", http.StatusForbidden)
						return
					}
					if r.URL.RawQuery != encodedScope {
						t.Errorf("mutation RawQuery = %q, want %q", r.URL.RawQuery, encodedScope)
					}
					if tc.method == http.MethodPost && strings.Contains(tc.path, "/api/schedules/") {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{}`))
						return
					}
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
					w.WriteHeader(http.StatusNotFound)
				}
			})
			m.selectedID = projectB
			m = runLine(t, m, tc.line)
			if tc.picker {
				if !m.selectorActive {
					t.Fatalf("expected picker:\n%s", transcript(m))
				}
				m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
			}
			if tc.destructive {
				if m.pendingConfirmation == nil {
					t.Fatalf("expected confirmation:\n%s", transcript(m))
				}
				m = runLine(t, m, "yes")
			}
			if mutations != 1 {
				t.Fatalf("mutations = %d, want 1", mutations)
			}
			if out := stripANSI(transcript(m)); strings.Contains(out, "error:") {
				t.Fatalf("Project B mutation failed despite explicit scope:\n%s", out)
			}
		})
	}
}

func TestScheduleMutationExplicitOwnershipMismatchRemainsRejected(t *testing.T) {
	var mutations int
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/schedule":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(selScheduleHTML))
		case r.Method == http.MethodPost && r.URL.Path == "/api/schedules/s-1/toggle":
			mutations++
			if got := r.URL.Query().Get("project_id"); got != "project A" {
				t.Errorf("mutation project_id = %q, want project A", got)
			}
			http.Error(w, "schedule belongs to another project", http.StatusForbidden)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
		}
	})
	m.selectedID = "project A"
	m = runLine(t, m, "/schedule toggle s-1")
	if mutations != 1 {
		t.Fatalf("mutations = %d, want 1", mutations)
	}
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "403") {
		t.Fatalf("ownership error missing from transcript:\n%s", out)
	}
	if strings.Contains(out, "toggled schedule") {
		t.Fatalf("ownership failure reported success:\n%s", out)
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

func TestScheduleMutationsKeepStatusWhenRefreshFails(t *testing.T) {
	actions := []struct {
		name           string
		line           string
		method         string
		path           string
		status         string
		initialGETs    int
		needsTaskBoard bool
	}{
		{
			name:           "add",
			line:           "/schedule add Refactor 2026-09-01T10:00 daily",
			method:         http.MethodPost,
			path:           "/tasks/t-1/schedule",
			status:         "scheduled Refactor the API for 2026-09-01T10:00 (daily)",
			needsTaskBoard: true,
		},
		{
			name:        "delete",
			line:        "/schedule delete s-1",
			method:      http.MethodDelete,
			path:        "/schedules/s-1",
			status:      "deleted schedule",
			initialGETs: 1,
		},
		{
			name:        "toggle",
			line:        "/schedule toggle s-1",
			method:      http.MethodPost,
			path:        "/api/schedules/s-1/toggle",
			status:      "toggled schedule",
			initialGETs: 1,
		},
	}
	failures := []struct {
		name      string
		transport bool
	}{
		{name: "server error"},
		{name: "transport failure", transport: true},
	}

	for _, tc := range actions {
		tc := tc
		for _, failure := range failures {
			failure := failure
			t.Run(tc.name+"/"+failure.name, func(t *testing.T) {
				rec := &recorder{}
				var scheduleGETs atomic.Int32
				m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
					rec.recordURL(r.Method, r.URL.RequestURI())
					switch {
					case tc.needsTaskBoard && r.Method == http.MethodGet && r.URL.Path == "/tasks":
						w.Header().Set("Content-Type", "text/html")
						_, _ = w.Write([]byte(taskBoardHTML))
					case r.Method == http.MethodGet && r.URL.Path == "/schedule":
						scheduleGET := scheduleGETs.Add(1)
						if got := r.URL.Query().Get("project_id"); got != "p1" {
							t.Errorf("schedule GET project_id = %q, want p1", got)
						}
						if scheduleGET <= int32(tc.initialGETs) {
							w.Header().Set("Content-Type", "text/html")
							_, _ = w.Write([]byte(selScheduleHTML))
							return
						}
						if failure.transport {
							hijacker, ok := w.(http.Hijacker)
							if !ok {
								t.Error("test server does not support connection hijacking")
								return
							}
							conn, _, err := hijacker.Hijack()
							if err != nil {
								t.Errorf("hijack refresh request: %v", err)
								return
							}
							_ = conn.Close()
							return
						}
						http.Error(w, "refresh failed", http.StatusInternalServerError)
					case r.Method == tc.method && r.URL.Path == tc.path:
						if tc.name == "toggle" {
							w.Header().Set("Content-Type", "application/json")
							_, _ = w.Write([]byte(`{}`))
							return
						}
						w.WriteHeader(http.StatusNoContent)
					default:
						t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				})

				if tc.name == "delete" {
					m = confirmDestructive(t, m, tc.line)
				} else {
					m = runLine(t, m, tc.line)
				}

				if got := rec.count(tc.method, tc.path); got != 1 {
					t.Fatalf("%s mutation count = %d, want 1; calls:\n%s", tc.name, got, rec.all())
				}
				if got, want := scheduleGETs.Load(), int32(tc.initialGETs+1); got < want {
					t.Fatalf("%s schedule GET count = %d, want at least %d; calls:\n%s", tc.name, got, want, rec.all())
				}
				out := stripANSI(transcript(m))
				if !strings.Contains(out, tc.status) {
					t.Fatalf("successful %s status missing after refresh failure:\n%s", tc.name, out)
				}
				if strings.Contains(out, "nothing scheduled") {
					t.Fatalf("failed refresh must not claim that nothing is scheduled:\n%s", out)
				}
				if strings.Contains(out, "error:") || strings.Contains(out, "refresh failed") {
					t.Fatalf("successful mutation must not be paired with a refresh error:\n%s", out)
				}
			})
		}
	}
}

func TestScheduleMutationsSurfaceActionFailures(t *testing.T) {
	actions := []struct {
		name        string
		line        string
		method      string
		path        string
		status      string
		initialGETs int
	}{
		{
			name:   "add",
			line:   "/schedule add Refactor 2026-09-01T10:00 daily",
			method: http.MethodPost,
			path:   "/tasks/t-1/schedule",
			status: "scheduled Refactor the API",
		},
		{
			name:        "delete",
			line:        "/schedule delete s-1",
			method:      http.MethodDelete,
			path:        "/schedules/s-1",
			status:      "deleted schedule",
			initialGETs: 1,
		},
		{
			name:        "toggle",
			line:        "/schedule toggle s-1",
			method:      http.MethodPost,
			path:        "/api/schedules/s-1/toggle",
			status:      "toggled schedule",
			initialGETs: 1,
		},
	}

	for _, tc := range actions {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			scheduleGETs := 0
			m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
				rec.recordURL(r.Method, r.URL.RequestURI())
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/tasks":
					w.Header().Set("Content-Type", "text/html")
					_, _ = w.Write([]byte(taskBoardHTML))
				case r.Method == http.MethodGet && r.URL.Path == "/schedule":
					scheduleGETs++
					if got := r.URL.Query().Get("project_id"); got != "p1" {
						t.Errorf("schedule GET project_id = %q, want p1", got)
					}
					w.Header().Set("Content-Type", "text/html")
					_, _ = w.Write([]byte(selScheduleHTML))
				case r.Method == tc.method && r.URL.Path == tc.path:
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"error":"schedule mutation failed"}`))
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			})

			if tc.name == "delete" {
				m = confirmDestructive(t, m, tc.line)
			} else {
				m = runLine(t, m, tc.line)
			}

			if got := rec.count(tc.method, tc.path); got != 1 {
				t.Fatalf("%s mutation count = %d, want 1; calls:\n%s", tc.name, got, rec.all())
			}
			if got, want := scheduleGETs, tc.initialGETs; got != want {
				t.Fatalf("%s schedule GET count = %d, want %d after action failure; calls:\n%s", tc.name, got, want, rec.all())
			}
			out := stripANSI(transcript(m))
			if !strings.Contains(out, "schedule mutation failed") {
				t.Fatalf("%s action failure missing from transcript:\n%s", tc.name, out)
			}
			if strings.Contains(out, tc.status) {
				t.Fatalf("failed %s mutation reported success:\n%s", tc.name, out)
			}
		})
	}
}
func TestScheduleInspectionSelectionRoutesHaveParity(t *testing.T) {
	const projectID = "project-two"
	cases := []struct {
		name             string
		taskID           string
		taskStatus       int
		taskBody         string
		json             bool
		want             string
		wantTaskInOutput bool
	}{
		{name: "plain pending task", taskID: "task-1", taskStatus: http.StatusOK, taskBody: `<div data-task-id="task-1" data-project-id="project-two"><h2 class="font-bold">Pending task</h2><div data-task-status="pending"></div></div>`, want: "Task: Pending task (pending)", wantTaskInOutput: true},
		{name: "plain running task", taskID: "task-1", taskStatus: http.StatusOK, taskBody: `<div data-task-id="task-1" data-project-id="project-two"><h2 class="font-bold">Running task</h2><div data-task-status="running"></div></div>`, want: "Task: Running task (running)", wantTaskInOutput: true},
		{name: "plain completed task", taskID: "task-1", taskStatus: http.StatusOK, taskBody: `<div data-task-id="task-1" data-project-id="project-two"><h2 class="font-bold">Completed task</h2><div data-task-status="completed"></div></div>`, want: "Task: Completed task (completed)", wantTaskInOutput: true},
		{name: "plain missing task", taskID: "task-1", taskStatus: http.StatusNotFound, want: "Bound task unavailable (task-1)"},
		{name: "plain entry without task id", taskStatus: http.StatusOK, want: "Bound task unavailable"},
		{name: "json task", taskID: "task-1", taskStatus: http.StatusOK, taskBody: `<div data-task-id="task-1" data-project-id="project-two"><h2 class="font-bold">Running task</h2><div data-task-status="running"></div></div>`, json: true, wantTaskInOutput: true},
		{name: "json missing task", taskID: "task-1", taskStatus: http.StatusNotFound, json: true},
		{name: "json entry without task id", taskStatus: http.StatusOK, json: true},
	}

	previousJSON := jsonMode
	defer func() { jsonMode = previousJSON }()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var scheduleRequests, taskRequests int
			m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Query().Get("project_id"); got != projectID {
					t.Errorf("%s %s project_id = %q, want %q", r.Method, r.URL.Path, got, projectID)
				}
				switch r.URL.Path {
				case "/schedule":
					scheduleRequests++
					_, _ = io.WriteString(w, `<div id="schedule-content"><div data-task-id="`+tc.taskID+`" data-schedule-id="sched-target">Target schedule</div><div data-task-id="other-task" data-schedule-id="sched-other">Other schedule</div></div>`)
				case "/tasks/task-1":
					taskRequests++
					w.WriteHeader(tc.taskStatus)
					_, _ = io.WriteString(w, tc.taskBody)
				default:
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
				}
			})
			m.selectedID = projectID
			jsonMode = tc.json

			var outputs []string
			for _, action := range []string{"show", "open"} {
				for _, selectedByPicker := range []bool{false, true} {
					route := action + " typed"
					result := m
					if selectedByPicker {
						route = action + " picker"
						result = runLine(t, result, "/schedule "+action)
						if !result.selectorActive {
							t.Fatalf("%s did not open a selector:\n%s", route, transcript(result))
						}
						result = selKey(t, result, tea.KeyMsg{Type: tea.KeyEnter})
					} else {
						result = runLine(t, result, "/schedule "+action+" sched-target")
					}
					if len(result.log) == 0 {
						t.Fatalf("%s produced no transcript output", route)
					}
					last := result.log[len(result.log)-1]
					if last.role != "result" {
						t.Fatalf("%s output role = %q, want result:\n%s", route, last.role, transcript(result))
					}
					outputs = append(outputs, last.text)
				}
			}

			for _, output := range outputs[1:] {
				if output != outputs[0] {
					t.Fatalf("typed/picker or show/open output drifted:\nfirst:  %q\nactual: %q", outputs[0], output)
				}
			}
			if tc.json {
				var output map[string]json.RawMessage
				if err := json.Unmarshal([]byte(outputs[0]), &output); err != nil {
					t.Fatalf("inspection JSON is invalid: %v\n%s", err, outputs[0])
				}
				if output["schedule"] == nil {
					t.Fatalf("inspection JSON omitted schedule: %s", outputs[0])
				}
				_, hasTask := output["task"]
				if hasTask != tc.wantTaskInOutput {
					t.Fatalf("inspection JSON task presence = %t, want %t: %s", hasTask, tc.wantTaskInOutput, outputs[0])
				}
			} else if !strings.Contains(stripANSI(outputs[0]), tc.want) {
				t.Fatalf("inspection output missing %q:\n%s", tc.want, stripANSI(outputs[0]))
			}

			if got, want := scheduleRequests, 4; got != want {
				t.Fatalf("schedule requests = %d, want %d", got, want)
			}
			wantTaskRequests := 0
			if tc.taskID != "" {
				wantTaskRequests = 4
			}
			if taskRequests != wantTaskRequests {
				t.Fatalf("bound task requests = %d, want %d", taskRequests, wantTaskRequests)
			}
		})
	}
}

func TestScheduleInspectionOutputPropagatesBoundTaskLookupFailures(t *testing.T) {
	const projectID = "project-two"
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
		auth   bool
	}{
		{name: "authentication", status: http.StatusUnauthorized, want: "unauthorized", auth: true},
		{name: "backend failure", status: http.StatusBadGateway, body: `{"error":"task service unavailable"}`, want: "server error (502): task service unavailable"},
		{name: "malformed task", status: http.StatusOK, body: `<div data-project-id="project-two"><h2 class="font-bold">Unverified</h2></div>`, want: `task "task-1" was not found in selected project`},
		{name: "foreign task", status: http.StatusOK, body: `<div data-task-id="task-1" data-project-id="other-project"><h2 class="font-bold">Foreign</h2></div>`, want: `task "task-1" was not found in selected project`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Query().Get("project_id"); got != projectID {
					t.Errorf("task lookup project_id = %q, want %q", got, projectID)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			output, err := scheduleInspectionOutput(context.Background(), m.client, projectID, client.ScheduleEntry{ScheduleID: "sched-1", TaskID: "task-1"})
			if output != "" {
				t.Fatalf("failed task lookup returned output: %q", output)
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("task lookup error = %v, want visible %q", err, tc.want)
			}
			if tc.auth && !client.IsAuthRequired(err) {
				t.Fatalf("authentication error lost its type: %T %v", err, err)
			}
		})
	}

	t.Run("cancelled", func(t *testing.T) {
		m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("cancelled lookup reached the backend")
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		output, err := scheduleInspectionOutput(ctx, m.client, projectID, client.ScheduleEntry{ScheduleID: "sched-1", TaskID: "task-1"})
		if output != "" || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled lookup output=%q error=%v, want context cancellation", output, err)
		}
	})

	t.Run("transport", func(t *testing.T) {
		m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("project_id"); got != projectID {
				t.Errorf("task lookup project_id = %q, want %q", got, projectID)
			}
			<-r.Context().Done()
		})
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		output, err := scheduleInspectionOutput(ctx, m.client, projectID, client.ScheduleEntry{ScheduleID: "sched-1", TaskID: "task-1"})
		if output != "" || !client.IsTransportError(err) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("transport lookup output=%q error=%v, want propagated deadline transport error", output, err)
		}
	})
}

func TestScheduleShowAndOpenResolveReferencesAndRenderBoundTask(t *testing.T) {
	const schedules = `<div id="schedule-content">
		<div data-task-id="task-1" data-schedule-id="sched-alpha">Alpha nightly</div>
		<div data-task-id="task-2" data-schedule-id="alpha">Alpha weekly</div>
		<div data-task-id="task-2" data-schedule-id="sched-beta">Beta report</div>
	</div>`
	for _, tc := range []struct {
		name, action, ref, wantSchedule, wantTask string
	}{
		{name: "show exact id precedence", action: "show", ref: "alpha", wantSchedule: "Schedule ID: alpha", wantTask: "Weekly task"},
		{name: "open unique prefix", action: "open", ref: "sched-b", wantSchedule: "Schedule ID: sched-beta", wantTask: "Weekly task"},
		{name: "show unique substring", action: "show", ref: "night", wantSchedule: "Schedule ID: sched-alpha", wantTask: "Nightly task"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Query().Get("project_id"); got != "p1" {
					t.Errorf("%s %s project_id = %q, want p1", r.Method, r.URL.Path, got)
				}
				switch r.URL.Path {
				case "/schedule":
					_, _ = io.WriteString(w, schedules)
				case "/tasks/task-1":
					_, _ = io.WriteString(w, `<div data-task-id="task-1" data-project-id="p1"><h2 class="font-bold">Nightly task</h2><div data-task-status="pending"></div></div>`)
				case "/tasks/task-2":
					_, _ = io.WriteString(w, `<div data-task-id="task-2" data-project-id="p1"><h2 class="font-bold">Weekly task</h2><div data-task-status="running"></div></div>`)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
					w.WriteHeader(http.StatusNotFound)
				}
			})
			m = runLine(t, m, "/schedule "+tc.action+" "+tc.ref)
			out := stripANSI(transcript(m))
			for _, want := range []string{tc.wantSchedule, tc.wantTask, "/tasks open " + map[string]string{"Weekly task": "task-2", "Nightly task": "task-1"}[tc.wantTask]} {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestScheduleShowExactSemanticNamePrecedesRenderedTextPrefixCollision(t *testing.T) {
	const schedules = `<div id="schedule-content">
		<div data-task-id="task-1" data-schedule-id="sched-one"><div class="font-semibold truncate leading-tight">Nightly build</div><div class="opacity-60 leading-tight">02:00</div></div>
		<div data-task-id="task-2" data-schedule-id="sched-two"><div class="font-semibold truncate leading-tight">Nightly build extended</div><div class="opacity-60 leading-tight">03:00</div></div>
	</div>`
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/schedule":
			_, _ = io.WriteString(w, schedules)
		case "/tasks/task-1":
			_, _ = io.WriteString(w, `<div data-task-id="task-1" data-project-id="p1"><h2 class="font-bold">Nightly task</h2></div>`)
		case "/tasks/task-2":
			t.Fatal("exact schedule name resolved to the longer prefix collision")
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	})
	m = runLine(t, m, "/schedule show Nightly build")
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "Schedule ID: sched-one") || strings.Contains(out, "ambiguous") {
		t.Fatalf("exact semantic name did not win:\n%s", out)
	}
}

func TestScheduleShowDistinguishesMissingTaskFromLookupFailures(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		taskBody    string
		want        string
		unavailable bool
	}{
		{name: "deleted", status: http.StatusNotFound, want: "Bound task unavailable (task-1)", unavailable: true},
		{name: "backend failure", status: http.StatusBadGateway, taskBody: `{"error":"task service unavailable"}`, want: "server error (502): task service unavailable"},
		{name: "malformed identity", status: http.StatusOK, taskBody: `<div data-project-id="p1"><h2 class="font-bold">Unverified</h2></div>`, want: `task "task-1" was not found in selected project`},
		{name: "foreign identity", status: http.StatusOK, taskBody: `<div data-task-id="task-1" data-project-id="p2"><h2 class="font-bold">Foreign</h2></div>`, want: `task "task-1" was not found in selected project`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/schedule":
					_, _ = io.WriteString(w, `<div id="schedule-content"><div data-task-id="task-1" data-schedule-id="sched-one">Nightly</div></div>`)
				case "/tasks/task-1":
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.taskBody)
				default:
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
				}
			})
			m = runLine(t, m, "/schedule show sched-one")
			out := stripANSI(transcript(m))
			if !strings.Contains(out, tc.want) {
				t.Fatalf("output missing %q:\n%s", tc.want, out)
			}
			if !tc.unavailable && strings.Contains(out, "Bound task unavailable") {
				t.Fatalf("lookup failure was reported as unavailable:\n%s", out)
			}
		})
	}
}

func TestScheduleShowHandlesMissingAndInvalidReferencesWithoutMutation(t *testing.T) {
	const schedules = `<div id="schedule-content">
		<div data-task-id="deleted-task" data-schedule-id="sched-one">Nightly one</div>
		<div data-task-id="" data-schedule-id="sched-two">Nightly two</div>
	</div>`
	for _, tc := range []struct {
		name, ref, want string
	}{
		{name: "deleted task", ref: "sched-one", want: "Bound task unavailable (deleted-task)"},
		{name: "missing task id", ref: "sched-two", want: "Bound task unavailable"},
		{name: "ambiguous", ref: "Nightly", want: "ambiguous"},
		{name: "unknown", ref: "missing", want: "nothing matches"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mutations int
			m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					mutations++
				}
				switch r.URL.Path {
				case "/schedule":
					_, _ = io.WriteString(w, schedules)
				case "/tasks":
					_, _ = io.WriteString(w, `<div></div>`)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			})
			m = runLine(t, m, "/schedule show "+tc.ref)
			if out := stripANSI(transcript(m)); !strings.Contains(out, tc.want) {
				t.Fatalf("output missing %q:\n%s", tc.want, out)
			}
			if mutations != 0 {
				t.Fatalf("read action made %d mutation requests", mutations)
			}
		})
	}
}

func scheduleEditDetail(projectID string) string {
	return `<div id="task-detail-content"><div data-project-id="` + projectID + `"></div>
		<div data-schedule-id="s-1"><form hx-put="/schedules/s-1?project_id=` + projectID + `"><input name="run_at" value="2026-01-02T09:00"><select name="repeat_type"><option value="daily" selected>Daily</option></select><input name="repeat_interval" value="1"><input type="checkbox" name="clear_context_on_start" value="true" checked></form></div>
		<div data-schedule-id="s-2"><form hx-put="/schedules/s-2?project_id=` + projectID + `"><input name="run_at" value="2026-02-03T10:30"><select name="repeat_type"><option value="daily">Daily</option><option value="weekly" selected>Weekly</option></select><input name="repeat_interval" value="3"><input type="hidden" name="clear_context_on_start" value="false"><input type="checkbox" name="clear_context_on_start" value="true"></form></div></div>`
}

func TestScheduleEditReferencesMayContainSettingWordsWhenQuoted(t *testing.T) {
	cases := []struct {
		name       string
		line       string
		scheduleID string
	}{
		{name: "exact reserved word", line: `/schedule edit "repeat" interval 4`, scheduleID: "s-1"},
		{name: "multiword", line: `/schedule edit "run-at repeat interval clear-context report" interval 4`, scheduleID: "s-2"},
	}
	const scheduleHTML = `<div id="schedule-content">
		<div data-task-id="t-1" data-schedule-id="s-1">repeat</div>
		<div data-task-id="t-1" data-schedule-id="s-2">run-at repeat interval clear-context report</div>
	</div>`
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var putPath string
			m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/schedule":
					_, _ = io.WriteString(w, scheduleHTML)
				case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1":
					_, _ = io.WriteString(w, scheduleEditDetail("p1"))
				case r.Method == http.MethodPut:
					putPath = r.URL.Path
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
					w.WriteHeader(http.StatusNotFound)
				}
			})
			m = runLine(t, m, tc.line)
			if want := "/schedules/" + tc.scheduleID; putPath != want {
				t.Fatalf("PUT path = %q, want %q; transcript:\n%s", putPath, want, stripANSI(transcript(m)))
			}
		})
	}
}

func TestScheduleEditSettingWordReferencePreservesAmbiguity(t *testing.T) {
	const scheduleHTML = `<div id="schedule-content">
		<div data-task-id="t-1" data-schedule-id="s-1">Weekly repeat report alpha</div>
		<div data-task-id="t-2" data-schedule-id="s-2">Weekly repeat report beta</div>
	</div>`
	var puts int
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts++
		}
		_, _ = io.WriteString(w, scheduleHTML)
	})
	m = runLine(t, m, `/schedule edit "Weekly repeat report" interval 4`)
	out := stripANSI(transcript(m))
	if puts != 0 || !strings.Contains(out, `"Weekly repeat report" is ambiguous`) {
		t.Fatalf("ambiguous edit puts=%d:\n%s", puts, out)
	}
}

func TestScheduleEditReferencesMayContainValidSettingPairsWhenQuoted(t *testing.T) {
	cases := []struct {
		name       string
		line       string
		scheduleID string
	}{
		{name: "run-at", line: `/schedule edit "Run run-at 2026-01-02T09:00 report" interval 5`, scheduleID: "s-run-at"},
		{name: "repeat", line: `/schedule edit "Run repeat daily report" interval 5`, scheduleID: "s-repeat"},
		{name: "interval", line: `/schedule edit "Run interval 4 report" interval 5`, scheduleID: "s-interval"},
		{name: "clear-context", line: `/schedule edit "Run clear-context true report" interval 5`, scheduleID: "s-clear"},
		{name: "exact title ending in valid pair", line: `/schedule edit "Weekly repeat daily" interval 5`, scheduleID: "s-ending"},
	}
	const scheduleHTML = `<div id="schedule-content">
		<div data-task-id="t-1" data-schedule-id="s-run-at">Run run-at 2026-01-02T09:00 report</div>
		<div data-task-id="t-1" data-schedule-id="s-repeat">Run repeat daily report</div>
		<div data-task-id="t-1" data-schedule-id="s-interval">Run interval 4 report</div>
		<div data-task-id="t-1" data-schedule-id="s-clear">Run clear-context true report</div>
		<div data-task-id="t-1" data-schedule-id="s-ending">Weekly repeat daily</div>
		<div data-task-id="t-2" data-schedule-id="s-other">Weekly summary</div>
	</div>`
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var putPath string
			m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/schedule":
					_, _ = io.WriteString(w, scheduleHTML)
				case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1":
					_, _ = io.WriteString(w, scheduleEditDetailForIDs("p1", tc.scheduleID))
				case r.Method == http.MethodPut:
					putPath = r.URL.Path
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
					w.WriteHeader(http.StatusNotFound)
				}
			})
			m = runLine(t, m, tc.line)
			if want := "/schedules/" + tc.scheduleID; putPath != want {
				t.Fatalf("PUT path = %q, want %q; transcript:\n%s", putPath, want, stripANSI(transcript(m)))
			}
		})
	}
}

func scheduleEditDetailForIDs(projectID string, scheduleIDs ...string) string {
	var forms strings.Builder
	for _, scheduleID := range scheduleIDs {
		fmt.Fprintf(&forms, `<div data-schedule-id="%s"><form hx-put="/schedules/%s?project_id=%s"><input name="run_at" value="2026-01-02T09:00"><select name="repeat_type"><option value="daily" selected>Daily</option></select><input name="repeat_interval" value="1"><input type="checkbox" name="clear_context_on_start" value="true" checked></form></div>`, scheduleID, scheduleID, projectID)
	}
	return `<div id="task-detail-content"><div data-project-id="` + projectID + `"></div>` + forms.String() + `</div>`
}

func TestScheduleEditQuotedValidSettingPairReferencePreservesAmbiguity(t *testing.T) {
	const scheduleHTML = `<div id="schedule-content">
		<div data-task-id="t-1" data-schedule-id="s-1">Weekly repeat daily report alpha</div>
		<div data-task-id="t-2" data-schedule-id="s-2">Weekly repeat daily report beta</div>
	</div>`
	var puts int
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts++
		}
		_, _ = io.WriteString(w, scheduleHTML)
	})
	m = runLine(t, m, `/schedule edit "Weekly repeat daily report" interval 5`)
	out := stripANSI(transcript(m))
	if puts != 0 || !strings.Contains(out, `"Weekly repeat daily report" is ambiguous`) {
		t.Fatalf("ambiguous edit puts=%d:\n%s", puts, out)
	}
}

func TestScheduleEditSettingsTakePrecedenceOverUnquotedTitleText(t *testing.T) {
	const scheduleHTML = `<div id="schedule-content">
		<div data-task-id="t-1" data-schedule-id="s-1">Weekly</div>
		<div data-task-id="t-2" data-schedule-id="s-2">Weekly repeat daily</div>
	</div>`
	var detailPath, putPath string
	var gotForm url.Values
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/schedule":
			_, _ = io.WriteString(w, scheduleHTML)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/tasks/"):
			detailPath = r.URL.Path
			_, _ = io.WriteString(w, scheduleEditDetailForIDs("p1", "s-1"))
		case r.Method == http.MethodPut:
			putPath = r.URL.Path
			_ = r.ParseForm()
			gotForm = r.PostForm
			w.WriteHeader(http.StatusNoContent)
		}
	})
	m = runLine(t, m, "/schedule edit Weekly repeat daily interval 5")
	if detailPath != "/tasks/t-1" || putPath != "/schedules/s-1" {
		t.Fatalf("setting precedence detail=%q put=%q:\n%s", detailPath, putPath, stripANSI(transcript(m)))
	}
	if gotForm.Get("repeat_type") != "daily" || gotForm.Get("repeat_interval") != "5" {
		t.Fatalf("repeat form = %v, want every 5 days", gotForm)
	}
}

func TestScheduleEditRepeatRetainsEnabledContextReset(t *testing.T) {
	var gotForm url.Values
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/schedule":
			_, _ = io.WriteString(w, selScheduleHTML)
		case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1":
			_, _ = io.WriteString(w, scheduleEditDetail("p1"))
		case r.Method == http.MethodPut && r.URL.Path == "/schedules/s-1":
			_ = r.ParseForm()
			gotForm = r.PostForm
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	m = runLine(t, m, "/schedule edit s-1 repeat weekly")
	if got := gotForm.Get("clear_context_on_start"); got != "true" {
		t.Fatalf("clear_context_on_start = %q, want true; form = %#v", got, gotForm)
	}
	if got := gotForm.Get("repeat_type"); got != "weekly" {
		t.Fatalf("repeat_type = %q, want weekly; form = %#v", got, gotForm)
	}
}

func TestScheduleEditHourlyAliasUsesBackendHours(t *testing.T) {
	var gotForm url.Values
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/schedule":
			_, _ = io.WriteString(w, selScheduleHTML)
		case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1":
			_, _ = io.WriteString(w, scheduleEditDetail("p1"))
		case r.Method == http.MethodPut && r.URL.Path == "/schedules/s-2":
			_ = r.ParseForm()
			gotForm = r.PostForm
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	m = runLine(t, m, "/schedule edit s-2 repeat hourly")
	if gotForm.Get("repeat_type") != "hours" || gotForm.Get("repeat_interval") != "3" {
		t.Fatalf("hourly edit form = %v, want backend hours with preserved interval", gotForm)
	}
	if out := stripANSI(transcript(m)); !strings.Contains(out, "updated schedule s-2") {
		t.Fatalf("hourly edit success missing:\n%s", out)
	}
}

func TestScheduleEditResolvesNonFirstCardAndPreservesOmittedSettings(t *testing.T) {
	var gotForm url.Values
	var scheduleGETs int
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/schedule":
			scheduleGETs++
			_, _ = io.WriteString(w, selScheduleHTML)
		case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1":
			if r.URL.Query().Get("project_id") != "p1" || r.URL.Query().Get("tab") != "schedules" {
				t.Errorf("detail query = %s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, scheduleEditDetail("p1"))
		case r.Method == http.MethodPut && r.URL.Path == "/schedules/s-2":
			if r.URL.Query().Get("project_id") != "p1" {
				t.Errorf("mutation project = %q", r.URL.Query().Get("project_id"))
			}
			_ = r.ParseForm()
			gotForm = r.PostForm
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
		}
	})
	m = runLine(t, m, "/schedule edit s-2 run-at 2026-04-05T11:45 repeat hours clear-context true")
	want := url.Values{"run_at": {"2026-04-05T11:45"}, "repeat_type": {"hours"}, "repeat_interval": {"3"}, "clear_context_on_start": {"true"}}
	if !reflect.DeepEqual(gotForm, want) {
		t.Fatalf("form = %#v, want %#v", gotForm, want)
	}
	if scheduleGETs != 2 {
		t.Fatalf("schedule GETs = %d, want resolution and refresh", scheduleGETs)
	}
	if out := stripANSI(transcript(m)); !strings.Contains(out, "updated schedule s-2") {
		t.Fatalf("success output missing:\n%s", out)
	}
}

func TestScheduleEditRejectsMalformedEarlierOptionsBeforeRequests(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{name: "timestamp", line: "/schedule edit s-1 run-at bad interval 5", want: "run time must use"},
		{name: "repeat", line: "/schedule edit s-1 repeat yearly interval 5", want: `unknown repeat type "yearly"`},
		{name: "interval", line: "/schedule edit s-1 interval 0 repeat daily", want: "repeat interval must be between"},
		{name: "boolean", line: "/schedule edit s-1 clear-context maybe interval 5", want: "clear-context must be true or false"},
		{name: "duplicate", line: "/schedule edit s-1 repeat daily repeat weekly interval 5", want: "usage"},
		{name: "surplus", line: "/schedule edit s-1 repeat daily surplus interval 5", want: "usage"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, nil)
			m = runLine(t, m, tc.line)
			if calls := rec.all(); calls != "" {
				t.Fatalf("malformed command made requests:\n%s", calls)
			}
			if out := stripANSI(transcript(m)); !strings.Contains(out, tc.want) {
				t.Fatalf("validation output missing %q:\n%s", tc.want, out)
			}
		})
	}
}

func TestScheduleEditRejectsMalformedEarlierOptionsForTitleBeforeDetailOrMutation(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{name: "timestamp", line: "/schedule edit Nightly run-at bad interval 5", want: "run time must use"},
		{name: "repeat", line: "/schedule edit Nightly repeat yearly interval 5", want: `unknown repeat type "yearly"`},
		{name: "interval", line: "/schedule edit Nightly interval 0 repeat daily", want: "repeat interval must be between"},
		{name: "boolean", line: "/schedule edit Nightly clear-context maybe interval 5", want: "clear-context must be true or false"},
		{name: "duplicate", line: "/schedule edit Nightly repeat daily repeat weekly interval 5", want: "usage"},
		{name: "surplus", line: "/schedule edit Nightly repeat daily surplus interval 5", want: "usage"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, nil)
			m = runLine(t, m, tc.line)
			if calls := rec.all(); calls != "" {
				t.Fatalf("malformed title edit made requests:\n%s", calls)
			}
			if out := stripANSI(transcript(m)); !strings.Contains(out, tc.want) {
				t.Fatalf("validation output missing %q:\n%s", tc.want, out)
			}
		})
	}
}

func TestScheduleEditExactIDWinsAcrossCandidateBoundaries(t *testing.T) {
	const scheduleHTML = `<div id="schedule-content">
		<div data-task-id="t-1" data-schedule-id="s-1">Canonical ID target</div>
		<div data-task-id="t-2" data-schedule-id="s-2">s-1 repeat daily</div>
	</div>`
	var detailPath, putPath string
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/schedule":
			_, _ = io.WriteString(w, scheduleHTML)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/tasks/"):
			detailPath = r.URL.Path
			_, _ = io.WriteString(w, scheduleEditDetailForIDs("p1", "s-1"))
		case r.Method == http.MethodPut:
			putPath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		}
	})
	m = runLine(t, m, "/schedule edit s-1 repeat daily interval 5")
	if detailPath != "/tasks/t-1" || putPath != "/schedules/s-1" {
		t.Fatalf("exact-ID precedence detail=%q put=%q:\n%s", detailPath, putPath, stripANSI(transcript(m)))
	}
}

func TestScheduleEditPreservesEarlierCandidateAmbiguity(t *testing.T) {
	const scheduleHTML = `<div id="schedule-content">
		<div data-task-id="t-1" data-schedule-id="s-1">Weekly alpha</div>
		<div data-task-id="t-2" data-schedule-id="s-2">Weekly beta</div>
	</div>`
	var detailGets, puts int
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/schedule":
			_, _ = io.WriteString(w, scheduleHTML)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/tasks/"):
			detailGets++
		case r.Method == http.MethodPut:
			puts++
		}
	})
	m = runLine(t, m, "/schedule edit Weekly repeat daily interval 5")
	out := stripANSI(transcript(m))
	if detailGets != 0 || puts != 0 || !strings.Contains(out, `"Weekly" is ambiguous`) {
		t.Fatalf("candidate ambiguity detail GETs=%d puts=%d:\n%s", detailGets, puts, out)
	}
}

func TestScheduleEditRejectsInvalidSyntaxBeforeRequests(t *testing.T) {
	cases := []string{
		"/schedule unknown", "/schedule list extra", "/schedule edit s-1", "/schedule edit s-1 run-at bad",
		"/schedule edit s-1 repeat yearly", "/schedule edit s-1 interval 0", "/schedule edit s-1 interval 366",
		"/schedule edit s-1 clear-context maybe", "/schedule edit s-1 repeat daily surplus",
	}
	for _, line := range cases {
		t.Run(line, func(t *testing.T) {
			m, rec := dispatchModel(t, nil)
			m = runLine(t, m, line)
			if rec.all() != "" {
				t.Fatalf("invalid command made requests:\n%s", rec.all())
			}
			if !strings.Contains(stripANSI(transcript(m)), "usage") && !strings.Contains(stripANSI(transcript(m)), "must") && !strings.Contains(stripANSI(transcript(m)), "unknown repeat") {
				t.Fatalf("validation error missing:\n%s", transcript(m))
			}
		})
	}
}

func TestScheduleEditRejectsForeignProjectAndKeepsMutationSuccessOnRefreshFailure(t *testing.T) {
	for _, foreign := range []bool{true, false} {
		t.Run(fmt.Sprintf("foreign=%t", foreign), func(t *testing.T) {
			var puts, scheduleGETs int
			m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/schedule":
					scheduleGETs++
					if !foreign && scheduleGETs > 1 {
						http.Error(w, "refresh failed", http.StatusInternalServerError)
						return
					}
					_, _ = io.WriteString(w, selScheduleHTML)
				case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1":
					project := "p1"
					if foreign {
						project = "p2"
					}
					_, _ = io.WriteString(w, scheduleEditDetail(project))
				case r.Method == http.MethodPut:
					puts++
					w.WriteHeader(http.StatusNoContent)
				}
			})
			m = runLine(t, m, "/schedule edit s-2 interval 4")
			out := stripANSI(transcript(m))
			if foreign {
				if puts != 0 || !strings.Contains(out, "belongs to project") {
					t.Fatalf("foreign result puts=%d:\n%s", puts, out)
				}
				return
			}
			if puts != 1 || !strings.Contains(out, "updated schedule s-2") || strings.Contains(out, "refresh failed") {
				t.Fatalf("refresh failure result puts=%d:\n%s", puts, out)
			}
		})
	}
}

func TestScheduleEditJSONSuccessIsStableWhenRefreshFails(t *testing.T) {
	oldJSON := jsonMode
	jsonMode = true
	defer func() { jsonMode = oldJSON }()
	var scheduleGETs int
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/schedule":
			scheduleGETs++
			if scheduleGETs > 1 {
				http.Error(w, "refresh failed", http.StatusInternalServerError)
				return
			}
			_, _ = io.WriteString(w, selScheduleHTML)
		case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1":
			_, _ = io.WriteString(w, scheduleEditDetail("p1"))
		case r.Method == http.MethodPut && r.URL.Path == "/schedules/s-2":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	m = runLine(t, m, "/schedule edit s-2 interval 4 clear-context true")
	out := stripANSI(transcript(m))
	for _, want := range []string{`"id":"s-2"`, `"task_id":"t-1"`, `"project_id":"p1"`, `"run_at":"2026-02-03T10:30"`, `"repeat_type":"weekly"`, `"repeat_interval":4`, `"clear_context_on_start":true`} {
		if !strings.Contains(out, want) {
			t.Fatalf("JSON output missing %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "refresh failed") {
		t.Fatalf("successful JSON mutation exposed refresh failure:\n%s", out)
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

func TestResolvePersonalityReferenceTiers(t *testing.T) {
	const personalitiesHTML = `<div id="personality-section" data-selected-personality="release_coach">
		<div data-personality-key="" data-personality-name="Base" data-personality-is-preset="true"></div>
		<div data-personality-key="release_coach" data-personality-name="Release Coach" data-personality-is-preset="false"></div>
		<div data-personality-key="quiet_mode" data-personality-name="Quiet Builder" data-personality-is-preset="false"></div>
		<div data-personality-key="archive_mode" data-personality-name="Archive Specialist" data-personality-is-preset="false"></div>
		<div data-personality-key="review_one" data-personality-name="Review One" data-personality-is-preset="false"></div>
		<div data-personality-key="review_two" data-personality-name="Review Two" data-personality-is-preset="false"></div>
	</div>`
	var catalogRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/personality" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		catalogRequests++
		if got := r.URL.Query().Get("project_id"); got != "project-two" {
			t.Errorf("personality catalog project_id = %q, want project-two", got)
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(personalitiesHTML))
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		ref       string
		wantKey   string
		wantError string
	}{
		{name: "exact key", ref: "RELEASE_COACH", wantKey: "release_coach"},
		{name: "exact name", ref: "release coach", wantKey: "release_coach"},
		{name: "unique prefix", ref: "quiet", wantKey: "quiet_mode"},
		{name: "unique substring", ref: "pecial", wantKey: "archive_mode"},
		{name: "ambiguous prefix", ref: "review", wantError: "ambiguous"},
		{name: "unknown", ref: "missing", wantError: "nothing matches"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolvePersonality(context.Background(), c, "project-two", tc.ref)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("resolvePersonality(%q) error = %v, want %q", tc.ref, err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolvePersonality(%q) failed: %v", tc.ref, err)
			}
			if got.Key != tc.wantKey {
				t.Fatalf("resolvePersonality(%q) key = %q, want %q", tc.ref, got.Key, tc.wantKey)
			}
		})
	}
	if catalogRequests != len(cases) {
		t.Fatalf("personality catalog requests = %d, want %d", catalogRequests, len(cases))
	}
}

func TestPersonalityDirectActionsRejectBaseEntry(t *testing.T) {
	const personalitiesHTML = `<div id="personality-section" data-selected-personality="">
		<div data-personality-key="" data-personality-name="Base" data-personality-is-preset="true"></div>
		<div data-personality-key="custom" data-personality-name="Custom" data-personality-is-preset="false"></div>
	</div>`
	cases := []struct {
		name         string
		line         string
		mutationVerb string
		errorText    string
		confirm      bool
	}{
		{
			name:         "edit",
			line:         "/personality edit Base | Updated Base | description | A valid prompt that is long enough",
			mutationVerb: "PUT",
			errorText:    "base personality cannot be edited",
		},
		{
			name:         "delete",
			line:         "/personality delete Base",
			mutationVerb: "DELETE",
			errorText:    "base personality cannot be deleted",
			confirm:      true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/personality": personalitiesHTML})
			m = runLine(t, m, tc.line)
			if tc.confirm {
				if m.pendingConfirmation == nil {
					t.Fatal("delete did not wait for confirmation")
				}
				m = runLine(t, m, "yes")
			}
			out := stripANSI(transcript(m))
			if !strings.Contains(out, tc.errorText) {
				t.Fatalf("Base %s error missing:\n%s", tc.name, out)
			}
			if strings.Contains(rec.all(), tc.mutationVerb+" /personality/custom") {
				t.Fatalf("Base %s reached a mutation route:\n%s", tc.name, rec.all())
			}
		})
	}
}

func TestPersonalityDirectActionsStopOnCatalogFailure(t *testing.T) {
	cases := []struct {
		name         string
		line         string
		needsConfirm bool
	}{
		{name: "show", line: "/personality show known"},
		{name: "edit", line: "/personality edit known | Updated | description | A valid prompt that is long enough"},
		{name: "set", line: "/personality set known"},
		{name: "delete", line: "/personality delete known", needsConfirm: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var catalogRequests, mutationRequests int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Path == "/personality" {
					catalogRequests++
					if got := r.URL.Query().Get("project_id"); got != "p1" {
						t.Errorf("catalog project_id = %q, want p1", got)
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadGateway)
					_, _ = fmt.Fprint(w, `{"error":"personality catalog unavailable"}`)
					return
				}
				mutationRequests++
				t.Errorf("catalog failure must not reach mutation route: %s %s", r.Method, r.URL.Path)
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

			if tc.needsConfirm {
				m = runLine(t, m, tc.line)
				if m.pendingConfirmation == nil {
					t.Fatal("delete did not wait for confirmation")
				}
				m = runLine(t, m, "yes")
			} else {
				m = runLine(t, m, tc.line)
			}
			if catalogRequests != 1 {
				t.Fatalf("catalog requests = %d, want 1", catalogRequests)
			}
			if mutationRequests != 0 {
				t.Fatalf("mutation requests = %d, want 0", mutationRequests)
			}
			if out := stripANSI(transcript(m)); !strings.Contains(out, "personality catalog unavailable") {
				t.Fatalf("catalog failure missing from transcript:\n%s", out)
			}
		})
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
	m = runLine(t, m, "/alerts read-all")
	if !rec.saw("POST", "/alerts/read-all") {
		t.Errorf("calls:\n%s", rec.all())
	}
	m = runLine(t, m, "/alerts clear")
	if m.pendingConfirmation == nil || rec.saw("DELETE", "/alerts") {
		t.Fatalf("clear must remain confirmation-gated: pending=%v calls:\n%s", m.pendingConfirmation != nil, rec.all())
	}
	m = runLine(t, m, "yes")
	if !rec.saw("DELETE", "/alerts") {
		t.Errorf("clear did not retain its all-alert delete route:\n%s", rec.all())
	}
}

func TestAlertReadBulkResolvesPaginatedSelectionAndRefreshes(t *testing.T) {
	const firstPage = `<div data-card-pagination-root data-card-pagination-card-selector="[data-alert-id]" data-card-pagination-key="data-alert-id" data-card-pagination-has-more="true">
		<div data-alert-id="a-first" data-alert-scroll-anchor="a-first"><p class="font-semibold">First alert</p></div>
		<div data-alert-id="a-unselected" data-alert-scroll-anchor="a-unselected"><p class="font-semibold">Not selected</p></div>
	</div>`
	const laterPage = `<div data-card-pagination-root data-card-pagination-card-selector="[data-alert-id]" data-card-pagination-key="data-alert-id" data-card-pagination-has-more="false">
		<div data-alert-id="a-later" data-alert-scroll-anchor="a-later"><p class="font-semibold">Later page alert</p></div>
	</div>`
	const refreshedPage = `<div data-alert-id="a-remaining" data-alert-scroll-anchor="a-remaining"><p class="font-semibold">Remaining alert</p></div>`

	var listRequests, mutations int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/alerts":
			if r.URL.Query().Get("project_id") != "p1" {
				t.Errorf("alert list lost project scope: %s", r.URL.RequestURI())
			}
			listRequests++
			w.Header().Set("Content-Type", "text/html")
			switch listRequests {
			case 1:
				_, _ = io.WriteString(w, firstPage)
			case 2:
				if r.URL.Query().Get("card_page") != "1" {
					t.Errorf("continuation query = %s", r.URL.RawQuery)
				}
				w.Header().Set("X-OpenVibely-Card-Page-Has-More", "false")
				_, _ = io.WriteString(w, laterPage)
			case 3:
				_, _ = io.WriteString(w, refreshedPage)
			default:
				t.Fatalf("unexpected alert list request %d", listRequests)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/alerts/read-bulk":
			mutations++
			if r.URL.Query().Get("project_id") != "p1" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				t.Errorf("bulk read request = %s %s content-type=%q", r.Method, r.URL.RequestURI(), r.Header.Get("Content-Type"))
			}
			var payload struct {
				IDs []string `json:"ids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode bulk read body: %v", err)
			}
			if want := []string{"a-first", "a-later"}; !reflect.DeepEqual(payload.IDs, want) {
				t.Errorf("bulk read IDs = %#v, want %#v", payload.IDs, want)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"updated":2}`)
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
	m.selectedID, m.selectedName = "p1", "demo"
	m = runLineWithFollowUp(t, m, `/alerts read-bulk a-first "Later page alert"`)

	if mutations != 1 || listRequests != 3 {
		t.Fatalf("requests = lists %d mutations %d, want 3 and 1; transcript:\n%s", listRequests, mutations, stripANSI(transcript(m)))
	}
	if out := stripANSI(transcript(m)); !strings.Contains(out, "marked 2 alerts read") || !strings.Contains(out, "Remaining alert") {
		t.Fatalf("bulk read output did not report count and refreshed list:\n%s", out)
	}
}

func TestAlertDeleteBulkConfirmsAndCapturesSelectedIDs(t *testing.T) {
	const selected = `<div data-alert-id="a-one" data-alert-scroll-anchor="a-one"><p class="font-semibold">First selected</p></div>
		<div data-alert-id="a-two" data-alert-scroll-anchor="a-two"><p class="font-semibold">Second selected</p></div>`
	const changed = `<div data-alert-id="a-rebound" data-alert-scroll-anchor="a-rebound"><p class="font-semibold">Changed after confirmation</p></div>`
	const refreshed = `<div data-alert-id="a-three" data-alert-scroll-anchor="a-three"><p class="font-semibold">Remaining</p></div>`
	var lists, deletes int
	catalog := selected
	mutationStarted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/alerts":
			lists++
			w.Header().Set("Content-Type", "text/html")
			if mutationStarted {
				_, _ = io.WriteString(w, refreshed)
			} else {
				_, _ = io.WriteString(w, catalog)
			}
		case r.Method == http.MethodDelete && r.URL.Path == "/alerts/bulk":
			deletes++
			mutationStarted = true
			if r.URL.Query().Get("project_id") != "p1" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				t.Errorf("bulk delete request = %s content-type=%q", r.URL.RequestURI(), r.Header.Get("Content-Type"))
			}
			var payload struct {
				IDs []string `json:"ids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode bulk delete body: %v", err)
			}
			if want := []string{"a-one", "a-two"}; !reflect.DeepEqual(payload.IDs, want) {
				t.Errorf("bulk delete IDs = %#v, want captured selection %#v", payload.IDs, want)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"deleted":2}`)
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
	m.selectedID, m.selectedName = "p1", "demo"

	const command = `/alerts delete-bulk Fir "Second selected"`
	const prompt = `Delete 2 selected alerts: "First selected" (a-one), "Second selected" (a-two)? Type 'yes' to confirm or Esc to cancel.`
	m = runLine(t, m, command)
	if m.pendingConfirmation == nil || deletes != 0 || m.pendingConfirmation.message != prompt {
		got := ""
		if m.pendingConfirmation != nil {
			got = m.pendingConfirmation.message
		}
		t.Fatalf("bulk delete confirmation = %q, want %q; deletes=%d", got, prompt, deletes)
	}
	m = runLine(t, m, "no")
	if m.pendingConfirmation != nil || deletes != 0 {
		t.Fatalf("cancelled bulk delete mutated or stayed pending: pending=%v deletes=%d", m.pendingConfirmation != nil, deletes)
	}

	m = runLine(t, m, command)
	if m.pendingConfirmation == nil || m.pendingConfirmation.message != prompt {
		t.Fatalf("bulk delete confirmation = %#v, want %q", m.pendingConfirmation, prompt)
	}
	// The list may change after the user reviews the resolved targets. Confirming
	// must retain those targets rather than resolving the refs again.
	catalog = changed
	m = runLine(t, m, "yes")
	if deletes != 1 || lists != 3 {
		t.Fatalf("confirmed bulk delete requests = lists %d deletes %d, want 3 and 1", lists, deletes)
	}
	if out := stripANSI(transcript(m)); !strings.Contains(out, "deleted 2 alerts") || !strings.Contains(out, "Remaining") {
		t.Fatalf("bulk delete output did not report count and refreshed list:\n%s", out)
	}
}

func TestAlertBulkActionsRejectInvalidOrForeignReferencesBeforeMutation(t *testing.T) {
	const alertsHTML = `<div data-alert-id="a-own" data-alert-scroll-anchor="a-own"><p class="font-semibold">Own</p></div>
		<div data-alert-id="a-one" data-alert-scroll-anchor="a-one"><p class="font-semibold">Duplicate</p></div>
		<div data-alert-id="a-two" data-alert-scroll-anchor="a-two"><p class="font-semibold">duplicate</p></div>`
	cases := []struct {
		name   string
		action string
		refs   string
		want   string
	}{
		{name: "duplicate read", action: "read-bulk", refs: "a-own Own", want: "selected more than once"},
		{name: "duplicate delete", action: "delete-bulk", refs: "a-own Own", want: "selected more than once"},
		{name: "ambiguous read", action: "read-bulk", refs: "Duplicate", want: "is ambiguous"},
		{name: "missing read", action: "read-bulk", refs: "missing-alert", want: "nothing matches"},
		{name: "foreign delete", action: "delete-bulk", refs: "foreign-alert", want: "nothing matches"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutations := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/alerts":
					w.Header().Set("Content-Type", "text/html")
					_, _ = io.WriteString(w, alertsHTML)
				case (r.Method == http.MethodPost && r.URL.Path == "/alerts/read-bulk") || (r.Method == http.MethodDelete && r.URL.Path == "/alerts/bulk"):
					mutations++
					w.WriteHeader(http.StatusOK)
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
			m.selectedID, m.selectedName = "p1", "demo"
			m = runLine(t, m, "/alerts "+tc.action+" "+tc.refs)
			if mutations != 0 || m.pendingConfirmation != nil {
				t.Fatalf("invalid selection mutated or prompted: mutations=%d pending=%v", mutations, m.pendingConfirmation != nil)
			}
			if out := strings.ToLower(stripANSI(transcript(m))); !strings.Contains(out, strings.ToLower(tc.want)) {
				t.Fatalf("invalid selection output missing %q:\n%s", tc.want, out)
			}
		})
	}
}

func TestAlertBulkMutationFailureDoesNotRefreshOrReportSuccess(t *testing.T) {
	const alertsHTML = `<div data-alert-id="a-one" data-alert-scroll-anchor="a-one"><p class="font-semibold">One</p></div>`
	var lists, mutations int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/alerts":
			lists++
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, alertsHTML)
		case r.Method == http.MethodPost && r.URL.Path == "/alerts/read-bulk":
			mutations++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"bulk read rejected"}`)
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
	m.selectedID, m.selectedName = "p1", "demo"
	m = runLineWithFollowUp(t, m, "/alerts read-bulk a-one")
	if mutations != 1 || lists != 1 {
		t.Fatalf("failed bulk read requests = lists %d mutations %d, want 1 each", lists, mutations)
	}
	if out := stripANSI(transcript(m)); !strings.Contains(out, "bulk read rejected") || strings.Contains(out, "marked 1 alerts read") {
		t.Fatalf("backend failure output = %s", out)
	}
}

func TestAlertBulkMalformedMutationResponsesDoNotRefreshOrReportSuccess(t *testing.T) {
	const alertsHTML = `<div data-alert-id="a-one" data-alert-scroll-anchor="a-one"><p class="font-semibold">One</p></div>`
	const refreshedHTML = `<div data-alert-id="a-remaining" data-alert-scroll-anchor="a-remaining"><p class="font-semibold">Remaining</p></div>`
	endpoints := []struct {
		name       string
		action     string
		method     string
		path       string
		countName  string
		successMsg string
	}{
		{name: "read", action: "read-bulk", method: http.MethodPost, path: "/alerts/read-bulk", countName: "updated", successMsg: "marked 0 alerts read"},
		{name: "delete", action: "delete-bulk", method: http.MethodDelete, path: "/alerts/bulk", countName: "deleted", successMsg: "deleted 0 alerts"},
	}
	responses := []struct {
		name      string
		body      func(string) string
		wantError string
	}{
		{name: "missing count", body: func(string) string { return `{}` }, wantError: "missing required"},
		{name: "null count", body: func(field string) string { return fmt.Sprintf(`{%q:null}`, field) }, wantError: "missing required"},
		{name: "negative count", body: func(field string) string { return fmt.Sprintf(`{%q:-1}`, field) }, wantError: "must not be negative"},
		{name: "error object with count", body: func(field string) string { return fmt.Sprintf(`{"error":"bulk mutation rejected",%q:0}`, field) }, wantError: "received an error object"},
		{name: "explicit zero", body: func(field string) string { return fmt.Sprintf(`{%q:0}`, field) }},
	}

	for _, endpoint := range endpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			for _, response := range responses {
				t.Run(response.name, func(t *testing.T) {
					var lists, mutations int
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						switch {
						case r.Method == http.MethodGet && r.URL.Path == "/alerts":
							lists++
							w.Header().Set("Content-Type", "text/html")
							if lists == 1 {
								_, _ = io.WriteString(w, alertsHTML)
								return
							}
							_, _ = io.WriteString(w, refreshedHTML)
						case r.Method == endpoint.method && r.URL.Path == endpoint.path:
							mutations++
							w.Header().Set("Content-Type", "application/json")
							_, _ = io.WriteString(w, response.body(endpoint.countName))
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
					m.selectedID, m.selectedName = "p1", "demo"
					line := "/alerts " + endpoint.action + " a-one"
					if endpoint.action == "delete-bulk" {
						m = runLine(t, m, line)
						if m.pendingConfirmation == nil {
							t.Fatal("bulk delete did not request confirmation")
						}
						m = runLine(t, m, "yes")
					} else {
						m = runLineWithFollowUp(t, m, line)
					}

					out := stripANSI(transcript(m))
					if response.wantError != "" {
						if mutations != 1 || lists != 1 {
							t.Fatalf("malformed response requests = lists %d mutations %d, want 1 each", lists, mutations)
						}
						if !strings.Contains(out, response.wantError) || strings.Contains(out, endpoint.successMsg) || strings.Contains(out, "Remaining") {
							t.Fatalf("malformed response output = %q", out)
						}
						return
					}
					if mutations != 1 || lists != 2 {
						t.Fatalf("zero count requests = lists %d mutations %d, want 2 and 1", lists, mutations)
					}
					if !strings.Contains(out, endpoint.successMsg) || !strings.Contains(out, "Remaining") {
						t.Fatalf("zero count output = %q", out)
					}
				})
			}
		})
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
		{name: "show", line: "/alerts show a-1"},
		{name: "read_all", line: "/alerts read-all"},
		{name: "clear", line: "/alerts clear", checkConfirmation: true},
		{name: "approve", line: "/alerts approve a-1"},
		{name: "reject", line: "/alerts reject a-1"},
		{name: "dismiss", line: "/alerts dismiss a-1"},
		{name: "read", line: "/alerts read a-1"},
		{name: "read_bulk", line: "/alerts read-bulk a-1 a-2"},
		{name: "delete", line: "/alerts delete a-1", checkConfirmation: true},
		{name: "delete_bulk", line: "/alerts delete-bulk a-1 a-2", checkConfirmation: true},
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

func TestModelsInteractiveAddMasksCredentialsRefreshesAndSupportsDefault(t *testing.T) {
	secret := "interactive-model-api-key"
	var postForm url.Values
	var postCount, listCount, defaultCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /models":
			postCount++
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			postForm = r.PostForm
			w.WriteHeader(http.StatusOK)
		case "GET /models":
			listCount++
			_, _ = io.WriteString(w, `<div data-model-id="m-openai" data-model-name="OpenAI" data-model-provider="openai" data-model-model="gpt-4o"></div>`)
		case "POST /models/m-openai/set-default":
			defaultCount++
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
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
	m.selectedID, m.selectedName = "p1", "demo"
	m = runLine(t, m, "/models add")
	if m.modelWizard == nil {
		t.Fatalf("model wizard did not start:\n%s", transcript(m))
	}
	for _, value := range []string{"openai", "OpenAI", "gpt-4o", "api_key", secret} {
		m = runLine(t, m, value)
	}
	if postCount != 1 || listCount != 1 || m.modelWizard != nil || m.input.EchoMode != textinput.EchoNormal {
		t.Fatalf("model wizard did not complete cleanly: POST/list=%d/%d wizard=%v echo=%v\n%s", postCount, listCount, m.modelWizard != nil, m.input.EchoMode, transcript(m))
	}
	for key, want := range map[string]string{
		"name": "OpenAI", "provider": "openai", "model": "gpt-4o", "openai_auth_type": "api_key", "api_key": secret,
	} {
		if got := postForm.Get(key); got != want {
			t.Errorf("form[%q] = %q, want %q", key, got, want)
		}
	}
	if strings.Contains(m.View(), secret) || strings.Contains(transcript(m), secret) {
		t.Fatal("model API key appeared in terminal output")
	}
	for _, item := range m.history {
		if strings.Contains(item, secret) {
			t.Fatalf("model API key appeared in command history: %q", item)
		}
	}
	m = runLine(t, m, "/models default OpenAI")
	if defaultCount != 1 || listCount != 3 {
		t.Fatalf("default flow did not use refreshed model list: defaults=%d lists=%d", defaultCount, listCount)
	}
}

func TestModelsInteractiveAddValidatesOllamaAndBackendErrorsWithoutLeaks(t *testing.T) {
	t.Run("ollama endpoint", func(t *testing.T) {
		var postForm url.Values
		var postCount int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method + " " + r.URL.Path {
			case "POST /models":
				postCount++
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				postForm = r.PostForm
				w.WriteHeader(http.StatusOK)
			case "GET /models":
				_, _ = io.WriteString(w, `<div data-model-id="m-ollama" data-model-name="Local Ollama" data-model-provider="ollama" data-model-model="llama3"></div>`)
			default:
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
			}
		}))
		defer srv.Close()
		c, err := client.New(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		m := New(c)
		m = runLine(t, m, "/models add")
		for _, value := range []string{"ollama", "Local Ollama", "llama3", "http://localhost:11434"} {
			m = runLine(t, m, value)
		}
		if postCount != 1 || postForm.Get("ollama_base_url") != "http://localhost:11434" || postForm.Get("api_key") != "" {
			t.Fatalf("unexpected Ollama setup form: %v", postForm)
		}

		m = runLine(t, m, "/models add")
		for _, value := range []string{"ollama", "Broken Ollama", "llama3", "http:/missing-host"} {
			m = runLine(t, m, value)
		}
		if postCount != 1 || m.modelWizard == nil || !strings.Contains(transcript(m), "--endpoint must be an absolute HTTP(S) URL") {
			t.Fatalf("malformed endpoint was not rejected before POST:\n%s", transcript(m))
		}
	})

	t.Run("backend validation", func(t *testing.T) {
		secret := "interactive-backend-reflected-secret"
		var postCount, listCount int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method + " " + r.URL.Path {
			case "POST /models":
				postCount++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"rejected `+secret+`"}`)
			case "GET /models":
				listCount++
				_, _ = io.WriteString(w, `<div></div>`)
			default:
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
			}
		}))
		defer srv.Close()
		c, err := client.New(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		m := New(c)
		m = runLine(t, m, "/models add")
		for _, value := range []string{"openai", "OpenAI", "gpt-4o", "api_key", secret} {
			m = runLine(t, m, value)
		}
		if postCount != 1 || listCount != 0 {
			t.Fatalf("backend validation POST/list counts = %d/%d, want 1/0", postCount, listCount)
		}
		if strings.Contains(m.View(), secret) || strings.Contains(transcript(m), secret) {
			t.Fatalf("backend-reflected secret leaked:\n%s", transcript(m))
		}
	})

	t.Run("inline secret is redacted", func(t *testing.T) {
		apiKey := "inline-model-secret"
		endpointSecret := "inline-endpoint-secret"
		m, rec := dispatchModel(t, nil)
		m = runLine(t, m, `/models add openai OpenAI gpt-4o --api-key "`+apiKey+`"`)
		m = runLine(t, m, `/models add ollama Local llama3 --endpoint "http://user:`+endpointSecret+`@localhost:11434"`)
		for _, secret := range []string{apiKey, endpointSecret} {
			if strings.Contains(m.View(), secret) || strings.Contains(transcript(m), secret) {
				t.Fatalf("inline model secret %q appeared in terminal output", secret)
			}
			for _, item := range m.history {
				if strings.Contains(item, secret) {
					t.Fatalf("inline model secret %q appeared in history: %q", secret, item)
				}
			}
		}
		if calls := rec.all(); calls != "" {
			t.Fatalf("rejected inline secret made requests:\n%s", calls)
		}
	})

	t.Run("API-key stdin misuse is redacted", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			line func(secret string) string
		}{
			{
				name: "normal command",
				line: func(secret string) string {
					return `/models add openai OpenAI gpt-4o --api-key-stdin ` + secret
				},
			},
			{
				name: "unmatched quote",
				line: func(secret string) string {
					return `/models add openai OpenAI gpt-4o --api-key-stdin "` + secret
				},
			},
			{
				name: "empty stdin assignment",
				line: func(secret string) string {
					return `/models add openai OpenAI gpt-4o --api-key-stdin= ` + secret
				},
			},
			{
				name: "empty API key assignment",
				line: func(secret string) string {
					return `/models add openai OpenAI gpt-4o --api-key= ` + secret
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				secret := "api-key-stdin-misuse-" + strings.ReplaceAll(tc.name, " ", "-")
				m, rec := dispatchModel(t, nil)
				m = runLine(t, m, tc.line(secret))
				if strings.Contains(m.View(), secret) || strings.Contains(transcript(m), secret) {
					t.Fatalf("API-key stdin misuse exposed the secret:\n%s", transcript(m))
				}
				for _, item := range m.history {
					if strings.Contains(item, secret) {
						t.Fatalf("API-key stdin misuse exposed the secret in history: %q", item)
					}
				}
				if calls := rec.all(); calls != "" {
					t.Fatalf("API-key stdin misuse made requests:\n%s", calls)
				}
			})
		}
	})
	t.Run("empty quoted sensitive operands are redacted", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			line func(secret string) string
		}{
			{
				name: "API key stdin empty double quoted operand",
				line: func(secret string) string {
					return `/models add openai OpenAI gpt-4o --api-key-stdin "" ` + secret
				},
			},
			{
				name: "API key stdin empty single quoted operand",
				line: func(secret string) string {
					return `/models add openai OpenAI gpt-4o --api-key-stdin '' ` + secret
				},
			},
			{
				name: "API key empty double quoted operand",
				line: func(secret string) string {
					return `/models add openai OpenAI gpt-4o --api-key "" ` + secret
				},
			},
			{
				name: "API key empty single quoted operand",
				line: func(secret string) string {
					return `/models add openai OpenAI gpt-4o --api-key '' ` + secret
				},
			},
			{
				name: "endpoint empty double quoted operand",
				line: func(secret string) string {
					return `/models add ollama Local llama3 --endpoint "" http://user:` + secret + `@localhost:11434`
				},
			},
			{
				name: "endpoint empty single quoted operand",
				line: func(secret string) string {
					return `/models add ollama Local llama3 --endpoint '' http://user:` + secret + `@localhost:11434`
				},
			},
			{
				name: "API key stdin repeated empty double quoted operands",
				line: func(secret string) string {
					return `/models add openai OpenAI gpt-4o --api-key-stdin "" "" ` + secret
				},
			},
			{
				name: "API key stdin repeated empty single quoted operands",
				line: func(secret string) string {
					return `/models add openai OpenAI gpt-4o --api-key-stdin '' '' ` + secret
				},
			},
			{
				name: "API key stdin empty double quoted operand before API key",
				line: func(secret string) string {
					return `/models add openai OpenAI gpt-4o --api-key-stdin "" --api-key ` + secret
				},
			},
			{
				name: "API key stdin empty single quoted operand before API key",
				line: func(secret string) string {
					return `/models add openai OpenAI gpt-4o --api-key-stdin '' --api-key ` + secret
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				secret := "empty-quoted-sensitive-operand-" + strings.ReplaceAll(tc.name, " ", "-")
				m, rec := dispatchModel(t, nil)
				m = runLine(t, m, tc.line(secret))
				if !strings.Contains(transcript(m), "unsupported models add option") {
					t.Fatalf("empty quoted operand did not report its parse error:\n%s", transcript(m))
				}
				if strings.Contains(m.View(), secret) || strings.Contains(transcript(m), secret) {
					t.Fatalf("empty quoted sensitive operand exposed the secret:\n%s", transcript(m))
				}
				for _, item := range m.history {
					if strings.Contains(item, secret) {
						t.Fatalf("empty quoted sensitive operand exposed the secret in history: %q", item)
					}
				}
				if calls := rec.all(); calls != "" {
					t.Fatalf("empty quoted sensitive operand made requests:\n%s", calls)
				}
			})
		}
	})
	t.Run("direct adjacent sensitive options are redacted", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			line    func(secret string) string
			wantErr string
		}{
			{
				name: "API key stdin before API key",
				line: func(secret string) string {
					return `/models add openai OpenAI gpt-4o --api-key-stdin --api-key ` + secret
				},
				wantErr: "unsupported models add option",
			},
			{
				name: "endpoint before API key",
				line: func(secret string) string {
					return `/models add ollama Local llama3 --endpoint --api-key ` + secret
				},
				wantErr: "--endpoint must be an absolute HTTP(S) URL",
			},
			{
				name: "empty API key stdin assignment before API key",
				line: func(secret string) string {
					return `/models add openai OpenAI gpt-4o --api-key-stdin= --api-key ` + secret
				},
				wantErr: "unsupported models add option",
			},
			{
				name: "empty endpoint assignment before API key",
				line: func(secret string) string {
					return `/models add ollama Local llama3 --endpoint= --api-key ` + secret
				},
				wantErr: "unsupported models add option",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				secret := "adjacent-sensitive-option-" + strings.ReplaceAll(tc.name, " ", "-")
				m, rec := dispatchModel(t, nil)
				m = runLine(t, m, tc.line(secret))
				if !strings.Contains(transcript(m), tc.wantErr) {
					t.Fatalf("adjacent sensitive options did not report local rejection:\n%s", transcript(m))
				}
				if strings.Contains(m.View(), secret) || strings.Contains(transcript(m), secret) {
					t.Fatalf("adjacent sensitive options exposed the secret:\n%s", transcript(m))
				}
				for _, item := range m.history {
					if strings.Contains(item, secret) {
						t.Fatalf("adjacent sensitive options exposed the secret in history: %q", item)
					}
				}
				if calls := rec.all(); calls != "" {
					t.Fatalf("adjacent sensitive options made requests:\n%s", calls)
				}
			})
		}
	})
	t.Run("inline sensitive assignments redact their tail", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			line    func(secret string) string
			wantErr string
		}{
			{
				name: "API key assignment",
				line: func(secret string) string {
					return `/models add openai OpenAI gpt-4o --api-key=placeholder ` + secret
				},
				wantErr: "unsupported models add option",
			},
			{
				name: "API key stdin assignment",
				line: func(secret string) string {
					return `/models add openai OpenAI gpt-4o --api-key-stdin=placeholder ` + secret
				},
				wantErr: "unsupported models add option",
			},
			{
				name: "endpoint assignment",
				line: func(secret string) string {
					return `/models add ollama Local llama3 --endpoint=http://localhost:11434 ` + secret
				},
				wantErr: "unsupported models add option",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				secret := "inline-sensitive-assignment-" + strings.ReplaceAll(tc.name, " ", "-")
				m, rec := dispatchModel(t, nil)
				m = runLine(t, m, tc.line(secret))
				if !strings.Contains(transcript(m), tc.wantErr) {
					t.Fatalf("inline sensitive assignment did not report local rejection:\n%s", transcript(m))
				}
				if strings.Contains(m.View(), secret) || strings.Contains(transcript(m), secret) {
					t.Fatalf("inline sensitive assignment exposed the secret:\n%s", transcript(m))
				}
				for _, item := range m.history {
					if strings.Contains(item, secret) {
						t.Fatalf("inline sensitive assignment exposed the secret in history: %q", item)
					}
				}
				if calls := rec.all(); calls != "" {
					t.Fatalf("inline sensitive assignment made requests:\n%s", calls)
				}
			})
		}
	})
	t.Run("separated sensitive options redact their tail", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			line func(secret string) string
		}{
			{
				name: "API key placeholder",
				line: func(secret string) string {
					return `/models add openai OpenAI gpt-4o --api-key placeholder ` + secret
				},
			},
			{
				name: "API key stdin placeholder",
				line: func(secret string) string {
					return `/models add openai OpenAI gpt-4o --api-key-stdin placeholder ` + secret
				},
			},
			{
				name: "endpoint placeholder",
				line: func(secret string) string {
					return `/models add ollama Local llama3 --endpoint http://localhost:11434 ` + secret
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				secret := "separated-sensitive-option-" + strings.ReplaceAll(tc.name, " ", "-")
				m, rec := dispatchModel(t, nil)
				m = runLine(t, m, tc.line(secret))
				if !strings.Contains(transcript(m), "unsupported models add option") {
					t.Fatalf("separated sensitive option did not report local rejection:\n%s", transcript(m))
				}
				if strings.Contains(m.View(), secret) || strings.Contains(transcript(m), secret) {
					t.Fatalf("separated sensitive option exposed the secret:\n%s", transcript(m))
				}
				for _, item := range m.history {
					if strings.Contains(item, secret) {
						t.Fatalf("separated sensitive option exposed the secret in history: %q", item)
					}
				}
				if calls := rec.all(); calls != "" {
					t.Fatalf("separated sensitive option made requests:\n%s", calls)
				}
			})
		}
	})

	t.Run("malformed quoted option API key is redacted", func(t *testing.T) {
		secret := "quoted-option-unmatched-quote-model-secret"
		m, rec := dispatchModel(t, nil)
		m = runLine(t, m, `/models add openai OpenAI gpt-4o "--api-key" "`+secret)
		if !strings.Contains(transcript(m), "unmatched double quote") {
			t.Fatalf("malformed command did not report its parse error:\n%s", transcript(m))
		}
		if strings.Contains(m.View(), secret) || strings.Contains(transcript(m), secret) {
			t.Fatalf("malformed command exposed the API key:\n%s", transcript(m))
		}
		for _, item := range m.history {
			if strings.Contains(item, secret) {
				t.Fatalf("malformed command exposed the API key in history: %q", item)
			}
		}
		if calls := rec.all(); calls != "" {
			t.Fatalf("malformed command made requests:\n%s", calls)
		}
	})

	t.Run("malformed embedded-quote option API key is redacted", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			option    string
			quoteName string
		}{
			{name: "API key double quote", option: `--api-key"`, quoteName: "double"},
			{name: "API key stdin double quote", option: `--api-key-stdin"`, quoteName: "double"},
			{name: "API key single quote", option: `--api-key'`, quoteName: "single"},
			{name: "API key stdin single quote", option: `--api-key-stdin'`, quoteName: "single"},
			{name: "API key embedded double quote", option: `--api"-key`, quoteName: "double"},
			{name: "API key stdin embedded double quote", option: `--api"-key-stdin`, quoteName: "double"},
			{name: "API key embedded single quote", option: `--api'-key`, quoteName: "single"},
			{name: "API key stdin embedded single quote", option: `--api'-key-stdin`, quoteName: "single"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				secret := "embedded-quote-model-secret-" + strings.ReplaceAll(tc.name, " ", "-")
				m, rec := dispatchModel(t, nil)
				m = runLine(t, m, "/models add openai OpenAI gpt-4o "+tc.option+"="+secret)
				if !strings.Contains(transcript(m), "unmatched "+tc.quoteName+" quote") {
					t.Fatalf("malformed command did not report its parse error:\n%s", transcript(m))
				}
				if strings.Contains(m.View(), secret) || strings.Contains(transcript(m), secret) {
					t.Fatalf("malformed embedded-quote option exposed the API key:\n%s", transcript(m))
				}
				for _, item := range m.history {
					if strings.Contains(item, secret) {
						t.Fatalf("malformed embedded-quote option exposed the API key in history: %q", item)
					}
				}
				if calls := rec.all(); calls != "" {
					t.Fatalf("malformed embedded-quote option made requests:\n%s", calls)
				}
			})
		}
	})

	t.Run("malformed quoted root API key is redacted", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			root string
		}{
			{name: "double slash root", root: `//models`},
			{name: "empty double quoted fragment before slash root", root: `""/models`},
			{name: "empty single quoted fragment before slash root", root: `''/models`},
			{name: "slash then double quoted root", root: `/"models"`},
			{name: "slash then single quoted root", root: `/'models'`},
			{name: "spaced double quoted root", root: `/ "models"`},
			{name: "spaced single quoted root", root: `/ 'models'`},
			{name: "fully double quoted root", root: `"/models"`},
			{name: "fully single quoted root", root: `'/models'`},
			{name: "double quoted root fragment", root: `"/m"odels`},
			{name: "single quoted root fragment", root: `'/m'odels`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				secret := "quoted-root-unmatched-quote-model-secret-" + strings.ReplaceAll(tc.name, " ", "-")
				m, rec := dispatchModel(t, nil)
				m = runLine(t, m, tc.root+` add openai OpenAI gpt-4o --api-key "`+secret)
				if !strings.Contains(transcript(m), "unmatched double quote") {
					t.Fatalf("malformed command did not report its parse error:\n%s", transcript(m))
				}
				if strings.Contains(m.View(), secret) || strings.Contains(transcript(m), secret) {
					t.Fatalf("malformed quoted-root command exposed the API key:\n%s", transcript(m))
				}
				for _, item := range m.history {
					if strings.Contains(item, secret) {
						t.Fatalf("malformed quoted-root command exposed the API key in history: %q", item)
					}
				}
				if calls := rec.all(); calls != "" {
					t.Fatalf("malformed quoted-root command made requests:\n%s", calls)
				}
			})
		}
	})

}

func TestModelsListFilterOutputDistinguishesMatchesFromNoMatches(t *testing.T) {
	const modelsHTML = `<div data-model-id="m-1" data-model-name="Sonnet"
		data-model-provider="Anthropic" data-model-model="claude-sonnet-4"></div>`
	cases := []struct {
		name      string
		line      string
		want      string
		forbidden []string
	}{
		{name: "name match", line: "/models sonNET", want: "Sonnet"},
		{name: "model match", line: "/models CLAUDE-SONNET", want: "Sonnet"},
		{name: "provider match", line: "/models anthROPIC", want: "Sonnet"},
		{name: "no match", line: "/models Missing", want: `no models match "Missing"`, forbidden: []string{"no models configured", "web UI", "API"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/models": modelsHTML})
			m.selectedID = ""
			m = runLine(t, m, tc.line)
			out := stripANSI(transcript(m))
			if !strings.Contains(out, tc.want) {
				t.Fatalf("output missing %q:\n%s", tc.want, out)
			}
			for _, forbidden := range tc.forbidden {
				if strings.Contains(out, forbidden) {
					t.Errorf("output unexpectedly contains %q:\n%s", forbidden, out)
				}
			}
			if !rec.saw("GET", "/models") || rec.sawQuery("GET /models?") {
				t.Fatalf("filtered model list was not requested globally:\n%s", rec.all())
			}
		})
	}
}

func TestModelsListDoesNotRequireSelectedProject(t *testing.T) {
	const modelsHTML = `<div data-model-id="m-1" data-model-name="Sonnet"
		data-model-provider="anthropic" data-model-model="claude-sonnet-4"></div>`
	states := []struct {
		name     string
		projects []client.Project
	}{
		{name: "zero projects", projects: []client.Project{}},
		{name: "single project", projects: []client.Project{{ID: "p1", Name: "solo"}}},
		{name: "multiple projects", projects: []client.Project{{ID: "p1", Name: "demo"}, {ID: "p2", Name: "other"}}},
	}
	for _, state := range states {
		state := state
		for _, line := range []string{"/models", "/models list", "/models Sonnet"} {
			line := line
			t.Run(state.name+" "+line, func(t *testing.T) {
				m, rec := dispatchModel(t, map[string]string{"/models": modelsHTML})
				m.projects = state.projects
				m.projectsLoaded = true
				m.selectedID = ""
				m.selectedName = ""

				m = runLine(t, m, line)
				out := stripANSI(transcript(m))
				if !strings.Contains(out, "Sonnet") {
					t.Fatalf("expected global model listing for %s:\n%s", line, out)
				}
				if strings.Contains(out, "no project selected") || strings.Contains(out, "multiple projects") {
					t.Fatalf("global model listing required a project for %s:\n%s", line, out)
				}
				if !rec.saw("GET", "/models") {
					t.Fatalf("%s did not request the global model list:\n%s", line, rec.all())
				}
				if rec.sawQuery("GET /models?") {
					t.Fatalf("%s sent query parameters on a global model request; request URLs:\n%s", line, strings.Join(rec.urlsSnapshot(), "\n"))
				}
			})
		}
	}
}

func TestProjectScopedModelsCommandsRequireSelectedProject(t *testing.T) {
	cases := []string{
		"/models capacity",
		"/models edit Sonnet --model claude-sonnet-4-6",
		"/models default Sonnet",
		"/models delete Sonnet",
		"/models edit",
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
	if !rec.saw("POST", "/models/m-1/set-default") || !rec.sawQuery("POST /models/m-1/set-default?project_id=p1") {
		t.Errorf("calls:\n%s\nrequests:\n%s", rec.all(), strings.Join(rec.urlsSnapshot(), "\n"))
	}
}

func TestModelsDelete(t *testing.T) {
	const modelsHTML = `<div data-model-id="m-1" data-model-name="Sonnet"
		data-model-provider="anthropic" data-model-model="claude-sonnet-4"></div>`
	m, rec := dispatchModel(t, map[string]string{"/models": modelsHTML})
	confirmDestructive(t, m, "/models delete Sonnet")
	if !rec.saw("DELETE", "/models/m-1") || !rec.sawQuery("DELETE /models/m-1?project_id=p1") {
		t.Errorf("calls:\n%s\nrequests:\n%s", rec.all(), strings.Join(rec.urlsSnapshot(), "\n"))
	}
}

func TestModelsResolvedActionsMatchAcrossEntryRoutes(t *testing.T) {
	for _, action := range []string{"default", "delete"} {
		for _, entry := range []string{"typed", "picker"} {
			t.Run(action+"/"+entry, func(t *testing.T) {
				m, rec := dispatchModel(t, map[string]string{"/models": selModelsHTML})
				m.selectedID = "project-selected"
				m.selectedName = "selected"

				if entry == "typed" {
					m = runLine(t, m, "/models "+action+" GPT-4o")
				} else {
					m = runLine(t, m, "/models "+action)
					if !m.selectorActive {
						t.Fatalf("picker route did not open selector:\n%s", transcript(m))
					}
					m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
				}

				method, path := http.MethodPost, "/models/mo-1/set-default"
				mutationQuery := "POST /models/mo-1/set-default?project_id=project-selected"
				if action == "delete" {
					method, path = http.MethodDelete, "/models/mo-1"
					mutationQuery = "DELETE /models/mo-1?project_id=project-selected"
					if m.pendingConfirmation == nil {
						t.Fatalf("delete did not wait for confirmation:\n%s", transcript(m))
					}
					wantPrompt := `Delete model "GPT-4o"? Type 'yes' to confirm or Esc to cancel.`
					if entry == "picker" {
						wantPrompt = `Delete model "mo-1"? Type 'yes' to confirm or Esc to cancel.`
					}
					if out := stripANSI(m.View()); !strings.Contains(out, wantPrompt) {
						t.Fatalf("confirmation text missing %q:\n%s", wantPrompt, out)
					}
					if got := rec.count(method, path); got != 0 {
						t.Fatalf("delete requests before confirmation = %d, want 0", got)
					}
					m = runLine(t, m, "yes")
				}

				if got := rec.count(method, path); got != 1 {
					t.Fatalf("%s requests = %d, want 1; calls:\n%s", path, got, rec.all())
				}
				if !rec.sawQuery(mutationQuery) {
					t.Fatalf("mutation lost selected project; requests:\n%s", strings.Join(rec.urlsSnapshot(), "\n"))
				}
				if got := rec.count(http.MethodGet, "/models"); got != 2 {
					t.Fatalf("model list requests = %d, want resolution/selection and refresh; calls:\n%s", got, rec.all())
				}
				urls := rec.urlsSnapshot()
				for _, request := range urls {
					if strings.HasPrefix(request, "GET /models") && !strings.Contains(request, "project_id=project-selected") {
						t.Fatalf("model list request lost selected project: %s", request)
					}
				}
				out := stripANSI(transcript(m))
				for _, want := range []string{action + ": GPT-4o", "Claude", "claude-sonnet"} {
					if !strings.Contains(out, want) {
						t.Errorf("successful output missing %q:\n%s", want, out)
					}
				}
				if strings.Contains(out, "error:") {
					t.Fatalf("successful action reported an error:\n%s", out)
				}
			})
		}
	}
}

func TestModelsResolvedActionMutationFailuresSkipSuccessAndRefresh(t *testing.T) {
	for _, action := range []string{"default", "delete"} {
		for _, entry := range []string{"typed", "picker"} {
			t.Run(action+"/"+entry, func(t *testing.T) {
				var gets, mutations int
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.Method == http.MethodGet && r.URL.Path == "/models":
						gets++
						w.Header().Set("Content-Type", "text/html")
						_, _ = w.Write([]byte(selModelsHTML))
					case r.URL.Path == "/models/mo-1/set-default" || r.URL.Path == "/models/mo-1":
						mutations++
						http.Error(w, "model mutation failed", http.StatusInternalServerError)
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

				if entry == "typed" {
					m = runLine(t, m, "/models "+action+" GPT-4o")
				} else {
					m = runLine(t, m, "/models "+action)
					m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
				}
				if action == "delete" {
					m = runLine(t, m, "yes")
				}

				if mutations != 1 {
					t.Fatalf("mutation requests = %d, want 1", mutations)
				}
				if gets != 1 {
					t.Fatalf("model GETs = %d, want only resolution/selection; refresh ran after failure", gets)
				}
				out := stripANSI(transcript(m))
				if !strings.Contains(out, "server error (500)") {
					t.Fatalf("mutation error missing:\n%s", out)
				}
				if strings.Contains(out, action+": GPT-4o") {
					t.Fatalf("mutation failure reported success:\n%s", out)
				}
			})
		}
	}
}

func TestModelsResolvedActionRefreshFailuresReturnOnlySuccess(t *testing.T) {
	for _, action := range []string{"default", "delete"} {
		for _, entry := range []string{"typed", "picker"} {
			t.Run(action+"/"+entry, func(t *testing.T) {
				var gets, mutations int
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.Method == http.MethodGet && r.URL.Path == "/models":
						gets++
						if gets > 1 {
							http.Error(w, "refresh failed", http.StatusInternalServerError)
							return
						}
						w.Header().Set("Content-Type", "text/html")
						_, _ = w.Write([]byte(selModelsHTML))
					case r.URL.Path == "/models/mo-1/set-default" || r.URL.Path == "/models/mo-1":
						mutations++
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

				if entry == "typed" {
					m = runLine(t, m, "/models "+action+" GPT-4o")
				} else {
					m = runLine(t, m, "/models "+action)
					m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
				}
				if action == "delete" {
					m = runLine(t, m, "yes")
				}

				if mutations != 1 || gets != 2 {
					t.Fatalf("mutations/GETs = %d/%d, want 1/2", mutations, gets)
				}
				out := stripANSI(transcript(m))
				if !strings.Contains(out, action+": GPT-4o") {
					t.Fatalf("success missing after refresh failure:\n%s", out)
				}
				for _, forbidden := range []string{"refresh failed", "error:", "Claude", "claude-sonnet"} {
					if strings.Contains(out, forbidden) {
						t.Errorf("refresh failure output unexpectedly contains %q:\n%s", forbidden, out)
					}
				}
			})
		}
	}
}

func TestModelsTypedReferenceFailuresAndPickerCancellationDoNotMutate(t *testing.T) {
	const ambiguousModels = `<div>
		<div data-model-id="mo-1" data-model-name="Sonnet Alpha" data-model-provider="anthropic" data-model-model="claude-alpha"></div>
		<div data-model-id="mo-2" data-model-name="Sonnet Beta" data-model-provider="anthropic" data-model-model="claude-beta"></div>
	</div>`
	for _, action := range []string{"default", "delete"} {
		for _, tc := range []struct {
			name string
			ref  string
			want string
		}{
			{name: "ambiguous", ref: "Sonnet", want: "ambiguous"},
			{name: "unknown", ref: "Missing", want: "nothing matches"},
		} {
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				m, rec := dispatchModel(t, map[string]string{"/models": ambiguousModels})
				m = runLine(t, m, "/models "+action+" "+tc.ref)
				if action == "delete" {
					if m.pendingConfirmation == nil {
						t.Fatal("typed delete should confirm before resolving its reference")
					}
					if got := rec.count(http.MethodGet, "/models"); got != 0 {
						t.Fatalf("typed delete resolved before confirmation with %d GETs", got)
					}
					m = runLine(t, m, "yes")
				}
				out := stripANSI(transcript(m))
				if !strings.Contains(out, tc.want) {
					t.Fatalf("reference error missing %q:\n%s", tc.want, out)
				}
				if rec.count(http.MethodPost, "/models/mo-1/set-default") != 0 ||
					rec.count(http.MethodPost, "/models/mo-2/set-default") != 0 ||
					rec.count(http.MethodDelete, "/models/mo-1") != 0 ||
					rec.count(http.MethodDelete, "/models/mo-2") != 0 {
					t.Fatalf("invalid reference mutated a model:\n%s", rec.all())
				}
			})
		}

		t.Run(action+"/picker_cancel", func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/models": selModelsHTML})
			m = runLine(t, m, "/models "+action)
			m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
			if m.selectorActive || m.pendingConfirmation != nil {
				t.Fatal("Esc did not cleanly cancel the model picker")
			}
			if rec.count(http.MethodPost, "/models/mo-1/set-default") != 0 || rec.count(http.MethodDelete, "/models/mo-1") != 0 {
				t.Fatalf("cancelled picker mutated a model:\n%s", rec.all())
			}
		})
	}
}

func TestModelsDeleteConfirmationCancellationDoesNotMutate(t *testing.T) {
	for _, entry := range []string{"typed", "picker"} {
		t.Run(entry, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/models": selModelsHTML})
			if entry == "typed" {
				m = runLine(t, m, "/models delete GPT-4o")
			} else {
				m = runLine(t, m, "/models delete")
				m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
			}
			if m.pendingConfirmation == nil {
				t.Fatal("delete did not enter confirmation mode")
			}
			m = runLine(t, m, "no")
			if m.pendingConfirmation != nil {
				t.Fatal("confirmation cancellation left pending state")
			}
			if got := rec.count(http.MethodDelete, "/models/mo-1"); got != 0 {
				t.Fatalf("cancelled delete requests = %d, want 0", got)
			}
		})
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

func TestResolveAutomationRefPreservesMatchRefPrecedenceAndScope(t *testing.T) {
	automationsHTML := "<div>" +
		automationCardHTML("au-1", "Primary", "active") +
		automationCardHTML("au-2", "AU-1", "paused") +
		automationCardHTML("au-3", "Native SDLC", "active") +
		automationCardHTML("au-4", "Build cleanup", "active") +
		automationCardHTML("au-5", "Run nightly cleanup", "active") +
		"</div>"

	for _, tc := range []struct {
		name string
		ref  string
		id   string
	}{
		{name: "exact ID outranks name", ref: "au-1", id: "au-1"},
		{name: "case insensitive exact name", ref: "native sdlc", id: "au-3"},
		{name: "unique prefix", ref: "build", id: "au-4"},
		{name: "unique substring", ref: "nightly", id: "au-5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/automations": automationsHTML})
			got, err := resolveAutomationRef(context.Background(), m.client, "p1", tc.ref)
			if err != nil {
				t.Fatal(err)
			}
			if got.ID != tc.id {
				t.Fatalf("resolved %q as %q, want %q", tc.ref, got.ID, tc.id)
			}
			if got := rec.count(http.MethodGet, "/automations"); got != 1 {
				t.Fatalf("catalog requests = %d, want 1: %s", got, rec.all())
			}
			if !rec.sawQuery("GET /automations?project_id=p1") {
				t.Fatalf("resolver catalog request lost selected project: %s", rec.urlsSnapshot())
			}
		})
	}
}

func TestResolveAutomationRefFailsWithoutExtraCatalogRequests(t *testing.T) {
	automationsHTML := "<div>" +
		automationCardHTML("au-1", "Native SDLC", "active") +
		automationCardHTML("au-2", "GitHub SDLC", "paused") +
		"</div>"

	for _, tc := range []struct {
		name string
		ref  string
		want string
	}{
		{name: "unknown", ref: "missing", want: "nothing matches"},
		{name: "ambiguous", ref: "SDLC", want: "ambiguous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/automations": automationsHTML})
			_, err := resolveAutomationRef(context.Background(), m.client, "p1", tc.ref)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.want)) {
				t.Fatalf("resolve error = %v, want %q", err, tc.want)
			}
			if got := rec.count(http.MethodGet, "/automations"); got != 1 {
				t.Fatalf("catalog requests = %d, want 1: %s", got, rec.all())
			}
		})
	}
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
			if got := rec.count(http.MethodGet, "/automations"); got != 1 {
				t.Fatalf("reference failure catalog requests = %d, want 1: %s", got, rec.all())
			}
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
		m = runLine(t, m, "/automations run Native")
		if !rec.saw("POST", "/automations/au-1/run-now") {
			t.Errorf("calls:\n%s", rec.all())
		}
		if !strings.Contains(transcript(m), "run: Native SDLC") {
			t.Errorf("expected canonical run status:\n%s", transcript(m))
		}
	})

	t.Run("run-now compatibility alias", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/automations": automationsHTML})
		m = runLine(t, m, "/automations run-now Native")
		if !rec.saw("POST", "/automations/au-1/run-now") {
			t.Errorf("calls:\n%s", rec.all())
		}
		if !strings.Contains(transcript(m), "run: Native SDLC") {
			t.Errorf("legacy alias must use canonical status wording:\n%s", transcript(m))
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

func TestAutomationsPickerAndTypedActionsRenderIdenticalStructuredResults(t *testing.T) {
	automationsHTML := "<div>" +
		automationCardHTML("au-1", "Native SDLC", "active") +
		automationCardHTML("au-2", "GitHub SDLC", "paused") +
		"</div>"

	cases := []struct {
		name          string
		typedAction   string
		pickerAction  string
		backendAction string
		destructive   bool
	}{
		{name: "run", typedAction: "run", pickerAction: "run", backendAction: "run-now"},
		{name: "run now alias", typedAction: "run-now", pickerAction: "run-now", backendAction: "run-now"},
		{name: "pause", typedAction: "pause", pickerAction: "pause", backendAction: "pause"},
		{name: "resume", typedAction: "resume", pickerAction: "resume", backendAction: "resume"},
		{name: "delete", typedAction: "delete", pickerAction: "delete", backendAction: "delete", destructive: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			typed, typedRec := dispatchModel(t, map[string]string{"/automations": automationsHTML})
			typed = runLine(t, typed, "/automations "+tc.typedAction+" au-1")
			if tc.destructive {
				typed = runLine(t, typed, "yes")
			}

			picked, pickerRec := dispatchModel(t, map[string]string{"/automations": automationsHTML})
			picked = runLine(t, picked, "/automations "+tc.pickerAction)
			if !picked.selectorActive {
				t.Fatalf("picker did not open: %s", transcript(picked))
			}
			picked = selKey(t, picked, tea.KeyMsg{Type: tea.KeyEnter})
			if tc.destructive {
				if picked.pendingConfirmation == nil {
					t.Fatalf("picker delete did not require confirmation: %s", transcript(picked))
				}
				picked = runLine(t, picked, "yes")
			}

			for name, rec := range map[string]*recorder{"typed": typedRec, "picker": pickerRec} {
				if got := rec.count(http.MethodPost, "/automations/au-1/"+tc.backendAction); got != 1 {
					t.Fatalf("%s action calls = %d, want 1: %s", name, got, rec.all())
				}
				if !rec.sawQuery("POST /automations/au-1/" + tc.backendAction + "?project_id=p1") {
					t.Fatalf("%s action lost project scope: %s", name, rec.urlsSnapshot())
				}
			}

			typedResult := typed.log[len(typed.log)-1]
			pickerResult := picked.log[len(picked.log)-1]
			if typedResult.role != "result" || pickerResult.role != "result" {
				t.Fatalf("expected result entries, typed=%+v picker=%+v", typedResult, pickerResult)
			}
			if typedResult.text != pickerResult.text {
				t.Fatalf("action output differs by entry route:\ntyped:  %q\npicker: %q", typedResult.text, pickerResult.text)
			}
			if !strings.Contains(pickerResult.text, "STATE") {
				t.Fatalf("picker refresh is not a structured automation list: %q", pickerResult.text)
			}
			if tc.backendAction == "run-now" && !strings.HasPrefix(pickerResult.text, "run: Native SDLC") {
				t.Fatalf("run status is not canonical: %q", pickerResult.text)
			}
		})
	}
}

func TestAutomationActionPickerSingleItemAutoSelectionUsesStructuredRefresh(t *testing.T) {
	automationsHTML := "<div>" + automationCardHTML("au-1", "Native SDLC", "active") + "</div>"
	m, rec := dispatchModel(t, map[string]string{"/automations": automationsHTML})
	m, cmd := typeLine(t, m, "/automations pause")
	for i := 0; cmd != nil && i < 5; i++ {
		msg := cmd()
		if msg == nil {
			break
		}
		next, follow := m.Update(msg)
		m = next.(Model)
		cmd = follow
	}

	if m.selectorActive {
		t.Fatalf("single automation should auto-select: %s", transcript(m))
	}
	if got := rec.count(http.MethodPost, "/automations/au-1/pause"); got != 1 {
		t.Fatalf("pause calls = %d, want 1: %s", got, rec.all())
	}
	result := m.log[len(m.log)-1]
	if result.role != "result" || !strings.Contains(result.text, "pause: Native SDLC") || !strings.Contains(result.text, "STATE") {
		t.Fatalf("single-item action output = %+v", result)
	}
}

func TestAutomationsPickerAndTypedActionErrorsDoNotRefreshOrClaimSuccess(t *testing.T) {
	automationsHTML := "<div>" +
		automationCardHTML("au-1", "Native SDLC", "active") +
		automationCardHTML("au-2", "GitHub SDLC", "paused") +
		"</div>"

	for _, action := range []string{"run", "pause", "resume", "delete"} {
		t.Run(action, func(t *testing.T) {
			backendAction := action
			if backendAction == "run" {
				backendAction = "run-now"
			}
			newFailedModel := func(t *testing.T) (Model, *recorder) {
				t.Helper()
				rec := &recorder{}
				m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
					rec.recordURL(r.Method, r.URL.RequestURI())
					switch {
					case r.Method == http.MethodGet && r.URL.Path == "/automations":
						_, _ = w.Write([]byte(automationsHTML))
					case r.Method == http.MethodPost && r.URL.Path == "/automations/au-1/"+backendAction:
						http.Error(w, "mutation failed", http.StatusInternalServerError)
					default:
						w.WriteHeader(http.StatusNotFound)
					}
				})
				return m, rec
			}

			typed, typedRec := newFailedModel(t)
			typed = runLine(t, typed, "/automations "+action+" au-1")
			if action == "delete" {
				typed = runLine(t, typed, "yes")
			}

			picked, pickerRec := newFailedModel(t)
			picked = runLine(t, picked, "/automations "+action)
			picked = selKey(t, picked, tea.KeyMsg{Type: tea.KeyEnter})
			if action == "delete" {
				picked = runLine(t, picked, "yes")
			}

			for name, rec := range map[string]*recorder{"typed": typedRec, "picker": pickerRec} {
				if got := rec.count(http.MethodPost, "/automations/au-1/"+backendAction); got != 1 {
					t.Fatalf("%s mutation calls = %d, want 1: %s", name, got, rec.all())
				}
				if got := rec.count(http.MethodGet, "/automations"); got != 1 {
					t.Fatalf("%s refreshed after a mutation failure: %s", name, rec.all())
				}
			}
			typedResult := typed.log[len(typed.log)-1]
			pickerResult := picked.log[len(picked.log)-1]
			if typedResult.role != "error" || pickerResult.role != "error" || typedResult.text != pickerResult.text {
				t.Fatalf("error result differs by route: typed=%+v picker=%+v", typedResult, pickerResult)
			}
			if strings.Contains(typedResult.text, action+": Native SDLC") {
				t.Fatalf("mutation error claimed success: %q", typedResult.text)
			}
		})
	}
}

func TestAutomationActionRefreshFailurePreservesOnlySuccessStatusForBothRoutes(t *testing.T) {
	automationsHTML := "<div>" +
		automationCardHTML("au-1", "Native SDLC", "active") +
		automationCardHTML("au-2", "GitHub SDLC", "paused") +
		"</div>"
	newRefreshFailureModel := func(t *testing.T) (Model, *recorder) {
		t.Helper()
		var gets int
		rec := &recorder{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec.recordURL(r.Method, r.URL.RequestURI())
			if r.Method == http.MethodGet && r.URL.Path == "/automations" {
				gets++
				if gets > 1 {
					http.Error(w, "refresh failed", http.StatusInternalServerError)
					return
				}
				_, _ = w.Write([]byte(automationsHTML))
				return
			}
			if r.Method == http.MethodPost && r.URL.Path == "/automations/au-1/pause" {
				_, _ = w.Write([]byte(`{}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(srv.Close)
		c, err := client.New(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		m := New(c)
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		m = updated.(Model)
		m.selectedID, m.selectedName = "p1", "demo"
		return m, rec
	}

	typed, typedRec := newRefreshFailureModel(t)
	typed = runLine(t, typed, "/automations pause au-1")
	picked, pickerRec := newRefreshFailureModel(t)
	picked = runLine(t, picked, "/automations pause")
	picked = selKey(t, picked, tea.KeyMsg{Type: tea.KeyEnter})

	for name, rec := range map[string]*recorder{"typed": typedRec, "picker": pickerRec} {
		if got := rec.count(http.MethodPost, "/automations/au-1/pause"); got != 1 {
			t.Fatalf("%s mutation calls = %d, want 1: %s", name, got, rec.all())
		}
		if got := rec.count(http.MethodGet, "/automations"); got != 2 {
			t.Fatalf("%s list calls = %d, want 2: %s", name, got, rec.all())
		}
	}
	typedResult := typed.log[len(typed.log)-1]
	pickerResult := picked.log[len(picked.log)-1]
	if typedResult.role != "result" || pickerResult.role != "result" || typedResult.text != "pause: Native SDLC" || pickerResult.text != typedResult.text {
		t.Fatalf("refresh failure result differs by route: typed=%+v picker=%+v", typedResult, pickerResult)
	}
}

func TestAutomationActionResolutionAndPickerCancellationDoNotMutate(t *testing.T) {
	ambiguousHTML := "<div>" +
		automationCardHTML("au-1", "Deploy A", "active") +
		automationCardHTML("au-2", "Deploy B", "paused") +
		"</div>"
	for _, ref := range []string{"Deploy", "missing"} {
		t.Run("typed "+ref, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/automations": ambiguousHTML})
			m = runLine(t, m, "/automations pause "+ref)
			if rec.count(http.MethodPost, "/automations/au-1/pause") != 0 || rec.count(http.MethodPost, "/automations/au-2/pause") != 0 {
				t.Fatalf("unresolved reference mutated: %s", rec.all())
			}
			if result := m.log[len(m.log)-1]; result.role != "error" {
				t.Fatalf("unresolved reference result = %+v", result)
			}
		})
	}
	for _, action := range []string{"run", "pause", "resume", "delete"} {
		t.Run("cancel picker "+action, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/automations": ambiguousHTML})
			m = runLine(t, m, "/automations "+action)
			if !m.selectorActive {
				t.Fatalf("picker did not open: %s", transcript(m))
			}
			m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
			if m.selectorActive || strings.Contains(rec.all(), "POST /automations/") {
				t.Fatalf("picker cancellation changed automation state: %s", rec.all())
			}
		})
	}
}

func TestAutomationDeleteConfirmationTimingAndCancellationRemainRouteSpecific(t *testing.T) {
	automationsHTML := "<div>" +
		automationCardHTML("au-1", "Native SDLC", "active") +
		automationCardHTML("au-2", "GitHub SDLC", "paused") +
		"</div>"

	typed, typedRec := dispatchModel(t, map[string]string{"/automations": automationsHTML})
	typed = runLine(t, typed, "/automations delete au-1")
	if typed.pendingConfirmation == nil || typedRec.count(http.MethodGet, "/automations") != 0 {
		t.Fatalf("typed delete resolved before confirmation: confirmation=%v calls=%s", typed.pendingConfirmation, typedRec.all())
	}
	typed = runLine(t, typed, "no")
	if typedRec.count(http.MethodPost, "/automations/au-1/delete") != 0 {
		t.Fatalf("cancelled typed delete mutated: %s", typedRec.all())
	}

	picked, pickerRec := dispatchModel(t, map[string]string{"/automations": automationsHTML})
	picked = runLine(t, picked, "/automations delete")
	picked = selKey(t, picked, tea.KeyMsg{Type: tea.KeyEnter})
	if picked.pendingConfirmation == nil || pickerRec.count(http.MethodGet, "/automations") != 1 {
		t.Fatalf("picker delete did not preserve selection-before-confirmation flow: confirmation=%v calls=%s", picked.pendingConfirmation, pickerRec.all())
	}
	picked = runLine(t, picked, "no")
	if pickerRec.count(http.MethodPost, "/automations/au-1/delete") != 0 || pickerRec.count(http.MethodGet, "/automations") != 1 {
		t.Fatalf("cancelled picker delete mutated or refreshed: %s", pickerRec.all())
	}
}

func TestAutomationTypedDeleteCapturesResolvedCanonicalIDAfterConfirmation(t *testing.T) {
	automationsHTML := "<div>" +
		automationCardHTML("au-1", "Native SDLC", "active") +
		automationCardHTML("au-2", "GitHub SDLC", "paused") +
		"</div>"
	m, rec := dispatchModel(t, map[string]string{"/automations": automationsHTML})

	m = runLine(t, m, "/automations delete Native")
	if m.pendingConfirmation == nil || rec.count(http.MethodGet, "/automations") != 0 {
		t.Fatalf("typed delete resolved before confirmation: confirmation=%v calls=%s", m.pendingConfirmation, rec.all())
	}
	m = runLine(t, m, "yes")

	if got := rec.count(http.MethodPost, "/automations/au-1/delete"); got != 1 {
		t.Fatalf("captured delete ID calls = %d, want 1: %s", got, rec.all())
	}
	if rec.count(http.MethodPost, "/automations/au-2/delete") != 0 {
		t.Fatalf("delete rebound to a different automation: %s", rec.all())
	}
	if got := rec.count(http.MethodGet, "/automations"); got != 2 {
		t.Fatalf("catalog requests = %d, want resolution plus refresh: %s", got, rec.all())
	}
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
		{"run", "/automations/au-1/run-now"},
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

func TestAutomationEditExportPreservesCompleteDefinitionWithoutMutation(t *testing.T) {
	const current = "schema_version: 1\nname: Original\ndescription: preserve me\n"
	builder := `<div id="automation-builder"><form id="automation-design-form" action="/automations/au-1/builder?project_id=p1"></form><textarea name="automation_yaml">` + current + `</textarea></div>`
	m, rec := dispatchModel(t, map[string]string{
		"/automations":              `<div>` + automationCardHTML("au-1", "Original", "active") + `</div>`,
		"/automations/au-1/builder": builder,
	})
	path := filepath.Join(t.TempDir(), "automation.yaml")
	m = runLine(t, m, "/automations edit Original --export "+path)
	if got := rec.count(http.MethodGet, "/automations"); got != 1 {
		t.Fatalf("typed export catalog requests = %d, want 1: %s", got, rec.all())
	}
	if got := rec.count(http.MethodGet, "/automations/au-1/builder"); got != 1 {
		t.Fatalf("typed export definition loads = %d, want 1: %s", got, rec.all())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != current {
		t.Fatalf("export = %q", data)
	}
	if rec.count("POST", "/automations/au-1/builder") != 0 {
		t.Fatalf("export mutated backend: %s", rec.all())
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("export mode = %v, err=%v", info.Mode().Perm(), err)
	}
}

func TestAutomationEditRoundTripsThroughPreviewBeforeSave(t *testing.T) {
	const current = "schema_version: 1\nname: Original\nnodes: []\nedges: []\n"
	const edited = "schema_version: 1\nname: Edited\nnodes: []\nedges: []\n"
	builder := `<div id="automation-builder"><form id="automation-design-form" action="/automations/au-1/builder?project_id=p1"></form><textarea name="automation_yaml">` + current + `</textarea></div>`
	m, rec := dispatchModel(t, map[string]string{
		"/automations":              `<div>` + automationCardHTML("au-1", "Original", "active") + `</div>`,
		"/automations/au-1/builder": builder,
	})
	path := filepath.Join(t.TempDir(), "automation.yaml")
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	m = runLine(t, m, "/automations edit Original --file "+path)
	if got := rec.count("GET", "/automations"); got != 1 {
		t.Fatalf("list requests = %d", got)
	}
	if got := rec.count("GET", "/automations/au-1/builder"); got != 1 {
		t.Fatalf("builder GETs = %d", got)
	}
	if got := rec.count("POST", "/automations/au-1/builder"); got != 2 {
		t.Fatalf("builder POSTs = %d, want preview then save\n%s", got, rec.all())
	}
	if !strings.Contains(transcript(m), "updated automation Original") {
		t.Fatalf("output = %s", transcript(m))
	}
	if strings.Contains(transcript(m), edited) {
		t.Fatal("edited definition leaked into terminal output")
	}
}

func TestAutomationEditUnchangedDefinitionDoesNotMutate(t *testing.T) {
	const current = "schema_version: 1\nname: Original\n"
	builder := `<div id="automation-builder"><form id="automation-design-form" action="/automations/au-1/builder?project_id=p1"></form><textarea name="automation_yaml">` + current + `</textarea></div>`
	m, rec := dispatchModel(t, map[string]string{
		"/automations":              `<div>` + automationCardHTML("au-1", "Original", "active") + `</div>`,
		"/automations/au-1/builder": builder,
	})
	path := filepath.Join(t.TempDir(), "automation.yaml")
	if err := os.WriteFile(path, []byte(current), 0o600); err != nil {
		t.Fatal(err)
	}
	m = runLine(t, m, "/automations edit Original --file "+path)
	if rec.count("POST", "/automations/au-1/builder") != 0 {
		t.Fatalf("unchanged edit mutated backend: %s", rec.all())
	}
	if !strings.Contains(transcript(m), "definition unchanged") {
		t.Fatalf("output = %s", transcript(m))
	}
}

func TestAutomationEditInvalidFileFailsBeforeRequests(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{"/automations": `<div></div>`})
	m = runLine(t, m, "/automations edit Original --file "+filepath.Join(t.TempDir(), "missing.yaml"))
	if strings.Contains(rec.all(), "/automations") {
		t.Fatalf("invalid local input made requests: %s", rec.all())
	}
	if !strings.Contains(transcript(m), "read automation definition") {
		t.Fatalf("output = %s", transcript(m))
	}
}

func TestAutomationInteractiveEditLoadsSavesAndDoesNotEchoDefinition(t *testing.T) {
	const current = "schema_version: 1\nname: Original\nprompt: TOP-SECRET-PROMPT\n"
	builder := `<div id="automation-builder"><form id="automation-design-form" action="/automations/au-1/builder?project_id=p1"></form><textarea name="automation_yaml">` + current + `</textarea></div>`
	m, rec := dispatchModel(t, map[string]string{
		"/automations":              `<div>` + automationCardHTML("au-1", "Original", "active") + `</div>`,
		"/automations/au-1/builder": builder,
	})
	m = runLine(t, m, "/automations edit Original")
	if got := rec.count(http.MethodGet, "/automations"); got != 1 {
		t.Fatalf("interactive edit catalog requests = %d, want 1: %s", got, rec.all())
	}
	if got := rec.count(http.MethodGet, "/automations/au-1/builder"); got != 1 {
		t.Fatalf("interactive edit definition loads = %d, want 1: %s", got, rec.all())
	}
	if !m.automationEditActive || m.automationEditor.Value() != current {
		t.Fatalf("editor did not load authoritative definition: active=%v value=%q", m.automationEditActive, m.automationEditor.Value())
	}
	if strings.Contains(transcript(m), "TOP-SECRET-PROMPT") || rec.count("POST", "/automations/au-1/builder") != 0 {
		t.Fatalf("opening editor leaked or mutated: transcript=%q calls=%s", transcript(m), rec.all())
	}
	m.automationEditor.SetValue(strings.Replace(current, "Original", "Edited", 1))
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if m.automationEditActive {
		t.Fatal("editor remained active after successful save")
	}
	if got := rec.count("POST", "/automations/au-1/builder"); got != 2 {
		t.Fatalf("POSTs = %d, want preview then save: %s", got, rec.all())
	}
	if !strings.Contains(transcript(m), "updated automation Original") || strings.Contains(transcript(m), "TOP-SECRET-PROMPT") {
		t.Fatalf("unsafe or missing success output: %q", transcript(m))
	}
}

func TestAutomationInteractiveEditCancellationAndProjectSwitchNeverMutate(t *testing.T) {
	const current = "schema_version: 1\nname: Original\n"
	builder := `<div id="automation-builder"><form id="automation-design-form" action="/automations/au-1/builder?project_id=p1"></form><textarea name="automation_yaml">` + current + `</textarea></div>`
	for _, tc := range []struct {
		name string
		end  func(*Model)
	}{
		{name: "escape", end: func(m *Model) { next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc}); *m = next.(Model) }},
		{name: "project switch", end: func(m *Model) { m.setActiveProject(client.Project{ID: "p2", Name: "other"}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{
				"/automations":              `<div>` + automationCardHTML("au-1", "Original", "active") + `</div>`,
				"/automations/au-1/builder": builder,
			})
			m = runLine(t, m, "/automations edit Original")
			m.automationEditor.SetValue(current + "description: changed\n")
			tc.end(&m)
			if m.automationEditActive || rec.count("POST", "/automations/au-1/builder") != 0 {
				t.Fatalf("cancel/switch retained editor or mutated: active=%v calls=%s", m.automationEditActive, rec.all())
			}
		})
	}
}

func TestAutomationInteractiveEditCancelsInFlightSaveOnTeardown(t *testing.T) {
	for _, tc := range []struct {
		name     string
		teardown func(Model) Model
	}{
		{name: "escape", teardown: func(m Model) Model {
			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			return next.(Model)
		}},
		{name: "ctrl+c", teardown: func(m Model) Model {
			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
			return next.(Model)
		}},
		{name: "project switch", teardown: func(m Model) Model {
			m.setActiveProject(client.Project{ID: "p2", Name: "other"})
			return m
		}},
		{name: "authentication invalidation", teardown: func(m Model) Model {
			m.markAuthRequired()
			return m
		}},
		{name: "global cleanup", teardown: func(m Model) Model {
			m.Cleanup()
			return m
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const current = "schema_version: 1\nname: Original\n"
			builder := `<div id="automation-builder"><form id="automation-design-form" action="/automations/au-1/builder?project_id=p1"></form><textarea name="automation_yaml">` + current + `</textarea></div>`
			started := make(chan struct{})
			cancelled := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseRequest := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseRequest()
			var posts atomic.Int32
			m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/automations":
					_, _ = fmt.Fprint(w, `<div>`+automationCardHTML("au-1", "Original", "active")+`</div>`)
				case r.Method == http.MethodGet && r.URL.Path == "/automations/au-1/builder":
					_, _ = fmt.Fprint(w, builder)
				case r.Method == http.MethodPost && r.URL.Path == "/automations/au-1/builder":
					posts.Add(1)
					close(started)
					select {
					case <-r.Context().Done():
						close(cancelled)
					case <-release:
					}
				}
			})
			m = runLine(t, m, "/automations edit Original")
			m.automationEditor.SetValue(current + "description: changed\n")
			next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
			m = next.(Model)
			if cmd == nil || m.automationEditCancel == nil {
				t.Fatal("Ctrl+S did not start cancellable save command")
			}
			result := make(chan tea.Msg, 1)
			go func() { result <- cmd() }()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("preview request did not start")
			}
			m = tc.teardown(m)
			select {
			case <-result:
			case <-time.After(time.Second):
				releaseRequest()
				t.Fatal("save command did not return after cancellation")
			}
			select {
			case <-cancelled:
			default:
				// net/http does not guarantee that the server observes a disconnected
				// client before the handler returns. The command return above proves
				// the model-owned request context was canceled; release the fixture.
				releaseRequest()
			}
			if posts.Load() != 1 {
				t.Fatalf("POSTs = %d, want only the canceled preview", posts.Load())
			}
			if m.automationEditActive || m.automationEditSaving {
				t.Fatalf("editor state survived teardown: active=%v saving=%v", m.automationEditActive, m.automationEditSaving)
			}
		})
	}
}

func TestAutomationInteractiveEditCancelsInFlightPersistenceOnTeardown(t *testing.T) {
	for _, tc := range []struct {
		name     string
		teardown func(Model) Model
	}{
		{name: "escape", teardown: func(m Model) Model {
			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			return next.(Model)
		}},
		{name: "ctrl+c", teardown: func(m Model) Model {
			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
			return next.(Model)
		}},
		{name: "project switch", teardown: func(m Model) Model {
			m.setActiveProject(client.Project{ID: "p2", Name: "other"})
			return m
		}},
		{name: "authentication invalidation", teardown: func(m Model) Model {
			m.markAuthRequired()
			return m
		}},
		{name: "global cleanup", teardown: func(m Model) Model {
			m.Cleanup()
			return m
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const current = "schema_version: 1\nname: Original\n"
			builder := `<div id="automation-builder"><form id="automation-design-form" action="/automations/au-1/builder?project_id=p1"></form><textarea name="automation_yaml">` + current + `</textarea></div>`
			persistenceStarted := make(chan struct{})
			persistenceCancelled := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseRequest := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseRequest()
			var posts atomic.Int32
			var persisted atomic.Bool
			m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/automations":
					_, _ = fmt.Fprint(w, `<div>`+automationCardHTML("au-1", "Original", "active")+`</div>`)
				case r.Method == http.MethodGet && r.URL.Path == "/automations/au-1/builder":
					_, _ = fmt.Fprint(w, builder)
				case r.Method == http.MethodPost && r.URL.Path == "/automations/au-1/builder":
					posts.Add(1)
					if err := r.ParseForm(); err != nil {
						t.Errorf("parse form: %v", err)
						return
					}
					if r.FormValue("save_changes") != "true" {
						_, _ = fmt.Fprint(w, builder)
						return
					}
					close(persistenceStarted)
					select {
					case <-r.Context().Done():
						close(persistenceCancelled)
					case <-release:
						persisted.Store(true)
					}
				}
			})
			m = runLine(t, m, "/automations edit Original")
			m.automationEditor.SetValue(current + "description: changed\n")
			next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
			m = next.(Model)
			if cmd == nil || m.automationEditCancel == nil {
				t.Fatal("Ctrl+S did not start cancellable save command")
			}
			result := make(chan tea.Msg, 1)
			go func() { result <- cmd() }()
			select {
			case <-persistenceStarted:
			case <-time.After(time.Second):
				t.Fatal("persistence request did not start")
			}
			m = tc.teardown(m)
			select {
			case <-result:
			case <-time.After(time.Second):
				releaseRequest()
				t.Fatal("save command did not return after persistence cancellation")
			}
			select {
			case <-persistenceCancelled:
			case <-time.After(time.Second):
				releaseRequest()
				t.Fatal("persistence handler did not observe cancellation")
			}
			if persisted.Load() || posts.Load() != 2 {
				t.Fatalf("persistence continued after teardown: persisted=%v posts=%d", persisted.Load(), posts.Load())
			}
			if m.automationEditActive || m.automationEditSaving {
				t.Fatalf("editor state survived teardown: active=%v saving=%v", m.automationEditActive, m.automationEditSaving)
			}
		})
	}
}

func TestAutomationInteractiveEditIgnoresStaleLoadMessages(t *testing.T) {
	m, _ := dispatchModel(t, map[string]string{})
	m.automationEditRequestID = 2
	definition := &client.AutomationDefinition{AutomationID: "au-1", ProjectID: "p1", YAML: "TOP-SECRET-STALE"}
	staleRequest := automationEditLoadedMsg{
		sessionGeneration: m.sessionGeneration, projectGeneration: m.projectGeneration,
		projectID: "p1", requestID: 1, automation: client.Automation{ID: "au-1", Name: "Old"}, definition: definition,
	}
	next, _ := m.Update(staleRequest)
	m = next.(Model)
	if m.automationEditActive || strings.Contains(transcript(m), "TOP-SECRET-STALE") {
		t.Fatal("superseded same-project edit load was accepted")
	}
	oldGeneration := m.projectGeneration
	m.setActiveProject(client.Project{ID: "p2", Name: "other"})
	staleProject := staleRequest
	staleProject.requestID = m.automationEditRequestID
	staleProject.projectGeneration = oldGeneration
	next, _ = m.Update(staleProject)
	m = next.(Model)
	if m.automationEditActive || m.selectedID != "p2" || strings.Contains(transcript(m), "TOP-SECRET-STALE") {
		t.Fatal("foreign-project edit load was accepted")
	}
}

func TestAutomationInteractiveEditValidationFailureKeepsDraftWithoutSaving(t *testing.T) {
	const current = "schema_version: 1\nname: Original\n"
	validBuilder := `<div id="automation-builder"><form id="automation-design-form" action="/automations/au-1/builder?project_id=p1"></form><textarea name="automation_yaml">` + current + `</textarea></div>`
	requests := 0
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/automations":
			_, _ = fmt.Fprint(w, `<div>`+automationCardHTML("au-1", "Original", "active")+`</div>`)
		case r.Method == http.MethodGet && r.URL.Path == "/automations/au-1/builder":
			_, _ = fmt.Fprint(w, validBuilder)
		case r.Method == http.MethodPost && r.URL.Path == "/automations/au-1/builder":
			requests++
			_, _ = fmt.Fprint(w, `<div id="automation-builder"><form id="automation-design-form" action="/automations/au-1/builder?project_id=p1"></form><div data-automation-validation-summary><ul><li>trigger is required</li></ul></div><textarea name="automation_yaml">invalid</textarea></div>`)
		}
	})
	m = runLine(t, m, "/automations edit Original")
	m.automationEditor.SetValue("invalid")
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if !m.automationEditActive || m.automationEditor.Value() != "invalid" || requests != 1 {
		t.Fatalf("validation did not preserve draft: active=%v value=%q requests=%d", m.automationEditActive, m.automationEditor.Value(), requests)
	}
	if !strings.Contains(transcript(m), "trigger is required") {
		t.Fatalf("validation error missing: %q", transcript(m))
	}
}

func TestAutomationEditValidationFailureDoesNotSave(t *testing.T) {
	const current = "schema_version: 1\nname: Original\n"
	builder := `<div id="automation-builder"><form id="automation-design-form" action="/automations/au-1/builder?project_id=p1"></form><div data-automation-validation-summary><ul><li>trigger is required</li></ul></div><textarea name="automation_yaml">` + current + `</textarea></div>`
	m, rec := dispatchModel(t, map[string]string{
		"/automations":              `<div>` + automationCardHTML("au-1", "Original", "active") + `</div>`,
		"/automations/au-1/builder": builder,
	})
	path := filepath.Join(t.TempDir(), "automation.yaml")
	if err := os.WriteFile(path, []byte(current+"description: changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m = runLine(t, m, "/automations edit Original --file "+path)
	if got := rec.count("POST", "/automations/au-1/builder"); got != 1 {
		t.Fatalf("POSTs = %d, want preview only: %s", got, rec.all())
	}
	if !strings.Contains(transcript(m), "trigger is required") || strings.Contains(transcript(m), "updated automation") {
		t.Fatalf("output = %s", transcript(m))
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

func TestAlertsWorkflowStatusFilters(t *testing.T) {
	t.Run("combined predicates preserve project scope and later page", func(t *testing.T) {
		requests := 0
		m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/alerts" {
				http.NotFound(w, r)
				return
			}
			requests++
			q := r.URL.Query()
			if q.Get("project_id") != "p-filter" || q.Get("decision_state") != "pending" || q.Get("processing_state") != "unclaimed" {
				t.Errorf("filtered request = %s", r.URL.RequestURI())
			}
			w.Header().Set("Content-Type", "text/html")
			if requests == 1 {
				_, _ = io.WriteString(w, `<div data-card-pagination-root data-card-pagination-card-selector="[data-alert-id]" data-card-pagination-key="data-alert-id" data-card-pagination-has-more="true"><div data-alert-id="first" data-alert-scroll-anchor="first"><p class="font-semibold">First pending alert</p></div></div>`)
				return
			}
			if q.Get("card_page") != "1" || q.Get("page") != "1" || q.Get("page_size") != "50" || q.Get("offset") != "1" {
				t.Errorf("continuation query = %s", r.URL.RawQuery)
			}
			w.Header().Set("X-OpenVibely-Card-Page-Has-More", "false")
			_, _ = io.WriteString(w, `<div data-card-pagination-root data-card-pagination-card-selector="[data-alert-id]" data-card-pagination-key="data-alert-id" data-card-pagination-has-more="false"><div data-alert-id="later" data-alert-scroll-anchor="later"><p class="font-semibold">Later pending alert</p></div></div>`)
		})
		m.selectedID, m.selectedName = "p-filter", "filter demo"
		m = runLine(t, m, "/alerts list --decision-state pending --processing-state unclaimed")
		if out := stripANSI(transcript(m)); !strings.Contains(out, "First pending alert") || !strings.Contains(out, "Later pending alert") {
			t.Fatalf("filtered output = %s", out)
		}
		if requests != 2 {
			t.Fatalf("requests = %d, want 2", requests)
		}
	})

	t.Run("state words remain free text", func(t *testing.T) {
		requests := 0
		m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/alerts" {
				http.NotFound(w, r)
				return
			}
			requests++
			if q := r.URL.Query(); q.Get("project_id") != "p-filter" || q.Get("decision_state") != "" || q.Get("processing_state") != "" {
				t.Errorf("free-text request unexpectedly used workflow predicates: %s", r.URL.RequestURI())
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<div data-alert-id="text-match" data-alert-scroll-anchor="text-match" data-search-text="approved deployment notice"><p class="font-semibold">Deployment notice</p></div><div data-alert-id="other" data-alert-scroll-anchor="other" data-search-text="waiting on review"><p class="font-semibold">Other alert</p></div>`)
		})
		m.selectedID, m.selectedName = "p-filter", "filter demo"
		m = runLine(t, m, "/alerts approved")
		out := stripANSI(transcript(m))
		if !strings.Contains(out, "Deployment notice") || strings.Contains(out, "Other alert") {
			t.Fatalf("free-text output = %s", out)
		}
		if requests != 1 {
			t.Fatalf("requests = %d, want 1", requests)
		}
	})

	t.Run("empty predicate result is explicit", func(t *testing.T) {
		m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/alerts" {
				http.NotFound(w, r)
				return
			}
			q := r.URL.Query()
			if q.Get("project_id") != "p-filter" || q.Get("decision_state") != "dismissed" {
				t.Errorf("empty filtered request = %s", r.URL.RequestURI())
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<div data-card-pagination-root data-card-pagination-card-selector="[data-alert-id]" data-card-pagination-key="data-alert-id" data-card-pagination-has-more="false"></div>`)
		})
		m.selectedID, m.selectedName = "p-filter", "filter demo"
		m = runLine(t, m, "/alerts list --decision-state dismissed")
		if out := stripANSI(transcript(m)); !strings.Contains(out, "no alerts match decision_state=dismissed") {
			t.Fatalf("empty filtered output = %s", out)
		}
	})

	t.Run("invalid predicates make no request", func(t *testing.T) {
		requests := 0
		m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
			requests++
			http.NotFound(w, r)
		})
		m.selectedID = "p-filter"
		m = runLine(t, m, "/alerts list --decision-state not_required")
		if out := stripANSI(transcript(m)); !strings.Contains(out, "valid values: pending, approved, rejected, dismissed") {
			t.Fatalf("invalid predicate output = %s", out)
		}
		if requests != 0 {
			t.Fatalf("invalid predicate sent %d requests", requests)
		}
	})
}

func TestAlertsLaterPageInteractiveCommands(t *testing.T) {
	tests := []struct {
		name        string
		command     string
		wantMethod  string
		wantPath    string
		wantOutput  string
		destructive bool
	}{
		{name: "list filter", command: "/alerts Later", wantOutput: "Later page alert"},
		{name: "show", command: "/alerts show a-later", wantMethod: http.MethodGet, wantPath: "/alerts/a-later/details", wantOutput: "Later page detail"},
		{name: "approve", command: "/alerts approve a-later", wantMethod: http.MethodPost, wantPath: "/alerts/a-later/approve", wantOutput: "approve: Later page alert"},
		{name: "reject", command: "/alerts reject a-later", wantMethod: http.MethodPost, wantPath: "/alerts/a-later/reject", wantOutput: "reject: Later page alert"},
		{name: "dismiss", command: "/alerts dismiss a-later", wantMethod: http.MethodPost, wantPath: "/alerts/a-later/dismiss", wantOutput: "dismiss: Later page alert"},
		{name: "read", command: "/alerts read a-later", wantMethod: http.MethodPost, wantPath: "/alerts/a-later/read", wantOutput: "read: Later page alert"},
		{name: "delete", command: "/alerts delete a-later", wantMethod: http.MethodDelete, wantPath: "/alerts/a-later", wantOutput: "delete: Later page alert", destructive: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.Method+" "+r.URL.Path)
				if r.URL.Query().Get("project_id") != "p1" {
					t.Errorf("request lost project scope: %s", r.URL.RequestURI())
				}
				w.Header().Set("Content-Type", "text/html")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/alerts" && r.URL.Query().Get("card_page") == "1":
					q := r.URL.Query()
					if q.Get("page") != "1" || q.Get("page_size") != "50" || q.Get("offset") != "1" {
						t.Errorf("bad continuation query: %s", r.URL.RawQuery)
					}
					w.Header().Set("X-OpenVibely-Card-Page-Has-More", "false")
					_, _ = io.WriteString(w, `<div data-card-pagination-root data-card-pagination-card-selector="[data-alert-id]" data-card-pagination-key="data-alert-id" data-card-pagination-has-more="false"><div data-alert-id="a-later" data-alert-scroll-anchor="a-later" data-search-text="later page"><p class="font-semibold">Later page alert</p></div></div>`)
				case r.Method == http.MethodGet && r.URL.Path == "/alerts":
					_, _ = io.WriteString(w, `<div data-card-pagination-root data-card-pagination-card-selector="[data-alert-id]" data-card-pagination-key="data-alert-id" data-card-pagination-has-more="true"><div data-alert-id="a-first" data-alert-scroll-anchor="a-first"><p class="font-semibold">First alert</p></div></div>`)
				case r.Method == http.MethodGet && r.URL.Path == "/alerts/a-later/details":
					_, _ = io.WriteString(w, `<div data-alert-detail-loaded><div data-alert-markdown data-raw-content="Later page detail"></div></div>`)
				case r.Method == http.MethodDelete && r.URL.Path == "/alerts/a-later":
					_, _ = io.WriteString(w, `<div data-alert-id="a-first" data-alert-scroll-anchor="a-first"><p class="font-semibold">First alert</p></div>`)
				case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/alerts/a-later/"):
					w.WriteHeader(http.StatusNoContent)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			m := New(c)
			m.selectedID, m.selectedName = "p1", "demo"
			if tt.destructive {
				m = confirmDestructive(t, m, tt.command)
			} else {
				m = runLine(t, m, tt.command)
			}
			if !strings.Contains(stripANSI(transcript(m)), tt.wantOutput) {
				t.Fatalf("output missing %q:\n%s", tt.wantOutput, stripANSI(transcript(m)))
			}
			if tt.wantPath != "" {
				want := tt.wantMethod + " " + tt.wantPath
				found := false
				for _, request := range requests {
					if request == want {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("requests = %#v, want %q", requests, want)
				}
			}
		})
	}
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

func TestAgentsDeleteResolvesBeforeConfirmation(t *testing.T) {
	const agentsHTML = `<div data-agent-id="ag-reviewer" data-agent-key="reviewer" data-agent-name="Code Reviewer"
		data-agent-description="reviews code" data-agent-model="claude" data-agent-scope="project"></div>
	<div data-agent-id="ag-alpha" data-agent-key="alpha" data-agent-name="Review Alpha"
		data-agent-description="reviews releases" data-agent-model="claude" data-agent-scope="project"></div>
	<div data-agent-id="ag-beta" data-agent-key="beta" data-agent-name="Review Beta"
		data-agent-description="reviews releases" data-agent-model="claude" data-agent-scope="project"></div>`

	for _, tc := range []struct {
		name    string
		ref     string
		wantErr string
	}{
		{name: "ambiguous", ref: "review", wantErr: "is ambiguous"},
		{name: "unknown", ref: "missing", wantErr: "nothing matches"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/agents": agentsHTML})
			m = runLine(t, m, "/agents delete "+tc.ref)
			if m.pendingConfirmation != nil {
				t.Fatal("invalid reference opened confirmation")
			}
			if calls := rec.all(); strings.Contains(calls, "DELETE /agents/") {
				t.Fatalf("invalid reference made a DELETE request:\n%s", calls)
			}
			if out := stripANSI(transcript(m)); !strings.Contains(out, tc.wantErr) {
				t.Fatalf("output = %q, want %q", out, tc.wantErr)
			}
		})
	}

	t.Run("partial canonical prompt and cancellation", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/agents": agentsHTML})
		m = runLine(t, m, "/agents delete code")
		if m.pendingConfirmation == nil {
			t.Fatal("unique partial reference did not open confirmation")
		}
		if prompt := m.pendingConfirmation.message; !strings.Contains(prompt, `"Code Reviewer"`) || strings.Contains(prompt, `"code"`) {
			t.Fatalf("confirmation = %q, want canonical agent name", prompt)
		}
		if calls := rec.all(); strings.Contains(calls, "DELETE /agents/") {
			t.Fatalf("delete occurred before confirmation:\n%s", calls)
		}
		m = runLine(t, m, "no")
		if m.pendingConfirmation != nil {
			t.Fatal("cancellation left confirmation active")
		}
		if calls := rec.all(); strings.Contains(calls, "DELETE /agents/") {
			t.Fatalf("cancellation made a DELETE request:\n%s", calls)
		}
	})
}

func TestAgentsDeleteConfirmationUsesCapturedID(t *testing.T) {
	var (
		mu      sync.Mutex
		changed bool
		deleted []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/agents":
			mu.Lock()
			useChanged := changed
			mu.Unlock()
			id := "ag-original"
			if useChanged {
				id = "ag-replacement"
			}
			_, _ = fmt.Fprintf(w, `<div data-agent-id="%s" data-agent-key="reviewer" data-agent-name="Code Reviewer" data-agent-scope="project"></div>`, id)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/agents/"):
			mu.Lock()
			deleted = append(deleted, strings.TrimPrefix(r.URL.Path, "/agents/"))
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
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
	m.selectedID, m.selectedName = "p1", "demo"

	m = runLine(t, m, "/agents delete code")
	if m.pendingConfirmation == nil {
		t.Fatal("delete did not open confirmation")
	}
	mu.Lock()
	changed = true
	mu.Unlock()
	m = runLine(t, m, "yes")

	mu.Lock()
	defer mu.Unlock()
	if len(deleted) != 1 || deleted[0] != "ag-original" {
		t.Fatalf("deleted IDs = %v, want captured ag-original", deleted)
	}
}

func TestAgentsDeleteResolutionRejectsStaleResults(t *testing.T) {
	const agentsHTML = `<div data-agent-id="ag-1" data-agent-key="reviewer" data-agent-name="Code Reviewer" data-agent-scope="project"></div>`
	for _, tc := range []struct {
		name   string
		mutate func(*Model)
	}{
		{name: "project", mutate: func(m *Model) { m.setActiveProject(client.Project{ID: "p2", Name: "other"}) }},
		{name: "session", mutate: func(m *Model) { m.sessionGeneration++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := dispatchModel(t, map[string]string{"/agents": agentsHTML})
			m, cmd := typeLine(t, m, "/agents delete code")
			if cmd == nil {
				t.Fatal("delete did not start target resolution")
			}
			tc.mutate(&m)
			next, _ := m.Update(cmd())
			m = next.(Model)
			if m.pendingConfirmation != nil {
				t.Fatal("stale resolution installed confirmation")
			}
		})
	}
}

func TestAgentsDeleteUsesSelectedProjectScope(t *testing.T) {
	const agentsHTML = `<div data-agent-id="ag-1" data-agent-key="reviewer" data-agent-name="Reviewer"
		data-agent-description="reviews code" data-agent-model="claude" data-agent-scope="project"></div>`

	newModel := func(t *testing.T) (Model, *recorder) {
		t.Helper()
		m, rec := dispatchModel(t, map[string]string{"/agents": agentsHTML})
		m.projects = []client.Project{
			{ID: "p1", Name: "demo"},
			{ID: "p2", Name: "other"},
		}
		m.setActiveProject(client.Project{ID: "p2", Name: "other"})
		return m, rec
	}
	assertProjectScoped := func(t *testing.T, rec *recorder) {
		t.Helper()
		for _, uri := range rec.urlsSnapshot() {
			if strings.HasPrefix(uri, "GET /agents") && !strings.Contains(uri, "project_id=p2") {
				t.Errorf("agent request is not scoped to selected project: %s", uri)
			}
		}
	}

	t.Run("cancellation makes no delete request", func(t *testing.T) {
		m, rec := newModel(t)
		m = runLine(t, m, "/agents delete Reviewer")
		if m.pendingConfirmation == nil {
			t.Fatal("delete did not open confirmation")
		}
		if rec.saw("DELETE", "/agents/ag-1") {
			t.Fatalf("delete occurred before confirmation:\n%s", rec.all())
		}
		m = runLine(t, m, "no")
		if rec.saw("DELETE", "/agents/ag-1") {
			t.Fatalf("cancellation made a DELETE request:\n%s", rec.all())
		}
		assertProjectScoped(t, rec)
	})

	t.Run("yes deletes and refreshes selected project", func(t *testing.T) {
		m, rec := newModel(t)
		m = confirmDestructive(t, m, "/agents delete Reviewer")
		if !rec.saw("DELETE", "/agents/ag-1") {
			t.Fatalf("confirmed deletion did not reach backend:\n%s", rec.all())
		}
		wantDelete := "DELETE /agents/ag-1?project_id=p2"
		foundDelete := false
		for _, uri := range rec.urlsSnapshot() {
			if uri == wantDelete {
				foundDelete = true
				break
			}
		}
		if !foundDelete {
			t.Fatalf("confirmed deletion was not scoped to p2; requests:\n%s", rec.all())
		}
		assertProjectScoped(t, rec)
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

func TestChannelAddConditionalValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "github pat", args: []string{"add", "github", "--auth-mode", "pat"}, want: "requires --pat"},
		{name: "github app", args: []string{"add", "github", "--auth-mode", "app", "--app-id", "1"}, want: "requires --app-id, --app-slug, and --private-key"},
		{name: "github invalid endpoint", args: []string{"edit", "github", "--api-endpoint", "not a URL"}, want: "--api-endpoint must be an absolute HTTP(S) URL"},
		{name: "slack manual", args: []string{"add", "slack", "--client-id", "id", "--client-secret", "secret", "--app-token", "app", "--bot-token-mode", "manual"}, want: "requires --bot-token"},
		{name: "x credentials", args: []string{"add", "x", "--consumer-key", "key", "--consumer-secret", "secret"}, want: "requires --consumer-key, --consumer-secret, --access-token, and --access-token-secret"},
		{name: "x poll interval low", args: []string{"add", "x", "--consumer-key", "key", "--consumer-secret", "secret", "--access-token", "token", "--access-token-secret", "token-secret", "--poll-interval", "14"}, want: "--poll-interval must be between 15 and 300 seconds"},
		{name: "x poll interval high", args: []string{"edit", "x", "--poll-interval", "301"}, want: "--poll-interval must be between 15 and 300 seconds"},
		{name: "unknown email provider", args: []string{"add", "email", "--provider", "other", "--address", "a@example.com", "--password", "secret"}, want: "provider must be one of"},
		{name: "invalid email address", args: []string{"edit", "email", "--address", "not-an-email"}, want: "--address must be a valid email address"},
		{name: "custom email hosts", args: []string{"add", "email", "--provider", "custom", "--address", "a@example.com", "--password", "secret"}, want: "custom Email requires --imap-host and --smtp-host"},
	}
	oldCLI := cliMode
	cliMode = true
	defer func() { cliMode = oldCLI }()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateChannelsArgs(tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestChannelWizardEditTransitionsRequireNewModeFields(t *testing.T) {
	tests := []struct {
		name     string
		channel  string
		original url.Values
		form     url.Values
		step     channelWizardStep
	}{
		{name: "github pat to app private key", channel: "github", original: url.Values{"github_auth_mode": {"pat"}}, form: url.Values{"github_auth_mode": {"app"}, "github_app_id": {"1"}, "github_app_slug": {"slug"}}, step: channelWizardStep{field: "github_app_private_key"}},
		{name: "slack oauth to manual bot token", channel: "slack", original: url.Values{"slack_bot_token_mode": {"oauth"}}, form: url.Values{"slack_bot_token_mode": {"manual"}}, step: channelWizardStep{field: "slack_bot_token"}},
		{name: "email preset to custom imap", channel: "email", original: url.Values{"email_provider": {"gmail"}, "email_imap_host": {"imap.gmail.com"}}, form: url.Values{"email_provider": {"custom"}, "email_imap_host": {"imap.gmail.com"}}, step: channelWizardStep{field: "email_imap_host"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wizard := &channelWizardState{action: "edit", channel: client.Channel{Type: tt.channel}, original: tt.original, form: tt.form, supplied: map[string]bool{}}
			if !channelWizardFieldRequired(wizard, tt.step) {
				t.Fatalf("transition field %s was not required", tt.step.field)
			}
		})
	}
}

func TestNormalizeGitHubPrivateKeyRestoresMultilinePEMFlattenedByTextInput(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	multiline := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}))
	flattened := strings.Join(strings.Fields(multiline), " ")

	normalized, err := normalizeGitHubPrivateKey(flattened)
	if err != nil {
		t.Fatal(err)
	}
	block, rest := pem.Decode([]byte(normalized))
	if block == nil || block.Type != "RSA PRIVATE KEY" || !reflect.DeepEqual(block.Bytes, der) || strings.TrimSpace(string(rest)) != "" {
		t.Fatalf("normalized PEM was not lossless: block=%#v rest=%q", block, rest)
	}
	if !strings.Contains(normalized, "\n") {
		t.Fatalf("normalized PEM remained single-line: %q", normalized)
	}

	_, values, err := parseChannelMutationArgs("add", []string{"add", "github", "--auth-mode", "app", "--app-id", "1", "--app-slug", "terminal-app", "--private-key", flattened})
	if err != nil {
		t.Fatalf("headless parser rejected flattened valid PEM: %v", err)
	}
	if parsed, _ := pem.Decode([]byte(values["github_app_private_key"])); parsed == nil || !reflect.DeepEqual(parsed.Bytes, der) {
		t.Fatal("headless parser did not preserve normalized private key")
	}
}

func TestChannelURLAndEmailValidationAcceptsBrowserValidValues(t *testing.T) {
	oldCLI := cliMode
	cliMode = true
	defer func() { cliMode = oldCLI }()
	for _, args := range [][]string{
		{"edit", "github", "--api-endpoint", "https://github.example.test/api/v3"},
		{"edit", "email", "--address", "bot+tasks@example.com"},
	} {
		if err := validateChannelsArgs(args); err != nil {
			t.Errorf("valid browser-equivalent value rejected for %v: %v", args, err)
		}
	}
}

func TestChannelXMutationArgsUseBackendFieldsAndDefaults(t *testing.T) {
	ch, values, err := parseChannelMutationArgs("add", []string{"add", "x", "--consumer-key", "key", "--consumer-secret", "consumer", "--access-token", "token", "--access-token-secret", "access"})
	if err != nil {
		t.Fatal(err)
	}
	if ch.Type != "x" {
		t.Fatalf("channel = %#v", ch)
	}
	want := map[string]string{
		"x_consumer_key": "key", "x_consumer_secret": "consumer", "x_access_token": "token", "x_access_token_secret": "access",
		"x_poll_interval_seconds": "30", "x_send_responses": "true",
	}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("X values = %#v, want %#v", values, want)
	}
}

func TestChannelEmailProvidersAreAccepted(t *testing.T) {
	oldCLI := cliMode
	cliMode = true
	defer func() { cliMode = oldCLI }()
	for _, provider := range []string{"gmail", "outlook", "yahoo", "fastmail", "icloud"} {
		args := []string{"add", "email", "--provider", provider, "--address", "a@example.com", "--password", "secret"}
		if err := validateChannelsArgs(args); err != nil {
			t.Errorf("provider %s rejected: %v", provider, err)
		}
	}
	custom := []string{"add", "email", "--provider", "custom", "--address", "a@example.com", "--password", "secret", "--imap-host", "imap.example.com", "--smtp-host", "smtp.example.com"}
	if err := validateChannelsArgs(custom); err != nil {
		t.Errorf("custom provider rejected: %v", err)
	}
}

func TestChannelsRejectMalformedArgumentsBeforeSideEffects(t *testing.T) {
	cases := []struct {
		line      string
		wantUsage string
	}{
		{line: "/channels nonsense", wantUsage: "usage: /channels [list|show|add|connect|edit|test|remove|disconnect|access|webhooks]"},
		{line: "/channels list extra", wantUsage: "usage: /channels [list|show|add|connect|edit|test|remove|disconnect|access|webhooks]"},
		{line: "/channels test telegram extra", wantUsage: "nothing matches"},
		{line: "/channels remove slack extra", wantUsage: "nothing matches"},
	}

	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/channels": `<html><body>channels</body></html>`})
			m = runLine(t, m, tc.line)

			out := stripANSI(transcript(m))
			if !strings.Contains(out, tc.wantUsage) {
				t.Fatalf("malformed command output = %q, want canonical usage", out)
			}
			if calls := rec.all(); calls != "" {
				t.Fatalf("malformed command made backend requests:\n%s", calls)
			}
			if m.selectorActive {
				t.Fatal("malformed command opened a selector")
			}
			if m.pendingConfirmation != nil {
				t.Fatal("malformed command opened a confirmation")
			}
		})
	}
}

const structuredChannelsPage = `<div data-channel-type="github" data-search-text="GitHub Connected"></div><div data-channel-type="slack" data-search-text="Slack Configured"></div><div data-channel-type="telegram" data-channel-running="true" data-search-text="Telegram Bot Connected"></div><div data-channel-type="discord" data-search-text="Discord Not configured"></div><div data-channel-type="x" data-search-text="X formerly Twitter mentions posts"><span class="badge badge-success">Connected</span></div><div data-channel-type="email" data-search-text="Email Running"><input name="email_address" value="bot@example.com"></div><form action="/channels/x/configure"><input name="x_poll_interval_seconds" value="30"><input type="checkbox" name="x_send_responses" checked></form>`

type channelAccessTestRow struct {
	id        string
	projectID string
	name      string
	identity  string
}

func channelAccessTestRoute(provider string) (route, container string) {
	switch provider {
	case "telegram":
		return "/channels/telegram/authorized-users", "telegram-authorized-users"
	case "slack":
		return "/channels/slack/authorized-users", "slack-authorized-users"
	case "discord":
		return "/channels/discord/authorized-users", "discord-authorized-users"
	case "email":
		return "/channels/email/authorized-senders", "email-authorized-senders"
	case "x":
		return "/channels/x/authorized-users", "x_config_modal"
	default:
		panic("unsupported test channel access provider")
	}
}

func channelAccessTestPage(provider string, rows ...channelAccessTestRow) string {
	route, container := channelAccessTestRoute(provider)
	var b strings.Builder
	fmt.Fprintf(&b, `<div id="%s">`, container)
	if provider == "x" {
		b.WriteString(`<form action="/channels/x/authorized-users"><input name="project_id" value="p1"></form>`)
	}
	for _, row := range rows {
		projectID := row.projectID
		if projectID == "" {
			projectID = "p1"
		}
		if provider == "x" {
			fmt.Fprintf(&b, `<div data-x-authorized-user-id="%s" data-x-authorized-project-id="%s" data-x-user-id="%s" data-x-username="%s">`, row.id, projectID, row.identity, strings.TrimPrefix(row.name, "@"))
		} else {
			fmt.Fprintf(&b, `<div data-project-id="%s"><div>`, projectID)
		}
		switch provider {
		case "telegram":
			fmt.Fprintf(&b, `<span class="text-sm font-medium">%s</span><span class="text-xs opacity-50">@%s</span><span class="text-xs opacity-50">ID: 987</span>`, row.name, strings.TrimPrefix(row.identity, "@"))
		case "slack", "discord":
			fmt.Fprintf(&b, `<span>%s</span><span>ID: %s</span>`, row.name, row.identity)
		case "email":
			fmt.Fprintf(&b, `<span class="text-sm font-medium truncate">%s</span><span class="text-xs opacity-50 truncate">%s</span>`, row.name, row.identity)
		case "x":
			if row.name != "" {
				fmt.Fprintf(&b, `<span><span>@%s</span> <span class="opacity-60">ID %s</span></span>`, strings.TrimPrefix(row.name, "@"), row.identity)
			} else {
				fmt.Fprintf(&b, `<span><span class="opacity-60">ID %s</span></span>`, row.identity)
			}
		}
		b.WriteString(`<input value="channel-access-backend-secret">`)
		if provider != "x" {
			b.WriteString(`</div>`)
		}
		fmt.Fprintf(&b, `<button hx-delete="%s/%s?project_id=p1">remove</button></div>`, route, row.id)
	}
	b.WriteString(`</div>`)
	return b.String()
}

func TestChannelsCommandsRequireProjectAndPreserveScope(t *testing.T) {
	const channelsPage = structuredChannelsPage
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

func TestChannelAccessTUICommandsValidateScopeProvidersAndCapturedRemoval(t *testing.T) {
	providers := []struct {
		provider string
		identity string
		addInput string
	}{
		{provider: "telegram", identity: "telegram_user", addInput: "@New_User"},
		{provider: "slack", identity: "U12345678", addInput: "u87654321"},
		{provider: "discord", identity: "123456789012345678", addInput: "987654321098765432"},
		{provider: "email", identity: "person@example.com", addInput: " New@Example.COM "},
	}
	for _, tc := range providers {
		t.Run(tc.provider, func(t *testing.T) {
			route, _ := channelAccessTestRoute(tc.provider)
			page := channelAccessTestPage(tc.provider, channelAccessTestRow{id: "row-1", name: "Visible User", identity: tc.identity})

			m, rec := dispatchModel(t, map[string]string{route: page})
			m = runLine(t, m, "/channels access "+tc.provider+" list")
			if !rec.saw(http.MethodGet, route) || !rec.sawQuery("project_id=p1") {
				t.Fatalf("unscoped access list: %s", rec.all())
			}
			if output := transcript(m); !strings.Contains(output, "Visible User") || strings.Contains(output, "channel-access-backend-secret") {
				t.Fatalf("unsafe list output: %s", output)
			}

			m, rec = dispatchModel(t, map[string]string{route: page})
			m = runLine(t, m, "/channels access "+tc.provider+" add "+tc.addInput+` "New User"`)
			if !rec.saw(http.MethodPost, route) || !rec.sawQuery("project_id=p1") {
				t.Fatalf("add did not use scoped %s route: %s", tc.provider, rec.all())
			}
			if tc.provider == "email" && !rec.sawForm("authorized_email_address=new%40example.com") {
				t.Fatalf("email access was not normalized: %v", rec.forms)
			}
			if output := transcript(m); !strings.Contains(output, "authorized "+titleFor(tc.provider)+" access") || strings.Contains(output, "channel-access-backend-secret") {
				t.Fatalf("unsafe add output: %s", output)
			}

			m, rec = dispatchModel(t, map[string]string{route: page})
			m = runLine(t, m, "/channels access "+tc.provider+" remove "+tc.identity)
			if m.pendingConfirmation == nil || rec.saw(http.MethodDelete, route+"/row-1") {
				t.Fatalf("remove did not resolve before confirmation: %s", rec.all())
			}
			m = runLine(t, m, "yes")
			if !rec.saw(http.MethodDelete, route+"/row-1") || !rec.sawQuery("project_id=p1") {
				t.Fatalf("confirmed remove did not use captured scoped target: %s", rec.all())
			}
		})
	}

	t.Run("remove selector and cancellation", func(t *testing.T) {
		route, _ := channelAccessTestRoute("slack")
		page := channelAccessTestPage("slack",
			channelAccessTestRow{id: "row-1", name: "Visible User", identity: "U12345678"},
			channelAccessTestRow{id: "row-2", name: "Other User", identity: "U87654321"})
		m, rec := dispatchModel(t, map[string]string{route: page})
		m = runLine(t, m, "/channels access slack remove")
		if !m.selectorActive || rec.saw(http.MethodDelete, route+"/row-1") {
			t.Fatalf("missing remove reference did not safely open a selector: %s", rec.all())
		}
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = next.(Model)
		if rec.saw(http.MethodDelete, route+"/row-1") {
			t.Fatalf("canceled access removal mutated backend: %s", rec.all())
		}
	})

	t.Run("foreign rows are rejected before confirmation or deletion", func(t *testing.T) {
		route, _ := channelAccessTestRoute("slack")
		page := channelAccessTestPage("slack", channelAccessTestRow{id: "foreign-row", projectID: "other-project", name: "Foreign User", identity: "U12345678"})
		m, rec := dispatchModel(t, map[string]string{route: page})
		m = runLine(t, m, "/channels access slack remove U12345678")
		if m.pendingConfirmation != nil || rec.saw(http.MethodDelete, route+"/foreign-row") || !strings.Contains(transcript(m), "authorized channel access list unavailable") {
			t.Fatalf("foreign access removal was not rejected safely: output=%s calls=%s", transcript(m), rec.all())
		}
	})

	t.Run("owner changes after resolution block deletion", func(t *testing.T) {
		route, _ := channelAccessTestRoute("slack")
		var listCalls, deletes int
		m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != route {
				http.NotFound(w, r)
				return
			}
			if r.Method == http.MethodGet {
				listCalls++
				owner := "p1"
				if listCalls > 1 {
					owner = "other-project"
				}
				_, _ = io.WriteString(w, channelAccessTestPage("slack", channelAccessTestRow{id: "row-1", projectID: owner, name: "Visible User", identity: "U12345678"}))
				return
			}
			if r.Method == http.MethodDelete {
				deletes++
			}
		})
		m = runLine(t, m, "/channels access slack remove U12345678")
		if m.pendingConfirmation == nil || listCalls != 1 {
			t.Fatalf("initial access target was not resolved safely: lists=%d output=%s", listCalls, transcript(m))
		}
		m = runLine(t, m, "yes")
		if deletes != 0 || listCalls != 2 || !strings.Contains(transcript(m), "authorized channel access list unavailable") {
			t.Fatalf("ownership revalidation did not block deletion: lists=%d deletes=%d output=%s", listCalls, deletes, transcript(m))
		}
	})

	t.Run("email display text cannot replace sender identity", func(t *testing.T) {
		route, _ := channelAccessTestRoute("email")
		page := channelAccessTestPage("email", channelAccessTestRow{id: "email-row", name: "support@example.com Team", identity: "real.sender@example.com"})

		m, rec := dispatchModel(t, map[string]string{route: page})
		m = runLine(t, m, "/channels access email add REAL.SENDER@EXAMPLE.COM")
		if rec.saw(http.MethodPost, route) || !strings.Contains(transcript(m), "already exists") {
			t.Fatalf("canonical email duplicate was not rejected: output=%s calls=%s", transcript(m), rec.all())
		}

		m, rec = dispatchModel(t, map[string]string{route: page})
		m = runLine(t, m, "/channels access email remove real.sender@example.com")
		if m.pendingConfirmation == nil || rec.saw(http.MethodDelete, route+"/email-row") {
			t.Fatalf("canonical email removal was not resolved before confirmation: %s", rec.all())
		}
		m = runLine(t, m, "yes")
		if !rec.saw(http.MethodDelete, route+"/email-row") {
			t.Fatalf("canonical email removal used the wrong target: %s", rec.all())
		}
	})
	t.Run("telegram display text cannot replace canonical identity", func(t *testing.T) {
		route, _ := channelAccessTestRoute("telegram")
		const page = `<div id="telegram-authorized-users">
			<div data-project-id="p1"><div><span class="text-sm font-medium">ID: 42</span><span class="text-xs opacity-50">@real_user</span></div><button hx-delete="/channels/telegram/authorized-users/username-row?project_id=p1">remove</button></div>
			<div data-project-id="p1"><div><span class="text-sm font-medium">@misleading</span><span class="text-xs opacity-50">ID: 987</span></div><button hx-delete="/channels/telegram/authorized-users/numeric-row?project_id=p1">remove</button></div>
		</div>`

		for _, ref := range []string{"42", "ID: 42", "misleading"} {
			m, rec := dispatchModel(t, map[string]string{route: page})
			m = runLine(t, m, "/channels access telegram remove "+ref)
			if m.pendingConfirmation != nil || rec.saw(http.MethodDelete, route+"/username-row") || rec.saw(http.MethodDelete, route+"/numeric-row") {
				t.Fatalf("display text %q selected a Telegram row: output=%s calls=%s", ref, transcript(m), rec.all())
			}
		}

		m, rec := dispatchModel(t, map[string]string{route: page})
		m = runLine(t, m, "/channels access telegram remove real_user")
		if m.pendingConfirmation == nil || rec.saw(http.MethodDelete, route+"/username-row") {
			t.Fatalf("canonical Telegram identity was not captured before confirmation: output=%s calls=%s", transcript(m), rec.all())
		}
		if !strings.Contains(m.pendingConfirmation.message, "ID: 42 (@real_user)") {
			t.Fatalf("Telegram confirmation did not show the canonical identity: %q", m.pendingConfirmation.message)
		}
		m = runLine(t, m, "yes")
		if !rec.saw(http.MethodDelete, route+"/username-row") {
			t.Fatalf("canonical Telegram removal used the wrong target: %s", rec.all())
		}
	})

	t.Run("unselected project", func(t *testing.T) {
		m, rec := dispatchModel(t, nil)
		m.selectedID, m.selectedName = "", ""
		m = runLine(t, m, "/channels access telegram list")
		if !strings.Contains(transcript(m), "no project selected") || rec.all() != "" {
			t.Fatalf("unselected access command made requests or hid guidance: %s", transcript(m))
		}
	})

	for _, tc := range []struct {
		line string
		want string
	}{
		{"/channels access github list", "provider"},
		{"/channels access telegram add not-valid!", "numeric user ID or username"},
		{"/channels access telegram add 0", "numeric user ID or username"},
		{"/channels access telegram add 9223372036854775808", "numeric user ID or username"},
		{"/channels access slack add alice", "Slack user ID"},
		{"/channels access discord add username", "numeric user ID"},
		{"/channels access email add not-an-email", "valid email"},
		{"/channels access telegram list surplus", "usage:"},
	} {
		t.Run(tc.line, func(t *testing.T) {
			m, rec := dispatchModel(t, nil)
			m = runLine(t, m, tc.line)
			if !strings.Contains(transcript(m), tc.want) || rec.all() != "" || m.pendingConfirmation != nil {
				t.Fatalf("invalid access command was not rejected locally: output=%s calls=%s", transcript(m), rec.all())
			}
		})
	}
}

func TestChannelAccessXCommandsNormalizeScopeAndProtectTargets(t *testing.T) {
	route, _ := channelAccessTestRoute("x")
	page := channelAccessTestPage("x", channelAccessTestRow{id: "row-1", name: "Alice", identity: "00123"})
	bodies := map[string]string{route: page, "/channels": page}

	t.Run("list and add are safe and scoped", func(t *testing.T) {
		m, rec := dispatchModel(t, bodies)
		m = runLine(t, m, "/channels access x list")
		out := transcript(m)
		if !strings.Contains(out, "@Alice") || !strings.Contains(out, "123") || strings.Contains(out, "channel-access-backend-secret") {
			t.Fatalf("unsafe X list output: %s", out)
		}
		m = runLine(t, m, "/channels access x add 456 @Release_User")
		if !rec.saw(http.MethodPost, route) || !rec.sawQuery("project_id=p1") || !rec.sawForm("x_user_id=456") || !rec.sawForm("x_username=Release_User") || rec.sawForm("display_name=") {
			t.Fatalf("X add was not normalized/scoped: %s forms=%v", rec.all(), rec.forms)
		}
		if strings.Contains(transcript(m), "channel-access-backend-secret") {
			t.Fatalf("X add leaked backend content: %s", transcript(m))
		}
	})

	t.Run("leading-zero numeric identity resolves normalized X ID", func(t *testing.T) {
		m, rec := dispatchModel(t, bodies)
		m = runLine(t, m, "/channels access x remove 00123")
		if m.pendingConfirmation == nil || rec.count(http.MethodDelete, route+"/row-1") != 0 {
			t.Fatalf("leading-zero X identity was not captured before confirmation: %s", rec.all())
		}
		m = runLine(t, m, "yes")
		if got := rec.count(http.MethodDelete, route+"/row-1"); got != 1 {
			t.Fatalf("leading-zero X identity deleted %d rows, want 1: %s", got, rec.all())
		}
	})

	t.Run("cancellation and canonical removal are safe", func(t *testing.T) {
		m, rec := dispatchModel(t, bodies)
		m = runLine(t, m, "/channels access x remove @alice")
		if m.pendingConfirmation == nil || rec.saw(http.MethodDelete, route+"/row-1") {
			t.Fatalf("X username removal was not captured before confirmation: %s", rec.all())
		}
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = next.(Model)
		if rec.saw(http.MethodDelete, route+"/row-1") {
			t.Fatalf("canceled X removal mutated backend: %s", rec.all())
		}

		m = runLine(t, m, "/channels access x remove row-1")
		if m.pendingConfirmation == nil {
			t.Fatalf("canonical X row ID did not resolve: %s", transcript(m))
		}
		m = runLine(t, m, "yes")
		if !rec.saw(http.MethodDelete, route+"/row-1") || !rec.sawQuery("project_id=p1") {
			t.Fatalf("confirmed X removal was not scoped/canonical: %s", rec.all())
		}
	})

	t.Run("foreign and invalid targets do not confirm or mutate", func(t *testing.T) {
		foreign := `<div id="x_config_modal"><form action="/channels/x/authorized-users"><input name="project_id" value="p1"></form><div data-project-id="other-project"><span><span>@alice</span><span class="opacity-60">ID 123</span></span><button hx-delete="/channels/x/authorized-users/foreign-row?project_id=p1"></button></div></div>`
		m, rec := dispatchModel(t, map[string]string{"/channels": foreign, route: foreign})
		m = runLine(t, m, "/channels access x remove 123")
		if m.pendingConfirmation != nil || rec.saw(http.MethodDelete, route+"/foreign-row") || !strings.Contains(transcript(m), "authorized channel access list unavailable") {
			t.Fatalf("foreign X target was not rejected: output=%s calls=%s", transcript(m), rec.all())
		}
		for _, line := range []string{"/channels access x add", "/channels access x add not-numeric", "/channels access x add 123 @bad-name", "/channels access x list extra", "/channels access x remove 123 extra"} {
			m, rec = dispatchModel(t, nil)
			m = runLine(t, m, line)
			if rec.all() != "" || m.pendingConfirmation != nil {
				t.Fatalf("invalid X command made a request or opened confirmation: %s calls=%s", transcript(m), rec.all())
			}
		}
	})
}

// and renders the integrations page unchanged from the read-only behavior.
func TestChannelsInteractiveSetupEnforcesConditionalRequirements(t *testing.T) {
	pressEnter := func(m Model) Model {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		return next.(Model)
	}

	t.Run("github pat", func(t *testing.T) {
		m, rec := dispatchModel(t, nil)
		m = runLine(t, m, "/channels add github")
		m = runLine(t, m, "pat")
		m = pressEnter(m)
		if m.channelWizard == nil || m.channelWizard.steps[m.channelWizard.index].field != "github_pat" || !strings.Contains(transcript(m), "Personal access token (blank for app mode) is required") {
			t.Fatalf("GitHub PAT requirement not enforced: %s", transcript(m))
		}
		if calls := rec.all(); calls != "" {
			t.Fatalf("invalid wizard mutated backend: %s", calls)
		}
	})

	t.Run("github app", func(t *testing.T) {
		m, rec := dispatchModel(t, nil)
		m = runLine(t, m, "/channels add github")
		m = runLine(t, m, "app")
		m = pressEnter(m) // PAT is optional in app mode.
		m = pressEnter(m) // App ID is required.
		if m.channelWizard == nil || m.channelWizard.steps[m.channelWizard.index].field != "github_app_id" || !strings.Contains(transcript(m), "GitHub App ID is required") {
			t.Fatalf("GitHub App requirement not enforced: %s", transcript(m))
		}
		if calls := rec.all(); calls != "" {
			t.Fatalf("invalid wizard mutated backend: %s", calls)
		}
	})

	t.Run("slack manual", func(t *testing.T) {
		m, rec := dispatchModel(t, nil)
		m = runLine(t, m, "/channels add slack")
		for _, value := range []string{"client", "secret", "app-token", "manual"} {
			m = runLine(t, m, value)
		}
		m = pressEnter(m)
		if m.channelWizard == nil || m.channelWizard.steps[m.channelWizard.index].field != "slack_bot_token" || !strings.Contains(transcript(m), "Manual bot token") {
			t.Fatalf("Slack manual-token requirement not enforced: %s", transcript(m))
		}
		if calls := rec.all(); calls != "" {
			t.Fatalf("invalid wizard mutated backend: %s", calls)
		}
	})

	t.Run("email custom", func(t *testing.T) {
		m, rec := dispatchModel(t, nil)
		m = runLine(t, m, "/channels add email")
		for _, value := range []string{"custom", "bot@example.com", "password"} {
			m = runLine(t, m, value)
		}
		m = pressEnter(m)
		if m.channelWizard == nil || m.channelWizard.steps[m.channelWizard.index].field != "email_imap_host" || !strings.Contains(transcript(m), "IMAP host (custom provider) is required") {
			t.Fatalf("custom Email host requirement not enforced: %s", transcript(m))
		}
		if calls := rec.all(); calls != "" {
			t.Fatalf("invalid wizard mutated backend: %s", calls)
		}
	})

	t.Run("x poll interval", func(t *testing.T) {
		m, rec := dispatchModel(t, nil)
		m = runLine(t, m, "/channels add x")
		for _, value := range []string{"consumer-key", "consumer-secret", "access-token", "access-token-secret"} {
			m = runLine(t, m, value)
		}
		m = runLine(t, m, "14")
		if m.channelWizard == nil || m.channelWizard.steps[m.channelWizard.index].field != "x_poll_interval_seconds" || !strings.Contains(transcript(m), "between 15 and 300 seconds") {
			t.Fatalf("X poll interval requirement not enforced: %s", transcript(m))
		}
		if calls := rec.all(); calls != "" {
			t.Fatalf("invalid wizard mutated backend: %s", calls)
		}
	})

	t.Run("github edit pat to app", func(t *testing.T) {
		page := `<div data-channel-type="github" data-search-text="GitHub Configured"></div><form action="/channels/github/configure"><select name="github_auth_mode"><option value="pat" selected>PAT</option><option value="app">App</option></select><input name="github_app_id" value="stale-id"><input name="github_app_slug" value="stale-slug"><input name="github_pat" value="stored-pat"><textarea name="github_app_private_key">stored-key</textarea><input name="github_api_endpoint" value="https://api.github.com"></form>`
		m, rec := dispatchModel(t, map[string]string{"/channels": page})
		m = runLine(t, m, "/channels edit github")
		m = runLine(t, m, "app")
		m = pressEnter(m) // PAT is optional in App mode.
		m = pressEnter(m) // Stale App ID cannot satisfy a PAT-to-App transition.
		if m.channelWizard == nil || m.channelWizard.steps[m.channelWizard.index].field != "github_app_id" || !strings.Contains(transcript(m), "GitHub App ID is required") {
			t.Fatalf("GitHub edit transition reused stale App settings: %s", transcript(m))
		}
		if rec.saw("POST", "/channels/github/configure") {
			t.Fatalf("invalid GitHub edit mutated backend: %s", rec.all())
		}
	})

	t.Run("slack edit oauth to manual", func(t *testing.T) {
		page := `<div data-channel-type="slack" data-search-text="Slack Configured"></div><form action="/channels/slack/configure"><input name="slack_client_id" value="client"><input name="slack_client_secret" value="secret"><input name="slack_app_token" value="app"><select name="slack_bot_token_mode"><option value="oauth" selected>OAuth</option><option value="manual">Manual</option></select><input name="slack_bot_token" value="stale-bot"></form>`
		m, rec := dispatchModel(t, map[string]string{"/channels": page})
		m = runLine(t, m, "/channels edit slack")
		for _, value := range []string{"", "", "", "manual"} {
			m.input.SetValue(value)
			m = pressEnter(m)
		}
		m = pressEnter(m)
		if m.channelWizard == nil || m.channelWizard.steps[m.channelWizard.index].field != "slack_bot_token" || !strings.Contains(transcript(m), "Manual bot token") {
			t.Fatalf("Slack edit transition reused stale bot token: %s", transcript(m))
		}
		if rec.saw("POST", "/channels/slack/configure") {
			t.Fatalf("invalid Slack edit mutated backend: %s", rec.all())
		}
	})

	t.Run("email edit preset to custom", func(t *testing.T) {
		page := `<div data-channel-type="email" data-search-text="Email Configured"></div><form action="/channels/email/configure"><select name="email_provider"><option value="gmail" selected>Gmail</option><option value="custom">Custom</option></select><input name="email_address" value="bot@example.com"><input name="email_password" value="stored-password"><input name="email_imap_host" value="imap.gmail.com"><input name="email_imap_port" value="993"><input name="email_smtp_host" value="smtp.gmail.com"><input name="email_smtp_port" value="587"></form>`
		m, rec := dispatchModel(t, map[string]string{"/channels": page})
		m = runLine(t, m, "/channels edit email")
		for _, value := range []string{"custom", "", ""} {
			m.input.SetValue(value)
			m = pressEnter(m)
		}
		m = pressEnter(m)
		if m.channelWizard == nil || m.channelWizard.steps[m.channelWizard.index].field != "email_imap_host" || !strings.Contains(transcript(m), "IMAP host (custom provider) is required") {
			t.Fatalf("Email edit transition reused preset hosts: %s", transcript(m))
		}
		if rec.saw("POST", "/channels/email/configure") {
			t.Fatalf("invalid Email edit mutated backend: %s", rec.all())
		}
	})

	t.Run("email provider", func(t *testing.T) {
		m, rec := dispatchModel(t, nil)
		m = runLine(t, m, "/channels add email")
		m = runLine(t, m, "unknown")
		if m.channelWizard == nil || m.channelWizard.index != 0 || !strings.Contains(transcript(m), "provider must be one of") {
			t.Fatalf("Email provider requirement not enforced: %s", transcript(m))
		}
		if calls := rec.all(); calls != "" {
			t.Fatalf("invalid wizard mutated backend: %s", calls)
		}
	})
}

func TestChannelsInteractiveGitHubAppAcceptsFlattenedMultilinePEM(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}))
	flattened := strings.Join(strings.Fields(pemText), " ")

	m, rec := dispatchModel(t, map[string]string{"/channels": structuredChannelsPage})
	m = runLine(t, m, "/channels add github")
	for _, value := range []string{"app", "", "123", "terminal-app", flattened, "https://api.github.com"} {
		m.input.SetValue(value)
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = next.(Model)
		if cmd != nil {
			if msg := cmd(); msg != nil {
				next, _ = m.Update(msg)
				m = next.(Model)
			}
		}
	}
	if !rec.saw("POST", "/channels/github/configure") || m.channelWizard != nil {
		t.Fatalf("GitHub App wizard did not complete: %s\n%s", transcript(m), rec.all())
	}
	rec.mu.Lock()
	forms := append([]string(nil), rec.forms...)
	rec.mu.Unlock()
	var posted string
	for _, form := range forms {
		if strings.HasPrefix(form, "POST /channels/github/configure?") {
			posted = strings.TrimPrefix(form, "POST /channels/github/configure?")
		}
	}
	values, err := url.ParseQuery(posted)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(values.Get("github_app_private_key")))
	if block == nil || !reflect.DeepEqual(block.Bytes, der) {
		t.Fatal("GitHub App wizard did not submit a valid reconstructed PEM")
	}
	if strings.Contains(m.View(), flattened) || strings.Contains(transcript(m), flattened) {
		t.Fatal("GitHub private key appeared in terminal output")
	}
}

func TestChannelsInteractiveSetupSubmitsMaskedCredentialWithoutEcho(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{"/channels": structuredChannelsPage})
	m = runLine(t, m, "/channels add telegram")
	secret := "wizard-telegram-secret"
	m = runLine(t, m, secret)
	m = runLine(t, m, "true")
	if !rec.saw("POST", "/channels/telegram") || !rec.sawQuery("project_id=p1") {
		t.Fatalf("wizard did not submit scoped channel form: %s", rec.all())
	}
	if strings.Contains(m.View(), secret) || strings.Contains(transcript(m), secret) {
		t.Fatal("wizard credential appeared after submission")
	}
	for _, history := range m.history {
		if strings.Contains(history, secret) {
			t.Fatal("wizard credential entered command history")
		}
	}
	if m.channelWizard != nil || m.input.EchoMode != textinput.EchoNormal {
		t.Fatal("wizard did not reset after submission")
	}
}

func TestChannelsInteractiveSecretOptionsAreRedactedFromTranscriptAndHistory(t *testing.T) {
	m, rec := dispatchModel(t, nil)
	secret := "sentinel-visible-command-secret"
	m = runLine(t, m, `/channels add telegram --token "`+secret+`"`)
	if strings.Contains(m.View(), secret) || strings.Contains(transcript(m), secret) {
		t.Fatal("secret option appeared in interactive output")
	}
	for _, item := range m.history {
		if strings.Contains(item, secret) {
			t.Fatalf("secret option appeared in history: %q", item)
		}
	}
	if !strings.Contains(transcript(m), "<redacted>") {
		t.Fatalf("redacted command was not shown: %s", transcript(m))
	}
	if calls := rec.all(); calls != "" {
		t.Fatalf("interactive options should not bypass masked prompts:\n%s", calls)
	}
}

func TestChannelsInteractiveXSecretOptionsAreRedactedFromTranscriptAndHistory(t *testing.T) {
	m, rec := dispatchModel(t, nil)
	secrets := []string{"x-consumer-key-secret", "x-consumer-secret", "x-access-token", "x-access-token-secret"}
	line := `/channels add x --consumer-key "` + secrets[0] + `" --consumer-secret "` + secrets[1] + `" --access-token "` + secrets[2] + `" --access-token-secret "` + secrets[3] + `"`
	m = runLine(t, m, line)
	for _, secret := range secrets {
		if strings.Contains(m.View(), secret) || strings.Contains(transcript(m), secret) {
			t.Fatalf("X credential %q appeared in interactive output", secret)
		}
		for _, item := range m.history {
			if strings.Contains(item, secret) {
				t.Fatalf("X credential %q appeared in history", secret)
			}
		}
	}
	if calls := rec.all(); calls != "" {
		t.Fatalf("interactive options should not bypass masked prompts:\n%s", calls)
	}
}

func TestChannelsInteractiveSetupMasksSecretsForEveryTypeAndCancelsCleanly(t *testing.T) {
	tests := []struct {
		channel string
		before  []string
	}{
		{"telegram", nil},
		{"github", []string{""}},
		{"slack", []string{"client-id"}},
		{"discord", nil},
		{"x", nil},
		{"email", []string{"", "bot@example.com"}},
	}
	for _, tt := range tests {
		t.Run(tt.channel, func(t *testing.T) {
			m, rec := dispatchModel(t, nil)
			m = runLine(t, m, "/channels add "+tt.channel)
			if m.channelWizard == nil {
				t.Fatalf("wizard did not start: %s", transcript(m))
			}
			for _, value := range tt.before {
				m.input.SetValue(value)
				next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
				m = next.(Model)
			}
			if m.input.EchoMode != textinput.EchoPassword {
				t.Fatalf("%s credential prompt is not masked", tt.channel)
			}
			secret := "sentinel-" + tt.channel + "-credential"
			m.input.SetValue(secret)
			if strings.Contains(m.View(), secret) || strings.Contains(transcript(m), secret) {
				t.Fatalf("%s credential was visible", tt.channel)
			}
			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			m = next.(Model)
			if m.channelWizard != nil || m.input.EchoMode != textinput.EchoNormal {
				t.Fatalf("%s wizard did not reset after cancellation", tt.channel)
			}
			if calls := rec.all(); calls != "" {
				t.Fatalf("%s cancellation made requests:\n%s", tt.channel, calls)
			}
		})
	}
}

func TestChannelsInteractiveSetupCompletesEverySupportedType(t *testing.T) {
	tests := []struct {
		channel string
		values  []string
		path    string
	}{
		{channel: "telegram", values: []string{"telegram-secret", "true"}, path: "/channels/telegram"},
		{channel: "github", values: []string{"pat", "github-secret", "", "", "", "https://api.github.com"}, path: "/channels/github/configure"},
		{channel: "slack", values: []string{"client", "client-secret", "app-secret", "oauth", "", "true"}, path: "/channels/slack/configure"},
		{channel: "discord", values: []string{"discord-secret", "true"}, path: "/channels/discord/configure"},
		{channel: "x", values: []string{"consumer-key", "consumer-secret", "access-token", "access-token-secret", "30", "true"}, path: "/channels/x/configure"},
		{channel: "email", values: []string{"gmail", "bot@example.com", "email-secret", "", "", "", "", "15", "true", "false", "true"}, path: "/channels/email/configure"},
	}
	for _, tt := range tests {
		t.Run(tt.channel, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/channels": structuredChannelsPage})
			m = runLine(t, m, "/channels add "+tt.channel)
			for _, value := range tt.values {
				m.input.SetValue(value)
				next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
				m = next.(Model)
				if cmd != nil {
					msg := cmd()
					if msg != nil {
						next, _ = m.Update(msg)
						m = next.(Model)
					}
				}
			}
			if !rec.saw("POST", tt.path) || m.channelWizard != nil {
				t.Fatalf("%s wizard did not complete: %s\n%s", tt.channel, transcript(m), rec.all())
			}
			for _, value := range tt.values {
				if strings.Contains(value, "secret") && strings.Contains(m.View(), value) {
					t.Fatalf("%s wizard exposed credential", tt.channel)
				}
			}
		})
	}
}

func TestChannelAddEditCompletionAcrossGuidedAndHeadlessFlows(t *testing.T) {
	const storedSecret = "stored-channel-credential"
	const submittedSecret = "submitted-channel-credential"
	const channelsPage = `<div data-channel-type="telegram" data-channel-token="` + storedSecret + `" data-channel-running="true" data-search-text="Telegram Bot Connected"></div><form><input type="checkbox" name="telegram_rich_messages_v2" checked></form>`

	flows := []struct {
		name       string
		guided     bool
		action     string
		priorLists int
	}{
		{name: "guided add", guided: true, action: "add"},
		{name: "guided edit", guided: true, action: "edit", priorLists: 2},
		{name: "headless add", action: "add"},
		{name: "headless edit", action: "edit", priorLists: 1},
	}
	outcomes := []string{"success", "mutation failure", "refresh failure"}

	for _, flow := range flows {
		for _, outcome := range outcomes {
			flow, outcome := flow, outcome
			t.Run(flow.name+"/"+outcome, func(t *testing.T) {
				oldCLIMode := cliMode
				cliMode = !flow.guided
				t.Cleanup(func() { cliMode = oldCLIMode })

				var listCalls, mutationCalls int
				m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.Method == http.MethodGet && r.URL.Path == "/channels":
						listCalls++
						if outcome == "refresh failure" && listCalls == flow.priorLists+1 {
							http.Error(w, "refresh failed", http.StatusInternalServerError)
							return
						}
						w.Header().Set("Content-Type", "text/html")
						_, _ = io.WriteString(w, channelsPage)
					case r.Method == http.MethodPost && r.URL.Path == "/channels/telegram":
						mutationCalls++
						if outcome == "mutation failure" {
							http.Error(w, "mutation failed", http.StatusInternalServerError)
							return
						}
						w.WriteHeader(http.StatusNoContent)
					default:
						http.NotFound(w, r)
					}
				})

				if flow.guided {
					m = runLine(t, m, "/channels "+flow.action+" tele")
					if m.channelWizard == nil {
						t.Fatalf("guided %s did not start the channel wizard:\n%s", flow.action, transcript(m))
					}
					for _, value := range []string{submittedSecret, "false"} {
						m = runLine(t, m, value)
					}
				} else {
					m = runLine(t, m, "/channels "+flow.action+" tele --token "+submittedSecret+" --rich-messages false")
				}

				wantLists := flow.priorLists + 1
				if outcome == "mutation failure" {
					wantLists = flow.priorLists
				}
				if listCalls != wantLists || mutationCalls != 1 {
					t.Fatalf("GET /channels = %d, POST /channels/telegram = %d; want %d and 1", listCalls, mutationCalls, wantLists)
				}

				out := transcript(m)
				if strings.Contains(out, storedSecret) || strings.Contains(out, submittedSecret) || strings.Contains(m.View(), storedSecret) || strings.Contains(m.View(), submittedSecret) {
					t.Fatalf("channel completion exposed credentials:\n%s", out)
				}
				switch outcome {
				case "success":
					if !strings.Contains(out, flow.action+"ed Telegram Bot") || !strings.Contains(out, "TYPE       NAME") || strings.Contains(out, "error:") {
						t.Fatalf("successful completion output = %q", out)
					}
				case "mutation failure":
					if strings.Contains(out, flow.action+"ed Telegram Bot") || !strings.Contains(out, "error:") {
						t.Fatalf("mutation failure output = %q", out)
					}
				case "refresh failure":
					if !strings.Contains(out, flow.action+"ed Telegram Bot") || strings.Contains(out, "TYPE       NAME") || strings.Contains(out, "error:") || strings.Contains(out, "refresh failed") {
						t.Fatalf("refresh failure output = %q", out)
					}
				}
			})
		}
	}
}

func TestChannelsCommandListsPage(t *testing.T) {
	const channelsPage = structuredChannelsPage
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
	const refreshedChannelsPage = structuredChannelsPage
	cases := []struct {
		action      string
		channelName string
		wantPath    string
	}{
		{"remove", "github", "/channels/github/remove"},
		{"test", "telegram", "/channels/telegram/test"},
		{"remove", "telegram", "/channels/telegram/remove"},
		{"test", "slack", "/channels/slack/test"},
		{"remove", "slack", "/channels/slack/disconnect"},
		{"test", "discord", "/channels/discord/test"},
		{"remove", "discord", "/channels/discord/remove"},
		{"test", "x", "/channels/x/test"},
		{"remove", "x", "/channels/x/remove"},
		{"test", "email", "/channels/email/test"},
		{"remove", "email", "/channels/email/remove"},
		{"disconnect", "github", "/channels/github/disconnect"},
		{"disconnect", "slack", "/channels/slack/disconnect"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.action+" "+tc.channelName, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/channels": refreshedChannelsPage})
			if tc.action == "remove" || tc.action == "disconnect" {
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
			// Status lines use the canonical registry display name.
			channel, err := matchChannelRef(tc.channelName)
			if err != nil {
				t.Fatal(err)
			}
			displayName := channel.Name
			if !strings.Contains(out, tc.action+": "+displayName) {
				t.Errorf("expected status line %q:\n%s", tc.action+": "+displayName, out)
			}
			if !strings.Contains(out, "CONNECTION") {
				t.Errorf("expected refreshed structured channel list after action:\n%s", out)
			}
		})
	}
}

func TestChannelActionCompletionPolicy(t *testing.T) {
	const channelsPage = structuredChannelsPage
	cases := []struct {
		action      string
		channelName string
		wantPath    string
		confirm     bool
	}{
		{action: "test", channelName: "telegram", wantPath: "/channels/telegram/test"},
		{action: "remove", channelName: "telegram", wantPath: "/channels/telegram/remove", confirm: true},
		{action: "disconnect", channelName: "github", wantPath: "/channels/github/disconnect", confirm: true},
		{action: "disconnect", channelName: "slack", wantPath: "/channels/slack/disconnect", confirm: true},
	}
	for _, tc := range cases {
		for _, outcome := range []string{"success", "mutation failure", "refresh failure"} {
			tc, outcome := tc, outcome
			t.Run(tc.action+" "+tc.channelName+"/"+outcome, func(t *testing.T) {
				var actionCalls, listCalls int
				m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.Method == http.MethodGet && r.URL.Path == "/channels":
						listCalls++
						if outcome == "refresh failure" {
							http.Error(w, "refresh failed", http.StatusInternalServerError)
							return
						}
						w.Header().Set("Content-Type", "text/html")
						_, _ = io.WriteString(w, channelsPage)
					case r.Method == http.MethodPost && r.URL.Path == tc.wantPath:
						actionCalls++
						if outcome == "mutation failure" {
							http.Error(w, "mutation failed", http.StatusInternalServerError)
							return
						}
						if tc.action == "test" {
							w.Header().Set("Content-Type", "text/html")
							_, _ = io.WriteString(w, `<div class="text-success">Connection successful!</div>`)
							return
						}
						w.WriteHeader(http.StatusNoContent)
					default:
						http.NotFound(w, r)
					}
				})

				line := "/channels " + tc.action + " " + tc.channelName
				if tc.confirm {
					m = runLine(t, m, line)
					if actionCalls != 0 || m.pendingConfirmation == nil {
						t.Fatalf("%s requested mutation before confirmation: actions=%d pending=%v", line, actionCalls, m.pendingConfirmation != nil)
					}
					if !strings.Contains(stripANSI(m.View()), "Type 'yes' to confirm") {
						t.Fatalf("%s did not show the confirmation prompt:\n%s", line, transcript(m))
					}
					m = runLine(t, m, "yes")
				} else {
					m = runLine(t, m, line)
				}

				wantLists := 1
				if outcome == "mutation failure" {
					wantLists = 0
				}
				if actionCalls != 1 || listCalls != wantLists {
					t.Fatalf("POST %s=%d, GET /channels=%d; want 1 and %d", tc.wantPath, actionCalls, listCalls, wantLists)
				}

				channel, err := matchChannelRef(tc.channelName)
				if err != nil {
					t.Fatal(err)
				}
				status := tc.action + ": " + channel.Name
				out := stripANSI(transcript(m))
				switch outcome {
				case "success":
					if !strings.Contains(out, status) || !strings.Contains(out, "CONNECTION") || strings.Contains(out, "error:") {
						t.Fatalf("successful %s output = %q", line, out)
					}
				case "mutation failure":
					if strings.Contains(out, status) || strings.Contains(out, "CONNECTION") || !strings.Contains(out, "error:") {
						t.Fatalf("mutation failure %s output = %q", line, out)
					}
				case "refresh failure":
					if !strings.Contains(out, status) || strings.Contains(out, "CONNECTION") || strings.Contains(out, "refresh failed") || strings.Contains(out, "error:") {
						t.Fatalf("refresh failure %s output = %q", line, out)
					}
				}
			})
		}
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

func TestChannelsRemoveResolvesReferenceBeforeConfirmation(t *testing.T) {
	t.Run("ambiguous reference fails immediately", func(t *testing.T) {
		m, rec := dispatchModel(t, nil)
		m = runLine(t, m, "/channels remove a")

		out := stripANSI(transcript(m))
		if !strings.Contains(out, `"a" is ambiguous`) {
			t.Fatalf("ambiguous removal output = %q", out)
		}
		if m.pendingConfirmation != nil {
			t.Fatal("ambiguous removal opened a confirmation")
		}
		if calls := rec.all(); calls != "" {
			t.Fatalf("ambiguous removal made backend requests:\n%s", calls)
		}
	})

	t.Run("unknown reference fails immediately", func(t *testing.T) {
		m, rec := dispatchModel(t, nil)
		m = runLine(t, m, "/channels remove irc")

		out := stripANSI(transcript(m))
		if !strings.Contains(out, `nothing matches "irc"`) {
			t.Fatalf("unknown removal output = %q", out)
		}
		if m.pendingConfirmation != nil {
			t.Fatal("unknown removal opened a confirmation")
		}
		if calls := rec.all(); calls != "" {
			t.Fatalf("unknown removal made backend requests:\n%s", calls)
		}
	})

	t.Run("unique partial uses canonical name without requesting", func(t *testing.T) {
		m, rec := dispatchModel(t, nil)
		m = runLine(t, m, "/channels remove tele")

		out := stripANSI(m.View())
		if !strings.Contains(out, `Remove channel "Telegram Bot"?`) {
			t.Fatalf("partial removal prompt = %q, want canonical channel name", out)
		}
		if m.pendingConfirmation == nil {
			t.Fatal("valid partial removal did not open a confirmation")
		}
		if calls := rec.all(); calls != "" {
			t.Fatalf("partial removal made requests before confirmation:\n%s", calls)
		}

		m = runLine(t, m, "no")
		if calls := rec.all(); calls != "" {
			t.Fatalf("cancelled partial removal made backend requests:\n%s", calls)
		}
	})
}

func TestChannelsRemoveRequiresConfirmation(t *testing.T) {
	const refreshedChannelsPage = structuredChannelsPage

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
		if !strings.Contains(out, "remove: Slack") || !strings.Contains(out, "CONNECTION") {
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
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div class="text-success"><span>Connection successful!</span></div>`))
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
	if !strings.Contains(out, "test: Telegram Bot") {
		t.Errorf("expected status line despite refresh failure:\n%s", out)
	}
	if strings.Contains(out, "error:") || strings.Contains(out, "refresh failed") {
		t.Errorf("refresh failure must be swallowed:\n%s", out)
	}
}

func TestChannelsHTTP200TestFailureIsSafeAndDoesNotReportSuccess(t *testing.T) {
	const secret = "backend-test-secret"
	m, _ := dispatchModel(t, map[string]string{
		"POST /channels/email/test": `<div class="text-error"><span>Connection failed: ` + secret + `</span></div>`,
	})
	m = runLine(t, m, "/channels test email")
	out := transcript(m)
	if !strings.Contains(out, "channel test failed") {
		t.Fatalf("test failure was not surfaced: %s", out)
	}
	if strings.Contains(out, "test: Email") || strings.Contains(out, secret) {
		t.Fatalf("test failure reported success or leaked backend text: %s", out)
	}
}

func TestChannelsDisconnectUsesOnlySupportedRoutesAfterConfirmation(t *testing.T) {
	for _, channelType := range []string{"github", "slack"} {
		t.Run(channelType, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/channels": structuredChannelsPage})
			m = runLine(t, m, "/channels disconnect "+channelType)
			if rec.saw("POST", "/channels/"+channelType+"/disconnect") || m.pendingConfirmation == nil {
				t.Fatalf("disconnect was not gated before request: %s", rec.all())
			}
			if !strings.Contains(stripANSI(m.View()), `Disconnect channel "`+map[string]string{"github": "GitHub", "slack": "Slack"}[channelType]+`"?`) {
				t.Fatalf("disconnect prompt was not canonical: %s", m.View())
			}
			m = runLine(t, m, "yes")
			if !rec.saw("POST", "/channels/"+channelType+"/disconnect") {
				t.Fatalf("confirmed disconnect route missing: %s", rec.all())
			}
			if rec.saw("POST", "/channels/"+channelType+"/remove") || m.pendingConfirmation != nil {
				t.Fatalf("disconnect used remove or left confirmation: %s", rec.all())
			}
		})
	}
	m, rec := dispatchModel(t, nil)
	m = runLine(t, m, "/channels disconnect discord")
	if !strings.Contains(transcript(m), "does not support disconnect") || rec.saw("POST", "/channels/discord/remove") {
		t.Fatalf("unsupported disconnect was not rejected safely: %s\n%s", transcript(m), rec.all())
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

// TestChannelsMissingArgOpensSelector verifies that every reference-based channel
// action opens the searchable inline selector without requesting or mutating the
// backend, cancels cleanly, and retains a usage error in headless mode.
func TestChannelsMissingArgOpensSelector(t *testing.T) {
	for _, action := range []string{"show", "add", "connect", "edit", "test", "remove", "disconnect"} {
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
			wantItems := len(client.KnownChannels)
			if action == "test" {
				wantItems--
			}
			if action == "connect" || action == "disconnect" {
				wantItems = 2
			}
			if len(m.selectorItems) != wantItems {
				t.Errorf("selector items = %d, want %d", len(m.selectorItems), wantItems)
			}
			if action != "connect" && action != "disconnect" {
				foundX := false
				for _, item := range m.selectorItems {
					foundX = foundX || item.ref == "x"
				}
				if !foundX {
					t.Errorf("%s selector omitted X: %#v", action, m.selectorItems)
				}
			}
			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			m = next.(Model)
			if m.selectorActive || rec.all() != "" {
				t.Errorf("selector cancellation did not remain side-effect free: %s", rec.all())
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

func TestBriefingCommandLifecycleTable(t *testing.T) {
	type testCase struct {
		name        string
		line        string
		fetchPath   string
		fetchBody   string
		triggerPath string
		triggerFail bool
	}

	cases := []testCase{
		{name: "pulse bare", line: "/pulse", fetchPath: "/upcoming", fetchBody: "<div>upcoming</div>"},
		{name: "pulse show", line: "/pulse show", fetchPath: "/upcoming", fetchBody: "<div>upcoming</div>"},
		{name: "pulse summary", line: "/pulse summary", fetchPath: "/upcoming", fetchBody: "<div>upcoming</div>", triggerPath: "/upcoming/summary"},
		{name: "pulse failed summary", line: "/pulse summary", fetchPath: "/upcoming", fetchBody: "<div>upcoming</div>", triggerPath: "/upcoming/summary", triggerFail: true},
		{name: "reflection bare", line: "/reflection", fetchPath: "/history", fetchBody: "<div>history</div>"},
		{name: "reflection show", line: "/reflection show", fetchPath: "/history", fetchBody: "<div>history</div>"},
		{name: "reflection summary", line: "/reflection summary", fetchPath: "/history", fetchBody: "<div>history</div>", triggerPath: "/history/summary"},
		{name: "reflection failed summary", line: "/reflection summary", fetchPath: "/history", fetchBody: "<div>history</div>", triggerPath: "/history/summary", triggerFail: true},
		{name: "grades bare", line: "/grades", fetchPath: "/history", fetchBody: `<div id="idea-grade-content">grades</div>`},
		{name: "grades show", line: "/grades show", fetchPath: "/history", fetchBody: `<div id="idea-grade-content">grades</div>`},
		{name: "grades run", line: "/grades run", fetchPath: "/history", fetchBody: `<div id="idea-grade-content">grades</div>`, triggerPath: "/history/grade-ideas"},
		{name: "grades failed run", line: "/grades run", fetchPath: "/history", fetchBody: `<div id="idea-grade-content">grades</div>`, triggerPath: "/history/grade-ideas", triggerFail: true},
		{name: "insights bare", line: "/insights", fetchPath: "/insights", fetchBody: "<div>insights</div>"},
		{name: "insights show", line: "/insights show", fetchPath: "/insights", fetchBody: "<div>insights</div>"},
		{name: "insights analyze", line: "/insights analyze", fetchPath: "/insights", fetchBody: "<div>insights</div>", triggerPath: "/insights/analyze"},
		{name: "insights failed analyze", line: "/insights analyze", fetchPath: "/insights", fetchBody: "<div>insights</div>", triggerPath: "/insights/analyze", triggerFail: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				if r.URL.Path == tc.triggerPath && tc.triggerPath != "" {
					if tc.triggerFail {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					return
				}
				if r.URL.Path == tc.fetchPath {
					w.Header().Set("Content-Type", "text/html")
					_, _ = w.Write([]byte(tc.fetchBody))
					return
				}
				w.WriteHeader(http.StatusNotFound)
			})

			m = runLine(t, m, tc.line)
			if tc.triggerPath == "" {
				if got := strings.Count(strings.Join(calls, "\n"), "POST "); got != 0 {
					t.Fatalf("%s made %d trigger requests, want zero: %v", tc.line, got, calls)
				}
			} else if got := countCall(calls, "POST "+tc.triggerPath); got != 1 {
				t.Fatalf("%s made %d trigger requests, want one: %v", tc.line, got, calls)
			}

			if tc.triggerFail {
				if countCall(calls, "GET "+tc.fetchPath) != 0 {
					t.Fatalf("%s fetched after trigger failure: %v", tc.line, calls)
				}
				if !strings.Contains(transcript(m), "error:") {
					t.Fatalf("%s did not report trigger failure:\n%s", tc.line, transcript(m))
				}
				return
			}
			if got := countCall(calls, "GET "+tc.fetchPath); got != 1 {
				t.Fatalf("%s made %d fetch requests, want one: %v", tc.line, got, calls)
			}
			if tc.triggerPath != "" {
				triggerIndex := firstCallIndex(calls, "POST "+tc.triggerPath)
				fetchIndex := firstCallIndex(calls, "GET "+tc.fetchPath)
				if triggerIndex >= fetchIndex {
					t.Fatalf("%s requests were not trigger-then-fetch: %v", tc.line, calls)
				}
			}
			if strings.Contains(transcript(m), "error:") {
				t.Fatalf("%s returned an error:\n%s", tc.line, transcript(m))
			}
		})
	}
}

func countCall(calls []string, want string) int {
	count := 0
	for _, call := range calls {
		if call == want {
			count++
		}
	}
	return count
}

func firstCallIndex(calls []string, want string) int {
	for i, call := range calls {
		if call == want {
			return i
		}
	}
	return -1
}

func TestBriefingCommandsAcceptShowAction(t *testing.T) {
	cases := []struct {
		name      string
		line      string
		fetchPath string
		body      string
	}{
		{name: "pulse", line: "/pulse show", fetchPath: "/upcoming", body: "<div>upcoming briefing</div>"},
		{name: "reflection", line: "/reflection show", fetchPath: "/history", body: "<div>history debrief</div>"},
		{name: "grades", line: "/grades show", fetchPath: "/history", body: `<div id="idea-grade-content">grades</div>`},
		{name: "insights", line: "/insights show", fetchPath: "/insights", body: "<div>insights</div>"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{tc.fetchPath: tc.body})
			m = runLine(t, m, tc.line)

			if got := rec.count("GET", tc.fetchPath); got != 1 {
				t.Fatalf("%s made %d GET requests to %s, want 1:\n%s", tc.line, got, tc.fetchPath, rec.all())
			}
			if calls := rec.all(); strings.Contains(calls, "POST ") {
				t.Fatalf("%s unexpectedly made a POST request:\n%s", tc.line, calls)
			}
			if strings.Contains(transcript(m), "error:") {
				t.Fatalf("%s returned an error:\n%s", tc.line, transcript(m))
			}
		})
	}
}

func TestBriefingCommandsRejectInvalidOperands(t *testing.T) {
	cases := []struct {
		name  string
		line  string
		usage string
	}{
		{name: "pulse unknown action", line: "/pulse sumary", usage: "usage: /pulse [show|summary]"},
		{name: "pulse surplus operand", line: "/pulse summary now", usage: "usage: /pulse [show|summary]"},
		{name: "reflection unknown action", line: "/reflection refresh", usage: "usage: /reflection [show|summary]"},
		{name: "reflection surplus operand", line: "/reflection show extra", usage: "usage: /reflection [show|summary]"},
		{name: "grades unknown action", line: "/grades rerun", usage: "usage: /grades [show|run]"},
		{name: "grades surplus operand", line: "/grades run again", usage: "usage: /grades [show|run]"},
		{name: "insights unknown action", line: "/insights analyse", usage: "usage: /insights [show|analyze]"},
		{name: "insights surplus operand", line: "/insights analyze tomorrow", usage: "usage: /insights [show|analyze]"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, nil)
			m = runLine(t, m, tc.line)

			if calls := rec.all(); calls != "" {
				t.Fatalf("%s made backend requests:\n%s", tc.line, calls)
			}
			if out := transcript(m); !strings.Contains(out, tc.usage) {
				t.Fatalf("%s output missing usage %q:\n%s", tc.line, tc.usage, out)
			}
		})
	}
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
		{name: "pulse show", line: "/pulse show", fetchPath: "/upcoming", fetchOutput: "pulse output"},
		{name: "pulse summary", line: "/pulse summary", fetchPath: "/upcoming", fetchOutput: "pulse output", triggerPath: "/upcoming/summary"},
		{name: "reflection", line: "/reflection", fetchPath: "/history", fetchOutput: "reflection output"},
		{name: "reflection show", line: "/reflection show", fetchPath: "/history", fetchOutput: "reflection output"},
		{name: "reflection summary", line: "/reflection summary", fetchPath: "/history", fetchOutput: "reflection output", triggerPath: "/history/summary"},
		{name: "grades", line: "/grades", fetchPath: "/history", fetchOutput: "grades output"},
		{name: "grades show", line: "/grades show", fetchPath: "/history", fetchOutput: "grades output"},
		{name: "grades run", line: "/grades run", fetchPath: "/history", fetchOutput: "grades output", triggerPath: "/history/grade-ideas"},
		{name: "insights", line: "/insights", fetchPath: "/insights", fetchOutput: "insights output"},
		{name: "insights show", line: "/insights show", fetchPath: "/insights", fetchOutput: "insights output"},
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

// TestWorkersShowFetchesConcurrently verifies that the workers table uses the
// three structured capacity sources concurrently and applies the documented
// primary/partial failure behavior.
func TestWorkersShowFetchesConcurrently(t *testing.T) {
	const delay = 150 * time.Millisecond
	const capacityJSON = `{"total_running":2,"max_workers":8,"queue_size":0,"available_slots":6}`

	t.Run("all structured fetches run concurrently", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/capacity/global":
				time.Sleep(delay)
				_, _ = w.Write([]byte(capacityJSON))
			case "/api/capacity/projects":
				time.Sleep(delay)
				_, _ = w.Write([]byte(`[{"id":"p1","name":"Demo","running":1,"queue_size":2,"max_workers":3}]`))
			case "/api/capacity/models":
				time.Sleep(delay)
				_, _ = w.Write([]byte(`[{"name":"Sonnet","model":"claude-sonnet","running":1,"max_workers":4}]`))
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
		m = runLine(t, m, "/works show")
		elapsed := time.Since(start)
		if elapsed >= 2*delay {
			t.Errorf("workers show took %v, want well under %v", elapsed, 2*delay)
		}
		out := stripANSI(transcript(m))
		for _, want := range []string{"SCOPE", "All Projects", "Demo", "MODEL", "Sonnet (claude-sonnet)"} {
			if !strings.Contains(out, want) {
				t.Errorf("workers alias output missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("show works without a selected project", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{
			"/api/capacity/global":   capacityJSON,
			"/api/capacity/projects": `[]`,
			"/api/capacity/models":   `[]`,
		})
		m.selectedID = ""
		m.selectedName = ""

		m = runLine(t, m, "/works show")
		out := stripANSI(transcript(m))
		if !strings.Contains(out, "All Projects") || strings.Contains(out, "no project selected") {
			t.Fatalf("no-project workers show did not render global capacity:\n%s", out)
		}
		for _, path := range []string{"/api/capacity/global", "/api/capacity/projects", "/api/capacity/models"} {
			if got := rec.count("GET", path); got != 1 {
				t.Errorf("GET %s count = %d, want 1; calls:\n%s", path, got, rec.all())
			}
		}
		if got := len(rec.urlsSnapshot()); got != 3 {
			t.Errorf("workers show made %d requests, want exactly 3; calls:\n%s", got, rec.all())
		}
	})

	t.Run("secondary failures render partial table", func(t *testing.T) {
		m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/capacity/global" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(capacityJSON))
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
		})
		m = runLine(t, m, "/workers show")
		out := stripANSI(transcript(m))
		for _, want := range []string{"All Projects", "project worker capacity unavailable", "model worker capacity unavailable"} {
			if !strings.Contains(out, want) {
				t.Errorf("partial workers output missing %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "no dedicated model worker pools") {
			t.Errorf("failed model source was misrepresented as an empty result:\n%s", out)
		}
		if strings.Contains(out, "error::") {
			t.Errorf("secondary failure incorrectly failed command:\n%s", out)
		}
	})

	t.Run("global failure propagates", func(t *testing.T) {
		m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/capacity/global" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		})
		m = runLine(t, m, "/workers show")
		if out := transcript(m); !strings.Contains(out, "error:") {
			t.Errorf("expected global capacity error; got:\n%s", out)
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
	// The status path uses only compact JSON projections, not the ordinary HTML
	// alert/task collections. The responses deliberately represent mixed states
	// that the old card parser would have counted as one pending, two active,
	// and one queued.
	m, rec := dispatchModel(t, map[string]string{
		"/api/alerts/pending-count": `{"count":1}`,
		"/api/tasks/status-counts":  `{"active_tasks":2,"queued_tasks":1}`,
	})

	// First /status call triggers fetchStatusCounts and processes the result
	// (runLine follows one level of chaining, so statusCountsMsg is applied).
	m = runLine(t, m, "/status")

	if !rec.saw("GET", "/api/alerts/pending-count") {
		t.Errorf("expected compact pending-alert request during status counts fetch:\n%s", rec.all())
	}
	if !rec.saw("GET", "/api/tasks/status-counts") {
		t.Errorf("expected compact task-count request during status counts fetch:\n%s", rec.all())
	}
	if rec.saw("GET", "/alerts") || rec.saw("GET", "/tasks") {
		t.Errorf("status counts must not fetch full collections:\n%s", rec.all())
	}
	if !rec.sawQuery("project_id=p1") {
		t.Errorf("compact status requests must carry selected project_id: %v", rec.urlsSnapshot())
	}

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

func TestTaskReviewReadPathsHaveEquivalentJSONOutputAndCanonicalRouting(t *testing.T) {
	const taskID = "0123456789abcdef0123456789abcdef"
	board := `<div data-task-id="` + taskID + `" data-task-status="running" data-task-category="active">
		<a href="/tasks/` + taskID + `" title="Exact task">Exact task</a>
	</div>`
	reviews := strings.Replace(taskReviewHTML, `data-task-id="t-1"`, `data-task-id="`+taskID+`"`, 1)
	tests := []struct {
		name      string
		line      string
		canonical bool
	}{
		{name: "show review tab", line: "/tasks show Exact task review"},
		{name: "reviews default list", line: "/tasks reviews Exact task"},
		{name: "reviews list subcommand", line: "/tasks reviews list Exact task"},
		{name: "canonical full ID review", line: "/tasks show " + taskID + " review", canonical: true},
	}

	previousJSON := jsonMode
	jsonMode = true
	defer func() { jsonMode = previousJSON }()

	var want string
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{
				"/tasks":                        board,
				"/tasks/" + taskID:              canonicalTaskDetailHTML(taskID, "p1"),
				"/tasks/" + taskID + "/reviews": reviews,
			})
			m = runLine(t, m, tc.line)

			got := taskReviewsResultText(m)
			var decoded []client.ReviewComment
			if err := json.Unmarshal([]byte(got), &decoded); err != nil {
				t.Fatalf("review output is not a JSON array: %v\n%s", err, transcript(m))
			}
			if len(decoded) != 1 || decoded[0].ID != "rc-1" || decoded[0].CommentText != "Needs error handling" {
				t.Fatalf("review JSON = %+v, want parsed review comment", decoded)
			}
			if want == "" {
				want = got
			} else if got != want {
				t.Errorf("review JSON differs from the first read path:\n%s\nwant:\n%s", got, want)
			}
			if got := rec.count("GET", "/tasks/"+taskID+"/reviews"); got != 1 {
				t.Fatalf("review display should make exactly one review request, got %d:\n%s", got, rec.all())
			}
			if tc.canonical {
				if got := rec.count("GET", "/tasks"); got != 0 {
					t.Fatalf("canonical review made %d board requests:\n%s", got, rec.all())
				}
				if !rec.sawQuery("GET /tasks/" + taskID + "/reviews?project_id=p1") {
					t.Fatalf("canonical review request was not project scoped: %v", rec.urlsSnapshot())
				}
			} else if got := rec.count("GET", "/tasks"); got != 1 {
				t.Fatalf("ordinary review resolution should make exactly one board request, got %d:\n%s", got, rec.all())
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

func TestCanonicalTaskReviewPropagatesScopedFetchErrorWithoutBoard(t *testing.T) {
	const taskID = "0123456789abcdef0123456789abcdef"
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.recordURL(r.Method, r.URL.RequestURI())
		switch r.URL.Path {
		case "/tasks/" + taskID:
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(canonicalTaskDetailHTML(taskID, "p1")))
		case "/tasks/" + taskID + "/reviews":
			if got := r.URL.Query().Get("project_id"); got != "p1" {
				t.Errorf("review project_id = %q, want p1", got)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"review fetch failed"}`))
		default:
			http.NotFound(w, r)
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
	m.selectedID, m.selectedName = "p1", "demo"
	m = runLine(t, m, "/tasks show "+taskID+" review")

	if got := rec.count("GET", "/tasks"); got != 0 {
		t.Fatalf("canonical review made %d board requests:\n%s", got, rec.all())
	}
	if got := rec.count("GET", "/tasks/"+taskID+"/reviews"); got != 1 {
		t.Fatalf("canonical review made %d review requests, want 1:\n%s", got, rec.all())
	}
	if got, want := taskReviewErrorText(m), "server error (502): review fetch failed"; got != want {
		t.Fatalf("review error = %q, want %q; transcript:\n%s", got, want, transcript(m))
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

func TestTasksReviewsAddUnquotedTitleContainingLocationToken(t *testing.T) {
	board := strings.Replace(taskBoardHTML, "Refactor the API", "Add 1:1 customer support", 1)
	addedReview := strings.Replace(taskReviewHTML, "internal/client/tasks.go", "internal/auth.go", 1)
	m, rec := dispatchModel(t, map[string]string{
		"/tasks":             board,
		"/tasks/t-1/reviews": addedReview,
	})
	m = runLine(t, m, "/tasks reviews add Add 1:1 customer support internal/auth.go:42 Needs error handling")

	if got := rec.count("POST", "/tasks/t-1/reviews"); got != 1 {
		t.Fatalf("expected exactly one add review call, got %d; calls:\n%s", got, rec.all())
	}
	wantForm := "POST /tasks/t-1/reviews?comment_text=Needs+error+handling&file_path=internal%2Fauth.go&line_number=42&line_type=new"
	if !rec.sawForm(wantForm) {
		t.Fatalf("unquoted location-shaped title posted unexpected form, want %q; forms: %v", wantForm, rec.forms)
	}
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "added review comment on internal/auth.go:42 for Add 1:1 customer support") {
		t.Fatalf("success confirmation did not identify the complete task and location:\n%s", out)
	}
	if strings.Contains(out, "for 1:1") {
		t.Fatalf("success confirmation used a synthetic task reference:\n%s", out)
	}
}

func TestTasksReviewsAddLocationShapedCommentTokenRemainsComment(t *testing.T) {
	addedReview := strings.Replace(taskReviewHTML, "internal/client/tasks.go", "internal/auth.go", 1)
	addedReview = strings.Replace(addedReview, "Needs error handling", "See 1:1 for context", 1)
	m, rec := dispatchModel(t, map[string]string{
		"/tasks":             taskBoardHTML,
		"/tasks/t-1/reviews": addedReview,
	})
	m = runLine(t, m, "/tasks reviews add Refactor the API internal/auth.go:42 See 1:1 for context")

	if got := rec.count("POST", "/tasks/t-1/reviews"); got != 1 {
		t.Fatalf("expected exactly one add review call, got %d; calls:\n%s", got, rec.all())
	}
	wantForm := "POST /tasks/t-1/reviews?comment_text=See+1%3A1+for+context&file_path=internal%2Fauth.go&line_number=42&line_type=new"
	if !rec.sawForm(wantForm) {
		t.Fatalf("location-shaped comment token was parsed as a location, want %q; forms: %v", wantForm, rec.forms)
	}
}

func TestTasksReviewsAddAmbiguousPrefixDoesNotMutate(t *testing.T) {
	const ambiguousTasks = `<div>
	  <div data-task-id="t-1" data-task-status="pending" data-task-category="backlog">
	    <a href="/tasks/t-1?from=tasks" title="Add 1:1 customer support">Add 1:1 customer support</a>
	  </div>
	  <div data-task-id="t-2" data-task-status="pending" data-task-category="backlog">
	    <a href="/tasks/t-2?from=tasks" title="Add 1:1 customer success">Add 1:1 customer success</a>
	  </div>
	</div>`
	m, rec := dispatchModel(t, map[string]string{"/tasks": ambiguousTasks})
	m = runLine(t, m, "/tasks reviews add Add 1:1 customer internal/auth.go:42 Needs error handling")

	if got := rec.count("POST", "/tasks/t-1/reviews") + rec.count("POST", "/tasks/t-2/reviews"); got != 0 {
		t.Fatalf("ambiguous task/location boundary must not mutate, got %d POSTs; calls:\n%s", got, rec.all())
	}
	if out := strings.ToLower(stripANSI(transcript(m))); !strings.Contains(out, "ambiguous") {
		t.Fatalf("expected an ambiguous task-reference error:\n%s", transcript(m))
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

const webhookCardsHTML = `<div data-card-pagination-root data-card-pagination-card-selector="[data-webhook-id]" data-card-pagination-key="data-webhook-id"><div data-webhook-id="w1" data-webhook-name="Pager Duty" data-webhook-enabled="false" data-webhook-token="safe-token" data-webhook-default-priority="4"></div><div data-webhook-id="w2" data-webhook-name="Pager Build" data-webhook-enabled="true" data-webhook-token="build-token" data-webhook-default-priority="2"></div></div>`

const webhookDetailJSON = `{"id":"w1","project_id":"p1","name":"Pager Duty","enabled":false,"path_token":"safe-token","secret":"never-print-this","system_instructions":"keep system","title_template":"keep title","prompt_template":"keep prompt","default_priority":4,"agent_ids":["a1","a2"]}`

const canonicalWebhookID = "0123456789abcdef0123456789abcdef"

func canonicalWebhookDetailJSON(id, projectID, name string) string {
	return fmt.Sprintf(`{"id":%q,"project_id":%q,"name":%q,"enabled":true,"path_token":"safe-token","secret":"never-rendered","system_instructions":"keep system","title_template":"keep title","prompt_template":"keep prompt","default_priority":2,"agent_ids":["a1","a2"]}`, id, projectID, name)
}

func TestIsCanonicalWebhookIDMatchesBackendGrammar(t *testing.T) {
	for _, tc := range []struct {
		ref  string
		want bool
	}{
		{ref: canonicalWebhookID, want: true},
		{ref: strings.ToUpper(canonicalWebhookID), want: false},
		{ref: canonicalWebhookID[:31], want: false},
		{ref: canonicalWebhookID[:31] + "g", want: false},
		{ref: "wh-" + canonicalWebhookID[:29], want: false},
	} {
		if got := isCanonicalWebhookID(tc.ref); got != tc.want {
			t.Errorf("isCanonicalWebhookID(%q) = %t, want %t", tc.ref, got, tc.want)
		}
	}
}

func TestWebhooksCanonicalIDUsesScopedDetailWithoutCatalog(t *testing.T) {
	for _, tc := range []struct {
		action       string
		line         string
		wantMutation string
		wantDetails  int
	}{
		{action: "show", line: "/channels webhooks show " + canonicalWebhookID, wantDetails: 1},
		{action: "edit", line: "/channels webhooks edit " + canonicalWebhookID + " --name Renamed", wantMutation: "PUT", wantDetails: 2},
		{action: "test", line: "/channels webhooks test " + canonicalWebhookID, wantMutation: "POST", wantDetails: 1},
		{action: "rotate", line: "/channels webhooks rotate " + canonicalWebhookID, wantMutation: "POST", wantDetails: 1},
		{action: "delete", line: "/channels webhooks delete " + canonicalWebhookID, wantMutation: "DELETE", wantDetails: 1},
	} {
		t.Run(tc.action, func(t *testing.T) {
			var catalogRequests, detailRequests, mutationRequests int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/channels":
					catalogRequests++
					http.Error(w, "canonical ID must not scan catalog", http.StatusInternalServerError)
				case "/channels/webhooks/" + canonicalWebhookID:
					switch r.Method {
					case http.MethodGet:
						detailRequests++
						if got := r.URL.Query().Get("project_id"); got != "p1" {
							t.Errorf("detail project_id = %q, want p1", got)
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, canonicalWebhookDetailJSON(canonicalWebhookID, "p1", "Canonical Hook"))
					case http.MethodPut, http.MethodDelete:
						mutationRequests++
						w.WriteHeader(http.StatusNoContent)
					default:
						http.NotFound(w, r)
					}
				case "/channels/webhooks/" + canonicalWebhookID + "/test":
					mutationRequests++
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"task_id":"webhook-test-task"}`)
				case "/channels/webhooks/" + canonicalWebhookID + "/rotate-secret":
					mutationRequests++
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"secret":"replacement"}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			m := New(c)
			m.selectedID, m.selectedName = "p1", "demo"
			if tc.action == "rotate" || tc.action == "delete" {
				m = confirmDestructive(t, m, tc.line)
			} else {
				m = runLine(t, m, tc.line)
			}
			if catalogRequests != 0 || detailRequests != tc.wantDetails || mutationRequests != map[bool]int{true: 1, false: 0}[tc.wantMutation != ""] {
				t.Fatalf("catalog/details/mutations = %d/%d/%d, want 0/%d/%d", catalogRequests, detailRequests, mutationRequests, tc.wantDetails, map[bool]int{true: 1, false: 0}[tc.wantMutation != ""])
			}
			if tc.action == "edit" && !strings.Contains(transcript(m), "updated webhook") {
				t.Fatalf("edit did not complete:\n%s", transcript(m))
			}
		})
	}
}

func TestWebhooksCanonicalIDNotFoundFallsBackToCatalog(t *testing.T) {
	var detailRequests, catalogRequests, testRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/webhooks/" + canonicalWebhookID:
			detailRequests++
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"error":"webhook not found"}`, http.StatusNotFound)
		case "/channels":
			catalogRequests++
			_, _ = io.WriteString(w, `<div data-webhook-id="`+canonicalWebhookID+`" data-webhook-name="Fallback Hook" data-webhook-token="token"></div>`)
		case "/channels/webhooks/" + canonicalWebhookID + "/test":
			testRequests++
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"task_id":"fallback-test"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL)
	m := New(c)
	m.selectedID, m.selectedName = "p1", "demo"
	m = runLine(t, m, "/webhooks test "+canonicalWebhookID)
	if detailRequests != 1 || catalogRequests != 1 || testRequests != 1 {
		t.Fatalf("detail/catalog/test = %d/%d/%d, want 1/1/1", detailRequests, catalogRequests, testRequests)
	}
	if out := transcript(m); !strings.Contains(out, "fallback-test") {
		t.Fatalf("fallback action output = %q", out)
	}
}

func TestWebhooksCanonicalDetailFailuresDoNotFallbackOrMutate(t *testing.T) {
	cases := []struct {
		name  string
		write func(http.ResponseWriter)
	}{
		{name: "authentication", write: func(w http.ResponseWriter) { w.Header().Set("Location", "/login"); w.WriteHeader(http.StatusFound) }},
		{name: "server", write: func(w http.ResponseWriter) {
			http.Error(w, `{"error":"backend unavailable"}`, http.StatusInternalServerError)
		}},
		{name: "malformed JSON", write: func(w http.ResponseWriter) { _, _ = io.WriteString(w, `{`) }},
		{name: "foreign project", write: func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, canonicalWebhookDetailJSON(canonicalWebhookID, "p2", "Foreign Hook"))
		}},
		{name: "mismatched identity", write: func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, canonicalWebhookDetailJSON("fedcba9876543210fedcba9876543210", "p1", "Wrong Hook"))
		}},
		{name: "transport", write: func(w http.ResponseWriter) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var catalogRequests, mutationRequests int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/channels/webhooks/" + canonicalWebhookID:
					tc.write(w)
				case "/channels":
					catalogRequests++
					_, _ = io.WriteString(w, webhookCardsHTML)
				case "/channels/webhooks/" + canonicalWebhookID + "/test":
					mutationRequests++
					_, _ = io.WriteString(w, `{"task_id":"must-not-run"}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			c, _ := client.New(srv.URL)
			m := New(c)
			m.selectedID, m.selectedName = "p1", "demo"
			m = runLine(t, m, "/webhooks test "+canonicalWebhookID)
			if catalogRequests != 0 || mutationRequests != 0 {
				t.Fatalf("terminal detail failure fell back or mutated: catalog=%d mutations=%d\n%s", catalogRequests, mutationRequests, transcript(m))
			}
		})
	}
}

func TestWebhooksCanonicalInvalidDetailDoesNotRenderOrUpdate(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "different ID",
			body: canonicalWebhookDetailJSON("fedcba9876543210fedcba9876543210", "p1", "Wrong Hook"),
		},
		{
			name: "missing ID",
			body: `{"project_id":"p1","name":"Missing ID Hook","enabled":true,"path_token":"missing-id-token","system_instructions":"keep system","title_template":"keep title","prompt_template":"keep prompt","default_priority":2,"agent_ids":["a1","a2"]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var catalogRequests, detailRequests, putRequests int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/channels/webhooks/" + canonicalWebhookID:
					switch r.Method {
					case http.MethodGet:
						detailRequests++
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, tc.body)
					case http.MethodPut:
						putRequests++
						w.WriteHeader(http.StatusNoContent)
					default:
						http.NotFound(w, r)
					}
				case "/channels":
					catalogRequests++
					http.Error(w, "detail failure must not scan catalog", http.StatusInternalServerError)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}

			showModel := New(c)
			showModel.selectedID, showModel.selectedName = "p1", "demo"
			showModel = runLine(t, showModel, "/channels webhooks show "+canonicalWebhookID)
			showOutput := transcript(showModel)
			if strings.Contains(showOutput, "Webhook:") || strings.Contains(showOutput, "Wrong Hook") || strings.Contains(showOutput, "Missing ID Hook") {
				t.Fatalf("show rendered rejected webhook detail:\n%s", showOutput)
			}

			editModel := New(c)
			editModel.selectedID, editModel.selectedName = "p1", "demo"
			editModel = runLine(t, editModel, "/channels webhooks edit "+canonicalWebhookID+" --name Renamed")
			editOutput := transcript(editModel)
			if strings.Contains(editOutput, "updated webhook") || strings.Contains(editOutput, "Wrong Hook") || strings.Contains(editOutput, "Missing ID Hook") {
				t.Fatalf("edit rendered rejected webhook detail:\n%s", editOutput)
			}
			if catalogRequests != 0 || detailRequests != 2 || putRequests != 0 {
				t.Fatalf("catalog/details/PUT = %d/%d/%d, want 0/2/0", catalogRequests, detailRequests, putRequests)
			}
		})
	}
}

func TestWebhooksCanonicalMismatchedDetailCannotMutate(t *testing.T) {
	for _, tc := range []struct {
		action string
		line   string
	}{
		{action: "edit", line: "/webhooks edit " + canonicalWebhookID + " --name Renamed"},
		{action: "test", line: "/webhooks test " + canonicalWebhookID},
		{action: "rotate", line: "/webhooks rotate " + canonicalWebhookID},
		{action: "delete", line: "/webhooks delete " + canonicalWebhookID},
	} {
		t.Run(tc.action, func(t *testing.T) {
			var catalogRequests, mutationRequests int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/channels/webhooks/" + canonicalWebhookID:
					if r.Method == http.MethodGet {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, canonicalWebhookDetailJSON("fedcba9876543210fedcba9876543210", "p1", "Wrong Hook"))
						return
					}
					mutationRequests++
				case "/channels/webhooks/" + canonicalWebhookID + "/test", "/channels/webhooks/" + canonicalWebhookID + "/rotate-secret":
					mutationRequests++
				case "/channels":
					catalogRequests++
				}
				http.NotFound(w, r)
			}))
			defer srv.Close()
			c, _ := client.New(srv.URL)
			m := New(c)
			m.selectedID, m.selectedName = "p1", "demo"
			m = runLine(t, m, tc.line)
			if catalogRequests != 0 || mutationRequests != 0 || m.pendingConfirmation != nil {
				t.Fatalf("mismatched detail reached catalog/mutation/confirmation: %d/%d/%#v", catalogRequests, mutationRequests, m.pendingConfirmation)
			}
			if out := strings.ToLower(transcript(m)); !strings.Contains(out, "does not match requested webhook") {
				t.Fatalf("missing mismatched-detail error:\n%s", transcript(m))
			}
		})
	}
}

func TestWebhooksCanonicalDeleteCapturesVerifiedTarget(t *testing.T) {
	var catalogRequests int
	var deletedID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/webhooks/" + canonicalWebhookID:
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, canonicalWebhookDetailJSON(canonicalWebhookID, "p1", "Captured Hook"))
				return
			}
			if r.Method == http.MethodDelete {
				deletedID = canonicalWebhookID
				w.WriteHeader(http.StatusNoContent)
				return
			}
		case "/channels":
			catalogRequests++
			_, _ = io.WriteString(w, `<div data-webhook-id="replacement" data-webhook-name="Replacement Hook"></div>`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL)
	m := New(c)
	m.selectedID, m.selectedName = "p1", "demo"
	m = runLine(t, m, "/webhooks delete "+canonicalWebhookID)
	if m.pendingConfirmation == nil || !strings.Contains(m.pendingConfirmation.message, `"Captured Hook"`) {
		t.Fatalf("confirmation did not capture verified detail: %#v", m.pendingConfirmation)
	}
	m = runLine(t, m, "yes")
	if catalogRequests != 0 || deletedID != canonicalWebhookID {
		t.Fatalf("catalog/deleted ID = %d/%q, want 0/%q", catalogRequests, deletedID, canonicalWebhookID)
	}
}

func TestWebhooksNoncanonicalReferencesKeepCatalogResolution(t *testing.T) {
	cases := []struct {
		name string
		ref  string
		id   string
	}{
		{name: "name", ref: "Named Hook", id: "name-id"},
		{name: "partial", ref: "unique", id: "partial-id"},
		{name: "uppercase ID", ref: strings.ToUpper(canonicalWebhookID), id: canonicalWebhookID},
		{name: "malformed ID", ref: "0123456789abcdef0123456789abcdeg", id: "malformed-id"},
		{name: "option-like reference", ref: canonicalWebhookID + " --priority 3", id: "option-id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var catalogRequests, detailRequests, testRequests int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/channels":
					catalogRequests++
					_, _ = io.WriteString(w, `<div data-webhook-id="name-id" data-webhook-name="Named Hook"></div><div data-webhook-id="partial-id" data-webhook-name="Unique Partial Hook"></div><div data-webhook-id="`+canonicalWebhookID+`" data-webhook-name="Uppercase ID Hook"></div><div data-webhook-id="malformed-id" data-webhook-name="0123456789abcdef0123456789abcdeg"></div><div data-webhook-id="option-id" data-webhook-name="`+canonicalWebhookID+` --priority 3"></div>`)
				case "/channels/webhooks/" + canonicalWebhookID:
					detailRequests++
					http.Error(w, "unexpected direct detail", http.StatusInternalServerError)
				case "/channels/webhooks/" + tc.id + "/test":
					testRequests++
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"task_id":"catalog-test"}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			c, _ := client.New(srv.URL)
			m := New(c)
			m.selectedID, m.selectedName = "p1", "demo"
			m = runLine(t, m, "/webhooks test "+tc.ref)
			if catalogRequests != 1 || detailRequests != 0 || testRequests != 1 {
				t.Fatalf("catalog/detail/test = %d/%d/%d, want 1/0/1\n%s", catalogRequests, detailRequests, testRequests, transcript(m))
			}
		})
	}
}

func webhookCatalogPageHTML(total, offset int) string {
	end := min(offset+50, total)
	var b strings.Builder
	b.Grow((end - offset) * 180)
	b.WriteString(`<div data-card-pagination-root data-card-pagination-card-selector="[data-webhook-id]" data-card-pagination-key="data-webhook-id">`)
	for i := offset; i < end; i++ {
		id, name := fmt.Sprintf("%032x", i+100000), fmt.Sprintf("Catalog Hook %04d", i)
		if i == total-1 {
			id, name = canonicalWebhookID, "Target Catalog Hook"
		}
		fmt.Fprintf(&b, `<div data-webhook-id=%q data-webhook-name=%q data-webhook-enabled="true" data-webhook-token="token-%04d" data-webhook-default-priority="2"></div>`, id, name, i)
	}
	b.WriteString(`</div>`)
	return b.String()
}

func TestWebhooksCanonicalIDCatalogPerformanceEvidence(t *testing.T) {
	const pageDelay = 5 * time.Millisecond
	for _, cards := range []int{10, 100, 1000} {
		t.Run(fmt.Sprintf("%d cards", cards), func(t *testing.T) {
			var catalogRequests atomic.Int64
			var catalogBytes atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/channels":
					catalogRequests.Add(1)
					offset, err := strconv.Atoi(r.URL.Query().Get("offset"))
					if err != nil && r.URL.Query().Get("offset") != "" {
						t.Errorf("invalid catalog offset: %v", err)
					}
					body := webhookCatalogPageHTML(cards, offset)
					catalogBytes.Add(int64(len(body)))
					w.Header().Set("X-OpenVibely-Card-Page-Has-More", strconv.FormatBool(offset+50 < cards))
					time.Sleep(pageDelay)
					_, _ = io.WriteString(w, body)
				case "/channels/webhooks/" + canonicalWebhookID:
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, canonicalWebhookDetailJSON(canonicalWebhookID, "p1", "Target Catalog Hook"))
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			runShow := func(ref string) {
				m := New(c)
				m.selectedID, m.selectedName = "p1", "demo"
				m = runLine(t, m, "/webhooks show "+ref)
				if out := strings.ToLower(transcript(m)); strings.Contains(out, "error:") {
					t.Fatalf("show %q failed:\n%s", ref, transcript(m))
				}
			}
			measure := func(ref string) (time.Duration, int64, int64) {
				beforeRequests, beforeBytes := catalogRequests.Load(), catalogBytes.Load()
				start := time.Now()
				runShow(ref)
				return time.Since(start), catalogRequests.Load() - beforeRequests, catalogBytes.Load() - beforeBytes
			}

			directLatency, directRequests, directBytes := measure(canonicalWebhookID)
			catalogLatency, nameRequests, nameBytes := measure("Target Catalog Hook")
			wantPages := int64((cards + 49) / 50)
			if directRequests != 0 || directBytes != 0 {
				t.Fatalf("known canonical ID catalog requests/bytes = %d/%d, want 0/0", directRequests, directBytes)
			}
			if nameRequests != wantPages || nameBytes <= 0 {
				t.Fatalf("name catalog requests/bytes = %d/%d, want %d/>0", nameRequests, nameBytes, wantPages)
			}
			if catalogLatency <= directLatency {
				t.Fatalf("controlled-delay latency did not improve: direct=%v catalog=%v", directLatency, catalogLatency)
			}

			directAllocs := testing.AllocsPerRun(1, func() { runShow(canonicalWebhookID) })
			catalogAllocs := testing.AllocsPerRun(1, func() { runShow("Target Catalog Hook") })
			if directAllocs >= catalogAllocs {
				t.Fatalf("canonical allocations %.0f, want less than catalog %.0f", directAllocs, catalogAllocs)
			}
			t.Logf("%d-card webhook catalog evidence: requests %d -> %d; bytes %d -> %d; latency %v -> %v; allocations %.0f -> %.0f", cards, nameRequests, directRequests, nameBytes, directBytes, catalogLatency, directLatency, catalogAllocs, directAllocs)
		})
	}
}

func BenchmarkWebhooksCanonicalIDCatalogScale(b *testing.B) {
	for _, cards := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("%d-cards", cards), func(b *testing.B) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/channels":
					offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
					w.Header().Set("X-OpenVibely-Card-Page-Has-More", strconv.FormatBool(offset+50 < cards))
					_, _ = io.WriteString(w, webhookCatalogPageHTML(cards, offset))
				case "/channels/webhooks/" + canonicalWebhookID:
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, canonicalWebhookDetailJSON(canonicalWebhookID, "p1", "Target Catalog Hook"))
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			c, err := client.New(srv.URL)
			if err != nil {
				b.Fatal(err)
			}
			run := func(ref string) {
				m := New(c)
				m.selectedID, m.selectedName = "p1", "demo"
				_, cmd := runWebhooks(m, []string{"show", ref})
				if cmd != nil {
					_ = cmd()
				}
			}
			b.Run("canonical-detail", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					run(canonicalWebhookID)
				}
			})
			b.Run("catalog-name", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					run("Target Catalog Hook")
				}
			})
		})
	}
}

func TestWebhooksDispatchCreate(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{
		"POST /channels/webhooks":   `{"id":"w3","project_id":"p1","secret":"create-secret-must-not-print"}`,
		"GET /channels/webhooks/w3": `{"id":"w3","project_id":"p1","name":"Incident Hook","enabled":true,"path_token":"incident-token","secret":"create-secret-must-not-print","system_instructions":"","title_template":"","prompt_template":"","default_priority":3,"agent_ids":["agent-1"]}`,
	})
	m = runLine(t, m, `/webhooks create "Incident Hook" --priority 3 --agents agent-1`)
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "created webhook") || !strings.Contains(out, "/webhooks/inbound/incident-token") {
		t.Fatalf("create output:\n%s", out)
	}
	if strings.Contains(out, "create-secret-must-not-print") {
		t.Fatalf("create output disclosed secret:\n%s", out)
	}
	for _, want := range []string{"name=Incident+Hook", "enabled=true", "default_priority=3", "agent_ids=agent-1"} {
		if !rec.sawForm(want) {
			t.Errorf("create form missing %q: %#v", want, rec.forms)
		}
	}
}

func TestWebhooksDispatchListShowEditAndTest(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{
		"/channels":                       webhookCardsHTML,
		"GET /channels/webhooks/w1":       webhookDetailJSON,
		"PUT /channels/webhooks/w1":       "",
		"POST /channels/webhooks/w1/test": `{"task_id":"task-created-123"}`,
	})

	m = runLine(t, m, "/webhooks list")
	m = runLine(t, m, "/webhooks show w1")
	m = runLine(t, m, "/webhooks edit w1 --name Renamed --enabled true")
	m = runLine(t, m, "/webhooks test w1")
	out := stripANSI(transcript(m))
	for _, want := range []string{"Pager Duty", "/webhooks/inbound/safe-token", "URL:", "updated webhook", "test task created: task-created-123"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "never-print-this") || strings.Contains(out, "secret\":") {
		t.Fatalf("ordinary webhook output disclosed a secret:\n%s", out)
	}
	for _, want := range []string{"name=Renamed", "enabled=true", "system_instructions=keep+system", "title_template=keep+title", "prompt_template=keep+prompt", "default_priority=4", "agent_ids=a1%2Ca2"} {
		if !rec.sawForm(want) {
			t.Errorf("preserved edit form missing %q; forms: %#v", want, rec.forms)
		}
	}
	if !rec.sawQuery("project_id=p1") {
		t.Fatalf("webhook requests were not project scoped: %#v", rec.urlsSnapshot())
	}
}

func TestWebhooksOptionLikeTrailingOperandsAreNotDiscarded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action string
		tail   string
	}{
		{name: "unknown option", action: "show", tail: "--bogus value"},
		{name: "malformed known option", action: "test", tail: "--enabled maybe"},
		{name: "missing option value", action: "rotate", tail: "--enabled"},
		{name: "surplus option pair", action: "delete", tail: "--priority 3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/channels": webhookCardsHTML})
			m = runLine(t, m, "/webhooks "+tc.action+" w1 "+tc.tail)
			if m.pendingConfirmation != nil {
				t.Fatalf("surplus operands opened confirmation: %q", m.pendingConfirmation.message)
			}
			if out := strings.ToLower(transcript(m)); !strings.Contains(out, "nothing matches") {
				t.Fatalf("surplus operands were not rejected as part of the reference:\n%s", transcript(m))
			}
			for _, call := range []struct{ method, path string }{
				{"GET", "/channels/webhooks/w1"},
				{"POST", "/channels/webhooks/w1/test"},
				{"POST", "/channels/webhooks/w1/rotate-secret"},
				{"DELETE", "/channels/webhooks/w1"},
			} {
				if rec.saw(call.method, call.path) {
					t.Fatalf("surplus operands dispatched %s %s; calls:\n%s", call.method, call.path, rec.all())
				}
			}
		})
	}
}

func TestWebhooksOptionLikeTokensRemainPartOfExactName(t *testing.T) {
	const cards = `<div data-webhook-id="w-opt" data-webhook-name="Hook --enabled maybe" data-webhook-token="opt-token"></div><div data-webhook-id="w-short" data-webhook-name="Hook" data-webhook-token="short-token"></div>`
	const detail = `{"id":"w-opt","project_id":"p1","name":"Hook --enabled maybe","enabled":true,"path_token":"opt-token","system_instructions":"","title_template":"","prompt_template":"","default_priority":2,"agent_ids":[]}`
	for _, action := range []string{"show", "test", "rotate", "delete"} {
		t.Run(action, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{
				"/channels":                          cards,
				"GET /channels/webhooks/w-opt":       detail,
				"POST /channels/webhooks/w-opt/test": `{"task_id":"task-option-name"}`,
			})
			m = runLine(t, m, "/webhooks "+action+" Hook --enabled maybe")
			switch action {
			case "show":
				if !rec.saw("GET", "/channels/webhooks/w-opt") || rec.saw("GET", "/channels/webhooks/w-short") {
					t.Fatalf("show did not resolve the full exact name; calls:\n%s", rec.all())
				}
			case "test":
				if !rec.saw("POST", "/channels/webhooks/w-opt/test") || rec.saw("POST", "/channels/webhooks/w-short/test") {
					t.Fatalf("test did not resolve the full exact name; calls:\n%s", rec.all())
				}
			case "rotate", "delete":
				if m.pendingConfirmation == nil || !strings.Contains(m.pendingConfirmation.message, `"Hook --enabled maybe"`) {
					t.Fatalf("%s did not resolve the full canonical name: %#v", action, m.pendingConfirmation)
				}
			}
		})
	}
}

func TestWebhooksAmbiguousAndForeignRefsDoNotMutate(t *testing.T) {
	for _, line := range []string{"/webhooks test pager", "/webhooks test foreign-id"} {
		t.Run(line, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/channels": webhookCardsHTML})
			m = runLine(t, m, line)
			if rec.count("POST", "/channels/webhooks/w1/test")+rec.count("POST", "/channels/webhooks/w2/test") != 0 {
				t.Fatalf("invalid reference mutated:\n%s", rec.all())
			}
			out := strings.ToLower(stripANSI(transcript(m)))
			if !strings.Contains(out, "ambiguous") && !strings.Contains(out, "nothing matches") {
				t.Fatalf("missing reference error:\n%s", out)
			}
		})
	}
}

func TestWebhooksDestructiveReferencesResolveBeforeConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want string
	}{
		{name: "unknown rotate", line: "/webhooks rotate foreign-id", want: "nothing matches"},
		{name: "ambiguous delete", line: "/webhooks delete pager", want: "ambiguous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/channels": webhookCardsHTML})
			m = runLine(t, m, tc.line)
			if m.pendingConfirmation != nil {
				t.Fatalf("invalid reference opened confirmation: %q", m.pendingConfirmation.message)
			}
			if !strings.Contains(strings.ToLower(transcript(m)), tc.want) {
				t.Fatalf("missing matching error %q:\n%s", tc.want, transcript(m))
			}
			if rec.saw("POST", "/channels/webhooks/w1/rotate-secret") || rec.saw("DELETE", "/channels/webhooks/w1") || rec.saw("DELETE", "/channels/webhooks/w2") {
				t.Fatalf("invalid reference mutated:\n%s", rec.all())
			}
		})
	}

	t.Run("unique partial captures canonical target", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{
			"/channels": webhookCardsHTML,
			"POST /channels/webhooks/w1/rotate-secret": `{"secret":"new-secret"}`,
		})
		m = runLine(t, m, "/webhooks rotate duty")
		if m.pendingConfirmation == nil || !strings.Contains(m.pendingConfirmation.message, `"Pager Duty"`) || strings.Contains(m.pendingConfirmation.message, `"duty"`) {
			t.Fatalf("confirmation did not use canonical target: %#v", m.pendingConfirmation)
		}
		if got := rec.count("GET", "/channels"); got != 1 {
			t.Fatalf("resolution requests = %d, want 1; calls:\n%s", got, rec.all())
		}
		m = runLine(t, m, "yes")
		if !rec.saw("POST", "/channels/webhooks/w1/rotate-secret") {
			t.Fatalf("confirmed captured target did not mutate w1:\n%s", rec.all())
		}
		if got := rec.count("GET", "/channels"); got != 1 {
			t.Fatalf("confirmed action rebound target with %d discoveries; calls:\n%s", got, rec.all())
		}
	})

	t.Run("canonical id uses canonical name", func(t *testing.T) {
		m, _ := dispatchModel(t, map[string]string{"/channels": webhookCardsHTML})
		m = runLine(t, m, "/webhooks delete w1")
		if m.pendingConfirmation == nil || !strings.Contains(m.pendingConfirmation.message, `"Pager Duty"`) {
			t.Fatalf("canonical ID confirmation = %#v", m.pendingConfirmation)
		}
	})
}

func TestWebhookReferenceErrorsAreTerminalSafeRaw(t *testing.T) {
	tests := []struct {
		name  string
		cards string
		ref   string
		want  string
	}{
		{
			name:  "backend controlled ambiguous names",
			cards: `<div data-webhook-id="w1" data-webhook-name="Bad&#10;&#13;&#9;&#1;Ref One"></div><div data-webhook-id="w2" data-webhook-name="Bad&#10;&#13;&#9;&#1;Ref Two"></div>`,
			ref:   "Bad",
			want:  "ambiguous",
		},
		{
			name:  "user controlled missing reference",
			cards: webhookCardsHTML,
			ref:   "Missing\x1b[31m\n\r\t\x01Ref",
			want:  "nothing matches",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := dispatchModel(t, map[string]string{"/channels": tc.cards})
			m = runLine(t, m, `/webhooks test "`+tc.ref+`"`)
			if len(m.log) == 0 {
				t.Fatal("missing reference error entry")
			}
			out := m.log[len(m.log)-1].text
			if !strings.Contains(strings.ToLower(out), tc.want) {
				t.Fatalf("expected reference error %q: %q", tc.want, out)
			}
			for _, forbidden := range []string{"\x1b", "\n", "\r", "\t", "\x01"} {
				if strings.Contains(out, forbidden) {
					t.Fatalf("raw reference error contains terminal control %q: %q", forbidden, out)
				}
			}
		})
	}
}

func TestWebhooksRotateDeleteConfirmationAndSanitization(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{
		"/channels": webhookCardsHTML,
		"POST /channels/webhooks/w1/rotate-secret": `{"secret":"new-secret\u001b[31m\nvalue"}`,
		"DELETE /channels/webhooks/w1":             "",
	})
	m = runLine(t, m, "/channels webhooks rotate w1")
	if rec.saw("POST", "/channels/webhooks/w1/rotate-secret") {
		t.Fatal("rotation ran before confirmation")
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if rec.saw("POST", "/channels/webhooks/w1/rotate-secret") {
		t.Fatal("cancelled rotation mutated")
	}
	m = confirmDestructive(t, m, "/channels webhooks rotate w1")
	if !rec.saw("POST", "/channels/webhooks/w1/rotate-secret") {
		t.Fatal("confirmed rotation did not run")
	}
	out := transcript(m)
	if strings.Contains(out, "\x1b[31m") || !strings.Contains(stripANSI(out), "new-secret value") {
		t.Fatalf("rotation output was not terminal safe:\n%q", out)
	}

	m = runLine(t, m, "/channels webhooks delete w1")
	if rec.saw("DELETE", "/channels/webhooks/w1") {
		t.Fatal("delete ran before confirmation")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if rec.saw("DELETE", "/channels/webhooks/w1") {
		t.Fatal("cancelled delete mutated")
	}
}

func TestWebhooksInvalidOptionsFailBeforeRequests(t *testing.T) {
	for _, line := range []string{"/webhooks create hook --enabled maybe", "/webhooks edit w1 --priority 5", "/webhooks edit w1 --name"} {
		m, rec := dispatchModel(t, nil)
		m = runLine(t, m, line)
		if got := rec.all(); got != "" {
			t.Fatalf("%q made requests:\n%s", line, got)
		}
	}
}

func TestWebhooksRequireSelectedProjectBeforeDiscovery(t *testing.T) {
	for _, line := range []string{"/webhooks", "/webhooks show w1", "/webhooks create hook", "/webhooks edit w1 --enabled true", "/webhooks test w1", "/webhooks rotate w1", "/webhooks delete w1"} {
		m, rec := dispatchModel(t, nil)
		m.selectedID = ""
		m = runLine(t, m, line)
		if got := rec.all(); got != "" {
			t.Fatalf("%q made requests without a project:\n%s", line, got)
		}
		if !strings.Contains(stripANSI(transcript(m)), "no project selected") {
			t.Fatalf("%q missing project guidance:\n%s", line, transcript(m))
		}
		if m.selectorActive || m.pendingConfirmation != nil {
			t.Fatalf("%q opened selector/confirmation without a project", line)
		}
	}
}

func TestSkillsShowCanonicalHandleUsesOneScopedDetailRequest(t *testing.T) {
	var listRequests, detailRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/skills":
			listRequests++
			http.Error(w, "catalog scan should not be needed", http.StatusInternalServerError)
		case "/skills/deploy/details":
			detailRequests++
			if got := r.URL.Query().Get("project_id"); got != "p1" {
				t.Errorf("project_id = %q, want p1", got)
			}
			if got := r.URL.Query().Get("scope"); got != "project" {
				t.Errorf("scope = %q, want project", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"handle":"deploy","name":"Deploy","description":"ship safely","scope":"project","source":"project","content":"# Deploy\nRun the release checklist.","enabled":true,"always_use":false}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL)
	m := New(c)
	m.selectedID, m.selectedName = "p1", "project one"

	m = runLine(t, m, "/skills show deploy")
	if listRequests != 0 || detailRequests != 1 {
		t.Fatalf("list requests = %d, detail requests = %d; want 0 and 1", listRequests, detailRequests)
	}
	out := stripANSI(transcript(m))
	for _, want := range []string{"Deploy", "Run the release checklist.", "scope project", "source project"} {
		if !strings.Contains(out, want) {
			t.Errorf("show output missing %q:\n%s", want, out)
		}
	}
}

func TestSkillsShowCanonicalGlobalSkillFallsBackWhenProjectScopeUnavailable(t *testing.T) {
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RequestURI())
		switch r.URL.Path {
		case "/skills/global-review/details":
			switch r.URL.Query().Get("scope") {
			case "project":
				w.Header().Set("Content-Type", "application/json")
				http.Error(w, `{"message":"project skill root not configured"}`, http.StatusServiceUnavailable)
			case "global":
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"handle":"global-review","name":"Global Review","scope":"global","source":"global","content":"global instruction body","enabled":true}`)
			default:
				http.Error(w, "unexpected detail scope", http.StatusBadRequest)
			}
		case "/skills":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<div><div data-skill-handle="global-review" data-skill-name="Global Review" data-skill-scope="global" data-skill-source="global" data-skill-enabled="true"></div></div>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL)
	m := New(c)
	m.selectedID = "p1"

	m = runLine(t, m, "/skills show global-review")
	if want := []string{
		"/skills/global-review/details?project_id=p1&scope=project",
		"/skills?project_id=p1",
		"/skills/global-review/details?project_id=p1&scope=global",
	}; !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests = %v, want %v", requests, want)
	}
	if out := stripANSI(transcript(m)); !strings.Contains(out, "global instruction body") {
		t.Fatalf("show output = %q, want global detail", out)
	}
}

func TestResolveSkillForShowDoesNotFallbackFromAuthenticationOrTransport(t *testing.T) {
	t.Run("authentication", func(t *testing.T) {
		var listRequests int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/skills" {
				listRequests++
			}
			http.Redirect(w, r, "/login?next=%2Fskills", http.StatusFound)
		}))
		defer srv.Close()
		c, _ := client.New(srv.URL)

		_, err := resolveSkillForShow(context.Background(), c, "p1", "global-review")
		if !client.IsAuthRequired(err) {
			t.Fatalf("error = %v, want authentication required", err)
		}
		if listRequests != 0 {
			t.Fatalf("list requests = %d, want 0", listRequests)
		}
	})

	t.Run("transport", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		c, _ := client.New(srv.URL)
		srv.Close()

		_, err := resolveSkillForShow(context.Background(), c, "p1", "global-review")
		if !client.IsTransportError(err) {
			t.Fatalf("error = %v, want transport error", err)
		}
		if !strings.Contains(err.Error(), "/skills/global-review/details") {
			t.Fatalf("error = %v, want initial detail request failure", err)
		}
	})
}

func TestSkillsShowResolvesSummaryThenFetchesOnlySelectedDetail(t *testing.T) {
	var listRequests int
	var detailHandles []string
	const listBody = `<div>
		<div data-skill-handle="deploy" data-skill-name="Deploy Skill" data-skill-description="releases" data-skill-scope="project" data-skill-source="project" data-skill-enabled="true" data-skill-content="list body must not render"></div>
		<div data-skill-handle="review" data-skill-name="Review Skill" data-skill-description="reviews" data-skill-scope="global" data-skill-source="global" data-skill-enabled="false"></div>
	</div>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/skills":
			listRequests++
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, listBody)
		case "/skills/deploy/details":
			detailHandles = append(detailHandles, "deploy")
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"handle":"deploy","name":"Deploy Skill","scope":"project","source":"project","content":"selected detail body","enabled":true}`)
		case "/skills/review/details":
			detailHandles = append(detailHandles, "review")
			http.Error(w, "unselected detail", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL)
	m := New(c)
	m.selectedID = "p1"

	m = runLine(t, m, "/skills show Deploy Skill")
	if listRequests != 1 || !reflect.DeepEqual(detailHandles, []string{"deploy"}) {
		t.Fatalf("list requests = %d, detail handles = %v", listRequests, detailHandles)
	}
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "selected detail body") || strings.Contains(out, "list body must not render") {
		t.Fatalf("show output did not use deferred selected body:\n%s", out)
	}
}

func TestSkillsShowUnknownAndAmbiguousReferencesDoNotFetchSelectedBodies(t *testing.T) {
	const listBody = `<div>
		<div data-skill-handle="deploy-one" data-skill-name="Deploy One" data-skill-scope="project"></div>
		<div data-skill-handle="deploy-two" data-skill-name="Deploy Two" data-skill-scope="project"></div>
	</div>`
	for _, tc := range []struct {
		name string
		ref  string
		want string
	}{
		{name: "ambiguous", ref: "deploy", want: "ambiguous"},
		{name: "unknown", ref: "missing", want: "nothing matches"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var selectedDetails int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/skills":
					w.Header().Set("Content-Type", "text/html")
					_, _ = io.WriteString(w, listBody)
				case "/skills/deploy/details", "/skills/missing/details":
					http.Error(w, `{"error":"skill not found"}`, http.StatusNotFound)
				default:
					selectedDetails++
					http.Error(w, "unexpected selected detail", http.StatusInternalServerError)
				}
			}))
			defer srv.Close()
			c, _ := client.New(srv.URL)
			m := New(c)
			m.selectedID = "p1"

			m = runLine(t, m, "/skills show "+tc.ref)
			if selectedDetails != 0 {
				t.Fatalf("selected detail requests = %d, want 0", selectedDetails)
			}
			if out := stripANSI(transcript(m)); !strings.Contains(strings.ToLower(out), tc.want) {
				t.Fatalf("output = %q, want %q", out, tc.want)
			}
		})
	}
}

func TestCLISkillsShowJSONResolvesReferencesAndPreservesCompleteContent(t *testing.T) {
	const listBody = `<div>
		<div data-skill-handle="deploy" data-skill-name="Deploy" data-skill-description="release" data-skill-scope="project" data-skill-source="project" data-skill-enabled="true" data-skill-always-use="false"></div>
		<div data-skill-handle="review" data-skill-name="Review Skill" data-skill-description="review" data-skill-scope="global" data-skill-source="global" data-skill-enabled="false" data-skill-always-use="true"></div>
		<div data-skill-handle="shipping-checklist" data-skill-name="Shipping Checklist Skill" data-skill-description="ship" data-skill-scope="project" data-skill-source="project" data-skill-enabled="true" data-skill-always-use="false"></div>
		<div data-skill-handle="release-gate" data-skill-name="Release Gate for Production" data-skill-description="gate" data-skill-scope="project" data-skill-source="project" data-skill-enabled="true" data-skill-always-use="false"></div>
	</div>`
	const completeContent = "# Selected skill\n\nRun step one.\nRun step two.\n"
	cases := []struct {
		name          string
		ref           string
		wantHandle    string
		wantScope     string
		wantList      bool
		wantDetailRef string
	}{
		{name: "exact handle", ref: "deploy", wantHandle: "deploy", wantScope: "project", wantDetailRef: "deploy"},
		{name: "exact name", ref: "Review Skill", wantHandle: "review", wantScope: "global", wantList: true, wantDetailRef: "review"},
		{name: "unique prefix", ref: "Shipping Checklist", wantHandle: "shipping-checklist", wantScope: "project", wantList: true, wantDetailRef: "shipping-checklist"},
		{name: "unique substring", ref: "Gate for", wantHandle: "release-gate", wantScope: "project", wantList: true, wantDetailRef: "release-gate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var listRequests, detailRequests int
			var detailHandle, detailScope string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/projects":
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, cliProjects)
				case "/skills":
					listRequests++
					w.Header().Set("Content-Type", "text/html")
					_, _ = io.WriteString(w, listBody)
				case "/skills/" + tc.wantDetailRef + "/details":
					detailRequests++
					detailHandle = r.URL.Path[len("/skills/") : len(r.URL.Path)-len("/details")]
					detailScope = r.URL.Query().Get("scope")
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(w, `{"handle":%q,"name":%q,"description":"selected","scope":%q,"source":%q,"content":%q,"enabled":true,"always_use":false}`,
						tc.wantHandle, tc.name, tc.wantScope, tc.wantScope, completeContent)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}

			var out bytes.Buffer
			if err := RunCLI(c, &out, "demo", []string{"skills", "show", tc.ref}, false, true); err != nil {
				t.Fatalf("skills show --json failed: %v", err)
			}
			wantListRequests := 0
			if tc.wantList {
				wantListRequests = 1
			}
			if listRequests != wantListRequests || detailRequests != 1 {
				t.Fatalf("list requests = %d, detail requests = %d; want %d and 1", listRequests, detailRequests, wantListRequests)
			}
			if detailHandle != tc.wantDetailRef || detailScope != tc.wantScope {
				t.Fatalf("detail request = %s scope %s; want %s scope %s", detailHandle, detailScope, tc.wantDetailRef, tc.wantScope)
			}
			raw := out.String()
			if strings.Contains(raw, "\x1b") || !strings.HasPrefix(strings.TrimSpace(raw), "{") {
				t.Fatalf("JSON output contains styling or an extra header: %q", raw)
			}
			var skill client.Skill
			if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &skill); err != nil {
				t.Fatalf("output is not one JSON skill object: %q: %v", raw, err)
			}
			if skill.Handle != tc.wantHandle || skill.Scope != tc.wantScope || skill.Content != completeContent {
				t.Fatalf("skill = %+v; want handle %q scope %q and complete content", skill, tc.wantHandle, tc.wantScope)
			}
			for _, key := range []string{`"handle"`, `"description"`, `"scope"`, `"source"`, `"content"`, `"enabled"`, `"always_use"`} {
				if !strings.Contains(raw, key) {
					t.Errorf("JSON output missing stable key %s: %s", key, raw)
				}
			}
		})
	}

	c, _ := cliServer(t, map[string]string{
		"/api/projects":          cliProjects,
		"/skills":                listBody,
		"/skills/deploy/details": `{"handle":"deploy","name":"Deploy","scope":"project","source":"project","content":"plain body","enabled":true}`,
	})
	var plain bytes.Buffer
	if err := RunCLI(c, &plain, "demo", []string{"skills", "show", "deploy"}, false, false); err != nil {
		t.Fatalf("plain skills show failed: %v", err)
	}
	if got := plain.String(); strings.HasPrefix(strings.TrimSpace(got), "{") || !strings.Contains(stripANSI(got), "plain body") {
		t.Fatalf("plain show output = %q", got)
	}
}

func TestCLISkillsShowJSONFailuresDoNotEmitSuccessfulPayload(t *testing.T) {
	const listBody = `<div>
		<div data-skill-handle="deploy-one" data-skill-name="Deploy One" data-skill-scope="project" data-skill-source="project"></div>
		<div data-skill-handle="deploy-two" data-skill-name="Deploy Two" data-skill-scope="project" data-skill-source="project"></div>
	</div>`
	for _, tc := range []struct {
		name      string
		ref       string
		handler   func(http.ResponseWriter, *http.Request)
		wantError string
	}{
		{
			name: "ambiguous reference",
			ref:  "deploy",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/skills" {
					w.Header().Set("Content-Type", "text/html")
					_, _ = io.WriteString(w, listBody)
					return
				}
				http.NotFound(w, r)
			},
			wantError: "ambiguous",
		},
		{
			name: "unknown reference",
			ref:  "missing",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/skills" {
					w.Header().Set("Content-Type", "text/html")
					_, _ = io.WriteString(w, listBody)
					return
				}
				http.NotFound(w, r)
			},
			wantError: "nothing matches",
		},
		{
			name: "authentication failure",
			ref:  "deploy",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", "/login")
				w.WriteHeader(http.StatusFound)
			},
			wantError: "requires sign-in",
		},
		{
			name: "detail identity mismatch",
			ref:  "deploy",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/skills/deploy/details" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"handle":"other","name":"Other","scope":"project","content":"must not render"}`)
			},
			wantError: "mismatched skill detail",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/projects" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, cliProjects)
					return
				}
				tc.handler(w, r)
			}))
			defer srv.Close()
			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err = RunCLI(c, &out, "demo", []string{"skills", "show", tc.ref}, false, true)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.wantError)) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantError)
			}
			if out.Len() != 0 {
				t.Fatalf("failure emitted misleading successful payload: %q", out.String())
			}
		})
	}
}

func TestSkillsListJSONKeepsFullContentContract(t *testing.T) {
	var listRequests, detailRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"projects":[{"id":"p1","name":"project one"}]}`)
		case "/skills":
			listRequests++
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<div><div data-skill-handle="deploy" data-skill-name="Deploy" data-skill-scope="project" data-skill-source="project" data-skill-enabled="true"></div></div>`)
		case "/skills/deploy/details":
			detailRequests++
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"handle":"deploy","name":"Deploy","scope":"project","source":"project","content":"complete instruction body","enabled":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL)
	var out strings.Builder

	if err := RunCLI(c, &out, "p1", []string{"skills", "list"}, false, true); err != nil {
		t.Fatal(err)
	}
	var skills []client.Skill
	if err := json.Unmarshal([]byte(out.String()), &skills); err != nil {
		t.Fatalf("JSON output = %q: %v", out.String(), err)
	}
	if len(skills) != 1 || skills[0].Content != "complete instruction body" {
		t.Fatalf("JSON skills = %+v", skills)
	}
	if listRequests != 1 || detailRequests != 1 {
		t.Fatalf("list requests = %d, detail requests = %d", listRequests, detailRequests)
	}
}
