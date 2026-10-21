package terminal

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

const mutationBoardFixture = `<div class="kanban">
<div data-task-id="t-2" data-task-status="running" data-task-category="active" data-display-order="0"><a href="/tasks/t-2" title="Beta task">Beta task</a><span class="badge">Goal</span></div>
<div data-task-id="t-1" data-task-status="pending" data-task-category="active" data-display-order="1"><a href="/tasks/t-1" title="Alpha task">Alpha task</a></div>
<div data-task-id="t-3" data-task-status="completed" data-task-category="completed" data-display-order="0"><a href="/tasks/t-3" title="Finished task">Finished task</a></div>
</div>`

type taskMutationRequestRecorder struct {
	mu       sync.Mutex
	requests map[string]int
	bytes    int64
}

func (r *taskMutationRequestRecorder) record(method, path string, n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.requests == nil {
		r.requests = make(map[string]int)
	}
	r.requests[method+" "+path]++
	r.bytes += int64(n)
}

func (r *taskMutationRequestRecorder) count(method, path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests[method+" "+path]
}

func (r *taskMutationRequestRecorder) snapshot() (map[string]int, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := make(map[string]int, len(r.requests))
	for key, count := range r.requests {
		copy[key] = count
	}
	return copy, r.bytes
}

func (r *taskMutationRequestRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = make(map[string]int)
	r.bytes = 0
}

func taskMutationServer(t *testing.T, mutationBody string, mutationStatus int, refreshStatus int, delay time.Duration, rec *taskMutationRequestRecorder) Model {
	t.Helper()
	catalogReads := 0
	return newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		var body string
		status := http.StatusOK
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/tasks/reference-catalog":
			catalogReads++
			if catalogReads > 1 {
				status = refreshStatus
			}
			body = compactTaskCatalogForProjectTest(mutationBoardFixture, "p1")
			w.Header().Set("Content-Type", "application/json")
		case r.Method == http.MethodGet && r.URL.Path == "/tasks":
			status = refreshStatus
			body = mutationBoardFixture
		case r.URL.Path == "/tasks" || strings.HasPrefix(r.URL.Path, "/tasks/") || r.URL.Path == "/tasks/move-completed" || r.URL.Path == "/tasks/completed" || r.URL.Path == "/tasks/backlog" || r.URL.Path == "/tasks/backlog/activate":
			status = mutationStatus
			body = mutationBody
		default:
			http.NotFound(w, r)
			return
		}
		if rec != nil {
			rec.record(r.Method, r.URL.Path, len(body))
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
		}
		if body != "" && status != http.StatusNoContent {
			_, _ = io.WriteString(w, body)
		}
	})
}

func TestTaskMutationsRefreshCompactReferenceCatalog(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		method     string
		path       string
		confirm    bool
		resolution bool
		status     string
	}{
		{name: "new", line: "/tasks new New task | Make it happen", method: http.MethodPost, path: "/tasks", status: "created New task"},
		{name: "move", line: "/tasks move Alpha task backlog", method: http.MethodPatch, path: "/tasks/t-1/category", resolution: true, status: "Alpha task → backlog"},
		{name: "order", line: "/tasks order Alpha task 2", method: http.MethodPatch, path: "/tasks/t-1/reorder", resolution: true, status: "moved Alpha task to position 2"},
		{name: "stop", line: "/tasks stop Alpha task", method: http.MethodPost, path: "/tasks/t-1/cancel", resolution: true, status: "stop: Alpha task"},
		{name: "activate", line: "/tasks activate", method: http.MethodPost, path: "/tasks/backlog/activate", status: "activated the backlog"},
		{name: "sweep", line: "/tasks sweep", method: http.MethodPost, path: "/tasks/move-completed", status: "swept finished tasks"},
		{name: "clear completed", line: "/tasks clear completed", method: http.MethodDelete, path: "/tasks/completed", confirm: true, status: "cleared completed"},
		{name: "clear backlog", line: "/tasks clear backlog", method: http.MethodDelete, path: "/tasks/backlog", confirm: true, status: "cleared backlog"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &taskMutationRequestRecorder{}
			m := taskMutationServer(t, mutationBoardFixture, http.StatusOK, http.StatusOK, 0, rec)
			m = runLine(t, m, tc.line)
			if tc.confirm {
				m = runLine(t, m, "yes")
			}
			if got := rec.count(tc.method, tc.path); got != 1 {
				t.Fatalf("mutation requests = %d, want 1", got)
			}
			if got := rec.count(http.MethodGet, "/tasks"); got != 0 {
				t.Fatalf("full HTML board GET requests = %d, want 0", got)
			}
			wantCatalogReads := 1
			if tc.resolution {
				wantCatalogReads++
			}
			if got := rec.count(http.MethodGet, "/api/tasks/reference-catalog"); got != wantCatalogReads {
				t.Fatalf("compact catalog GET requests = %d, want %d", got, wantCatalogReads)
			}
			out := transcript(m)
			if !strings.Contains(out, tc.status) || !strings.Contains(out, "Alpha task") || !strings.Contains(out, "Beta task") {
				t.Fatalf("mutation status or board was not rendered:\n%s", out)
			}
			for _, marker := range []string{"Backlog (0)", "Active (2)", "Completed (1)", "▶ running", "[Goal]"} {
				if !strings.Contains(out, marker) {
					t.Fatalf("rendered output omitted %q:\n%s", marker, out)
				}
			}
			activeStart := strings.Index(out, "Active (")
			if activeStart < 0 || strings.Index(out[activeStart:], "Beta task") > strings.Index(out[activeStart:], "Alpha task") {
				t.Fatalf("board order changed from the server's ordered response:\n%s", out)
			}
		})
	}
}

func TestTaskMutationRefreshAlwaysUsesCompactCatalog(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		status int
	}{
		{name: "empty 200", status: http.StatusOK},
		{name: "204", status: http.StatusNoContent},
		{name: "task detail fragment", body: `<div id="task-detail-content"><div id="tab-details">updated</div></div>`, status: http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &taskMutationRequestRecorder{}
			m := taskMutationServer(t, tc.body, tc.status, http.StatusOK, 0, rec)
			m = runLine(t, m, "/tasks new New task")
			if got := rec.count(http.MethodGet, "/tasks"); got != 0 || rec.count(http.MethodGet, "/api/tasks/reference-catalog") != 1 {
				requests, _ := rec.snapshot()
				t.Fatalf("mutation refresh requests = %+v, want one compact catalog GET and no HTML board GET", requests)
			}
			if out := transcript(m); !strings.Contains(out, "created New task") || !strings.Contains(out, "Alpha task") {
				t.Fatalf("fallback output lost mutation status or board:\n%s", out)
			}
		})
	}

	t.Run("task details update fragment falls back", func(t *testing.T) {
		rec := &taskMutationRequestRecorder{}
		m := taskMutationServer(t, `<div id="task-detail-content"><div id="tab-details">updated</div></div>`, http.StatusOK, http.StatusOK, 0, rec)
		m = runLine(t, m, "/tasks edit Alpha task | Renamed task")
		if rec.count(http.MethodPut, "/tasks/t-1") != 1 || rec.count(http.MethodGet, "/tasks") != 0 || rec.count(http.MethodGet, "/api/tasks/reference-catalog") != 2 {
			requests, _ := rec.snapshot()
			t.Fatalf("edit did not refresh the compact catalog after reference resolution: %+v", requests)
		}
		if !strings.Contains(transcript(m), "updated Renamed task") {
			t.Fatalf("edit success status missing: %s", transcript(m))
		}
	})

	t.Run("run task details fragment falls back", func(t *testing.T) {
		rec := &taskMutationRequestRecorder{}
		m := taskMutationServer(t, `<div id="task-detail-content"><div id="tab-details">running</div></div>`, http.StatusOK, http.StatusOK, 0, rec)
		m = runLine(t, m, "/tasks run Alpha task")
		if rec.count(http.MethodPost, "/tasks/t-1/run") != 1 || rec.count(http.MethodGet, "/tasks") != 0 || rec.count(http.MethodGet, "/api/tasks/reference-catalog") != 2 {
			requests, _ := rec.snapshot()
			t.Fatalf("run task did not refresh the compact catalog after reference resolution: %+v", requests)
		}
	})

	t.Run("activate non-board fragment falls back", func(t *testing.T) {
		rec := &taskMutationRequestRecorder{}
		m := taskMutationServer(t, `<div id="task-detail-content">activated</div>`, http.StatusOK, http.StatusOK, 0, rec)
		m = runLine(t, m, "/tasks activate")
		if rec.count(http.MethodPost, "/tasks/backlog/activate") != 1 || rec.count(http.MethodGet, "/tasks") != 0 || rec.count(http.MethodGet, "/api/tasks/reference-catalog") != 1 {
			requests, _ := rec.snapshot()
			t.Fatalf("activate did not refresh the compact catalog: %+v", requests)
		}
	})

	t.Run("empty board fragment still refreshes catalog", func(t *testing.T) {
		rec := &taskMutationRequestRecorder{}
		const emptyBoard = `<div class="kanban"><div class="kanban-columns"><section data-category="backlog"></section><section data-category="active"></section><section data-category="completed"></section></div></div>`
		m := taskMutationServer(t, emptyBoard, http.StatusOK, http.StatusOK, 0, rec)
		m = runLine(t, m, "/tasks new New task")
		if got := rec.count(http.MethodGet, "/tasks"); got != 0 || rec.count(http.MethodGet, "/api/tasks/reference-catalog") != 1 {
			requests, _ := rec.snapshot()
			t.Fatalf("empty fragment refresh requests = %+v, want one compact catalog GET", requests)
		}
		out := transcript(m)
		if !strings.Contains(out, "created New task") || !strings.Contains(out, "Alpha task") {
			t.Fatalf("mutation output did not use the refreshed catalog: %s", out)
		}
	})

	t.Run("failed catalog refresh preserves successful mutation", func(t *testing.T) {
		rec := &taskMutationRequestRecorder{}
		m := taskMutationServer(t, "", http.StatusNoContent, http.StatusInternalServerError, 0, rec)
		m = runLine(t, m, "/tasks move Alpha task backlog")
		out := transcript(m)
		if !strings.Contains(out, "Alpha task → backlog") || strings.Contains(out, "500") {
			t.Fatalf("successful mutation result was replaced by catalog refresh failure:\n%s", out)
		}
		if got := rec.count(http.MethodGet, "/api/tasks/reference-catalog"); got != 2 {
			t.Fatalf("catalog reads = %d, want resolution plus failed refresh", got)
		}
	})
}

func TestTaskMutationRenderUsesCatalogRegardlessOfFragment(t *testing.T) {
	run := func(response string, status int) string {
		t.Helper()
		rec := &taskMutationRequestRecorder{}
		m := taskMutationServer(t, response, status, http.StatusOK, 0, rec)
		m = runLine(t, m, "/tasks new New task")
		out := transcript(m)
		start := strings.Index(out, "result:Tasks:")
		if start < 0 {
			t.Fatalf("task result missing from transcript: %s", out)
		}
		return out[start:]
	}
	fromMutation := run(mutationBoardFixture, http.StatusOK)
	fromFallback := run("", http.StatusNoContent)
	if fromMutation != fromFallback {
		t.Fatalf("rendered board differs between mutation response and GET fallback\nmutation:\n%s\nfallback:\n%s", fromMutation, fromFallback)
	}
}

func makeSyntheticTaskBoard(count int) string {
	var board strings.Builder
	board.Grow(count * 220)
	board.WriteString(`<div class="kanban">`)
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("task-%04d", i)
		title := fmt.Sprintf("Synthetic task %04d", i)
		category := "active"
		if i%3 == 0 {
			category = "backlog"
		} else if i%3 == 1 {
			category = "completed"
		}
		fmt.Fprintf(&board, `<div class="card" data-task-id="%s" data-task-status="pending" data-task-category="%s" data-display-order="%d"><a href="/tasks/%s" title="%s">%s</a><p class="line-clamp-2">Synthetic prompt for card %d</p><span class="badge">Goal</span></div>`, id, category, i, id, title, title, i)
	}
	board.WriteString(`</div>`)
	return board.String()
}

func TestTaskMutationBoardLatencyAndBytes(t *testing.T) {
	const serverDelay = 20 * time.Millisecond
	board := makeSyntheticTaskBoard(500)
	var mu sync.Mutex
	var requestCount int
	var bytesWritten int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(serverDelay)
		mu.Lock()
		requestCount++
		mu.Unlock()
		_, _ = io.WriteString(w, board)
		mu.Lock()
		bytesWritten += int64(len(board))
		mu.Unlock()
	}))
	defer server.Close()

	// The old flow drained the mutation fragment, then fetched and rendered the
	// full board. Keep this raw request unparsed to model that path accurately.
	form := url.Values{"title": {"Synthetic task"}, "prompt": {"Synthetic prompt"}, "category": {"backlog"}}
	httpClient := &http.Client{}
	c, err := client.New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	const samples = 3
	var baselineTotal, optimizedTotal time.Duration
	for i := 0; i < samples; i++ {
		request, err := http.NewRequest(http.MethodPost, server.URL+"/tasks?project_id=p1", strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("HX-Request", "true")
		request.Header.Set("Accept", "text/html, application/json")
		start := time.Now()
		response, err := httpClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		tasks, err := c.ListTasks(context.Background(), "p1")
		if err != nil {
			t.Fatal(err)
		}
		_ = renderBoard(tasks, "")
		baselineTotal += time.Since(start)
	}
	mu.Lock()
	baselineRequests, baselineBytes := requestCount, bytesWritten
	requestCount, bytesWritten = 0, 0
	mu.Unlock()

	for i := 0; i < samples; i++ {
		start := time.Now()
		mutation, err := c.CreateTaskWithBoard(context.Background(), "p1", client.TaskForm{Title: "Synthetic task", Prompt: "Synthetic prompt", Category: "backlog"})
		if err != nil {
			t.Fatal(err)
		}
		if !mutation.HasBoard {
			t.Fatal("synthetic mutation response did not detect the board")
		}
		_ = renderBoard(mutation.Tasks, "")
		optimizedTotal += time.Since(start)
	}
	baseline := baselineTotal / samples
	optimized := optimizedTotal / samples
	mu.Lock()
	optimizedRequests, optimizedBytes := requestCount, bytesWritten
	mu.Unlock()

	if baselineRequests != 2*samples || optimizedRequests != samples {
		t.Fatalf("request counts baseline=%d optimized=%d, want %d and %d", baselineRequests, optimizedRequests, 2*samples, samples)
	}
	if baselineBytes != int64(2*samples*len(board)) || optimizedBytes != int64(samples*len(board)) {
		t.Fatalf("response bytes baseline=%d optimized=%d payload=%d", baselineBytes, optimizedBytes, len(board))
	}
	reduction := 1 - float64(optimized)/float64(baseline)
	t.Logf("500-card task board: baseline=%s optimized=%s latency reduction=%.1f%%; bytes baseline=%d optimized=%d (one payload saved per mutation)", baseline, optimized, reduction*100, baselineBytes, optimizedBytes)
	if reduction < 0.40 {
		t.Fatalf("latency reduction = %.1f%%, want at least 40%%", reduction*100)
	}
}
