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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/openvibely/openvibely-terminal/internal/client"
)

func workersWatchCapacityHTML(globalRunning, projectRunning, modelRunning int) string {
	return fmt.Sprintf(`<div id="worker-settings-content">
<table><tbody id="project-stats-tbody">
<tr id="global-row"><td>Global</td><td>All Projects</td><td>%d / 8</td><td>1</td><td><input name="max_workers" id="limit-input-global" value="8"></td><td>Active</td></tr>
<tr id="project-row-p1"><td>Project</td><td>Demo</td><td>%d / 4</td><td>2</td><td><input name="max_workers" id="limit-input-p1" value="4"></td><td>Active</td></tr>
</tbody></table>
<table><tbody id="model-stats-tbody"><tr><td><div>Sonnet</div><div>claude-sonnet</div></td><td>%d / 5</td><td>5</td><td>Active</td></tr></tbody></table>
</div>`, globalRunning, projectRunning, modelRunning)
}

type workersWatchSessionWriter struct {
	bytes.Buffer
	cancel    context.CancelFunc
	changedAt *sync.Map
	latencies []time.Duration
}

func (w *workersWatchSessionWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	if err != nil {
		return n, err
	}
	for _, line := range bytes.Split(p, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var snapshot struct {
			Workers []workerCapacityRow `json:"workers"`
		}
		if err := json.Unmarshal(line, &snapshot); err != nil || len(snapshot.Workers) == 0 {
			continue
		}
		if changedAt, ok := w.changedAt.Load(snapshot.Workers[0].Running); ok {
			w.latencies = append(w.latencies, time.Since(time.Unix(0, changedAt.(int64))))
		}
		if w.cancel != nil && bytes.Count(w.Bytes(), []byte("\n")) >= 200 {
			w.cancel()
			w.cancel = nil
		}
	}
	return n, nil
}

func runWorkersWatchSession(t *testing.T, c *client.Client, changedAt *sync.Map) *workersWatchSessionWriter {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &workersWatchSessionWriter{cancel: cancel, changedAt: changedAt}
	if err := runCLIWorkersWatch(ctx, c, out, true); err != nil {
		t.Fatalf("workers watch returned error after session cancellation: %v", err)
	}
	if got := bytes.Count(out.Bytes(), []byte("\n")); got != 200 {
		t.Fatalf("workers watch displayed %d snapshots, want 200", got)
	}
	return out
}

func TestWorkersWatchCombinedSnapshotTenMinuteRequestBudgetAndFreshness(t *testing.T) {
	const refreshes = 200 // 600 seconds at the user-visible 3 second refresh interval.
	const handlerDelay = 2 * time.Millisecond
	previousInterval := workersLiveRefreshInterval
	workersLiveRefreshInterval = time.Millisecond // scale wall time while keeping the real watch loop.
	t.Cleanup(func() { workersLiveRefreshInterval = previousInterval })

	var optimizedRequests atomic.Int32
	var optimizedChanges sync.Map
	optimizedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		optimizedRequests.Add(1)
		if r.URL.Path != "/workers" {
			t.Errorf("optimized watch unexpected route %s", r.URL.RequestURI())
			http.NotFound(w, r)
			return
		}
		n := int(optimizedRequests.Load())
		optimizedChanges.Store(n, time.Now().UnixNano())
		time.Sleep(handlerDelay)
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, workersWatchCapacityHTML(n, n, n))
	}))
	t.Cleanup(optimizedServer.Close)
	optimizedClient, err := client.New(optimizedServer.URL)
	if err != nil {
		t.Fatal(err)
	}

	var baselineRequests, baselineCapacityRequests, baselineState atomic.Int32
	var baselineChanges sync.Map
	baselineServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		baselineRequests.Add(1)
		if r.URL.Path == "/workers" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Path {
		case "/api/capacity/global", "/api/capacity/projects", "/api/capacity/models":
			baselineCapacityRequests.Add(1)
		default:
			t.Errorf("baseline watch unexpected route %s", r.URL.RequestURI())
			http.NotFound(w, r)
			return
		}
		var n int
		if r.URL.Path == "/api/capacity/global" {
			n = int(baselineState.Add(1))
			baselineChanges.Store(n, time.Now().UnixNano())
		} else {
			n = int(baselineState.Load())
		}
		time.Sleep(handlerDelay)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/capacity/global":
			_, _ = fmt.Fprintf(w, `{"max_workers":8,"total_running":%d,"queue_size":1}`, n)
		case "/api/capacity/projects":
			_, _ = fmt.Fprintf(w, `[{"id":"p1","name":"Demo","running":%d,"queue_size":2,"max_workers":4}]`, n)
		case "/api/capacity/models":
			_, _ = fmt.Fprintf(w, `[{"id":"m1","name":"Sonnet","model":"claude-sonnet","running":%d,"max_workers":5}]`, n)
		}
	}))
	t.Cleanup(baselineServer.Close)
	baselineClient, err := client.New(baselineServer.URL)
	if err != nil {
		t.Fatal(err)
	}

	optimizedOutput := runWorkersWatchSession(t, optimizedClient, &optimizedChanges)
	baselineOutput := runWorkersWatchSession(t, baselineClient, &baselineChanges)
	optimizedP95 := durationPercentile(optimizedOutput.latencies, 0.95)
	baselineP95 := durationPercentile(baselineOutput.latencies, 0.95)
	if got := optimizedRequests.Load(); got != refreshes {
		t.Fatalf("combined endpoint requests in ten-minute watch session = %d, want <= %d", got, refreshes)
	}
	if got, want := baselineCapacityRequests.Load(), int32(refreshes*3); got != want {
		t.Fatalf("legacy capacity requests in ten-minute watch session = %d, want baseline %d", got, want)
	}
	if got, want := baselineRequests.Load(), int32(refreshes*3+1); got != want {
		t.Fatalf("legacy total requests including one route probe = %d, want %d", got, want)
	}
	for _, output := range []*workersWatchSessionWriter{optimizedOutput, baselineOutput} {
		lines := strings.Split(strings.TrimSpace(output.String()), "\n")
		if len(lines) != refreshes {
			t.Fatalf("watch output contained %d snapshots, want %d", len(lines), refreshes)
		}
		var snapshot struct {
			Workers []workerCapacityRow      `json:"workers"`
			Models  []modelWorkerCapacityRow `json:"models"`
		}
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &snapshot); err != nil {
			t.Fatalf("last watch snapshot is invalid JSON: %v", err)
		}
		if len(snapshot.Workers) != 2 || snapshot.Workers[1].Name != "Demo" || len(snapshot.Models) != 1 || snapshot.Models[0].Name != "Sonnet" {
			t.Fatalf("watch omitted project or model details: %+v", snapshot)
		}
	}
	if optimizedP95 > 3*time.Second {
		t.Fatalf("changed-snapshot-to-display p95 = %s, want <= 3s", optimizedP95)
	}
	const p95MeasurementTolerance = 250 * time.Microsecond
	if optimizedP95 > baselineP95+p95MeasurementTolerance {
		t.Fatalf("optimized changed-snapshot-to-display p95 = %s, materially slower than baseline %s (tolerance %s)", optimizedP95, baselineP95, p95MeasurementTolerance)
	}
	t.Logf("10-minute-equivalent CLI watch: optimized requests=%d, legacy capacity requests=%d (total=%d including one capability probe); changed-snapshot-to-display p95 optimized=%s, baseline=%s", optimizedRequests.Load(), baselineCapacityRequests.Load(), baselineRequests.Load(), optimizedP95, baselineP95)
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

func TestFetchWorkersOverviewCombinedSnapshotPreservesSourceWarnings(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/workers" {
			http.NotFound(w, r)
			return
		}
		html := workersWatchCapacityHTML(1, 0, 0)
		html = strings.Replace(html, `id="project-stats-tbody"`, `id="project-stats-tbody" data-capacity-available="false"`, 1)
		html = strings.Replace(html, `id="model-stats-tbody"`, `id="model-stats-tbody" data-capacity-available="false"`, 1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, html)
	}))
	t.Cleanup(server.Close)
	c, err := client.New(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	overview, err := fetchWorkersOverview(context.Background(), c)
	if err != nil {
		t.Fatalf("fetchWorkersOverview: %v", err)
	}
	if overview.Global == nil || overview.Global.TotalRunning != 1 || len(overview.Projects) != 0 || len(overview.Models) != 0 {
		t.Fatalf("partial snapshot lost available global capacity: %+v", overview)
	}
	if overview.ModelsAvailable || len(overview.Warnings) != 2 || overview.Warnings[0] != "project worker capacity unavailable" || overview.Warnings[1] != "model worker capacity unavailable" {
		t.Fatalf("combined partial-source warnings = %+v, models available=%t", overview.Warnings, overview.ModelsAvailable)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("combined snapshot requests = %d, want 1", got)
	}
}

func TestFetchWorkersOverviewFallsBackAndPreservesPartialWarnings(t *testing.T) {
	var snapshotRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/workers" {
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
		if r.URL.Path != "/workers" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(workersWatchCapacityHTML(1, 1, 1)))
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
		if r.URL.Path == "/workers" {
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
		if r.URL.Path != "/workers" {
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
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, workersWatchCapacityHTML(int(running), int(running), int(running)))
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
