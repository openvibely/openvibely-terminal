package client

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestGetUsageAnalytics(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/analytics/usage" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("project_id"); got != "p1" {
			t.Errorf("project_id = %q", got)
		}
		json.NewEncoder(w).Encode(UsageAnalytics{
			Totals:         UsageTotals{CallCount: 10, TotalTokens: 5000, CostUSD: 0.42, CostAvailable: true},
			ModelBreakdown: []ModelUsagePoint{{Model: "gpt-x", CallCount: 10, Percent: 100}},
		})
	}))

	u, err := c.GetUsageAnalytics(context.Background(), "p1")
	if err != nil {
		t.Fatalf("GetUsageAnalytics: %v", err)
	}
	if u.Totals.CallCount != 10 || len(u.ModelBreakdown) != 1 {
		t.Errorf("unexpected usage: %+v", u)
	}
}

func TestGetSkillAnalytics(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/analytics/skills" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(SkillAnalytics{
			TopSkills: []SkillMetric{{SkillHandle: "debug_go_tests", ActivityCount: 7}},
		})
	}))

	s, err := c.GetSkillAnalytics(context.Background(), "")
	if err != nil {
		t.Fatalf("GetSkillAnalytics: %v", err)
	}
	if len(s.TopSkills) != 1 || s.TopSkills[0].SkillHandle != "debug_go_tests" {
		t.Errorf("unexpected skills: %+v", s)
	}
}

func TestGetUsageAnalyticsError(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	}))

	u, err := c.GetUsageAnalytics(context.Background(), "")
	if err == nil {
		t.Fatal("expected error")
	}
	if u != nil {
		t.Errorf("expected nil pointer on error, got %+v", u)
	}
}

func TestGetSkillAnalyticsWithProjectID(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/analytics/skills" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("project_id"); got != "p1" {
			t.Errorf("project_id = %q", got)
		}
		json.NewEncoder(w).Encode(SkillAnalytics{
			TopSkills: []SkillMetric{{SkillHandle: "openvibely_backend_client", ActivityCount: 3}},
		})
	}))

	s, err := c.GetSkillAnalytics(context.Background(), "p1")
	if err != nil {
		t.Fatalf("GetSkillAnalytics: %v", err)
	}
	if len(s.TopSkills) != 1 || s.TopSkills[0].SkillHandle != "openvibely_backend_client" {
		t.Errorf("unexpected skills: %+v", s)
	}
}

func TestGetSkillAnalyticsError(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	}))

	s, err := c.GetSkillAnalytics(context.Background(), "")
	if err == nil {
		t.Fatal("expected error")
	}
	if s != nil {
		t.Errorf("expected nil pointer on error, got %+v", s)
	}
}

func TestGetModelCapacities(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/capacity/models" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode([]ModelCapacity{{ID: "m1", Name: "Sonnet", Running: 1, HasCapacity: true}})
	}))

	caps, err := c.GetModelCapacities(context.Background())
	if err != nil {
		t.Fatalf("GetModelCapacities: %v", err)
	}
	if len(caps) != 1 || caps[0].Name != "Sonnet" {
		t.Errorf("unexpected caps: %+v", caps)
	}
}

func TestGetBestAgent(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"agent":   map[string]string{"name": "Claude", "model": "claude-sonnet"},
			"metrics": AgentMetric{TaskType: "general", AvgQualityScore: 0.9},
		})
	}))

	rec, err := c.GetBestAgent(context.Background(), "general")
	if err != nil {
		t.Fatalf("GetBestAgent: %v", err)
	}
	if rec.AgentName() != "Claude" {
		t.Errorf("AgentName = %q", rec.AgentName())
	}
	if rec.Metrics == nil || rec.Metrics.AvgQualityScore != 0.9 {
		t.Errorf("metrics = %+v", rec.Metrics)
	}
}

func TestGetBestAgentNoData(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"message": "No performance data available"})
	}))

	rec, err := c.GetBestAgent(context.Background(), "")
	if err != nil {
		t.Fatalf("GetBestAgent: %v", err)
	}
	if rec.Message == "" {
		t.Error("expected message for no-data response")
	}
}

func TestTriggerAutonomousBuild(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/autonomous/trigger" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("project_id"); got != "p1" {
			t.Errorf("project_id = %q", got)
		}
		w.Write([]byte("<div>ok</div>")) // backend responds with HTML
	}))

	if err := c.TriggerAutonomousBuild(context.Background(), "p1"); err != nil {
		t.Fatalf("TriggerAutonomousBuild: %v", err)
	}
}

func TestPostJSONServerError(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "autonomous build service not available"})
	}))

	err := c.TriggerAutonomousBuild(context.Background(), "p1")
	if err == nil {
		t.Fatal("expected error")
	}
	if !contains(err.Error(), "autonomous build service not available") {
		t.Errorf("error = %v", err)
	}
}

func TestListTaskLifecycleExecutions(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tasks/t1/lifecycle-executions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode([]LifecycleExecution{{ID: "le1", SkillKey: "router", Status: "completed"}})
	}))

	execs, err := c.ListTaskLifecycleExecutions(context.Background(), "t1")
	if err != nil {
		t.Fatalf("ListTaskLifecycleExecutions: %v", err)
	}
	if len(execs) != 1 || execs[0].SkillKey != "router" {
		t.Errorf("unexpected execs: %+v", execs)
	}
}

func TestGetLifecycleExecutionEvents(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/lifecycle-executions/exec-1/events" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode([]LifecycleEvent{{
			ID:        "event-1",
			Seq:       1,
			EventType: "started",
			Payload:   map[string]any{"message": "ok"},
			CreatedAt: "2026-01-20T10:00:00Z",
		}})
	}))

	events, err := c.GetLifecycleExecutionEvents(context.Background(), "exec-1")
	if err != nil {
		t.Fatalf("GetLifecycleExecutionEvents: %v", err)
	}
	if len(events) != 1 || events[0].EventType != "started" || events[0].Payload["message"] != "ok" {
		t.Errorf("unexpected events: %+v", events)
	}
}

func TestLifecycleEventJSONUsesSnakeCaseTags(t *testing.T) {
	encoded, err := json.Marshal(LifecycleEvent{
		ID:        "event-1",
		Seq:       2,
		EventType: "completed",
		Payload:   map[string]any{"ok": true},
		CreatedAt: "2026-01-20T10:00:01Z",
	})
	if err != nil {
		t.Fatalf("marshal lifecycle event: %v", err)
	}
	got := string(encoded)
	for _, want := range []string{`"event_type"`, `"created_at"`, `"payload"`} {
		if !contains(got, want) {
			t.Errorf("JSON missing %s: %s", want, got)
		}
	}
	if contains(got, `"EventType"`) || contains(got, `"CreatedAt"`) {
		t.Errorf("JSON used Go field names: %s", got)
	}
}
func TestGetAvgExecutionTimeByTask(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/analytics/avg-execution-time-by-task" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("project_id"); got != "p1" {
			t.Errorf("project_id = %q", got)
		}
		json.NewEncoder(w).Encode([]AvgExecutionTime{
			{ID: "t1", Name: "Triage", AvgMs: 1234.5, Count: 10},
		})
	}))

	items, err := c.GetAvgExecutionTimeByTask(context.Background(), "p1")
	if err != nil {
		t.Fatalf("GetAvgExecutionTimeByTask: %v", err)
	}
	if len(items) != 1 || items[0].Name != "Triage" {
		t.Errorf("unexpected items: %+v", items)
	}
}

func TestGetAvgExecutionTimeByTaskUnauthorized(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	}))

	_, err := c.GetAvgExecutionTimeByTask(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for unauthorized response")
	}
}

func TestGetAvgExecutionTimeByAgent(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/analytics/avg-execution-time-by-agent" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("project_id"); got != "p2" {
			t.Errorf("project_id = %q", got)
		}
		json.NewEncoder(w).Encode([]AvgExecutionTime{
			{ID: "a1", Name: "claude-sonnet", AvgMs: 800.0, Count: 5},
		})
	}))

	items, err := c.GetAvgExecutionTimeByAgent(context.Background(), "p2")
	if err != nil {
		t.Fatalf("GetAvgExecutionTimeByAgent: %v", err)
	}
	if len(items) != 1 || items[0].Name != "claude-sonnet" {
		t.Errorf("unexpected items: %+v", items)
	}
}

func TestGetAvgExecutionTimeByAgentUnauthorized(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	}))

	_, err := c.GetAvgExecutionTimeByAgent(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for unauthorized response")
	}
}

func TestGetSuccessFailureRates(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/analytics/success-failure-rates" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("project_id"); got != "p1" {
			t.Errorf("project_id = %q", got)
		}
		json.NewEncoder(w).Encode([]SuccessFailureRate{
			{Period: "2024-01", SuccessCount: 8, FailureCount: 2, TotalCount: 10, SuccessRate: 0.8},
		})
	}))

	items, err := c.GetSuccessFailureRates(context.Background(), "p1")
	if err != nil {
		t.Fatalf("GetSuccessFailureRates: %v", err)
	}
	if len(items) != 1 || items[0].Period != "2024-01" || items[0].SuccessRate != 0.8 {
		t.Errorf("unexpected items: %+v", items)
	}
}

func TestGetSuccessFailureRatesUnauthorized(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	}))

	_, err := c.GetSuccessFailureRates(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for unauthorized response")
	}
}

func TestGetMostFrequentTasks(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/analytics/most-frequent-tasks" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("project_id"); got != "p2" {
			t.Errorf("project_id = %q", got)
		}
		json.NewEncoder(w).Encode([]TaskFrequency{
			{TaskID: "t1", TaskTitle: "Triage", ExecutionCount: 42},
		})
	}))

	items, err := c.GetMostFrequentTasks(context.Background(), "p2")
	if err != nil {
		t.Fatalf("GetMostFrequentTasks: %v", err)
	}
	if len(items) != 1 || items[0].TaskTitle != "Triage" || items[0].ExecutionCount != 42 {
		t.Errorf("unexpected items: %+v", items)
	}
}

func TestGetMostFrequentTasksUnauthorized(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	}))

	_, err := c.GetMostFrequentTasks(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for unauthorized response")
	}
}

func TestGetFailedTaskPatterns(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/analytics/failed-task-patterns" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("project_id"); got != "p3" {
			t.Errorf("project_id = %q", got)
		}
		json.NewEncoder(w).Encode([]FailedTaskPattern{
			{TaskID: "t2", TaskTitle: "Deploy", FailureCount: 3, LastError: "timeout"},
		})
	}))

	items, err := c.GetFailedTaskPatterns(context.Background(), "p3")
	if err != nil {
		t.Fatalf("GetFailedTaskPatterns: %v", err)
	}
	if len(items) != 1 || items[0].TaskTitle != "Deploy" || items[0].LastError != "timeout" {
		t.Errorf("unexpected items: %+v", items)
	}
}

func TestGetFailedTaskPatternsUnauthorized(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	}))

	_, err := c.GetFailedTaskPatterns(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for unauthorized response")
	}
}
