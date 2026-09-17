package terminal

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

// TestTaskReferencePerformanceEvidence is an opt-in, repeatable comparison
// harness for reference-only task resolution. It measures the historical HTML
// board lookup against the compact JSON catalog at 100, 1,000, and 5,000
// cards. Run it with OPENVIBELY_TASK_REFERENCE_PERF_EVIDENCE=1. The fixed
// delay, bandwidth, and run count can be changed with the corresponding
// OPENVIBELY_TASK_REFERENCE_PERF_* variables.
func TestTaskReferencePerformanceEvidence(t *testing.T) {
	if os.Getenv("OPENVIBELY_TASK_REFERENCE_PERF_EVIDENCE") != "1" {
		t.Skip("set OPENVIBELY_TASK_REFERENCE_PERF_EVIDENCE=1 to run the task reference performance harness")
	}

	fixedDelay := time.Duration(statusPerformanceEnvInt(t, "OPENVIBELY_TASK_REFERENCE_PERF_DELAY_MS", 10)) * time.Millisecond
	runs := statusPerformanceEnvInt(t, "OPENVIBELY_TASK_REFERENCE_PERF_RUNS", 7)
	if runs < 3 {
		t.Fatalf("OPENVIBELY_TASK_REFERENCE_PERF_RUNS = %d, want at least 3", runs)
	}
	bandwidth := int64(statusPerformanceEnvInt(t, "OPENVIBELY_TASK_REFERENCE_PERF_BPS", 2000000))
	if bandwidth < 1 {
		t.Fatalf("OPENVIBELY_TASK_REFERENCE_PERF_BPS = %d, want positive", bandwidth)
	}

	for _, cards := range []int{100, 1000, 5000} {
		for _, delay := range []time.Duration{0, fixedDelay} {
			results := make(map[taskReferencePerfMode]taskReferencePerfResult, 2)
			for _, mode := range []taskReferencePerfMode{taskReferencePerfFull, taskReferencePerfCompact} {
				result := measureTaskReferencePerformance(t, mode, cards, delay, 0, runs)
				results[mode] = result
				t.Logf("task_reference_perf mode=%s cards=%d fixed_delay=%s bandwidth_bps=unlimited runs=%d requests=%v response_bytes=%v median=%s p95=%s allocs/op=%.0f allocated_bytes/op=%d identity=%s", mode, cards, delay, runs, result.requests, result.responseBytes, result.median, result.p95, result.allocsPerOp, result.allocatedBytesPerOp, result.identity)
			}

			if cards == 5000 && delay == 0 {
				full, compact := results[taskReferencePerfFull], results[taskReferencePerfCompact]
				if compact.median*4 > full.median*3 {
					t.Fatalf("5,000-card compact median = %s, full median = %s; want at least 25%% lower", compact.median, full.median)
				}
				if compact.responseBytes[len(compact.responseBytes)-1]*4 > full.responseBytes[len(full.responseBytes)-1] {
					t.Fatalf("5,000-card compact response = %d bytes, full response = %d; want at least 75%% lower", compact.responseBytes[len(compact.responseBytes)-1], full.responseBytes[len(full.responseBytes)-1])
				}
			}
			if cards == 100 && delay == fixedDelay {
				full, compact := results[taskReferencePerfFull], results[taskReferencePerfCompact]
				if compact.median > full.median+(full.median/20) {
					t.Fatalf("100-card compact median = %s, full median = %s; compact path regressed by more than 5%%", compact.median, full.median)
				}
			}
		}

		for _, mode := range []taskReferencePerfMode{taskReferencePerfFull, taskReferencePerfCompact} {
			result := measureTaskReferencePerformance(t, mode, cards, 0, bandwidth, runs)
			t.Logf("task_reference_perf mode=%s cards=%d fixed_delay=0 bandwidth_bps=%d runs=%d requests=%v response_bytes=%v median=%s p95=%s allocs/op=%.0f allocated_bytes/op=%d identity=%s", mode, cards, bandwidth, runs, result.requests, result.responseBytes, result.median, result.p95, result.allocsPerOp, result.allocatedBytesPerOp, result.identity)
		}
	}
}

type taskReferencePerfMode string

const (
	taskReferencePerfFull    taskReferencePerfMode = "full-html"
	taskReferencePerfCompact taskReferencePerfMode = "compact-json"
)

type taskReferencePerfResult struct {
	requests            []int
	responseBytes       []int64
	durations           []time.Duration
	median              time.Duration
	p95                 time.Duration
	allocsPerOp         float64
	allocatedBytesPerOp int64
	identity            string
}

type taskReferencePerfServer struct {
	mode          taskReferencePerfMode
	body          []byte
	delay         time.Duration
	bandwidth     int64
	requests      atomic.Int64
	responseBytes atomic.Int64
}

func measureTaskReferencePerformance(t *testing.T, mode taskReferencePerfMode, cards int, delay time.Duration, bandwidth int64, runs int) taskReferencePerfResult {
	t.Helper()
	serverState := newTaskReferencePerfServer(mode, cards, delay, bandwidth)
	srv := httptest.NewServer(serverState)
	defer srv.Close()
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ref := fmt.Sprintf("Task %05d", cards-1)
	wantID := fmt.Sprintf("task-%05d", cards-1)
	result := taskReferencePerfResult{
		requests:      make([]int, 0, runs),
		responseBytes: make([]int64, 0, runs),
		durations:     make([]time.Duration, 0, runs),
	}
	for i := 0; i < runs; i++ {
		serverState.requests.Store(0)
		serverState.responseBytes.Store(0)
		start := time.Now()
		task, err := lookupTaskReferencePerformance(c, mode, ref)
		result.durations = append(result.durations, time.Since(start))
		if err != nil {
			t.Fatalf("%s %d-card lookup %d failed: %v", mode, cards, i+1, err)
		}
		if task.ID != wantID {
			t.Fatalf("%s %d-card lookup identity = %q, want %q", mode, cards, task.ID, wantID)
		}
		result.identity = task.ID
		result.requests = append(result.requests, int(serverState.requests.Load()))
		result.responseBytes = append(result.responseBytes, serverState.responseBytes.Load())
	}
	for i, requests := range result.requests {
		if requests != 1 {
			t.Fatalf("%s %d-card lookup %d made %d requests, want exactly one", mode, cards, i+1, requests)
		}
	}
	result.median, result.p95 = taskReferencePerfPercentiles(result.durations)
	result.allocsPerOp = testing.AllocsPerRun(3, func() {
		if _, err := lookupTaskReferencePerformance(c, mode, ref); err != nil {
			t.Fatalf("%s allocation lookup failed: %v", mode, err)
		}
	})
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	const allocationRuns = 3
	for i := 0; i < allocationRuns; i++ {
		if _, err := lookupTaskReferencePerformance(c, mode, ref); err != nil {
			t.Fatalf("%s allocated-byte lookup failed: %v", mode, err)
		}
	}
	runtime.ReadMemStats(&after)
	result.allocatedBytesPerOp = int64(after.TotalAlloc-before.TotalAlloc) / allocationRuns
	return result
}

func newTaskReferencePerfServer(mode taskReferencePerfMode, cards int, delay time.Duration, bandwidth int64) *taskReferencePerfServer {
	full, compact := taskReferencePerfBodies(cards)
	body := full
	if mode == taskReferencePerfCompact {
		body = compact
	}
	return &taskReferencePerfServer{mode: mode, body: body, delay: delay, bandwidth: bandwidth}
}

func (s *taskReferencePerfServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	wantPath := "/tasks"
	if s.mode == taskReferencePerfCompact {
		wantPath = "/api/tasks/reference-catalog"
	}
	if r.URL.Path != wantPath {
		http.NotFound(w, r)
		return
	}
	s.requests.Add(1)
	s.responseBytes.Add(int64(len(s.body)))
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	if s.mode == taskReferencePerfCompact {
		w.Header().Set("Content-Type", "application/json")
	} else {
		w.Header().Set("Content-Type", "text/html")
	}
	if s.bandwidth < 1 {
		_, _ = w.Write(s.body)
		return
	}
	const chunkSize = 32 * 1024
	for offset := 0; offset < len(s.body); {
		start := offset
		end := offset + chunkSize
		if end > len(s.body) {
			end = len(s.body)
		}
		_, _ = w.Write(s.body[start:end])
		offset = end
		if offset < len(s.body) {
			delay := time.Duration(int64(end-start) * int64(time.Second) / s.bandwidth)
			if delay > 0 {
				time.Sleep(delay)
			}
		}
	}
}

func lookupTaskReferencePerformance(c *client.Client, mode taskReferencePerfMode, ref string) (client.Task, error) {
	var (
		tasks []client.Task
		err   error
	)
	if mode == taskReferencePerfFull {
		tasks, err = c.ListTasks(context.Background(), "p1")
	} else {
		tasks, err = c.ListTaskReferences(context.Background(), "p1")
	}
	if err != nil {
		return client.Task{}, err
	}
	return matchRef(tasks, ref,
		func(task client.Task) string { return task.ID },
		func(task client.Task) string { return task.Title })
}

func taskReferencePerfBodies(cards int) ([]byte, []byte) {
	tasks := make([]client.Task, cards)
	var board strings.Builder
	for i := 0; i < cards; i++ {
		id := fmt.Sprintf("task-%05d", i)
		title := fmt.Sprintf("Task %05d", i)
		task := client.Task{
			ID: id, ProjectID: "p1", Title: title, Prompt: "prompt",
			Category: "active", Status: "queued", DisplayOrder: i, Badges: []string{"Goal"},
		}
		tasks[i] = task
		fmt.Fprintf(&board, `<div class="card" data-task-id="%s" data-task-status="%s" data-task-category="%s" data-display-order="%d"><a href="/tasks/%s" title="%s">%s</a><p class="line-clamp-2">%s</p></div>`, id, task.Status, task.Category, i, id, title, title, task.Prompt)
	}
	if cards == 5000 && board.Len() < 3428940 {
		board.WriteString(strings.Repeat(" ", 3428940-board.Len()))
	}
	compact, _ := json.Marshal(struct {
		Tasks []client.Task `json:"tasks"`
	}{Tasks: tasks})
	return []byte(board.String()), compact
}

func taskReferencePerfPercentiles(values []time.Duration) (time.Duration, time.Duration) {
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	if len(sorted) == 0 {
		return 0, 0
	}
	p95Index := int(math.Ceil(float64(len(sorted))*0.95)) - 1
	if p95Index < 0 {
		p95Index = 0
	}
	if p95Index >= len(sorted) {
		p95Index = len(sorted) - 1
	}
	return sorted[len(sorted)/2], sorted[p95Index]
}
