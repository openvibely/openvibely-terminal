package client

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestProjectQueryAcrossJSONLifecycleAndHTMLCalls(t *testing.T) {
	const projectID = "team space/a&b?c"
	const encodedProject = "project_id=team+space%2Fa%26b%3Fc"

	wantRequests := []string{
		"GET /api/analytics/usage?" + encodedProject,
		"GET /api/analytics/success-failure-rates?" + encodedProject,
		"GET /api/tasks/task%2Fone/lifecycle-executions?" + encodedProject,
		"GET /api/lifecycle-executions/exec%2Fone/events?" + encodedProject,
		"GET /tasks?" + encodedProject,
		"POST /tasks/move-completed?" + encodedProject,
	}
	var gotRequests []string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRequests = append(gotRequests, r.Method+" "+r.URL.RequestURI())
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
		case "/tasks/move-completed":
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
	if err := c.SweepCompletedTasks(ctx, projectID); err != nil {
		t.Fatalf("SweepCompletedTasks: %v", err)
	}

	if fmt.Sprint(gotRequests) != fmt.Sprint(wantRequests) {
		t.Fatalf("requests = %q, want %q", gotRequests, wantRequests)
	}
}

func TestProjectQueryOmitsEmptyValue(t *testing.T) {
	if got := query("project_id", ""); got != "" {
		t.Fatalf("query(project_id, empty) = %q, want empty suffix", got)
	}
}
