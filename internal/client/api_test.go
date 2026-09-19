package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
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

func TestGetUsageProviderLimitsRequestsProjectionAndDecodesAccountLimits(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/analytics/usage" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("project_id"); got != "p1" {
			t.Errorf("project_id = %q", got)
		}
		if got := r.URL.Query().Get("projection"); got != "account_limits" {
			t.Errorf("projection = %q, want account_limits", got)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"totals":          UsageTotals{CallCount: 5000},
			"model_breakdown": []ModelUsagePoint{{Provider: "openai", Model: "gpt-x", CallCount: 5000}},
			"account_limits": []AccountUsage{{
				Provider:    "OpenAI",
				StatusLabel: "healthy",
				Limits:      []AccountLimit{{Label: "requests", UsedPercent: 42.5}},
			}},
		})
	}))

	limits, err := c.GetUsageProviderLimits(context.Background(), "p1")
	if err != nil {
		t.Fatalf("GetUsageProviderLimits: %v", err)
	}
	if len(limits.AccountLimits) != 1 || limits.AccountLimits[0].Provider != "OpenAI" || limits.AccountLimits[0].StatusLabel != "healthy" {
		t.Fatalf("unexpected provider limits: %+v", limits)
	}
}

func TestUsageProviderLimitsProjectionLargeFixtureMeasurement(t *testing.T) {
	fullPayload, compactPayload := usageProviderLimitFixturePayloads(t, 5000)
	if got, wantMax := len(compactPayload), len(fullPayload)/10; got > wantMax {
		t.Fatalf("compact response bytes = %d, want <= 10%% of full response bytes %d", got, len(fullPayload))
	}

	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/analytics/usage" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("projection") == "account_limits" {
			_, _ = w.Write(compactPayload)
			return
		}
		_, _ = w.Write(fullPayload)
	}))

	fullStats := measureUsageAnalyticsCall(t, 24, func(ctx context.Context) error {
		_, err := c.GetUsageAnalytics(ctx, "p1")
		return err
	})
	compactStats := measureUsageAnalyticsCall(t, 24, func(ctx context.Context) error {
		_, err := c.GetUsageProviderLimits(ctx, "p1")
		return err
	})

	t.Logf("usage provider-limit projection fixture rows=5000 full_bytes=%d compact_bytes=%d reduction=%.1f%%", len(fullPayload), len(compactPayload), 100*(1-float64(len(compactPayload))/float64(len(fullPayload))))
	t.Logf("full usage path: p50=%s p95=%s alloc_p50=%dB alloc_p95=%dB", fullStats.p50, fullStats.p95, fullStats.allocP50, fullStats.allocP95)
	t.Logf("compact provider-limit path: p50=%s p95=%s alloc_p50=%dB alloc_p95=%dB", compactStats.p50, compactStats.p95, compactStats.allocP50, compactStats.allocP95)

	if compactStats.p50 >= fullStats.p50 {
		t.Fatalf("compact p50 latency = %s, want below full p50 %s", compactStats.p50, fullStats.p50)
	}
	if compactStats.allocP50 >= fullStats.allocP50 {
		t.Fatalf("compact p50 allocated bytes = %d, want below full p50 allocated bytes %d", compactStats.allocP50, fullStats.allocP50)
	}
}

func usageProviderLimitFixturePayloads(t *testing.T, rows int) ([]byte, []byte) {
	t.Helper()
	accountLimits := `[{"provider":"OpenAI","plan_type":"team","status_label":"healthy","primary_limit":{"label":"requests","status":"healthy","used_percent":42.5,"resets_at":"2026-09-17T00:00:00Z"},"limits":[{"label":"requests","status":"healthy","used_percent":42.5,"resets_at":"2026-09-17T00:00:00Z"}]}]`

	var full bytes.Buffer
	full.WriteString(`{"totals":{"call_count":5000,"input_tokens":123456,"output_tokens":654321,"total_tokens":777777,"cached_input_tokens":111,"cost_usd":12.34,"cost_available":true},"model_breakdown":[`)
	for i := 0; i < rows; i++ {
		if i > 0 {
			full.WriteByte(',')
		}
		fmt.Fprintf(&full, `{"provider":"openai","model":"fixture-model-%04d","call_count":%d,"total_tokens":%d,"cost_usd":%.4f,"percent":%.6f}`, i, i+1, (i+1)*100, float64(i+1)/1000, float64(i+1)/float64(rows))
	}
	full.WriteString(`],"account_limits":`)
	full.WriteString(accountLimits)
	full.WriteString(`,"errors":[],"last_updated_at":"2026-09-17T00:00:00Z"}`)

	compact := []byte(`{"account_limits":` + accountLimits + `,"errors":[],"last_updated_at":"2026-09-17T00:00:00Z"}`)
	if !json.Valid(full.Bytes()) {
		t.Fatal("full fixture is not valid JSON")
	}
	if !json.Valid(compact) {
		t.Fatal("compact fixture is not valid JSON")
	}
	return full.Bytes(), compact
}

type usageMeasurementStats struct {
	p50      time.Duration
	p95      time.Duration
	allocP50 uint64
	allocP95 uint64
}

func measureUsageAnalyticsCall(t *testing.T, samples int, call func(context.Context) error) usageMeasurementStats {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := call(ctx); err != nil {
			t.Fatalf("warm usage call failed: %v", err)
		}
	}

	durations := make([]time.Duration, 0, samples)
	allocs := make([]uint64, 0, samples)
	for i := 0; i < samples; i++ {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		if err := call(ctx); err != nil {
			t.Fatalf("measured usage call failed: %v", err)
		}
		durations = append(durations, time.Since(start))
		runtime.ReadMemStats(&after)
		allocs = append(allocs, after.TotalAlloc-before.TotalAlloc)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	sort.Slice(allocs, func(i, j int) bool { return allocs[i] < allocs[j] })
	return usageMeasurementStats{
		p50:      percentileDuration(durations, 50),
		p95:      percentileDuration(durations, 95),
		allocP50: percentileUint64(allocs, 50),
		allocP95: percentileUint64(allocs, 95),
	}
}

func percentileDuration(values []time.Duration, pct int) time.Duration {
	if len(values) == 0 {
		return 0
	}
	idx := (len(values)*pct + 99) / 100
	if idx < 1 {
		idx = 1
	}
	if idx > len(values) {
		idx = len(values)
	}
	return values[idx-1]
}

func percentileUint64(values []uint64, pct int) uint64 {
	if len(values) == 0 {
		return 0
	}
	idx := (len(values)*pct + 99) / 100
	if idx < 1 {
		idx = 1
	}
	if idx > len(values) {
		idx = len(values)
	}
	return values[idx-1]
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

func TestGetVoteRecordsUsesEscapedStepExecutionRoute(t *testing.T) {
	const stepExecID = "step/exec?audit#1"
	var requests int
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if got, want := r.URL.EscapedPath(), "/api/workflows/votes/step%2Fexec%3Faudit%231"; got != want {
			t.Errorf("escaped path = %q, want %q (raw URI %q)", got, want, r.URL.RequestURI())
		}
		if r.URL.RawQuery != "" {
			t.Errorf("unexpected query %q", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode([]VoteRecord{
			{ID: "vote-1", StepExecutionID: stepExecID, AgentConfigID: "agent-a", Vote: "approve", Confidence: 0.91, Reasoning: "looks safe"},
			{ID: "vote-2", StepExecutionID: stepExecID, AgentConfigID: "agent-b", Vote: "reject", Confidence: 0.42, Reasoning: "needs more evidence"},
		})
	}))

	records, err := c.GetVoteRecords(context.Background(), stepExecID)
	if err != nil {
		t.Fatalf("GetVoteRecords: %v", err)
	}
	if requests != 1 {
		t.Fatalf("GetVoteRecords made %d requests, want exactly one", requests)
	}
	if len(records) != 2 {
		t.Fatalf("decoded %d vote records, want 2: %+v", len(records), records)
	}
	if records[0].AgentConfigID != "agent-a" || records[0].Vote != "approve" || records[0].Confidence != 0.91 || records[0].Reasoning != "looks safe" {
		t.Errorf("first record = %+v", records[0])
	}
	if records[1].AgentConfigID != "agent-b" || records[1].Vote != "reject" || records[1].Confidence != 0.42 || records[1].Reasoning != "needs more evidence" {
		t.Errorf("second record = %+v", records[1])
	}
}

func TestGetVoteRecordsNormalizesEmptyJSONCollection(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/api/workflows/votes/step-empty" {
			t.Errorf("escaped path = %q", r.URL.EscapedPath())
		}
		_, _ = w.Write([]byte("null"))
	}))

	records, err := c.GetVoteRecords(context.Background(), "step-empty")
	if err != nil {
		t.Fatalf("GetVoteRecords: %v", err)
	}
	if records == nil {
		t.Fatal("empty vote collection must be non-nil for stable JSON output")
	}
	if len(records) != 0 {
		t.Fatalf("empty vote collection length = %d, want 0", len(records))
	}
}

func TestPostJSONServerError(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "mutation service not available"})
	}))

	err := c.postJSON(context.Background(), "/test-mutation", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !contains(err.Error(), "mutation service not available") {
		t.Errorf("error = %v", err)
	}
}

func TestPostJSONNonLoginRedirectIsNotAuthentication(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/test-mutation" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Location", "/after-mutation")
		w.WriteHeader(http.StatusFound)
	}))

	err := c.postJSON(context.Background(), "/test-mutation", nil, nil)
	if err == nil {
		t.Fatal("expected non-login redirect error")
	}
	if IsAuthRequired(err) {
		t.Fatalf("ordinary non-login redirect was classified as authentication: %v", err)
	}
}

func TestPostJSONLoginResponsesAreAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        int
		loginRedirect bool
	}{
		{name: "unauthorized", status: http.StatusUnauthorized},
		{name: "login redirect", status: http.StatusFound, loginRedirect: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.loginRedirect {
					w.Header().Set("Location", "/login?next=%2Ftest-mutation")
				}
				w.WriteHeader(tc.status)
			}))

			err := c.postJSON(context.Background(), "/test-mutation", nil, nil)
			if err == nil || !IsAuthRequired(err) {
				t.Fatalf("error = %v, want authentication-required", err)
			}
		})
	}
}

func TestPostJSONNearLoginRedirectsAreNotAuthentication(t *testing.T) {
	for _, location := range []string{"/login-help", "/login2", "/after-mutation"} {
		location := location
		t.Run(location, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", location)
				w.WriteHeader(http.StatusFound)
			}))

			err := c.postJSON(context.Background(), "/test-mutation", nil, nil)
			if err == nil || IsAuthRequired(err) {
				t.Fatalf("redirect %q error = %v, want ordinary mutation error", location, err)
			}
		})
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

func TestListTaskLifecycleExecutionPageForProject(t *testing.T) {
	const response = `{
		"items":[{
			"id":"le2","skill_key":"reviewer","when":"after_complete","status":"running",
			"agent_id":"agent-2","output_contract":"learning_summary",
			"started_at":"2026-09-08T10:00:00Z","selected_skills":["review"],
			"selected_memories":[{"file":"review.md","topic":"Review","summary":"Check the diff","snippet":"bounded evidence"}]
		}],
		"has_more":true,"next_cursor":"cursor-2"
	}`
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tasks/t1/lifecycle-executions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("project_id"); got != "p2" {
			t.Errorf("project_id = %q, want p2", got)
		}
		_, _ = w.Write([]byte(response))
	}))

	page, err := c.ListTaskLifecycleExecutionPageForProject(context.Background(), "t1", "p2")
	if err != nil {
		t.Fatalf("ListTaskLifecycleExecutionPageForProject: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "le2" || !page.HasMore || page.NextCursor != "cursor-2" {
		t.Fatalf("unexpected page: %+v", page)
	}
	if page.Items[0].OutputContract != "learning_summary" || len(page.Items[0].SelectedMemories) != 1 || page.Items[0].SelectedMemories[0].Topic != "Review" {
		t.Fatalf("documented execution fields were not decoded: %+v", page.Items[0])
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("marshal page: %v", err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(response)); err != nil {
		t.Fatalf("compact fixture: %v", err)
	}
	if string(encoded) != compact.String() {
		t.Fatalf("page JSON did not preserve the complete response\ngot:  %s\nwant: %s", encoded, compact.String())
	}
}

func TestListTaskLifecycleExecutionPageLegacyArray(t *testing.T) {
	const response = `[{"id":"legacy-1","skill_key":"router","status":"completed"}]`
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(response))
	}))

	page, err := c.ListTaskLifecycleExecutionPageForProject(context.Background(), "t1", "p2")
	if err != nil {
		t.Fatalf("legacy array: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "legacy-1" || page.HasMore || page.NextCursor != "" {
		t.Fatalf("legacy page = %+v", page)
	}
	encoded, err := json.Marshal(page)
	if err != nil || string(encoded) != response {
		t.Fatalf("legacy JSON = %s, %v; want %s", encoded, err, response)
	}
}

func TestListTaskLifecycleExecutionPageRejectsMalformedPayloads(t *testing.T) {
	for _, payload := range []string{
		`null`,
		`[null]`,
		`{}`,
		`{"items":null,"has_more":false}`,
		`{"items":[null],"has_more":false}`,
		`{"items":{},"has_more":false}`,
		`{"items":[],"has_more":"yes"}`,
		`{"items":[],"has_more":true,"next_cursor":7}`,
		`{"items":[],"has_more":false} trailing`,
	} {
		t.Run(payload, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(payload))
			}))
			if _, err := c.ListTaskLifecycleExecutionPageForProject(context.Background(), "t1", "p2"); err == nil {
				t.Fatalf("payload %q unexpectedly decoded", payload)
			}
		})
	}
}

func TestListTaskLifecycleExecutionPagePreservesHTTPAndAuthErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		location   string
		body       string
		wantAuth   bool
		wantDetail string
	}{
		{name: "api error", status: http.StatusBadGateway, body: `{"error":"lifecycle unavailable"}`, wantDetail: "lifecycle unavailable"},
		{name: "unauthorized", status: http.StatusUnauthorized, wantAuth: true},
		{name: "login redirect", status: http.StatusFound, location: "/login?next=%2Ftasks", wantAuth: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.location != "" {
					w.Header().Set("Location", tc.location)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			_, err := c.ListTaskLifecycleExecutionPageForProject(context.Background(), "t1", "p2")
			if err == nil || IsAuthRequired(err) != tc.wantAuth {
				t.Fatalf("error = %v, auth = %t; want auth %t", err, IsAuthRequired(err), tc.wantAuth)
			}
			if tc.wantDetail != "" && !strings.Contains(err.Error(), tc.wantDetail) {
				t.Fatalf("error %q missing %q", err, tc.wantDetail)
			}
		})
	}
}

func TestListTaskLifecycleExecutionPagePreservesCancellation(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[],"has_more":false}`))
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.ListTaskLifecycleExecutionPageForProject(ctx, "t1", "p2")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestListTaskLifecycleExecutionsForProject(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tasks/t1/lifecycle-executions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("project_id"); got != "p2" {
			t.Errorf("project_id = %q, want p2", got)
		}
		json.NewEncoder(w).Encode([]LifecycleExecution{{ID: "le2", SkillKey: "reviewer", Status: "running"}})
	}))

	execs, err := c.ListTaskLifecycleExecutionsForProject(context.Background(), "t1", "p2")
	if err != nil {
		t.Fatalf("ListTaskLifecycleExecutionsForProject: %v", err)
	}
	if len(execs) != 1 || execs[0].ID != "le2" {
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

func TestGetLifecycleExecutionEventsForProject(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/lifecycle-executions/exec-2/events" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("project_id"); got != "p2" {
			t.Errorf("project_id = %q, want p2", got)
		}
		json.NewEncoder(w).Encode([]LifecycleEvent{{
			ID:        "event-2",
			Seq:       1,
			EventType: "completed",
			Payload:   map[string]any{"ok": true},
		}})
	}))

	events, err := c.GetLifecycleExecutionEventsForProject(context.Background(), "exec-2", "p2")
	if err != nil {
		t.Fatalf("GetLifecycleExecutionEventsForProject: %v", err)
	}
	if len(events) != 1 || events[0].ID != "event-2" {
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
		if got := r.URL.Query().Get("limit"); got != "" {
			t.Errorf("unbounded request limit = %q, want omitted", got)
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

func TestGetAvgExecutionTimeByTaskWithLimit(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/analytics/avg-execution-time-by-task" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("project_id"); got != "p1" {
			t.Errorf("project_id = %q, want p1", got)
		}
		if got := r.URL.Query().Get("limit"); got != "12" {
			t.Errorf("limit = %q, want 12", got)
		}
		json.NewEncoder(w).Encode([]AvgExecutionTime{
			{ID: "t1", Name: "Triage", AvgMs: 1234.5, Count: 10},
		})
	}))

	items, err := c.GetAvgExecutionTimeByTaskWithLimit(context.Background(), "p1", 12)
	if err != nil {
		t.Fatalf("GetAvgExecutionTimeByTaskWithLimit: %v", err)
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
		if got := r.URL.Query().Get("limit"); got != "" {
			t.Errorf("unbounded request limit = %q, want omitted", got)
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

func TestGetAvgExecutionTimeByAgentWithLimit(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/analytics/avg-execution-time-by-agent" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("project_id"); got != "p2" {
			t.Errorf("project_id = %q, want p2", got)
		}
		if got := r.URL.Query().Get("limit"); got != "12" {
			t.Errorf("limit = %q, want 12", got)
		}
		json.NewEncoder(w).Encode([]AvgExecutionTime{
			{ID: "a1", Name: "claude-sonnet", AvgMs: 800.0, Count: 5},
		})
	}))

	items, err := c.GetAvgExecutionTimeByAgentWithLimit(context.Background(), "p2", 12)
	if err != nil {
		t.Fatalf("GetAvgExecutionTimeByAgentWithLimit: %v", err)
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
		if got := r.URL.Query().Get("limit"); got != "0" {
			t.Errorf("full-history request limit = %q, want 0", got)
		}
		json.NewEncoder(w).Encode([]TaskFrequency{
			{TaskID: "t1", TaskTitle: "Triage", ExecutionCount: 42},
			{TaskID: "t2", TaskTitle: "Deploy", ExecutionCount: 7},
			{TaskID: "t3", TaskTitle: "Review", ExecutionCount: 1},
		})
	}))

	items, err := c.GetMostFrequentTasks(context.Background(), "p2")
	if err != nil {
		t.Fatalf("GetMostFrequentTasks: %v", err)
	}
	if len(items) != 3 || items[0].TaskTitle != "Triage" || items[0].ExecutionCount != 42 || items[2].TaskTitle != "Review" {
		t.Errorf("unexpected complete history: %+v", items)
	}
}

func TestGetMostFrequentTasksWithLimit(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/analytics/most-frequent-tasks" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("project_id"); got != "selected/p2" {
			t.Errorf("project_id = %q, want selected/p2", got)
		}
		if got := r.URL.Query().Get("limit"); got != "12" {
			t.Errorf("limit = %q, want 12", got)
		}
		json.NewEncoder(w).Encode([]TaskFrequency{
			{TaskID: "t1", TaskTitle: "Triage", ExecutionCount: 42},
		})
	}))

	items, err := c.GetMostFrequentTasksWithLimit(context.Background(), "selected/p2", 12)
	if err != nil {
		t.Fatalf("GetMostFrequentTasksWithLimit: %v", err)
	}
	if len(items) != 1 || items[0].TaskTitle != "Triage" || items[0].ExecutionCount != 42 {
		t.Errorf("unexpected items: %+v", items)
	}
}

func TestGetMostFrequentTasksWithLimitUnauthorized(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login?next=/analytics", http.StatusFound)
	}))

	_, err := c.GetMostFrequentTasksWithLimit(context.Background(), "p2", 12)
	if err == nil {
		t.Fatal("expected error for unauthorized response")
	}
	if !IsAuthRequired(err) {
		t.Fatalf("error = %v, want authentication-required error", err)
	}
}

func TestGetMostFrequentTasksWithLimitPreservesEmptyAndErrorBehavior(t *testing.T) {
	tests := []struct {
		name  string
		h     http.HandlerFunc
		check func(t *testing.T, items []TaskFrequency, err error)
	}{
		{
			name: "empty response",
			h: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("[]"))
			},
			check: func(t *testing.T, items []TaskFrequency, err error) {
				if err != nil {
					t.Fatalf("empty response error: %v", err)
				}
				if items == nil || len(items) != 0 {
					t.Fatalf("empty response items = %#v, want non-nil empty slice", items)
				}
			},
		},
		{
			name: "malformed JSON",
			h: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("[{"))
			},
			check: func(t *testing.T, items []TaskFrequency, err error) {
				if err == nil || items != nil {
					t.Fatalf("malformed JSON returned items=%#v err=%v", items, err)
				}
			},
		},
		{
			name: "backend error",
			h: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "backend failure", http.StatusBadGateway)
			},
			check: func(t *testing.T, items []TaskFrequency, err error) {
				var statusErr *HTTPStatusError
				if err == nil || items != nil || !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusBadGateway {
					t.Fatalf("backend error returned items=%#v err=%v", items, err)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, tc.h)
			items, err := c.GetMostFrequentTasksWithLimit(context.Background(), "p2", 12)
			tc.check(t, items, err)
		})
	}
}

func TestGetMostFrequentTasksWithLimitTimeoutAndCancellation(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		_, err := c.GetMostFrequentTasksWithLimit(ctx, "p2", 12)
		if err == nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timeout error = %v, want context deadline exceeded", err)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		started := make(chan struct{})
		c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			<-r.Context().Done()
		}))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error, 1)
		go func() {
			_, err := c.GetMostFrequentTasksWithLimit(ctx, "p2", 12)
			result <- err
		}()
		select {
		case <-started:
			cancel()
		case <-time.After(time.Second):
			t.Fatal("server did not receive analytics request")
		}
		select {
		case err := <-result:
			if err == nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation error = %v, want context canceled", err)
			}
		case <-time.After(time.Second):
			t.Fatal("canceled analytics request did not return")
		}
	})
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
		if got := r.URL.Query().Get("limit"); got != "0" {
			t.Errorf("full-history request limit = %q, want 0", got)
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

func TestGetFailedTaskPatternsWithLimit(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/analytics/failed-task-patterns" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("project_id"); got != "selected/p3" {
			t.Errorf("project_id = %q, want selected/p3", got)
		}
		if got := r.URL.Query().Get("limit"); got != "12" {
			t.Errorf("limit = %q, want 12", got)
		}
		json.NewEncoder(w).Encode([]FailedTaskPattern{
			{TaskID: "t2", TaskTitle: "Deploy", FailureCount: 3, LastError: "timeout"},
		})
	}))

	items, err := c.GetFailedTaskPatternsWithLimit(context.Background(), "selected/p3", 12)
	if err != nil {
		t.Fatalf("GetFailedTaskPatternsWithLimit: %v", err)
	}
	if len(items) != 1 || items[0].TaskTitle != "Deploy" || items[0].LastError != "timeout" {
		t.Errorf("unexpected items: %+v", items)
	}
}

func TestGetFailedTaskPatternsWithLimitPreservesEmptyAndErrorBehavior(t *testing.T) {
	tests := []struct {
		name  string
		h     http.HandlerFunc
		check func(t *testing.T, items []FailedTaskPattern, err error)
	}{
		{
			name: "empty response",
			h: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("[]"))
			},
			check: func(t *testing.T, items []FailedTaskPattern, err error) {
				if err != nil {
					t.Fatalf("empty response error: %v", err)
				}
				if items == nil || len(items) != 0 {
					t.Fatalf("empty response items = %#v, want non-nil empty slice", items)
				}
			},
		},
		{
			name: "malformed JSON",
			h: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("[{"))
			},
			check: func(t *testing.T, items []FailedTaskPattern, err error) {
				if err == nil || items != nil {
					t.Fatalf("malformed JSON returned items=%#v err=%v", items, err)
				}
			},
		},
		{
			name: "backend error",
			h: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "backend failure", http.StatusBadGateway)
			},
			check: func(t *testing.T, items []FailedTaskPattern, err error) {
				var statusErr *HTTPStatusError
				if err == nil || items != nil || !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusBadGateway {
					t.Fatalf("backend error returned items=%#v err=%v", items, err)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, tc.h)
			items, err := c.GetFailedTaskPatternsWithLimit(context.Background(), "p3", 12)
			tc.check(t, items, err)
		})
	}
}

func TestGetFailedTaskPatternsWithLimitUnauthorized(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	}))

	_, err := c.GetFailedTaskPatternsWithLimit(context.Background(), "", 12)
	if err == nil {
		t.Fatal("expected error for unauthorized response")
	}
	if !IsAuthRequired(err) {
		t.Fatalf("error = %v, want authentication-required error", err)
	}
}

func TestGetFailedTaskPatternsWithLimitTimeoutAndCancellation(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		_, err := c.GetFailedTaskPatternsWithLimit(ctx, "p3", 12)
		if err == nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timeout error = %v, want context deadline exceeded", err)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		started := make(chan struct{})
		c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			<-r.Context().Done()
		}))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error, 1)
		go func() {
			_, err := c.GetFailedTaskPatternsWithLimit(ctx, "p3", 12)
			result <- err
		}()
		select {
		case <-started:
			cancel()
		case <-time.After(time.Second):
			t.Fatal("server did not receive analytics request")
		}
		select {
		case err := <-result:
			if err == nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation error = %v, want context canceled", err)
			}
		case <-time.After(time.Second):
			t.Fatal("canceled analytics request did not return")
		}
	})
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
