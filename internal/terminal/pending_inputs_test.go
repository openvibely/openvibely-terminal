package terminal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
		case "/api/tasks/reference-catalog":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"tasks":[{"id":"t-1","project_id":"p1","title":"Refactor","status":"running","category":"active"},{"id":"t-2","project_id":"p1","title":"Review docs","status":"pending","category":"backlog"}]}`)
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

func TestTaskThreadInputTargetUsesCompactProjectScopedCatalog(t *testing.T) {
	tasks := []client.Task{
		{ID: "t-refactor", ProjectID: "p1", Title: "Refactor"},
		{ID: "t-review", ProjectID: "p1", Title: "Review docs"},
		{ID: "t-ambiguous-a", ProjectID: "p1", Title: "Review cleanup"},
		{ID: "t-ambiguous-b", ProjectID: "p1", Title: "Review cleanup"},
	}
	pendingByTask := map[string]string{
		"t-refactor": pendingInputHTML("p1", "t-refactor", "q1", "queued"),
		"t-review":   pendingInputHTML("p1", "t-review", "q1", "queued"),
	}
	c, requests := newPendingTargetClient(t, tasks, pendingByTask)
	cases := []struct {
		name, action string
		args         []string
		wantTask     string
		wantInput    string
		wantErr      string
	}{
		{name: "cancel exact task ID", action: "cancel", args: []string{"t-refactor", "q1"}, wantTask: "t-refactor", wantInput: "q1"},
		{name: "steer multi-word title with trailing input ID", action: "steer", args: []string{"Review", "docs", "q1"}, wantTask: "t-review", wantInput: "q1"},
		{name: "ambiguous title", action: "cancel", args: []string{"Review", "cleanup", "q1"}, wantErr: "ambiguous"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requests.reset()
			m := New(c)
			m.selectedID = "p1"
			msg := resolveTaskThreadInputTarget(m, c, "p1", tc.action, tc.args)().(taskThreadInputTargetMsg)
			if tc.wantErr != "" {
				if msg.err == nil || !strings.Contains(msg.err.Error(), tc.wantErr) {
					t.Fatalf("target error = %v, want %q", msg.err, tc.wantErr)
				}
				_, mutationCmd := m.Update(msg)
				if mutationCmd != nil {
					t.Fatalf("invalid target produced a mutation command: %v", mutationCmd)
				}
				if requests.countPath("/tasks/t-ambiguous-a/thread/pending-inputs") != 0 || requests.countPath("/tasks/t-ambiguous-b/thread/pending-inputs") != 0 {
					t.Fatalf("ambiguous resolution fetched a pending-input list: %v", requests.snapshot())
				}
			} else {
				if msg.err != nil {
					t.Fatalf("target resolution: %v", msg.err)
				}
				if msg.task.ID != tc.wantTask || msg.input.ID != tc.wantInput {
					t.Fatalf("resolved task/input = %q/%q, want %q/%q", msg.task.ID, msg.input.ID, tc.wantTask, tc.wantInput)
				}
				next, actionCmd := m.Update(msg)
				updated := next.(Model)
				if tc.action == "cancel" && updated.pendingConfirmation == nil {
					t.Fatal("valid cancel target did not reach its confirmation")
				}
				if tc.action == "steer" && actionCmd == nil {
					t.Fatal("valid steer target did not produce its mutation command")
				}
			}
			got := requests.snapshot()
			if requests.countPath("/api/tasks/reference-catalog") != 1 || requests.countPath("/tasks") != 0 {
				t.Fatalf("task listing requests = %v, want one compact catalog and no HTML board", got)
			}
			for _, request := range got {
				if strings.Contains(request, "/api/tasks/reference-catalog?") && !strings.Contains(request, "project_id=p1") {
					t.Fatalf("task catalog request is not project scoped: %q", request)
				}
			}
			if requests.mutationCount() != 0 {
				t.Fatalf("target resolution performed a mutation: %v", got)
			}
		})
	}
}

func TestTaskThreadInputTargetRejectsMissingStaleAndForeignInputs(t *testing.T) {
	tasks := []client.Task{{ID: "t-1", ProjectID: "p1", Title: "Review docs"}}
	cases := []struct {
		name, pendingHTML, action, inputRef, wantErr string
	}{
		{name: "missing input", pendingHTML: pendingInputHTML("p1", "t-1", "q1", "queued"), action: "cancel", inputRef: "missing", wantErr: "nothing matches"},
		{name: "stale steering input cannot be steered", pendingHTML: pendingInputHTML("p1", "t-1", "q1", "steering"), action: "steer", inputRef: "q1", wantErr: "not a queued follow-up"},
		{name: "foreign project", pendingHTML: pendingInputHTML("p2", "t-1", "q1", "queued"), action: "cancel", inputRef: "q1", wantErr: "project"},
		{name: "foreign task", pendingHTML: pendingInputHTML("p1", "t-foreign", "q1", "queued"), action: "cancel", inputRef: "q1", wantErr: "not requested task"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requests := &pendingTargetRequestLog{}
			c := newPendingTargetClientWithLog(t, tasks, map[string]string{"t-1": tc.pendingHTML}, requests)
			m := New(c)
			m.selectedID = "p1"
			msg := resolveTaskThreadInputTarget(m, c, "p1", tc.action, []string{"Review", "docs", tc.inputRef})().(taskThreadInputTargetMsg)
			if msg.err == nil || !strings.Contains(msg.err.Error(), tc.wantErr) {
				t.Fatalf("target error = %v, want %q", msg.err, tc.wantErr)
			}
			_, mutationCmd := m.Update(msg)
			if mutationCmd != nil {
				t.Fatalf("invalid target produced a mutation command: %v", mutationCmd)
			}
			if requests.mutationCount() != 0 {
				t.Fatalf("invalid target performed a mutation: %v", requests.snapshot())
			}
			if requests.countPath("/api/tasks/reference-catalog") != 1 || requests.countPath("/tasks") != 0 {
				t.Fatalf("task listing requests = %v, want one compact catalog and no HTML board", requests.snapshot())
			}
		})
	}
}

type pendingTargetRequestLog struct {
	mu       sync.Mutex
	requests []string
}

func (l *pendingTargetRequestLog) add(request string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests = append(l.requests, request)
}

func (l *pendingTargetRequestLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.requests...)
}

func (l *pendingTargetRequestLog) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests = nil
}

func (l *pendingTargetRequestLog) countPath(path string) int {
	count := 0
	for _, request := range l.snapshot() {
		fields := strings.Fields(request)
		if len(fields) != 2 {
			continue
		}
		requestPath := strings.SplitN(fields[1], "?", 2)[0]
		if requestPath == path {
			count++
		}
	}
	return count
}

func (l *pendingTargetRequestLog) mutationCount() int {
	count := 0
	for _, request := range l.snapshot() {
		if strings.HasPrefix(request, "POST ") || strings.HasPrefix(request, "DELETE ") {
			count++
		}
	}
	return count
}

func newPendingTargetClient(t *testing.T, tasks []client.Task, pendingByTask map[string]string) (*client.Client, *pendingTargetRequestLog) {
	t.Helper()
	requests := &pendingTargetRequestLog{}
	return newPendingTargetClientWithLog(t, tasks, pendingByTask, requests), requests
}

func newPendingTargetClientWithLog(t *testing.T, tasks []client.Task, pendingByTask map[string]string, requests *pendingTargetRequestLog) *client.Client {
	t.Helper()
	catalog, err := json.Marshal(map[string]any{"tasks": tasks})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.Method + " " + r.URL.RequestURI())
		switch r.URL.Path {
		case "/api/tasks/reference-catalog":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(catalog)
		case "/tasks":
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprint(w, pendingTaskBoard)
		default:
			if strings.HasPrefix(r.URL.Path, "/tasks/") && strings.HasSuffix(r.URL.Path, "/thread/pending-inputs") {
				taskID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/tasks/"), "/thread/pending-inputs")
				if body, ok := pendingByTask[taskID]; ok {
					w.Header().Set("Content-Type", "text/html")
					_, _ = fmt.Fprint(w, body)
					return
				}
			}
			if strings.HasPrefix(r.URL.Path, "/thread-inputs/") || strings.Contains(r.URL.Path, "/steer") {
				t.Errorf("unexpected pending-input mutation request %s %s", r.Method, r.URL.RequestURI())
			} else {
				t.Errorf("unexpected pending-input request %s %s", r.Method, r.URL.RequestURI())
			}
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

func pendingInputHTML(projectID, taskID, inputID, mode string) string {
	return fmt.Sprintf(`<div id="pending-thread-inputs" data-task-id="%s" data-project-id="%s"><div data-thread-input-id="%s" data-task-id="%s" data-input-mode="%s"><div class="truncate">continue</div></div></div>`, taskID, projectID, inputID, taskID, mode)
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

func TestTaskThreadInputMissingTaskSelectorsUseVocabularyPrefixes(t *testing.T) {
	pending := `<div id="pending-thread-inputs" data-task-id="t-1"></div>`
	var posts int
	c := pendingCLIClient(t, pending, true, &posts)
	cases := []struct {
		line string
		want string
	}{
		{line: "/tasks inputs ", want: "tasks inputs"},
		{line: "/tasks pending ", want: "tasks pending"},
		{line: "/tasks pending-inputs ", want: "tasks pending-inputs"},
		{line: "/tasks inputs cancel ", want: "tasks inputs cancel"},
		{line: "/tasks inputs delete ", want: "tasks inputs cancel"},
		{line: "/tasks inputs remove ", want: "tasks inputs cancel"},
		{line: "/tasks cancel-input ", want: "tasks inputs cancel"},
		{line: "/tasks inputs steer ", want: "tasks inputs steer"},
		{line: "/tasks inputs redirect ", want: "tasks inputs steer"},
		{line: "/tasks steer-queued ", want: "tasks inputs steer"},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			m := New(c)
			m.selectedID, m.selectedName = "p1", "demo"
			m, cmd := typeLine(t, m, tc.line)
			if cmd == nil {
				t.Fatalf("missing selector command for %q", tc.line)
			}
			next, _ := m.Update(cmd())
			m = next.(Model)
			if !m.selectorActive || m.pendingCommand != tc.want || m.selectorPrefillSuffix != " " {
				t.Fatalf("selector state for %q = active:%t pending:%q suffix:%q", tc.line, m.selectorActive, m.pendingCommand, m.selectorPrefillSuffix)
			}
		})
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
