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
				if r.URL.Path == "/events/ws" {
					live := acceptLiveTest(t, w, r)
					if live == nil {
						return
					}
					defer live.Close()
					live.Wait()
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
					if r.URL.Path == "/events/ws" {
						live := acceptLiveTest(t, w, r)
						if live == nil {
							return
						}
						defer live.Close()
						live.Wait()
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
				attempted := make(chan struct{}, 8)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/events/ws" {
						select {
						case attempted <- struct{}{}:
						default:
						}
						if mode == "connect failure" {
							w.WriteHeader(http.StatusServiceUnavailable)
							return
						}
						if mode == "auth" {
							w.WriteHeader(http.StatusUnauthorized)
							return
						}
						live := acceptLiveTest(t, w, r)
						if live == nil {
							return
						}
						defer live.Close()
						live.Delta("partial λ")
						return // connection loss is repaired inside the transport
					}
					reads.Add(1)
					_ = json.NewEncoder(w).Encode(client.ChatStatus{MessageID: "exec-1", Status: terminal, Response: "partial λ"})
				}))
				defer srv.Close()
				m := pendingChatStreamTestModel(t)
				m.client, _ = client.New(srv.URL)
				defer func() { m.invalidateChatStream() }()
				m.armChatPoll()
				batch := m.connectChatStream("exec-1", 0)().(tea.BatchMsg)
				select {
				case <-attempted:
				case <-time.After(3 * time.Second):
					t.Fatal("no connection attempt")
				}
				if mode == "disconnect" {
					result := make(chan tea.Msg, 1)
					go func() { result <- batch[0]() }()
					select {
					case msg := <-result:
						pollTestUpdate(&m, msg)
					case <-time.After(3 * time.Second):
						t.Fatal("no output")
					}
					if m.chatStreamOffset != len("partial λ") {
						t.Fatalf("offset=%d", m.chatStreamOffset)
					}
				}
				// No terminal stream error is emitted for a transient socket failure.
				// The existing fallback poll must still settle the accepted execution.
				var cmd tea.Cmd
				if mode == "disconnect" {
					cmd = pollTestUpdate(&m, chatStreamWatchdogMsg{id: m.chatStreamWatchdogID, activity: m.chatStreamActivity})
				} else {
					cmd = pollTestUpdate(&m, chatPollTickMsg{pollGeneration: m.chatPollGeneration})
				}
				if cmd == nil {
					t.Fatal("fallback tick did not fetch status")
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
		if r.URL.Path == "/events/ws" {
			live := acceptLiveTest(t, w, r)
			if live == nil {
				return
			}
			defer live.Close()
			live.Wait()
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

func TestRepeatedChatReconnectsCannotSuppressFallback(t *testing.T) {
	for _, terminal := range []string{"completed", "failed", "cancelled"} {
		for _, snapshot := range []string{"", "partial reply"} {
			t.Run(terminal+"/"+snapshot, func(t *testing.T) {
				var reads atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/events/ws" {
						live := acceptLiveTest(t, w, r)
						if live == nil {
							return
						}
						defer live.Close()
						live.Wait()
						return
					}
					n := reads.Add(1)
					if n == 2 {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					status := "processing"
					if n == 4 {
						status = terminal
					}
					_ = json.NewEncoder(w).Encode(client.ChatStatus{MessageID: "exec-1", Status: status, Response: snapshot})
				}))
				defer srv.Close()
				m := pendingChatStreamTestModel(t)
				m.client, _ = client.New(srv.URL)
				defer func() { m.invalidateChatStream() }()
				pollTestConnect(t, &m)
				m.updateChatStreamOutput("partial reply")
				for cycle := 0; cycle < 4; cycle++ {
					// Deliver disconnect and successful reconnect before the 1.5s
					// poll tick, reproducing a stream that repeatedly closes at 2s.
					pollTestUpdate(&m, chatStreamDisconnectedMsg{generation: m.chatStreamGeneration, submissionID: m.chatSubmissionID, projectID: m.selectedID, execID: m.pendingMsgID})
					pollTestConnect(t, &m)
					if !m.chatStreamHealthy || !m.chatPollPending {
						t.Fatal("reconnect lost health or pending fallback")
					}
					cmd := pollTestUpdate(&m, chatPollTickMsg{pollGeneration: m.chatPollGeneration})
					if cmd == nil {
						t.Fatal("successful reconnect suppressed required status check")
					}
					pollTestUpdate(&m, cmd())
					if cycle < 3 && !m.busy {
						t.Fatal("nonterminal status settled chat")
					}
					if cycle == 0 && (m.chatPollPending || m.armChatPoll() != nil) {
						t.Fatal("healthy stream continued polling after successful check")
					}
					if cycle == 1 && !m.chatPollPending {
						t.Fatal("failed status check did not schedule retry")
					}
				}
				if reads.Load() != 4 || m.busy || m.pendingMsgID != "" || !strings.Contains(transcript(m), "agent::partial reply") {
					t.Fatalf("reconnect fallback lost completion/output: reads=%d transcript=%s", reads.Load(), transcript(m))
				}
			})
		}
	}
}

func TestChatDisconnectDuringStatusRequestPreservesNextCheck(t *testing.T) {
	m := pendingChatStreamTestModel(t)
	m.armChatPoll()
	if cmd := pollTestUpdate(&m, chatPollTickMsg{pollGeneration: m.chatPollGeneration}); cmd == nil {
		t.Fatal("missing initial status request")
	}
	pollTestUpdate(&m, chatStreamDisconnectedMsg{generation: m.chatStreamGeneration, submissionID: m.chatSubmissionID, projectID: m.selectedID, execID: m.pendingMsgID})
	m.markChatStreamHealthy()
	// Represent the reconnect attempt without starting network commands.
	m.chatStreamCancel = func() {}
	pollTestUpdate(&m, chatStatusMsg{pollGeneration: m.chatPollGeneration, status: &client.ChatStatus{Status: "processing"}})
	if !m.chatPollRequired || !m.chatPollPending {
		t.Fatal("in-flight response lost the disconnect-triggered check")
	}
	if cmd := pollTestUpdate(&m, chatPollTickMsg{pollGeneration: m.chatPollGeneration}); cmd == nil {
		t.Fatal("healthy reconnect suppressed the next check")
	}
	m.resetChatPoll()
	if m.chatPollRequired || m.chatPollPending {
		t.Fatal("poll reset retained disconnect state")
	}
}
