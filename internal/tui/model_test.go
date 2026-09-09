package tui

import (
	"context"
	"encoding/json"
	"errors"
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

func TestHealthFailuresUseReachableBackendPresentation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		diagnostic string
	}{
		{
			name:       "http 500",
			status:     http.StatusInternalServerError,
			body:       `{"error":"capacity service failed"}`,
			diagnostic: "capacity service failed",
		},
		{
			name:       "malformed json",
			status:     http.StatusOK,
			body:       `{"has_capacity":`,
			diagnostic: "decoding /api/capacity/global response",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/capacity/global":
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
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
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			m = updated.(Model)

			updated, _ = m.Update(m.checkConnection()())
			m = updated.(Model)
			if m.connected || m.authRequired || !m.connReachableError {
				t.Fatalf("health state = connected=%t authRequired=%t reachableError=%t", m.connected, m.authRequired, m.connReachableError)
			}
			if !strings.Contains(m.connErr, tc.diagnostic) {
				t.Fatalf("health diagnostic = %q, want %q", m.connErr, tc.diagnostic)
			}

			rendered := strings.ToLower(stripANSI(transcript(m) + "\n" + m.renderHeader() + "\n" + m.renderStatus() + "\n" + m.hint()))
			for _, want := range []string{"backend error", "unhealthy", strings.ToLower(tc.diagnostic)} {
				if !strings.Contains(rendered, want) {
					t.Errorf("reachable health presentation missing %q:\n%s", want, rendered)
				}
			}
			for _, unwanted := range []string{"offline", "unable to reach", "start or check your local backend"} {
				if strings.Contains(rendered, unwanted) {
					t.Errorf("reachable health presentation contains offline guidance %q:\n%s", unwanted, rendered)
				}
			}
		})
	}
}

func TestProjectLoadFailuresUseReachableBackendPresentation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		diagnostic string
	}{
		{
			name:       "http 500",
			status:     http.StatusInternalServerError,
			body:       `{"error":"project store failed"}`,
			diagnostic: "project store failed",
		},
		{
			name:       "malformed json",
			status:     http.StatusOK,
			body:       `{"projects":`,
			diagnostic: "decoding /api/projects response",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/projects" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			m := New(c)
			m.connected = true
			m.connChecked = true
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			m = updated.(Model)

			updated, _ = m.Update(m.loadProjects(false, "")())
			m = updated.(Model)
			if m.connected || m.authRequired || !m.connReachableError {
				t.Fatalf("project state = connected=%t authRequired=%t reachableError=%t", m.connected, m.authRequired, m.connReachableError)
			}
			if !strings.Contains(m.connErr, tc.diagnostic) {
				t.Fatalf("project diagnostic = %q, want %q", m.connErr, tc.diagnostic)
			}

			rendered := strings.ToLower(stripANSI(transcript(m) + "\n" + m.renderHeader() + "\n" + m.renderStatus() + "\n" + m.hint()))
			for _, want := range []string{"backend error", "unhealthy", strings.ToLower(tc.diagnostic)} {
				if !strings.Contains(rendered, want) {
					t.Errorf("reachable project presentation missing %q:\n%s", want, rendered)
				}
			}
			for _, unwanted := range []string{"offline", "unable to reach", "start or check your local backend"} {
				if strings.Contains(rendered, unwanted) {
					t.Errorf("reachable project presentation contains offline guidance %q:\n%s", unwanted, rendered)
				}
			}
		})
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

func TestStartupEmptyProjectLoadShowsCreationGuidance(t *testing.T) {
	m := newTestModel(t)
	m.connected = true
	m.connChecked = true

	updated, _ := m.Update(projectsLoadedMsg{projects: []client.Project{}})
	m = updated.(Model)

	if !strings.Contains(transcript(m), "/projects create <name> <path>") {
		t.Fatalf("startup empty-project load omitted creation guidance:\n%s", transcript(m))
	}
	if got := stripANSI(m.View()); !strings.Contains(got, "/projects create <name> <path>") {
		t.Fatalf("startup empty-project view omitted creation guidance:\n%s", got)
	}
}

func TestDelayedStartupProjectLoadDoesNotOfferPrematureCreationGuidance(t *testing.T) {
	projectsStarted := make(chan struct{})
	releaseProjects := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseProjects) }) }
	defer release()
	var projectStartOnce sync.Once

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			projectStartOnce.Do(func() { close(projectsStarted) })
			<-releaseProjects
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"projects":[{"id":"p1","name":"demo"}]}`))
		case "/api/capacity/global":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"has_capacity":true}`))
		case "/auth/me":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"authenticated":true}`))
		case "/events/live":
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
	batch, ok := m.Init()().(tea.BatchMsg)
	if !ok || len(batch) < 2 {
		t.Fatalf("Init command = %T with %d commands, want startup health and project-load commands", m.Init()(), len(batch))
	}

	loaded := make(chan tea.Msg, 1)
	go func() { loaded <- batch[1]() }()
	select {
	case <-projectsStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for delayed startup project request")
	}

	updated, _ = m.Update(batch[0]())
	m = updated.(Model)
	if m.projectsLoaded {
		t.Fatal("project list marked loaded before the delayed response")
	}
	if strings.Contains(m.hint(), "/projects create <name> <path>") || strings.Contains(stripANSI(m.View()), "/projects create <name> <path>") {
		t.Fatalf("pending non-empty project load offered creation guidance:\nhint: %s\nview:\n%s", m.hint(), stripANSI(m.View()))
	}
	m, _ = typeLine(t, m, "hello there")
	out := transcript(m)
	if !strings.Contains(out, "no project selected — use /project <name>") {
		t.Fatalf("pending project load did not retain selection guidance:\n%s", out)
	}
	if strings.Contains(out, "/projects create <name> <path>") {
		t.Fatalf("pending project load offered creation guidance:\n%s", out)
	}

	release()
	var msg tea.Msg
	select {
	case msg = <-loaded:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for delayed startup project response")
	}
	updated, _ = m.Update(msg)
	m = updated.(Model)
	defer m.Cleanup()
	if !m.projectsLoaded {
		t.Fatal("successful project response did not mark the list loaded")
	}
	if m.selectedID != "p1" || m.selectedName != "demo" {
		t.Fatalf("startup project selection = %q (%q), want p1 (demo)", m.selectedID, m.selectedName)
	}
	if strings.Contains(m.hint(), "/projects create <name> <path>") {
		t.Fatalf("non-empty project load retained creation guidance: %s", m.hint())
	}
}

func TestAgentsDispatchDuringDelayedStartupProjectLoadRequiresSelection(t *testing.T) {
	projectsStarted := make(chan struct{})
	releaseProjects := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseProjects) }) }
	defer release()
	var projectStartOnce sync.Once
	var agentRequests atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/projects":
			projectStartOnce.Do(func() { close(projectsStarted) })
			<-releaseProjects
			_, _ = w.Write([]byte(`{"projects":[{"id":"p1","name":"demo"}]}`))
		case "/agents", "/agents/generate":
			agentRequests.Add(1)
			_, _ = w.Write([]byte(`<div data-agent-id="ag-1" data-agent-key="reviewer" data-agent-name="Reviewer"></div>`))
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
	batch, ok := m.Init()().(tea.BatchMsg)
	if !ok || len(batch) < 2 {
		t.Fatalf("Init command = %T with %d commands, want startup health and project-load commands", m.Init()(), len(batch))
	}

	loaded := make(chan tea.Msg, 1)
	go func() { loaded <- batch[1]() }()
	select {
	case <-projectsStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for delayed startup project request")
	}

	m = runLine(t, m, "/agents")
	gotAgentRequests := agentRequests.Load()
	gotGuidance := strings.Contains(transcript(m), "no project selected — use /project <name>")
	leftBusy := m.busy
	openedSelector := m.selectorActive
	openedConfirmation := m.pendingConfirmation != nil

	release()
	select {
	case <-loaded:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for delayed startup project response")
	}

	if gotAgentRequests != 0 {
		t.Fatalf("agent dispatch made %d requests while startup project loading was pending", gotAgentRequests)
	}
	if !gotGuidance {
		t.Fatalf("agent dispatch omitted no-project guidance:\n%s", transcript(m))
	}
	if leftBusy {
		t.Fatal("agent dispatch left the model busy")
	}
	if openedSelector {
		t.Fatal("agent dispatch opened a selector before project loading completed")
	}
	if openedConfirmation {
		t.Fatal("agent dispatch opened confirmation before project loading completed")
	}
}

func TestPlainTextOffersCreationGuidanceWhenNoProjects(t *testing.T) {
	m := newTestModel(t)
	m.projectsLoaded = true
	m, _ = typeLine(t, m, "hello there")

	out := transcript(m)
	if !strings.Contains(out, "no projects") || !strings.Contains(out, "/projects create <name> <path>") {
		t.Fatalf("expected actionable project-creation guidance, got:\n%s", out)
	}
	if strings.Contains(out, "no project selected — use /project <name>") {
		t.Fatalf("zero-project guidance still only offered project selection:\n%s", out)
	}
}

func TestPlainTextRetainsSelectionGuidanceWhenProjectsExist(t *testing.T) {
	m := newTestModel(t)
	m.projects = []client.Project{{ID: "p1", Name: "demo"}}
	m, _ = typeLine(t, m, "hello there")

	if !strings.Contains(transcript(m), "no project selected — use /project <name>") {
		t.Fatalf("projects-without-selection guidance changed:\n%s", transcript(m))
	}
	if strings.Contains(transcript(m), "/projects create <name> <path>") {
		t.Fatalf("existing projects incorrectly received creation guidance:\n%s", transcript(m))
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

func chatAckClient(t *testing.T) (*client.Client, *atomic.Int32) {
	t.Helper()
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat/message" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		n := posts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(client.ChatAccepted{
			MessageID: fmt.Sprintf("msg-%d", n),
			Status:    "processing",
		})
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c, &posts
}

func TestBufferedChatOutputPrecedesRejectedOverlappingPlainSubmission(t *testing.T) {
	m := pendingChatStreamTestModel(t)
	m.updateChatStreamOutput("accepted partial reply")
	if !m.chatStreamRenderQueued {
		m.chatStreamRenderQueued = true
	}

	m, cmd := typeLine(t, m, "second message")
	if cmd != nil {
		t.Fatal("overlapping plain submission returned a command")
	}
	if len(m.log) < 2 {
		t.Fatalf("expected assistant output and rejection, got %#v", m.log)
	}
	assistant := m.log[len(m.log)-2]
	warning := m.log[len(m.log)-1]
	if assistant.role != "agent" || assistant.text != "accepted partial reply" {
		t.Fatalf("entry before rejection = %#v, want accepted assistant output", assistant)
	}
	if warning.role != "system" || warning.text != chatStillProcessingMessage {
		t.Fatalf("final entry = %#v, want pending-chat warning", warning)
	}
	if got := strings.Count(transcript(m), "agent::accepted partial reply"); got != 1 {
		t.Fatalf("accepted output rendered %d times; transcript:\n%s", got, transcript(m))
	}
	if m.chatStreamRenderQueued {
		t.Fatal("forced ordering flush left cadence render queued")
	}
	if m.pendingMsgID != "exec-1" || !m.chatSubmissionPending {
		t.Fatalf("rejection changed pending chat: pending=%t id=%q", m.chatSubmissionPending, m.pendingMsgID)
	}
}

func TestRapidPlainChatSubmissionsAreSerialized(t *testing.T) {
	c, posts := chatAckClient(t)
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.selectedID = "p1"

	first, firstCmd := typeLine(t, m, "first message")
	if firstCmd == nil || !first.chatSubmissionPending || first.pendingMsgID != "" {
		t.Fatalf("first submission state = pending=%t id=%q cmd=%v", first.chatSubmissionPending, first.pendingMsgID, firstCmd)
	}
	second, secondCmd := typeLine(t, first, "second message")
	if secondCmd != nil {
		t.Fatal("overlapping plain submission returned a second command")
	}
	if second.pendingMsgID != "" || !second.chatSubmissionPending {
		t.Fatalf("overlapping plain submission changed active state: pending=%t id=%q", second.chatSubmissionPending, second.pendingMsgID)
	}
	if !strings.Contains(transcript(second), chatStillProcessingMessage) {
		t.Fatalf("overlapping plain submission gave no visible feedback:\n%s", transcript(second))
	}
	if got := posts.Load(); got != 0 {
		t.Fatalf("second plain submission made a POST before the first command ran: %d", got)
	}

	firstMsg := firstCmd()
	next, pollCmd := first.Update(firstMsg)
	m = next.(Model)
	if pollCmd == nil || posts.Load() != 1 {
		t.Fatalf("first acknowledgement did not establish one pending turn: poll=%v posts=%d", pollCmd != nil, posts.Load())
	}
	if m.pendingMsgID != "msg-1" || !m.chatSubmissionPending {
		t.Fatalf("first acknowledgement changed pending identity: pending=%t id=%q", m.chatSubmissionPending, m.pendingMsgID)
	}

	next, _ = m.Update(chatStatusMsg{
		messageID: "msg-1",
		projectID: "p1",
		status: &client.ChatStatus{
			MessageID: "msg-1",
			Status:    "completed",
			Response:  "first reply",
			TaskIDs:   []string{"task-1"},
		},
	})
	m = next.(Model)
	out := transcript(m)
	if m.chatSubmissionPending || m.pendingMsgID != "" {
		t.Fatalf("terminal completion left chat pending: pending=%t id=%q", m.chatSubmissionPending, m.pendingMsgID)
	}
	if strings.Count(out, "agent::first reply") != 1 || strings.Count(out, "created tasks: task-1") != 1 {
		t.Fatalf("first response/task IDs were not rendered exactly once:\n%s", out)
	}
	duplicate, _ := m.Update(chatStatusMsg{
		messageID: "msg-1",
		projectID: "p1",
		status:    &client.ChatStatus{MessageID: "msg-1", Status: "completed", Response: "first reply", TaskIDs: []string{"task-1"}},
	})
	m = duplicate.(Model)
	if got := strings.Count(transcript(m), "agent::first reply"); got != 1 {
		t.Fatalf("duplicate terminal status rendered the response %d times", got)
	}
	if got := strings.Count(transcript(m), "created tasks: task-1"); got != 1 {
		t.Fatalf("duplicate terminal status rendered task IDs %d times", got)
	}

	m, nextCmd := typeLine(t, m, "third message")
	if nextCmd == nil {
		t.Fatal("new plain submission after completion was rejected")
	}
	updated, _ = m.Update(nextCmd())
	m = updated.(Model)
	if posts.Load() != 2 || m.pendingMsgID != "msg-2" {
		t.Fatalf("post-completion submission state: posts=%d pending=%q", posts.Load(), m.pendingMsgID)
	}
}

func TestRapidChatCommandSubmissionsAreSerialized(t *testing.T) {
	c, posts := chatAckClient(t)
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.selectedID = "p1"

	first, firstCmd := typeLine(t, m, "/chat first command")
	if firstCmd == nil || !first.chatSubmissionPending {
		t.Fatalf("first /chat state = pending=%t cmd=%v", first.chatSubmissionPending, firstCmd)
	}
	second, secondCmd := typeLine(t, first, "/chat second command")
	if secondCmd != nil {
		t.Fatal("overlapping /chat submission returned a second command")
	}
	if !strings.Contains(transcript(second), chatStillProcessingMessage) {
		t.Fatalf("overlapping /chat submission gave no visible feedback:\n%s", transcript(second))
	}
	if posts.Load() != 0 {
		t.Fatalf("overlapping /chat submission made a POST before the first command ran: %d", posts.Load())
	}

	next, _ := first.Update(firstCmd())
	m = next.(Model)
	if posts.Load() != 1 || m.pendingMsgID != "msg-1" {
		t.Fatalf("first /chat acknowledgement changed pending state: posts=%d pending=%q", posts.Load(), m.pendingMsgID)
	}
	next, _ = m.Update(chatStatusMsg{
		messageID: "msg-1",
		projectID: "p1",
		status:    &client.ChatStatus{MessageID: "msg-1", Status: "completed", Response: "command reply"},
	})
	m = next.(Model)
	if strings.Count(transcript(m), "agent::command reply") != 1 {
		t.Fatalf("/chat reply did not render exactly once:\n%s", transcript(m))
	}

	m, nextCmd := typeLine(t, m, "/chat after completion")
	if nextCmd == nil {
		t.Fatal("new /chat submission after completion was rejected")
	}
	updated, _ = m.Update(nextCmd())
	m = updated.(Model)
	if posts.Load() != 2 || m.pendingMsgID != "msg-2" {
		t.Fatalf("post-completion /chat state: posts=%d pending=%q", posts.Load(), m.pendingMsgID)
	}
}

func TestChatAcknowledgementsCannotReplaceActiveSubmission(t *testing.T) {
	for _, tc := range []struct {
		name       string
		staleFirst bool
	}{
		{name: "stale acknowledgement first", staleFirst: true},
		{name: "active acknowledgement first", staleFirst: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, posts := chatAckClient(t)
			m := New(c)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			m = updated.(Model)
			m.selectedID = "p1"

			first, firstCmd := typeLine(t, m, "first")
			firstNext, _ := first.Update(firstCmd())
			m = firstNext.(Model)
			firstSubmissionID := m.chatSubmissionID
			next, _ := m.Update(chatStatusMsg{
				messageID: "msg-1",
				projectID: "p1",
				status:    &client.ChatStatus{MessageID: "msg-1", Status: "completed", Response: "first"},
			})
			m = next.(Model)

			second, secondCmd := typeLine(t, m, "second")
			activeSubmissionID := second.chatSubmissionID
			if activeSubmissionID == firstSubmissionID || !second.chatSubmissionPending {
				t.Fatalf("new submission token = %d, previous = %d, pending=%t", activeSubmissionID, firstSubmissionID, second.chatSubmissionPending)
			}
			staleAck := chatSentMsg{
				projectID:    "p1",
				submissionID: firstSubmissionID,
				accepted:     &client.ChatAccepted{MessageID: "stale-message"},
			}

			if tc.staleFirst {
				next, _ := second.Update(staleAck)
				second = next.(Model)
				if second.pendingMsgID != "" || !second.chatSubmissionPending {
					t.Fatalf("stale first acknowledgement changed active send: pending=%q active=%t", second.pendingMsgID, second.chatSubmissionPending)
				}
			}
			next, _ = second.Update(secondCmd())
			second = next.(Model)
			if second.pendingMsgID != "msg-2" || posts.Load() != 2 {
				t.Fatalf("active acknowledgement did not install original identity: pending=%q posts=%d", second.pendingMsgID, posts.Load())
			}
			if !tc.staleFirst {
				next, _ := second.Update(staleAck)
				second = next.(Model)
			}
			if second.pendingMsgID != "msg-2" || second.pendingMsgProjectID != "p1" {
				t.Fatalf("stale acknowledgement replaced active identity: pending=%q project=%q", second.pendingMsgID, second.pendingMsgProjectID)
			}
		})
	}
}

func TestChatTerminalStatesPermitResubmission(t *testing.T) {
	for _, terminalState := range []string{"completed", "failed", "cancelled"} {
		t.Run(terminalState, func(t *testing.T) {
			c, posts := chatAckClient(t)
			m := New(c)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			m = updated.(Model)
			m.selectedID = "p1"

			m, cmd := typeLine(t, m, "before terminal")
			if cmd == nil {
				t.Fatal("initial chat submission returned no command")
			}
			updated, _ = m.Update(cmd())
			m = updated.(Model)
			if posts.Load() != 1 || m.pendingMsgID != "msg-1" {
				t.Fatalf("initial acknowledgement: posts=%d pending=%q", posts.Load(), m.pendingMsgID)
			}

			status := &client.ChatStatus{MessageID: "msg-1", Status: terminalState}
			if terminalState == "failed" {
				status.Error = "failed by test"
			}
			updated, _ = m.Update(chatStatusMsg{messageID: "msg-1", projectID: "p1", status: status})
			m = updated.(Model)
			if m.chatSubmissionPending || m.pendingMsgID != "" {
				t.Fatalf("%s state remained pending: active=%t id=%q", terminalState, m.chatSubmissionPending, m.pendingMsgID)
			}

			m, cmd = typeLine(t, m, "after terminal")
			if cmd == nil {
				t.Fatalf("new chat after %s was rejected", terminalState)
			}
			updated, _ = m.Update(cmd())
			m = updated.(Model)
			if posts.Load() != 2 || m.pendingMsgID != "msg-2" {
				t.Fatalf("resubmission after %s: posts=%d pending=%q", terminalState, posts.Load(), m.pendingMsgID)
			}
		})
	}
}

func TestChatStatusFromOlderSubmissionCannotSettleNewTurn(t *testing.T) {
	c, posts := chatAckClient(t)
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.selectedID = "p1"

	m, cmd := typeLine(t, m, "first")
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	firstSubmissionID := m.chatSubmissionID
	updated, _ = m.Update(chatStatusMsg{
		messageID: "msg-1",
		projectID: "p1",
		status:    &client.ChatStatus{MessageID: "msg-1", Status: "completed", Response: "first reply"},
	})
	m = updated.(Model)

	second, secondCmd := typeLine(t, m, "second")
	if !second.chatSubmissionPending || second.pendingMsgID != "" {
		t.Fatalf("second submission state: active=%t pending=%q", second.chatSubmissionPending, second.pendingMsgID)
	}
	oldStatus := chatStatusMsg{
		messageID:    "msg-1",
		submissionID: firstSubmissionID,
		projectID:    "p1",
		status:       &client.ChatStatus{MessageID: "msg-1", Status: "completed", Response: "old reply", TaskIDs: []string{"old-task"}},
	}
	updated, _ = second.Update(oldStatus)
	second = updated.(Model)
	if second.pendingMsgID != "" || !second.chatSubmissionPending || strings.Contains(transcript(second), "old reply") {
		t.Fatalf("old status settled the pre-ack new turn: pending=%q active=%t transcript=%q", second.pendingMsgID, second.chatSubmissionPending, transcript(second))
	}

	updated, _ = second.Update(secondCmd())
	second = updated.(Model)
	if posts.Load() != 2 || second.pendingMsgID != "msg-2" {
		t.Fatalf("second acknowledgement: posts=%d pending=%q", posts.Load(), second.pendingMsgID)
	}
	updated, _ = second.Update(oldStatus)
	second = updated.(Model)
	if second.pendingMsgID != "msg-2" || strings.Contains(transcript(second), "old reply") {
		t.Fatalf("old status replaced active second turn: pending=%q transcript=%q", second.pendingMsgID, transcript(second))
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

func TestSlashCommandMenuSelectionCompletesCanonicalCommand(t *testing.T) {
	m := typeInput(t, newTestModel(t), "/")
	for range 4 {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = next.(Model)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(Model)
	if got := m.menu[m.menuSel].name; got != "skills" {
		t.Fatalf("selected command = %q, want skills", got)
	}

	m = pressTab(t, m)
	if got := m.input.Value(); got != "/skills " {
		t.Fatalf("input after selected tab completion = %q, want /skills ", got)
	}
	if got, want := m.input.Position(), len([]rune(m.input.Value())); got != want {
		t.Fatalf("cursor after selected tab completion = %d, want %d", got, want)
	}
}

func TestSlashCommandCompletionCancellationAndRepeat(t *testing.T) {
	t.Run("direct partial and repeated completion", func(t *testing.T) {
		m := pressTab(t, typeInput(t, newTestModel(t), "/sk"))
		if got := m.input.Value(); got != "/skills " {
			t.Fatalf("direct completion = %q, want /skills ", got)
		}
		m = pressTab(t, m)
		if got := m.input.Value(); got != "/skills " {
			t.Fatalf("repeated completion = %q, want /skills ", got)
		}
	})

	t.Run("escape cancels menu completion", func(t *testing.T) {
		m := typeInput(t, newTestModel(t), "/")
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = next.(Model)
		if len(m.menu) != 0 {
			t.Fatalf("menu remained active after escape: %+v", m.menu)
		}
		m = pressTab(t, m)
		if got := m.input.Value(); got != "/" {
			t.Fatalf("cancelled completion changed input to %q", got)
		}
	})
}

func TestSlashCommandCompletionPreservesQuotedAndPipeArguments(t *testing.T) {
	const input = `/tasks ru "quoted task" | keep  spacing`
	m := pressTab(t, typeInput(t, newTestModel(t), input))
	if got, want := m.input.Value(), `/tasks run "quoted task" | keep  spacing`; got != want {
		t.Fatalf("input after tab = %q, want %q", got, want)
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
		{"automations", "/automations ru job", "/automations run job"},
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

func TestSlashCommandDeepCompletionFromRegistry(t *testing.T) {
	cases := map[string]string{
		"/tasks attachments de":                  "/tasks attachments delete ",
		"/tasks reviews a":                       "/tasks reviews add ",
		"/tasks move api ac":                     "/tasks move api ac",
		"/schedule add api 2026-01-20T09:00 mon": "/schedule add api 2026-01-20T09:00 monthly ",
		"/events fal":                            "/events false ",
	}
	for input, want := range cases {
		m := pressTab(t, typeInput(t, newTestModel(t), input))
		if got := m.input.Value(); got != want {
			t.Fatalf("input after Tab = %q, want %q", got, want)
		}
	}
}

func TestSlashCompletionAtCursorPreservesSuffix(t *testing.T) {
	m := typeInput(t, newTestModel(t), "/tasks attachments de --force")
	m.input.SetCursor(strings.Index(m.input.Value(), " --force"))
	m = pressTab(t, m)
	if got := m.input.Value(); got != "/tasks attachments delete --force" {
		t.Fatalf("input after Tab = %q", got)
	}
	if got, want := m.input.Position(), strings.Index(m.input.Value(), " --force"); got != want {
		t.Fatalf("cursor after Tab = %d, want %d", got, want)
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

func TestCompletedRequestHandlersShareErrorPolicyAndPreserveBehavior(t *testing.T) {
	type handlerCase struct {
		name    string
		message func(Model, error) tea.Msg
		success func(*testing.T, Model, tea.Cmd)
	}
	cases := []handlerCase{
		{
			name: "project creation",
			message: func(m Model, err error) tea.Msg {
				return projectCreatedMsg{
					sessionGeneration: m.sessionGeneration,
					projectGeneration: m.projectGeneration,
					requestID:         m.projectRequestID,
					project:           client.Project{ID: "created", Name: "Created", Path: "/tmp/created"},
					err:               err,
				}
			},
			success: func(t *testing.T, m Model, cmd tea.Cmd) {
				if cmd != nil || m.busy || m.selectedID != "created" || len(m.projects) != 1 || m.projects[0].ID != "created" {
					t.Fatalf("project success changed: busy=%t selected=%q projects=%+v cmd=%v", m.busy, m.selectedID, m.projects, cmd)
				}
				if !strings.Contains(transcript(m), `created project "Created"`) {
					t.Fatalf("project success output missing: %q", transcript(m))
				}
			},
		},
		{
			name: "attachment delete target",
			message: func(m Model, err error) tea.Msg {
				return attachmentDeleteTargetMsg{
					sessionGeneration: m.sessionGeneration,
					projectGeneration: m.projectGeneration,
					projectID:         "p1",
					task:              client.Task{ID: "task-1", Title: "Task One"},
					attachment:        client.Attachment{ID: "attachment-1", FileName: "notes.txt"},
					err:               err,
				}
			},
			success: func(t *testing.T, m Model, cmd tea.Cmd) {
				if cmd != nil || m.busy || m.pendingConfirmation == nil {
					t.Fatalf("attachment success changed: busy=%t confirmation=%v cmd=%v", m.busy, m.pendingConfirmation != nil, cmd)
				}
				if want := `Delete attachment "notes.txt" from task "Task One"?`; !strings.Contains(m.pendingConfirmation.message, want) {
					t.Fatalf("attachment confirmation = %q, want %q", m.pendingConfirmation.message, want)
				}
			},
		},
		{
			name: "thread open",
			message: func(m Model, err error) tea.Msg {
				return threadOpenedMsg{
					sessionGeneration: m.sessionGeneration,
					projectGeneration: m.projectGeneration,
					requestID:         m.threadOpenRequestID,
					projectID:         "p1",
					taskID:            "task-1",
					title:             "Task One",
					status:            "RUNNING",
					body:              "thread body",
					err:               err,
				}
			},
			success: func(t *testing.T, m Model, cmd tea.Cmd) {
				if cmd != nil || m.busy || m.threadID != "task-1" || m.threadTitle != "Task One" || m.threadStatus != "running" {
					t.Fatalf("thread success changed: busy=%t id=%q title=%q status=%q cmd=%v", m.busy, m.threadID, m.threadTitle, m.threadStatus, cmd)
				}
				if out := transcript(m); !strings.Contains(out, "thread body") || !strings.Contains(out, "in task thread") {
					t.Fatalf("thread success output missing: %q", out)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("authentication precedes transport", func(t *testing.T) {
				m := newTestModel(t)
				m.selectedID = "p1"
				m.projectRequestID = 7
				m.threadOpenRequestID = 9
				m.connected = true
				m.connChecked = true
				m.busy = true
				authErr := &client.AuthRequiredError{Method: http.MethodGet, Path: "/protected", StatusCode: http.StatusUnauthorized}
				combined := errors.Join(authErr, refusedTransportError(t))

				next, cmd := m.Update(tc.message(m, combined))
				m = next.(Model)
				out := transcript(m)
				if cmd != nil || !m.authRequired || m.connected || m.busy {
					t.Fatalf("auth result state: authRequired=%t connected=%t busy=%t cmd=%v", m.authRequired, m.connected, m.busy, cmd)
				}
				if strings.Count(out, authRecoveryMessage(m.client.BaseURL())) != 1 || strings.Contains(out, "Backend offline or unreachable") || m.connErr != "" {
					t.Fatalf("auth did not take precedence exactly once: connErr=%q transcript=%q", m.connErr, out)
				}
			})

			t.Run("transport guidance once", func(t *testing.T) {
				m := newTestModel(t)
				m.selectedID = "p1"
				m.projectRequestID = 7
				m.threadOpenRequestID = 9
				m.connected = true
				m.connChecked = true
				m.busy = true
				transportErr := refusedTransportError(t)

				next, cmd := m.Update(tc.message(m, transportErr))
				m = next.(Model)
				guidance := OfflineRecoveryMessage(m.client.BaseURL(), transportErr)
				if cmd != nil || m.connected || !m.connChecked || m.connErr != safeConnectionDiagnostic(transportErr) || m.busy {
					t.Fatalf("transport result state: connected=%t checked=%t connErr=%q busy=%t cmd=%v", m.connected, m.connChecked, m.connErr, m.busy, cmd)
				}
				if got := strings.Count(transcript(m), guidance); got != 1 {
					t.Fatalf("transport guidance count = %d, want 1: %q", got, transcript(m))
				}
			})

			t.Run("ordinary error once unchanged", func(t *testing.T) {
				m := newTestModel(t)
				m.selectedID = "p1"
				m.projectRequestID = 7
				m.threadOpenRequestID = 9
				m.busy = true
				ordinaryErr := errors.New("ordinary completed-request failure")

				next, cmd := m.Update(tc.message(m, ordinaryErr))
				m = next.(Model)
				if cmd != nil || m.busy {
					t.Fatalf("ordinary error state: busy=%t cmd=%v", m.busy, cmd)
				}
				if got := strings.Count(transcript(m), "error::"+ordinaryErr.Error()+"\n"); got != 1 {
					t.Fatalf("ordinary error count = %d, want exact entry once: %q", got, transcript(m))
				}
			})

			t.Run("stale ignored", func(t *testing.T) {
				m := newTestModel(t)
				m.selectedID = "p1"
				m.projectRequestID = 7
				m.threadOpenRequestID = 9
				m.sessionGeneration = 2
				m.busy = true
				before := transcript(m)

				staleBase := m
				staleBase.sessionGeneration = 1
				next, cmd := m.Update(tc.message(staleBase, errors.New("stale failure")))
				m = next.(Model)
				if cmd != nil || !m.busy || transcript(m) != before || m.authRequired || m.connErr != "" || m.pendingConfirmation != nil || m.threadID != "" {
					t.Fatalf("stale result changed state: busy=%t auth=%t connErr=%q confirmation=%v thread=%q cmd=%v transcript=%q", m.busy, m.authRequired, m.connErr, m.pendingConfirmation != nil, m.threadID, cmd, transcript(m))
				}
			})

			t.Run("success", func(t *testing.T) {
				m := newTestModel(t)
				m.selectedID = "p1"
				m.projectRequestID = 7
				m.threadOpenRequestID = 9
				m.busy = true

				next, cmd := m.Update(tc.message(m, nil))
				tc.success(t, next.(Model), cmd)
			})
		})
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
	m.pendingMsgExecutionID = "exec-1"
	m.pendingMsgProjectID = "project-1"
	m.pendingMsgProjectGeneration = 7
	m.busy = true

	next, _ := m.Update(chatStatusMsg{status: &client.ChatStatus{
		Status: "completed", Response: "done!", TaskIDs: []string{"t1", "t2"},
	}})
	m = next.(Model)

	if m.busy || m.pendingMsgID != "" || m.pendingMsgExecutionID != "" || m.pendingMsgProjectID != "" || m.pendingMsgProjectGeneration != 0 {
		t.Errorf("completion should clear pending state: busy=%t id=%q execution=%q project=%q generation=%d", m.busy, m.pendingMsgID, m.pendingMsgExecutionID, m.pendingMsgProjectID, m.pendingMsgProjectGeneration)
	}
	out := transcript(m)
	if !strings.Contains(out, "agent::done!") {
		t.Errorf("agent reply missing:\n%s", out)
	}
	if !strings.Contains(out, "created tasks: t1, t2") {
		t.Errorf("task ids missing:\n%s", out)
	}
}

func TestChatFailureIsReportedAndClearsPendingState(t *testing.T) {
	for _, status := range []string{"failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			m := newTestModel(t)
			m.pendingMsgID = "msg-1"
			m.pendingMsgExecutionID = "exec-1"
			m.pendingMsgProjectID = "project-1"
			m.pendingMsgProjectGeneration = 7
			m.busy = true

			next, _ := m.Update(chatStatusMsg{status: &client.ChatStatus{Status: status, Error: "nope"}})
			m = next.(Model)

			if m.busy || m.pendingMsgID != "" || m.pendingMsgExecutionID != "" || m.pendingMsgProjectID != "" || m.pendingMsgProjectGeneration != 0 {
				t.Errorf("%s should clear pending state: busy=%t id=%q execution=%q project=%q generation=%d", status, m.busy, m.pendingMsgID, m.pendingMsgExecutionID, m.pendingMsgProjectID, m.pendingMsgProjectGeneration)
			}
			if !strings.Contains(transcript(m), status+": nope") {
				t.Errorf("expected %s text:\n%s", status, transcript(m))
			}
		})
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

func TestLoginCompletionRestoresCommonFormState(t *testing.T) {
	for _, tc := range []struct {
		name          string
		passwordStage bool
		success       bool
	}{
		{name: "success", passwordStage: true, success: true},
		{name: "cancel username stage"},
		{name: "cancel password stage", passwordStage: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.input.Prompt = "chat: "
			m.input.Placeholder = "restored placeholder"
			m.input.EchoMode = textinput.EchoNone
			m.input.Blur()

			m, _ = m.beginLogin()
			m.loginPassword = tc.passwordStage
			m.loginSubmitting = tc.success
			m.loginUsername = "admin"
			m.loginResumeSSE = !tc.success
			m.input.SetValue("secret")
			m.input.EchoMode = textinput.EchoPassword
			m.input.Prompt = "password: "
			m.input.Placeholder = "password"
			m.input.Blur()
			m.menu = []command{{name: "stale"}}
			m.busy = true

			var cmd tea.Cmd
			if tc.success {
				var next tea.Model
				next, cmd = m.Update(loginResultMsg{sessionGeneration: m.sessionGeneration})
				m = next.(Model)
			} else {
				var next tea.Model
				next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
				m = next.(Model)
			}

			if tc.success && cmd == nil {
				t.Fatal("successful login did not start recovery")
			}
			if m.loginActive || m.loginPassword || m.loginSubmitting || m.loginUsername != "" || m.loginResumeSSE {
				t.Fatalf("login state not reset: active=%t password=%t submitting=%t username=%q resume=%t", m.loginActive, m.loginPassword, m.loginSubmitting, m.loginUsername, m.loginResumeSSE)
			}
			if m.input.Value() != "" || m.input.Prompt != "chat: " || m.input.Placeholder != "restored placeholder" || m.input.EchoMode != textinput.EchoNone || !m.input.Focused() {
				t.Fatalf("input state not restored: value=%q prompt=%q placeholder=%q echo=%v focused=%t", m.input.Value(), m.input.Prompt, m.input.Placeholder, m.input.EchoMode, m.input.Focused())
			}
			if m.menu != nil || m.busy {
				t.Fatalf("menu/busy state not reset: menu=%v busy=%t", m.menu, m.busy)
			}
			if !tc.success && !strings.Contains(transcript(m), "sign-in cancelled") {
				t.Fatal("cancellation message missing")
			}
		})
	}
}

func TestStaleLoginResultDoesNotResetCurrentForm(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.beginLogin()
	m.loginPassword = true
	m.loginSubmitting = true
	m.loginUsername = "current-user"
	m.input.Prompt = "password: "
	m.input.Placeholder = "password"
	m.input.EchoMode = textinput.EchoPassword
	m.input.Blur()
	m.menu = []command{{name: "current"}}
	m.busy = true

	next, cmd := m.Update(loginResultMsg{sessionGeneration: m.sessionGeneration - 1})
	m = next.(Model)
	if cmd != nil {
		t.Fatalf("stale login result returned command %v", cmd)
	}
	if !m.loginActive || !m.loginPassword || !m.loginSubmitting || m.loginUsername != "current-user" || !m.busy {
		t.Fatalf("stale login result changed current attempt: active=%t password=%t submitting=%t username=%q busy=%t", m.loginActive, m.loginPassword, m.loginSubmitting, m.loginUsername, m.busy)
	}
	if m.input.Prompt != "password: " || m.input.Placeholder != "password" || m.input.EchoMode != textinput.EchoPassword || m.input.Focused() || len(m.menu) != 1 {
		t.Fatalf("stale login result changed form: prompt=%q placeholder=%q echo=%v focused=%t menu=%v", m.input.Prompt, m.input.Placeholder, m.input.EchoMode, m.input.Focused(), m.menu)
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
		_, _ = w.Write([]byte(`{"message_id":"msg-42","status":"completed","response":"fallback reply"}`))
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
	m.pendingMsgExecutionID = "exec-42"
	m.pendingMsgProjectID = "project-A"
	m.pendingMsgProjectGeneration = 1
	m.selectedID = "project-A"
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
		Data: json.RawMessage(`{"exec_id":"msg-42","project_id":"project-A"}`),
	}
	_, cmd := m.Update(sseEventMsg{event: ev})

	if cmd == nil {
		t.Fatal("expected a non-nil cmd from sseEventMsg with chat_response_done")
	}

	// tea.Batch returns a Cmd that, when called, yields a BatchMsg containing
	// the sub-cmds rather than running them immediately. Execute each one so
	// the immediate status result can be fed back through Update.
	result := cmd()
	batch, ok := result.(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected tea.BatchMsg from sseEventMsg chat_response_done branch, got %T", result)
	}
	var fetched chatStatusMsg
	for _, subcmd := range batch {
		if subcmd == nil {
			continue
		}
		if msg, ok := subcmd().(chatStatusMsg); ok {
			fetched = msg
		}
	}

	select {
	case <-fetchCalled:
		// immediate fetch confirmed
	default:
		t.Fatal("expected an immediate GetChatStatus fetch")
	}
	if fetched.status == nil {
		t.Fatal("immediate fetch did not return a chat status")
	}

	updated, _ = m.Update(fetched)
	m = updated.(Model)
	if m.busy || m.pendingMsgID != "" || m.pendingMsgExecutionID != "" || m.pendingMsgProjectID != "" || m.pendingMsgProjectGeneration != 0 {
		t.Errorf("fallback completion should clear pending state: busy=%t id=%q execution=%q project=%q generation=%d", m.busy, m.pendingMsgID, m.pendingMsgExecutionID, m.pendingMsgProjectID, m.pendingMsgProjectGeneration)
	}
	if !strings.Contains(transcript(m), "agent::fallback reply") {
		t.Fatalf("status fallback did not render the response:\n%s", transcript(m))
	}
}

func TestChatStreamForcedFlushPreservesBytesCacheAndTruncation(t *testing.T) {
	m := newTestModel(t)
	m.log = nil
	for i := 0; i < maxTranscript; i++ {
		m.log = append(m.log, entry{role: "system", text: fmt.Sprintf("history-%03d", i)})
	}
	m.refreshTranscript()
	unchanged := append([]string(nil), m.transcriptBlocks[1:]...)

	want := "prefix λ🙂\nwrapped suffix"
	for _, chunk := range []string{want[:8], want[8:11], want[11:]} {
		m.updateChatStreamOutput(chunk)
	}
	if m.chatStreamOffset != len([]byte(want)) {
		t.Fatalf("UTF-8 byte offset = %d, want %d", m.chatStreamOffset, len([]byte(want)))
	}
	m.flushChatStreamOutput()
	if len(m.log) != maxTranscript || m.chatStreamLogIndex != maxTranscript-1 || m.log[m.chatStreamLogIndex].text != want {
		t.Fatalf("flush/truncation mismatch: entries=%d index=%d text=%q", len(m.log), m.chatStreamLogIndex, m.log[m.chatStreamLogIndex].text)
	}
	for i, block := range unchanged {
		if m.transcriptBlocks[i] != block {
			t.Fatalf("immutable cached block %d changed", i)
		}
	}
	wantBlock := renderTranscriptEntry(entry{role: "agent", text: want}, transcriptWrap(m.effectiveTranscriptWidth()))
	if m.transcriptBlocks[m.chatStreamLogIndex] != wantBlock || !strings.HasSuffix(m.transcriptContent, wantBlock) {
		t.Fatalf("forced output differs from canonical rendering:\ngot  %q\nwant %q", m.transcriptBlocks[m.chatStreamLogIndex], wantBlock)
	}

	m.transcriptReady = false
	m.updateChatStreamOutput(" authoritative")
	m.flushChatStreamOutput()
	incremental := m.transcriptContent
	m.refreshTranscript()
	if m.transcriptContent != incremental {
		t.Fatalf("cache-invalidated fallback differs from full refresh\ngot  %q\nwant %q", incremental, m.transcriptContent)
	}
}

func TestChatStreamSnapshotFlushesMatchingBufferedOutput(t *testing.T) {
	m := newTestModel(t)
	m.updateChatStreamOutput("matching durable snapshot")
	m.chatStreamRenderQueued = true
	m.updateChatStreamSnapshot("matching durable snapshot")
	if m.chatStreamRenderQueued || m.chatStreamLogIndex < 0 || !strings.Contains(transcript(m), "agent::matching durable snapshot") {
		t.Fatalf("matching durable snapshot did not flush buffered output: queued=%t index=%d transcript=%q", m.chatStreamRenderQueued, m.chatStreamLogIndex, transcript(m))
	}
}

func TestChatStreamRedrawsAreCadenceBoundedAndFinalOutputMatches(t *testing.T) {
	m := pendingChatStreamTestModel(t)
	chunk := strings.Repeat("λ", 16) // 32 UTF-8 bytes per chunk.
	cadenceChunks := int(chatStreamRenderInterval / time.Millisecond)
	var want strings.Builder
	for i := 1; i <= 100; i++ {
		want.WriteString(chunk)
		next, _ := m.Update(chatStreamEventMsg{generation: 3, submissionID: 9, projectID: "project-A", execID: "exec-1", event: client.ChatOutputEvent{Data: chunk}})
		m = next.(Model)
		if i%cadenceChunks == 0 {
			next, _ = m.Update(chatStreamRenderMsg{generation: 3, renderGeneration: m.chatStreamRenderGeneration, submissionID: 9, projectID: "project-A", execID: "exec-1"})
			m = next.(Model)
		}
	}
	next, _ := m.Update(chatStreamEventMsg{generation: 3, submissionID: 9, projectID: "project-A", execID: "exec-1", event: client.ChatOutputEvent{Name: "done"}})
	m = next.(Model)
	if m.chatStreamRedraws != 4 {
		t.Fatalf("redraws = %d, want 3 cadence redraws and 1 terminal flush", m.chatStreamRedraws)
	}
	if len(m.log) == 0 || m.log[len(m.log)-1].role != "agent" || m.log[len(m.log)-1].text != want.String() {
		t.Fatal("cadence/terminal rendering changed final output bytes")
	}
}

func TestNonStreamAuthInvalidationFlushesQueuedChatOutput(t *testing.T) {
	authErr := &client.AuthRequiredError{
		Method:     http.MethodGet,
		Path:       "/api/protected",
		StatusCode: http.StatusUnauthorized,
	}
	for _, tc := range []struct {
		name string
		msg  func(Model) tea.Msg
	}{
		{
			name: "health check",
			msg: func(m Model) tea.Msg {
				return connCheckedMsg{generation: m.connectionGeneration, err: authErr}
			},
		},
		{
			name: "status counts",
			msg: func(m Model) tea.Msg {
				return statusCountsMsg{sessionGeneration: m.sessionGeneration, projectGeneration: m.projectGeneration, err: authErr}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := pendingChatStreamTestModel(t)
			const partial = "queued λ output"
			m.updateChatStreamOutput(partial)
			m.chatStreamRenderQueued = true
			generation := m.chatStreamGeneration

			next, _ := m.Update(tc.msg(m))
			m = next.(Model)

			if !m.authRequired || m.chatStreamRenderQueued || m.chatStreamGeneration == generation {
				t.Fatalf("auth invalidation state: required=%t queued=%t generation=%d, want required, flushed, and generation > %d", m.authRequired, m.chatStreamRenderQueued, m.chatStreamGeneration, generation)
			}
			out := transcript(m)
			partialAt := strings.Index(out, "agent::"+partial)
			authAt := strings.Index(out, authRecoveryMessage(m.client.BaseURL()))
			if partialAt < 0 || authAt < 0 || partialAt >= authAt {
				t.Fatalf("queued output was not preserved before auth recovery message: %q", out)
			}
		})
	}
}

func TestManualLoginFlushesQueuedChatOutputBeforeInvalidation(t *testing.T) {
	m := pendingChatStreamTestModel(t)
	const partial = "queued λ before login"
	m.updateChatStreamOutput(partial)
	m.chatStreamRenderQueued = true
	streamGeneration := m.chatStreamGeneration

	m, cmd := typeLine(t, m, "/login")
	if cmd != nil {
		t.Fatalf("/login command = %v, want nil", cmd)
	}
	if !m.loginActive || m.chatStreamRenderQueued || m.chatStreamGeneration == streamGeneration {
		t.Fatalf("login transition state: active=%t queued=%t generation=%d, want active, flushed, and generation > %d", m.loginActive, m.chatStreamRenderQueued, m.chatStreamGeneration, streamGeneration)
	}
	if !m.chatSubmissionPending || m.pendingMsgID != "exec-1" {
		t.Fatalf("accepted pending chat was cleared: pending=%t id=%q", m.chatSubmissionPending, m.pendingMsgID)
	}
	out := transcript(m)
	partialAt := strings.Index(out, "agent::"+partial)
	commandAt := strings.Index(out, "you::/login")
	loginAt := strings.Index(out, "sign-in: enter username")
	if partialAt < 0 || commandAt < 0 || loginAt < 0 || partialAt >= commandAt || commandAt >= loginAt || strings.Count(out, "agent::"+partial) != 1 {
		t.Fatalf("queued output, login command, and guidance are out of order: %q", out)
	}
}

func TestProjectSwitchFlushesQueuedChatOutputBeforeReset(t *testing.T) {
	m := pendingChatStreamTestModel(t)
	m.projects = []client.Project{
		{ID: "project-A", Name: "alpha"},
		{ID: "project-B", Name: "beta"},
	}
	const partial = "queued λ before project switch"
	m.updateChatStreamOutput(partial)
	m.chatStreamRenderQueued = true
	streamGeneration := m.chatStreamGeneration
	projectGeneration := m.projectGeneration

	m, cmd := typeLine(t, m, "/project beta")
	if cmd != nil {
		t.Fatalf("/project command = %v, want nil without an active SSE stream", cmd)
	}
	if m.selectedID != "project-B" || m.selectedName != "beta" {
		t.Fatalf("selected project = %q (%q), want project-B (beta)", m.selectedID, m.selectedName)
	}
	if m.chatStreamRenderQueued || m.chatStreamGeneration == streamGeneration || m.projectGeneration == projectGeneration {
		t.Fatalf("project transition generations: queued=%t stream=%d project=%d, want flushed and advanced from stream=%d project=%d", m.chatStreamRenderQueued, m.chatStreamGeneration, m.projectGeneration, streamGeneration, projectGeneration)
	}
	if m.chatSubmissionPending || m.pendingMsgID != "" || m.pendingMsgProjectID != "" || m.busy {
		t.Fatalf("old-project pending chat survived switch: pending=%t id=%q project=%q busy=%t", m.chatSubmissionPending, m.pendingMsgID, m.pendingMsgProjectID, m.busy)
	}
	out := transcript(m)
	partialAt := strings.Index(out, "agent::"+partial)
	commandAt := strings.Index(out, "you::/project beta")
	projectAt := strings.Index(out, "active project: beta")
	if partialAt < 0 || commandAt < 0 || projectAt < 0 || partialAt >= commandAt || commandAt >= projectAt || strings.Count(out, "agent::"+partial) != 1 {
		t.Fatalf("queued output, project command, and selection output are out of order: %q", out)
	}
}

func TestChatStreamTerminalEventFlushesQueuedOutput(t *testing.T) {
	for _, eventName := range []string{"done", "error"} {
		t.Run(eventName, func(t *testing.T) {
			m := newTestModel(t)
			m.selectedID = "project-A"
			m.pendingMsgID = "exec-1"
			m.pendingMsgProjectID = "project-A"
			m.chatSubmissionPending = true
			m.chatSubmissionID = 7
			m.chatStreamGeneration = 4
			m.chatStreamExecID = "exec-1"
			m.updateChatStreamOutput("queued λ output")
			m.chatStreamRenderQueued = true

			next, cmd := m.Update(chatStreamEventMsg{generation: 4, submissionID: 7, projectID: "project-A", execID: "exec-1", event: client.ChatOutputEvent{Name: eventName}})
			m = next.(Model)
			if cmd == nil || m.chatStreamRenderQueued || !strings.Contains(transcript(m), "agent::queued λ output") {
				t.Fatalf("terminal %s did not force queued output: cmd=%v queued=%t transcript=%q", eventName, cmd != nil, m.chatStreamRenderQueued, transcript(m))
			}
		})
	}
}

func TestChatOutputStreamIncrementalTerminalStaleAndRecovery(t *testing.T) {
	m := newTestModel(t)
	m.selectedID = "project-A"
	m.pendingMsgID = "exec-1"
	m.pendingMsgProjectID = "project-A"
	m.pendingMsgProjectGeneration = m.projectGeneration
	m.chatSubmissionPending = true
	m.chatSubmissionID = 9
	m.busy = true
	m.chatStreamGeneration = 3

	next, _ := m.Update(chatStreamEventMsg{generation: 3, submissionID: 9, projectID: "project-A", execID: "exec-1", event: client.ChatOutputEvent{Data: "Hello"}})
	m = next.(Model)
	if got := transcript(m); strings.Contains(got, "agent::Hello") || !m.chatStreamRenderQueued || m.chatStreamRedraws != 0 {
		t.Fatalf("first delta should queue rather than redraw immediately: transcript=%q queued=%t redraws=%d", got, m.chatStreamRenderQueued, m.chatStreamRedraws)
	}
	next, _ = m.Update(chatStreamEventMsg{generation: 3, submissionID: 9, projectID: "project-A", execID: "exec-1", event: client.ChatOutputEvent{Data: " world"}})
	m = next.(Model)
	if !m.chatStreamRenderQueued || m.chatStreamRedraws != 0 {
		t.Fatalf("additional delta should share the queued redraw: queued=%t redraws=%d", m.chatStreamRenderQueued, m.chatStreamRedraws)
	}
	next, _ = m.Update(chatStreamRenderMsg{generation: 3, renderGeneration: m.chatStreamRenderGeneration, submissionID: 9, projectID: "project-A", execID: "exec-1"})
	m = next.(Model)
	if got := transcript(m); strings.Count(got, "agent::") != 1 || !strings.Contains(got, "agent::Hello world") || m.chatStreamRedraws != 1 {
		t.Fatalf("cadence flush should update one transcript entry once: transcript=%q redraws=%d", got, m.chatStreamRedraws)
	}

	before := transcript(m)
	next, _ = m.Update(chatStreamEventMsg{generation: 2, submissionID: 9, projectID: "project-A", execID: "exec-1", event: client.ChatOutputEvent{Data: " stale"}})
	m = next.(Model)
	if got := transcript(m); got != before {
		t.Fatalf("stale stream changed transcript: %q", got)
	}

	m.chatStreamOffset = len([]byte("Hello world"))
	next, cmd := m.Update(chatStreamDisconnectedMsg{generation: 3, submissionID: 9, projectID: "project-A", execID: "exec-1"})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("disconnected pending stream did not schedule recovery")
	}
	msg := cmd()
	reconnect, ok := msg.(chatStreamReconnectMsg)
	if !ok || reconnect.offset != len([]byte("Hello world")) {
		t.Fatalf("recovery = %#v, want byte offset 11", msg)
	}

	next, _ = m.Update(chatStatusMsg{projectGeneration: m.projectGeneration, messageID: "exec-1", submissionID: 9, projectID: "project-A", status: &client.ChatStatus{MessageID: "exec-1", Status: "completed", Response: "Hello world!"}})
	m = next.(Model)
	if m.busy || m.pendingMsgID != "" || m.chatStreamCancel != nil {
		t.Fatalf("completion did not clean up chat state: busy=%t pending=%q cancel=%v", m.busy, m.pendingMsgID, m.chatStreamCancel != nil)
	}
	if got := transcript(m); strings.Count(got, "agent::") != 1 || !strings.Contains(got, "agent::Hello world!") {
		t.Fatalf("terminal response did not reconcile streamed entry: %q", got)
	}
}

func TestChatOutputTerminalEventsFetchAuthoritativeStatus(t *testing.T) {
	for _, eventName := range []string{"done", "error"} {
		t.Run(eventName, func(t *testing.T) {
			m := newTestModel(t)
			m.selectedID = "project-A"
			m.pendingMsgID = "exec-1"
			m.pendingMsgExecutionID = "exec-1"
			m.pendingMsgProjectID = "project-A"
			m.pendingMsgProjectGeneration = m.projectGeneration
			m.chatSubmissionPending = true
			m.chatSubmissionID = 6
			m.busy = true
			m.chatStreamGeneration = 2

			next, cmd := m.Update(chatStreamEventMsg{generation: 2, submissionID: 6, projectID: "project-A", execID: "exec-1", event: client.ChatOutputEvent{Name: eventName, Data: "terminal signal"}})
			m = next.(Model)
			if cmd == nil {
				t.Fatal("terminal stream event did not fetch status")
			}
			status, ok := cmd().(chatStatusMsg)
			if !ok || status.messageID != "exec-1" {
				t.Fatalf("terminal command returned %#v", status)
			}
			if m.pendingMsgID != "exec-1" || !m.busy {
				t.Fatalf("stream signal settled chat before status: pending=%q busy=%t", m.pendingMsgID, m.busy)
			}
		})
	}
}

func TestChatStatusPartialVisibleWhenOutputStreamDisconnected(t *testing.T) {
	m := newTestModel(t)
	m.selectedID = "project-A"
	m.pendingMsgID = "exec-1"
	m.pendingMsgExecutionID = "exec-1"
	m.pendingMsgProjectID = "project-A"
	m.pendingMsgProjectGeneration = m.projectGeneration
	m.chatSubmissionPending = true
	m.chatSubmissionID = 5
	m.busy = true

	next, cmd := m.Update(chatStatusMsg{projectGeneration: m.projectGeneration, messageID: "exec-1", submissionID: 5, projectID: "project-A", status: &client.ChatStatus{MessageID: "exec-1", Status: "processing", Response: "durable partial"}})
	m = next.(Model)
	if got := transcript(m); !strings.Contains(got, "agent::durable partial") {
		t.Fatalf("polling partial output not visible: %q", got)
	}
	if cmd == nil || m.chatStreamOffset != len([]byte("durable partial")) {
		t.Fatalf("processing status did not preserve polling/recovery offset: cmd=%v offset=%d", cmd != nil, m.chatStreamOffset)
	}
}

func TestChatOutputStreamFailureKeepsPartialAndCleansUp(t *testing.T) {
	for _, status := range []string{"failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			m := newTestModel(t)
			m.selectedID = "project-A"
			m.pendingMsgID = "exec-1"
			m.pendingMsgProjectID = "project-A"
			m.pendingMsgProjectGeneration = m.projectGeneration
			m.chatSubmissionPending = true
			m.chatSubmissionID = 4
			m.busy = true
			m.chatStreamGeneration = 1

			next, _ := m.Update(chatStreamEventMsg{generation: 1, submissionID: 4, projectID: "project-A", execID: "exec-1", event: client.ChatOutputEvent{Data: "partial"}})
			m = next.(Model)
			next, _ = m.Update(chatStatusMsg{projectGeneration: m.projectGeneration, messageID: "exec-1", submissionID: 4, projectID: "project-A", status: &client.ChatStatus{MessageID: "exec-1", Status: status, Error: "boom"}})
			m = next.(Model)
			got := transcript(m)
			if !strings.Contains(got, "agent::partial") || !strings.Contains(got, "error::"+status+": boom") || m.busy || m.pendingMsgID != "" {
				t.Fatalf("%s cleanup/transcript mismatch: %q busy=%t pending=%q", status, got, m.busy, m.pendingMsgID)
			}
		})
	}
}

func TestChatStreamDisconnectFlushesBeforeReconnect(t *testing.T) {
	m := pendingChatStreamTestModel(t)
	m.updateChatStreamOutput("buffered λ")
	m.chatStreamRenderQueued = true

	next, cmd := m.Update(chatStreamDisconnectedMsg{generation: 3, submissionID: 9, projectID: "project-A", execID: "exec-1"})
	m = next.(Model)
	if cmd == nil || m.chatStreamRenderQueued || !strings.Contains(transcript(m), "agent::buffered λ") {
		t.Fatalf("disconnect did not flush before reconnect: cmd=%v queued=%t transcript=%q", cmd != nil, m.chatStreamRenderQueued, transcript(m))
	}
	reconnect, ok := cmd().(chatStreamReconnectMsg)
	if !ok || reconnect.offset != len([]byte("buffered λ")) {
		t.Fatalf("reconnect = %#v, want UTF-8 byte offset %d", reconnect, len([]byte("buffered λ")))
	}
}

func TestChatStreamAuthDisconnectFlushesBeforeInvalidation(t *testing.T) {
	m := pendingChatStreamTestModel(t)
	m.updateChatStreamOutput("visible before sign-in")
	m.chatStreamRenderQueued = true
	authErr := &client.AuthRequiredError{Method: http.MethodGet, Path: "/events/chat/exec-1", StatusCode: http.StatusUnauthorized}

	next, cmd := m.Update(chatStreamDisconnectedMsg{generation: 3, submissionID: 9, projectID: "project-A", execID: "exec-1", err: authErr})
	m = next.(Model)
	if cmd != nil || !m.authRequired || m.chatStreamRenderQueued || !strings.Contains(transcript(m), "agent::visible before sign-in") {
		t.Fatalf("auth disconnect lost buffered output: cmd=%v auth=%t queued=%t transcript=%q", cmd != nil, m.authRequired, m.chatStreamRenderQueued, transcript(m))
	}
}

func TestChatStreamEmptyTerminalFramesDoNotCreateAgentEntry(t *testing.T) {
	for _, eventName := range []string{"done", "error"} {
		t.Run(eventName, func(t *testing.T) {
			m := pendingChatStreamTestModel(t)
			before := len(m.log)
			next, _ := m.Update(chatStreamEventMsg{generation: 3, submissionID: 9, projectID: "project-A", execID: "exec-1", event: client.ChatOutputEvent{Name: eventName}})
			m = next.(Model)
			if len(m.log) != before || m.chatStreamLogIndex >= 0 || strings.Contains(transcript(m), "agent::") {
				t.Fatalf("empty %s frame created agent output: entries=%d index=%d transcript=%q", eventName, len(m.log), m.chatStreamLogIndex, transcript(m))
			}
		})
	}
}

func TestChatStreamMutableReplacementKeepsViewportAtBottom(t *testing.T) {
	m := pendingChatStreamTestModel(t)
	m.log = nil
	for i := 0; i < 80; i++ {
		m.log = append(m.log, entry{role: "system", text: fmt.Sprintf("line-%03d", i)})
	}
	m.refreshTranscript()
	m.updateChatStreamOutput("first")
	m.flushChatStreamOutput()
	m.transcript.GotoTop()
	if m.transcript.AtBottom() {
		t.Fatal("fixture did not scroll away from bottom")
	}
	m.updateChatStreamOutput(" second")
	m.flushChatStreamOutput()
	if !m.transcript.AtBottom() {
		t.Fatal("mutable transcript replacement did not return viewport to bottom")
	}
}

func TestChatStreamReplacementFallsBackAfterCacheInvalidation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		invalidate func(*Model)
	}{
		{name: "resize width", invalidate: func(m *Model) { m.transcript.Width = 44 }},
		{name: "cache shape", invalidate: func(m *Model) { m.transcriptBlocks = m.transcriptBlocks[:len(m.transcriptBlocks)-1] }},
		{name: "history mutation", invalidate: func(m *Model) { m.log = append(m.log, entry{role: "system", text: "intervening history"}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := pendingChatStreamTestModel(t)
			m.updateChatStreamOutput("first")
			m.flushChatStreamOutput()
			tc.invalidate(&m)
			m.updateChatStreamOutput(" second")
			m.flushChatStreamOutput()
			got := m.transcriptContent
			m.refreshTranscript()
			if got != m.transcriptContent || m.log[m.chatStreamLogIndex].text != "first second" {
				t.Fatalf("fallback differs from canonical refresh\ngot  %q\nwant %q", got, m.transcriptContent)
			}
		})
	}
}

func pendingChatStreamTestModel(t *testing.T) Model {
	t.Helper()
	m := newTestModel(t)
	m.selectedID = "project-A"
	m.pendingMsgID = "exec-1"
	m.pendingMsgExecutionID = "exec-1"
	m.pendingMsgProjectID = "project-A"
	m.pendingMsgProjectGeneration = m.projectGeneration
	m.chatSubmissionPending = true
	m.chatSubmissionID = 9
	m.busy = true
	m.chatStreamGeneration = 3
	m.chatStreamExecID = "exec-1"
	return m
}

func TestAuthoritativeCompletionReconcilesBufferedFirstBlockOnce(t *testing.T) {
	m := pendingChatStreamTestModel(t)
	m.updateChatStreamOutput("partial reply")
	m.chatStreamRenderQueued = true

	m.completeChat("final reply", nil)

	if got := strings.Count(transcript(m), "agent::"); got != 1 {
		t.Fatalf("completion rendered %d assistant entries; transcript:\n%s", got, transcript(m))
	}
	if !strings.Contains(transcript(m), "agent::final reply") || strings.Contains(transcript(m), "partial reply") {
		t.Fatalf("completion did not reconcile buffered output exactly: %s", transcript(m))
	}
	if m.busy || m.pendingMsgID != "" || m.chatStreamRenderQueued {
		t.Fatalf("completion left pending state: busy=%t id=%q queued=%t", m.busy, m.pendingMsgID, m.chatStreamRenderQueued)
	}
}

func TestCompletionPreservesStreamedOutputWhenSnapshotEmpty(t *testing.T) {
	m := pendingChatStreamTestModel(t)
	m.updateChatStreamOutput("streamed λ🙂 reply")
	m.chatStreamRenderQueued = true

	m.completeChat("", nil)

	if got := strings.Count(transcript(m), "agent::"); got != 1 {
		t.Fatalf("completion rendered %d assistant entries; transcript:\n%s", got, transcript(m))
	}
	if !strings.Contains(transcript(m), "agent::streamed λ🙂 reply") {
		t.Fatalf("empty completion snapshot erased streamed output: %s", transcript(m))
	}
	if m.busy || m.pendingMsgID != "" || m.chatStreamRenderQueued {
		t.Fatalf("completion left pending state: busy=%t id=%q queued=%t", m.busy, m.pendingMsgID, m.chatStreamRenderQueued)
	}
}

func TestCompletionPreservesStreamedOutputWhenSnapshotIsShorterPrefix(t *testing.T) {
	m := pendingChatStreamTestModel(t)
	m.updateChatStreamOutput("streamed λ🙂 reply")
	m.chatStreamRenderQueued = true

	m.completeChat("streamed λ", nil)

	if got := strings.Count(transcript(m), "agent::"); got != 1 {
		t.Fatalf("completion rendered %d assistant entries; transcript:\n%s", got, transcript(m))
	}
	if !strings.Contains(transcript(m), "agent::streamed λ🙂 reply") {
		t.Fatalf("lagging completion snapshot shortened streamed output: %s", transcript(m))
	}
	if m.busy || m.pendingMsgID != "" || m.chatStreamRenderQueued {
		t.Fatalf("completion left pending state: busy=%t id=%q queued=%t", m.busy, m.pendingMsgID, m.chatStreamRenderQueued)
	}
}

func TestForcedChatStreamFlushInvalidatesOldCadenceTimer(t *testing.T) {
	m := pendingChatStreamTestModel(t)
	m.updateChatStreamOutput("first")
	if cmd := m.scheduleChatStreamRender(); cmd == nil {
		t.Fatal("first delta did not schedule render")
	}
	oldRenderGeneration := m.chatStreamRenderGeneration

	m.append(entry{role: "system", text: "later entry"})
	if m.chatStreamRenderQueued || m.chatStreamRedraws != 1 {
		t.Fatalf("forced append flush state: queued=%t redraws=%d, want false/1", m.chatStreamRenderQueued, m.chatStreamRedraws)
	}
	m.updateChatStreamOutput(" second")
	if cmd := m.scheduleChatStreamRender(); cmd == nil {
		t.Fatal("second delta did not schedule replacement render")
	}
	newRenderGeneration := m.chatStreamRenderGeneration
	if newRenderGeneration == oldRenderGeneration {
		t.Fatalf("replacement timer reused render generation %d", newRenderGeneration)
	}

	next, _ := m.Update(chatStreamRenderMsg{
		generation:       3,
		renderGeneration: oldRenderGeneration,
		submissionID:     9,
		projectID:        "project-A",
		execID:           "exec-1",
	})
	m = next.(Model)
	if !m.chatStreamRenderQueued || m.chatStreamRedraws != 1 || strings.Contains(transcript(m), "first second") {
		t.Fatalf("old timer affected replacement render: queued=%t redraws=%d transcript=%q", m.chatStreamRenderQueued, m.chatStreamRedraws, transcript(m))
	}

	next, _ = m.Update(chatStreamRenderMsg{
		generation:       3,
		renderGeneration: newRenderGeneration,
		submissionID:     9,
		projectID:        "project-A",
		execID:           "exec-1",
	})
	m = next.(Model)
	if m.chatStreamRenderQueued || m.chatStreamRedraws != 2 || !strings.Contains(transcript(m), "agent::first second") {
		t.Fatalf("replacement timer did not render: queued=%t redraws=%d transcript=%q", m.chatStreamRenderQueued, m.chatStreamRedraws, transcript(m))
	}
}

func TestBufferedChatOutputPrecedesEveryLaterTranscriptAppend(t *testing.T) {
	for _, tc := range []struct {
		name   string
		update func(Model) Model
		role   string
		text   string
	}{
		{
			name: "unrelated visible SSE event",
			update: func(m Model) Model {
				m.showEvents = true
				next, _ := m.Update(sseEventMsg{event: client.Event{
					Name: "task_updated",
					Data: json.RawMessage(`{"type":"task_updated","project_id":"project-A","task_name":"later task"}`),
				}})
				return next.(Model)
			},
			role: "event",
			text: "task_updated",
		},
		{
			name: "asynchronous command result",
			update: func(m Model) Model {
				next, _ := m.Update(resultMsg{
					sessionGeneration: sessionGenerationOf(m),
					projectGeneration: projectGenerationOf(m),
					title:             "Later result",
					body:              "command finished",
				})
				return next.(Model)
			},
			role: "result",
			text: "command finished",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := pendingChatStreamTestModel(t)
			m.updateChatStreamOutput("accepted assistant bytes")
			m.chatStreamRenderQueued = true

			m = tc.update(m)

			if len(m.log) < 2 {
				t.Fatalf("expected assistant and later entry, got %#v", m.log)
			}
			assistant := m.log[len(m.log)-2]
			later := m.log[len(m.log)-1]
			if assistant.role != "agent" || assistant.text != "accepted assistant bytes" {
				t.Fatalf("entry before later append = %#v, want buffered assistant", assistant)
			}
			if later.role != tc.role || !strings.Contains(later.text, tc.text) {
				t.Fatalf("later entry = %#v, want role=%q containing %q", later, tc.role, tc.text)
			}
			if got := strings.Count(transcript(m), "agent::accepted assistant bytes"); got != 1 {
				t.Fatalf("assistant rendered %d times; transcript:\n%s", got, transcript(m))
			}
		})
	}
}

func TestBufferedChatOutputPrecedesVisibleSSECompletionEvent(t *testing.T) {
	m := pendingChatStreamTestModel(t)
	m.showEvents = true
	m.chatStreamRenderQueued = true
	m.updateChatStreamOutput("partial reply")

	payload, err := json.Marshal(client.ChatEvent{
		Type:            "chat_response_done",
		ProjectID:       "project-A",
		ExecID:          "exec-1",
		CompletedOutput: "final reply",
	})
	if err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(sseEventMsg{event: client.Event{
		Name: "chat_response_done",
		Data: payload,
	}})
	m = next.(Model)

	if len(m.log) < 2 {
		t.Fatalf("expected assistant output and visible event, got %#v", m.log)
	}
	assistant := m.log[len(m.log)-2]
	visibleEvent := m.log[len(m.log)-1]
	if assistant.role != "agent" || assistant.text != "final reply" {
		t.Fatalf("entry before event = %#v, want reconciled assistant output", assistant)
	}
	if visibleEvent.role != "event" || !strings.Contains(visibleEvent.text, "chat_response_done") {
		t.Fatalf("final entry = %#v, want visible completion event", visibleEvent)
	}
	if got := strings.Count(transcript(m), "agent::final reply"); got != 1 {
		t.Fatalf("final output rendered %d times; transcript:\n%s", got, transcript(m))
	}
	if strings.Contains(transcript(m), "partial reply") {
		t.Fatalf("partial output was not authoritatively reconciled:\n%s", transcript(m))
	}
	if m.busy || m.pendingMsgID != "" || m.chatStreamRenderQueued {
		t.Fatalf("completion left pending state: busy=%t id=%q queued=%t", m.busy, m.pendingMsgID, m.chatStreamRenderQueued)
	}
}

// TestSSEChatResponseDoneCompletedOutputFastPath verifies that when the
// chat_response_done event carries a non-empty CompletedOutput, the model
// displays the reply immediately without an extra HTTP round-trip.
func TestSSEChatResponseDoneCompletedOutputFastPath(t *testing.T) {
	m := newTestModel(t)
	m.selectedID = "project-A"
	m.pendingMsgID = "msg-99"
	m.pendingMsgExecutionID = "exec-99"
	m.pendingMsgProjectID = "project-A"
	m.pendingMsgProjectGeneration = 1
	m.busy = true

	payload, _ := json.Marshal(client.ChatEvent{
		Type:            "chat_response_done",
		ProjectID:       "project-A",
		ExecID:          "msg-99",
		CompletedOutput: "hello from the agent",
	})
	ev := client.Event{
		Name: "chat_response_done",
		Data: json.RawMessage(payload),
	}
	next, _ := m.Update(sseEventMsg{event: ev})
	m = next.(Model)

	if m.busy || m.pendingMsgID != "" || m.pendingMsgExecutionID != "" || m.pendingMsgProjectID != "" || m.pendingMsgProjectGeneration != 0 {
		t.Errorf("inline completion should clear pending state: busy=%t id=%q execution=%q project=%q generation=%d", m.busy, m.pendingMsgID, m.pendingMsgExecutionID, m.pendingMsgProjectID, m.pendingMsgProjectGeneration)
	}
	out := transcript(m)
	if !strings.Contains(out, "agent::hello from the agent") {
		t.Errorf("expected agent reply in transcript:\n%s", out)
	}
	if strings.Contains(out, "created tasks:") {
		t.Errorf("inline SSE completion should not invent task metadata:\n%s", out)
	}
	duplicate, _ := m.Update(sseEventMsg{event: ev})
	m = duplicate.(Model)
	if got := strings.Count(transcript(m), "agent::hello from the agent"); got != 1 {
		t.Fatalf("duplicate SSE completion rendered the reply %d times", got)
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

func runStartupProjectLoginRecovery(t *testing.T, switchProject bool) Model {
	t.Helper()

	projectIDs := make(chan string, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			http.SetCookie(w, &http.Cookie{Name: "ov_session", Value: "session-token"})
			w.Header().Set("Location", "/")
			w.WriteHeader(http.StatusFound)
		case "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"projects":[{"id":"alpha-id","name":"alpha"},{"id":"beta-id","name":"beta"}]}`))
		case "/api/capacity/global":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"has_capacity":true}`))
		case "/auth/me":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"authenticated":true}`))
		case "/events/live":
			projectIDs <- r.URL.Query().Get("project_id")
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, ": ping\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c).WithProject("alpha")
	var loadCmd tea.Cmd
	m, loadCmd = m.beginProjectLoadWithSSE(false, m.wantProject, true)
	loaded, ok := loadCmd().(projectsLoadedMsg)
	if !ok {
		t.Fatalf("startup project load returned %T, want projectsLoadedMsg", loadCmd())
	}
	updated, streamCmd := m.Update(loaded)
	m = updated.(Model)
	if streamCmd == nil {
		t.Fatal("startup project load did not start SSE")
	}
	if m.selectedID != "alpha-id" || m.selectedName != "alpha" {
		t.Fatalf("startup project selection = %q (%q), want alpha-id (alpha)", m.selectedID, m.selectedName)
	}
	waitForProjectID := func(want string) {
		t.Helper()
		select {
		case got := <-projectIDs:
			if got != want {
				t.Fatalf("SSE project_id = %q, want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for SSE project_id %q", want)
		}
	}
	waitForProjectID("alpha-id")

	if switchProject {
		m.threadID = "alpha-thread"
		m.threadTitle = "alpha thread"
		m.pendingMsgID = "alpha-message"
		m.pendingMsgProjectID = "alpha-id"
		m.pendingMsgProjectGeneration = m.projectGeneration
		m.busy = true

		var reconnect tea.Cmd
		m, reconnect = m.pickProject("beta")
		if reconnect == nil {
			t.Fatal("explicit project switch did not reconnect SSE")
		}
		if m.selectedID != "beta-id" || m.selectedName != "beta" {
			t.Fatalf("explicit project selection = %q (%q), want beta-id (beta)", m.selectedID, m.selectedName)
		}
		if m.threadID != "" || m.pendingMsgID != "" || m.pendingMsgProjectID != "" || m.busy {
			t.Fatalf("project switch did not clear old project context: thread=%q pending=%q pendingProject=%q busy=%t", m.threadID, m.pendingMsgID, m.pendingMsgProjectID, m.busy)
		}
		waitForProjectID("beta-id")
	}

	m.authRequired = true
	m.connected = false
	m.connChecked = true
	m, _ = m.beginLogin()
	m.loginPassword = true
	m.loginSubmitting = true
	loginResult, ok := m.login("admin", "correct-password")().(loginResultMsg)
	if !ok {
		t.Fatalf("login command returned %T, want loginResultMsg", m.login("admin", "correct-password")())
	}
	updated, retry := m.Update(loginResult)
	m = updated.(Model)
	m = applyImmediateAuthRetryBatch(t, m, retry)

	wantProjectID := "alpha-id"
	if switchProject {
		wantProjectID = "beta-id"
	}
	waitForProjectID(wantProjectID)
	if m.selectedID != wantProjectID {
		t.Fatalf("post-login selected project = %q, want %q", m.selectedID, wantProjectID)
	}
	if switchProject && (m.threadID != "" || m.pendingMsgID != "" || m.pendingMsgProjectID != "") {
		t.Fatalf("post-login reinstated old project context: thread=%q pending=%q pendingProject=%q", m.threadID, m.pendingMsgID, m.pendingMsgProjectID)
	}
	m.Cleanup()
	return m
}

func TestLoginRecoveryPreservesExplicitProjectSwitch(t *testing.T) {
	m := runStartupProjectLoginRecovery(t, true)
	if m.selectedID != "beta-id" || m.selectedName != "beta" {
		t.Fatalf("selected project after explicit switch/login = %q (%q), want beta-id (beta)", m.selectedID, m.selectedName)
	}
}

func TestLoginRecoveryPreservesStartupProjectWithoutSwitch(t *testing.T) {
	m := runStartupProjectLoginRecovery(t, false)
	if m.selectedID != "alpha-id" || m.selectedName != "alpha" {
		t.Fatalf("selected project without explicit switch/login = %q (%q), want alpha-id (alpha)", m.selectedID, m.selectedName)
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
