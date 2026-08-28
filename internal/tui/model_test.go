package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
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
	m = typeInput(t, m, text)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return next.(Model), cmd
}

func typeInput(t *testing.T, m Model, text string) Model {
	t.Helper()
	for _, r := range text {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(Model)
	}
	return m
}

func pressTab(t *testing.T, m Model) Model {
	t.Helper()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	return next.(Model)
}

func transcript(m Model) string {
	var b strings.Builder
	for _, e := range m.log {
		b.WriteString(e.role + ":" + e.head + ":" + e.text + "\n")
	}
	return b.String()
}

func TestStartupUsesConnectingCopyUntilHealthPasses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/capacity/global":
			_, _ = w.Write([]byte(`{"has_capacity":true}`))
		case "/auth/me":
			_, _ = w.Write([]byte(`{"authenticated":false}`))
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
	initial := transcript(m)
	if strings.Contains(initial, "Connected to") {
		t.Fatalf("initial transcript should not claim a connection before health checks pass:\n%s", initial)
	}
	if !strings.Contains(initial, "Connecting to") {
		t.Fatalf("initial transcript should use neutral connecting copy:\n%s", initial)
	}

	updated, _ := m.Update(m.checkConnection()())
	m = updated.(Model)
	if !m.connected {
		t.Fatal("health check should mark the model connected")
	}
	if !strings.Contains(transcript(m), "Connected to "+srv.URL) {
		t.Fatalf("successful health check should announce the connection:\n%s", transcript(m))
	}
}

func TestOfflineProjectLoadShowsRecoveryGuidance(t *testing.T) {
	c, err := client.New("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	loadResult := m.loadProjects(false, "")()
	msg, ok := loadResult.(projectsLoadedMsg)
	if !ok {
		t.Fatalf("project load returned %T, want projectsLoadedMsg", loadResult)
	}
	updated, _ := m.Update(msg)
	m = updated.(Model)

	out := transcript(m)
	want := "loading projects: " + OfflineRecoveryMessage(c.BaseURL(), msg.err)
	if !strings.Contains(out, want) {
		t.Fatalf("project load omitted shared recovery message:\n got: %s\nwant: %s", out, want)
	}
	for _, want := range []string{
		"Unable to reach the OpenVibely backend",
		"Start or check your local OpenVibely backend",
		"-server <url>",
		"OPENVIBELY_SERVER_URL",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("offline guidance missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Connected to") {
		t.Fatalf("offline transcript should not claim a connection:\n%s", out)
	}
}

func TestTransportFailureRemainsOfflineNotSignInRequired(t *testing.T) {
	c, err := client.New("http://127.0.0.1:1") // nothing listening
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	updated, _ = m.Update(m.checkConnection()())
	m = updated.(Model)

	if m.authRequired || m.connected || m.connErr == "" {
		t.Fatalf("transport state = authRequired=%t connected=%t connErr=%q", m.authRequired, m.connected, m.connErr)
	}
	rendered := strings.ToLower(transcript(m) + "\n" + m.renderStatus() + "\n" + m.View())
	if !strings.Contains(rendered, "offline") {
		t.Fatalf("connection-refused recovery lost offline guidance:\n%s", rendered)
	}
	if strings.Contains(rendered, "sign-in required") {
		t.Fatalf("connection-refused backend was classified as auth-required:\n%s", rendered)
	}
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
	m := typeInput(t, newTestModel(t), "/ta")
	if len(m.menu) == 0 {
		t.Fatal("expected a command menu for /ta")
	}
	if m.menu[0].name != "tasks" {
		t.Errorf("first suggestion = %q, want tasks", m.menu[0].name)
	}

	m = pressTab(t, m)
	if got := m.input.Value(); got != "/tasks " {
		t.Errorf("after tab input = %q, want /tasks ", got)
	}
	if !strings.Contains(m.View(), "tasks") {
		t.Error("menu should be visible in the view")
	}
}

func TestSlashCommandSubcommandCompletesFromRegistry(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"tasks", "/tasks ru", "/tasks run "},
		{"models", "/models cap model-x", "/models capacity model-x"},
		{"automations", "/automations run- job", "/automations run-now job"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := pressTab(t, typeInput(t, newTestModel(t), tc.input))
			if got := m.input.Value(); got != tc.want {
				t.Fatalf("input after tab = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSlashCommandSubcommandTabDoesNotErasePartialToken(t *testing.T) {
	m := pressTab(t, typeInput(t, newTestModel(t), "/skills zz"))
	if got := m.input.Value(); got != "/skills zz" {
		t.Fatalf("input after tab = %q, want partial token preserved", got)
	}
}

func TestSlashCommandSkillsLoadCompletionRegression(t *testing.T) {
	m := pressTab(t, typeInput(t, newTestModel(t), "/skills lo"))
	if got := m.input.Value(); got != "/skills load " {
		t.Fatalf("input after tab = %q, want /skills load ", got)
	}

	m = pressTab(t, typeInput(t, newTestModel(t), "/skills lo "))
	if got := m.input.Value(); got != "/skills load " {
		t.Fatalf("trailing-space input after tab = %q, want /skills load ", got)
	}
}

func TestSlashCommandSubcommandAmbiguousAndArgsArePreserved(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"ambiguous action prefix", "/tasks s"},
		{"resource arg after action", "/tasks run sk"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := pressTab(t, typeInput(t, newTestModel(t), tc.input))
			if got := m.input.Value(); got != tc.input {
				t.Fatalf("input after tab = %q, want %q", got, tc.input)
			}
		})
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
	if !strings.Contains(transcript(m), "nothing matches") {
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
	if !strings.Contains(out, "is ambiguous") ||
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

func TestDeferredProjectSelectionStartsScopedSSE(t *testing.T) {
	requestURI := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"projects":[{"id":"p2","name":"docs site"}]}`))
		case "/api/capacity/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case "/events/live":
			select {
			case requestURI <- r.URL.RequestURI():
			default:
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, ": ping\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
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
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)

	m, loadCmd := typeLine(t, m, "/project docs")
	if loadCmd == nil {
		t.Fatal("deferred project selection did not start a project load")
	}
	loaded, ok := loadCmd().(projectsLoadedMsg)
	if !ok {
		t.Fatalf("load command returned %T, want projectsLoadedMsg", loadCmd())
	}

	updated, streamCmd := m.Update(loaded)
	m = updated.(Model)
	defer m.Cleanup()
	if m.selectedID != "p2" {
		t.Fatalf("selected = %q, want p2", m.selectedID)
	}
	if streamCmd == nil {
		t.Fatal("deferred project selection did not start an SSE command")
	}

	select {
	case got := <-requestURI:
		if got != "/events/live?project_id=p2" {
			t.Fatalf("SSE request URI = %q, want scoped project p2", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for scoped SSE request")
	}
}

func TestProjectCommandDeferredLoadSkipsCapacity(t *testing.T) {
	var capacityRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/projects":
			_, _ = w.Write([]byte(`{"projects":[{"id":"p1","name":"OpenVibely Chrome Plugin"},{"id":"p2","name":"docs site"}]}`))
		case "/api/capacity/projects":
			capacityRequests.Add(1)
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
	m, cmd := typeLine(t, m, "/project docs")
	if m.selectedID != "" {
		t.Fatalf("selected %q before deferred project load", m.selectedID)
	}

	loaded, ok := cmd().(projectsLoadedMsg)
	if !ok {
		t.Fatalf("load command returned %T, want projectsLoadedMsg", cmd())
	}
	updated, _ := m.Update(loaded)
	m = updated.(Model)
	if m.selectedID != "p2" || m.selectedName != "docs site" {
		t.Fatalf("selected = %q (%q), want p2 (docs site)", m.selectedID, m.selectedName)
	}
	if got := capacityRequests.Load(); got != 0 {
		t.Fatalf("deferred project selection made %d per-project capacity requests, want 0", got)
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
	if !strings.Contains(transcript(m), "nothing matches") {
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

func TestProjectSelectionReconnectsSSEForNewProject(t *testing.T) {
	projectIDs := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/events/live" {
			projectIDs <- r.URL.Query().Get("project_id")
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
	m.selectedName = "alpha"
	m.projects = []client.Project{
		{ID: "p1", Name: "alpha"},
		{ID: "p2", Name: "beta"},
	}

	_ = m.connectSSE()
	select {
	case got := <-projectIDs:
		if got != "p1" {
			t.Fatalf("initial SSE project_id = %q, want p1", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("initial SSE request not received within 2s")
	}

	m, cmd := m.pickProject("beta")
	if cmd == nil {
		t.Fatal("project selection should reconnect an active SSE stream")
	}
	select {
	case got := <-projectIDs:
		if got != "p2" {
			t.Fatalf("reconnected SSE project_id = %q, want p2", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reconnected SSE request not received within 2s")
	}
	m.Cleanup()

	if !strings.Contains(transcript(m), "active project: beta") {
		t.Fatalf("selection output missing:\n%s", transcript(m))
	}
}

func TestStaleProjectCreationIsIgnoredAfterSelection(t *testing.T) {
	m := newTestModel(t)
	m.projects = []client.Project{
		{ID: "old-project", Name: "Old Project"},
		{ID: "selected-project", Name: "Selected Project"},
	}
	m.selectedID = "old-project"
	m.selectedName = "Old Project"

	staleRequestID := nextProjectRequestID()
	m.projectRequestID = staleRequestID
	m, _ = m.pickProject("selected-project")

	updated, _ := m.Update(projectCreatedMsg{
		requestID: staleRequestID,
		project:   client.Project{ID: "created-project", Name: "Created Project", Path: "/tmp/created"},
	})
	m = updated.(Model)

	if m.selectedID != "selected-project" || m.selectedName != "Selected Project" {
		t.Fatalf("stale creation changed selection: %q (%q)", m.selectedID, m.selectedName)
	}
	if len(m.projects) != 2 {
		t.Fatalf("stale creation changed project list: %+v", m.projects)
	}
	if strings.Contains(transcript(m), "Created Project") {
		t.Fatalf("stale creation rendered success output:\n%s", transcript(m))
	}
}

func TestStaleProjectListCannotOverwriteCreatedProject(t *testing.T) {
	m := newTestModel(t)
	m.selectedID = "created-project"
	m.selectedName = "Created Project"
	m.projects = []client.Project{{ID: "created-project", Name: "Created Project", Path: "/tmp/created"}}

	staleRequestID := nextProjectRequestID()
	currentRequestID := nextProjectRequestID()
	m.projectRequestID = currentRequestID

	updated, _ := m.Update(projectsLoadedMsg{
		requestID: staleRequestID,
		echo:      true,
		projects:  []client.Project{{ID: "old-project", Name: "Old Project", Path: "/tmp/old"}},
	})
	m = updated.(Model)

	if m.selectedID != "created-project" || m.selectedName != "Created Project" {
		t.Fatalf("stale project list changed selection: %q (%q)", m.selectedID, m.selectedName)
	}
	if len(m.projects) != 1 || m.projects[0].ID != "created-project" {
		t.Fatalf("stale project list overwrote created project: %+v", m.projects)
	}
}

func TestStaleProjectLoadCannotClearAuthRequiredState(t *testing.T) {
	m := newTestModel(t)
	m.projectRequestID = 17

	updated, _ := m.Update(resultMsg{
		sessionGeneration: m.sessionGeneration,
		err: &client.AuthRequiredError{
			Method:     http.MethodGet,
			Path:       "/api/capacity/global",
			StatusCode: http.StatusUnauthorized,
		},
	})
	m = updated.(Model)
	if !m.authRequired {
		t.Fatal("auth failure did not enter sign-in-required state")
	}
	before := transcript(m)

	updated, cmd := m.Update(projectsLoadedMsg{
		sessionGeneration: 1,
		requestID:         17,
		projects:          []client.Project{{ID: "p1", Name: "stale"}},
	})
	m = updated.(Model)

	if cmd != nil || !m.authRequired || m.connected || len(m.projects) != 0 || transcript(m) != before {
		t.Fatalf("stale project load changed auth/project state: authRequired=%t connected=%t projects=%+v cmd=%v transcript=%q", m.authRequired, m.connected, m.projects, cmd, transcript(m))
	}
}

func TestCurrentSessionProjectLoadCannotClearAuthRequiredState(t *testing.T) {
	m := newTestModel(t)
	m.projectRequestID = 17
	m.markAuthRequired()
	sessionGeneration := m.sessionGeneration
	projectGeneration := m.projectGeneration
	before := transcript(m)

	updated, cmd := m.Update(projectsLoadedMsg{
		sessionGeneration: sessionGeneration,
		projectGeneration: projectGeneration,
		requestID:         17,
		projects:          []client.Project{{ID: "p1", Name: "still protected"}},
		startSSE:          true,
	})
	m = updated.(Model)

	if cmd != nil || !m.authRequired || m.connected || transcript(m) != before {
		t.Fatalf("current-session project success cleared auth state: authRequired=%t connected=%t cmd=%v transcript=%q", m.authRequired, m.connected, cmd, transcript(m))
	}
	if !m.sseRetryAfterProject {
		t.Fatal("project refresh should defer SSE until health confirms authentication")
	}
	if len(m.projects) != 1 || m.projects[0].ID != "p1" {
		t.Fatalf("current-session project data was not retained: %+v", m.projects)
	}

	// A current health confirmation, rather than project data alone, is allowed
	// to establish the session and clear sign-in-required state.
	updated, cmd = m.Update(connCheckedMsg{
		generation: m.connectionGeneration,
		capacity:   &client.GlobalCapacity{HasCapacity: true},
		auth:       &client.AuthStatus{Authenticated: true},
	})
	m = updated.(Model)
	if cmd == nil || m.authRequired || !m.connected || m.sseRetryAfterProject {
		t.Fatalf("current health confirmation did not establish the session/retry SSE: authRequired=%t connected=%t retry=%t cmd=%v", m.authRequired, m.connected, m.sseRetryAfterProject, cmd)
	}
	m.Cleanup()
}

func TestCapacityOnlyHealthSuccessCannotClearAuthRequired(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		code int
	}{
		{name: "anonymous auth response", body: `{"authenticated":false}`, code: http.StatusOK},
		{name: "auth response unavailable", body: `auth backend unavailable`, code: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/capacity/global":
					_, _ = w.Write([]byte(`{"has_capacity":true}`))
				case "/auth/me":
					w.WriteHeader(tc.code)
					_, _ = w.Write([]byte(tc.body))
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
			m.authRequired = true
			m.connChecked = true

			updated, cmd := m.Update(m.checkConnection()())
			m = updated.(Model)
			if cmd != nil || !m.authRequired || m.connected || m.connErr != "" {
				t.Fatalf("capacity-only health cleared auth state: authRequired=%t connected=%t connErr=%q cmd=%v", m.authRequired, m.connected, m.connErr, cmd)
			}
		})
	}
}

func TestProjectScopedAsyncResultsAreIgnoredAfterProjectSwitch(t *testing.T) {
	authErr := &client.AuthRequiredError{
		Method:     http.MethodGet,
		Path:       "/api/projects",
		StatusCode: http.StatusUnauthorized,
	}
	cases := []struct {
		name  string
		msg   func(projectGeneration uint64) tea.Msg
		check func(t *testing.T, m Model, before string)
	}{
		{
			name: "command result",
			msg: func(projectGeneration uint64) tea.Msg {
				return resultMsg{
					sessionGeneration: 1,
					projectGeneration: projectGeneration,
					title:             "Old project",
					body:              "project A result",
				}
			},
			check: func(t *testing.T, m Model, before string) {
				if transcript(m) != before {
					t.Fatalf("stale command result was rendered: %q", transcript(m))
				}
			},
		},
		{
			name: "chat acknowledgement",
			msg: func(projectGeneration uint64) tea.Msg {
				return chatSentMsg{
					sessionGeneration: 1,
					projectGeneration: projectGeneration,
					accepted:          &client.ChatAccepted{MessageID: "old-message"},
				}
			},
			check: func(t *testing.T, m Model, _ string) {
				if m.pendingMsgID != "current-message" {
					t.Fatalf("stale chat acknowledgement replaced current pending message: %q", m.pendingMsgID)
				}
			},
		},
		{
			name: "chat status",
			msg: func(projectGeneration uint64) tea.Msg {
				return chatStatusMsg{
					sessionGeneration: 1,
					projectGeneration: projectGeneration,
					messageID:         "old-message",
					projectID:         "project-a",
					status:            &client.ChatStatus{MessageID: "old-message", Status: "completed", Response: "project A reply"},
				}
			},
			check: func(t *testing.T, m Model, before string) {
				if m.pendingMsgID != "current-message" || transcript(m) != before {
					t.Fatalf("stale chat status changed project B state: pending=%q transcript=%q", m.pendingMsgID, transcript(m))
				}
			},
		},
		{
			name: "selector result",
			msg: func(projectGeneration uint64) tea.Msg {
				return selectorActiveMsg{
					sessionGeneration: 1,
					projectGeneration: projectGeneration,
					title:             "Old project tasks",
					items:             []selectorItem{{ref: "old-task", label: "old task"}},
				}
			},
			check: func(t *testing.T, m Model, before string) {
				if m.selectorActive || transcript(m) != before {
					t.Fatalf("stale selector result changed project B state: active=%t transcript=%q", m.selectorActive, transcript(m))
				}
			},
		},
		{
			name: "command auth error",
			msg: func(projectGeneration uint64) tea.Msg {
				return resultMsg{
					sessionGeneration: 1,
					projectGeneration: projectGeneration,
					err:               authErr,
				}
			},
			check: func(t *testing.T, m Model, before string) {
				if m.authRequired || m.connected || transcript(m) != before {
					t.Fatalf("stale command auth error changed state: authRequired=%t connected=%t transcript=%q", m.authRequired, m.connected, transcript(m))
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.projects = []client.Project{
				{ID: "project-a", Name: "Project A"},
				{ID: "project-b", Name: "Project B"},
			}
			m.selectedID = "project-a"
			m.selectedName = "Project A"
			oldProjectGeneration := m.projectGeneration
			m, _ = m.pickProject("project-b")
			if m.projectGeneration == oldProjectGeneration {
				t.Fatal("project switch did not advance project generation")
			}
			m.busy = true
			m.pendingMsgID = "current-message"
			before := transcript(m)

			updated, cmd := m.Update(tc.msg(oldProjectGeneration))
			m = updated.(Model)
			if cmd != nil || !m.busy {
				t.Fatalf("stale %s result changed busy state: busy=%t cmd=%v", tc.name, m.busy, cmd)
			}
			tc.check(t, m, before)
		})
	}
}
func TestStaleNonHealthAuthResultCannotReenterSignIn(t *testing.T) {
	m := newTestModel(t)
	m.sessionGeneration = 2
	m.connected = true
	m.connChecked = true
	before := transcript(m)

	next, cmd := m.Update(resultMsg{
		sessionGeneration: 1,
		err: &client.AuthRequiredError{
			Method:     http.MethodGet,
			Path:       "/tasks",
			StatusCode: http.StatusUnauthorized,
		},
	})
	m = next.(Model)

	if cmd != nil || !m.connected || m.authRequired || m.connErr != "" || transcript(m) != before {
		t.Fatalf("stale command auth result changed state: connected=%t authRequired=%t connErr=%q cmd=%v transcript=%q", m.connected, m.authRequired, m.connErr, cmd, transcript(m))
	}
}

func TestStaleNonHealthAuthMessagesAreIgnored(t *testing.T) {
	authErr := &client.AuthRequiredError{
		Method:     http.MethodGet,
		Path:       "/protected",
		StatusCode: http.StatusUnauthorized,
	}
	cases := []struct {
		name string
		msg  tea.Msg
	}{
		{name: "project creation", msg: projectCreatedMsg{sessionGeneration: 1, requestID: 1, err: authErr}},
		{name: "chat acknowledgement", msg: chatSentMsg{sessionGeneration: 1, err: authErr}},
		{name: "chat status", msg: chatStatusMsg{sessionGeneration: 1, err: authErr}},
		{name: "thread", msg: threadOpenedMsg{sessionGeneration: 1, err: authErr}},
		{name: "selector", msg: selectorActiveMsg{sessionGeneration: 1, err: authErr}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.sessionGeneration = 2
			m.connected = true
			m.connChecked = true
			m.busy = true
			m.pendingMsgID = "current-message"
			before := transcript(m)

			next, cmd := m.Update(tc.msg)
			m = next.(Model)
			if cmd != nil || !m.connected || m.authRequired || !m.busy || m.connErr != "" || transcript(m) != before {
				t.Fatalf("stale %s auth result changed state: connected=%t authRequired=%t busy=%t connErr=%q cmd=%v transcript=%q", tc.name, m.connected, m.authRequired, m.busy, m.connErr, cmd, transcript(m))
			}
		})
	}
}

func TestCommandResultCarriesSessionGeneration(t *testing.T) {
	m := newTestModel(t)
	m.selectedID = "p1"

	next, cmd := m.runCommand("/tasks")
	m = next.(Model)
	if cmd == nil {
		t.Fatal("expected command result")
	}
	msg, ok := cmd().(resultMsg)
	if !ok {
		t.Fatalf("command message = %T, want resultMsg", cmd())
	}
	if msg.sessionGeneration != m.sessionGeneration {
		t.Fatalf("result session generation = %d, want %d", msg.sessionGeneration, m.sessionGeneration)
	}
	if msg.projectGeneration != m.projectGeneration {
		t.Fatalf("result project generation = %d, want %d", msg.projectGeneration, m.projectGeneration)
	}
}

func TestProjectLoadTransportFailureClearsConnectedState(t *testing.T) {
	c, err := client.New("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.connected = true
	m.connChecked = true

	updated, _ = m.Update(m.loadProjects(false, "")())
	m = updated.(Model)

	if m.connected || m.authRequired || m.connErr == "" {
		t.Fatalf("project transport failure state = connected=%t authRequired=%t connErr=%q", m.connected, m.authRequired, m.connErr)
	}
	if strings.Contains(strings.ToLower(m.renderHeader()), "online") {
		t.Fatalf("project transport failure left online header:\n%s", m.renderHeader())
	}
	if !strings.Contains(strings.ToLower(m.renderStatus()), "offline") {
		t.Fatalf("project transport failure omitted offline status:\n%s", m.renderStatus())
	}
}

func TestProjectLoadTransportFailurePreservesAuthRequired(t *testing.T) {
	c, err := client.New("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.connected = true
	m.authRequired = true
	m.connChecked = true

	updated, _ = m.Update(m.loadProjects(false, "")())
	m = updated.(Model)

	if m.connected || !m.authRequired || m.connErr == "" {
		t.Fatalf("project transport failure lost known auth state: connected=%t authRequired=%t connErr=%q", m.connected, m.authRequired, m.connErr)
	}
	if !strings.Contains(strings.ToLower(m.renderHeader()), "sign-in required") {
		t.Fatalf("project transport failure lost sign-in precedence:\n%s", m.renderHeader())
	}
	status := strings.ToLower(m.renderStatus())
	if !strings.Contains(status, "offline") || !strings.Contains(status, "sign-in required") {
		t.Fatalf("project transport failure omitted combined recovery state:\n%s", m.renderStatus())
	}
}

func refusedTransportError(t *testing.T) error {
	t.Helper()
	c, err := client.New("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ListTasks(context.Background(), "p1")
	if err == nil {
		t.Fatal("expected connection-refused transport error")
	}
	return err
}

func TestNonHealthTransportFailuresEnterOfflineRecovery(t *testing.T) {
	cases := []struct {
		name string
		msg  func(error) tea.Msg
	}{
		{
			name: "command result",
			msg: func(err error) tea.Msg {
				return resultMsg{err: err}
			},
		},
		{
			name: "chat send",
			msg: func(err error) tea.Msg {
				return chatSentMsg{projectID: "p1", err: err}
			},
		},
		{
			name: "chat status",
			msg: func(err error) tea.Msg {
				return chatStatusMsg{messageID: "msg-1", projectID: "p1", err: err}
			},
		},
		{
			name: "thread",
			msg: func(err error) tea.Msg {
				return threadOpenedMsg{projectID: "p1", err: err}
			},
		},
		{
			name: "selector",
			msg: func(err error) tea.Msg {
				return selectorActiveMsg{title: "Tasks", err: err}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.selectedID = "p1"
			m.connected = true
			m.connChecked = true
			m.busy = true
			if tc.name == "chat status" {
				m.pendingMsgID = "msg-1"
			}

			transportErr := refusedTransportError(t)
			next, _ := m.Update(tc.msg(transportErr))
			m = next.(Model)

			if m.connected || !m.connChecked || m.connErr == "" {
				t.Fatalf("transport state = connected=%t connChecked=%t connErr=%q", m.connected, m.connChecked, m.connErr)
			}
			if strings.Contains(strings.ToLower(m.renderHeader()), "online") {
				t.Fatalf("transport failure left online header:\n%s", m.renderHeader())
			}
			if want := OfflineRecoveryMessage(m.client.BaseURL(), transportErr); !strings.Contains(transcript(m), want) {
				t.Fatalf("transport failure did not use the shared recovery message:\n got: %s\nwant: %s", transcript(m), want)
			}
			if !strings.Contains(strings.ToLower(m.renderStatus()), "offline") {
				t.Fatalf("transport failure omitted offline status:\n%s", m.renderStatus())
			}
		})
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

func TestQueuedChatPromotionCorrelatesSSECompletion(t *testing.T) {
	var statusRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/message":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(client.ChatAccepted{
				MessageID: "queued-input",
				Status:    "queued",
				Queued:    true,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/chat/message/queued-input":
			statusRequests.Add(1)
			_ = json.NewEncoder(w).Encode(client.ChatStatus{
				MessageID: "promoted-execution",
				Status:    "processing",
			})
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
	m.selectedID = "project-A"
	m.busy = true

	sent, ok := m.sendChat("project-A", "hello")().(chatSentMsg)
	if !ok {
		t.Fatalf("sendChat message = %T, want chatSentMsg", m.sendChat("project-A", "hello")())
	}
	updated, _ = m.Update(sent)
	m = updated.(Model)
	if m.pendingMsgID != "queued-input" {
		t.Fatalf("pending message ID = %q, want queued input ID", m.pendingMsgID)
	}

	// The status endpoint promotes the queued input to a distinct execution ID.
	// The original queue ID remains the polling key, while the promoted ID must
	// become a valid completion identity for the project-scoped SSE stream.
	statusMsg := m.doChatStatus(m.pendingMsgID, m.pendingMsgProjectID, m.pendingMsgProjectGeneration)
	updated, _ = m.Update(statusMsg)
	m = updated.(Model)
	if statusRequests.Load() != 1 {
		t.Fatalf("status requests = %d, want 1", statusRequests.Load())
	}
	if m.pendingMsgID != "queued-input" || !m.busy {
		t.Fatalf("promoted processing status changed pending state: pending=%q busy=%t", m.pendingMsgID, m.busy)
	}

	// Make the re-arm command harmless; this test is checking the completion
	// correlation, not stream shutdown behavior.
	events := make(chan client.Event)
	errs := make(chan error)
	close(events)
	close(errs)
	m.sseEvents = events
	m.sseErrs = errs

	payload, _ := json.Marshal(client.ChatEvent{
		Type:            "chat_response_done",
		ProjectID:       "project-A",
		ExecID:          "promoted-execution",
		CompletedOutput: "queued reply",
	})
	updated, _ = m.Update(sseEventMsg{
		generation: m.sseGeneration,
		event: client.Event{
			Name: "chat_response_done",
			Data: json.RawMessage(payload),
		},
	})
	m = updated.(Model)

	if m.pendingMsgID != "" || m.busy {
		t.Fatalf("promoted completion did not settle queued chat: pending=%q busy=%t", m.pendingMsgID, m.busy)
	}
	if !strings.Contains(transcript(m), "agent::queued reply") {
		t.Fatalf("promoted completion missing from transcript:\n%s", transcript(m))
	}
}
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

func TestSSEWaitMessagesCarryGeneration(t *testing.T) {
	m := newTestModel(t)
	events := make(chan client.Event, 1)
	errs := make(chan error)
	events <- client.Event{Name: "task_update", Data: json.RawMessage(`{"type":"task_status_changed"}`)}

	msg := m.waitForSSE(7, events, errs)()
	eventMsg, ok := msg.(sseEventMsg)
	if !ok {
		t.Fatalf("message = %T, want sseEventMsg", msg)
	}
	if eventMsg.generation != 7 {
		t.Fatalf("event generation = %d, want 7", eventMsg.generation)
	}
	close(events)
	close(errs)

	msg = m.waitForSSE(7, events, errs)()
	disconnected, ok := msg.(sseDisconnectedMsg)
	if !ok {
		t.Fatalf("message = %T, want sseDisconnectedMsg", msg)
	}
	if disconnected.generation != 7 {
		t.Fatalf("disconnected generation = %d, want 7", disconnected.generation)
	}
}

func TestStaleSSEMessagesAreIgnoredAfterReconnect(t *testing.T) {
	m := newTestModel(t)
	m.sseGeneration = 2
	m.selectedID = "project-b"
	m.showEvents = true
	before := transcript(m)

	next, cmd := m.Update(sseConnectedMsg{generation: 1})
	m = next.(Model)
	if cmd != nil || m.sseConnected {
		t.Fatalf("stale connected message changed state: connected=%t cmd=%v", m.sseConnected, cmd)
	}

	staleEvent := client.Event{
		Name: "task_update",
		Data: json.RawMessage(`{"type":"task_status_changed","project_id":"project-a","status":"running","task_name":"old"}`),
	}
	next, cmd = m.Update(sseEventMsg{generation: 1, event: staleEvent})
	m = next.(Model)
	if cmd != nil || transcript(m) != before {
		t.Fatalf("stale event leaked into current transcript or returned a command: cmd=%v transcript=%q", cmd, transcript(m))
	}

	next, cmd = m.Update(sseDisconnectedMsg{
		generation: 1,
		err:        &client.AuthRequiredError{Method: http.MethodGet, Path: "/events/live", StatusCode: http.StatusUnauthorized},
	})
	m = next.(Model)
	if cmd != nil || m.authRequired || m.sseConnected {
		t.Fatalf("stale disconnected message changed state: authRequired=%t connected=%t cmd=%v", m.authRequired, m.sseConnected, cmd)
	}

	next, cmd = m.Update(reconnectTickMsg{generation: 1})
	m = next.(Model)
	if cmd != nil || m.sseGeneration != 2 {
		t.Fatalf("stale reconnect tick started a stream: generation=%d cmd=%v", m.sseGeneration, cmd)
	}
}

func TestQueuedSSETaskEventFromPreviousProjectIsIgnoredAfterSwitch(t *testing.T) {
	m := newTestModel(t)
	m.showEvents = true
	m.projects = []client.Project{
		{ID: "project-A", Name: "Project A"},
		{ID: "project-B", Name: "Project B"},
	}
	m.selectedID = "project-A"
	m.selectedName = "Project A"
	m.sseGeneration = 1

	// Dequeue an event from the old stream before switching projects. The
	// resulting message remains queued for Update until after the switch.
	oldEvents := make(chan client.Event, 1)
	oldErrs := make(chan error)
	oldEvents <- client.Event{
		Name: "task_status_changed",
		Data: json.RawMessage(`{"type":"task_status_changed","task_id":"task-a","task_name":"old Project A task","project_id":"project-A","status":"running"}`),
	}
	oldMessage, ok := m.waitForSSE(1, oldEvents, oldErrs)().(sseEventMsg)
	if !ok {
		t.Fatal("old stream command did not return sseEventMsg")
	}

	m, cmd := m.pickProject("project-B")
	if cmd != nil {
		t.Fatal("project switch without an active stream should not start a command")
	}
	if m.selectedID != "project-B" {
		t.Fatalf("selected project = %q, want project-B", m.selectedID)
	}
	currentGeneration := m.sseGeneration
	if currentGeneration == oldMessage.generation {
		t.Fatalf("project switch did not invalidate SSE generation %d", currentGeneration)
	}

	// Install deterministic replacement-stream channels, then deliver the old
	// queued event followed by a valid event from Project B.
	currentEvents := make(chan client.Event)
	currentErrs := make(chan error)
	m.sseEvents = currentEvents
	m.sseErrs = currentErrs
	before := transcript(m)

	updated, cmd := m.Update(oldMessage)
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("stale queued event should be discarded without re-arming the old stream")
	}
	if transcript(m) != before {
		t.Fatalf("stale Project A event changed the transcript:\n%s", transcript(m))
	}

	updated, cmd = m.Update(sseEventMsg{
		generation: currentGeneration,
		event: client.Event{
			Name: "task_status_changed",
			Data: json.RawMessage(`{"type":"task_status_changed","task_id":"task-b","task_name":"current Project B task","project_id":"project-B","status":"completed"}`),
		},
	})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("current Project B event should re-arm the replacement stream")
	}
	out := transcript(m)
	if strings.Contains(out, "old Project A task") {
		t.Fatalf("queued Project A event appeared after the switch:\n%s", out)
	}
	if !strings.Contains(out, "current Project B task") {
		t.Fatalf("current Project B event was not rendered:\n%s", out)
	}
}

func TestStaleSSEChatResponseDoneIsIgnoredWithAndWithoutProjectMetadata(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{
			name: "project metadata present",
			data: `{"type":"chat_response_done","project_id":"project-A","exec_id":"exec-A","completed_output":"stale Project A reply"}`,
		},
		{
			name: "project metadata missing",
			data: `{"type":"chat_response_done","exec_id":"exec-A"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fetchCalled := make(chan struct{}, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/api/chat/message/") {
					select {
					case fetchCalled <- struct{}{}:
					default:
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":"pending"}`))
			}))
			t.Cleanup(srv.Close)

			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			m := New(c)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			m = updated.(Model)
			m.showEvents = true
			m.selectedID = "project-B"
			m.pendingMsgID = "exec-A"
			m.pendingMsgProjectID = "project-B"
			m.pendingMsgProjectGeneration = 2
			m.busy = true
			m.sseGeneration = 2

			// Closed replacement-stream channels make any incorrectly returned
			// re-arm command deterministic, while the stale generation must be
			// rejected before it can reach the status-fetch branch.
			events := make(chan client.Event)
			errs := make(chan error)
			close(events)
			close(errs)
			m.sseEvents = events
			m.sseErrs = errs
			before := transcript(m)

			updated, cmd := m.Update(sseEventMsg{
				generation: 1,
				event:      client.Event{Name: "chat_response_done", Data: json.RawMessage(tc.data)},
			})
			m = updated.(Model)

			if cmd != nil {
				t.Fatal("stale completion should be discarded without re-arming or fetching status")
			}
			if m.pendingMsgID != "exec-A" || !m.busy {
				t.Fatalf("stale completion changed pending state: pending=%q busy=%t", m.pendingMsgID, m.busy)
			}
			if transcript(m) != before {
				t.Fatalf("stale completion changed the transcript:\n%s", transcript(m))
			}
			select {
			case <-fetchCalled:
				t.Fatal("stale completion triggered a chat status fetch")
			default:
			}
		})
	}
}

func TestStaleConnectionCheckIsIgnoredAfterLoginRetryStarts(t *testing.T) {
	m := newTestModel(t)
	m.connectionGeneration = 2
	m.connected = true
	m.connChecked = true
	before := transcript(m)

	next, cmd := m.Update(connCheckedMsg{
		generation: 1,
		err:        &client.AuthRequiredError{Method: http.MethodGet, Path: "/api/capacity/global", StatusCode: http.StatusUnauthorized},
	})
	m = next.(Model)
	if cmd != nil || !m.connected || m.authRequired || m.connErr != "" || transcript(m) != before {
		t.Fatalf("stale connection result changed state: connected=%t authRequired=%t connErr=%q cmd=%v transcript=%q", m.connected, m.authRequired, m.connErr, cmd, transcript(m))
	}
}

func TestConnectionCheckCarriesCurrentGeneration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/capacity/global":
			_, _ = w.Write([]byte(`{"has_capacity":true}`))
		case "/auth/me":
			_, _ = w.Write([]byte(`{"authenticated":false}`))
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
	m.connectionGeneration = 9
	msg, ok := m.checkConnection()().(connCheckedMsg)
	if !ok {
		t.Fatalf("checkConnection message = %T, want connCheckedMsg", m.checkConnection()())
	}
	if msg.generation != 9 {
		t.Fatalf("connection generation = %d, want 9", msg.generation)
	}
}

func TestAuthRequiredInvalidatesInFlightConnectionChecks(t *testing.T) {
	m := newTestModel(t)
	m.connectionGeneration = 4
	m.markAuthRequired()
	if m.connectionGeneration != 5 {
		t.Fatalf("auth transition generation = %d, want 5", m.connectionGeneration)
	}

	next, cmd := m.Update(connCheckedMsg{
		generation: 4,
		capacity:   &client.GlobalCapacity{MaxWorkers: 99},
	})
	m = next.(Model)
	if cmd != nil || !m.authRequired || m.connected || m.capacity != nil {
		t.Fatalf("in-flight health result changed auth state: authRequired=%t connected=%t capacity=%+v cmd=%v", m.authRequired, m.connected, m.capacity, cmd)
	}
}

func TestCurrentForeignSSEEventIsIgnored(t *testing.T) {
	m := newTestModel(t)
	m.sseGeneration = 2
	m.selectedID = "project-b"
	m.showEvents = true
	events := make(chan client.Event)
	errs := make(chan error)
	close(events)
	close(errs)
	m.sseEvents = events
	m.sseErrs = errs
	before := transcript(m)

	next, cmd := m.Update(sseEventMsg{
		generation: 2,
		event: client.Event{
			Name: "task_update",
			Data: json.RawMessage(`{"type":"task_status_changed","project_id":"project-a","status":"running","task_name":"foreign"}`),
		},
	})
	m = next.(Model)
	if cmd == nil || transcript(m) != before {
		t.Fatalf("foreign current-generation event was displayed or not re-armed: cmd=%v transcript=%q", cmd, transcript(m))
	}
}

func TestSuccessfulLoginInvalidatesPreviousSSEStream(t *testing.T) {
	m := newTestModel(t)
	m.loginActive = true
	m.loginPassword = true
	m.loginUsername = "admin"
	m.sseGeneration = 4
	canceled := false
	m.sseCancel = func() { canceled = true }

	next, cmd := m.Update(loginResultMsg{})
	m = next.(Model)
	if cmd == nil || !canceled || m.sseCancel != nil || m.sseGeneration != 5 {
		t.Fatalf("successful login did not invalidate prior stream: canceled=%t cancel-nil=%t generation=%d cmd=%v", canceled, m.sseCancel == nil, m.sseGeneration, cmd)
	}
}

func TestBeginLoginInvalidatesActiveSSEBeforeLoginResult(t *testing.T) {
	m := newTestModel(t)
	m.sseGeneration = 4
	canceled := false
	m.sseCancel = func() { canceled = true }

	var cmd tea.Cmd
	m, cmd = m.beginLogin()
	if cmd != nil || !canceled || m.sseCancel != nil || m.sseGeneration != 5 {
		t.Fatalf("beginLogin did not invalidate active stream: canceled=%t cancel-nil=%t generation=%d cmd=%v", canceled, m.sseCancel == nil, m.sseGeneration, cmd)
	}
	m.loginPassword = true
	m.loginSubmitting = true
	loginSessionGeneration := m.sessionGeneration

	// This message belongs to the stream that was active before /login. It must
	// not advance the login session generation or invalidate the in-flight form.
	next, follow := m.Update(sseDisconnectedMsg{
		generation: 4,
		err:        &client.AuthRequiredError{Method: http.MethodGet, Path: "/events/live", StatusCode: http.StatusUnauthorized},
	})
	m = next.(Model)
	if follow != nil || m.sessionGeneration != loginSessionGeneration || !m.loginSubmitting || m.authRequired {
		t.Fatalf("stale SSE auth failure changed login state: session=%d submitting=%t authRequired=%t follow=%v", m.sessionGeneration, m.loginSubmitting, m.authRequired, follow)
	}

	// The login result from the current attempt must remain deliverable after
	// the stale stream message was ignored, including a retryable credential
	// failure that clears submitting state.
	next, follow = m.Update(loginResultMsg{
		sessionGeneration: loginSessionGeneration,
		err:               fmt.Errorf("login failed: invalid credentials"),
	})
	m = next.(Model)
	if follow != nil || !m.loginActive || !m.loginPassword || m.loginSubmitting || m.input.Value() != "" {
		t.Fatalf("login result was rejected after stale SSE failure: active=%t password=%t submitting=%t input=%q follow=%v", m.loginActive, m.loginPassword, m.loginSubmitting, m.input.Value(), follow)
	}
}

func TestTickDoesNotStartHealthCheckWhileLoginActive(t *testing.T) {
	m := newTestModel(t)
	m.connectionGeneration = 7
	m.loginActive = true
	m.loginPassword = true
	m.loginSubmitting = true

	next, cmd := m.Update(tickMsg{})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("active login should keep the periodic tick scheduled")
	}
	if m.connectionGeneration != 7 {
		t.Fatalf("tick advanced connection generation during login: got %d, want 7", m.connectionGeneration)
	}
	if !m.loginActive || !m.loginSubmitting {
		t.Fatalf("tick changed login state: active=%t submitting=%t", m.loginActive, m.loginSubmitting)
	}
}

func TestCancelLoginAfterTransportFailureRestoresPreviousSSE(t *testing.T) {
	m := newTestModel(t)
	m.connected = true
	m.connChecked = true
	m.selectedID = "project-a"
	m.sseGeneration = 4
	canceled := false
	m.sseCancel = func() { canceled = true }

	m, _ = m.beginLogin()
	if !m.loginResumeSSE {
		t.Fatal("online login should remember the active SSE stream")
	}
	m.loginPassword = true
	m.loginSubmitting = true
	loginGeneration := m.sessionGeneration

	next, cmd := m.Update(loginResultMsg{
		sessionGeneration: loginGeneration,
		err:               &client.LoginTransportError{},
	})
	m = next.(Model)
	if cmd != nil || !m.loginActive || m.loginSubmitting {
		t.Fatalf("transport failure changed retry state unexpectedly: active=%t submitting=%t cmd=%v", m.loginActive, m.loginSubmitting, cmd)
	}
	if m.connected || m.authRequired {
		t.Fatalf("transport failure state = connected=%t authRequired=%t", m.connected, m.authRequired)
	}

	next, resume := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	defer m.Cleanup()
	if resume == nil || m.loginActive || m.loginResumeSSE || m.sseCancel == nil || !canceled {
		t.Fatalf("cancel did not restore the previous SSE stream: resume=%v active=%t resumeSSE=%t cancel-nil=%t old-canceled=%t", resume, m.loginActive, m.loginResumeSSE, m.sseCancel == nil, canceled)
	}
}

func TestCancelLoginWithOfflineHealthStateRestoresPreviousSSE(t *testing.T) {
	m := newTestModel(t)
	m.connected = false
	m.connChecked = true
	m.connErr = "connection refused"
	m.authRequired = false
	m.selectedID = "project-a"
	m.sseGeneration = 4
	canceled := false
	m.sseCancel = func() { canceled = true }

	m, _ = m.beginLogin()
	if !m.loginResumeSSE {
		t.Fatal("login should remember the active SSE stream even when health state is transiently offline")
	}

	next, resume := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	defer m.Cleanup()
	if resume == nil || m.loginActive || m.loginResumeSSE || m.sseCancel == nil || !canceled {
		t.Fatalf("cancel did not restore the previous SSE stream: resume=%v active=%t resumeSSE=%t cancel-nil=%t old-canceled=%t", resume, m.loginActive, m.loginResumeSSE, m.sseCancel == nil, canceled)
	}
}

func TestInteractiveLoginTransportFailureUsesOfflineRecovery(t *testing.T) {
	const password = "transport-password-that-must-not-appear"
	c, err := client.New("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.authRequired = true
	m.connChecked = true

	m, _ = typeLine(t, m, "/login")
	m = typeInput(t, m, "admin")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	m = typeInput(t, m, password)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("expected login request command")
	}

	updated, _ = m.Update(cmd())
	m = updated.(Model)
	if !m.loginActive || !m.loginPassword || m.loginSubmitting {
		t.Fatalf("transport failure should remain retryable: active=%t password=%t submitting=%t", m.loginActive, m.loginPassword, m.loginSubmitting)
	}
	if m.connected || !m.authRequired || !m.connChecked || m.connErr == "" {
		t.Fatalf("transport failure state = connected=%t authRequired=%t checked=%t connErr=%q", m.connected, m.authRequired, m.connChecked, m.connErr)
	}
	visible := strings.ToLower(transcript(m) + "\n" + m.renderStatus() + "\n" + m.View())
	for _, want := range []string{"offline", "unable to reach the openvibely backend", "start or check"} {
		if !strings.Contains(visible, want) {
			t.Fatalf("offline login guidance missing %q:\n%s", want, visible)
		}
	}
	if strings.Contains(visible, password) || strings.Contains(visible, "admin") {
		t.Fatalf("transport login exposed credentials:\n%s", visible)
	}
}

func TestLoginTransportFailurePreservesAuthUntilHealthConfirmsSession(t *testing.T) {
	m := newTestModel(t)
	m.authRequired = true
	m.connected = false
	m.connChecked = true
	m.loginActive = true
	m.loginPassword = true
	m.loginSubmitting = true
	loginGeneration := m.sessionGeneration

	next, cmd := m.Update(loginResultMsg{
		sessionGeneration: loginGeneration,
		err:               &client.LoginTransportError{},
	})
	m = next.(Model)
	if cmd != nil || !m.loginActive || m.loginSubmitting || !m.authRequired {
		t.Fatalf("login transport failure lost retry/auth state: active=%t submitting=%t authRequired=%t cmd=%v", m.loginActive, m.loginSubmitting, m.authRequired, cmd)
	}

	for _, auth := range []*client.AuthStatus{nil, {Authenticated: false}} {
		next, cmd = m.Update(connCheckedMsg{
			generation: m.connectionGeneration,
			capacity:   &client.GlobalCapacity{HasCapacity: true},
			auth:       auth,
		})
		m = next.(Model)
		if cmd != nil || !m.authRequired || m.connected {
			t.Fatalf("partial health result cleared known auth state: auth=%+v authRequired=%t connected=%t cmd=%v", auth, m.authRequired, m.connected, cmd)
		}
	}

	next, cmd = m.Update(connCheckedMsg{
		generation: m.connectionGeneration,
		capacity:   &client.GlobalCapacity{HasCapacity: true},
		auth:       &client.AuthStatus{Authenticated: true},
	})
	m = next.(Model)
	if cmd != nil || m.authRequired || !m.connected {
		t.Fatalf("authenticated health result did not clear auth state: authRequired=%t connected=%t cmd=%v", m.authRequired, m.connected, cmd)
	}
}

func TestOfflineProjectRecoveryStartsScopedSSEWithoutPriorStream(t *testing.T) {
	requestURI := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"projects":[{"id":"recovered-project","name":"Recovered"}]}`))
		case "/api/capacity/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case "/events/live":
			select {
			case requestURI <- r.URL.RequestURI():
			default:
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, ": ping\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
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
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.connected = false
	m.connChecked = true
	m.connErr = "connection refused"
	if m.sseCancel != nil || m.sseRetryAfterProject {
		t.Fatal("test must start without an existing stream or retry flag")
	}

	m, loadCmd := typeLine(t, m, "/projects")
	if loadCmd == nil {
		t.Fatal("offline recovery did not start a project load")
	}
	loaded, ok := loadCmd().(projectsLoadedMsg)
	if !ok {
		t.Fatalf("project load returned %T, want projectsLoadedMsg", loadCmd())
	}

	updated, streamCmd := m.Update(loaded)
	m = updated.(Model)
	defer m.Cleanup()
	if m.selectedID != "recovered-project" {
		t.Fatalf("recovered project was not selected: %q", m.selectedID)
	}
	if streamCmd == nil {
		t.Fatal("offline recovery did not start an SSE command")
	}

	select {
	case got := <-requestURI:
		if got != "/events/live?project_id=recovered-project" {
			t.Fatalf("recovery SSE request URI = %q, want scoped project", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for recovery SSE request")
	}
}

func TestStatusCountsAuthFailureEntersSignInRequired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	m.selectedID = "project-1"
	m.connected = true
	m.connChecked = true
	m.pendingAlertCount = 4
	m.activeTaskCount = 3
	m.queuedTaskCount = 2

	msg, ok := m.fetchStatusCounts()().(statusCountsMsg)
	if !ok {
		t.Fatalf("status count command returned %T, want statusCountsMsg", m.fetchStatusCounts()())
	}
	if !client.IsAuthRequired(msg.err) {
		t.Fatalf("status count error = %v, want authentication-required", msg.err)
	}

	next, cmd := m.Update(msg)
	m = next.(Model)
	if cmd != nil || !m.authRequired || m.connected {
		t.Fatalf("status count auth failure did not enter recovery: authRequired=%t connected=%t cmd=%v", m.authRequired, m.connected, cmd)
	}
	if m.pendingAlertCount != 4 || m.activeTaskCount != 3 || m.queuedTaskCount != 2 {
		t.Fatalf("auth failure overwrote cached counts: alerts=%d active=%d queued=%d", m.pendingAlertCount, m.activeTaskCount, m.queuedTaskCount)
	}
}

func TestInitDoesNotScheduleUnscopedSSE(t *testing.T) {
	c, err := client.New("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	batch, ok := m.Init()().(tea.BatchMsg)
	if !ok {
		t.Fatalf("Init command = %T, want tea.BatchMsg", m.Init()())
	}
	// Init contains the health check, project load, periodic refresh, spinner,
	// and input blink commands. A separate immediate reconnect command would
	// make this count six and would open an unscoped stream before selection.
	if got, want := len(batch), 5; got != want {
		t.Fatalf("Init command count = %d, want %d without an unscoped SSE retry", got, want)
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

func TestIncrementalTranscriptAppendMatchesFullRefresh(t *testing.T) {
	entries := []entry{
		{role: "you", text: "please summarize this fairly long request so wrapping is exercised"},
		{role: "agent", text: "agent response with enough words to wrap at the configured terminal width"},
		{role: "result", head: "Tasks", text: "task one\ntask two"},
		{role: "error", text: "boom"},
		{role: "event", text: "[12:34:56] task running demo"},
		{role: "system", text: "system note"},
		{role: "unknown", text: "default note"},
	}

	m := newTestModel(t)
	m.log = nil
	m.refreshTranscript()
	for _, e := range entries {
		m.append(e)
	}
	incremental := m.transcriptContent
	incrementalView := m.transcript.View()

	m.refreshTranscript()
	if m.transcriptContent != incremental {
		t.Fatalf("incremental transcript differs from full refresh\nincremental:\n%q\nfull:\n%q", incremental, m.transcriptContent)
	}
	if got := m.transcript.View(); got != incrementalView {
		t.Fatalf("incremental viewport differs from full refresh\nincremental:\n%q\nfull:\n%q", incrementalView, got)
	}
}

func TestTranscriptResizeRebuildsWrappedContent(t *testing.T) {
	m := newTestModel(t)
	m.log = nil
	m.refreshTranscript()
	m.append(entry{role: "agent", text: strings.Repeat("wrapped content ", 12)})
	wide := m.transcriptContent

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 30})
	m = updated.(Model)
	if m.transcriptRenderWidth != 40 {
		t.Fatalf("transcript render width = %d, want 40", m.transcriptRenderWidth)
	}
	if m.transcriptContent == wide {
		t.Fatal("resize should rebuild wrapped transcript content")
	}

	rebuilt := m.transcriptContent
	m.refreshTranscript()
	if m.transcriptContent != rebuilt {
		t.Fatal("resized transcript should match a full refresh at the new width")
	}
}

func TestTranscriptAppendPreservesTruncationOrderAndBottom(t *testing.T) {
	m := newTestModel(t)
	m.log = nil
	m.refreshTranscript()
	for i := 0; i < maxTranscript; i++ {
		m.append(entry{role: "event", text: fmt.Sprintf("event-%03d", i)})
	}
	m.append(entry{role: "event", text: "event-new"})

	if len(m.log) != maxTranscript {
		t.Fatalf("log length = %d, want %d", len(m.log), maxTranscript)
	}
	if got := m.log[0].text; got != "event-001" {
		t.Fatalf("first retained event = %q, want event-001", got)
	}
	if got := m.log[len(m.log)-1].text; got != "event-new" {
		t.Fatalf("last retained event = %q, want event-new", got)
	}
	if strings.Contains(m.transcriptContent, "event-000") {
		t.Fatal("truncated event still appears in transcript content")
	}
	if !m.transcript.AtBottom() {
		t.Fatal("append should keep the transcript at the bottom")
	}

	incremental := m.transcriptContent
	m.refreshTranscript()
	if m.transcriptContent != incremental {
		t.Fatal("truncated incremental transcript should match full refresh")
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

// loadProjects fetches ListProjects and GetProjectCapacities concurrently for
// capacity-aware output, so total latency should track the max of the two
// fetch times, not their sum.
func TestLoadProjectsFetchesConcurrently(t *testing.T) {
	const delay = 150 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/projects":
			time.Sleep(delay)
			_, _ = w.Write([]byte(`{"projects":[{"id":"p1","name":"demo"}]}`))
		case "/api/capacity/projects":
			time.Sleep(delay)
			_, _ = w.Write([]byte(`[{"id":"p1","name":"demo","running":2,"queue_size":3,"has_capacity":true}]`))
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

	start := time.Now()
	msg := m.loadProjects(true, "")()
	elapsed := time.Since(start)

	got, ok := msg.(projectsLoadedMsg)
	if !ok {
		t.Fatalf("msg = %T, want projectsLoadedMsg", msg)
	}
	if got.err != nil {
		t.Fatalf("err = %v", got.err)
	}
	if len(got.projects) != 1 || len(got.capacities) != 1 {
		t.Fatalf("unexpected result: %+v", got)
	}
	updated, _ := m.Update(got)
	rendered := stripANSI(transcript(updated.(Model)))
	for _, want := range []string{"Projects", "RUNNING", "QUEUED", "2", "3"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("capacity-aware project output missing %q:\n%s", want, rendered)
		}
	}
	if elapsed >= 2*delay {
		t.Errorf("loadProjects took %v, want well under %v (fetches should run concurrently)", elapsed, 2*delay)
	}
}

// List-only project loads must not wait for or request the optional capacity
// endpoint. The delayed capacity response makes the latency difference
// observable while the request count pins the transport contract.
func TestLoadProjectsListOnlySkipsCapacityAndIsFaster(t *testing.T) {
	const capacityDelay = 200 * time.Millisecond
	var capacityRequests atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/projects":
			_, _ = w.Write([]byte(`{"projects":[{"id":"p1","name":"demo"}]}`))
		case "/api/capacity/projects":
			capacityRequests.Add(1)
			time.Sleep(capacityDelay)
			_, _ = w.Write([]byte(`[{"id":"p1","name":"demo","running":2,"queue_size":1}]`))
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

	listStart := time.Now()
	listMsg := m.loadProjects(false, "")()
	listElapsed := time.Since(listStart)
	listResult, ok := listMsg.(projectsLoadedMsg)
	if !ok {
		t.Fatalf("list-only message = %T, want projectsLoadedMsg", listMsg)
	}
	if listResult.err != nil {
		t.Fatalf("list-only err = %v", listResult.err)
	}
	if len(listResult.projects) != 1 || len(listResult.capacities) != 0 {
		t.Fatalf("list-only result = %+v, want project without capacities", listResult)
	}
	if got := capacityRequests.Load(); got != 0 {
		t.Fatalf("list-only load made %d capacity requests, want 0", got)
	}

	capacityStart := time.Now()
	capacityMsg := m.loadProjects(true, "")()
	capacityElapsed := time.Since(capacityStart)
	capacityResult, ok := capacityMsg.(projectsLoadedMsg)
	if !ok {
		t.Fatalf("capacity-aware message = %T, want projectsLoadedMsg", capacityMsg)
	}
	if capacityResult.err != nil {
		t.Fatalf("capacity-aware err = %v", capacityResult.err)
	}
	if len(capacityResult.projects) != 1 || len(capacityResult.capacities) != 1 {
		t.Fatalf("capacity-aware result = %+v, want project and capacity", capacityResult)
	}
	if got := capacityRequests.Load(); got != 1 {
		t.Fatalf("capacity-aware load made %d capacity requests, want 1", got)
	}
	if listElapsed >= capacityElapsed {
		t.Fatalf("list-only load took %v, capacity-aware load took %v; list-only should be faster", listElapsed, capacityElapsed)
	}
}

// checkConnection issues GetGlobalCapacity and AuthMe concurrently, so total
// latency should track the max of the two fetch times, not their sum.
func TestCheckConnectionFetchesConcurrently(t *testing.T) {
	const delay = 100 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/capacity/global":
			time.Sleep(delay)
			_, _ = w.Write([]byte(`{"has_capacity":true}`))
		case "/auth/me":
			time.Sleep(delay)
			_, _ = w.Write([]byte(`{"authenticated":true}`))
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

	start := time.Now()
	msg := m.checkConnection()()
	elapsed := time.Since(start)

	got, ok := msg.(connCheckedMsg)
	if !ok {
		t.Fatalf("msg = %T, want connCheckedMsg", msg)
	}
	if got.err != nil {
		t.Fatalf("err = %v", got.err)
	}
	if got.capacity == nil {
		t.Fatal("capacity should not be nil")
	}
	if elapsed >= 2*delay {
		t.Errorf("checkConnection took %v, want well under %v (fetches should run concurrently)", elapsed, 2*delay)
	}
}

// If ListProjects fails, loadProjects must still report the error. The
// capacity-aware path may fetch both endpoints concurrently, but the list
// failure remains the returned error.
func TestLoadProjectsListFailsReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			w.WriteHeader(http.StatusInternalServerError)
		case "/api/capacity/projects":
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

	msg := m.loadProjects(true, "")()
	got, ok := msg.(projectsLoadedMsg)
	if !ok {
		t.Fatalf("msg = %T, want projectsLoadedMsg", msg)
	}
	if got.err == nil {
		t.Fatal("expected error when ListProjects fails")
	}
}

// If GetProjectCapacities fails while ListProjects succeeds, the failure is
// silently ignored and capacities come back empty.
func TestLoadProjectsCapacitiesFailIgnored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"projects":[{"id":"p1","name":"demo"}]}`))
		case "/api/capacity/projects":
			w.WriteHeader(http.StatusInternalServerError)
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

	msg := m.loadProjects(true, "")()
	got, ok := msg.(projectsLoadedMsg)
	if !ok {
		t.Fatalf("msg = %T, want projectsLoadedMsg", msg)
	}
	if got.err != nil {
		t.Fatalf("err = %v, want nil", got.err)
	}
	if len(got.projects) != 1 {
		t.Fatalf("projects = %+v, want 1 entry", got.projects)
	}
	if len(got.capacities) != 0 {
		t.Fatalf("capacities = %+v, want empty", got.capacities)
	}
}

// TestSSEChatResponseDoneTriggersImmediateFetch verifies that receiving a
// chat_response_done event while a message is pending triggers an immediate
// GetChatStatus call rather than waiting for the next 1500ms poll tick.
func TestSSEChatResponseDoneTriggersImmediateFetch(t *testing.T) {
	fetchCalled := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/chat/message/") {
			select {
			case fetchCalled <- struct{}{}:
			default:
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"pending"}`))
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)

	m.pendingMsgID = "msg-42"
	m.busy = true

	// Wire up closed SSE channels so waitForSSE() returns immediately in the test.
	events := make(chan client.Event)
	errs := make(chan error)
	close(events)
	close(errs)
	m.sseEvents = events
	m.sseErrs = errs

	// Deliver a chat_response_done SSE event for the pending execution with no CompletedOutput.
	ev := client.Event{
		Name: "chat_response_done",
		Data: json.RawMessage(`{"exec_id":"msg-42"}`),
	}
	_, cmd := m.Update(sseEventMsg{event: ev})

	if cmd == nil {
		t.Fatal("expected a non-nil cmd from sseEventMsg with chat_response_done")
	}

	// tea.Batch returns a Cmd that, when called, yields a BatchMsg containing
	// the sub-cmds rather than running them immediately. Unwrap the batch and
	// run each sub-cmd in its own goroutine so we can observe the fetch.
	result := cmd()
	batch, ok := result.(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected tea.BatchMsg from sseEventMsg chat_response_done branch, got %T", result)
	}
	for _, subcmd := range batch {
		if subcmd != nil {
			go subcmd()
		}
	}

	select {
	case <-fetchCalled:
		// immediate fetch confirmed
	case <-time.After(2 * time.Second):
		t.Error("expected an immediate GetChatStatus fetch, but none arrived within 2s")
	}
}

// TestSSEChatResponseDoneCompletedOutputFastPath verifies that when the
// chat_response_done event carries a non-empty CompletedOutput, the model
// displays the reply immediately without an extra HTTP round-trip.
func TestSSEChatResponseDoneCompletedOutputFastPath(t *testing.T) {
	m := newTestModel(t)
	m.pendingMsgID = "msg-99"
	m.busy = true

	payload, _ := json.Marshal(client.ChatEvent{
		Type:            "chat_response_done",
		ExecID:          "msg-99",
		CompletedOutput: "hello from the agent",
	})
	ev := client.Event{
		Name: "chat_response_done",
		Data: json.RawMessage(payload),
	}
	next, _ := m.Update(sseEventMsg{event: ev})
	m = next.(Model)

	if m.pendingMsgID != "" {
		t.Error("pendingMsgID should be cleared after CompletedOutput fast path")
	}
	if m.busy {
		t.Error("busy should be false after CompletedOutput fast path")
	}
	if !strings.Contains(transcript(m), "agent::hello from the agent") {
		t.Errorf("expected agent reply in transcript:\n%s", transcript(m))
	}
}

func TestSSEChatResponseDoneSameProjectDifferentExecutionIsIgnored(t *testing.T) {
	m := newTestModel(t)
	m.selectedID = "project-A"
	m.pendingMsgID = "msg-99"
	m.busy = true

	// Wire up closed SSE channels so waitForSSE() returns immediately.
	events := make(chan client.Event)
	errs := make(chan error)
	close(events)
	close(errs)
	m.sseEvents = events
	m.sseErrs = errs
	before := transcript(m)

	payload, _ := json.Marshal(client.ChatEvent{
		Type:            "chat_response_done",
		ProjectID:       "project-A",
		ExecID:          "other-execution",
		CompletedOutput: "unrelated reply",
	})
	next, cmd := m.Update(sseEventMsg{
		event: client.Event{Name: "chat_response_done", Data: json.RawMessage(payload)},
	})
	m = next.(Model)

	if m.pendingMsgID != "msg-99" || !m.busy || transcript(m) != before {
		t.Fatalf("unrelated same-project completion settled pending chat: pending=%q busy=%t transcript=%q", m.pendingMsgID, m.busy, transcript(m))
	}
	if cmd == nil {
		t.Fatal("expected SSE wait command after unrelated completion")
	}
	if _, ok := cmd().(sseDisconnectedMsg); !ok {
		t.Fatalf("wait command returned %T, want sseDisconnectedMsg", cmd())
	}
}

// chat_response_done when no message is pending does not trigger a fetch.
func TestSSEChatResponseDoneWithNoPendingIsNoOp(t *testing.T) {
	m := newTestModel(t)
	// pendingMsgID is empty — no pending chat

	// Wire up closed SSE channels so waitForSSE() returns immediately.
	events := make(chan client.Event)
	errs := make(chan error)
	close(events)
	close(errs)
	m.sseEvents = events
	m.sseErrs = errs

	ev := client.Event{
		Name: "chat_response_done",
		Data: json.RawMessage(`{}`),
	}
	next, cmd := m.Update(sseEventMsg{event: ev})
	m = next.(Model)
	_ = m

	if cmd == nil {
		t.Fatal("expected waitForSSE re-arm cmd")
	}
	// The cmd should just be waitForSSE, not a batch. Run it and confirm no
	// extra fetch request arrives — only sseDisconnectedMsg from closed channels.
	result := cmd()
	if _, ok := result.(sseDisconnectedMsg); !ok {
		t.Errorf("expected sseDisconnectedMsg from waitForSSE on closed channels, got %T", result)
	}
}

// TestSSEChatResponseDoneFromOtherProjectIsIgnored verifies that a
// chat_response_done event whose ProjectID does not match m.selectedID is
// silently re-armed without calling fetchChatStatus or appending a reply.
func TestSSEChatResponseDoneFromOtherProjectIsIgnored(t *testing.T) {
	fetchCalled := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/chat/message/") {
			select {
			case fetchCalled <- struct{}{}:
			default:
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"pending"}`))
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)

	m.selectedID = "project-A"
	m.pendingMsgID = "msg-42"
	m.busy = true

	// Wire up closed SSE channels so waitForSSE() returns immediately.
	events := make(chan client.Event)
	errs := make(chan error)
	close(events)
	close(errs)
	m.sseEvents = events
	m.sseErrs = errs

	// Deliver a chat_response_done from a different project.
	payload, _ := json.Marshal(client.ChatEvent{
		Type:      "chat_response_done",
		ProjectID: "project-B",
		ExecID:    "e99",
	})
	ev := client.Event{
		Name: "chat_response_done",
		Data: json.RawMessage(payload),
	}
	next, cmd := m.Update(sseEventMsg{event: ev})
	m = next.(Model)

	// pendingMsgID must remain set — the foreign event must not consume it.
	if m.pendingMsgID == "" {
		t.Error("pendingMsgID should NOT be cleared for a foreign-project event")
	}
	// No agent reply must appear in the transcript.
	if strings.Contains(transcript(m), "agent::") {
		t.Errorf("unexpected agent reply in transcript for foreign-project event:\n%s", transcript(m))
	}

	// The returned cmd must be a plain waitForSSE re-arm (not a batch with a fetch).
	if cmd == nil {
		t.Fatal("expected a re-arm cmd, got nil")
	}
	result := cmd()
	if _, ok := result.(sseDisconnectedMsg); !ok {
		t.Errorf("expected sseDisconnectedMsg from waitForSSE re-arm, got %T", result)
	}

	// Confirm no HTTP fetch was triggered.
	select {
	case <-fetchCalled:
		t.Error("fetchChatStatus must NOT be called for a foreign-project chat_response_done")
	case <-time.After(200 * time.Millisecond):
		// expected: no fetch
	}
}

func TestUnauthorizedHealthAndProjectLoadRequireSignIn(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusUnauthorized} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if status == http.StatusFound {
					w.Header().Set("Location", "/login")
				}
				w.WriteHeader(status)
			}))
			defer srv.Close()

			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			m := New(c)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			m = updated.(Model)

			updated, _ = m.Update(m.checkConnection()())
			m = updated.(Model)
			if !m.authRequired || m.connected || m.connErr != "" {
				t.Fatalf("health state = authRequired=%t connected=%t connErr=%q", m.authRequired, m.connected, m.connErr)
			}

			updated, _ = m.Update(m.loadProjects(false, "")())
			m = updated.(Model)
			if !m.authRequired || m.connErr != "" {
				t.Fatalf("project state = authRequired=%t connErr=%q", m.authRequired, m.connErr)
			}
			rendered := strings.ToLower(transcript(m) + "\n" + m.renderStatus() + "\n" + m.View())
			if strings.Contains(rendered, "offline") {
				t.Fatalf("reachable unauthorized backend was presented as offline:\n%s", rendered)
			}
			for _, want := range []string{"requires sign-in", "/login", "sign-in required"} {
				if !strings.Contains(rendered, want) {
					t.Fatalf("auth guidance missing %q:\n%s", want, rendered)
				}
			}
		})
	}
}

func applyImmediateAuthRetryBatch(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected retry command")
	}
	outer, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("retry command returned %T, want tea.BatchMsg", cmd())
	}

	var apply func(tea.Msg)
	apply = func(msg tea.Msg) {
		if msg == nil {
			return
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, sub := range batch {
				if sub == nil {
					continue
				}
				apply(sub())
			}
			return
		}
		next, follow := m.Update(msg)
		m = next.(Model)
		// Project refresh starts a correctly scoped SSE stream when the login
		// retry began before a project had been selected. Do not execute timers
		// or long-lived wait commands in this synchronous test helper.
		if _, ok := msg.(projectsLoadedMsg); ok && follow != nil {
			apply(follow())
		}
	}
	for _, sub := range outer {
		if sub != nil {
			apply(sub())
		}
	}
	return m
}

func TestInteractiveLoginRetriesHealthProjectsAndSSE(t *testing.T) {
	const (
		username = "admin"
		password = "correct-password"
	)
	var mu sync.Mutex
	var requests []string
	var validSession atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()

		cookie, cookieErr := r.Cookie("ov_session")
		authed := cookieErr == nil && cookie.Value == "session-token"
		switch r.URL.Path {
		case "/login":
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
			}
			if r.FormValue("username") != username || r.FormValue("password") != password {
				t.Errorf("unexpected login form")
				w.Header().Set("Location", "/login")
				w.WriteHeader(http.StatusFound)
				return
			}
			validSession.Store(true)
			http.SetCookie(w, &http.Cookie{Name: "ov_session", Value: "session-token"})
			w.Header().Set("Location", "/")
			w.WriteHeader(http.StatusFound)
		case "/auth/me":
			w.Header().Set("Content-Type", "application/json")
			if validSession.Load() && authed {
				_, _ = w.Write([]byte(`{"authenticated":true,"username":"admin"}`))
				return
			}
			_, _ = w.Write([]byte(`{"authenticated":false}`))
		case "/api/capacity/global":
			if !validSession.Load() || !authed {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"has_capacity":true,"max_workers":4}`))
		case "/api/projects":
			if !validSession.Load() || !authed {
				w.Header().Set("Location", "/login")
				w.WriteHeader(http.StatusFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"projects":[{"id":"p1","name":"demo"}]}`))
		case "/api/capacity/projects":
			if !validSession.Load() || !authed {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case "/events/live":
			if !validSession.Load() || !authed {
				w.Header().Set("Location", "/login")
				w.WriteHeader(http.StatusFound)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, ": ping\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
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
	m.authRequired = true
	m.connChecked = true

	m, cmd := typeLine(t, m, "/login")
	if cmd != nil || !m.loginActive || m.loginPassword {
		t.Fatalf("login did not start username stage: active=%t password=%t cmd=%v", m.loginActive, m.loginPassword, cmd)
	}
	m = typeInput(t, m, username)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if !m.loginPassword || m.input.EchoMode != textinput.EchoPassword {
		t.Fatalf("login did not enter masked password stage: password=%t echo=%v", m.loginPassword, m.input.EchoMode)
	}
	m = typeInput(t, m, password)
	if strings.Contains(m.View(), password) {
		t.Fatal("password appeared in the masked login view")
	}
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil || !m.loginSubmitting || m.input.Value() != "" {
		t.Fatalf("login submission state = active=%t submitting=%t input=%q", m.loginActive, m.loginSubmitting, m.input.Value())
	}
	loginResult := cmd()
	updated, retry := m.Update(loginResult)
	m = updated.(Model)
	m = applyImmediateAuthRetryBatch(t, m, retry)
	m.Cleanup()

	if m.loginActive || m.authRequired || !m.connected || m.selectedID != "p1" {
		t.Fatalf("post-login state = login=%t authRequired=%t connected=%t project=%q", m.loginActive, m.authRequired, m.connected, m.selectedID)
	}
	mu.Lock()
	gotRequests := append([]string(nil), requests...)
	mu.Unlock()
	joined := strings.Join(gotRequests, "\n")
	if !strings.Contains(joined, "POST /login") || !strings.Contains(joined, "GET /api/projects") {
		t.Fatalf("login retry requests missing:\n%s", joined)
	}
	if !strings.Contains(joined, "GET /events/live?project_id=p1") {
		t.Fatalf("SSE was not refreshed with the selected project:\n%s", joined)
	}
	if strings.Contains(strings.ToLower(m.renderStatus()), "offline") {
		t.Fatalf("successful login left offline status:\n%s", m.renderStatus())
	}
}

func TestInteractiveLoginFailureIsRetryableCancelableAndRedacted(t *testing.T) {
	const password = "wrong-password-that-must-not-appear"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/login" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Location", "/login?error=invalid")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m, _ = typeLine(t, m, "/login")
	m = typeInput(t, m, "admin")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	m = typeInput(t, m, password)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("expected login request command")
	}
	updated, _ = m.Update(cmd())
	m = updated.(Model)

	if !m.loginActive || !m.loginPassword || m.loginSubmitting {
		t.Fatalf("failed login should return to password stage: active=%t password=%t submitting=%t", m.loginActive, m.loginPassword, m.loginSubmitting)
	}
	if m.input.EchoMode != textinput.EchoPassword || m.input.Value() != "" {
		t.Fatalf("failed login input = echo=%v value=%q", m.input.EchoMode, m.input.Value())
	}
	visible := transcript(m) + "\n" + m.View()
	if strings.Contains(visible, password) {
		t.Fatalf("failed login exposed password:\n%s", visible)
	}
	if !strings.Contains(visible, "invalid credentials") {
		t.Fatalf("failed login did not provide retry guidance:\n%s", visible)
	}
	for _, item := range m.history {
		if strings.Contains(item, password) {
			t.Fatalf("password entered input history: %q", item)
		}
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.loginActive || m.input.EchoMode != textinput.EchoNormal {
		t.Fatalf("Esc did not cancel login: active=%t echo=%v", m.loginActive, m.input.EchoMode)
	}
	if strings.Contains(transcript(m), password) {
		t.Fatal("cancelled login retained password in transcript")
	}
}

func benchmarkTranscriptModel(b *testing.B) Model {
	b.Helper()
	c, err := client.New("http://127.0.0.1")
	if err != nil {
		b.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.log = nil
	for i := 0; i < maxTranscript; i++ {
		m.log = append(m.log, entry{role: "event", text: fmt.Sprintf("[12:34:%02d] task running transcript entry %03d with enough detail to render", i%60, i)})
	}
	m.refreshTranscript()
	return m
}

func BenchmarkAppendEventWith500EntryTranscript(b *testing.B) {
	for i := 0; i < b.N; i++ {
		m := benchmarkTranscriptModel(b)
		b.StartTimer()
		m.append(entry{role: "event", text: "[12:35:00] task completed benchmark event"})
		b.StopTimer()
	}
}

func BenchmarkAppend100EventBurstWith500EntryTranscript(b *testing.B) {
	for i := 0; i < b.N; i++ {
		m := benchmarkTranscriptModel(b)
		b.StartTimer()
		for j := 0; j < 100; j++ {
			m.append(entry{role: "event", text: fmt.Sprintf("[12:35:%02d] task running burst event %03d", j%60, j)})
		}
		b.StopTimer()
	}
}

// TestConnectSSEPassesSelectedProjectID verifies that connectSSE passes
// m.selectedID to StreamEvents, resulting in ?project_id=<selectedID> on
// the GET /events/live request.
func TestConnectSSEPassesSelectedProjectID(t *testing.T) {
	reqCh := make(chan *http.Request, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/events/live" {
			// Record the request, then close the connection so the goroutine exits.
			select {
			case reqCh <- r.Clone(r.Context()):
			default:
			}
			// Returning immediately closes the connection, ending the stream.
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

	m.selectedID = "proj-123"

	// connectSSE is a pointer-receiver method; it starts the SSE goroutine
	// immediately (inside StreamEvents) before returning the cmd.
	_ = m.connectSSE()

	select {
	case req := <-reqCh:
		got := req.URL.Query().Get("project_id")
		if got != "proj-123" {
			t.Errorf("project_id query param = %q, want %q", got, "proj-123")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no request to /events/live received within 2s")
	}
}
