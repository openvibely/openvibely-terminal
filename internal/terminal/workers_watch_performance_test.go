package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/openvibely/openvibely-terminal/internal/client"
)

func TestWorkersWatchCombinedSnapshotTenMinuteRequestBudgetAndFreshness(t *testing.T) {
	const refreshes = 200 // 600 seconds at the user-visible 3 second refresh interval.
	const optimizedDelay = 2 * time.Millisecond
	const baselineDelay = 12 * time.Millisecond

	var optimizedState, optimizedRequests atomic.Int32
	optimizedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		optimizedRequests.Add(1)
		if r.URL.Path != "/api/capacity/snapshot" {
			http.NotFound(w, r)
			return
		}
		time.Sleep(optimizedDelay)
		n := optimizedState.Load()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"global":{"max_workers":8,"total_running":%d,"queue_size":1},"projects":[{"id":"p1","name":"Demo","running":%d,"queue_size":2,"max_workers":4}],"models":[{"id":"m1","name":"Sonnet","model":"claude-sonnet","running":%d,"max_workers":5}]}`, n, n, n)
	}))
	t.Cleanup(optimizedServer.Close)
	optimizedClient, err := client.New(optimizedServer.URL)
	if err != nil {
		t.Fatal(err)
	}

	var baselineState, baselineRequests atomic.Int32
	baselineServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		baselineRequests.Add(1)
		time.Sleep(baselineDelay)
		n := baselineState.Load()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/capacity/global":
			_, _ = fmt.Fprintf(w, `{"max_workers":8,"total_running":%d,"queue_size":1}`, n)
		case "/api/capacity/projects":
			_, _ = fmt.Fprintf(w, `[{"id":"p1","name":"Demo","running":%d,"queue_size":2,"max_workers":4}]`, n)
		case "/api/capacity/models":
			_, _ = fmt.Fprintf(w, `[{"id":"m1","name":"Sonnet","model":"claude-sonnet","running":%d,"max_workers":5}]`, n)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(baselineServer.Close)
	baselineClient, err := client.New(baselineServer.URL)
	if err != nil {
		t.Fatal(err)
	}

	optimizedLatencies := make([]time.Duration, 0, refreshes)
	baselineLatencies := make([]time.Duration, 0, refreshes)
	for i := 1; i <= refreshes; i++ {
		optimizedState.Store(int32(i))
		changedAt := time.Now()
		overview, err := fetchWorkersOverview(context.Background(), optimizedClient)
		if err != nil {
			t.Fatalf("optimized refresh %d: %v", i, err)
		}
		displayed := renderWorkers(overview)
		optimizedLatencies = append(optimizedLatencies, time.Since(changedAt))
		if !strings.Contains(displayed, fmt.Sprintf("%d / 8", i)) || !strings.Contains(displayed, "Demo") || !strings.Contains(displayed, "Sonnet") {
			t.Fatalf("optimized refresh %d omitted changed/global/project/model capacity:\n%s", i, displayed)
		}

		baselineState.Store(int32(i))
		changedAt = time.Now()
		baselineOverview, err := fetchLegacyWorkersOverview(context.Background(), baselineClient)
		if err != nil {
			t.Fatalf("baseline refresh %d: %v", i, err)
		}
		displayed = renderWorkers(baselineOverview)
		baselineLatencies = append(baselineLatencies, time.Since(changedAt))
		if !strings.Contains(displayed, fmt.Sprintf("%d / 8", i)) || !strings.Contains(displayed, "Demo") || !strings.Contains(displayed, "Sonnet") {
			t.Fatalf("baseline refresh %d omitted changed/global/project/model capacity:\n%s", i, displayed)
		}
	}

	optimizedP95 := durationPercentile(optimizedLatencies, 0.95)
	baselineP95 := durationPercentile(baselineLatencies, 0.95)
	if got := optimizedRequests.Load(); got != refreshes {
		t.Fatalf("combined endpoint requests in ten-minute session = %d, want <= %d", got, refreshes)
	}
	if got, want := baselineRequests.Load(), int32(refreshes*3); got != want {
		t.Fatalf("legacy endpoint requests in ten-minute session = %d, want baseline %d", got, want)
	}
	if optimizedP95 > 3*time.Second {
		t.Fatalf("changed-snapshot-to-display p95 = %s, want <= 3s", optimizedP95)
	}
	if optimizedP95 > baselineP95 {
		t.Fatalf("optimized changed-snapshot-to-display p95 = %s, slower than baseline %s", optimizedP95, baselineP95)
	}
	t.Logf("equivalent 10-minute session: optimized requests=%d, baseline requests=%d; changed-snapshot-to-display p95 optimized=%s, baseline=%s", optimizedRequests.Load(), baselineRequests.Load(), optimizedP95, baselineP95)
}

func durationPercentile(values []time.Duration, percentile float64) time.Duration {
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	index := int(float64(len(ordered))*percentile+0.999999) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(ordered) {
		index = len(ordered) - 1
	}
	return ordered[index]
}

func TestFetchWorkersOverviewFallsBackAndPreservesPartialWarnings(t *testing.T) {
	var snapshotRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/capacity/snapshot" {
			snapshotRequests.Add(1)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/capacity/global":
			_, _ = w.Write([]byte(`{"max_workers":4,"total_running":1}`))
		case "/api/capacity/projects":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"projects unavailable"}`))
		case "/api/capacity/models":
			_, _ = w.Write([]byte(`not-json`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	c, err := client.New(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		overview, err := fetchWorkersOverview(context.Background(), c)
		if err != nil {
			t.Fatalf("fetchWorkersOverview: %v", err)
		}
		if overview.Global == nil || overview.Global.TotalRunning != 1 || len(overview.Projects) != 0 || len(overview.Models) != 0 {
			t.Fatalf("fallback overview lost available global capacity: %+v", overview)
		}
		if overview.ModelsAvailable || len(overview.Warnings) != 2 || overview.Warnings[0] != "project worker capacity unavailable" || overview.Warnings[1] != "model worker capacity unavailable" {
			t.Fatalf("fallback partial-source warnings = %+v, models available=%t", overview.Warnings, overview.ModelsAvailable)
		}
	}
	if got := snapshotRequests.Load(); got != 1 {
		t.Fatalf("old-server snapshot probes = %d, want one cached probe", got)
	}
}

func TestWorkersWatchCombinedSnapshotUnchangedDisplayAndRepeatedStartStop(t *testing.T) {
	previousTick := workersLiveTick
	workersLiveTick = func(_ time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
		return func() tea.Msg { return fn(time.Now()) }
	}
	t.Cleanup(func() { workersLiveTick = previousTick })

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/api/capacity/snapshot" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"global":{"max_workers":4,"total_running":1},"projects":[{"id":"p1","name":"Demo","running":1,"max_workers":2}],"models":[{"name":"Sonnet","model":"claude-sonnet","running":1,"max_workers":3}]}`))
	}))
	t.Cleanup(server.Close)
	c, err := client.New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	defer m.Cleanup()

	for start := 0; start < 2; start++ {
		m, cmd := m.beginWorkersLive()
		if cmd == nil || !m.workersLiveActive {
			t.Fatalf("start %d did not begin live workers: active=%t cmd=%v", start, m.workersLiveActive, cmd)
		}
		next, tick := m.Update(cmd())
		m = next.(Model)
		if tick == nil || !strings.Contains(stripANSI(transcript(m)), "Demo") || !strings.Contains(stripANSI(transcript(m)), "Sonnet") {
			t.Fatalf("start %d did not display global/project/model data:\n%s", start, stripANSI(transcript(m)))
		}
		oldTranscript := m.transcriptContent
		next, fetch := m.Update(tick())
		m = next.(Model)
		if fetch == nil {
			t.Fatalf("start %d did not schedule a fetch after its tick", start)
		}
		next, tick = m.Update(fetch())
		m = next.(Model)
		if m.transcriptContent != oldTranscript {
			t.Fatalf("unchanged refresh %d replaced the transcript", start)
		}
		if tick == nil {
			t.Fatalf("unchanged refresh %d stopped the watch", start)
		}
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = next.(Model)
		if m.workersLiveActive {
			t.Fatalf("stop %d left live workers active", start)
		}
	}
	if got := requests.Load(); got != 4 {
		t.Fatalf("combined snapshot requests across two start/stop sessions = %d, want 4", got)
	}
}

func TestWorkersWatchSupportedSnapshotDoesNotFallbackOnAuthFailure(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/api/capacity/snapshot" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		t.Errorf("auth failure should not fall back to %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	c, err := client.New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fetchWorkersOverview(context.Background(), c); !client.IsAuthRequired(err) {
		t.Fatalf("fetchWorkersOverview error = %v, want authentication error", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests after snapshot auth failure = %d, want one", got)
	}
}

type workersWatchLatencyWriter struct {
	bytes.Buffer
	cancel    context.CancelFunc
	lines     int
	changedAt *atomic.Int64
	latency   atomic.Int64
}

func (w *workersWatchLatencyWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	w.lines += bytes.Count(p, []byte("\n"))
	if w.lines >= 2 && w.cancel != nil {
		changedAt := w.changedAt.Load()
		if changedAt != 0 {
			w.latency.Store(time.Since(time.Unix(0, changedAt)).Nanoseconds())
		}
		w.cancel()
		w.cancel = nil
	}
	return n, err
}

func TestCLIWorkersWatchDisplaysCombinedCapacityChangeWithinThreeSeconds(t *testing.T) {
	previousInterval := workersLiveRefreshInterval
	workersLiveRefreshInterval = 100 * time.Millisecond
	t.Cleanup(func() { workersLiveRefreshInterval = previousInterval })

	var requests atomic.Int32
	var changedAt atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/capacity/snapshot" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
			return
		}
		request := requests.Add(1)
		running := request
		if request == 2 {
			changedAt.Store(time.Now().UnixNano())
			time.Sleep(10 * time.Millisecond)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"global":{"max_workers":4,"total_running":%d},"projects":[{"id":"p1","name":"Demo","running":%d,"max_workers":2}],"models":[{"name":"Sonnet","model":"claude-sonnet","running":%d,"max_workers":3}]}`, running, running, running)
	}))
	t.Cleanup(server.Close)
	c, err := client.New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := &workersWatchLatencyWriter{cancel: cancel, changedAt: &changedAt}
	if err := RunCLIContext(ctx, c, out, "", []string{"workers", "watch"}, false, true); err != nil {
		t.Fatalf("CLI workers watch returned error after cancellation: %v", err)
	}
	if latency := time.Duration(out.latency.Load()); latency > 3*time.Second || latency <= 0 {
		t.Fatalf("CLI changed-snapshot-to-display latency = %s, want >0 and <=3s", latency)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("CLI workers watch emitted %d JSON snapshots, want 2:\n%s", len(lines), out.String())
	}
	var snapshot struct {
		Workers []workerCapacityRow      `json:"workers"`
		Models  []modelWorkerCapacityRow `json:"models"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &snapshot); err != nil {
		t.Fatalf("changed CLI snapshot is invalid JSON: %v\n%s", err, lines[1])
	}
	if len(snapshot.Workers) != 2 || snapshot.Workers[0].Running != 2 || snapshot.Workers[1].Running != 2 || len(snapshot.Models) != 1 || snapshot.Models[0].Running != 2 {
		t.Fatalf("CLI watch did not display changed global/project/model values: %+v", snapshot)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("combined snapshot requests = %d, want 2", got)
	}
}
