package terminal

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-terminal/internal/client"
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

func TestPreAckTaskTerminalCannotSupersedeDelayedThreadReply(t *testing.T) {
	m, _ := threadModel(t)
	m = runLine(t, m, "/tasks open Refactor")
	m.input.SetValue("please add tests")
	submitted, replyCmd := m.submit()
	m = submitted.(Model)
	before := transcript(m)

	m.sseGeneration = 11
	m.sseEvents = make(chan client.Event)
	m.sseErrs = make(chan error)
	updated, _ := m.Update(sseEventMsg{generation: 11, event: client.Event{
		Name: "task_status_changed",
		Data: json.RawMessage(`{"type":"task_status_changed","project_id":"p1","task_id":"t-1","status":"completed"}`),
	}})
	m = updated.(Model)
	if m.threadStatus != "running" || transcript(m) != before || !m.chatSubmissionPending {
		t.Fatalf("uncorrelated pre-ack terminal event superseded reply: status=%q pending=%t transcript=%q", m.threadStatus, m.chatSubmissionPending, transcript(m))
	}

	updated, _ = m.Update(replyCmd())
	m = updated.(Model)
	if m.threadStatus != "running" || m.busy || m.hasPendingChat() {
		t.Fatalf("delayed reply did not settle after rejected terminal event: status=%q busy=%t pending=%t", m.threadStatus, m.busy, m.hasPendingChat())
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

func TestTaskAliasOpenMatchesCanonicalCommand(t *testing.T) {
	for _, command := range []string{"/tasks open Refactor", "/task open Refactor"} {
		t.Run(command, func(t *testing.T) {
			m, _ := threadModel(t)
			m = runLine(t, m, command)
			if m.threadID != "t-1" || !strings.Contains(stripANSI(transcript(m)), "agent: working on it") {
				t.Fatalf("task open alias did not render the assistant thread: id=%q transcript=%q", m.threadID, transcript(m))
			}
		})
	}
}

func TestChatMessageOverlapPreservesPendingTaskReplyOwnership(t *testing.T) {
	m := pendingChatStreamTestModel(t)
	m.threadID = "t-1"
	m.threadTitle = "Refactor the API"
	m.threadStatus = "running"
	m.pendingMsgTaskID = "t-1"
	m.pendingMsgThreadRequestID = m.threadOpenRequestID
	requestID := m.threadOpenRequestID

	updated, cmd := m.runCommand("/chat start another turn")
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("overlapping /chat message started a project chat request")
	}
	if m.threadOpenRequestID != requestID || m.threadID != "t-1" || !m.pendingChatScopeCurrent() {
		t.Fatalf("overlap invalidated active task reply: request=%d want=%d thread=%q current=%t", m.threadOpenRequestID, requestID, m.threadID, m.pendingChatScopeCurrent())
	}
	if !strings.Contains(transcript(m), chatStillProcessingMessage) {
		t.Fatalf("overlap warning missing: %q", transcript(m))
	}

	updated, _ = m.Update(chatStreamEventMsg{
		generation: 3, submissionID: 9, projectID: "project-A", execID: "exec-1",
		event: client.ChatOutputEvent{Data: "visible reply"},
	})
	m = updated.(Model)
	m.flushChatStreamOutput()
	if !strings.Contains(transcript(m), "visible reply") {
		t.Fatalf("active reply output was stranded after overlap: %q", transcript(m))
	}

	updated, _ = m.Update(chatStatusMsg{
		sessionGeneration: m.sessionGeneration, projectGeneration: m.projectGeneration,
		messageID: "exec-1", submissionID: 9, projectID: "project-A",
		status: &client.ChatStatus{MessageID: "exec-1", Status: "completed", Response: "visible reply"},
	})
	m = updated.(Model)
	if m.hasPendingChat() || m.busy {
		t.Fatalf("active task reply did not settle: pending=%t busy=%t", m.hasPendingChat(), m.busy)
	}
	if _, ok := m.beginChatSubmission("project-A"); !ok {
		t.Fatal("settled task reply still blocked future submissions")
	}
}

func TestTaskTerminalEventFlushesOutputBeforeSingleFailure(t *testing.T) {
	m := pendingChatStreamTestModel(t)
	m.threadID = "t-1"
	m.threadStatus = "running"
	m.pendingMsgTaskID = "t-1"
	m.pendingMsgThreadRequestID = m.threadOpenRequestID
	m.updateChatStreamOutput("buffered answer")
	m.chatStreamRenderQueued = true
	m.sseGeneration = 16
	m.sseEvents = make(chan client.Event)
	m.sseErrs = make(chan error)

	updated, cmd := m.Update(sseEventMsg{generation: 16, event: client.Event{
		Name: "task_status_changed",
		Data: json.RawMessage(`{"type":"task_status_changed","project_id":"project-A","task_id":"t-1","exec_id":"exec-1","status":"failed","message":"provider unavailable"}`),
	}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("matching terminal task event did not request authoritative status")
	}

	updated, _ = m.Update(chatStatusMsg{
		sessionGeneration: m.sessionGeneration, projectGeneration: m.projectGeneration,
		messageID: "exec-1", submissionID: 9, projectID: "project-A",
		status: &client.ChatStatus{MessageID: "exec-1", Status: "failed", Error: "provider unavailable"},
	})
	m = updated.(Model)
	if got := countRole(m.log, "error"); got != 1 {
		t.Fatalf("terminal failure diagnostics = %d, want 1; transcript=%q", got, transcript(m))
	}
	out := transcript(m)
	if outputAt, failureAt := strings.Index(out, "buffered answer"), strings.Index(out, "failed: provider unavailable"); outputAt < 0 || failureAt < 0 || outputAt > failureAt {
		t.Fatalf("buffered output was not ordered before terminal failure: %q", out)
	}
}

func TestTaskEventsRejectForeignActiveExecutionIdentity(t *testing.T) {
	for _, payload := range []string{
		`{"type":"task_status_changed","project_id":"project-A","task_id":"t-1","exec_id":"exec-old","status":"failed","message":"old execution"}`,
		`{"type":"task_status_changed","project_id":"project-A","task_id":"t-1","pending_input_id":"input-old","status":"completed","message":"old input"}`,
	} {
		m := pendingChatStreamTestModel(t)
		m.threadID = "t-1"
		m.threadStatus = "running"
		m.pendingMsgTaskID = "t-1"
		m.pendingMsgThreadRequestID = m.threadOpenRequestID
		m.sseGeneration = 17
		m.sseEvents = make(chan client.Event)
		m.sseErrs = make(chan error)
		before := transcript(m)

		updated, _ := m.Update(sseEventMsg{generation: 17, event: client.Event{
			Name: "task_status_changed", Data: json.RawMessage(payload),
		}})
		m = updated.(Model)
		if m.threadStatus != "running" || transcript(m) != before || !m.pendingChatScopeCurrent() {
			t.Fatalf("foreign execution event mutated current turn: status=%q current=%t transcript=%q", m.threadStatus, m.pendingChatScopeCurrent(), transcript(m))
		}
	}
}

func TestPostAckTaskEventsRequireExecutionIdentity(t *testing.T) {
	t.Run("terminal lifecycle event", func(t *testing.T) {
		m := pendingChatStreamTestModel(t)
		m.threadID = "t-1"
		m.threadStatus = "running"
		m.pendingMsgTaskID = "t-1"
		m.pendingMsgThreadRequestID = m.threadOpenRequestID
		m.showEvents = true
		m.sseGeneration = 19
		m.sseEvents = make(chan client.Event)
		m.sseErrs = make(chan error)
		before := transcript(m)

		updated, _ := m.Update(sseEventMsg{generation: 19, event: client.Event{
			Name: "task_status_changed",
			Data: json.RawMessage(`{"type":"task_status_changed","project_id":"project-A","task_id":"t-1","status":"failed","message":"uncorrelated failure"}`),
		}})
		m = updated.(Model)
		if m.threadStatus != "running" || transcript(m) != before || !m.pendingChatScopeCurrent() {
			t.Fatalf("identity-less post-ack terminal event mutated current turn: status=%q current=%t transcript=%q", m.threadStatus, m.pendingChatScopeCurrent(), transcript(m))
		}

		updated, cmd := m.Update(sseEventMsg{generation: 19, event: client.Event{
			Name: "task_status_changed",
			Data: json.RawMessage(`{"type":"task_status_changed","project_id":"project-A","task_id":"t-1","exec_id":"exec-1","status":"completed"}`),
		}})
		m = updated.(Model)
		if cmd == nil || m.threadStatus != "completed" {
			t.Fatalf("correlated lifecycle event was suppressed after identity-less event: cmd=%t status=%q", cmd != nil, m.threadStatus)
		}
	})

	t.Run("mirrored chat message", func(t *testing.T) {
		m := pendingChatStreamTestModel(t)
		m.threadID = "t-1"
		m.threadStatus = "running"
		m.pendingMsgTaskID = "t-1"
		m.pendingMsgThreadRequestID = m.threadOpenRequestID
		m.showEvents = true
		m.sseGeneration = 20
		m.sseEvents = make(chan client.Event)
		m.sseErrs = make(chan error)
		before := transcript(m)

		updated, _ := m.Update(sseEventMsg{generation: 20, event: client.Event{
			Name: "chat_new_message",
			Data: json.RawMessage(`{"type":"chat_new_message","project_id":"project-A","task_id":"t-1","message":"uncorrelated duplicate"}`),
		}})
		m = updated.(Model)
		if transcript(m) != before {
			t.Fatalf("identity-less mirrored message reached transcript or /events display: %q", transcript(m))
		}

		updated, _ = m.Update(chatStreamEventMsg{
			generation: 3, submissionID: 9, projectID: "project-A", execID: "exec-1",
			event: client.ChatOutputEvent{Data: "owned output"},
		})
		m = updated.(Model)
		m.flushChatStreamOutput()
		if !strings.Contains(transcript(m), "owned output") || strings.Contains(transcript(m), "uncorrelated duplicate") {
			t.Fatalf("identity-less mirror suppressed or duplicated owned stream: %q", transcript(m))
		}
	})
}

func TestTaskEventPromotesExecutionOnlyFromMatchingPendingInput(t *testing.T) {
	m := pendingChatStreamTestModel(t)
	m.pendingMsgID = "input-1"
	m.pendingMsgExecutionID = ""
	m.chatStreamExecID = ""
	m.threadID = "t-1"
	m.threadStatus = "running"
	m.pendingMsgTaskID = "t-1"
	m.pendingMsgThreadRequestID = m.threadOpenRequestID
	m.sseGeneration = 18
	m.sseEvents = make(chan client.Event)
	m.sseErrs = make(chan error)

	updated, cmd := m.Update(sseEventMsg{generation: 18, event: client.Event{
		Name: "task_status_changed",
		Data: json.RawMessage(`{"type":"task_status_changed","project_id":"project-A","task_id":"t-1","pending_input_id":"input-1","exec_id":"exec-promoted","status":"running"}`),
	}})
	m = updated.(Model)
	if cmd == nil || m.pendingMsgExecutionID != "exec-promoted" || m.threadStatus != "running" {
		t.Fatalf("matching queued event did not promote current turn: cmd=%t execution=%q status=%q", cmd != nil, m.pendingMsgExecutionID, m.threadStatus)
	}
}

func TestTaskOpenFailurePreservesActiveReply(t *testing.T) {
	const board = `<div>
		<div data-task-id="t-1" data-task-status="running" data-task-category="active"><a href="/tasks/t-1" title="Refactor the API">Refactor</a></div>
		<div data-task-id="t-2" data-task-status="running" data-task-category="active"><a href="/tasks/t-2" title="Duplicate target">Duplicate target</a></div>
		<div data-task-id="t-3" data-task-status="running" data-task-category="active"><a href="/tasks/t-3" title="Duplicate target later">Duplicate target later</a></div>
	</div>`
	for _, tt := range []struct {
		name string
		ref  string
	}{
		{name: "unknown", ref: "does-not-exist"},
		{name: "ambiguous", ref: "Duplicate"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := dispatchModel(t, map[string]string{
				"/tasks":            board,
				"/tasks/t-1/thread": `<div>existing answer</div>`,
			})
			m = runLine(t, m, "/tasks open Refactor")
			installPendingTaskReply(&m, "exec-current")
			m.updateChatStreamOutput("still streaming")
			beforeRefreshID := m.threadRefreshRequestID

			next, cmd := m.runCommand("/tasks open " + tt.ref)
			m = next.(Model)
			updated, _ := m.Update(cmd())
			m = updated.(Model)

			assertPendingTaskReply(t, m, "exec-current")
			if m.threadID != "t-1" || m.threadRefreshRequestID != beforeRefreshID {
				t.Fatalf("failed open replaced active thread state: thread=%q refresh=%d want=%d", m.threadID, m.threadRefreshRequestID, beforeRefreshID)
			}
			if !strings.Contains(transcript(m), "still streaming") {
				t.Fatalf("failed open hid accepted output: %q", transcript(m))
			}
		})
	}
}

func TestTaskOpenMissingReferenceAndPickerCancellationPreserveActiveReply(t *testing.T) {
	m, _ := dispatchModel(t, map[string]string{
		"/tasks": `<div>
			<div data-task-id="t-1" data-task-status="running" data-task-category="active"><a href="/tasks/t-1" title="Refactor">Refactor</a></div>
			<div data-task-id="t-2" data-task-status="running" data-task-category="active"><a href="/tasks/t-2" title="Other">Other</a></div>
		</div>`,
		"/tasks/t-1/thread": `<div>existing answer</div>`,
	})
	m = runLine(t, m, "/tasks open Refactor")
	installPendingTaskReply(&m, "exec-current")

	next, cmd := m.runCommand("/tasks open")
	m = next.(Model)
	assertPendingTaskReply(t, m, "exec-current")
	updated, _ := m.Update(cmd())
	m = updated.(Model)
	if !m.selectorActive {
		t.Fatal("missing task reference did not open picker")
	}
	assertPendingTaskReply(t, m, "exec-current")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.selectorActive || m.threadID != "t-1" {
		t.Fatalf("picker cancellation changed active thread: selector=%t thread=%q", m.selectorActive, m.threadID)
	}
	assertPendingTaskReply(t, m, "exec-current")
}

func TestFailedTaskOpenDoesNotRejectInFlightReplyAcknowledgement(t *testing.T) {
	m, _ := dispatchModel(t, map[string]string{
		"/tasks":                 `<div data-task-id="t-1" data-task-status="running" data-task-category="active"><a href="/tasks/t-1" title="Refactor">Refactor</a></div>`,
		"/tasks/t-1/thread":      `<div>existing answer</div>`,
		"POST /tasks/t-1/thread": `<div data-execution-pair="true" data-exec-id="exec-current" data-exec-status="running"></div>`,
	})
	m = runLine(t, m, "/tasks open Refactor")
	m.input.SetValue("continue")
	submitted, send := m.submit()
	m = submitted.(Model)
	originalOpenRequest := m.pendingMsgThreadRequestID

	next, openCmd := m.runCommand("/tasks open missing")
	m = next.(Model)
	updated, _ := m.Update(openCmd())
	m = updated.(Model)
	if m.threadOpenRequestID == originalOpenRequest {
		t.Fatal("failed open did not advance ordered open request ID")
	}

	updated, cmd := m.Update(send())
	m = updated.(Model)
	if cmd == nil || m.pendingMsgID != "exec-current" || m.pendingMsgTaskID != "t-1" || !m.pendingChatScopeCurrent() {
		t.Fatalf("failed open rejected in-flight acknowledgement: cmd=%t id=%q task=%q current=%t", cmd != nil, m.pendingMsgID, m.pendingMsgTaskID, m.pendingChatScopeCurrent())
	}
}

func TestSuccessfulTaskOpenCommitsReplacementAndCancelsPriorReply(t *testing.T) {
	m, _ := dispatchModel(t, map[string]string{
		"/tasks": `<div>
			<div data-task-id="t-1" data-task-status="running" data-task-category="active"><a href="/tasks/t-1" title="Refactor">Refactor</a></div>
			<div data-task-id="t-2" data-task-status="running" data-task-category="active"><a href="/tasks/t-2" title="Other">Other</a></div>
		</div>`,
		"/tasks/t-1/thread": `<div>existing answer</div>`,
		"/tasks/t-2/thread": `<div>replacement answer</div>`,
	})
	m = runLine(t, m, "/tasks open Refactor")
	installPendingTaskReply(&m, "exec-current")
	cancelled := false
	m.chatStreamCancel = func() { cancelled = true }
	m.updateChatStreamOutput("accepted before replacement")

	next, cmd := m.runCommand("/tasks open Other")
	m = next.(Model)
	assertPendingTaskReply(t, m, "exec-current")
	updated, _ := m.Update(cmd())
	m = updated.(Model)

	if m.threadID != "t-2" || m.hasPendingChat() || m.busy || !cancelled {
		t.Fatalf("successful replacement did not commit: thread=%q pending=%t busy=%t cancelled=%t", m.threadID, m.hasPendingChat(), m.busy, cancelled)
	}
	out := transcript(m)
	if !strings.Contains(out, "accepted before replacement") || !strings.Contains(out, "replacement answer") {
		t.Fatalf("successful replacement lost accepted output or new thread: %q", out)
	}
}

func TestTaskOpenRequestErrorsPreserveActiveReply(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		phase  string
	}{
		{name: "authentication during resolution", status: http.StatusUnauthorized, phase: "tasks"},
		{name: "backend during resolution", status: http.StatusInternalServerError, phase: "tasks"},
		{name: "authentication during thread load", status: http.StatusUnauthorized, phase: "thread"},
		{name: "backend during thread load", status: http.StatusInternalServerError, phase: "thread"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var fail bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if fail && ((tt.phase == "tasks" && r.URL.Path == "/tasks") || (tt.phase == "thread" && r.URL.Path == "/tasks/t-2/thread")) {
					w.WriteHeader(tt.status)
					return
				}
				switch r.URL.Path {
				case "/tasks":
					_, _ = w.Write([]byte(`<div>
						<div data-task-id="t-1" data-task-status="running" data-task-category="active"><a href="/tasks/t-1" title="Refactor">Refactor</a></div>
						<div data-task-id="t-2" data-task-status="running" data-task-category="active"><a href="/tasks/t-2" title="Other">Other</a></div>
					</div>`))
				case "/tasks/t-1/thread":
					_, _ = w.Write([]byte(`<div>existing answer</div>`))
				case "/tasks/t-2/thread":
					_, _ = w.Write([]byte(`<div>replacement answer</div>`))
				}
			}))
			defer srv.Close()
			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			m := New(c)
			m.selectedID = "p1"
			m = runLine(t, m, "/tasks open Refactor")
			installPendingTaskReply(&m, "exec-current")
			fail = true

			next, cmd := m.runCommand("/tasks open Other")
			m = next.(Model)
			updated, _ := m.Update(cmd())
			m = updated.(Model)
			assertPendingTaskReply(t, m, "exec-current")
			if m.threadID != "t-1" {
				t.Fatalf("request failure replaced active thread with %q", m.threadID)
			}
		})
	}

	t.Run("transport", func(t *testing.T) {
		m, _ := threadModel(t)
		m = runLine(t, m, "/tasks open Refactor")
		installPendingTaskReply(&m, "exec-current")
		m.client, _ = client.New("http://127.0.0.1:1")
		next, cmd := m.runCommand("/tasks open Other")
		m = next.(Model)
		updated, _ := m.Update(cmd())
		m = updated.(Model)
		assertPendingTaskReply(t, m, "exec-current")
		if m.threadID != "t-1" {
			t.Fatalf("transport failure replaced active thread with %q", m.threadID)
		}
	})
}

func installPendingTaskReply(m *Model, execID string) {
	m.pendingMsgID = execID
	m.pendingMsgExecutionID = execID
	m.pendingMsgProjectID = m.selectedID
	m.pendingMsgProjectGeneration = m.projectGeneration
	m.pendingMsgTaskID = m.threadID
	m.pendingMsgThreadRequestID = m.threadOpenRequestID
	m.chatSubmissionPending = true
	m.chatSubmissionID++
	m.busy = true
	m.chatStreamExecID = execID
}

func assertPendingTaskReply(t *testing.T, m Model, execID string) {
	t.Helper()
	if !m.hasPendingChat() || m.pendingMsgID != execID || m.pendingMsgTaskID != m.threadID || !m.pendingChatScopeCurrent() {
		t.Fatalf("active task reply was invalidated: pending=%t id=%q task=%q thread=%q current=%t", m.hasPendingChat(), m.pendingMsgID, m.pendingMsgTaskID, m.threadID, m.pendingChatScopeCurrent())
	}
}

func TestTaskEventsRejectIdentityBeforeReplyAcknowledgement(t *testing.T) {
	m, _ := threadModel(t)
	m = runLine(t, m, "/tasks open Refactor")
	m.pendingMsgProjectID = "p1"
	m.pendingMsgProjectGeneration = m.projectGeneration
	m.pendingMsgTaskID = "t-1"
	m.pendingMsgThreadRequestID = m.threadOpenRequestID
	m.chatSubmissionPending = true
	m.chatSubmissionID = 12
	m.busy = true
	m.sseGeneration = 18
	m.sseEvents = make(chan client.Event)
	m.sseErrs = make(chan error)
	before := transcript(m)

	for _, payload := range []string{
		`{"type":"task_status_changed","project_id":"p1","task_id":"t-1","exec_id":"exec-old","status":"failed","message":"old execution"}`,
		`{"type":"task_status_changed","project_id":"p1","task_id":"t-1","status":"failed","message":"unowned execution"}`,
	} {
		updated, _ := m.Update(sseEventMsg{generation: 18, event: client.Event{
			Name: "task_status_changed",
			Data: json.RawMessage(payload),
		}})
		m = updated.(Model)
	}
	if m.threadStatus != "running" || transcript(m) != before || m.pendingMsgID != "" || !m.chatSubmissionPending {
		t.Fatalf("pre-ack execution event mutated active send: status=%q pending=%q submitting=%t transcript=%q", m.threadStatus, m.pendingMsgID, m.chatSubmissionPending, transcript(m))
	}
}

func TestTaskEventsRejectConflictingExecutionAfterPromotion(t *testing.T) {
	m, _ := threadModel(t)
	m = runLine(t, m, "/tasks open Refactor")
	installPendingTaskReply(&m, "input-current")
	m.pendingMsgExecutionID = "exec-current"
	m.sseGeneration = 19
	m.sseEvents = make(chan client.Event)
	m.sseErrs = make(chan error)
	before := transcript(m)

	updated, _ := m.Update(sseEventMsg{generation: 19, event: client.Event{
		Name: "task_status_changed",
		Data: json.RawMessage(`{"type":"task_status_changed","project_id":"p1","task_id":"t-1","pending_input_id":"input-current","exec_id":"exec-conflict","status":"failed","message":"wrong promotion"}`),
	}})
	m = updated.(Model)
	if m.threadStatus != "running" || m.pendingMsgExecutionID != "exec-current" || transcript(m) != before {
		t.Fatalf("conflicting promoted execution mutated current turn: status=%q execution=%q transcript=%q", m.threadStatus, m.pendingMsgExecutionID, transcript(m))
	}
}

func TestTaskEventPromotionRejectsConflictingSecondExecution(t *testing.T) {
	m, _ := threadModel(t)
	m = runLine(t, m, "/tasks open Refactor")
	installPendingTaskReply(&m, "input-current")
	m.pendingMsgExecutionID = ""
	m.sseGeneration = 20
	m.sseEvents = make(chan client.Event)
	m.sseErrs = make(chan error)

	first, _ := m.Update(sseEventMsg{generation: 20, event: client.Event{
		Name: "task_status_changed",
		Data: json.RawMessage(`{"type":"task_status_changed","project_id":"p1","task_id":"t-1","pending_input_id":"input-current","exec_id":"exec-current","status":"running"}`),
	}})
	m = first.(Model)
	before := transcript(m)
	second, _ := m.Update(sseEventMsg{generation: 20, event: client.Event{
		Name: "task_status_changed",
		Data: json.RawMessage(`{"type":"task_status_changed","project_id":"p1","task_id":"t-1","pending_input_id":"input-current","exec_id":"exec-conflict","status":"failed"}`),
	}})
	m = second.(Model)
	if m.pendingMsgExecutionID != "exec-current" || m.threadStatus != "running" || transcript(m) != before {
		t.Fatalf("second execution replaced verified promotion: execution=%q status=%q transcript=%q", m.pendingMsgExecutionID, m.threadStatus, transcript(m))
	}
}

func TestInteractiveTaskReplyIdentitylessFallbackFailureIsExplicit(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
	}{
		{name: "authentication", status: http.StatusUnauthorized},
		{name: "backend", status: http.StatusInternalServerError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			threadGets := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/tasks":
					_, _ = w.Write([]byte(`<div data-task-id="t-1" data-task-status="running" data-task-category="active"><a href="/tasks/t-1" title="Refactor">Refactor</a></div>`))
				case r.Method == http.MethodPost && r.URL.Path == "/tasks/t-1/thread":
					_, _ = w.Write([]byte(`<div data-task-id="t-1" data-input-mode="swarm"></div>`))
				case r.URL.Path == "/tasks/t-1/thread":
					threadGets++
					if threadGets == 1 {
						_, _ = w.Write([]byte(`<div>existing answer</div>`))
						return
					}
					w.WriteHeader(tt.status)
				}
			}))
			defer srv.Close()
			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			m := New(c)
			m.selectedID = "p1"
			m = runLine(t, m, "/tasks open Refactor")
			m.input.SetValue("coordinate workers")
			submitted, send := m.submit()
			m = submitted.(Model)
			updated, _ := m.Update(send())
			m = updated.(Model)

			if m.busy || m.hasPendingChat() || strings.Contains(transcript(m), "Thread · Refactor::sent") {
				t.Fatalf("fallback failure reported success or remained pending: busy=%t pending=%t transcript=%q", m.busy, m.hasPendingChat(), transcript(m))
			}
			if tt.status == http.StatusUnauthorized && !m.authRequired {
				t.Fatalf("fallback auth failure did not enter auth recovery: %q", transcript(m))
			}
			if tt.status == http.StatusInternalServerError && !strings.Contains(transcript(m), "500") {
				t.Fatalf("fallback backend failure was hidden: %q", transcript(m))
			}
		})
	}
}

func TestInteractiveTaskReplyIdentitylessFallbackTransportFailureIsExplicit(t *testing.T) {
	threadGets := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/tasks":
			_, _ = w.Write([]byte(`<div data-task-id="t-1" data-task-status="running" data-task-category="active"><a href="/tasks/t-1" title="Refactor">Refactor</a></div>`))
		case r.Method == http.MethodPost && r.URL.Path == "/tasks/t-1/thread":
			_, _ = w.Write([]byte(`<div data-task-id="t-1" data-input-mode="swarm"></div>`))
		case r.URL.Path == "/tasks/t-1/thread":
			threadGets++
			if threadGets == 1 {
				_, _ = w.Write([]byte(`<div>existing answer</div>`))
				return
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}
	}))
	defer srv.Close()
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	m.selectedID = "p1"
	m.connected = true
	m = runLine(t, m, "/tasks open Refactor")
	m.input.SetValue("coordinate workers")
	submitted, send := m.submit()
	m = submitted.(Model)
	updated, _ := m.Update(send())
	m = updated.(Model)

	if m.busy || m.hasPendingChat() || m.connected || m.connErr == "" || strings.Contains(transcript(m), "Thread · Refactor::sent") {
		t.Fatalf("fallback transport failure did not use offline diagnostics: busy=%t pending=%t connected=%t connErr=%q transcript=%q", m.busy, m.hasPendingChat(), m.connected, m.connErr, transcript(m))
	}
}

func TestInteractiveTaskReplyInstallsExecutionStreaming(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{
		"/tasks":                 `<div data-task-id="t-1" data-task-status="running" data-task-category="active"><a href="/tasks/t-1" title="Refactor the API">Refactor</a></div>`,
		"/tasks/t-1/thread":      `<div>agent: existing answer</div>`,
		"POST /tasks/t-1/thread": `<div data-execution-pair="true" data-exec-id="exec-followup" data-exec-status="running"></div>`,
	})
	m = runLine(t, m, "/tasks open Refactor")
	m.input.SetValue("continue")
	submitted, send := m.submit()
	m = submitted.(Model)
	ack, ok := send().(chatSentMsg)
	if !ok {
		t.Fatalf("task reply acknowledgement = %T", send())
	}
	if ack.taskID != "t-1" || ack.accepted == nil || ack.accepted.MessageID != "exec-followup" || ack.accepted.Queued {
		t.Fatalf("task reply acknowledgement = %#v", ack)
	}
	if !rec.sawQuery("POST /tasks/t-1/thread?project_id=p1") {
		t.Fatalf("task reply was not project scoped: %v", rec.urlsSnapshot())
	}

	updated, cmd := m.Update(ack)
	m = updated.(Model)
	if cmd == nil || m.pendingMsgTaskID != "t-1" || m.pendingMsgID != "exec-followup" || m.pendingMsgExecutionID != "exec-followup" {
		t.Fatalf("task execution not installed: cmd=%v task=%q pending=%q execution=%q", cmd != nil, m.pendingMsgTaskID, m.pendingMsgID, m.pendingMsgExecutionID)
	}
}

func TestInteractiveTaskReplyIdentitylessFallbackRefreshesAndSettles(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{
		"/tasks":                 `<div data-task-id="t-1" data-task-status="running" data-task-category="active"><a href="/tasks/t-1" title="Refactor the API">Refactor</a></div>`,
		"/tasks/t-1/thread":      `<div>agent: orchestration accepted</div>`,
		"POST /tasks/t-1/thread": `<div data-task-id="t-1" data-input-mode="swarm"></div>`,
	})
	m = runLine(t, m, "/tasks open Refactor")
	m.input.SetValue("coordinate workers")
	submitted, send := m.submit()
	m = submitted.(Model)
	updated, cmd := m.Update(send())
	m = updated.(Model)
	if cmd != nil || m.busy || m.hasPendingChat() || m.pendingMsgTaskID != "" {
		t.Fatalf("identity-less fallback did not settle: cmd=%v busy=%t pending=%t task=%q", cmd != nil, m.busy, m.hasPendingChat(), m.pendingMsgTaskID)
	}
	if got := rec.count("GET", "/tasks/t-1/thread"); got != 2 {
		t.Fatalf("thread refresh count = %d, want open plus fallback refresh", got)
	}
	if !strings.Contains(transcript(m), "orchestration accepted") {
		t.Fatalf("fallback thread output hidden: %q", transcript(m))
	}
}

func TestInteractiveTaskReplySuppressesMirroredLiveMessageAndThreadRefresh(t *testing.T) {
	m, rec := threadModel(t)
	m = runLine(t, m, "/tasks open Refactor")
	m.pendingMsgID = "exec-1"
	m.pendingMsgExecutionID = "exec-1"
	m.pendingMsgProjectID = "p1"
	m.pendingMsgProjectGeneration = m.projectGeneration
	m.pendingMsgTaskID = "t-1"
	m.pendingMsgThreadRequestID = m.threadOpenRequestID
	m.chatSubmissionPending = true
	m.chatSubmissionID = 4
	m.busy = true
	m.sseGeneration = 15
	m.sseEvents = make(chan client.Event)
	m.sseErrs = make(chan error)
	before := transcript(m)

	updated, _ := m.Update(sseEventMsg{generation: 15, event: client.Event{
		Name: "chat_new_message",
		Data: json.RawMessage(`{"type":"chat_new_message","project_id":"p1","task_id":"t-1","exec_id":"exec-1","message":"duplicated final"}`),
	}})
	m = updated.(Model)
	if transcript(m) != before {
		t.Fatalf("mirrored live message duplicated streamed output: %q", transcript(m))
	}

	updated, cmd := m.Update(sseEventMsg{generation: 15, event: client.Event{
		Name: "task_status_changed",
		Data: json.RawMessage(`{"type":"task_status_changed","project_id":"p1","task_id":"t-1","exec_id":"exec-1","status":"completed"}`),
	}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("terminal task event did not schedule authoritative status fetch")
	}
	if got := rec.count("GET", "/tasks/t-1/thread"); got != 1 {
		t.Fatalf("streamed reply triggered duplicate thread refresh: count=%d", got)
	}
}

func TestInteractiveTaskReplyTerminalErrorFlushesPartialOutput(t *testing.T) {
	m := pendingChatStreamTestModel(t)
	m.threadID = "t-1"
	m.pendingMsgTaskID = "t-1"
	m.updateChatStreamOutput("partial answer")
	m.chatStreamRenderQueued = true
	updated, _ := m.Update(chatStatusMsg{
		sessionGeneration: m.sessionGeneration, projectGeneration: m.projectGeneration,
		messageID: "exec-1", submissionID: 9, projectID: "project-A",
		status: &client.ChatStatus{MessageID: "exec-1", Status: "failed", Error: "provider unavailable"},
	})
	m = updated.(Model)
	out := transcript(m)
	if m.busy || m.hasPendingChat() || !strings.Contains(out, "partial answer") || !strings.Contains(out, "failed: provider unavailable") {
		t.Fatalf("terminal task failure was hidden or left pending: busy=%t pending=%t transcript=%q", m.busy, m.hasPendingChat(), out)
	}
}

func TestInteractiveTaskReplyStreamsOneUTF8AssistantEntry(t *testing.T) {
	m := pendingChatStreamTestModel(t)
	m.threadID = "t-1"
	m.threadTitle = "Refactor the API"
	m.pendingMsgTaskID = "t-1"

	for _, chunk := range []string{"first λ", "🙂 second"} {
		updated, _ := m.Update(chatStreamEventMsg{generation: 3, submissionID: 9, projectID: "project-A", execID: "exec-1", event: client.ChatOutputEvent{Data: chunk}})
		m = updated.(Model)
	}
	updated, _ := m.Update(chatStreamRenderMsg{generation: 3, renderGeneration: m.chatStreamRenderGeneration, submissionID: 9, projectID: "project-A", execID: "exec-1"})
	m = updated.(Model)

	if m.chatStreamOffset != len([]byte("first λ🙂 second")) {
		t.Fatalf("task reply offset = %d", m.chatStreamOffset)
	}
	if got := countRole(m.log, "agent"); got != 1 {
		t.Fatalf("assistant entries = %d, want 1; transcript=%q", got, transcript(m))
	}
	if !strings.Contains(transcript(m), "first λ🙂 second") {
		t.Fatalf("incremental task reply hidden: %q", transcript(m))
	}
}

func TestInteractiveTaskReplyCompletionFlushesAndReconciles(t *testing.T) {
	m := pendingChatStreamTestModel(t)
	m.threadID = "t-1"
	m.pendingMsgTaskID = "t-1"
	m.updateChatStreamOutput("buffered λ")
	m.chatStreamRenderQueued = true

	updated, _ := m.Update(chatStatusMsg{
		sessionGeneration: m.sessionGeneration, projectGeneration: m.projectGeneration,
		messageID: "exec-1", submissionID: 9, projectID: "project-A",
		status: &client.ChatStatus{MessageID: "exec-1", Status: "completed", Response: "buffered λ final"},
	})
	m = updated.(Model)
	if m.busy || m.pendingMsgTaskID != "" || m.chatStreamRenderQueued {
		t.Fatalf("task completion did not settle: busy=%t task=%q queued=%t", m.busy, m.pendingMsgTaskID, m.chatStreamRenderQueued)
	}
	if got := countRole(m.log, "agent"); got != 1 || !strings.Contains(transcript(m), "buffered λ final") {
		t.Fatalf("task completion did not reconcile one assistant entry: count=%d transcript=%q", got, transcript(m))
	}
}

func TestInteractiveTaskReplyReconnectKeepsUTF8OffsetAndRejectsStaleTask(t *testing.T) {
	m := pendingChatStreamTestModel(t)
	m.threadID = "t-1"
	m.pendingMsgTaskID = "t-1"
	m.updateChatStreamOutput("λ🙂")
	m.chatStreamRenderQueued = true

	updated, cmd := m.Update(chatStreamDisconnectedMsg{generation: 3, submissionID: 9, projectID: "project-A", execID: "exec-1"})
	m = updated.(Model)
	if cmd == nil || m.chatStreamOffset != len([]byte("λ🙂")) || !strings.Contains(transcript(m), "λ🙂") {
		t.Fatalf("task reconnect lost buffered UTF-8 output: offset=%d transcript=%q", m.chatStreamOffset, transcript(m))
	}
	reconnect := m.chatStreamReconnectMessage(m.chatStreamGeneration)
	if reconnect.generation != m.chatStreamGeneration || reconnect.submissionID != 9 || reconnect.projectID != "project-A" || reconnect.execID != "exec-1" || reconnect.offset != len([]byte("λ🙂")) {
		t.Fatalf("task reconnect = %#v, want generation=%d submission=9 project=project-A exec=exec-1 UTF-8 byte offset %d", reconnect, m.chatStreamGeneration, len([]byte("λ🙂")))
	}

	m.threadID = "t-2"
	before := transcript(m)
	updated, next := m.Update(reconnect)
	m = updated.(Model)
	if next != nil || transcript(m) != before {
		t.Fatal("stale task reconnect survived active-thread replacement")
	}
}

func countRole(entries []entry, role string) int {
	count := 0
	for _, item := range entries {
		if item.role == role {
			count++
		}
	}
	return count
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
