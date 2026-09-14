package terminal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

const pendingTaskBoard = `<div data-task-id="t-1" data-task-status="running" data-task-category="active"><a href="/tasks/t-1" title="Refactor">Refactor</a></div>`

func pendingCLIClient(t *testing.T, pending string, active bool, posts *int) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"projects":[{"id":"p1","name":"demo"}]}`)
		case "/tasks":
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprint(w, pendingTaskBoard)
		case "/tasks/t-1/thread/pending-inputs":
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprint(w, pending)
		case "/tasks/t-1/thread":
			w.Header().Set("Content-Type", "text/html")
			if active {
				_, _ = fmt.Fprint(w, `<div data-execution-pair="true" data-exec-id="turn-1" data-exec-status="running"></div>`)
			} else {
				_, _ = fmt.Fprint(w, `<div data-execution-pair="true" data-exec-id="turn-1" data-exec-status="completed"></div>`)
			}
		case "/thread-inputs/q1/cancel", "/tasks/t-1/thread/queued/q1/steer":
			(*posts)++
			w.Header().Set("X-OpenVibely-Thread-Input-Status", "cancelled")
			w.Header().Set("Content-Type", "text/html")
			if r.URL.Path != "/thread-inputs/q1/cancel" {
				_, _ = fmt.Fprint(w, `<div data-thread-input-id="q1" data-task-id="t-1" data-input-mode="steering"></div>`)
			}
		default:
			t.Errorf("unexpected pending-input request %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTaskThreadInputListSeparatesModesAndOmitsControls(t *testing.T) {
	pending := `<div id="pending-thread-inputs" data-task-id="t-1"><div data-thread-input-id="q1" data-task-id="t-1" data-input-mode="queued"><div class="truncate">continue docs</div><button>secret</button></div><div data-thread-input-id="s1" data-task-id="t-1" data-input-mode="steering"><div class="truncate">stop now</div><button>Cancel</button></div></div>`
	var posts int
	c := pendingCLIClient(t, pending, true, &posts)
	m := New(c)
	m.selectedID, m.selectedName = "p1", "demo"
	m = runLine(t, m, "/tasks inputs Refactor")
	out := transcript(m)
	if !strings.Contains(out, "queued follow-up") || !strings.Contains(out, "steering") || !strings.Contains(out, "q1") || !strings.Contains(out, "s1") || !strings.Contains(out, "task t-1") || !strings.Contains(out, "project p1") {
		t.Fatalf("pending input list missing safe mode/IDs: %q", out)
	}
	if strings.Contains(out, "secret") || strings.Contains(out, "Cancel") || strings.Contains(out, "<button") {
		t.Fatalf("pending input controls leaked: %q", out)
	}
}

func TestTaskThreadInputEmptyJSONAndCancelConfirmationForce(t *testing.T) {
	empty := `<div id="pending-thread-inputs" data-task-id="t-1"></div>`
	var posts int
	c := pendingCLIClient(t, empty, true, &posts)
	var out bytes.Buffer
	if err := RunCLI(c, &out, "p1", []string{"tasks", "inputs", "Refactor"}, false, true); err != nil {
		t.Fatalf("empty JSON list: %v", err)
	}
	if strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("empty JSON = %q, want []", out.String())
	}

	pending := `<div id="pending-thread-inputs" data-task-id="t-1"><div data-thread-input-id="q1" data-task-id="t-1" data-input-mode="queued"><div class="truncate">continue docs</div></div></div>`
	posts = 0
	c = pendingCLIClient(t, pending, true, &posts)
	out.Reset()
	if err := RunCLI(c, &out, "p1", []string{"tasks", "inputs", "cancel", "Refactor", "q1"}, false, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("unforced cancellation error = %v", err)
	}
	if posts != 0 {
		t.Fatalf("unforced cancellation posts = %d", posts)
	}
	if err := RunCLI(c, &out, "p1", []string{"tasks", "inputs", "cancel", "Refactor", "q1"}, true, true); err != nil {
		t.Fatalf("forced cancellation: %v", err)
	}
	var ack map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &ack); err != nil {
		t.Fatalf("cancel JSON = %q: %v", out.String(), err)
	}
	if ack["status"] != "cancelled" || ack["input_id"] != "q1" || ack["input_mode"] != "queued" || posts != 1 {
		t.Fatalf("cancel acknowledgement = %#v posts=%d", ack, posts)
	}
}

func TestTaskThreadQueuedInputSteerRequiresActiveTurnAndReportsCanonicalIdentity(t *testing.T) {
	pending := `<div id="pending-thread-inputs" data-task-id="t-1"><div data-thread-input-id="q1" data-task-id="t-1" data-input-mode="queued"><div class="truncate">continue docs</div></div></div>`
	var posts int
	c := pendingCLIClient(t, pending, true, &posts)
	var out bytes.Buffer
	if err := RunCLI(c, &out, "p1", []string{"tasks", "inputs", "steer", "Refactor", "q1"}, false, true); err != nil {
		t.Fatalf("queued steer: %v", err)
	}
	var ack map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &ack); err != nil {
		t.Fatalf("steer JSON = %q: %v", out.String(), err)
	}
	if ack["status"] != "steered" || ack["input_id"] != "q1" || ack["input_mode"] != "steering" || posts != 1 {
		t.Fatalf("steer acknowledgement = %#v posts=%d", ack, posts)
	}

	posts = 0
	c = pendingCLIClient(t, pending, false, &posts)
	out.Reset()
	if err := RunCLI(c, &out, "p1", []string{"tasks", "inputs", "steer", "Refactor", "q1"}, false, false); err == nil || !strings.Contains(err.Error(), "no active response") {
		t.Fatalf("stale steer error = %v", err)
	}
	if posts != 0 {
		t.Fatalf("stale steer posts = %d", posts)
	}
}

func TestTaskThreadInputInteractiveCancellationRequiresYes(t *testing.T) {
	pending := `<div id="pending-thread-inputs" data-task-id="t-1"><div data-thread-input-id="q1" data-task-id="t-1" data-input-mode="queued"><div class="truncate">continue docs</div></div></div>`
	var posts int
	c := pendingCLIClient(t, pending, true, &posts)
	m := New(c)
	m.selectedID, m.selectedName = "p1", "demo"
	m, resolver := typeLine(t, m, "/tasks inputs cancel Refactor q1")
	if resolver == nil {
		t.Fatal("missing pending-input resolver")
	}
	target := resolver()
	next, cmd := m.Update(target)
	m = next.(Model)
	if cmd != nil || m.pendingConfirmation == nil {
		t.Fatalf("cancellation confirmation state = cmd:%v pending:%v", cmd != nil, m.pendingConfirmation != nil)
	}
	if !strings.Contains(m.pendingConfirmation.message, "q1") || !strings.Contains(m.pendingConfirmation.message, "yes") {
		t.Fatalf("confirmation = %q", m.pendingConfirmation.message)
	}
	m = runLine(t, m, "yes")
	if posts != 1 {
		t.Fatalf("confirmed cancellation posts = %d, want one", posts)
	}
}

func TestTaskThreadInputMutationResultIsIgnoredAfterThreadSwitch(t *testing.T) {
	pending := `<div id="pending-thread-inputs" data-task-id="t-1"><div data-thread-input-id="q1" data-task-id="t-1" data-input-mode="queued"><div class="truncate">continue docs</div></div></div>`
	var posts int
	c := pendingCLIClient(t, pending, true, &posts)
	m := New(c)
	m.selectedID, m.selectedName = "p1", "demo"
	m, resolver := typeLine(t, m, "/tasks inputs steer Refactor q1")
	if resolver == nil {
		t.Fatal("missing pending-input resolver")
	}
	target := resolver()
	next, mutation := m.Update(target)
	m = next.(Model)
	if mutation == nil {
		t.Fatal("missing mutation command")
	}
	m.threadID = "different-task"
	result := mutation()
	next, _ = m.Update(result)
	m = next.(Model)
	if strings.Contains(transcript(m), "queued follow-up q1 is now pending steering") {
		t.Fatalf("stale mutation acknowledgement rendered: %q", transcript(m))
	}
	if posts != 1 {
		t.Fatalf("mutation post count = %d, want one backend mutation with stale result suppressed", posts)
	}
}
