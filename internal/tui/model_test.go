package tui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-tui/internal/client"
)

// newTestModel returns a sized model wired to a stub server.
func newTestModel(t *testing.T) Model {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	return updated.(Model)
}

// typeLine types text into the input and presses enter.
func typeLine(t *testing.T, m Model, text string) (Model, tea.Cmd) {
	t.Helper()
	for _, r := range text {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(Model)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return next.(Model), cmd
}

func transcript(m Model) string {
	var b strings.Builder
	for _, e := range m.log {
		b.WriteString(e.role + ":" + e.head + ":" + e.text + "\n")
	}
	return b.String()
}

func TestPlainTextRequiresProject(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeLine(t, m, "hello there")

	if !strings.Contains(transcript(m), "no project selected") {
		t.Fatalf("expected a project error, got:\n%s", transcript(m))
	}
}

func TestPlainTextSendsChatWhenProjectSelected(t *testing.T) {
	m := newTestModel(t)
	m.selectedID = "p1"
	m.selectedName = "demo"

	m, cmd := typeLine(t, m, "build the thing")
	if cmd == nil {
		t.Fatal("expected a send command")
	}
	if !strings.Contains(transcript(m), "you::build the thing") {
		t.Errorf("user message missing from transcript:\n%s", transcript(m))
	}
	if !m.busy {
		t.Error("model should be busy while the chat is in flight")
	}
}

func TestSlashCommandMenuAppearsAndCompletes(t *testing.T) {
	m := newTestModel(t)
	for _, r := range "/ta" {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(Model)
	}
	if len(m.menu) == 0 {
		t.Fatal("expected a command menu for /ta")
	}
	if m.menu[0].name != "tasks" {
		t.Errorf("first suggestion = %q, want tasks", m.menu[0].name)
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(Model)
	if got := m.input.Value(); !strings.HasPrefix(got, "/tasks") {
		t.Errorf("after tab input = %q, want /tasks…", got)
	}
	if !strings.Contains(m.View(), "tasks") {
		t.Error("menu should be visible in the view")
	}
}

func TestUnknownCommandReportsError(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeLine(t, m, "/nonsense")

	if !strings.Contains(transcript(m), "unknown command") {
		t.Fatalf("expected unknown-command error, got:\n%s", transcript(m))
	}
}

func TestCommandAliasesResolve(t *testing.T) {
	cases := map[string]string{
		"t": "tasks", "board": "tasks", "task": "tasks",
		"alert": "alerts", "skill": "skills", "model": "models",
		"upcoming": "pulse", "history": "reflection", "stats": "analytics",
		"q": "quit", "?": "help", "log": "events",
	}
	for alias, want := range cases {
		c := lookupCommand(alias)
		if c == nil {
			t.Errorf("alias %q did not resolve", alias)
			continue
		}
		if c.name != want {
			t.Errorf("alias %q → %q, want %q", alias, c.name, want)
		}
	}
}

func TestHelpListsEveryCommand(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeLine(t, m, "/help")

	out := transcript(m)
	for _, name := range []string{
		"tasks", "schedule", "alerts", "skills", "agents", "models", "workers",
		"channels", "personality", "pulse", "reflection", "grades", "insights",
		"analytics", "projects", "status", "events",
	} {
		if !strings.Contains(out, "/"+name) {
			t.Errorf("help is missing /%s", name)
		}
	}
}

func TestHelpForOneCommandShowsActions(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeLine(t, m, "/help tasks")

	out := transcript(m)
	if !strings.Contains(out, "run") || !strings.Contains(out, "delete") {
		t.Errorf("expected task actions in help, got:\n%s", out)
	}
}

func TestProjectCommandSelectsByName(t *testing.T) {
	m := newTestModel(t)
	m.projects = []client.Project{
		{ID: "p1", Name: "alpha"},
		{ID: "p2", Name: "beta service"},
	}

	m, _ = typeLine(t, m, "/project beta")
	if m.selectedID != "p2" {
		t.Fatalf("selected = %q, want p2", m.selectedID)
	}
	if !strings.Contains(transcript(m), "beta service") {
		t.Errorf("expected confirmation, got:\n%s", transcript(m))
	}

	m, _ = typeLine(t, m, "/project nope")
	if !strings.Contains(transcript(m), "no project matching") {
		t.Errorf("expected a not-found message, got:\n%s", transcript(m))
	}
}

// An exact name must win over a longer project that merely contains it, and
// the listing order must not decide the winner.
func TestProjectCommandPrefersExactName(t *testing.T) {
	for _, order := range [][]client.Project{
		{{ID: "p1", Name: "OpenVibely Chrome Plugin"}, {ID: "p2", Name: "openvibely"}},
		{{ID: "p2", Name: "openvibely"}, {ID: "p1", Name: "OpenVibely Chrome Plugin"}},
	} {
		m := newTestModel(t)
		m.projects = order

		m, _ = typeLine(t, m, "/project openvibely")
		if m.selectedID != "p2" {
			t.Fatalf("order %v: selected %q (%s), want p2 (openvibely)",
				[]string{order[0].Name, order[1].Name}, m.selectedID, m.selectedName)
		}
	}
}

// A prefix that matches several projects and none exactly must refuse to guess.
func TestProjectCommandRejectsAmbiguousName(t *testing.T) {
	m := newTestModel(t)
	m.projects = []client.Project{
		{ID: "p1", Name: "OpenVibely Chrome Plugin"},
		{ID: "p2", Name: "OpenVibely TUI"},
	}

	m, _ = typeLine(t, m, "/project openvibely")
	if m.selectedID != "" {
		t.Fatalf("ambiguous ref selected %q, want no selection", m.selectedName)
	}
	out := transcript(m)
	if !strings.Contains(out, "matches 2 projects") ||
		!strings.Contains(out, "OpenVibely TUI") {
		t.Errorf("expected candidates to be listed, got:\n%s", out)
	}
}

// When projects are not loaded yet, "/project <name>" defers to the load and
// must still resolve the requested name rather than defaulting to the first
// project in the response.
func TestProjectCommandResolvesAfterDeferredLoad(t *testing.T) {
	m := newTestModel(t)

	m, _ = typeLine(t, m, "/project docs")
	if m.selectedID != "" {
		t.Fatalf("selected %q before projects loaded", m.selectedID)
	}

	updated, _ := m.Update(projectsLoadedMsg{
		projects: []client.Project{
			{ID: "p1", Name: "OpenVibely Chrome Plugin"},
			{ID: "p2", Name: "docs site"},
		},
		selectName: "docs",
	})
	m = updated.(Model)

	if m.selectedID != "p2" {
		t.Fatalf("selected = %q (%s), want p2", m.selectedID, m.selectedName)
	}
}

// An unknown name during a deferred load must select nothing at all, not the
// first project in the list.
func TestProjectCommandDeferredLoadDoesNotFallBack(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeLine(t, m, "/project nope")

	updated, _ := m.Update(projectsLoadedMsg{
		projects:   []client.Project{{ID: "p1", Name: "alpha"}},
		selectName: "nope",
	})
	m = updated.(Model)

	if m.selectedID != "" {
		t.Fatalf("selected %q for an unknown name", m.selectedName)
	}
	if !strings.Contains(transcript(m), "no project matching") {
		t.Errorf("expected a not-found message, got:\n%s", transcript(m))
	}
}

// A unique prefix still resolves without needing the full name.
func TestProjectCommandAcceptsUniquePrefix(t *testing.T) {
	m := newTestModel(t)
	m.projects = []client.Project{
		{ID: "p1", Name: "OpenVibely Chrome Plugin"},
		{ID: "p2", Name: "docs site"},
	}

	m, _ = typeLine(t, m, "/project openvibely")
	if m.selectedID != "p1" {
		t.Fatalf("selected = %q, want p1", m.selectedID)
	}
}

// A substring that is not a prefix resolves only when it is unique.
func TestProjectCommandAcceptsUniqueSubstring(t *testing.T) {
	m := newTestModel(t)
	m.projects = []client.Project{
		{ID: "p1", Name: "OpenVibely Chrome Plugin"},
		{ID: "p2", Name: "docs site"},
	}

	m, _ = typeLine(t, m, "/project chrome")
	if m.selectedID != "p1" {
		t.Fatalf("selected = %q, want p1", m.selectedID)
	}
}

func TestEventsCommandToggles(t *testing.T) {
	m := newTestModel(t)
	if m.showEvents {
		t.Fatal("events should start off")
	}
	m, _ = typeLine(t, m, "/events")
	if !m.showEvents {
		t.Error("/events should turn the stream on")
	}
	m, _ = typeLine(t, m, "/events off")
	if m.showEvents {
		t.Error("/events off should turn it back off")
	}
}

func TestClearEmptiesTranscript(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeLine(t, m, "/help")
	if len(m.log) == 0 {
		t.Fatal("expected transcript entries")
	}
	m, _ = typeLine(t, m, "/clear")
	if len(m.log) != 0 {
		t.Errorf("transcript should be empty, has %d entries", len(m.log))
	}
}

func TestQuitCommandStopsProgram(t *testing.T) {
	m := newTestModel(t)
	m, cmd := typeLine(t, m, "/quit")
	if cmd == nil {
		t.Fatal("expected a quit command")
	}
	if !m.quitting {
		t.Error("model should be quitting")
	}
}

func TestInputHistoryRecall(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeLine(t, m, "/status")
	m, _ = typeLine(t, m, "/help")

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(Model)
	if m.input.Value() != "/help" {
		t.Errorf("first recall = %q, want /help", m.input.Value())
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(Model)
	if m.input.Value() != "/status" {
		t.Errorf("second recall = %q, want /status", m.input.Value())
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(Model)
	if m.input.Value() != "/help" {
		t.Errorf("forward recall = %q, want /help", m.input.Value())
	}
}

func TestResultMessageRendersBlock(t *testing.T) {
	m := newTestModel(t)
	m.busy = true
	next, _ := m.Update(resultMsg{title: "Tasks", body: "one task"})
	m = next.(Model)

	if m.busy {
		t.Error("result should clear the busy flag")
	}
	if !strings.Contains(transcript(m), "result:Tasks:one task") {
		t.Errorf("unexpected transcript:\n%s", transcript(m))
	}
}

func TestResultErrorRendersError(t *testing.T) {
	m := newTestModel(t)
	next, _ := m.Update(resultMsg{err: errTest})
	m = next.(Model)

	if !strings.Contains(transcript(m), "error::boom") {
		t.Errorf("unexpected transcript:\n%s", transcript(m))
	}
}

var errTest = &testError{"boom"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

func TestChatCompletionAppendsAgentReply(t *testing.T) {
	m := newTestModel(t)
	m.pendingMsgID = "msg-1"
	m.busy = true

	next, _ := m.Update(chatStatusMsg{status: &client.ChatStatus{
		Status: "completed", Response: "done!", TaskIDs: []string{"t1"},
	}})
	m = next.(Model)

	if m.busy || m.pendingMsgID != "" {
		t.Error("completion should clear the pending state")
	}
	out := transcript(m)
	if !strings.Contains(out, "agent::done!") {
		t.Errorf("agent reply missing:\n%s", out)
	}
	if !strings.Contains(out, "created tasks: t1") {
		t.Errorf("task ids missing:\n%s", out)
	}
}

func TestChatFailureIsReported(t *testing.T) {
	m := newTestModel(t)
	m.pendingMsgID = "msg-1"
	next, _ := m.Update(chatStatusMsg{status: &client.ChatStatus{Status: "failed", Error: "nope"}})
	m = next.(Model)

	if !strings.Contains(transcript(m), "failed: nope") {
		t.Errorf("expected failure text:\n%s", transcript(m))
	}
}

func TestSSEBackoffGrowsAndResets(t *testing.T) {
	m := newTestModel(t)
	start := m.sseBackoff

	next, cmd := m.Update(sseDisconnectedMsg{})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("expected a reconnect timer")
	}
	if m.sseBackoff <= start {
		t.Errorf("backoff = %v, want > %v", m.sseBackoff, start)
	}
	if m.sseConnected {
		t.Error("stream should be marked disconnected")
	}

	next, _ = m.Update(sseConnectedMsg{})
	m = next.(Model)
	if !m.sseConnected || m.sseBackoff != start {
		t.Errorf("reconnect should reset state: connected=%t backoff=%v", m.sseConnected, m.sseBackoff)
	}
}

func TestEventsOnlyLoggedWhenEnabled(t *testing.T) {
	m := newTestModel(t)
	m.handleSSEEvent(client.Event{Name: "task_update", Data: []byte(`{"type":"task","status":"running"}`)})
	if len(m.log) != 1 { // only the startup banner
		t.Fatalf("events should be suppressed, log = %d", len(m.log))
	}

	m.showEvents = true
	m.handleSSEEvent(client.Event{Name: "task_update", Data: []byte(`{"type":"task","status":"running"}`)})
	if !strings.Contains(transcript(m), "running") {
		t.Errorf("expected the event in the transcript:\n%s", transcript(m))
	}
}

func TestCtrlCQuits(t *testing.T) {
	m := newTestModel(t)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(Model)
	if cmd == nil || !m.quitting {
		t.Error("ctrl+c should quit")
	}
}

func TestViewShowsHeaderAndPrompt(t *testing.T) {
	m := newTestModel(t)
	m.selectedName = "demo"
	m.connected = true
	view := m.View()

	if !strings.Contains(view, "OpenVibely") {
		t.Error("header title missing")
	}
	if !strings.Contains(view, "demo") {
		t.Error("project name missing from header")
	}
	if !strings.Contains(view, "online") {
		t.Error("connection state missing")
	}
	if !strings.Contains(view, "❯") {
		t.Error("input prompt missing")
	}
}
