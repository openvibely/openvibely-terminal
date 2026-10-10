package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTaskWorktreeActionsUseBackendAndSupportedStrategies(t *testing.T) {
	for _, tc := range []struct {
		name     string
		action   string
		strategy string
		path     string
	}{
		{name: "merge commit", action: "merge", strategy: "merge", path: "/tasks/t-1/worktree/merge"},
		{name: "fast forward", action: "merge", strategy: "ff", path: "/tasks/t-1/worktree/merge"},
		{name: "squash", action: "merge", strategy: "squash", path: "/tasks/t-1/worktree/merge"},
		{name: "rebase", action: "rebase", path: "/tasks/t-1/worktree/rebase"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mergeStatus := "pending"
			var posted bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/tasks/reference-catalog":
					if r.URL.Query().Get("project_id") != "p-1" {
						t.Errorf("catalog project_id = %q", r.URL.Query().Get("project_id"))
					}
					_, _ = io.WriteString(w, `{"tasks":[{"id":"t-1","project_id":"p-1","title":"Implement feature"}]}`)
				case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1/worktree":
					_, _ = io.WriteString(w, taskWorktreeHTML("task/feature", "main", mergeStatus))
				case r.Method == http.MethodPost && r.URL.Path == tc.path:
					posted = true
					if r.Header.Get("HX-Request") != "true" {
						t.Error("mutation missing HX-Request")
					}
					if err := r.ParseForm(); err != nil {
						t.Fatal(err)
					}
					if r.Form.Get("project_id") != "p-1" || r.Form.Get("merge_source") != "task_card" {
						t.Errorf("mutation scope = project %q source %q", r.Form.Get("project_id"), r.Form.Get("merge_source"))
					}
					if tc.action == "merge" && r.Form.Get("merge_type") != tc.strategy {
						t.Errorf("merge_type = %q, want %q", r.Form.Get("merge_type"), tc.strategy)
					}
					mergeStatus = "merged"
					w.Header().Set("HX-Trigger", `{"openvibelyToast":{"message":"Merged locally into main","status":"completed"}}`)
					w.WriteHeader(http.StatusOK)
				case r.URL.Path == "/tasks/t-1/worktree/merge":
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			c, err := New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			result, err := c.ApplyTaskWorktreeActionForProject(context.Background(), "t-1", "p-1", tc.action, tc.strategy)
			if err != nil {
				t.Fatalf("ApplyTaskWorktreeActionForProject: %v", err)
			}
			if !posted || result.Status != "succeeded" || result.Worktree == nil || result.Worktree.IntegrationStatus != "merged" {
				t.Fatalf("result = %+v, posted = %v", result, posted)
			}
		})
	}
}

func TestTaskWorktreeActionReportsIneligibleAndConflict(t *testing.T) {
	for _, tc := range []struct {
		name           string
		responseStatus int
		responseBody   string
		currentStatus  string
		wantStatus     string
		wantMessage    string
	}{
		{name: "ineligible task", responseStatus: http.StatusConflict, responseBody: "Task branch is not currently eligible to merge", currentStatus: "pending", wantStatus: "ineligible", wantMessage: "not currently eligible"},
		{name: "active conflict", responseStatus: http.StatusConflict, responseBody: "A merge conflict is already active. Resolve conflicts or abort the merge before trying another merge.", currentStatus: "conflict", wantStatus: "conflict", wantMessage: "conflict is already active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := worktreeActionTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					w.WriteHeader(tc.responseStatus)
					_, _ = io.WriteString(w, tc.responseBody)
					return
				}
				_, _ = io.WriteString(w, taskWorktreeHTML("task/feature", "main", tc.currentStatus))
			})
			defer srv.Close()
			c, _ := New(srv.URL)
			result, err := c.ApplyTaskWorktreeActionForProject(context.Background(), "t-1", "p-1", "merge", "merge")
			if err == nil {
				t.Fatal("expected backend action error")
			}
			if result.Status != tc.wantStatus || result.Worktree == nil || result.Worktree.IntegrationStatus != tc.currentStatus {
				t.Fatalf("result = %+v, error = %v", result, err)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.wantMessage)) {
				t.Fatalf("error = %q, want it to contain %q", err, tc.wantMessage)
			}
		})
	}
}

func TestTaskWorktreeActionRejectsInvalidStrategyBeforeMutation(t *testing.T) {
	var mutations int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			mutations++
		}
		if r.URL.Path == "/api/tasks/reference-catalog" {
			_, _ = io.WriteString(w, `{"tasks":[{"id":"t-1","project_id":"p-1","title":"Implement feature"}]}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	_, err := c.ApplyTaskWorktreeActionForProject(context.Background(), "t-1", "p-1", "merge", "ours")
	if err == nil || !strings.Contains(err.Error(), "supported strategies: merge, ff, squash") {
		t.Fatalf("error = %v", err)
	}
	if mutations != 0 {
		t.Fatalf("invalid strategy sent %d mutations", mutations)
	}
}

func TestTaskWorktreeActionRefreshesBranchStateAfterFailure(t *testing.T) {
	stateBranch := "task/feature"
	stateStatus := "pending"
	srv := worktreeActionTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			// The failed backend operation altered durable integration state. The
			// CLI must report the subsequent state read, not its preflight snapshot.
			stateBranch = "task/feature-recovered"
			stateStatus = "failed"
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, "Local merge failed: target update was rejected")
			return
		}
		_, _ = io.WriteString(w, taskWorktreeHTML(stateBranch, "release/next", stateStatus))
	})
	defer srv.Close()
	c, _ := New(srv.URL)
	result, err := c.ApplyTaskWorktreeActionForProject(context.Background(), "t-1", "p-1", "merge", "squash")
	if err == nil {
		t.Fatal("expected failed backend operation")
	}
	if result.Status != "failed" || result.Worktree == nil {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
	if result.Worktree.Branch != "task/feature-recovered" || result.Worktree.TargetBranch != "release/next" || result.Worktree.IntegrationStatus != "failed" {
		t.Fatalf("reported stale branch state: %+v", result.Worktree)
	}
	if !strings.Contains(err.Error(), "branch task/feature-recovered, target release/next, integration failed") {
		t.Fatalf("error does not report refreshed branch state: %v", err)
	}
}

func TestGetTaskWorktreeInfoRequiresSelectedProjectOwnership(t *testing.T) {
	var worktreeRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tasks/reference-catalog" {
			_, _ = io.WriteString(w, `{"tasks":[{"id":"t-1","project_id":"another-project","title":"Wrong project"}]}`)
			return
		}
		if strings.Contains(r.URL.Path, "/worktree") {
			worktreeRequests++
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	_, err := c.GetTaskWorktreeInfoForProject(context.Background(), "t-1", "p-1")
	if err == nil {
		t.Fatal("expected out-of-project task reference to be rejected")
	}
	if worktreeRequests != 0 {
		t.Fatalf("requested worktree for out-of-project task %d times", worktreeRequests)
	}
}

func worktreeActionTestServer(t *testing.T, worktree func(http.ResponseWriter, *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/tasks/reference-catalog" {
			_, _ = io.WriteString(w, `{"tasks":[{"id":"t-1","project_id":"p-1","title":"Implement feature"}]}`)
			return
		}
		if r.URL.Path == "/tasks/t-1/worktree" {
			worktree(w, r)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/tasks/t-1/worktree/merge" {
			worktree(w, r)
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
}

func taskWorktreeHTML(branch, target, status string) string {
	statusLabel := map[string]string{"pending": "Pending Merge", "merged": "Merged", "failed": "Merge Failed", "conflict": "Conflicts"}[status]
	return fmt.Sprintf(`<html><body><div id="worktree-info-panel"><span class="badge badge-warning">%s</span><div><span>Branch:</span><code>%s</code></div><div><span>Target:</span><code>%s</code></div></div></body></html>`, statusLabel, branch, target)
}
