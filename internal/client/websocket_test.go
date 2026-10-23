package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Fixtures speak the same multiplexed protocol as the backend, including the
// ready handshake and scoped subscription commands (not the removed SSE routes).
func acceptTestSocket(t *testing.T, w http.ResponseWriter, r *http.Request) (*websocket.Conn, context.Context) {
	t.Helper()
	if r.URL.Path != "/events/ws" {
		t.Errorf("path = %q", r.URL.Path)
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"openvibely-live"}})
	if err != nil {
		t.Errorf("accept: %v", err)
		return nil, r.Context()
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	t.Cleanup(cancel)
	if err := wsjson.Write(ctx, conn, liveFrame{Event: "ready"}); err != nil {
		t.Errorf("ready: %v", err)
	}
	return conn, ctx
}

func TestWebSocketExecutionReplay(t *testing.T) {
	var connections atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, ctx := acceptTestSocket(t, w, r)
		if conn == nil {
			return
		}
		defer conn.CloseNow()
		var cmd liveCommand
		if err := wsjson.Read(ctx, conn, &cmd); err != nil {
			t.Errorf("subscribe: %v", err)
			return
		}
		if cmd.Op != "execution" || cmd.ProjectID != "p1" || cmd.ExecID != "exec/one" {
			t.Errorf("command = %+v", cmd)
		}
		attempt := connections.Add(1)
		if attempt == 1 {
			if cmd.Offset != 5 {
				t.Errorf("initial offset = %d", cmd.Offset)
			}
			_ = wsjson.Write(ctx, conn, liveFrame{ID: cmd.ID, Event: "message", Start: 5, Offset: 11, Data: json.RawMessage(`"世界"`)})
			return
		}
		// A disconnect can race cursor publication; replaying an overlapping frame
		// must still deliver each UTF-8 byte only once to the caller.
		if cmd.Offset != 5 && cmd.Offset != 11 {
			t.Errorf("resume offset = %d", cmd.Offset)
		}
		_ = wsjson.Write(ctx, conn, liveFrame{ID: cmd.ID, Event: "message", Start: 5, Offset: 12, Data: json.RawMessage(`"世界!"`)})
		_ = wsjson.Write(ctx, conn, liveFrame{ID: cmd.ID, Event: "done", Data: json.RawMessage(`"completed"`)})
		_, _, _ = conn.Read(ctx)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, errs := c.StreamExecutionForProject(ctx, "p1", "exec/one", 5)
	var output string
	var done bool
	for event := range events {
		if event.Type == ExecutionDelta {
			output += event.Data
		}
		if event.Type == ExecutionDone {
			done = true
		}
	}
	for err := range errs {
		t.Errorf("stream: %v", err)
	}
	if output != "世界!" || !done || connections.Load() != 2 {
		t.Fatalf("output=%q done=%v connections=%d", output, done, connections.Load())
	}
}

func TestWebSocketHandshakeRecovery(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusUnauthorized, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var attempts atomic.Int32
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/login" {
					t.Error("followed login redirect")
					http.Error(w, "login", 401)
					return
				}
				if attempts.Add(1) == 1 {
					w.Header().Set("Location", "/login")
					w.WriteHeader(status)
					return
				}
				conn, ctx := acceptTestSocket(t, w, r)
				if conn == nil {
					return
				}
				defer conn.CloseNow()
				var cmd liveCommand
				if err := wsjson.Read(ctx, conn, &cmd); err != nil {
					return
				}
				_ = wsjson.Write(ctx, conn, liveFrame{ID: cmd.ID, Event: "subscribed"})
				_ = wsjson.Write(ctx, conn, liveFrame{Event: "reconcile"})
				_, _, _ = conn.Read(ctx)
			}))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			events, errs := c.StreamEvents(ctx, "project/two")
			for i := 0; i < 2; i++ {
				select {
				case ev := <-events:
					if ev.Name != "reconcile" || string(ev.Data) != `{"project_id":"project/two"}` {
						t.Fatalf("event = %+v", ev)
					}
				case <-ctx.Done():
					t.Fatal("recovery timed out")
				}
			}
			cancel()
			for range events {
			}
			for err := range errs {
				t.Errorf("error = %v", err)
			}
			if attempts.Load() != 2 {
				t.Errorf("attempts = %d", attempts.Load())
			}
		})
	}
}

func TestWebSocketSharedConnectionAndCancellation(t *testing.T) {
	var connections atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connections.Add(1)
		conn, ctx := acceptTestSocket(t, w, r)
		if conn == nil {
			return
		}
		defer conn.CloseNow()
		for {
			var cmd liveCommand
			if err := wsjson.Read(ctx, conn, &cmd); err != nil {
				return
			}
			switch cmd.Op {
			case "live":
				if cmd.ProjectID != "p1" {
					t.Errorf("scope = %q", cmd.ProjectID)
				}
				_ = wsjson.Write(ctx, conn, liveFrame{ID: cmd.ID, Event: "subscribed"})
				_ = wsjson.Write(ctx, conn, liveFrame{ID: cmd.ID, Event: "task_status_changed", Data: json.RawMessage(`{"project_id":"p1","task_id":"t1","status":"completed"}`)})
			case "execution":
				_ = wsjson.Write(ctx, conn, liveFrame{ID: cmd.ID, Event: "message", Offset: 3, Data: json.RawMessage(`"abc"`)})
				_ = wsjson.Write(ctx, conn, liveFrame{ID: cmd.ID, Event: "reset", Offset: 1, Data: json.RawMessage(`"a"`)})
				_ = wsjson.Write(ctx, conn, liveFrame{ID: cmd.ID, Event: "error", Data: json.RawMessage(`"failed"`)})
			}
		}
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	live, liveErrs := c.StreamEvents(ctx, "p1")
	if ev := <-live; ev.Name != "reconcile" {
		t.Fatalf("event = %+v", ev)
	}
	if ev := <-live; ev.Name != "task_status_changed" {
		t.Fatalf("event = %+v", ev)
	}
	events, errs := c.StreamChatOutputForProject(ctx, "p1", "e1", 0)
	var got []ChatOutputEvent
	for event := range events {
		got = append(got, event)
	}
	for err := range errs {
		t.Errorf("execution: %v", err)
	}
	if len(got) != 3 || got[0].Data != "abc" || got[1].Name != "reset" || got[1].Offset != 1 || got[2].Name != "error" {
		t.Fatalf("events = %+v", got)
	}
	cancel()
	for range live {
	}
	for err := range liveErrs {
		t.Errorf("live: %v", err)
	}
	if connections.Load() != 1 {
		t.Errorf("connections = %d", connections.Load())
	}
}

func TestWebSocketLargePayloadAndBackpressureCancellation(t *testing.T) {
	payload := "\uFEFF" + strings.Repeat("x", (1<<20)+137) + "\r\n"
	written := make(chan struct{})
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, ctx := acceptTestSocket(t, w, r)
		if conn == nil {
			return
		}
		defer conn.CloseNow()
		var cmd liveCommand
		if err := wsjson.Read(ctx, conn, &cmd); err != nil {
			return
		}
		data, _ := json.Marshal(payload)
		_ = wsjson.Write(ctx, conn, liveFrame{ID: cmd.ID, Event: "message", Offset: len(payload), Data: data})
		for i := 0; i < 40; i++ {
			_ = wsjson.Write(ctx, conn, liveFrame{ID: cmd.ID, Event: "message", Start: len(payload) + i, Offset: len(payload) + i + 1, Data: json.RawMessage(`"x"`)})
		}
		close(written)
		_, _, _ = conn.Read(ctx)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, errs := c.StreamChatOutputForProject(ctx, "p1", "e1", 0)
	if got := <-events; got.Data != payload {
		t.Fatalf("payload length = %d, want %d", len(got.Data), len(payload))
	}
	select {
	case <-written:
	case <-ctx.Done():
		t.Fatal("write timed out")
	}
	cancel()
	for range events {
	}
	for err := range errs {
		t.Errorf("cancel: %v", err)
	}
}

func TestWebSocketRequiresProjectScope(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unscoped request reached server") }))
	events, errs := c.StreamChatOutput(context.Background(), "e1", 0)
	for range events {
		t.Error("unexpected event")
	}
	if err := <-errs; err == nil || !strings.Contains(err.Error(), "project") {
		t.Fatalf("error = %v", err)
	}
}
