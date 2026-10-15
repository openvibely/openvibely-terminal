package terminal

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/openvibely/openvibely-terminal/internal/client"
)

func pollTestUpdate(m *Model, msg tea.Msg) tea.Cmd {
	next, cmd := m.Update(msg)
	*m = next.(Model)
	return cmd
}

func pollTestConnect(t *testing.T, m *Model) {
	t.Helper()
	cmd := m.connectChatStream(m.pendingMsgExecutionID, m.chatStreamOffset)
	batch := cmd().(tea.BatchMsg)
	result := make(chan tea.Msg, 1)
	go func() { result <- batch[1]() }()
	select {
	case msg := <-result:
		pollTestUpdate(m, msg)
	case <-time.After(3 * time.Second):
		m.invalidateChatStream()
		t.Fatal("stream did not connect")
	}
}

func TestHealthyChatStreamSuppressesPeriodicStatusRequests(t *testing.T) {
	for _, terminal := range []string{"done", "error"} {
		t.Run(terminal, func(t *testing.T) {
			var reads atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/events/chat/") {
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				reads.Add(1)
				_ = json.NewEncoder(w).Encode(client.ChatStatus{MessageID: "exec-1", Status: "completed", Response: "live reply", TaskIDs: []string{"task-1"}})
			}))
			defer srv.Close()
			m := pendingChatStreamTestModel(t)
			m.client, _ = client.New(srv.URL)
			defer func() { m.invalidateChatStream() }()
			m.armChatPoll()
			pollTestConnect(t, &m)
			// Advance 400 nominal polling intervals (ten minutes) without sleeping.
			// Activity keeps the watchdog alive, while old poll ticks must park.
			for i := 0; i < 400; i++ {
				pollTestUpdate(&m, chatStreamEventMsg{generation: m.chatStreamGeneration, submissionID: m.chatSubmissionID, projectID: m.selectedID, execID: m.pendingMsgID, event: client.ChatOutputEvent{Name: "heartbeat"}})
				if cmd := pollTestUpdate(&m, chatPollTickMsg{pollGeneration: m.chatPollGeneration}); cmd != nil {
					cmd()
				}
				if cmd := m.armChatPoll(); cmd != nil {
					t.Fatal("healthy stream rearmed polling")
				}
			}
			if reads.Load() != 0 {
				t.Fatalf("healthy stream made %d status reads", reads.Load())
			}
			pollTestUpdate(&m, chatStreamEventMsg{generation: m.chatStreamGeneration, submissionID: m.chatSubmissionID, projectID: m.selectedID, execID: m.pendingMsgID, event: client.ChatOutputEvent{Data: "live reply"}})
			cmd := pollTestUpdate(&m, chatStreamEventMsg{generation: m.chatStreamGeneration, submissionID: m.chatSubmissionID, projectID: m.selectedID, execID: m.pendingMsgID, event: client.ChatOutputEvent{Name: terminal}})
			pollTestUpdate(&m, cmd())
			if reads.Load() != 1 || m.busy || !strings.Contains(transcript(m), "agent::live reply") || !strings.Contains(transcript(m), "created tasks: task-1") {
				t.Fatalf("completion: reads=%d busy=%v transcript=%s", reads.Load(), m.busy, transcript(m))
			}
		})
	}
}

func TestSilentChatStreamWatchdogFetchesTerminalStatus(t *testing.T) {
	for _, status := range []string{"completed", "failed", "cancelled"} {
		for _, snapshot := range []string{"", "partial reply"} {
			t.Run(status+"/"+snapshot, func(t *testing.T) {
				var reads atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.Contains(r.URL.Path, "/events/chat/") {
						w.Header().Set("Content-Type", "text/event-stream")
						w.(http.Flusher).Flush()
						<-r.Context().Done()
						return
					}
					reads.Add(1)
					_ = json.NewEncoder(w).Encode(client.ChatStatus{MessageID: "exec-1", Status: status, Response: snapshot})
				}))
				defer srv.Close()
				m := pendingChatStreamTestModel(t)
				m.client, _ = client.New(srv.URL)
				defer func() { m.invalidateChatStream() }()
				pollTestConnect(t, &m)
				m.updateChatStreamOutput("partial reply")
				cmd := pollTestUpdate(&m, chatStreamWatchdogMsg{id: m.chatStreamWatchdogID, activity: m.chatStreamActivity})
				if cmd == nil {
					t.Fatal("silent stream did not fetch status")
				}
				pollTestUpdate(&m, cmd())
				if reads.Load() != 1 || m.busy || m.pendingMsgID != "" || !strings.Contains(transcript(m), "agent::partial reply") {
					t.Fatalf("fallback lost completion/output: %s", transcript(m))
				}
			})
		}
	}
}

func TestChatFallbackPollChainAndStaleMessages(t *testing.T) {
	m := pendingChatStreamTestModel(t)
	if m.armChatPoll() == nil || m.armChatPoll() != nil {
		t.Fatal("poll chain not deduplicated")
	}
	old := m.chatPollGeneration
	m.resetChatPoll()
	m.armChatPoll()
	if cmd := pollTestUpdate(&m, chatPollTickMsg{pollGeneration: old}); cmd != nil {
		t.Fatal("stale tick fetched status")
	}
	pollTestUpdate(&m, chatStatusMsg{pollGeneration: old, status: &client.ChatStatus{Status: "completed", Response: "stale"}})
	if !m.chatPollPending || m.pendingMsgID == "" {
		t.Fatal("stale response changed current poll chain")
	}
	pollTestUpdate(&m, chatStreamConnectedMsg{generation: m.chatStreamGeneration - 1, submissionID: m.chatSubmissionID, projectID: m.selectedID, execID: m.pendingMsgID})
	if m.chatStreamHealthy {
		t.Fatal("stale connect marked stream healthy")
	}
	m.markChatStreamHealthy()
	watchdog := chatStreamWatchdogMsg{id: m.chatStreamWatchdogID, activity: m.chatStreamActivity}
	m.markChatStreamHealthy()
	if cmd := pollTestUpdate(&m, watchdog); cmd == nil || !m.chatStreamHealthy {
		t.Fatal("activity failed to renew watchdog")
	}
	pollTestUpdate(&m, watchdog)
	if !m.chatStreamHealthy {
		t.Fatal("stale watchdog changed health")
	}
	pollTestUpdate(&m, chatStreamDisconnectedMsg{generation: m.chatStreamGeneration, submissionID: m.chatSubmissionID, projectID: m.selectedID, execID: m.pendingMsgID})
	if m.chatStreamHealthy || !m.chatPollPending {
		t.Fatal("disconnect did not restore fallback polling")
	}
}

func TestChatStreamFailureFallbackRequests(t *testing.T) {
	for _, mode := range []string{"connect failure", "disconnect", "auth"} {
		for _, terminal := range []string{"completed", "failed", "cancelled"} {
			t.Run(mode+"/"+terminal, func(t *testing.T) {
				var reads atomic.Int32
				offsets := make(chan string, 4)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.Contains(r.URL.Path, "/events/chat/") {
						offsets <- r.URL.Query().Get("offset")
						switch mode {
						case "connect failure":
							w.WriteHeader(http.StatusServiceUnavailable)
						case "auth":
							w.WriteHeader(http.StatusUnauthorized)
						default:
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = w.Write([]byte("data: partial λ\n\n"))
						}
						return
					}
					reads.Add(1)
					_ = json.NewEncoder(w).Encode(client.ChatStatus{MessageID: "exec-1", Status: terminal, Response: "partial λ"})
				}))
				defer srv.Close()
				m := pendingChatStreamTestModel(t)
				m.client, _ = client.New(srv.URL)
				defer func() { m.invalidateChatStream() }()
				batch := m.connectChatStream("exec-1", 0)().(tea.BatchMsg)
				msg := batch[0]()
				// Drain the stream, including buffered output before EOF.
				for {
					if _, ok := msg.(chatStreamEventMsg); !ok {
						break
					}
					pollTestUpdate(&m, msg)
					msg = m.chatStreamWaitCmd()
				}
				pollTestUpdate(&m, msg)
				if mode == "auth" {
					if !m.authRequired || m.pendingMsgID != "exec-1" || m.chatPollPending {
						t.Fatal("auth failure did not park resumable work")
					}
					return
				}
				if !m.chatPollPending {
					t.Fatal("failure did not arm fallback")
				}
				if mode == "disconnect" {
					if m.chatStreamOffset != len("partial λ") {
						t.Fatalf("offset=%d", m.chatStreamOffset)
					}
					reconnect := m.chatStreamReconnectMessage(m.chatStreamGeneration)
					pollTestUpdate(&m, reconnect)
					<-offsets // initial attempt
					select {
					case offset := <-offsets:
						if offset != "10" {
							t.Fatalf("reconnect offset=%s", offset)
						}
					case <-time.After(3 * time.Second):
						t.Fatal("no reconnect")
					}
				}
				cmd := pollTestUpdate(&m, chatPollTickMsg{pollGeneration: m.chatPollGeneration})
				if cmd == nil {
					t.Fatal("fallback tick did not fetch")
				}
				pollTestUpdate(&m, cmd())
				if reads.Load() != 1 || m.busy || !strings.Contains(transcript(m), "agent::partial λ") {
					t.Fatalf("fallback completion lost: %s", transcript(m))
				}
			})
		}
	}
}

func TestQueuedChatPollsUntilPromotion(t *testing.T) {
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/events/chat/") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		id, status := "queue-1", "queued"
		if reads.Add(1) > 1 {
			id, status = "exec-1", "processing"
		}
		_ = json.NewEncoder(w).Encode(client.ChatStatus{MessageID: id, Status: status})
	}))
	defer srv.Close()
	m := pendingChatStreamTestModel(t)
	m.client, _ = client.New(srv.URL)
	m.pendingMsgID = "queue-1"
	m.pendingMsgExecutionID = ""
	defer func() { m.invalidateChatStream() }()
	m.armChatPoll()
	for i := 0; i < 2; i++ {
		cmd := pollTestUpdate(&m, chatPollTickMsg{pollGeneration: m.chatPollGeneration})
		if cmd == nil {
			t.Fatal("queued chat stopped polling")
		}
		pollTestUpdate(&m, cmd())
	}
	if m.pendingMsgExecutionID != "exec-1" || m.chatStreamCancel == nil {
		t.Fatal("promotion did not start stream")
	}
	select {
	case <-m.chatStreamConnected:
	case <-time.After(3 * time.Second):
		t.Fatal("promoted stream did not connect")
	}
	pollTestUpdate(&m, chatStreamConnectedMsg{generation: m.chatStreamGeneration, submissionID: m.chatSubmissionID, projectID: m.selectedID, execID: "exec-1"})
	if cmd := pollTestUpdate(&m, chatPollTickMsg{pollGeneration: m.chatPollGeneration}); cmd != nil {
		t.Fatal("promoted healthy stream kept polling")
	}
	if reads.Load() != 2 {
		t.Fatalf("reads=%d", reads.Load())
	}
}
