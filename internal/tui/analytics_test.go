package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely-tui/internal/client"
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

	order := []string{"Usage & cost", "Avg execution time by agent", "Avg execution time by task"}
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
