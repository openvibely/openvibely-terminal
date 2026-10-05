package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const selectedTaskDetailID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

var deferredTaskDetailTabs = []string{"thread", "changes", "lifecycle"}

type detailRequestRecord struct {
	path  string
	bytes int
}

type detailMeasurementFixture struct {
	t               *testing.T
	taskID          string
	selectedTab     string
	selectedDelay   time.Duration
	unselectedDelay time.Duration
	unselectedBytes int
	initialLife     string
	intercept       func(http.ResponseWriter, *http.Request) bool
	mu              sync.Mutex
	records         []detailRequestRecord
	server          *httptest.Server
}

func newDetailMeasurementFixture(t *testing.T, selectedTab string, selectedDelay, unselectedDelay time.Duration, unselectedBytes int, configure ...func(*detailMeasurementFixture)) *detailMeasurementFixture {
	t.Helper()
	f := &detailMeasurementFixture{
		t:               t,
		taskID:          selectedTaskDetailID,
		selectedTab:     selectedTab,
		selectedDelay:   selectedDelay,
		unselectedDelay: unselectedDelay,
		unselectedBytes: unselectedBytes,
	}
	for _, configureFixture := range configure {
		configureFixture(f)
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.server.Close)
	return f
}

func (f *detailMeasurementFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if f.intercept != nil && f.intercept(w, r) {
		return
	}
	path := r.URL.Path
	if got := r.URL.Query().Get("project_id"); got != "p1" {
		f.t.Errorf("%s project_id = %q, want p1", r.URL.RequestURI(), got)
	}
	var body []byte
	switch path {
	case "/tasks/" + f.taskID:
		body = []byte(fmt.Sprintf(`<div data-task-id="%s" data-project-id="p1" data-task-status="running" data-task-category="active">
			<h2 class="font-bold">Measured task</h2>
			<div id="tab-details">task details</div>
			<div id="tab-chat" hx-get="/tasks/%s/thread">thread placeholder</div>
			<div id="tab-changes" hx-get="/tasks/%s/changes">changes placeholder</div>
			<div id="tab-lifecycle">%s</div>
		</div>`, f.taskID, f.taskID, f.taskID, f.initialLife))
	case "/tasks/" + f.taskID + "/thread":
		body = []byte("<div>agent: selected thread output</div>")
		if f.selectedTab != "thread" && f.unselectedBytes > 0 {
			body = []byte("<div>" + strings.Repeat("x", f.unselectedBytes) + "</div>")
		}
	case "/tasks/" + f.taskID + "/changes":
		body = []byte("<div>3 files changed in selected output</div>")
		if f.selectedTab != "changes" && f.unselectedBytes > 0 {
			body = []byte("<div>" + strings.Repeat("x", f.unselectedBytes) + "</div>")
		}
	case "/api/tasks/" + f.taskID + "/lifecycle-executions":
		body = []byte(`[{"id":"exec-1","skill_key":"review","when":"before task","status":"completed","started_at":"2025-01-02T03:04:05Z"}]`)
		if f.selectedTab != "lifecycle" && f.unselectedBytes > 0 {
			body = []byte(`{"items":[],"padding":"` + strings.Repeat("x", f.unselectedBytes) + `"}`)
		}
	default:
		http.NotFound(w, r)
		return
	}

	delay := time.Duration(0)
	if path != "/tasks/"+f.taskID {
		delay = f.unselectedDelay
		if f.selectedTab != "" && detailTabEndpoint(f.taskID, f.selectedTab) == path {
			delay = f.selectedDelay
		}
	}
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.Context().Done():
			return
		}
	}
	if strings.HasPrefix(path, "/api/") {
		w.Header().Set("Content-Type", "application/json")
	} else {
		w.Header().Set("Content-Type", "text/html")
	}
	written, err := w.Write(body)
	if err != nil && !errors.Is(err, http.ErrHandlerTimeout) {
		f.t.Errorf("write %s: %v", path, err)
	}
	f.mu.Lock()
	f.records = append(f.records, detailRequestRecord{path: path, bytes: written})
	f.mu.Unlock()
}

func detailTabEndpoint(taskID, tab string) string {
	switch tab {
	case "thread":
		return "/tasks/" + taskID + "/thread"
	case "changes":
		return "/tasks/" + taskID + "/changes"
	case "lifecycle":
		return "/api/tasks/" + taskID + "/lifecycle-executions"
	default:
		return ""
	}
}

func unselectedFixtureResponseBytes(selectedTab string, payloadBytes int) int {
	total := 0
	for _, tab := range deferredTaskDetailTabs {
		if tab == selectedTab {
			continue
		}
		switch tab {
		case "thread", "changes":
			total += len("<div>") + payloadBytes + len("</div>")
		case "lifecycle":
			total += len(`{"items":[],"padding":"`) + payloadBytes + len(`"}`)
		}
	}
	return total
}

func (f *detailMeasurementFixture) resetRecords() {
	f.mu.Lock()
	f.records = nil
	f.mu.Unlock()
}

func (f *detailMeasurementFixture) snapshotRecords() []detailRequestRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]detailRequestRecord(nil), f.records...)
}

func (f *detailMeasurementFixture) recordTotals() (int, int, map[string]int) {
	records := f.snapshotRecords()
	bytesRead := 0
	hits := make(map[string]int)
	for _, record := range records {
		bytesRead += record.bytes
		hits[record.path]++
	}
	return len(records), bytesRead, hits
}

func (f *detailMeasurementFixture) client(t *testing.T) *Client {
	t.Helper()
	c, err := New(f.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestGetTaskSelectedDeferredTabRequestsOnlyRequestedSection(t *testing.T) {
	for _, tab := range deferredTaskDetailTabs {
		t.Run(tab, func(t *testing.T) {
			f := newDetailMeasurementFixture(t, tab, 0, 0, 0)
			c := f.client(t)

			full, err := c.GetTaskForProjectExact(context.Background(), f.taskID, "p1")
			if err != nil {
				t.Fatalf("full detail: %v", err)
			}
			wantOutput := full.TabText(tab)
			f.resetRecords()

			detail, err := c.GetTaskForProjectExactTab(context.Background(), f.taskID, "p1", tab)
			if err != nil {
				t.Fatalf("selected detail: %v", err)
			}
			gotOutput := detail.TabText(tab)
			if gotOutput != wantOutput {
				t.Fatalf("selected output = %q, full output = %q", gotOutput, wantOutput)
			}
			requests, gotBytes, hits := f.recordTotals()
			if requests != 2 {
				t.Fatalf("requests = %d (%v), want initial page and one selected endpoint", requests, hits)
			}
			if hits["/tasks/"+f.taskID] != 1 || hits[detailTabEndpoint(f.taskID, tab)] != 1 {
				t.Fatalf("request paths = %v, want page and %s", hits, detailTabEndpoint(f.taskID, tab))
			}
			for _, other := range deferredTaskDetailTabs {
				if other != tab && hits[detailTabEndpoint(f.taskID, other)] != 0 {
					t.Errorf("unselected %s endpoint requested: %v", other, hits)
				}
			}
			wantBytes := 0
			for _, record := range f.snapshotRecords() {
				wantBytes += record.bytes
			}
			if gotBytes != wantBytes {
				t.Fatalf("response bytes = %d, recorded bytes = %d", gotBytes, wantBytes)
			}
			if detail.TabError(tab) != nil {
				t.Fatalf("successful tab error = %v", detail.TabError(tab))
			}
		})
	}

	t.Run("lifecycle still requests selected endpoint with page content", func(t *testing.T) {
		f := newDetailMeasurementFixture(t, "lifecycle", 0, 0, 0, func(f *detailMeasurementFixture) {
			f.initialLife = "initial lifecycle content"
		})
		detail, err := f.client(t).GetTaskForProjectExactTab(context.Background(), f.taskID, "p1", "lifecycle")
		if err != nil {
			t.Fatal(err)
		}
		requests, _, hits := f.recordTotals()
		if requests != 2 || hits["/tasks/"+f.taskID] != 1 || hits[detailTabEndpoint(f.taskID, "lifecycle")] != 1 {
			t.Fatalf("requests = %d (%v), want page and lifecycle endpoint", requests, hits)
		}
		if detail.Life != "initial lifecycle content" {
			t.Fatalf("selected lifecycle output = %q, want initial page text preserved", detail.Life)
		}
	})
}

func TestGetTaskForProjectExactTabFullDetailStillLoadsAllDeferredSections(t *testing.T) {
	f := newDetailMeasurementFixture(t, "", 0, 0, 0)
	c := f.client(t)
	detail, err := c.GetTaskForProjectExactTab(context.Background(), f.taskID, "p1", "")
	if err != nil {
		t.Fatal(err)
	}
	requests, _, hits := f.recordTotals()
	if requests != 4 {
		t.Fatalf("requests = %d (%v), want page plus all three deferred sections", requests, hits)
	}
	for _, tab := range deferredTaskDetailTabs {
		if got := detail.TabText(tab); got == "" || strings.Contains(got, "placeholder") {
			t.Errorf("%s output = %q, want loaded full-detail content", tab, got)
		}
	}
}

func TestGetTaskSelectedDeferredTabEmptyAndHTTPFailure(t *testing.T) {
	for _, tc := range []struct {
		name       string
		statusCode int
		body       string
		wantErr    bool
	}{
		{name: "successful empty fragment", body: `<div></div>`},
		{name: "server error", statusCode: http.StatusBadGateway, body: `{"error":"changes unavailable"}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const tab = "changes"
			f := newDetailMeasurementFixture(t, tab, 0, 0, 0, func(f *detailMeasurementFixture) {
				f.intercept = func(w http.ResponseWriter, r *http.Request) bool {
					if r.URL.Path != detailTabEndpoint(f.taskID, tab) {
						return false
					}
					if tc.statusCode != 0 {
						w.WriteHeader(tc.statusCode)
					}
					_, _ = io.WriteString(w, tc.body)
					return true
				}
			})
			detail, err := f.client(t).GetTaskForProjectExactTab(context.Background(), f.taskID, "p1", tab)
			if tc.wantErr {
				var loadErr *TaskDetailLoadError
				if !errors.As(err, &loadErr) || loadErr.Changes == nil || loadErr.Thread != nil || loadErr.Lifecycle != nil {
					t.Fatalf("error = %T %v, want changes-only load error", err, err)
				}
			} else if err != nil || detail.TabError(tab) != nil {
				t.Fatalf("empty successful tab: error=%v tab error=%v", err, detail.TabError(tab))
			}
		})
	}
}

func TestGetTaskSelectedDeferredTabPreservesAuthAndTransportErrors(t *testing.T) {
	t.Run("auth", func(t *testing.T) {
		f := newDetailMeasurementFixture(t, "thread", 0, 0, 0, func(f *detailMeasurementFixture) {
			f.intercept = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != detailTabEndpoint(f.taskID, "thread") {
					return false
				}
				w.Header().Set("Location", "/login")
				w.WriteHeader(http.StatusFound)
				return true
			}
		})
		detail, err := f.client(t).GetTaskForProjectExactTab(context.Background(), f.taskID, "p1", "thread")
		if err == nil || !IsAuthRequired(err) || detail.TabError("thread") == nil || !IsAuthRequired(detail.TabError("thread")) {
			t.Fatalf("auth result = (%v, %v), want typed thread authentication failure", detail, err)
		}
		if detail.TabError("changes") != nil || detail.TabError("lifecycle") != nil {
			t.Fatalf("unrequested errors = changes:%v lifecycle:%v", detail.TabError("changes"), detail.TabError("lifecycle"))
		}
	})

	t.Run("transport", func(t *testing.T) {
		f := newDetailMeasurementFixture(t, "changes", 0, 0, 0)
		c := f.client(t)
		c.http.Transport = htmlTestRoundTripper(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == detailTabEndpoint(f.taskID, "changes") {
				return nil, &url.Error{Op: http.MethodGet, URL: r.URL.String(), Err: errors.New("connection reset")}
			}
			return http.DefaultTransport.RoundTrip(r)
		})
		detail, err := c.GetTaskForProjectExactTab(context.Background(), f.taskID, "p1", "changes")
		if err == nil || !IsTransportError(err) || !IsTransportError(detail.TabError("changes")) {
			t.Fatalf("transport result = (%v, %v), want typed changes transport failure", detail, err)
		}
		if detail.TabError("thread") != nil || detail.TabError("lifecycle") != nil {
			t.Fatalf("unrequested errors = thread:%v lifecycle:%v", detail.TabError("thread"), detail.TabError("lifecycle"))
		}
	})
}

func TestGetTaskForProjectExactTabCancellationAndProjectMismatch(t *testing.T) {
	t.Run("canceled selected request", func(t *testing.T) {
		requested := make(chan struct{})
		release := make(chan struct{})
		f := newDetailMeasurementFixture(t, "thread", 0, 0, 0, func(f *detailMeasurementFixture) {
			f.intercept = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != detailTabEndpoint(f.taskID, "thread") {
					return false
				}
				close(requested)
				select {
				case <-r.Context().Done():
				case <-release:
				}
				return true
			}
		})
		defer close(release)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		c := f.client(t)
		done := make(chan error, 1)
		go func() {
			_, err := c.GetTaskForProjectExactTab(ctx, f.taskID, "p1", "thread")
			done <- err
		}()
		select {
		case <-requested:
		case <-time.After(2 * time.Second):
			t.Fatal("selected request was not started")
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("selected request did not return after cancellation")
		}
	})

	t.Run("project mismatch", func(t *testing.T) {
		var paths []string
		var mu sync.Mutex
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			paths = append(paths, r.URL.Path)
			mu.Unlock()
			_, _ = io.WriteString(w, fmt.Sprintf(`<div data-task-id="%s" data-project-id="p2"><h2 class="font-bold">Foreign</h2></div>`, selectedTaskDetailID))
		}))
		defer srv.Close()
		c, _ := New(srv.URL)
		detail, err := c.GetTaskForProjectExactTab(context.Background(), selectedTaskDetailID, "p1", "changes")
		if err == nil || detail != nil || !strings.Contains(err.Error(), "not found in selected project") {
			t.Fatalf("result = (%v, %v), want project mismatch", detail, err)
		}
		if len(paths) != 1 || paths[0] != "/tasks/"+selectedTaskDetailID {
			t.Fatalf("requests = %v, want only initial task page", paths)
		}
	})
}

func TestSelectedTaskDetailPerformanceMeasurements(t *testing.T) {
	runs := measurementInt(t, "TASK_DETAIL_PERF_RUNS", 20)
	if runs < 20 {
		runs = 20
	}
	selectedDelay := time.Duration(measurementInt(t, "TASK_DETAIL_SELECTED_DELAY_MS", 50)) * time.Millisecond
	unselectedDelay := time.Duration(measurementInt(t, "TASK_DETAIL_UNSELECTED_DELAY_MS", 250)) * time.Millisecond

	for _, tab := range deferredTaskDetailTabs {
		t.Run(tab+"/delay", func(t *testing.T) {
			fullFixture := newDetailMeasurementFixture(t, tab, selectedDelay, unselectedDelay, 0)
			fullClient := fullFixture.client(t)
			fullSamples := make([]time.Duration, 0, runs)
			var fullOutput string
			for i := 0; i < runs; i++ {
				start := time.Now()
				detail, err := fullClient.GetTaskForProjectExact(context.Background(), fullFixture.taskID, "p1")
				fullSamples = append(fullSamples, time.Since(start))
				if err != nil {
					t.Fatal(err)
				}
				fullOutput = detail.TabText(tab)
			}
			fullReqs, fullBytes, fullHits := fullFixture.recordTotals()
			if fullReqs != 4*runs || fullHits["/tasks/"+fullFixture.taskID] != runs {
				t.Fatalf("full detail requests: total=%d paths=%v, want one page and three sections per run", fullReqs, fullHits)
			}
			for _, fullTab := range deferredTaskDetailTabs {
				if fullHits[detailTabEndpoint(fullFixture.taskID, fullTab)] != runs {
					t.Fatalf("full detail %s requests = %d, want %d", fullTab, fullHits[detailTabEndpoint(fullFixture.taskID, fullTab)], runs)
				}
			}

			selectedFixture := newDetailMeasurementFixture(t, tab, selectedDelay, unselectedDelay, 0)
			selectedClient := selectedFixture.client(t)
			selectedSamples := make([]time.Duration, 0, runs)
			var selectedOutput string
			for i := 0; i < runs; i++ {
				start := time.Now()
				detail, err := selectedClient.GetTaskForProjectExactTab(context.Background(), selectedFixture.taskID, "p1", tab)
				selectedSamples = append(selectedSamples, time.Since(start))
				if err != nil {
					t.Fatal(err)
				}
				selectedOutput = detail.TabText(tab)
			}
			selectedReqs, selectedBytes, selectedHits := selectedFixture.recordTotals()
			if selectedOutput != fullOutput {
				t.Fatalf("selected output %q differs from full output %q", selectedOutput, fullOutput)
			}
			if selectedReqs != 2*runs || selectedHits["/tasks/"+selectedFixture.taskID] != runs || selectedHits[detailTabEndpoint(selectedFixture.taskID, tab)] != runs {
				t.Fatalf("selected requests: total=%d paths=%v, want %d page and %d selected requests", selectedReqs, selectedHits, runs, runs)
			}
			for _, other := range deferredTaskDetailTabs {
				if other != tab && selectedHits[detailTabEndpoint(selectedFixture.taskID, other)] != 0 {
					t.Fatalf("unselected %s hit during %s runs: %v", other, tab, selectedHits)
				}
			}
			fullP95, selectedP95 := percentile(fullSamples, .95), percentile(selectedSamples, .95)
			if fullP95 > 0 && selectedP95*2 > fullP95 {
				t.Errorf("selected p95 %v is not at least 50%% below full p95 %v", selectedP95, fullP95)
			}
			t.Logf("delay runs=%d selected_delay=%s unselected_delay=%s full requests/run=%.1f bytes/run=%.0f median=%s p95=%s output=%q selected requests/run=%.1f bytes/run=%.0f median=%s p95=%s output=%q", runs, selectedDelay, unselectedDelay, float64(fullReqs)/float64(runs), float64(fullBytes)/float64(runs), percentile(fullSamples, .50), fullP95, fullOutput, float64(selectedReqs)/float64(runs), float64(selectedBytes)/float64(runs), percentile(selectedSamples, .50), selectedP95, selectedOutput)
		})

		for _, payloadSize := range []int{500 << 10, 5 << 20} {
			size := payloadSize
			t.Run(fmt.Sprintf("%s/unselected-%d-bytes", tab, size), func(t *testing.T) {
				f := newDetailMeasurementFixture(t, tab, 0, 0, size)
				c := f.client(t)
				samples := make([]time.Duration, 0, runs)
				var output string
				for i := 0; i < runs; i++ {
					start := time.Now()
					detail, err := c.GetTaskForProjectExactTab(context.Background(), f.taskID, "p1", tab)
					samples = append(samples, time.Since(start))
					if err != nil {
						t.Fatal(err)
					}
					output = detail.TabText(tab)
				}
				requests, totalBytes, hits := f.recordTotals()
				if requests != 2*runs || hits["/tasks/"+f.taskID] != runs || hits[detailTabEndpoint(f.taskID, tab)] != runs {
					t.Fatalf("requests: total=%d paths=%v, want only page and selected section", requests, hits)
				}
				for _, other := range deferredTaskDetailTabs {
					if other != tab && hits[detailTabEndpoint(f.taskID, other)] != 0 {
						t.Fatalf("unselected %s endpoint sent payload during %s measurement", other, tab)
					}
				}
				t.Logf("payload runs=%d unselected_payload_each=%d avoided_unselected_response_bytes/run=%d requests/run=%.1f bytes/run=%.0f median=%s p95=%s output=%q", runs, size, unselectedFixtureResponseBytes(tab, size), float64(requests)/float64(runs), float64(totalBytes)/float64(runs), percentile(samples, .50), percentile(samples, .95), output)
			})
		}
	}
}

func measurementInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		t.Fatalf("%s = %q, want a non-negative integer", name, value)
	}
	return parsed
}

func percentile(samples []time.Duration, p float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	ordered := append([]time.Duration(nil), samples...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	index := int(float64(len(ordered))*p+.999999) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(ordered) {
		index = len(ordered) - 1
	}
	return ordered[index]
}
