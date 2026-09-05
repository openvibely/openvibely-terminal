package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-tui/internal/client"
)

const taskDetailHTML = `<div data-task-status="running" data-task-category="active">
  <h2 class="text-2xl font-bold truncate">Refactor the API</h2>
  <div id="tab-details">the prompt</div>
  <div id="tab-chat"></div>
</div>`

// threadModel wires a model whose backend serves a board and one task detail.
func threadModel(t *testing.T) (Model, *recorder) {
	t.Helper()
	return dispatchModel(t, map[string]string{
		"/tasks":                              strings.Replace(taskBoardHTML, `data-task-status="pending"`, `data-task-status="running"`, 1),
		"/tasks/t-1":                          taskDetailHTML,
		"/tasks/t-1/thread":                   `<div>agent: working on it</div>`,
		"/tasks/t-1/changes":                  `<div>2 files changed</div>`,
		"/api/tasks/t-1/lifecycle-executions": `[]`,
	})
}

// "/tasks open <ref>" should enter thread mode for that task.
func TestTasksOpenEntersThreadMode(t *testing.T) {
	m, _ := threadModel(t)
	m = runLine(t, m, "/tasks open Refactor")

	if m.threadID != "t-1" {
		t.Fatalf("threadID = %q, want t-1; transcript:\n%s", m.threadID, transcript(m))
	}
	if m.threadTitle != "Refactor the API" {
		t.Errorf("threadTitle = %q", m.threadTitle)
	}
	out := transcript(m)
	if !strings.Contains(out, "working on it") {
		t.Errorf("thread content missing:\n%s", out)
	}
	if !strings.Contains(m.View(), "Refactor the API") {
		t.Error("header should show the open thread")
	}
}

func TestTasksOpenLoadsOnlyConversationAndSuppressesModelControls(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{
		"/tasks":            `<div data-task-id="t-1" data-task-status="running" data-task-category="active"><a href="/tasks/t-1" title="Refactor the API">Refactor</a></div>`,
		"/tasks/t-1/thread": `<div data-task-thread><div class="chat-message">agent: existing answer</div><form><label>Model</label><select name="model"><option>Claude Sonnet</option><option>GPT-5</option></select></form></div>`,
	})

	m = runLine(t, m, "/tasks open Refactor")
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "existing answer") {
		t.Fatalf("task conversation missing:\n%s", out)
	}
	for _, unwanted := range []string{"Claude Sonnet", "GPT-5", "Model"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("opening task rendered unrelated model control %q:\n%s", unwanted, out)
		}
	}
	for _, unwantedRequest := range []string{"GET /tasks/t-1?project_id=p1", "GET /tasks/t-1/changes", "GET /api/tasks/t-1/lifecycle-executions", "GET /models"} {
		if rec.sawQuery(unwantedRequest) {
			t.Errorf("opening task made unrelated request %q:\n%v", unwantedRequest, rec.urlsSnapshot())
		}
	}
	if !rec.sawQuery("GET /tasks/t-1/thread?project_id=p1") {
		t.Fatalf("task thread request was not project scoped:\n%v", rec.urlsSnapshot())
	}
}

func TestRunningOpenTaskRefreshesFromMatchingSSEAndCompletes(t *testing.T) {
	m, rec := threadModel(t)
	m = runLine(t, m, "/tasks open Refactor")
	m.sseGeneration = 7
	m.sseEvents = make(chan client.Event)
	m.sseErrs = make(chan error)

	updated, cmd := m.Update(sseEventMsg{generation: 7, event: client.Event{
		Name: "task_status_changed",
		Data: json.RawMessage(`{"type":"task_status_changed","project_id":"p1","task_id":"t-1","status":"completed","message":"finished"}`),
	}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("matching task completion did not schedule a final thread refresh")
	}
	executeThreadRefreshFromBatch(t, &m, cmd)

	if m.threadStatus != "completed" {
		t.Errorf("threadStatus = %q, want completed", m.threadStatus)
	}
	if got := rec.count("GET", "/tasks/t-1/thread"); got != 2 {
		t.Errorf("thread requests = %d, want open plus final refresh; calls:\n%s", got, rec.all())
	}
	if !strings.Contains(stripANSI(transcript(m)), "completed") {
		t.Errorf("terminal task state missing:\n%s", transcript(m))
	}
}

func TestRunningOpenTaskShowsLiveMessageAndFailure(t *testing.T) {
	m, _ := threadModel(t)
	m = runLine(t, m, "/tasks open Refactor")
	m.sseGeneration = 8
	m.sseEvents = make(chan client.Event)
	m.sseErrs = make(chan error)

	updated, _ := m.Update(sseEventMsg{generation: 8, event: client.Event{
		Name: "chat_new_message",
		Data: json.RawMessage(`{"type":"chat_new_message","project_id":"p1","task_id":"t-1","message":"streamed worker output"}`),
	}})
	m = updated.(Model)
	if !strings.Contains(stripANSI(transcript(m)), "streamed worker output") {
		t.Fatalf("live task output missing:\n%s", transcript(m))
	}

	updated, cmd := m.Update(sseEventMsg{generation: 8, event: client.Event{
		Name: "task_status_changed",
		Data: json.RawMessage(`{"type":"task_status_changed","project_id":"p1","task_id":"t-1","status":"failed","message":"tests failed"}`),
	}})
	m = updated.(Model)
	executeThreadRefreshFromBatch(t, &m, cmd)
	if m.threadStatus != "failed" {
		t.Errorf("threadStatus = %q, want failed", m.threadStatus)
	}
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "failed: tests failed") {
		t.Errorf("failure state missing:\n%s", out)
	}
}

func TestOpenTaskNotRunningIgnoresLiveTaskOutput(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{
		"/tasks":            `<div data-task-id="t-1" data-task-status="completed" data-task-category="completed"><a href="/tasks/t-1" title="Finished task">Finished task</a></div>`,
		"/tasks/t-1/thread": `<div>final answer</div>`,
	})
	m = runLine(t, m, "/tasks open Finished")
	m.sseGeneration = 9
	m.sseEvents = make(chan client.Event)
	m.sseErrs = make(chan error)

	updated, _ := m.Update(sseEventMsg{generation: 9, event: client.Event{
		Name: "chat_new_message",
		Data: json.RawMessage(`{"type":"chat_new_message","project_id":"p1","task_id":"t-1","message":"late unrelated output"}`),
	}})
	m = updated.(Model)
	if strings.Contains(stripANSI(transcript(m)), "late unrelated output") {
		t.Errorf("inactive task consumed live output:\n%s", transcript(m))
	}
	if got := rec.count("GET", "/tasks/t-1/thread"); got != 1 {
		t.Errorf("inactive task triggered refresh; calls:\n%s", rec.all())
	}
}

func TestChatInvalidatesPendingTaskOpen(t *testing.T) {
	m, _ := threadModel(t)
	opened, openCmd := m.runCommand("/tasks open Refactor")
	m = opened.(Model)
	left, _ := m.runCommand("/chat")
	m = left.(Model)

	updated, _ := m.Update(openCmd())
	m = updated.(Model)
	if m.threadID != "" {
		t.Fatalf("delayed open entered thread %q after /chat", m.threadID)
	}
	if strings.Contains(stripANSI(transcript(m)), "working on it") {
		t.Fatalf("delayed open rendered after /chat:\n%s", transcript(m))
	}
}

func TestNewerTaskOpenSupersedesOlderOpenInSameProject(t *testing.T) {
	const board = `<div>
		<div class="card" data-task-id="t-a" data-task-status="running" data-task-category="active" data-display-order="0"><a href="/tasks/t-a" title="Alpha">Alpha</a></div>
		<div class="card" data-task-id="t-b" data-task-status="running" data-task-category="active" data-display-order="1"><a href="/tasks/t-b" title="Beta">Beta</a></div>
	</div>`
	m, _ := dispatchModel(t, map[string]string{
		"/tasks":            board,
		"/tasks/t-a/thread": `<div>alpha thread</div>`,
		"/tasks/t-b/thread": `<div>beta thread</div>`,
	})

	first, firstCmd := m.runCommand("/tasks open Alpha")
	m = first.(Model)
	second, secondCmd := m.runCommand("/tasks open Beta")
	m = second.(Model)

	updated, _ := m.Update(secondCmd())
	m = updated.(Model)
	updated, _ = m.Update(firstCmd())
	m = updated.(Model)

	if m.threadID != "t-b" || m.threadTitle != "Beta" {
		t.Fatalf("older open replaced newer thread: id=%q title=%q", m.threadID, m.threadTitle)
	}
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "beta thread") || strings.Contains(out, "alpha thread") {
		t.Fatalf("stale open changed transcript:\n%s", out)
	}
}

func TestOlderRunningRefreshCannotRegressTerminalThread(t *testing.T) {
	m, _ := threadModel(t)
	m = runLine(t, m, "/tasks open Refactor")
	m.sseGeneration = 10
	m.sseEvents = make(chan client.Event)
	m.sseErrs = make(chan error)

	updated, runningCmd := m.Update(sseEventMsg{generation: 10, event: client.Event{
		Name: "task_status_changed",
		Data: json.RawMessage(`{"type":"task_status_changed","project_id":"p1","task_id":"t-1","status":"running"}`),
	}})
	m = updated.(Model)
	updated, terminalCmd := m.Update(sseEventMsg{generation: 10, event: client.Event{
		Name: "task_status_changed",
		Data: json.RawMessage(`{"type":"task_status_changed","project_id":"p1","task_id":"t-1","status":"completed"}`),
	}})
	m = updated.(Model)

	terminal := threadRefreshMessageFromBatch(t, terminalCmd)
	terminal.body = "terminal thread"
	updated, _ = m.Update(terminal)
	m = updated.(Model)

	older := threadRefreshMessageFromBatch(t, runningCmd)
	older.body = "older running thread"
	updated, _ = m.Update(older)
	m = updated.(Model)

	if m.threadStatus != "completed" {
		t.Fatalf("older refresh regressed status to %q", m.threadStatus)
	}
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "terminal thread") || strings.Contains(out, "older running thread") {
		t.Fatalf("older refresh regressed thread content:\n%s", out)
	}
}

func threadRefreshMessageFromBatch(t *testing.T, cmd tea.Cmd) threadUpdatedMsg {
	t.Helper()
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok || len(batch) < 2 {
		t.Fatalf("refresh command = %T, want batch containing SSE wait and refresh", msg)
	}
	refresh, ok := batch[len(batch)-1]().(threadUpdatedMsg)
	if !ok {
		t.Fatalf("refresh message = %T, want threadUpdatedMsg", batch[len(batch)-1]())
	}
	return refresh
}

func executeThreadRefreshFromBatch(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	refreshMsg := threadRefreshMessageFromBatch(t, cmd)
	updated, _ := m.Update(refreshMsg)
	*m = updated.(Model)
}

func TestDelayedThreadReplyIgnoredAfterChat(t *testing.T) {
	m, _ := threadModel(t)
	m = runLine(t, m, "/tasks open Refactor")
	m.input.SetValue("please add tests")
	submitted, replyCmd := m.submit()
	m = submitted.(Model)

	left, _ := m.runCommand("/chat")
	m = left.(Model)
	before := transcript(m)

	updated, _ := m.Update(replyCmd())
	m = updated.(Model)
	if m.threadID != "" {
		t.Fatalf("delayed reply re-entered thread %q", m.threadID)
	}
	if got := transcript(m); got != before {
		t.Fatalf("delayed reply changed project-chat transcript:\nbefore:\n%s\nafter:\n%s", before, got)
	}
}

func TestDelayedThreadReplyIgnoredAfterNewerOpen(t *testing.T) {
	const board = `<div>
		<div class="card" data-task-id="t-a" data-task-status="running" data-task-category="active"><a href="/tasks/t-a" title="Alpha">Alpha</a></div>
		<div class="card" data-task-id="t-b" data-task-status="running" data-task-category="active"><a href="/tasks/t-b" title="Beta">Beta</a></div>
	</div>`
	m, _ := dispatchModel(t, map[string]string{
		"/tasks":            board,
		"/tasks/t-a/thread": `<div>alpha thread</div>`,
		"/tasks/t-b/thread": `<div>beta thread</div>`,
	})
	m = runLine(t, m, "/tasks open Alpha")
	m.input.SetValue("alpha follow-up")
	submitted, replyCmd := m.submit()
	m = submitted.(Model)
	m = runLine(t, m, "/tasks open Beta")
	before := transcript(m)

	updated, _ := m.Update(replyCmd())
	m = updated.(Model)
	if m.threadID != "t-b" || m.threadTitle != "Beta" {
		t.Fatalf("delayed reply replaced newer thread: id=%q title=%q", m.threadID, m.threadTitle)
	}
	if got := transcript(m); got != before {
		t.Fatalf("delayed reply changed newer thread transcript:\nbefore:\n%s\nafter:\n%s", before, got)
	}
}

func TestDelayedThreadReplyCannotRegressTerminalRefresh(t *testing.T) {
	m, _ := threadModel(t)
	m = runLine(t, m, "/tasks open Refactor")
	m.input.SetValue("please add tests")
	submitted, replyCmd := m.submit()
	m = submitted.(Model)

	m.sseGeneration = 11
	m.sseEvents = make(chan client.Event)
	m.sseErrs = make(chan error)
	updated, terminalCmd := m.Update(sseEventMsg{generation: 11, event: client.Event{
		Name: "task_status_changed",
		Data: json.RawMessage(`{"type":"task_status_changed","project_id":"p1","task_id":"t-1","status":"completed"}`),
	}})
	m = updated.(Model)
	terminal := threadRefreshMessageFromBatch(t, terminalCmd)
	terminal.body = "terminal thread"
	updated, _ = m.Update(terminal)
	m = updated.(Model)
	before := transcript(m)

	updated, _ = m.Update(replyCmd())
	m = updated.(Model)
	if m.threadStatus != "completed" {
		t.Fatalf("delayed reply regressed terminal status to %q", m.threadStatus)
	}
	if m.busy {
		t.Fatal("terminal refresh left superseded thread reply busy")
	}
	if got := transcript(m); got != before {
		t.Fatalf("delayed reply replaced terminal transcript:\nbefore:\n%s\nafter:\n%s", before, got)
	}
}

func TestOpenTaskLiveEventsRequireExactCurrentOwnership(t *testing.T) {
	tests := []struct {
		name       string
		generation int
		eventName  string
		payload    string
	}{
		{name: "message without project", generation: 12, eventName: "chat_new_message", payload: `{"type":"chat_new_message","task_id":"t-1","message":"unowned"}`},
		{name: "status without project", generation: 12, eventName: "task_status_changed", payload: `{"type":"task_status_changed","task_id":"t-1","status":"completed","message":"unowned status"}`},
		{name: "foreign project", generation: 12, eventName: "chat_new_message", payload: `{"type":"chat_new_message","project_id":"p2","task_id":"t-1","message":"foreign project"}`},
		{name: "foreign task", generation: 12, eventName: "chat_new_message", payload: `{"type":"chat_new_message","project_id":"p1","task_id":"t-2","message":"foreign task"}`},
		{name: "stale stream", generation: 11, eventName: "chat_new_message", payload: `{"type":"chat_new_message","project_id":"p1","task_id":"t-1","message":"stale stream"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, rec := threadModel(t)
			m = runLine(t, m, "/tasks open Refactor")
			m.sseGeneration = 12
			m.sseEvents = make(chan client.Event)
			m.sseErrs = make(chan error)
			before := transcript(m)

			updated, _ := m.Update(sseEventMsg{generation: tt.generation, event: client.Event{
				Name: tt.eventName,
				Data: json.RawMessage(tt.payload),
			}})
			m = updated.(Model)

			if got := transcript(m); got != before {
				t.Fatalf("unowned live event changed transcript:\nbefore:\n%s\nafter:\n%s", before, got)
			}
			if m.threadStatus != "running" {
				t.Fatalf("unowned live event changed status to %q", m.threadStatus)
			}
			if got := rec.count("GET", "/tasks/t-1/thread"); got != 1 {
				t.Fatalf("unowned live event triggered refresh; requests = %d", got)
			}
		})
	}
}

func TestTaskLiveEventsWithoutProjectAreRejectedBeforeEventsDisplay(t *testing.T) {
	for _, tt := range []struct {
		name      string
		eventName string
		payload   string
	}{
		{name: "message", eventName: "chat_new_message", payload: `{"type":"chat_new_message","task_id":"t-1","message":"unowned output"}`},
		{name: "status", eventName: "task_status_changed", payload: `{"type":"task_status_changed","task_id":"t-1","status":"completed","message":"unowned status"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, rec := threadModel(t)
			m = runLine(t, m, "/tasks open Refactor")
			m.showEvents = true
			m.sseGeneration = 13
			m.sseEvents = make(chan client.Event)
			m.sseErrs = make(chan error)
			before := transcript(m)

			updated, _ := m.Update(sseEventMsg{generation: 13, event: client.Event{
				Name: tt.eventName,
				Data: json.RawMessage(tt.payload),
			}})
			m = updated.(Model)

			if got := transcript(m); got != before {
				t.Fatalf("unowned task event reached /events display:\nbefore:\n%s\nafter:\n%s", before, got)
			}
			if m.threadStatus != "running" {
				t.Fatalf("unowned task event changed status to %q", m.threadStatus)
			}
			if got := rec.count("GET", "/tasks/t-1/thread"); got != 1 {
				t.Fatalf("unowned task event triggered refresh; requests = %d", got)
			}
		})
	}
}

func TestProjectChatEventWithoutProjectRetainsEventsDisplayCompatibility(t *testing.T) {
	m, _ := threadModel(t)
	m.showEvents = true
	m.sseGeneration = 14
	m.sseEvents = make(chan client.Event)
	m.sseErrs = make(chan error)

	updated, _ := m.Update(sseEventMsg{generation: 14, event: client.Event{
		Name: "chat_new_message",
		Data: json.RawMessage(`{"type":"chat_new_message","message":"compatible project chat"}`),
	}})
	m = updated.(Model)

	if !strings.Contains(transcript(m), "compatible project chat") {
		t.Fatalf("project-chat compatibility event was suppressed:\n%s", transcript(m))
	}
}

// While in a thread, plain text posts to that task's thread endpoint rather
// than to the project chat endpoint.
func TestThreadMessageGoesToTaskNotProjectChat(t *testing.T) {
	m, rec := threadModel(t)
	m = runLine(t, m, "/tasks open Refactor")
	if m.threadID == "" {
		t.Fatal("failed to enter thread")
	}

	m = runLine(t, m, "please add tests")

	if !rec.saw("POST", "/tasks/t-1/thread") {
		t.Fatalf("expected a thread post, calls:\n%s", rec.all())
	}
	if rec.saw("POST", "/api/chat/message") {
		t.Errorf("message leaked to project chat, calls:\n%s", rec.all())
	}
}

// Outside a thread, plain text still goes to the project agent.
func TestPlainTextGoesToProjectChatOutsideThread(t *testing.T) {
	m, rec := threadModel(t)
	m = runLine(t, m, "hello there")

	if !rec.saw("POST", "/api/chat/message") {
		t.Fatalf("expected a project chat post, calls:\n%s", rec.all())
	}
}

// "/chat" leaves thread mode; "/back" still works as an alias.
func TestChatLeavesThreadMode(t *testing.T) {
	for _, line := range []string{"/chat", "/back"} {
		t.Run(line, func(t *testing.T) {
			testLeavesThread(t, line)
		})
	}
}

func testLeavesThread(t *testing.T, line string) {
	t.Helper()
	m, _ := threadModel(t)
	m = runLine(t, m, "/tasks open Refactor")
	if m.threadID == "" {
		t.Fatal("failed to enter thread")
	}

	m = runLine(t, m, line)

	if m.threadID != "" || m.threadTitle != "" {
		t.Errorf("still in thread: id=%q title=%q", m.threadID, m.threadTitle)
	}
	if m.input.Placeholder != defaultPlaceholder {
		t.Errorf("placeholder = %q, want default", m.input.Placeholder)
	}
	if !strings.Contains(transcript(m), "back to project chat") {
		t.Errorf("no exit confirmation:\n%s", transcript(m))
	}
}

// "/chat" outside a thread is a no-op with a clear message.
func TestChatOutsideThreadIsNoop(t *testing.T) {
	m, _ := threadModel(t)
	m = runLine(t, m, "/chat")
	if !strings.Contains(transcript(m), "already in project chat") {
		t.Errorf("transcript:\n%s", transcript(m))
	}
	if m.threadID != "" {
		t.Errorf("unexpectedly entered a thread")
	}
}

// "/chat <message>" sends to the project agent, leaving any thread first.
func TestChatWithMessageSendsToProject(t *testing.T) {
	m, rec := threadModel(t)
	m = runLine(t, m, "/tasks open Refactor")
	if m.threadID == "" {
		t.Fatal("failed to enter thread")
	}

	m = runLine(t, m, "/chat ship the docs")

	if m.threadID != "" {
		t.Errorf("still in thread %q", m.threadID)
	}
	if !rec.saw("POST", "/api/chat/message") {
		t.Fatalf("expected a project chat post, calls:\n%s", rec.all())
	}
}

// Switching projects must leave a thread that belongs to the old project.
func TestSwitchingProjectLeavesThread(t *testing.T) {
	m, _ := threadModel(t)
	m = runLine(t, m, "/tasks open Refactor")
	if m.threadID == "" {
		t.Fatal("failed to enter thread")
	}

	m.projects = []client.Project{{ID: "p2", Name: "other"}}
	m, _ = m.pickProject("other")

	if m.selectedID != "p2" || m.selectedName != "other" {
		t.Fatalf("selected project = %q/%q, want p2/other", m.selectedID, m.selectedName)
	}
	if m.threadID != "" || m.threadTitle != "" {
		t.Errorf("thread survived a project switch: id=%q title=%q", m.threadID, m.threadTitle)
	}
	if m.input.Placeholder != defaultPlaceholder {
		t.Errorf("placeholder = %q, want default", m.input.Placeholder)
	}
}

// Selecting the already-active project must not leave the current task thread.
func TestSelectingSameProjectPreservesThread(t *testing.T) {
	m, _ := threadModel(t)
	m = runLine(t, m, "/tasks open Refactor")
	if m.threadID == "" {
		t.Fatal("failed to enter thread")
	}
	wantID, wantTitle, wantPlaceholder := m.threadID, m.threadTitle, m.input.Placeholder

	m.projects = []client.Project{{ID: "p1", Name: "demo"}}
	m, _ = m.pickProject("demo")

	if m.selectedID != "p1" || m.selectedName != "demo" {
		t.Fatalf("selected project = %q/%q, want p1/demo", m.selectedID, m.selectedName)
	}
	if m.threadID != wantID || m.threadTitle != wantTitle {
		t.Errorf("same-project selection changed thread: id=%q title=%q, want %q/%q", m.threadID, m.threadTitle, wantID, wantTitle)
	}
	if m.input.Placeholder != wantPlaceholder {
		t.Errorf("same-project selection changed placeholder: %q, want %q", m.input.Placeholder, wantPlaceholder)
	}
}

// A threadOpenedMsg from a previous project must be discarded after a project switch.
func TestStaleThreadOpenedMsgDiscardedAfterProjectSwitch(t *testing.T) {
	m, _ := threadModel(t)
	// Simulate a project switch: select project B, clear thread state.
	m.selectedID = "project-B"
	m.threadID = ""
	m.threadTitle = ""

	// Deliver a stale threadOpenedMsg carrying the old project's ID.
	updated, _ := m.Update(threadOpenedMsg{
		projectID: "project-A-id",
		taskID:    "task-from-A",
		title:     "Old Task",
		body:      "some content",
	})
	m = updated.(Model)

	if m.threadID != "" {
		t.Errorf("stale threadOpenedMsg set threadID = %q, want empty", m.threadID)
	}
}

// A failure to open a task must not put the model into thread mode.
func TestTasksOpenFailureDoesNotEnterThread(t *testing.T) {
	m, _ := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML})
	m = runLine(t, m, "/tasks open does-not-exist")

	if m.threadID != "" {
		t.Errorf("entered thread on failure: %q", m.threadID)
	}
	if !strings.Contains(transcript(m), "nothing matches") {
		t.Errorf("expected a not-found error:\n%s", transcript(m))
	}
}

// Opening a thread against an unreachable server surfaces an error rather than
// silently entering thread mode.
func TestTasksOpenServerErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, _ := client.New(srv.URL)
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.selectedID = "p1"

	m = runLine(t, m, "/tasks open anything")
	if m.threadID != "" {
		t.Errorf("entered thread despite server error")
	}
}
