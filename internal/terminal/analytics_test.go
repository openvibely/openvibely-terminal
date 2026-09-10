package terminal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

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
			if got := r.URL.Query().Get("limit"); got != "" {
				t.Errorf("frequent limit = %q, want omitted", got)
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
