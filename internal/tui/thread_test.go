package tui

import (
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
		"/tasks":             taskBoardHTML,
		"/tasks/t-1":         taskDetailHTML,
		"/tasks/t-1/thread":  `<div>agent: working on it</div>`,
		"/tasks/t-1/changes": `<div>2 files changed</div>`,
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

	if m.threadID != "" {
		t.Errorf("thread %q survived a project switch", m.threadID)
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
