package terminal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"runtime/debug"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/openvibely/openvibely-terminal/internal/client"
)

const (
	selectorActivationPerfSamples = 50
	selectorActivationPerfWarmup  = 8
	selectorActivationPerfFilter  = "review"
)

type selectorActivationPerfMode string

const (
	selectorActivationPerfEager selectorActivationPerfMode = "eager"
	selectorActivationPerfLazy  selectorActivationPerfMode = "lazy"
)

type selectorActivationPerfSample struct {
	latency time.Duration
	allocs  uint64
	bytes   uint64
}

// TestSelectorActivationPerformanceEvidence compares current eager search
// indexing with lazy indexing using task catalogs accepted by
// ListTaskReferences. It is opt-in because the full request and JSON path
// intentionally exercises large response bodies.
func TestSelectorActivationPerformanceEvidence(t *testing.T) {
	if testing.Short() {
		t.Skip("selector activation performance evidence is not run with -short")
	}
	if got := runtime.GOMAXPROCS(0); got < 1 {
		t.Fatalf("GOMAXPROCS = %d, want positive", got)
	}
	if !selectorActivationPerformanceEnabled() {
		t.Skip("set OPENVIBELY_SELECTOR_PERF_EVIDENCE=1 to run selector activation performance evidence")
	}
	oldGCPercent := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(oldGCPercent)

	t.Logf("selector_activation_environment go=%s os=%s arch=%s cpu_count=%d gomaxprocs=%d gc=disabled_manual_gc_before_samples=true width=120 filter=%q warmup=%d samples=%d title_profile=4 realistic 78-83 character titles id_profile=36-character UUID category_profile=backlog|active|completed|scheduled status_profile=pending|queued|running|completed|failed|cancelled|blocked", runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.GOMAXPROCS(0), selectorActivationPerfFilter, selectorActivationPerfWarmup, selectorActivationPerfSamples)

	for _, taskCount := range []int{100, 1000, 10000} {
		body := selectorActivationPerfCatalog(taskCount)
		tasks := selectorActivationPerfDecodeCatalog(t, body)
		items := taskSelectorItems(tasks)
		if len(items) != taskCount {
			t.Fatalf("taskSelectorItems returned %d rows for %d tasks", len(items), taskCount)
		}
		msg := selectorActiveMsg{title: "Tasks", command: "tasks open", items: items}
		base := Model{width: 120, height: 40}
		base.ensureInteractiveModels()

		activation := make(map[selectorActivationPerfMode][]selectorActivationPerfSample, 2)
		for i := 0; i < selectorActivationPerfWarmup; i++ {
			for _, mode := range selectorActivationPerfModes(i) {
				_ = selectorActivationPerfActivate(base, msg, mode)
			}
		}
		for i := 0; i < selectorActivationPerfSamples; i++ {
			for _, mode := range selectorActivationPerfModes(i) {
				runtime.GC()
				activation[mode] = append(activation[mode], selectorActivationPerfMeasureActivation(base, msg, mode))
			}
		}

		serverState := &selectorActivationPerfServer{body: body}
		server := httptest.NewServer(serverState)
		c, err := client.New(server.URL)
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		if _, err := c.ListTaskReferences(context.Background(), "p1"); err != nil {
			server.Close()
			t.Fatalf("decode valid %d-task reference catalog: %v", taskCount, err)
		}

		firstFilter := make(map[selectorActivationPerfMode][]time.Duration, 2)
		visible := make(map[selectorActivationPerfMode][]time.Duration, 2)
		for i := 0; i < selectorActivationPerfWarmup; i++ {
			for _, mode := range selectorActivationPerfModes(i) {
				runtime.GC()
				_, _ = selectorActivationPerfMeasureRequestPath(t, c, base, mode, false)
				runtime.GC()
				_, _ = selectorActivationPerfMeasureRequestPath(t, c, base, mode, true)
			}
		}
		for i := 0; i < selectorActivationPerfSamples; i++ {
			for _, mode := range selectorActivationPerfModes(i) {
				runtime.GC()
				visibleDuration, err := selectorActivationPerfMeasureRequestPath(t, c, base, mode, false)
				if err != nil {
					server.Close()
					t.Fatalf("%s %d-task request-to-picker sample %d: %v", mode, taskCount, i+1, err)
				}
				visible[mode] = append(visible[mode], visibleDuration)

				runtime.GC()
				filterDuration, err := selectorActivationPerfMeasureRequestPath(t, c, base, mode, true)
				if err != nil {
					server.Close()
					t.Fatalf("%s %d-task first-filter sample %d: %v", mode, taskCount, i+1, err)
				}
				firstFilter[mode] = append(firstFilter[mode], filterDuration)
			}
		}
		wantRequests := int64(1 + 2*selectorActivationPerfWarmup*2 + 2*selectorActivationPerfSamples*2)
		if got := serverState.requests.Load(); got != wantRequests {
			server.Close()
			t.Fatalf("%d-task local server requests = %d, want %d", taskCount, got, wantRequests)
		}
		server.Close()

		for _, mode := range []selectorActivationPerfMode{selectorActivationPerfEager, selectorActivationPerfLazy} {
			activationMedian, activationP95 := selectorActivationPerfDurationPercentiles(activation[mode])
			allocMedian, bytesMedian := selectorActivationPerfAllocationMedians(activation[mode])
			visibleMedian, visibleP95 := taskReferencePerfPercentiles(visible[mode])
			filterMedian, filterP95 := taskReferencePerfPercentiles(firstFilter[mode])
			t.Logf("selector_activation_result mode=%s tasks=%d samples=%d warmup=%d activation_median=%s activation_p95=%s activation_allocs_median=%d activation_bytes_median=%d request_to_picker_median=%s request_to_picker_p95=%s request_to_first_filter_median=%s request_to_first_filter_p95=%s response_bytes=%d requests_per_path=1", mode, taskCount, selectorActivationPerfSamples, selectorActivationPerfWarmup, activationMedian, activationP95, allocMedian, bytesMedian, visibleMedian, visibleP95, filterMedian, filterP95, len(body))
		}
		_, eagerFirstFilterP95 := taskReferencePerfPercentiles(firstFilter[selectorActivationPerfEager])
		_, lazyFirstFilterP95 := taskReferencePerfPercentiles(firstFilter[selectorActivationPerfLazy])
		if lazyFirstFilterP95*10 > eagerFirstFilterP95*11 {
			t.Errorf("%d-task lazy first-filter p95 %s regressed over eager p95 %s by more than 10%%", taskCount, lazyFirstFilterP95, eagerFirstFilterP95)
		}

		if taskCount == 100 {
			eagerMedian, _ := selectorActivationPerfDurationPercentiles(activation[selectorActivationPerfEager])
			lazyMedian, _ := selectorActivationPerfDurationPercentiles(activation[selectorActivationPerfLazy])
			eagerAllocs, eagerBytes := selectorActivationPerfAllocationMedians(activation[selectorActivationPerfEager])
			lazyAllocs, lazyBytes := selectorActivationPerfAllocationMedians(activation[selectorActivationPerfLazy])
			if lazyMedian*10 > eagerMedian*11 {
				t.Errorf("100-task lazy activation median %s regressed over eager median %s by more than 10%%", lazyMedian, eagerMedian)
			}
			meetsLatency := lazyMedian*10 <= eagerMedian*7
			meetsBytes := lazyBytes*10 <= eagerBytes*6
			t.Logf("selector_activation_acceptance size=100 median_activation_reduction_at_least_30_percent=%t allocation_reduction_at_least_40_percent=%t eager_allocs=%d lazy_allocs=%d", meetsLatency, meetsBytes, eagerAllocs, lazyAllocs)
			if !meetsLatency || !meetsBytes {
				t.Errorf("100-task activation did not meet the required 30%% latency and 40%% allocation reductions")
			}
		}
	}
}

func selectorActivationPerformanceEnabled() bool {
	return os.Getenv("OPENVIBELY_SELECTOR_PERF_EVIDENCE") == "1"
}

func selectorActivationPerfModes(sample int) []selectorActivationPerfMode {
	if sample%2 == 0 {
		return []selectorActivationPerfMode{selectorActivationPerfEager, selectorActivationPerfLazy}
	}
	return []selectorActivationPerfMode{selectorActivationPerfLazy, selectorActivationPerfEager}
}

func selectorActivationPerfCatalog(count int) []byte {
	categories := []string{"backlog", "active", "completed", "scheduled"}
	statuses := []string{"pending", "queued", "running", "completed", "failed", "cancelled", "blocked"}
	verbs := []string{"Review", "Improve", "Document", "Investigate"}
	tasks := make([]client.Task, count)
	for i := range tasks {
		tasks[i] = client.Task{
			ID:        fmt.Sprintf("%08x-%04x-4000-8000-%012x", i, i%65536, i),
			ProjectID: "p1",
			Title:     fmt.Sprintf("%s authentication callback behavior and session recovery for project %05d", verbs[i%len(verbs)], i),
			Category:  categories[i%len(categories)],
			Status:    statuses[i%len(statuses)],
		}
	}
	body, err := json.Marshal(struct {
		Tasks []client.Task `json:"tasks"`
	}{Tasks: tasks})
	if err != nil {
		panic(err)
	}
	return body
}

func selectorActivationPerfDecodeCatalog(t *testing.T, body []byte) []client.Task {
	t.Helper()
	var catalog struct {
		Tasks []client.Task `json:"tasks"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil {
		t.Fatalf("decode generated reference catalog: %v", err)
	}
	for i, task := range catalog.Tasks {
		if task.ID == "" || task.ProjectID != "p1" || task.Category == "" || task.Status == "" {
			t.Fatalf("generated task %d is not a valid task reference: %+v", i, task)
		}
	}
	return catalog.Tasks
}

func selectorActivationPerfActivate(base Model, msg selectorActiveMsg, mode selectorActivationPerfMode) Model {
	updated, _ := base.handleSelector(msg)
	active := updated.(Model)
	if mode == selectorActivationPerfEager {
		// This reproduces the removed unfiltered activation work after the
		// common selector setup: one lowercase label/detail/reference index
		// entry per row.
		active.selectorSearch = selectorSearchTexts(active.selectorItems)
	}
	return active
}

func selectorActivationPerfMeasureActivation(base Model, msg selectorActiveMsg, mode selectorActivationPerfMode) selectorActivationPerfSample {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	active := selectorActivationPerfActivate(base, msg, mode)
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(active)
	return selectorActivationPerfSample{latency: elapsed, allocs: after.Mallocs - before.Mallocs, bytes: after.TotalAlloc - before.TotalAlloc}
}

func selectorActivationPerfMeasureRequestPath(t *testing.T, c *client.Client, base Model, mode selectorActivationPerfMode, withFilter bool) (time.Duration, error) {
	t.Helper()
	start := time.Now()
	cmd := selectorForWithSuffix("Tasks", "tasks open", "no tasks", "", func(ctx context.Context) ([]selectorItem, error) {
		tasks, err := c.ListTaskReferences(ctx, "p1")
		if err != nil {
			return nil, err
		}
		return taskSelectorItems(tasks), nil
	})
	msg, ok := cmd().(selectorActiveMsg)
	if !ok {
		return 0, fmt.Errorf("selector command returned unexpected message")
	}
	active := selectorActivationPerfActivate(base, msg, mode)
	if withFilter {
		for _, r := range selectorActivationPerfFilter {
			next, _ := active.handleSelectorKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			active = next.(Model)
		}
	}
	view := active.renderSelector()
	elapsed := time.Since(start)
	if !active.selectorActive || view == "" {
		return 0, fmt.Errorf("selector did not produce visible rows")
	}
	runtime.KeepAlive(view)
	runtime.KeepAlive(active)
	return elapsed, nil
}

func selectorActivationPerfDurationPercentiles(samples []selectorActivationPerfSample) (time.Duration, time.Duration) {
	durations := make([]time.Duration, len(samples))
	for i := range samples {
		durations[i] = samples[i].latency
	}
	return taskReferencePerfPercentiles(durations)
}

func selectorActivationPerfAllocationMedians(samples []selectorActivationPerfSample) (uint64, uint64) {
	allocs := make([]uint64, len(samples))
	bytes := make([]uint64, len(samples))
	for i := range samples {
		allocs[i] = samples[i].allocs
		bytes[i] = samples[i].bytes
	}
	return selectorActivationPerfUintMedian(allocs), selectorActivationPerfUintMedian(bytes)
}

func selectorActivationPerfUintMedian(values []uint64) uint64 {
	sorted := append([]uint64(nil), values...)
	// Insertion sort is sufficient for the fixed 50-sample evidence set.
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	if len(sorted) == 0 {
		return 0
	}
	return sorted[len(sorted)/2]
}

type selectorActivationPerfServer struct {
	body     []byte
	requests atomic.Int64
}

func (s *selectorActivationPerfServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Path != "/api/tasks/reference-catalog" || r.URL.Query().Get("project_id") != "p1" {
		http.NotFound(w, r)
		return
	}
	s.requests.Add(1)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprint(len(s.body)))
	_, _ = w.Write(s.body)
}
