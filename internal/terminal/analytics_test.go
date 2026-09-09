package terminal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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

func TestLoadAnalyticsRendersIndependentAgentAndTaskExecutionTimes(t *testing.T) {
	execTimes := func(prefix string, count int) []client.AvgExecutionTime {
		items := make([]client.AvgExecutionTime, count)
		for i := range items {
			items[i] = client.AvgExecutionTime{
				ID:    prefix + "-" + strconv.Itoa(i),
				Name:  prefix + "-" + strconv.Itoa(i),
				AvgMs: float64(i),
			}
		}
		return items
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/analytics/avg-execution-time-by-agent":
			_ = json.NewEncoder(w).Encode(execTimes("agent", 13))
		case "/api/analytics/avg-execution-time-by-task":
			_ = json.NewEncoder(w).Encode(execTimes("task", 14))
		case "/api/analytics/usage":
			_ = json.NewEncoder(w).Encode(client.UsageAnalytics{})
		case "/api/analytics/success-failure-rates":
			_ = json.NewEncoder(w).Encode([]client.SuccessFailureRate{})
		case "/api/analytics/most-frequent-tasks":
			_ = json.NewEncoder(w).Encode([]client.TaskFrequency{})
		case "/api/analytics/failed-task-patterns":
			_ = json.NewEncoder(w).Encode([]client.FailedTaskPattern{})
		case "/api/analytics/skills":
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

	agentOut, err := loadAnalytics(context.Background(), c, "project-1", "agents")
	if err != nil {
		t.Fatalf("loadAnalytics agents: %v", err)
	}
	if !strings.Contains(agentOut, "\n  agent-12 ") || strings.Contains(agentOut, "\n  agent-0 ") {
		t.Fatalf("agent section did not render its independent top 12:\n%s", agentOut)
	}
	if strings.Contains(agentOut, "task-") {
		t.Fatalf("agent section rendered task records:\n%s", agentOut)
	}

	taskOut, err := loadAnalytics(context.Background(), c, "project-1", "trends")
	if err != nil {
		t.Fatalf("loadAnalytics trends: %v", err)
	}
	if !strings.Contains(taskOut, "\n  task-13 ") || strings.Contains(taskOut, "\n  task-0 ") || strings.Contains(taskOut, "agent-") {
		t.Fatalf("task section did not render its independent top 12:\n%s", taskOut)
	}

	allOut, err := loadAnalytics(context.Background(), c, "project-1", "")
	if err != nil {
		t.Fatalf("loadAnalytics all sections: %v", err)
	}
	for _, want := range []string{"Avg execution time by agent", "Avg execution time by task", "agent-12", "task-13"} {
		if !strings.Contains(allOut, want) {
			t.Fatalf("full analytics output missing %q:\n%s", want, allOut)
		}
	}
	if strings.Index(allOut, "Avg execution time by agent") > strings.Index(allOut, "Avg execution time by task") {
		t.Fatalf("execution-time sections changed order:\n%s", allOut)
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
