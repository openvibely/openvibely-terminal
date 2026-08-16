package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNewNormalizesURL(t *testing.T) {
	c, err := New("localhost:3001/")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, want := c.BaseURL(), "http://localhost:3001"; got != want {
		t.Errorf("BaseURL = %q, want %q", got, want)
	}

	if _, err := New("   "); err == nil {
		t.Error("expected error for empty URL")
	}
}

func TestListProjects(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"projects": []Project{{ID: "p1", Name: "Demo", Path: "/tmp/demo"}},
		})
	}))

	projects, err := c.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != "p1" || projects[0].Name != "Demo" {
		t.Errorf("unexpected projects: %+v", projects)
	}
}

func TestGetGlobalCapacity(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/capacity/global" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(GlobalCapacity{MaxWorkers: 5, TotalRunning: 2, AvailableSlots: 3, HasCapacity: true})
	}))

	cap, err := c.GetGlobalCapacity(context.Background())
	if err != nil {
		t.Fatalf("GetGlobalCapacity: %v", err)
	}
	if cap.MaxWorkers != 5 || cap.AvailableSlots != 3 {
		t.Errorf("unexpected capacity: %+v", cap)
	}
}

func TestSendChatMessage(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat/message" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if r.FormValue("message") != "hello" || r.FormValue("project_id") != "p1" {
			t.Errorf("unexpected form: %v", r.Form)
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(ChatAccepted{MessageID: "exec1", Status: "processing"})
	}))

	accepted, err := c.SendChatMessage(context.Background(), "p1", "hello")
	if err != nil {
		t.Fatalf("SendChatMessage: %v", err)
	}
	if accepted.MessageID != "exec1" {
		t.Errorf("unexpected accepted: %+v", accepted)
	}
}

func TestSendChatMessageServerError(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "project not found"})
	}))

	_, err := c.SendChatMessage(context.Background(), "missing", "hello")
	if err == nil {
		t.Fatal("expected error")
	}
	if want := "project not found"; !contains(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err.Error(), want)
	}
}

func TestGetChatStatus(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat/message/exec1" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(ChatStatus{MessageID: "exec1", Status: "completed", Response: "hi there"})
	}))

	status, err := c.GetChatStatus(context.Background(), "exec1")
	if err != nil {
		t.Fatalf("GetChatStatus: %v", err)
	}
	if status.Status != "completed" || status.Response != "hi there" {
		t.Errorf("unexpected status: %+v", status)
	}
}

func TestLoginSuccessAndFailure(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.FormValue("username") == "admin" && r.FormValue("password") == "secret" {
			http.SetCookie(w, &http.Cookie{Name: "ov_session", Value: "tok"})
			w.Header().Set("Location", "/")
			w.WriteHeader(http.StatusFound)
			return
		}
		w.Header().Set("Location", "/login?next=%2F")
		w.WriteHeader(http.StatusFound)
	}))

	if err := c.Login(context.Background(), "admin", "secret"); err != nil {
		t.Errorf("Login success case: %v", err)
	}
	if err := c.Login(context.Background(), "admin", "wrong"); err == nil {
		t.Error("expected login failure")
	}
}

func TestAuthMe(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(AuthStatus{Authenticated: true, Username: "admin"})
	}))

	auth, err := c.AuthMe(context.Background())
	if err != nil {
		t.Fatalf("AuthMe: %v", err)
	}
	if !auth.Authenticated || auth.Username != "admin" {
		t.Errorf("unexpected auth: %+v", auth)
	}
}

func TestStreamEvents(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events/live" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, ": ping\n\n")
		flusher.Flush()
		fmt.Fprint(w, "event: task_status_changed\n")
		fmt.Fprint(w, `data: {"type":"task_status_changed","task_id":"t1","status":"running"}`+"\n\n")
		flusher.Flush()
		fmt.Fprint(w, `data: {"type":"chat_new_message","project_id":"p1","exec_id":"e1"}`+"\n\n")
		flusher.Flush()
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, errs := c.StreamEvents(ctx, "")

	ev1, ok := <-events
	if !ok {
		t.Fatal("expected first event")
	}
	if ev1.Name != "task_status_changed" {
		t.Errorf("event name = %q", ev1.Name)
	}
	var te TaskEvent
	if err := json.Unmarshal(ev1.Data, &te); err != nil || te.TaskID != "t1" || te.Status != "running" {
		t.Errorf("unexpected task event: %+v err=%v", te, err)
	}

	ev2, ok := <-events
	if !ok {
		t.Fatal("expected second event")
	}
	var ce ChatEvent
	if err := json.Unmarshal(ev2.Data, &ce); err != nil || ce.ExecID != "e1" {
		t.Errorf("unexpected chat event: %+v err=%v", ce, err)
	}

	// Server closes the stream: expect a terminal error reporting closure.
	select {
	case err := <-errs:
		if err == nil {
			t.Error("expected stream-closed error")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for stream end")
	}
}

func TestStreamEventsCancellation(t *testing.T) {
	blockCh := make(chan struct{})
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-blockCh
	}))
	defer close(blockCh)

	ctx, cancel := context.WithCancel(context.Background())
	events, errs := c.StreamEvents(ctx, "")
	time.Sleep(50 * time.Millisecond)
	cancel()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-events:
			if !ok {
				events = nil
			}
		case err, ok := <-errs:
			if !ok {
				return // clean shutdown, no terminal error
			}
			if err != nil {
				t.Errorf("expected nil error on cancellation, got %v", err)
			}
		case <-deadline:
			t.Fatal("stream did not shut down after cancellation")
		}
		if events == nil {
			return
		}
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
