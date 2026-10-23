// Package testutil provides backend protocol fixtures for integration tests.
package testutil

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type LiveCommand struct {
	Op        string `json:"op"`
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	ExecID    string `json:"exec_id"`
	Offset    int    `json:"offset"`
}

type LiveSocket struct {
	Conn    *websocket.Conn
	Context context.Context
	Command LiveCommand
	cancel  context.CancelFunc
	offset  int
	t       testing.TB
}

// AcceptLive checks the actual upgrade route and reads the first scoped command.
// Scope stays in the command; no synthetic HTTP query parameters are introduced.
func AcceptLive(t testing.TB, w http.ResponseWriter, r *http.Request) *LiveSocket {
	t.Helper()
	if r.URL.Path != "/events/ws" {
		t.Errorf("WebSocket path = %q", r.URL.Path)
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"openvibely-live"}})
	if err != nil {
		t.Errorf("accept WebSocket: %v", err)
		return nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	s := &LiveSocket{Conn: conn, Context: ctx, cancel: cancel, t: t}
	if err = wsjson.Write(ctx, conn, map[string]any{"event": "ready"}); err == nil {
		err = wsjson.Read(ctx, conn, &s.Command)
	}
	if err != nil {
		s.Close()
		return nil
	}
	if s.Command.ID == "" || s.Command.ProjectID == "" || (s.Command.Op != "live" && s.Command.Op != "execution") {
		t.Errorf("invalid subscription: %+v", s.Command)
	}
	s.offset = s.Command.Offset
	s.Send("subscribed", nil)
	return s
}

func (s *LiveSocket) Close() { s.cancel(); _ = s.Conn.CloseNow() }
func (s *LiveSocket) Wait()  { s.waitSubscriptions(nil) }

// Subscriptions observes project switches that reuse the same shared socket.
func (s *LiveSocket) Subscriptions(observe func()) {
	observe()
	s.waitSubscriptions(observe)
}

func (s *LiveSocket) waitSubscriptions(observe func()) {
	for {
		var cmd LiveCommand
		if wsjson.Read(s.Context, s.Conn, &cmd) != nil {
			return
		}
		if cmd.Op != "live" && cmd.Op != "execution" {
			continue
		}
		if cmd.ProjectID == "" || cmd.ID == "" {
			s.t.Errorf("unscoped subscription: %+v", cmd)
		}
		s.Command = cmd
		s.offset = cmd.Offset
		s.Send("subscribed", nil)
		if observe != nil {
			observe()
		}
	}
}
func (s *LiveSocket) Send(event string, data any) {
	if err := wsjson.Write(s.Context, s.Conn, map[string]any{"id": s.Command.ID, "event": event, "data": data}); err != nil && s.Context.Err() == nil {
		s.t.Logf("fixture write after peer shutdown: %v", err)
	}
}
func (s *LiveSocket) Delta(data string) {
	start := s.offset
	s.offset += len(data)
	_ = wsjson.Write(s.Context, s.Conn, map[string]any{"id": s.Command.ID, "event": "message", "data": data, "start": start, "offset": s.offset})
}
func (s *LiveSocket) Event(name, payload string) { s.Send(name, json.RawMessage(payload)) }
