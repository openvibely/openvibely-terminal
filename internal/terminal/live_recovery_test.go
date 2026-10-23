package terminal

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/openvibely/openvibely-terminal/internal/client"
)

func TestLiveAuthenticationRecoveryRestoresSubscription(t *testing.T) {
	m := newTestModel(t)
	defer func() { m.Cleanup() }()
	m.selectedID = "p1"
	cancelled := false
	m.sseCancel = func() { cancelled = true }
	m.markAuthRequiredWithMessage(false)
	// Repeated unauthorized responses must not erase the remembered intent.
	m.markAuthRequiredWithMessage(false)
	if !cancelled || m.sseCancel != nil || !m.sseRetryAfterProject {
		t.Fatal("authentication loss did not preserve subscription intent")
	}
	next, cmd := m.Update(connCheckedMsg{generation: m.connectionGeneration, auth: &client.AuthStatus{Authenticated: true}})
	m = next.(Model)
	if cmd == nil || m.authRequired || m.sseRetryAfterProject || m.sseCancel == nil || m.sseEvents == nil {
		t.Fatalf("recovery: auth=%v retry=%v cmd=%v", m.authRequired, m.sseRetryAfterProject, cmd)
	}
}

func TestLiveAuthenticationRecoveryDoesNotCreateUnrequestedSubscription(t *testing.T) {
	m := newTestModel(t)
	defer func() { m.Cleanup() }()
	m.selectedID = "p1"
	m.markAuthRequiredWithMessage(false)
	next, cmd := m.Update(connCheckedMsg{generation: m.connectionGeneration, auth: &client.AuthStatus{Authenticated: true}})
	m = next.(Model)
	if cmd != nil || m.sseRetryAfterProject {
		t.Fatal("recovery created an unrequested subscription")
	}
}

func TestLiveReconciliationUsesConversationParser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tasks/t1":
			if r.URL.Query().Get("project_id") != "p1" {
				t.Error("missing metadata scope")
			}
			fmt.Fprint(w, `<div id="task-detail-view" data-task-id="t1" data-project-id="p1" data-task-status="completed"></div>`)
		case "/tasks/t1/thread":
			if r.URL.Query().Get("project_id") != "p1" {
				t.Error("missing thread scope")
			}
			fmt.Fprint(w, `<div data-task-thread><div>user: investigate</div><div>agent: fixed</div><form><label>Model</label><button>Send</button></form><button>Retry</button></div>`)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	m.selectedID = "p1"
	m.threadID = "t1"
	cmd := m.reconcileLiveState()
	if cmd == nil {
		t.Fatal("no reconciliation command")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatal("expected refresh batch")
	}
	var found bool
	for _, command := range batch {
		msg := command()
		if thread, ok := msg.(threadUpdatedMsg); ok {
			found = true
			if thread.err != nil {
				t.Fatal(thread.err)
			}
			if thread.status != "completed" || !strings.Contains(thread.body, "agent: fixed") {
				t.Fatalf("thread = %+v", thread)
			}
			for _, control := range []string{"Model", "Send", "Retry"} {
				if strings.Contains(thread.body, control) {
					t.Errorf("control %q in %q", control, thread.body)
				}
			}
			// A later thread refresh must invalidate this response.
			m.threadRefreshRequestID++
			before := transcript(m)
			next, staleCmd := m.Update(thread)
			updated := next.(Model)
			if staleCmd != nil || updated.threadStatus != m.threadStatus || transcript(updated) != before {
				t.Fatal("stale refresh changed the current thread")
			}
		}
	}
	if !found {
		t.Fatal("thread refresh missing")
	}
}
