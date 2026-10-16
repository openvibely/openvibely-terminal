package terminal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
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

func TestAnalyticsCommandActionsUseSharedSections(t *testing.T) {
	cmd := analyticsCommand()
	want := analyticsSectionNames(analyticsSections)
	if !reflect.DeepEqual(cmd.actions, want) {
		t.Fatalf("analytics command actions = %v, want shared sections %v", cmd.actions, want)
	}

	seen := make(map[string]bool, len(cmd.actions))
	help := strings.Split(stripANSI(renderCommandHelp(cmd)), "examples:")[0]
	for _, action := range cmd.actions {
		if seen[action] {
			t.Fatalf("analytics action %q listed more than once in command metadata", action)
		}
		seen[action] = true
		if got := strings.Count(help, "analytics "+action); got != 1 {
			t.Fatalf("analytics help lists action %q %d times, want exactly once:\n%s", action, got, help)
		}
	}
}

func TestAnalyticsTaskTimesHelpMatchesRenderedMetric(t *testing.T) {
	var taskTimesSection *analyticsSection
	for i := range analyticsSections {
		if analyticsSections[i].name == "task-times" {
			taskTimesSection = &analyticsSections[i]
			break
		}
	}
	if taskTimesSection == nil {
		t.Fatal("analytics sections do not define task-times")
	}
	if taskTimesSection.usageDescription != "average execution time by task" {
		t.Fatalf("task-times description = %q, want average execution time by task", taskTimesSection.usageDescription)
	}

	cmd := analyticsCommand()
	help := strings.Join(strings.Fields(stripANSI(renderCommandHelp(cmd))), " ")
	wantHelp := "analytics task-times average execution time by task"
	if !strings.Contains(help, wantHelp) {
		t.Fatalf("generated analytics help missing %q:\n%s", wantHelp, help)
	}
	if strings.Contains(help, "trends") {
		t.Fatalf("generated analytics help still describes task execution times as trends:\n%s", help)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/analytics/avg-execution-time-by-task" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]client.AvgExecutionTime{{ID: "task-1", Name: "Build report", AvgMs: 1250, Count: 4}})
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}

	out, err := loadAnalytics(context.Background(), c, "project-1", "task-times")
	if err != nil {
		t.Fatalf("loadAnalytics task-times: %v", err)
	}
	wantOutput := renderExecTimes("Avg execution time by task", []client.AvgExecutionTime{{ID: "task-1", Name: "Build report", AvgMs: 1250, Count: 4}})
	if out != wantOutput || !strings.Contains(out, "Avg execution time by task") || !strings.Contains(out, "Build report") || !strings.Contains(out, "1.2s") {
		t.Fatalf("task-times output does not match the described average execution-time metric:\n got: %s\nwant: %s", out, wantOutput)
	}
}

func TestAnalyticsSectionDefinitionDrivesCommandAndLoading(t *testing.T) {
	sections := []analyticsSection{
		{
			name:             "current",
			usageDescription: "current report",
			load: func(context.Context, *client.Client, string) (string, error) {
				return "Current section", nil
			},
		},
		{
			name:             "future",
			usageDescription: "future report",
			load: func(context.Context, *client.Client, string) (string, error) {
				return "Future section", nil
			},
		},
	}

	cmd := analyticsCommandForSections(sections)
	if !reflect.DeepEqual(cmd.actions, []string{"current", "future"}) {
		t.Fatalf("actions = %v, want both shared sections", cmd.actions)
	}
	if err := cmd.validateArgs([]string{"future"}); err != nil {
		t.Fatalf("future action was not accepted by validation: %v", err)
	}
	help := strings.Split(stripANSI(renderCommandHelp(cmd)), "examples:")[0]
	for _, action := range cmd.actions {
		if got := strings.Count(help, "analytics "+action); got != 1 {
			t.Fatalf("help lists action %q %d times, want exactly once:\n%s", action, got, help)
		}
	}

	out, err := loadAnalyticsSections(context.Background(), nil, "", "future", sections)
	if err != nil {
		t.Fatalf("loadAnalyticsSections named future: %v", err)
	}
	if strings.TrimSpace(out) != "Future section" {
		t.Fatalf("named future output = %q", out)
	}
	out, err = loadAnalyticsSections(context.Background(), nil, "", "", sections)
	if err != nil {
		t.Fatalf("loadAnalyticsSections all: %v", err)
	}
	if out != "Current section\n\nFuture section" {
		t.Fatalf("all-section output = %q", out)
	}
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

func TestAnalyticsRatesFailurePropagates(t *testing.T) {
	c := newAnalyticsTestClient(t, map[string]bool{"/api/analytics/success-failure-rates": true})
	if _, err := loadAnalytics(context.Background(), c, "rates-project", "rates"); err == nil {
		t.Fatal("expected rates endpoint error to propagate")
	}
}

func TestAnalyticsRatesDefaultFullAndRangeWindows(t *testing.T) {
	history := ratesAnalyticsFixture(5000)
	c, observations := newRatesAnalyticsTestClient(t, history)
	fullBody, err := json.Marshal(history)
	if err != nil {
		t.Fatalf("marshal full fixture: %v", err)
	}

	defaultOutput, err := loadAnalytics(context.Background(), c, "rates-project", "rates")
	if err != nil {
		t.Fatalf("load default rates: %v", err)
	}
	defaultObservation := lastRatesObservation(t, observations())
	if defaultObservation.query.Get("project_id") != "rates-project" || defaultObservation.query.Get("group_by") != "day" {
		t.Fatalf("default rates query lost scope or daily grouping: %v", defaultObservation.query)
	}
	if defaultObservation.query.Get("date_from") == "" || defaultObservation.query.Get("date_to") == "" {
		t.Fatalf("default rates query lacks server-side date bounds: %v", defaultObservation.query)
	}
	if defaultObservation.query.Get("limit") != "" {
		t.Fatalf("rates query used unsupported limit parameter: %v", defaultObservation.query)
	}
	if defaultObservation.rows > 30 {
		t.Fatalf("default server response had %d rows, want at most 30", defaultObservation.rows)
	}
	if defaultObservation.bytes*100 >= len(fullBody)*5 {
		t.Fatalf("default response bytes = %d, want at most 5%% of full history %d", defaultObservation.bytes, len(fullBody))
	}
	if !strings.Contains(stripANSI(defaultOutput), recentRatesWindowLabel) {
		t.Fatalf("default output does not identify its window:\n%s", stripANSI(defaultOutput))
	}

	fullOutput, err := loadAnalyticsSectionsWithRatesWindow(context.Background(), c, "rates-project", "rates", ratesWindow{kind: ratesWindowFull}, analyticsSections)
	if err != nil {
		t.Fatalf("load full rates: %v", err)
	}
	fullObservation := lastRatesObservation(t, observations())
	if fullObservation.query.Get("date_from") != "" || fullObservation.query.Get("date_to") != "" || fullObservation.rows != len(history) {
		t.Fatalf("full history query/response = %v / %d rows, want no bounds / %d", fullObservation.query, fullObservation.rows, len(history))
	}
	if !strings.Contains(stripANSI(fullOutput), "all history") || !strings.Contains(stripANSI(fullOutput), history[0].Period) {
		t.Fatalf("full history output does not identify/include complete history:\n%s", stripANSI(fullOutput))
	}

	end := time.Now().UTC().Truncate(24 * time.Hour)
	from := end.AddDate(0, 0, -30).Format("2006-01-02")
	to := end.Format("2006-01-02")
	rangeOutput, err := loadAnalyticsSectionsWithRatesWindow(context.Background(), c, "rates-project", "rates", ratesWindow{kind: ratesWindowRange, from: from, to: to}, analyticsSections)
	if err != nil {
		t.Fatalf("load range rates: %v", err)
	}
	rangeObservation := lastRatesObservation(t, observations())
	if rangeObservation.query.Get("date_from") != from || rangeObservation.query.Get("date_to") != to || rangeObservation.rows != 30 {
		t.Fatalf("range query/response = %v / %d rows, want [%s,%s) / 30", rangeObservation.query, rangeObservation.rows, from, to)
	}
	if !strings.Contains(stripANSI(rangeOutput), "from "+from+" to "+to+" (end exclusive)") {
		t.Fatalf("range output does not identify its window:\n%s", stripANSI(rangeOutput))
	}

	wideFrom := end.AddDate(0, 0, -40).Format("2006-01-02")
	wideOutput, err := loadAnalyticsSectionsWithRatesWindow(context.Background(), c, "rates-project", "rates", ratesWindow{kind: ratesWindowRange, from: wideFrom, to: to}, analyticsSections)
	if err != nil {
		t.Fatalf("load wide date range: %v", err)
	}
	wideObservation := lastRatesObservation(t, observations())
	if wideObservation.query.Get("date_from") != wideFrom || wideObservation.query.Get("date_to") != to || wideObservation.rows != 40 {
		t.Fatalf("wide range query/response = %v / %d rows, want [%s,%s) / 40", wideObservation.query, wideObservation.rows, wideFrom, to)
	}
	if !strings.Contains(stripANSI(wideOutput), "from "+wideFrom+" to "+to+" (end exclusive)") {
		t.Fatalf("wide range output does not identify its window:\n%s", stripANSI(wideOutput))
	}

	allOutput, err := loadAnalytics(context.Background(), c, "rates-project", "")
	if err != nil {
		t.Fatalf("load all analytics: %v", err)
	}
	allObservation := lastRatesObservation(t, observations())
	if allObservation.query.Get("date_from") == "" || allObservation.rows > 30 {
		t.Fatalf("all-sections rates request was not bounded: %v / %d rows", allObservation.query, allObservation.rows)
	}
	if !strings.Contains(stripANSI(allOutput), recentRatesWindowLabel) {
		t.Fatalf("all analytics output does not identify rates window:\n%s", stripANSI(allOutput))
	}
}

func TestRatesAnalyticsDefaultBoundAcrossFixtureSizes(t *testing.T) {
	for _, size := range []int{0, 1, 30, 31, 5000} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			c, observations := newRatesAnalyticsTestClient(t, ratesAnalyticsFixture(size))
			items, err := c.GetSuccessFailureRates(context.Background(), "fixture-project")
			if err != nil {
				t.Fatalf("GetSuccessFailureRates: %v", err)
			}
			observation := lastRatesObservation(t, observations())
			if len(items) != observation.rows || len(items) > 30 {
				t.Fatalf("decoded rows = %d, server sent %d, want <= 30", len(items), observation.rows)
			}
			for i := 1; i < len(items); i++ {
				if items[i-1].Period >= items[i].Period {
					t.Fatalf("periods are not in deterministic ascending order: %q then %q", items[i-1].Period, items[i].Period)
				}
			}
			if size == 0 && len(items) != 0 {
				t.Fatalf("empty history decoded %d rows", len(items))
			}
			if size == 1 && len(items) != 1 {
				t.Fatalf("single-period history decoded %d rows, want 1", len(items))
			}
		})
	}
}

func TestAnalyticsRatesArgumentValidationAndHelp(t *testing.T) {
	cmd := analyticsCommand()
	for _, args := range [][]string{
		{"rates", "full"},
		{"rates", "from", "2026-01-01"},
		{"rates", "from", "2026-01-01T00:00:00Z", "to", "2026-02-01"},
	} {
		if err := cmd.validateArgs(args); err != nil {
			t.Errorf("valid args %v rejected: %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"rates", "full", "extra"},
		{"rates", "from", "bad-date"},
		{"rates", "from", "2026-02-01", "to", "2026-01-01"},
		{"usage", "full"},
	} {
		if err := cmd.validateArgs(args); err == nil {
			t.Errorf("invalid args %v accepted", args)
		}
	}
	help := stripANSI(renderCommandHelp(cmd))
	for _, want := range []string{"last 28 days", "up to 30 daily periods", "rates full", "rates from <start> [to <end>]", "end exclusive"} {
		if !strings.Contains(help, want) {
			t.Errorf("analytics help missing %q:\n%s", want, help)
		}
	}
}

func ratesAnalyticsFixture(count int) []client.SuccessFailureRate {
	if count == 0 {
		return []client.SuccessFailureRate{}
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	first := today.AddDate(0, 0, -(count - 1))
	rates := make([]client.SuccessFailureRate, 0, count)
	for i := 0; i < count; i++ {
		period := first.AddDate(0, 0, i)
		rates = append(rates, client.SuccessFailureRate{
			Period: period.Format("2006-01-02"), SuccessCount: i % 17, FailureCount: i % 5,
			TotalCount: i%17 + i%5, SuccessRate: float64(i%17) * 3.25,
		})
	}
	return rates
}

type ratesResponseObservation struct {
	query url.Values
	bytes int
	rows  int
}

func newRatesAnalyticsTestClient(t *testing.T, history []client.SuccessFailureRate) (*client.Client, func() []ratesResponseObservation) {
	t.Helper()
	var mu sync.Mutex
	var seen []ratesResponseObservation
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/analytics/success-failure-rates" {
			switch r.URL.Path {
			case "/api/analytics/usage":
				_, _ = w.Write([]byte(`{"totals":{"call_count":1}}`))
			case "/api/analytics/avg-execution-time-by-agent", "/api/analytics/avg-execution-time-by-task", "/api/analytics/most-frequent-tasks", "/api/analytics/failed-task-patterns":
				_, _ = w.Write([]byte(`[]`))
			case "/api/analytics/skills":
				_, _ = w.Write([]byte(`{}`))
			default:
				http.NotFound(w, r)
			}
			return
		}
		rows := history
		if dateFrom := r.URL.Query().Get("date_from"); dateFrom != "" {
			from, err := parseRatesTestTime(dateFrom)
			if err != nil {
				t.Errorf("parse date_from %q: %v", dateFrom, err)
				http.Error(w, "bad date_from", http.StatusBadRequest)
				return
			}
			to := time.Time{}
			if dateTo := r.URL.Query().Get("date_to"); dateTo != "" {
				to, err = parseRatesTestTime(dateTo)
				if err != nil {
					t.Errorf("parse date_to %q: %v", dateTo, err)
					http.Error(w, "bad date_to", http.StatusBadRequest)
					return
				}
			}
			filtered := make([]client.SuccessFailureRate, 0, 30)
			for _, row := range history {
				period, err := time.Parse("2006-01-02", row.Period)
				if err != nil {
					t.Errorf("invalid fixture period %q: %v", row.Period, err)
					continue
				}
				if period.Before(from) || (!to.IsZero() && !period.Before(to)) {
					continue
				}
				filtered = append(filtered, row)
			}
			rows = filtered
		}
		body, err := json.Marshal(rows)
		if err != nil {
			t.Errorf("marshal analytics response: %v", err)
			http.Error(w, "marshal error", http.StatusInternalServerError)
			return
		}
		mu.Lock()
		seen = append(seen, ratesResponseObservation{query: r.URL.Query(), bytes: len(body), rows: len(rows)})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	return c, func() []ratesResponseObservation {
		mu.Lock()
		defer mu.Unlock()
		return append([]ratesResponseObservation(nil), seen...)
	}
}

func parseRatesTestTime(value string) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC(), nil
	}
	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		return parsed.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("unsupported datetime")
}

func lastRatesObservation(t *testing.T, observations []ratesResponseObservation) ratesResponseObservation {
	t.Helper()
	if len(observations) == 0 {
		t.Fatal("no rates request was observed")
	}
	return observations[len(observations)-1]
}

func TestRatesAnalyticsPerformanceEvidence(t *testing.T) {
	if os.Getenv("OPENVIBELY_ANALYTICS_PERF_EVIDENCE") != "1" {
		t.Skip("set OPENVIBELY_ANALYTICS_PERF_EVIDENCE=1 to run the 5,000-period request-to-render evidence harness")
	}
	const fixtureSize = 5000
	const runs = 40
	history := ratesAnalyticsFixture(fixtureSize)
	fullBody, err := json.Marshal(history)
	if err != nil {
		t.Fatalf("marshal full fixture: %v", err)
	}
	bounded := history
	if len(bounded) > 30 {
		bounded = bounded[len(bounded)-30:]
	}
	boundedBody, err := json.Marshal(bounded)
	if err != nil {
		t.Fatalf("marshal bounded fixture: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/analytics/success-failure-rates" || r.URL.Query().Get("project_id") != "perf-project" || r.URL.Query().Get("group_by") != "day" {
			http.Error(w, "unexpected rates request", http.StatusBadRequest)
			return
		}
		body := fullBody
		if r.URL.Query().Get("date_from") != "" && r.URL.Query().Get("date_to") != "" {
			// Represents the backend's SQL date-bound path: only matching grouped
			// rows are serialized, so discarded history never crosses the wire.
			body = boundedBody
		} else if r.URL.Query().Get("date_from") != "" || r.URL.Query().Get("date_to") != "" {
			http.Error(w, "incomplete date bounds", http.StatusBadRequest)
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

	run := func(isOptimized bool) (int, time.Duration, error) {
		started := time.Now()
		var (
			items []client.SuccessFailureRate
			err   error
		)
		if isOptimized {
			items, err = c.GetSuccessFailureRates(context.Background(), "perf-project")
		} else {
			items, err = c.GetSuccessFailureRatesFullHistory(context.Background(), "perf-project")
		}
		if err != nil {
			return 0, 0, err
		}
		label := recentRatesWindowLabel
		if !isOptimized {
			label = "all history"
		}
		_ = renderRatesForWindow(items, label)
		return len(items), time.Since(started), nil
	}

	for _, optimized := range []bool{false, true} {
		if _, _, err := run(optimized); err != nil {
			t.Fatalf("warmup optimized=%t: %v", optimized, err)
		}
	}
	optimizedDurations := make([]time.Duration, 0, runs)
	baselineDurations := make([]time.Duration, 0, runs)
	for i := 0; i < runs; i++ {
		rows, elapsed, err := run(false)
		if err != nil {
			t.Fatalf("baseline run %d: %v", i+1, err)
		}
		if rows != fixtureSize {
			t.Fatalf("baseline decoded rows = %d, want %d", rows, fixtureSize)
		}
		baselineDurations = append(baselineDurations, elapsed)

		rows, elapsed, err = run(true)
		if err != nil {
			t.Fatalf("optimized run %d: %v", i+1, err)
		}
		if rows != len(bounded) || rows > 30 {
			t.Fatalf("optimized decoded rows = %d, want %d and at most 30", rows, len(bounded))
		}
		optimizedDurations = append(optimizedDurations, elapsed)
	}

	baselineAllocBytes, baselineAllocCount := measureFrequentAnalyticsAllocations(runs, func() {
		if _, _, err := run(false); err != nil {
			panic(err)
		}
	})
	optimizedAllocBytes, optimizedAllocCount := measureFrequentAnalyticsAllocations(runs, func() {
		if _, _, err := run(true); err != nil {
			panic(err)
		}
	})
	baselineP50, baselineP95 := frequentAnalyticsPercentiles(baselineDurations)
	optimizedP50, optimizedP95 := frequentAnalyticsPercentiles(optimizedDurations)
	responseReduction := 100 * (1 - float64(len(boundedBody))/float64(len(fullBody)))
	allocReduction := 100 * (1 - optimizedAllocBytes/baselineAllocBytes)
	t.Logf("rates_analytics_perf fixture_periods=%d runs=%d baseline_response_bytes=%d optimized_response_bytes=%d baseline_decoded_rows=%d optimized_decoded_rows=%d baseline_p50=%s optimized_p50=%s baseline_p95=%s optimized_p95=%s baseline_alloc_bytes_per_op=%.0f optimized_alloc_bytes_per_op=%.0f baseline_allocs_per_op=%.1f optimized_allocs_per_op=%.1f response_reduction=%.2f%% allocation_reduction=%.2f%%", fixtureSize, runs, len(fullBody), len(boundedBody), fixtureSize, len(bounded), baselineP50, optimizedP50, baselineP95, optimizedP95, baselineAllocBytes, optimizedAllocBytes, baselineAllocCount, optimizedAllocCount, responseReduction, allocReduction)

	if responseReduction < 95 {
		t.Fatalf("response byte reduction = %.2f%%, want at least 95%%", responseReduction)
	}
	if allocReduction < 95 {
		t.Fatalf("allocated byte reduction = %.2f%%, want at least 95%%", allocReduction)
	}
	if optimizedP95*2 >= baselineP95 {
		t.Fatalf("optimized p95 = %s, want at least 50%% below baseline p95 %s", optimizedP95, baselineP95)
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
			if got := r.URL.Query().Get("limit"); got != strconv.Itoa(maxFailureRows) {
				t.Errorf("failures limit = %q, want %d", got, maxFailureRows)
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
	taskOut, err := loadAnalytics(context.Background(), c, "project-1", "task-times")
	if err != nil {
		t.Fatalf("loadAnalytics task-times: %v", err)
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
		{section: "task-times", path: "/api/analytics/avg-execution-time-by-task", title: "Avg execution time by task"},
		{section: "frequent", path: "/api/analytics/most-frequent-tasks", title: "Most frequent tasks"},
		{section: "failures", path: "/api/analytics/failed-task-patterns", title: "Failure patterns"},
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

func rankedFailurePatterns(history []client.FailedTaskPattern) []client.FailedTaskPattern {
	if len(history) == 0 {
		return make([]client.FailedTaskPattern, 0)
	}
	ranked := append([]client.FailedTaskPattern(nil), history...)
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].FailureCount != ranked[j].FailureCount {
			return ranked[i].FailureCount > ranked[j].FailureCount
		}
		return ranked[i].TaskID < ranked[j].TaskID
	})
	if len(ranked) > maxFailureRows {
		ranked = ranked[:maxFailureRows]
	}
	return ranked
}

func failurePatternRows(count int) []client.FailedTaskPattern {
	records := make([]client.FailedTaskPattern, count)
	for i := range records {
		records[i] = client.FailedTaskPattern{
			TaskID:       fmt.Sprintf("failure-%02d", i),
			TaskTitle:    fmt.Sprintf("failure task %02d", i),
			FailureCount: count - i,
			LastError:    fmt.Sprintf("last error %02d", i),
			LastFailedAt: "2026-09-13T12:34:56Z",
		}
	}
	return records
}

func TestLoadAnalyticsFailureBoundPreservesVisibleOutput(t *testing.T) {
	cases := []struct {
		name    string
		records []client.FailedTaskPattern
	}{
		{name: "empty", records: []client.FailedTaskPattern{}},
		{name: "one without last error", records: []client.FailedTaskPattern{{TaskID: "one", TaskTitle: "one failure", FailureCount: 4}}},
		{name: "one with last error", records: []client.FailedTaskPattern{{TaskID: "one", TaskTitle: "one failure", FailureCount: 4, LastError: "timeout"}}},
		{name: "exactly at limit", records: failurePatternRows(maxFailureRows)},
		{name: "limit plus one and stable tied cutoff", records: func() []client.FailedTaskPattern {
			records := make([]client.FailedTaskPattern, 0, maxFailureRows+1)
			for i := 0; i < maxFailureRows-1; i++ {
				records = append(records, client.FailedTaskPattern{
					TaskID:       fmt.Sprintf("rank-%02d", i),
					TaskTitle:    fmt.Sprintf("ranked failure %02d", i),
					FailureCount: 100 - i,
					LastError:    fmt.Sprintf("ranked error %02d", i),
				})
			}
			records = append(records,
				client.FailedTaskPattern{TaskID: "tie-a", TaskTitle: "tie early", FailureCount: 50, LastError: "kept tie"},
				client.FailedTaskPattern{TaskID: "tie-z", TaskTitle: "tie late", FailureCount: 50, LastError: "omitted tie"},
			)
			return records
		}()},
		{name: "long title and error", records: []client.FailedTaskPattern{{TaskID: "long", TaskTitle: strings.Repeat("title ", 20), FailureCount: 2, LastError: strings.Repeat("error ", 25)}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ranked := rankedFailurePatterns(tc.records)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Query().Get("project_id"); got != "analytics-project" {
					t.Errorf("project_id = %q, want analytics-project", got)
				}
				if got := r.URL.Query().Get("limit"); got != strconv.Itoa(maxFailureRows) {
					t.Errorf("limit = %q, want %d", got, maxFailureRows)
				}
				if err := json.NewEncoder(w).Encode(ranked); err != nil {
					t.Errorf("encode ranked failures: %v", err)
				}
			}))
			t.Cleanup(srv.Close)
			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatalf("client.New: %v", err)
			}

			got, err := loadAnalytics(context.Background(), c, "analytics-project", "failures")
			if err != nil {
				t.Fatalf("loadAnalytics failures: %v", err)
			}
			want := renderFailures(ranked)
			if got != want {
				t.Fatalf("failure output changed for %s\n got: %q\nwant: %q", tc.name, got, want)
			}
			plain := stripANSI(got)
			if tc.name == "empty" {
				if got != sectionStyle.Render("Failure patterns")+"\n  "+statusOKStyle.Render("no failing tasks") {
					t.Fatalf("empty failure output changed: %q", got)
				}
			}
			if tc.name == "limit plus one and stable tied cutoff" {
				if !strings.Contains(plain, "tie early") || strings.Contains(plain, "tie late") || strings.Contains(plain, "more failure patterns") {
					t.Fatalf("ranked stable cutoff or continuation hint changed:\n%s", plain)
				}
			}
			if tc.name == "long title and error" {
				if strings.Contains(plain, strings.Repeat("title ", 20)) || strings.Contains(plain, strings.Repeat("error ", 25)) {
					t.Fatalf("long failure fields were not truncated:\n%s", plain)
				}
			}
			if strings.HasSuffix(got, "\n") {
				t.Fatalf("failure output has trailing newline: %q", got)
			}
		})
	}
}

func TestRenderFailuresDefensivelyCapsOverLimitResponses(t *testing.T) {
	patterns := failurePatternRows(maxFailureRows + 1)
	out := stripANSI(renderFailures(patterns))
	if got := strings.Count(out, "failure task"); got != maxFailureRows {
		t.Fatalf("rendered failure rows = %d, want %d\n%s", got, maxFailureRows, out)
	}
	if !strings.Contains(out, "1 more failure patterns not shown") {
		t.Fatalf("over-limit output missing continuation hint:\n%s", out)
	}
}

func TestLoadAnalyticsFailureBoundInAllSections(t *testing.T) {
	failures := []client.FailedTaskPattern{{TaskID: "fail-1", TaskTitle: "bounded failure", FailureCount: 9, LastError: "boom"}}
	var mu sync.Mutex
	failureRequests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("project_id"); got != "all-project" {
			t.Errorf("%s project_id = %q, want all-project", r.URL.Path, got)
		}
		switch r.URL.Path {
		case "/api/analytics/usage":
			_ = json.NewEncoder(w).Encode(client.UsageAnalytics{})
		case "/api/analytics/success-failure-rates":
			_ = json.NewEncoder(w).Encode([]client.SuccessFailureRate{})
		case "/api/analytics/avg-execution-time-by-agent":
			_ = json.NewEncoder(w).Encode([]client.AvgExecutionTime{})
		case "/api/analytics/avg-execution-time-by-task":
			_ = json.NewEncoder(w).Encode([]client.AvgExecutionTime{})
		case "/api/analytics/most-frequent-tasks":
			_ = json.NewEncoder(w).Encode([]client.TaskFrequency{})
		case "/api/analytics/failed-task-patterns":
			mu.Lock()
			failureRequests++
			mu.Unlock()
			if got := r.URL.Query().Get("limit"); got != strconv.Itoa(maxFailureRows) {
				t.Errorf("failures limit = %q, want %d", got, maxFailureRows)
			}
			_ = json.NewEncoder(w).Encode(failures)
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

	single, err := loadAnalytics(context.Background(), c, "all-project", "failures")
	if err != nil {
		t.Fatalf("single failure analytics: %v", err)
	}
	if single != renderFailures(failures) {
		t.Fatalf("single failure output changed:\n%s", single)
	}
	all, err := loadAnalytics(context.Background(), c, "all-project", "")
	if err != nil {
		t.Fatalf("all analytics: %v", err)
	}
	if !strings.Contains(all, single) || !strings.Contains(all, "Usage & cost") {
		t.Fatalf("all analytics did not include bounded failures and independent sections:\n%s", all)
	}
	mu.Lock()
	gotFailureRequests := failureRequests
	mu.Unlock()
	if gotFailureRequests != 2 {
		t.Fatalf("failure request count = %d, want 2", gotFailureRequests)
	}
}

func TestLoadAnalyticsFailureAuthAndMalformedJSON(t *testing.T) {
	cases := []struct {
		name string
		h    http.HandlerFunc
	}{
		{
			name: "auth required",
			h: func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/login", http.StatusFound)
			},
		},
		{
			name: "malformed json",
			h: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("[{"))
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newAnalyticsFailureOnlyClient(t, tc.h)
			if _, err := loadAnalytics(context.Background(), c, "failure-project", "failures"); err == nil {
				t.Fatalf("single failure section should surface %s error", tc.name)
			}
		})
	}
}

func newAnalyticsFailureOnlyClient(t *testing.T, failureHandler http.HandlerFunc) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/analytics/failed-task-patterns" {
			failureHandler(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	return c
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
	return evidenceFrequentAnalyticsFixture(count)
}

func TestFrequentAnalyticsPerformanceEvidence(t *testing.T) {
	if os.Getenv("OPENVIBELY_ANALYTICS_PERF_EVIDENCE") != "1" {
		t.Skip("set OPENVIBELY_ANALYTICS_PERF_EVIDENCE=1 to run the 5,000-record analytics evidence harness")
	}
	const fixtureSize = frequentAnalyticsFixtureSize
	const runs = 20

	history := frequentAnalyticsFixture(fixtureSize)
	fullBody, err := json.Marshal(history)
	if err != nil {
		t.Fatalf("marshal full fixture: %v", err)
	}
	if len(fullBody) != frequentAnalyticsReviewedResponseSize {
		t.Fatalf("full fixture response bytes = %d, want reviewed workload %d", len(fullBody), frequentAnalyticsReviewedResponseSize)
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
