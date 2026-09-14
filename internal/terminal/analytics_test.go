package terminal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

// newAnalyticsTestClient starts an httptest server that serves all seven
// analytics endpoints. failPaths lists URL paths that should return a 500.
func newAnalyticsTestClient(t *testing.T, failPaths map[string]bool) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failPaths[r.URL.Path] {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		switch r.URL.Path {
		case "/api/analytics/usage":
			json.NewEncoder(w).Encode(client.UsageAnalytics{
				Totals: client.UsageTotals{CallCount: 1},
			})
		case "/api/analytics/success-failure-rates":
			json.NewEncoder(w).Encode([]client.SuccessFailureRate{})
		case "/api/analytics/avg-execution-time-by-agent":
			json.NewEncoder(w).Encode([]client.AvgExecutionTime{})
		case "/api/analytics/avg-execution-time-by-task":
			json.NewEncoder(w).Encode([]client.AvgExecutionTime{})
		case "/api/analytics/most-frequent-tasks":
			json.NewEncoder(w).Encode([]client.TaskFrequency{})
		case "/api/analytics/failed-task-patterns":
			json.NewEncoder(w).Encode([]client.FailedTaskPattern{})
		case "/api/analytics/skills":
			json.NewEncoder(w).Encode(client.SkillAnalytics{})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	return c
}

func TestLoadAnalyticsAllSectionsSucceed(t *testing.T) {
	c := newAnalyticsTestClient(t, nil)
	out, err := loadAnalytics(context.Background(), c, "", "")
	if err != nil {
		t.Fatalf("loadAnalytics: %v", err)
	}

	order := []string{
		"Usage & cost", "Success / failure", "Avg execution time by agent", "Avg execution time by task",
		"Most frequent tasks", "Failure patterns", "Skill usage",
	}
	prev := -1
	for _, s := range order {
		idx := strings.Index(out, s)
		if idx < 0 {
			t.Fatalf("missing section %q in output:\n%s", s, out)
		}
		if idx < prev {
			t.Fatalf("section %q out of order in output:\n%s", s, out)
		}
		prev = idx
	}
}

func TestLoadAnalyticsSkipsFailingSectionWhenAll(t *testing.T) {
	c := newAnalyticsTestClient(t, map[string]bool{"/api/analytics/usage": true})
	out, err := loadAnalytics(context.Background(), c, "", "")
	if err != nil {
		t.Fatalf("loadAnalytics: %v", err)
	}
	if strings.Contains(out, "Usage & cost") {
		t.Errorf("failing usage section should be omitted, got:\n%s", out)
	}
	if !strings.Contains(out, "Avg execution time by agent") {
		t.Errorf("other sections should still render, got:\n%s", out)
	}
}

func TestLoadAnalyticsSingleSectionErrorSurfaced(t *testing.T) {
	c := newAnalyticsTestClient(t, map[string]bool{"/api/analytics/usage": true})
	_, err := loadAnalytics(context.Background(), c, "", "usage")
	if err == nil {
		t.Fatal("expected error for failing named section, got nil")
	}
}

// boundedExecutionTimeResult models the endpoint contract: preserve source order
// for equal averages, order by average execution time descending, then limit.
func boundedExecutionTimeResult(history []client.AvgExecutionTime) []client.AvgExecutionTime {
	ordered := append([]client.AvgExecutionTime(nil), history...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].AvgMs > ordered[j].AvgMs
	})
	if len(ordered) > maxExecTimeRows {
		ordered = ordered[:maxExecTimeRows]
	}
	return ordered
}

func TestLoadAnalyticsRendersIndependentAgentAndTaskExecutionTimes(t *testing.T) {
	execTimes := func(prefix string) []client.AvgExecutionTime {
		return []client.AvgExecutionTime{
			{ID: prefix + "-1000", Name: prefix + "-1000", AvgMs: 1000},
			{ID: prefix + "-900", Name: prefix + "-900", AvgMs: 900},
			{ID: prefix + "-800", Name: prefix + "-800", AvgMs: 800},
			{ID: prefix + "-700", Name: prefix + "-700", AvgMs: 700},
			{ID: prefix + "-600", Name: prefix + "-600", AvgMs: 600},
			{ID: prefix + "-500", Name: prefix + "-500", AvgMs: 500},
			{ID: prefix + "-400", Name: prefix + "-400", AvgMs: 400},
			{ID: prefix + "-300", Name: prefix + "-300", AvgMs: 300},
			{ID: prefix + "-200", Name: prefix + "-200", AvgMs: 200},
			{ID: prefix + "-100", Name: prefix + "-100", AvgMs: 100},
			{ID: prefix + "-zero-id", AvgMs: 0},
			{ID: prefix + "-tie-early", Name: prefix + "-tie-early", AvgMs: -10},
			{ID: prefix + "-tie-late", Name: prefix + "-tie-late", AvgMs: -10},
			{ID: prefix + "-negative-20", Name: prefix + "-negative-20", AvgMs: -20},
			{ID: prefix + "-negative-30", Name: prefix + "-negative-30", AvgMs: -30},
		}
	}
	agents := execTimes("agent")
	tasks := execTimes("task")

	var mu sync.Mutex
	requests := make(map[string]int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("project_id"); got != "project-1" {
			t.Errorf("%s project_id = %q, want project-1", r.URL.Path, got)
		}
		switch r.URL.Path {
		case "/api/analytics/avg-execution-time-by-agent":
			if got := r.URL.Query().Get("limit"); got != strconv.Itoa(maxExecTimeRows) {
				t.Errorf("agent limit = %q, want %d", got, maxExecTimeRows)
			}
			mu.Lock()
			requests[r.URL.Path]++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(boundedExecutionTimeResult(agents))
		case "/api/analytics/avg-execution-time-by-task":
			if got := r.URL.Query().Get("limit"); got != strconv.Itoa(maxExecTimeRows) {
				t.Errorf("task limit = %q, want %d", got, maxExecTimeRows)
			}
			mu.Lock()
			requests[r.URL.Path]++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(boundedExecutionTimeResult(tasks))
		case "/api/analytics/usage":
			if got := r.URL.Query().Get("limit"); got != "" {
				t.Errorf("usage limit = %q, want omitted", got)
			}
			_ = json.NewEncoder(w).Encode(client.UsageAnalytics{})
		case "/api/analytics/success-failure-rates":
			if got := r.URL.Query().Get("limit"); got != "" {
				t.Errorf("rates limit = %q, want omitted", got)
			}
			_ = json.NewEncoder(w).Encode([]client.SuccessFailureRate{})
		case "/api/analytics/most-frequent-tasks":
			if got := r.URL.Query().Get("limit"); got != strconv.Itoa(maxFrequentRows) {
				t.Errorf("frequent limit = %q, want %d", got, maxFrequentRows)
			}
			_ = json.NewEncoder(w).Encode([]client.TaskFrequency{})
		case "/api/analytics/failed-task-patterns":
			if got := r.URL.Query().Get("limit"); got != "" {
				t.Errorf("failures limit = %q, want omitted", got)
			}
			_ = json.NewEncoder(w).Encode([]client.FailedTaskPattern{})
		case "/api/analytics/skills":
			if got := r.URL.Query().Get("limit"); got != "" {
				t.Errorf("skills limit = %q, want omitted", got)
			}
			_ = json.NewEncoder(w).Encode(client.SkillAnalytics{})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}

	wantAgent := renderExecTimes("Avg execution time by agent", agents)
	agentOut, err := loadAnalytics(context.Background(), c, "project-1", "agents")
	if err != nil {
		t.Fatalf("loadAnalytics agents: %v", err)
	}
	if agentOut != wantAgent {
		t.Fatalf("bounded agent output changed visible rows\n got: %s\nwant: %s", agentOut, wantAgent)
	}

	wantTask := renderExecTimes("Avg execution time by task", tasks)
	taskOut, err := loadAnalytics(context.Background(), c, "project-1", "trends")
	if err != nil {
		t.Fatalf("loadAnalytics trends: %v", err)
	}
	if taskOut != wantTask {
		t.Fatalf("bounded task output changed visible rows\n got: %s\nwant: %s", taskOut, wantTask)
	}

	for _, tc := range []struct {
		name string
		out  string
	}{
		{name: "agent", out: agentOut},
		{name: "task", out: taskOut},
	} {
		if !strings.Contains(tc.out, tc.name+"-zero-id") || !strings.Contains(tc.out, tc.name+"-tie-early") || !strings.Contains(tc.out, "-10ms") {
			t.Fatalf("%s output lost ID fallback, zero, or negative value:\n%s", tc.name, tc.out)
		}
		if strings.Contains(tc.out, tc.name+"-tie-late") {
			t.Fatalf("%s output did not preserve source-order cutoff tie:\n%s", tc.name, tc.out)
		}
	}

	allOut, err := loadAnalytics(context.Background(), c, "project-1", "")
	if err != nil {
		t.Fatalf("loadAnalytics all sections: %v", err)
	}
	if !strings.Contains(allOut, wantAgent) || !strings.Contains(allOut, wantTask) {
		t.Fatalf("all-section output changed bounded execution-time rows:\n%s", allOut)
	}
	if strings.Index(allOut, "Avg execution time by agent") > strings.Index(allOut, "Avg execution time by task") {
		t.Fatalf("execution-time sections changed order:\n%s", allOut)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, path := range []string{
		"/api/analytics/avg-execution-time-by-agent",
		"/api/analytics/avg-execution-time-by-task",
	} {
		if got := requests[path]; got != 2 {
			t.Errorf("%s request count = %d, want 2 (single section and all sections)", path, got)
		}
	}
}

func TestLoadAnalyticsPreservesExecutionTimeSectionErrorSemantics(t *testing.T) {
	cases := []struct {
		section string
		path    string
		title   string
	}{
		{section: "agents", path: "/api/analytics/avg-execution-time-by-agent", title: "Avg execution time by agent"},
		{section: "trends", path: "/api/analytics/avg-execution-time-by-task", title: "Avg execution time by task"},
		{section: "frequent", path: "/api/analytics/most-frequent-tasks", title: "Most frequent tasks"},
	}
	for _, tc := range cases {
		t.Run(tc.section, func(t *testing.T) {
			c := newAnalyticsTestClient(t, map[string]bool{tc.path: true})
			if _, err := loadAnalytics(context.Background(), c, "", tc.section); err == nil {
				t.Fatalf("named %s section should return its fetch error", tc.section)
			}

			c = newAnalyticsTestClient(t, map[string]bool{tc.path: true})
			out, err := loadAnalytics(context.Background(), c, "", "")
			if err != nil {
				t.Fatalf("all analytics with failing %s section: %v", tc.section, err)
			}
			if strings.Contains(out, tc.title) {
				t.Fatalf("failing %s section should be omitted from all-section output:\n%s", tc.section, out)
			}
		})
	}
}

func rankedFrequentTasks(history []client.TaskFrequency) []client.TaskFrequency {
	if len(history) == 0 {
		return make([]client.TaskFrequency, 0)
	}
	ranked := append([]client.TaskFrequency(nil), history...)
	sort.SliceStable(ranked, func(i, j int) bool {
		return ranked[i].ExecutionCount > ranked[j].ExecutionCount
	})
	if len(ranked) > maxFrequentRows {
		ranked = ranked[:maxFrequentRows]
	}
	return ranked
}

func TestLoadAnalyticsFrequentBoundPreservesVisibleOutput(t *testing.T) {
	cases := []struct {
		name    string
		records []client.TaskFrequency
	}{
		{name: "empty", records: []client.TaskFrequency{}},
		{name: "one", records: []client.TaskFrequency{{TaskID: "one", TaskTitle: "one", ExecutionCount: 4}}},
		{name: "exactly at limit", records: func() []client.TaskFrequency {
			records := make([]client.TaskFrequency, maxFrequentRows)
			for i := range records {
				records[i] = client.TaskFrequency{
					TaskID:         fmt.Sprintf("exact-%02d", i),
					TaskTitle:      fmt.Sprintf("exact title %02d", i),
					ExecutionCount: maxFrequentRows - i,
				}
			}
			return records
		}()},
		{name: "limit plus one and stable cutoff", records: func() []client.TaskFrequency {
			records := make([]client.TaskFrequency, 0, maxFrequentRows+1)
			for i := 0; i < maxFrequentRows-1; i++ {
				records = append(records, client.TaskFrequency{
					TaskID:         fmt.Sprintf("rank-%02d", i),
					TaskTitle:      fmt.Sprintf("ranked task %02d", i),
					ExecutionCount: 100 - i,
				})
			}
			records = append(records,
				client.TaskFrequency{TaskID: "tie-early", TaskTitle: "tie early", ExecutionCount: 50},
				client.TaskFrequency{TaskID: "tie-late", TaskTitle: "tie late", ExecutionCount: 50},
			)
			return records
		}()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ranked := rankedFrequentTasks(tc.records)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Query().Get("project_id"); got != "analytics-project" {
					t.Errorf("project_id = %q, want analytics-project", got)
				}
				if got := r.URL.Query().Get("limit"); got != strconv.Itoa(maxFrequentRows) {
					t.Errorf("limit = %q, want %d", got, maxFrequentRows)
				}
				if err := json.NewEncoder(w).Encode(ranked); err != nil {
					t.Errorf("encode ranked tasks: %v", err)
				}
			}))
			t.Cleanup(srv.Close)
			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatalf("client.New: %v", err)
			}

			got, err := loadAnalytics(context.Background(), c, "analytics-project", "frequent")
			if err != nil {
				t.Fatalf("loadAnalytics: %v", err)
			}
			want := renderFrequent(ranked)
			if got != want {
				t.Fatalf("frequent output changed for %s\n got: %q\nwant: %q", tc.name, got, want)
			}
			if len(ranked) > maxFrequentRows {
				t.Fatalf("backend-ranked response has %d rows, want at most %d", len(ranked), maxFrequentRows)
			}
			if tc.name == "limit plus one and stable cutoff" {
				plain := stripANSI(got)
				if !strings.Contains(plain, "tie early") || strings.Contains(plain, "tie late") {
					t.Fatalf("ranked stable cutoff changed:\n%s", plain)
				}
			}
		})
	}
}

func TestFrequentAnalyticsResponseRowsBoundedAcrossFixtureSizes(t *testing.T) {
	for _, size := range []int{0, 1, maxFrequentRows, maxFrequentRows + 1, 100, 1000, 5000} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			history := frequentAnalyticsFixture(size)
			ranked := rankedFrequentTasks(history)
			body, err := json.Marshal(ranked)
			if err != nil {
				t.Fatalf("marshal fixture: %v", err)
			}
			var gotBytes int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("project_id") != "fixture-project" {
					t.Errorf("project_id = %q, want fixture-project", r.URL.Query().Get("project_id"))
				}
				if got := r.URL.Query().Get("limit"); got != strconv.Itoa(maxFrequentRows) {
					t.Errorf("limit = %q, want %d", got, maxFrequentRows)
				}
				gotBytes = len(body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(body)
			}))
			t.Cleanup(srv.Close)
			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatalf("client.New: %v", err)
			}
			items, err := c.GetMostFrequentTasksWithLimit(context.Background(), "fixture-project", maxFrequentRows)
			if err != nil {
				t.Fatalf("GetMostFrequentTasksWithLimit: %v", err)
			}
			wantRows := size
			if wantRows > maxFrequentRows {
				wantRows = maxFrequentRows
			}
			if len(items) != wantRows {
				t.Fatalf("decoded rows = %d, want %d", len(items), wantRows)
			}
			if gotBytes != len(body) {
				t.Fatalf("response bytes = %d, want %d", gotBytes, len(body))
			}
			if rendered := renderFrequent(items); rendered != renderFrequent(ranked) {
				t.Fatalf("rendered rows differ from ranked response")
			}
		})
	}
}

func frequentAnalyticsFixture(count int) []client.TaskFrequency {
	items := make([]client.TaskFrequency, count)
	for i := range items {
		items[i] = client.TaskFrequency{
			TaskID:         fmt.Sprintf("task-%05d", i),
			TaskTitle:      fmt.Sprintf("task title %05d %s", i, strings.Repeat("x", 72)),
			ExecutionCount: count - i,
			LastExecutedAt: "2026-09-13T12:34:56Z",
		}
	}
	return items
}

func TestFrequentAnalyticsPerformanceEvidence(t *testing.T) {
	if os.Getenv("OPENVIBELY_ANALYTICS_PERF_EVIDENCE") != "1" {
		t.Skip("set OPENVIBELY_ANALYTICS_PERF_EVIDENCE=1 to run the 5,000-record analytics evidence harness")
	}
	const fixtureSize = 5000
	const runs = 20

	history := frequentAnalyticsFixture(fixtureSize)
	fullBody, err := json.Marshal(history)
	if err != nil {
		t.Fatalf("marshal full fixture: %v", err)
	}
	bounded := rankedFrequentTasks(history)
	boundedBody, err := json.Marshal(bounded)
	if err != nil {
		t.Fatalf("marshal bounded fixture: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("project_id"); got != "perf-project" {
			t.Errorf("project_id = %q, want perf-project", got)
		}
		var body []byte
		switch r.URL.Query().Get("limit") {
		case strconv.Itoa(maxFrequentRows):
			body = boundedBody
		case "0":
			body = fullBody
		default:
			t.Errorf("unexpected limit %q", r.URL.Query().Get("limit"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}

	run := func(isBounded bool) (int, time.Duration, error) {
		started := time.Now()
		var items []client.TaskFrequency
		var err error
		if isBounded {
			items, err = c.GetMostFrequentTasksWithLimit(context.Background(), "perf-project", maxFrequentRows)
		} else {
			items, err = c.GetMostFrequentTasks(context.Background(), "perf-project")
		}
		if err != nil {
			return 0, 0, err
		}
		_ = renderFrequent(items)
		return len(items), time.Since(started), nil
	}

	for _, boundedRun := range []bool{true, false} {
		if _, _, err := run(boundedRun); err != nil {
			t.Fatalf("warm-up bounded=%t: %v", boundedRun, err)
		}
	}
	boundedDurations := make([]time.Duration, 0, runs)
	fullDurations := make([]time.Duration, 0, runs)
	for i := 0; i < runs; i++ {
		boundedRows, elapsed, err := run(true)
		if err != nil {
			t.Fatalf("bounded run %d: %v", i+1, err)
		}
		if boundedRows != maxFrequentRows {
			t.Fatalf("bounded decoded rows = %d, want %d", boundedRows, maxFrequentRows)
		}
		boundedDurations = append(boundedDurations, elapsed)

		fullRows, elapsed, err := run(false)
		if err != nil {
			t.Fatalf("full run %d: %v", i+1, err)
		}
		if fullRows != fixtureSize {
			t.Fatalf("full decoded rows = %d, want %d", fullRows, fixtureSize)
		}
		fullDurations = append(fullDurations, elapsed)
	}

	boundedAllocs := testing.AllocsPerRun(runs, func() {
		if _, _, err := run(true); err != nil {
			panic(err)
		}
	})
	fullAllocs := testing.AllocsPerRun(runs, func() {
		if _, _, err := run(false); err != nil {
			panic(err)
		}
	})
	boundedAllocatedBytes, boundedMallocs := measureFrequentAnalyticsAllocations(runs, func() {
		if _, _, err := run(true); err != nil {
			panic(err)
		}
	})
	fullAllocatedBytes, fullMallocs := measureFrequentAnalyticsAllocations(runs, func() {
		if _, _, err := run(false); err != nil {
			panic(err)
		}
	})

	boundedP50, boundedP95 := frequentAnalyticsPercentiles(boundedDurations)
	fullP50, fullP95 := frequentAnalyticsPercentiles(fullDurations)
	t.Logf("frequent_analytics_perf fixture=%d limit=%d runs=%d bounded_response_bytes=%d full_response_bytes=%d bounded_p50=%s bounded_p95=%s full_p50=%s full_p95=%s bounded_decoded_rows=%d full_decoded_rows=%d bounded_allocated_bytes_per_op=%.0f full_allocated_bytes_per_op=%.0f bounded_allocs_per_op=%.1f full_allocs_per_op=%.1f bounded_mallocs_per_op=%.1f full_mallocs_per_op=%.1f bounded_durations=%v full_durations=%v", fixtureSize, maxFrequentRows, runs, len(boundedBody), len(fullBody), boundedP50, boundedP95, fullP50, fullP95, len(bounded), len(history), boundedAllocatedBytes, fullAllocatedBytes, boundedAllocs, fullAllocs, boundedMallocs, fullMallocs, boundedDurations, fullDurations)

	if len(boundedBody) >= len(fullBody) || len(bounded) > maxFrequentRows {
		t.Fatalf("bounded response was not bounded: bytes=%d/%d rows=%d", len(boundedBody), len(fullBody), len(bounded))
	}
	if boundedP50*2 >= 15*time.Millisecond+800*time.Microsecond {
		t.Fatalf("bounded p50 = %s, want below 50%% of the reviewed 15.8ms baseline", boundedP50)
	}
	if boundedP50*2 >= fullP50 {
		t.Fatalf("bounded p50 = %s, want at least 50%% below full p50 %s", boundedP50, fullP50)
	}
	if boundedAllocatedBytes*2 >= fullAllocatedBytes || boundedMallocs*2 >= fullMallocs {
		t.Fatalf("bounded allocations were not materially reduced: bytes/op %.0f vs %.0f, mallocs/op %.1f vs %.1f", boundedAllocatedBytes, fullAllocatedBytes, boundedMallocs, fullMallocs)
	}
}

func measureFrequentAnalyticsAllocations(runs int, operation func()) (bytesPerOp, mallocsPerOp float64) {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < runs; i++ {
		operation()
	}
	runtime.ReadMemStats(&after)
	return float64(after.TotalAlloc-before.TotalAlloc) / float64(runs), float64(after.Mallocs-before.Mallocs) / float64(runs)
}

func frequentAnalyticsPercentiles(values []time.Duration) (time.Duration, time.Duration) {
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2], sorted[(len(sorted)*95)/100]
}
