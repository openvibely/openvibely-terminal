package client

import (
	"context"
	"errors"
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
				case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1/changes":
					_, _ = io.WriteString(w, `<div id="changes-content"></div>`)
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
			if !posted || result.Status != "succeeded" || result.RefreshStatus != "succeeded" || result.Worktree == nil || result.Worktree.IntegrationStatus != "merged" {
				t.Fatalf("result = %+v, posted = %v", result, posted)
			}
		})
	}
}

func TestTaskWorktreeActionPreservesSuccessWhenRefreshFails(t *testing.T) {
	for _, action := range []struct {
		name     string
		action   string
		strategy string
	}{
		{name: "merge", action: "merge", strategy: "merge"},
		{name: "fast forward", action: "merge", strategy: "ff"},
		{name: "squash", action: "merge", strategy: "squash"},
		{name: "rebase", action: "rebase"},
	} {
		for _, failure := range []string{"reference catalog", "worktree panel", "changes"} {
			t.Run(action.name+"/"+failure, func(t *testing.T) {
				const projectID = "selected-project"
				var catalogReads, panelReads, changesReads, mutations int
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet && r.URL.Path != "/api/tasks/reference-catalog" && r.URL.Query().Get("project_id") != projectID {
						t.Errorf("%s project_id = %q, want %q", r.URL.Path, r.URL.Query().Get("project_id"), projectID)
					}
					switch {
					case r.Method == http.MethodGet && r.URL.Path == "/api/tasks/reference-catalog":
						catalogReads++
						if r.URL.Query().Get("project_id") != projectID {
							t.Errorf("catalog project_id = %q, want %q", r.URL.Query().Get("project_id"), projectID)
						}
						if failure == "reference catalog" && catalogReads == 2 {
							http.Error(w, "catalog refresh unavailable", http.StatusServiceUnavailable)
							return
						}
						_, _ = io.WriteString(w, `{"tasks":[{"id":"t-1","project_id":"selected-project","title":"Implement feature"}]}`)
					case r.Method == http.MethodPost && r.URL.Path == "/tasks/t-1/worktree/"+action.action:
						mutations++
						if err := r.ParseForm(); err != nil {
							t.Errorf("ParseForm: %v", err)
						}
						if r.Form.Get("project_id") != projectID {
							t.Errorf("mutation project_id = %q, want %q", r.Form.Get("project_id"), projectID)
						}
						if action.action == "merge" && r.Form.Get("merge_type") != action.strategy {
							t.Errorf("merge_type = %q, want %q", r.Form.Get("merge_type"), action.strategy)
						}
						w.Header().Set("HX-Trigger", `{"openvibelyToast":{"message":"Merged locally into main","status":"completed"}}`)
					case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1/worktree":
						panelReads++
						if failure == "worktree panel" {
							http.Error(w, "panel refresh unavailable", http.StatusServiceUnavailable)
							return
						}
						_, _ = io.WriteString(w, taskWorktreeHTML("task/feature", "main", "merged"))
					case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1/changes":
						changesReads++
						if failure == "changes" {
							http.Error(w, "changes refresh unavailable", http.StatusServiceUnavailable)
							return
						}
						_, _ = io.WriteString(w, `<div id="changes-content"></div>`)
					default:
						t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
						http.NotFound(w, r)
					}
				}))
				defer srv.Close()
				c, err := New(srv.URL)
				if err != nil {
					t.Fatal(err)
				}

				result, err := c.ApplyTaskWorktreeActionForProject(context.Background(), "t-1", projectID, action.action, action.strategy)
				if err == nil || !strings.Contains(err.Error(), "refresh failed") {
					t.Fatalf("action error = %v, want separate refresh failure", err)
				}
				var refreshStatusErr *HTTPStatusError
				if !errors.As(err, &refreshStatusErr) || refreshStatusErr.StatusCode != http.StatusServiceUnavailable {
					t.Fatalf("refresh status error is not discoverable: %v", err)
				}
				if strings.Contains(err.Error(), "worktree action failed") {
					t.Fatalf("confirmed operation described as failed: %v", err)
				}
				if result.Status != "succeeded" || result.Message != "Merged locally into main" || result.RefreshStatus != "failed" || result.RefreshError == "" {
					t.Fatalf("result = %+v", result)
				}
				if failure == "changes" {
					if result.Worktree == nil || result.Worktree.Branch != "task/feature" || result.Worktree.TargetBranch != "main" || result.Worktree.IntegrationStatus != "merged" {
						t.Fatalf("partial worktree state was not preserved: %+v", result.Worktree)
					}
				} else if result.Worktree != nil {
					t.Fatalf("unexpected worktree snapshot after %s failure: %+v", failure, result.Worktree)
				}
				wantCatalogReads, wantPanelReads, wantChangesReads := 2, 1, 1
				if failure == "reference catalog" {
					wantPanelReads, wantChangesReads = 0, 0
				} else if failure == "worktree panel" {
					wantChangesReads = 0
				}
				if catalogReads != wantCatalogReads || panelReads != wantPanelReads || changesReads != wantChangesReads || mutations != 1 {
					t.Fatalf("request counts catalog=%d panel=%d changes=%d mutations=%d; want %d/%d/%d/1", catalogReads, panelReads, changesReads, mutations, wantCatalogReads, wantPanelReads, wantChangesReads)
				}
			})
		}
	}
}

func TestTaskWorktreeActionEmptySuccessToastMessageKeepsOutcomeInError(t *testing.T) {
	for _, toastStatus := range []string{"completed", "success"} {
		t.Run(toastStatus, func(t *testing.T) {
			const projectID = "selected-project"
			var catalogReads, panelReads, mutations int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/tasks/reference-catalog":
					catalogReads++
					if r.URL.Query().Get("project_id") != projectID {
						t.Errorf("catalog project_id = %q, want %q", r.URL.Query().Get("project_id"), projectID)
					}
					_, _ = io.WriteString(w, `{"tasks":[{"id":"t-1","project_id":"selected-project","title":"Implement feature"}]}`)
				case r.Method == http.MethodPost && r.URL.Path == "/tasks/t-1/worktree/merge":
					mutations++
					if err := r.ParseForm(); err != nil {
						t.Errorf("ParseForm: %v", err)
					}
					if r.Form.Get("project_id") != projectID {
						t.Errorf("mutation project_id = %q, want %q", r.Form.Get("project_id"), projectID)
					}
					w.Header().Set("HX-Trigger", fmt.Sprintf(`{"openvibelyToast":{"status":%q}}`, toastStatus))
				case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1/worktree":
					panelReads++
					if r.URL.Query().Get("project_id") != projectID {
						t.Errorf("worktree project_id = %q, want %q", r.URL.Query().Get("project_id"), projectID)
					}
					http.Error(w, "panel refresh unavailable", http.StatusServiceUnavailable)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			c, err := New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}

			result, err := c.ApplyTaskWorktreeActionForProject(context.Background(), "t-1", projectID, "merge", "squash")
			if err == nil {
				t.Fatal("expected refresh error")
			}
			if result.Status != "succeeded" || result.Message != "" || result.RefreshStatus != "failed" {
				t.Fatalf("result = %+v", result)
			}
			if !strings.Contains(err.Error(), "worktree action succeeded; refresh failed:") || strings.Contains(err.Error(), "operation did not complete") {
				t.Fatalf("error contradicts confirmed success: %v", err)
			}
			if catalogReads != 2 || panelReads != 1 || mutations != 1 {
				t.Fatalf("request counts catalog=%d panel=%d mutations=%d, want 2/1/1", catalogReads, panelReads, mutations)
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

func TestGetTaskWorktreeInfoRetainsMergedStatusWithoutBranch(t *testing.T) {
	srv := worktreeActionTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<html><body><div id="worktree-info-panel"><span class="badge badge-warning">Merged</span><div><span>Target:</span><code>main</code></div></div></body></html>`)
	})
	defer srv.Close()
	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	info, err := c.GetTaskWorktreeInfoForProject(context.Background(), "t-1", "p-1")
	if err != nil {
		t.Fatalf("GetTaskWorktreeInfoForProject: %v", err)
	}
	if info.Branch != "" {
		t.Errorf("branch = %q, want empty after cleanup", info.Branch)
	}
	if info.IntegrationStatus != "merged" {
		t.Errorf("integration status = %q, want merged", info.IntegrationStatus)
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

func TestCreateTaskPullRequestForProjectCreatesAndReusesBackendPR(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/tasks/reference-catalog":
			if r.URL.Query().Get("project_id") != "p-1" {
				t.Errorf("catalog project_id = %q", r.URL.Query().Get("project_id"))
			}
			_, _ = io.WriteString(w, `{"tasks":[{"id":"t-1","project_id":"p-1","title":"Implement feature"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/tasks/t-1/worktree/pull-request":
			posts++
			if r.URL.Query().Get("project_id") != "p-1" {
				t.Errorf("mutation project_id = %q", r.URL.Query().Get("project_id"))
			}
			if r.Header.Get("HX-Request") != "true" || !strings.Contains(r.Header.Get("Accept"), "text/html") {
				t.Errorf("mutation headers = HX-Request %q Accept %q", r.Header.Get("HX-Request"), r.Header.Get("Accept"))
			}
			if r.Body != http.NoBody {
				body, _ := io.ReadAll(r.Body)
				if len(body) != 0 {
					t.Errorf("mutation body = %q, want empty", body)
				}
			}
			status := "created"
			if posts > 1 {
				status = "already exists"
			}
			w.Header().Set("HX-Trigger", fmt.Sprintf(`{"openvibelyToast":{"message":"GitHub PR %s (#42)","status":"success"}}`, status))
			_, _ = io.WriteString(w, `<div id="changes-content"><a href="https://github.example.com/acme/widgets/pull/42">View PR #42</a></div>`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	for _, wantStatus := range []string{"created", "reused"} {
		result, err := c.CreateTaskPullRequestForProject(context.Background(), "t-1", "p-1")
		if err != nil {
			t.Fatalf("CreateTaskPullRequestForProject: %v", err)
		}
		if result.Number != 42 || result.URL != "https://github.example.com/acme/widgets/pull/42" || result.Status != wantStatus {
			t.Errorf("result = %+v, want number 42, enterprise URL, and status %q", result, wantStatus)
		}
	}
	if posts != 2 {
		t.Fatalf("POST requests = %d, want 2", posts)
	}
}

func TestCreateTaskPullRequestForProjectReportsBackendFailures(t *testing.T) {
	for _, tc := range []struct {
		name         string
		statusCode   int
		toastStatus  string
		toastMessage string
		body         string
		wantError    string
	}{
		{name: "missing repository configuration", statusCode: http.StatusNoContent, toastStatus: "failed", toastMessage: "Project has no repository path configured", wantError: "Project has no repository path configured"},
		{name: "missing task branch", statusCode: http.StatusNoContent, toastStatus: "failed", toastMessage: "Task has no worktree branch", wantError: "Task has no worktree branch"},
		{name: "missing GitHub integration", statusCode: http.StatusNoContent, toastStatus: "failed", toastMessage: "GitHub integration is not configured", wantError: "GitHub integration is not configured"},
		{name: "failure toast on 2xx", statusCode: http.StatusOK, toastStatus: "failed", toastMessage: "provider unavailable", wantError: "provider unavailable"},
		{name: "success without refreshed PR link", statusCode: http.StatusOK, toastStatus: "success", toastMessage: "GitHub PR created (#42)", wantError: "did not include its link"},
		{name: "GitHub service error", statusCode: http.StatusBadRequest, toastStatus: "failed", toastMessage: "Failed to create pull request: provider unavailable", wantError: "provider unavailable"},
		{name: "HTTP backend error", statusCode: http.StatusInternalServerError, body: "backend failed", wantError: "500"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posts int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tasks/reference-catalog" {
					_, _ = io.WriteString(w, `{"tasks":[{"id":"t-1","project_id":"p-1","title":"Implement feature"}]}`)
					return
				}
				if r.Method == http.MethodPost && r.URL.Path == "/tasks/t-1/worktree/pull-request" {
					posts++
					if tc.toastStatus != "" {
						w.Header().Set("HX-Trigger", fmt.Sprintf(`{"openvibelyToast":{"message":%q,"status":%q}}`, tc.toastMessage, tc.toastStatus))
					}
					w.WriteHeader(tc.statusCode)
					_, _ = io.WriteString(w, tc.body)
					return
				}
				t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
				w.WriteHeader(http.StatusNotFound)
			}))
			defer srv.Close()
			c, _ := New(srv.URL)
			result, err := c.CreateTaskPullRequestForProject(context.Background(), "t-1", "p-1")
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.wantError)) {
				t.Fatalf("result = %+v error = %v, want error containing %q", result, err, tc.wantError)
			}
			if posts != 1 {
				t.Fatalf("POST requests = %d, want 1", posts)
			}
		})
	}
}

func TestCreateTaskPullRequestForProjectRejectsWrongProjectBeforeMutation(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tasks/reference-catalog" {
			_, _ = io.WriteString(w, `{"tasks":[]}`)
			return
		}
		if r.Method == http.MethodPost {
			posts++
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	_, err := c.CreateTaskPullRequestForProject(context.Background(), "t-foreign", "p-1")
	if err == nil || !strings.Contains(err.Error(), "not found in selected project") {
		t.Fatalf("wrong-project error = %v", err)
	}
	if posts != 0 {
		t.Fatalf("wrong-project task sent %d mutations", posts)
	}
}

func TestGetTaskWorktreeInfoIncludesRecordedPullRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/tasks/reference-catalog":
			_, _ = io.WriteString(w, `{"tasks":[{"id":"t-1","project_id":"p-1","title":"Implement feature"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1/worktree":
			_, _ = io.WriteString(w, taskWorktreeHTML("task/feature", "main", "pending"))
		case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1/changes":
			_, _ = io.WriteString(w, `<div><a href="https://github.example.com/acme/widgets/pull/42">View PR #42</a></div>`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	info, err := c.GetTaskWorktreeInfoForProject(context.Background(), "t-1", "p-1")
	if err != nil {
		t.Fatalf("GetTaskWorktreeInfoForProject: %v", err)
	}
	if info.PullRequest == nil || info.PullRequest.Number != 42 || info.PullRequest.URL != "https://github.example.com/acme/widgets/pull/42" || info.PullRequest.State != "recorded" {
		t.Fatalf("pull request info = %+v", info.PullRequest)
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
		if r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1/changes" {
			_, _ = io.WriteString(w, `<div id="changes-content"></div>`)
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
