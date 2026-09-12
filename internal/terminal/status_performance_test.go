package terminal

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

// TestStatusPerformanceEvidence is an opt-in, reproducible comparison harness
// for the status-count optimization. It is intentionally skipped during normal
// test runs because the full-list mode models the historical 5,000-card
// response. Run it with OPENVIBELY_STATUS_PERF_EVIDENCE=1. The same test file
// can be injected into the historical baseline commit so both modes exercise
// the real RunCLI orchestration and the same fixed-delay server.
func TestStatusPerformanceEvidence(t *testing.T) {
	if os.Getenv("OPENVIBELY_STATUS_PERF_EVIDENCE") != "1" {
		t.Skip("set OPENVIBELY_STATUS_PERF_EVIDENCE=1 to run the status performance harness")
	}
	mode := strings.TrimSpace(os.Getenv("OPENVIBELY_STATUS_PERF_MODE"))
	if mode != "compact" && mode != "full" {
		t.Fatalf("OPENVIBELY_STATUS_PERF_MODE = %q, want compact or full", mode)
	}
	cards := statusPerformanceEnvInt(t, "OPENVIBELY_STATUS_PERF_CARDS", 100)
	if cards < 1 {
		t.Fatalf("OPENVIBELY_STATUS_PERF_CARDS = %d, want positive", cards)
	}
	delay := time.Duration(statusPerformanceEnvInt(t, "OPENVIBELY_STATUS_PERF_DELAY_MS", 10)) * time.Millisecond
	runs := statusPerformanceEnvInt(t, "OPENVIBELY_STATUS_PERF_RUNS", 7)
	if runs < 3 {
		t.Fatalf("OPENVIBELY_STATUS_PERF_RUNS = %d, want at least 3", runs)
	}

	var collectionBytes atomic.Int64
	var alertsBody, tasksBody string
	if mode == "full" {
		alertsBody, tasksBody = statusPerformanceFullCollections(cards)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		var body string
		switch r.URL.Path {
		case "/api/projects":
			body = `{"projects":[{"id":"p1","name":"demo"}]}`
		case "/api/capacity/global":
			body = `{"total_running":1,"max_workers":4,"available_slots":3}`
		case "/auth/me":
			body = `{"authenticated":true,"username":"operator"}`
		case "/api/alerts/pending-count":
			if mode != "compact" {
				http.NotFound(w, r)
				return
			}
			body = `{"count":2500}`
		case "/api/tasks/status-counts":
			if mode != "compact" {
				http.NotFound(w, r)
				return
			}
			body = `{"active_tasks":5000,"queued_tasks":5000}`
		case "/alerts":
			if mode != "full" {
				http.NotFound(w, r)
				return
			}
			body = alertsBody
		case "/tasks":
			if mode != "full" {
				http.NotFound(w, r)
				return
			}
			body = tasksBody
		default:
			body = `{}`
		}
		if r.URL.Path == "/alerts" || r.URL.Path == "/tasks" || r.URL.Path == "/api/alerts/pending-count" || r.URL.Path == "/api/tasks/status-counts" {
			collectionBytes.Add(int64(len(body)))
		}
		if strings.HasPrefix(body, "{") || strings.HasPrefix(body, "[") {
			w.Header().Set("Content-Type", "application/json")
		} else {
			w.Header().Set("Content-Type", "text/html")
		}
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	durations := make([]time.Duration, 0, runs)
	bytesPerRun := make([]int64, 0, runs)
	for i := 0; i < runs; i++ {
		collectionBytes.Store(0)
		start := time.Now()
		if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"status"}, false, false); err != nil {
			t.Fatalf("status run %d failed: %v", i+1, err)
		}
		durations = append(durations, time.Since(start))
		bytesPerRun = append(bytesPerRun, collectionBytes.Load())
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	median := durations[len(durations)/2]
	if mode == "full" && cards == 5000 {
		if got := bytesPerRun[len(bytesPerRun)-1]; got != 1724476 {
			t.Fatalf("full 5,000-card collection bytes = %d, want reviewed baseline 1,724,476", got)
		}
	}
	t.Logf("status_perf mode=%s cards=%d fixed_delay=%s runs=%d collection_bytes=%d bytes_per_run=%v median=%s durations=%v", mode, cards, delay, runs, bytesPerRun[len(bytesPerRun)-1], bytesPerRun, median, durations)
}

func statusPerformanceEnvInt(t *testing.T, key string, fallback int) int {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		t.Fatalf("%s = %q: %v", key, value, err)
	}
	return parsed
}

func statusPerformanceFullCollections(cards int) (string, string) {
	var alerts, tasks strings.Builder
	for i := 0; i < cards; i++ {
		fmt.Fprintf(&alerts, `<div class="card" data-alert-id="a-%05d" data-alert-scroll-anchor="a-%05d"><span class="badge">pending</span><p>Alert %05d requires review.</p></div>`+"\n", i, i, i)
		fmt.Fprintf(&tasks, `<div class="card" data-task-id="t-%05d" data-task-status="queued" data-task-category="active"><p>Task %05d is queued.</p></div>`+"\n", i, i)
	}
	if cards == 5000 {
		const reviewedBaselineBytes = 1724476
		padding := reviewedBaselineBytes - alerts.Len() - tasks.Len()
		if padding > 0 {
			tasks.WriteString(strings.Repeat(" ", padding))
		}
	}
	return alerts.String(), tasks.String()
}
