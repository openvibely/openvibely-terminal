package client

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestProjectQueryAcrossJSONLifecycleAndHTMLCalls(t *testing.T) {
	const projectID = "team space/a&b?c"
	const encodedProject = "project_id=team+space%2Fa%26b%3Fc"

	wantRequests := []string{
		"GET /api/analytics/usage?" + encodedProject,
		"GET /api/tasks/task%2Fone/lifecycle-executions?" + encodedProject,
		"GET /api/lifecycle-executions/exec%2Fone/events?" + encodedProject,
		"GET /tasks?" + encodedProject,
		"POST /tasks/task%2Fone/goal?" + encodedProject,
		"POST /tasks/task%2Fone/goal/clear?" + encodedProject,
		"POST /tasks/task%2Fone/goal/pause?" + encodedProject,
		"POST /tasks/task%2Fone/goal/resume?" + encodedProject,
		"POST /tasks/move-completed?" + encodedProject,
	}
	var gotRequests []string
	var rateRequest *http.Request
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/analytics/success-failure-rates" {
			rateRequest = r.Clone(r.Context())
		} else {
			gotRequests = append(gotRequests, r.Method+" "+r.URL.RequestURI())
		}
		switch r.URL.Path {
		case "/api/analytics/usage":
			_, _ = w.Write([]byte(`{}`))
		case "/api/analytics/success-failure-rates",
			"/api/tasks/task/one/lifecycle-executions",
			"/api/lifecycle-executions/exec/one/events":
			_, _ = w.Write([]byte(`[]`))
		case "/tasks":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><body></body></html>`))
		case "/tasks/move-completed",
			"/tasks/task/one/goal",
			"/tasks/task/one/goal/clear",
			"/tasks/task/one/goal/pause",
			"/tasks/task/one/goal/resume":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))

	ctx := context.Background()
	if _, err := c.GetUsageAnalytics(ctx, projectID); err != nil {
		t.Fatalf("GetUsageAnalytics: %v", err)
	}
	if _, err := c.GetSuccessFailureRates(ctx, projectID); err != nil {
		t.Fatalf("GetSuccessFailureRates: %v", err)
	}
	if _, err := c.ListTaskLifecycleExecutionsForProject(ctx, "task/one", projectID); err != nil {
		t.Fatalf("ListTaskLifecycleExecutionsForProject: %v", err)
	}
	if _, err := c.GetLifecycleExecutionEventsForProject(ctx, "exec/one", projectID); err != nil {
		t.Fatalf("GetLifecycleExecutionEventsForProject: %v", err)
	}
	if _, err := c.ListTasks(ctx, projectID); err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if err := c.SetTaskGoalForProject(ctx, "task/one", projectID, "finish safely"); err != nil {
		t.Fatalf("SetTaskGoalForProject: %v", err)
	}
	if err := c.ClearTaskGoalForProject(ctx, "task/one", projectID); err != nil {
		t.Fatalf("ClearTaskGoalForProject: %v", err)
	}
	if err := c.PauseTaskGoalForProject(ctx, "task/one", projectID); err != nil {
		t.Fatalf("PauseTaskGoalForProject: %v", err)
	}
	if err := c.ResumeTaskGoalForProject(ctx, "task/one", projectID); err != nil {
		t.Fatalf("ResumeTaskGoalForProject: %v", err)
	}
	if err := c.SweepCompletedTasks(ctx, projectID); err != nil {
		t.Fatalf("SweepCompletedTasks: %v", err)
	}

	if fmt.Sprint(gotRequests) != fmt.Sprint(wantRequests) {
		t.Fatalf("requests = %q, want %q", gotRequests, wantRequests)
	}
	if rateRequest == nil {
		t.Fatal("missing success/failure rates request")
	}
	if got := rateRequest.URL.Query().Get("project_id"); got != projectID {
		t.Fatalf("rates project_id = %q, want %q", got, projectID)
	}
	if got := rateRequest.URL.Query().Get("group_by"); got != "day" {
		t.Fatalf("rates group_by = %q, want day", got)
	}
	from, err := time.Parse(time.RFC3339Nano, rateRequest.URL.Query().Get("date_from"))
	if err != nil {
		t.Fatalf("rates date_from: %v", err)
	}
	to, err := time.Parse(time.RFC3339Nano, rateRequest.URL.Query().Get("date_to"))
	if err != nil {
		t.Fatalf("rates date_to: %v", err)
	}
	if to.Sub(from) != 28*24*time.Hour {
		t.Fatalf("rates range = %s, want 28 days", to.Sub(from))
	}
	if got := rateRequest.URL.Query().Get("limit"); got != "" {
		t.Fatalf("rates request sent unsupported limit=%q", got)
	}
}

func TestProjectQueryOmitsEmptyValue(t *testing.T) {
	if got := query("project_id", ""); got != "" {
		t.Fatalf("query(project_id, empty) = %q, want empty suffix", got)
	}
}
