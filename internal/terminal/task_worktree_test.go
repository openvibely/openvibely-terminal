package terminal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

func TestCLITaskWorktreeInspectAndIntegrationConfirmation(t *testing.T) {
	state := "pending"
	var mutations int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			_, _ = io.WriteString(w, cliProjects)
		case r.Method == http.MethodGet && r.URL.Path == "/api/tasks/reference-catalog":
			_, _ = io.WriteString(w, `{"tasks":[{"id":"t-1","project_id":"p1","title":"Refactor API","category":"completed","status":"completed"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1/worktree":
			_, _ = io.WriteString(w, cliWorktreePanel("task/refactor", "main", state))
		case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1/changes":
			_, _ = io.WriteString(w, `<div id="changes-content"><a href="https://github.com/openvibely/openvibely/pull/42">View PR #42</a></div>`)
		case r.Method == http.MethodPost && r.URL.Path == "/tasks/t-1/worktree/merge":
			mutations++
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
			}
			if r.Form.Get("project_id") != "p1" || r.Form.Get("merge_source") != "task_card" || r.Form.Get("merge_type") != "squash" {
				t.Errorf("merge form = %v", r.Form)
			}
			state = "merged"
			w.Header().Set("HX-Trigger", `{"openvibelyToast":{"message":"Merged locally into main","status":"completed"}}`)
			_, _ = io.WriteString(w, `<div id="task-t-1"></div>`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	var inspected bytes.Buffer
	if err := RunCLI(c, &inspected, "demo", []string{"tasks", "worktree", "Refactor API"}, false, true); err != nil {
		t.Fatalf("tasks worktree --json: %v", err)
	}
	var task client.Task
	if err := json.Unmarshal(bytes.TrimSpace(inspected.Bytes()), &task); err != nil {
		t.Fatalf("worktree output is not Task JSON: %v\n%s", err, inspected.String())
	}
	if task.ID != "t-1" || task.WorktreeBranch != "task/refactor" || task.MergeTargetBranch != "main" || task.IntegrationStatus != "pending" {
		t.Fatalf("worktree task = %+v", task)
	}
	if task.PullRequest == nil || task.PullRequest.Number != 42 || task.PullRequest.State != "recorded" || task.PullRequest.URL != "https://github.com/openvibely/openvibely/pull/42" {
		t.Fatalf("worktree pull request = %+v", task.PullRequest)
	}
	var inspectedPlain bytes.Buffer
	if err := RunCLI(c, &inspectedPlain, "demo", []string{"tasks", "worktree", "Refactor API"}, false, false); err != nil {
		t.Fatalf("plain tasks worktree: %v", err)
	}
	if !strings.Contains(inspectedPlain.String(), "Pull request: recorded (#42) https://github.com/openvibely/openvibely/pull/42") {
		t.Fatalf("plain worktree output omits recorded PR state/link:\n%s", inspectedPlain.String())
	}

	var unconfirmed bytes.Buffer
	err = RunCLI(c, &unconfirmed, "demo", []string{"tasks", "merge", "squash", "Refactor API"}, false, false)
	if err == nil || !strings.Contains(err.Error(), "use --force to confirm squash merge") {
		t.Fatalf("unconfirmed merge error = %v", err)
	}
	if mutations != 0 {
		t.Fatalf("unconfirmed CLI changed the project branch %d times", mutations)
	}

	var merged bytes.Buffer
	if err := RunCLI(c, &merged, "demo", []string{"tasks", "merge", "squash", "Refactor API"}, true, false); err != nil {
		t.Fatalf("confirmed merge: %v", err)
	}
	if !strings.Contains(merged.String(), "merge succeeded") || !strings.Contains(merged.String(), "Branch: task/refactor") || !strings.Contains(merged.String(), "Integration: merged") {
		t.Fatalf("merge output omits result or refreshed state:\n%s", merged.String())
	}
	if mutations != 1 {
		t.Fatalf("confirmed merge made %d mutations, want 1", mutations)
	}
}

func TestTUITaskWorktreeMergeRequiresConfirmation(t *testing.T) {
	state := "pending"
	var mutations int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/tasks/reference-catalog":
			_, _ = io.WriteString(w, `{"tasks":[{"id":"t-1","project_id":"p1","title":"Refactor API","category":"completed","status":"completed"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1/worktree":
			_, _ = io.WriteString(w, cliWorktreePanel("task/refactor", "main", state))
		case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1/changes":
			_, _ = io.WriteString(w, `<div id="changes-content"><a href="https://github.com/openvibely/openvibely/pull/42">View PR #42</a></div>`)
		case r.Method == http.MethodPost && r.URL.Path == "/tasks/t-1/worktree/merge":
			mutations++
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
			}
			if r.Form.Get("project_id") != "p1" || r.Form.Get("merge_source") != "task_card" || r.Form.Get("merge_type") != "ff" {
				t.Errorf("merge form = %v", r.Form)
			}
			state = "merged"
			w.Header().Set("HX-Trigger", `{"openvibelyToast":{"message":"Merged locally into main","status":"completed"}}`)
			_, _ = io.WriteString(w, `<div id="task-t-1"></div>`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	m.selectedID, m.selectedName = "p1", "demo"
	m = runLine(t, m, "/tasks merge ff Refactor API")
	if m.pendingConfirmation == nil || mutations != 0 {
		t.Fatalf("merge confirmation state = pending:%v mutations:%d", m.pendingConfirmation != nil, mutations)
	}
	if !strings.Contains(m.pendingConfirmation.message, `Fast-forward merge task "Refactor API (t-1)"`) {
		t.Fatalf("confirmation prompt = %q", m.pendingConfirmation.message)
	}
	m = runLine(t, m, "yes")
	if m.pendingConfirmation != nil || mutations != 1 || state != "merged" {
		t.Fatalf("confirmed TUI merge state = pending:%v mutations:%d integration:%s", m.pendingConfirmation != nil, mutations, state)
	}
}

func TestCLITaskWorktreeConfirmedSuccessReportsRefreshFailureSeparately(t *testing.T) {
	for _, failure := range []string{"reference catalog", "worktree panel", "changes"} {
		for _, jsonOutput := range []bool{false, true} {
			name := failure + "/human"
			if jsonOutput {
				name = failure + "/json"
			}
			t.Run(name, func(t *testing.T) {
				var catalogReads, panelReads, changesReads, mutations int
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
						_, _ = io.WriteString(w, cliProjects)
					case r.Method == http.MethodGet && r.URL.Path == "/api/tasks/reference-catalog":
						catalogReads++
						if r.URL.Query().Get("project_id") != "p2" {
							t.Errorf("reference catalog project_id = %q, want selected non-default project p2", r.URL.Query().Get("project_id"))
						}
						if failure == "reference catalog" && catalogReads == 3 {
							http.Error(w, "catalog refresh unavailable", http.StatusServiceUnavailable)
							return
						}
						_, _ = io.WriteString(w, `{"tasks":[{"id":"t-1","project_id":"p2","title":"Refactor API"}]}`)
					case r.Method == http.MethodPost && r.URL.Path == "/tasks/t-1/worktree/merge":
						mutations++
						if err := r.ParseForm(); err != nil {
							t.Errorf("ParseForm: %v", err)
						}
						if r.Form.Get("project_id") != "p2" {
							t.Errorf("mutation project_id = %q, want p2", r.Form.Get("project_id"))
						}
						w.Header().Set("HX-Trigger", `{"openvibelyToast":{"message":"Merged locally into main","status":"completed"}}`)
					case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1/worktree":
						panelReads++
						if r.URL.Query().Get("project_id") != "p2" {
							t.Errorf("worktree project_id = %q, want p2", r.URL.Query().Get("project_id"))
						}
						if failure == "worktree panel" {
							http.Error(w, "panel refresh unavailable", http.StatusServiceUnavailable)
							return
						}
						_, _ = io.WriteString(w, cliWorktreePanel("task/refactor", "main", "merged"))
					case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1/changes":
						changesReads++
						if r.URL.Query().Get("project_id") != "p2" {
							t.Errorf("changes project_id = %q, want p2", r.URL.Query().Get("project_id"))
						}
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
				c, err := client.New(srv.URL)
				if err != nil {
					t.Fatal(err)
				}

				var out bytes.Buffer
				err = RunCLI(c, &out, "other", []string{"tasks", "merge", "squash", "Refactor API"}, true, jsonOutput)
				if err == nil || !strings.Contains(err.Error(), "refresh failed") {
					t.Fatalf("CLI error = %v, want refresh diagnostic", err)
				}
				if jsonOutput {
					var result client.TaskWorktreeActionResult
					if decodeErr := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &result); decodeErr != nil {
						t.Fatalf("JSON result decode: %v; output=%q", decodeErr, out.String())
					}
					if result.Status != "succeeded" || result.RefreshStatus != "failed" || result.RefreshError == "" {
						t.Fatalf("JSON result conflates operation and refresh: %+v", result)
					}
					if failure == "changes" && (result.Worktree == nil || result.Worktree.Branch != "task/refactor" || result.Worktree.IntegrationStatus != "merged") {
						t.Fatalf("JSON result lost partial worktree state: %+v", result.Worktree)
					}
				} else {
					if !strings.Contains(out.String(), "merge succeeded: Merged locally into main") || !strings.Contains(out.String(), "Refresh failed:") {
						t.Fatalf("human output does not separate operation and refresh:\n%s", out.String())
					}
					if strings.Contains(out.String(), "merge failed") {
						t.Fatalf("human output describes confirmed merge as failed:\n%s", out.String())
					}
					if failure == "changes" && (!strings.Contains(out.String(), "Branch: task/refactor") || !strings.Contains(out.String(), "Integration: merged")) {
						t.Fatalf("human output lost partial branch state:\n%s", out.String())
					}
				}
				wantCatalogReads, wantPanelReads, wantChangesReads := 3, 1, 1
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

func TestCLITaskWorktreeEmptySuccessToastMessageDoesNotImplyActionFailure(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		name := "human"
		if jsonOutput {
			name = "json"
		}
		t.Run(name, func(t *testing.T) {
			var catalogReads, panelReads, changesReads, mutations int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
					_, _ = io.WriteString(w, cliProjects)
				case r.Method == http.MethodGet && r.URL.Path == "/api/tasks/reference-catalog":
					catalogReads++
					if r.URL.Query().Get("project_id") != "p2" {
						t.Errorf("reference catalog project_id = %q, want p2", r.URL.Query().Get("project_id"))
					}
					_, _ = io.WriteString(w, `{"tasks":[{"id":"t-1","project_id":"p2","title":"Refactor API"}]}`)
				case r.Method == http.MethodPost && r.URL.Path == "/tasks/t-1/worktree/merge":
					mutations++
					if err := r.ParseForm(); err != nil {
						t.Errorf("ParseForm: %v", err)
					}
					if r.Form.Get("project_id") != "p2" {
						t.Errorf("mutation project_id = %q, want p2", r.Form.Get("project_id"))
					}
					w.Header().Set("HX-Trigger", `{"openvibelyToast":{"status":"completed"}}`)
				case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1/worktree":
					panelReads++
					if r.URL.Query().Get("project_id") != "p2" {
						t.Errorf("worktree project_id = %q, want p2", r.URL.Query().Get("project_id"))
					}
					_, _ = io.WriteString(w, cliWorktreePanel("task/refactor", "main", "merged"))
				case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1/changes":
					changesReads++
					if r.URL.Query().Get("project_id") != "p2" {
						t.Errorf("changes project_id = %q, want p2", r.URL.Query().Get("project_id"))
					}
					http.Error(w, "changes refresh unavailable", http.StatusServiceUnavailable)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}

			var out bytes.Buffer
			err = RunCLI(c, &out, "other", []string{"tasks", "merge", "squash", "Refactor API"}, true, jsonOutput)
			if err == nil || !strings.Contains(err.Error(), "worktree action succeeded; refresh failed:") || strings.Contains(err.Error(), "operation did not complete") {
				t.Fatalf("CLI error contradicts confirmed success: %v", err)
			}
			if jsonOutput {
				var result client.TaskWorktreeActionResult
				if decodeErr := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &result); decodeErr != nil {
					t.Fatalf("JSON result decode: %v; output=%q", decodeErr, out.String())
				}
				if result.Status != "succeeded" || result.Message != "" || result.RefreshStatus != "failed" || result.RefreshError == "" {
					t.Fatalf("JSON result conflates operation and refresh: %+v", result)
				}
			} else if !strings.Contains(out.String(), "merge succeeded: succeeded") || !strings.Contains(out.String(), "Refresh failed:") || strings.Contains(out.String(), "operation did not complete") {
				t.Fatalf("human output contradicts confirmed success:\n%s", out.String())
			}
			if catalogReads != 3 || panelReads != 1 || changesReads != 1 || mutations != 1 {
				t.Fatalf("request counts catalog=%d panel=%d changes=%d mutations=%d, want 3/1/1/1", catalogReads, panelReads, changesReads, mutations)
			}
		})
	}
}

func TestCLITaskWorktreeActionsReportEligibilityAndConflict(t *testing.T) {
	for _, tc := range []struct {
		name           string
		responseStatus int
		responseBody   string
		state          string
		wantStatus     string
	}{
		{name: "ineligible", responseStatus: http.StatusConflict, responseBody: "Task branch is not currently eligible to merge", state: "pending", wantStatus: "ineligible"},
		{name: "conflict", responseStatus: http.StatusConflict, responseBody: "A merge conflict is already active. Resolve conflicts or abort the merge before trying another merge.", state: "conflict", wantStatus: "conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := "pending"
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
					_, _ = io.WriteString(w, cliProjects)
				case r.Method == http.MethodGet && r.URL.Path == "/api/tasks/reference-catalog":
					_, _ = io.WriteString(w, `{"tasks":[{"id":"t-1","project_id":"p1","title":"Refactor API","category":"completed","status":"completed"}]}`)
				case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1/worktree":
					_, _ = io.WriteString(w, cliWorktreePanel("task/refactor", "main", state))
				case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1/changes":
					_, _ = io.WriteString(w, `<div id="changes-content"></div>`)
				case r.Method == http.MethodPost && r.URL.Path == "/tasks/t-1/worktree/merge":
					state = tc.state
					w.WriteHeader(tc.responseStatus)
					_, _ = io.WriteString(w, tc.responseBody)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			c, _ := client.New(srv.URL)
			var out bytes.Buffer
			err := RunCLI(c, &out, "demo", []string{"tasks", "merge", "merge", "Refactor API"}, true, false)
			if err == nil {
				t.Fatal("expected non-zero CLI result")
			}
			if !strings.Contains(out.String(), "merge "+tc.wantStatus) || !strings.Contains(out.String(), "Integration: "+tc.state) {
				t.Fatalf("CLI output omits outcome or current branch state:\n%s\nerror: %v", out.String(), err)
			}
		})
	}
}

func TestCLITaskPullRequestCreateReuseAndJSONOutput(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			_, _ = io.WriteString(w, cliProjects)
		case r.Method == http.MethodGet && r.URL.Path == "/api/tasks/reference-catalog":
			if r.URL.Query().Get("project_id") != "p1" {
				t.Errorf("reference catalog project_id = %q", r.URL.Query().Get("project_id"))
			}
			_, _ = io.WriteString(w, `{"tasks":[{"id":"t-1","project_id":"p1","title":"Refactor API","category":"completed","status":"completed"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/tasks/t-1/worktree/pull-request":
			posts++
			if r.URL.Query().Get("project_id") != "p1" || r.Header.Get("HX-Request") != "true" {
				t.Errorf("PR request scope/header = project %q HX %q", r.URL.Query().Get("project_id"), r.Header.Get("HX-Request"))
			}
			toast := "GitHub PR created (#42)"
			if posts > 1 {
				toast = "GitHub PR already exists (#42)"
			}
			w.Header().Set("HX-Trigger", fmt.Sprintf(`{"openvibelyToast":{"message":%q,"status":"success"}}`, toast))
			_, _ = io.WriteString(w, `<div id="changes-content"><a href="https://github.com/openvibely/openvibely/pull/42">View PR #42</a></div>`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL)

	var plain bytes.Buffer
	if err := RunCLI(c, &plain, "demo", []string{"tasks", "pull-request", "Refactor API"}, false, false); err != nil {
		t.Fatalf("plain tasks pull-request: %v", err)
	}
	if !strings.Contains(plain.String(), "pull request created: #42 https://github.com/openvibely/openvibely/pull/42") {
		t.Fatalf("plain output = %q", plain.String())
	}

	var machine bytes.Buffer
	if err := RunCLI(c, &machine, "demo", []string{"tasks", "pull-request", "Refactor API"}, false, true); err != nil {
		t.Fatalf("JSON tasks pull-request: %v", err)
	}
	var result client.TaskPullRequestResult
	if err := json.Unmarshal(bytes.TrimSpace(machine.Bytes()), &result); err != nil {
		t.Fatalf("JSON result decode: %v; output=%q", err, machine.String())
	}
	if result.Number != 42 || result.URL != "https://github.com/openvibely/openvibely/pull/42" || result.Status != "reused" {
		t.Fatalf("JSON result = %+v", result)
	}
	if posts != 2 {
		t.Fatalf("PR POST count = %d, want 2", posts)
	}
}

func TestTUITaskPullRequestUsesSelectedProjectAndRendersResult(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/tasks/reference-catalog":
			if r.URL.Query().Get("project_id") != "p1" {
				t.Errorf("reference catalog project_id = %q", r.URL.Query().Get("project_id"))
			}
			_, _ = io.WriteString(w, `{"tasks":[{"id":"t-1","project_id":"p1","title":"Refactor API"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/tasks/t-1/worktree/pull-request":
			posts++
			if r.URL.Query().Get("project_id") != "p1" {
				t.Errorf("PR project_id = %q", r.URL.Query().Get("project_id"))
			}
			w.Header().Set("HX-Trigger", `{"openvibelyToast":{"message":"GitHub PR created (#42)","status":"success"}}`)
			_, _ = io.WriteString(w, `<div><a href="https://github.com/openvibely/openvibely/pull/42">View PR #42</a></div>`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL)
	m := New(c)
	m.selectedID, m.selectedName = "p1", "demo"
	m = runLine(t, m, "/tasks pull-request Refactor API")
	if posts != 1 || !strings.Contains(transcript(m), "pull request created: #42 https://github.com/openvibely/openvibely/pull/42") {
		t.Fatalf("TUI PR result: posts=%d transcript=%q", posts, transcript(m))
	}
}

func TestCLITaskPullRequestFailuresDoNotWriteSuccessOutput(t *testing.T) {
	for _, tc := range []struct {
		name         string
		catalog      string
		statusCode   int
		toastMessage string
		toastStatus  string
		body         string
		wantError    string
		wantMutation bool
	}{
		{name: "wrong project", catalog: `{"tasks":[{"id":"t-1","project_id":"p2","title":"Foreign task"}]}`, wantError: "invalid task reference catalog response"},
		{name: "missing repository", catalog: `{"tasks":[{"id":"t-1","project_id":"p1","title":"Task"}]}`, statusCode: http.StatusNoContent, toastMessage: "Project has no repository path configured", toastStatus: "failed", wantError: "repository path configured", wantMutation: true},
		{name: "missing task branch", catalog: `{"tasks":[{"id":"t-1","project_id":"p1","title":"Task"}]}`, statusCode: http.StatusNoContent, toastMessage: "Task has no worktree branch", toastStatus: "failed", wantError: "no worktree branch", wantMutation: true},
		{name: "missing GitHub integration", catalog: `{"tasks":[{"id":"t-1","project_id":"p1","title":"Task"}]}`, statusCode: http.StatusNoContent, toastMessage: "GitHub integration is not configured", toastStatus: "failed", wantError: "GitHub integration is not configured", wantMutation: true},
		{name: "backend failure", catalog: `{"tasks":[{"id":"t-1","project_id":"p1","title":"Task"}]}`, statusCode: http.StatusInternalServerError, body: "backend unavailable", wantError: "500", wantMutation: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posts int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
					_, _ = io.WriteString(w, cliProjects)
				case r.Method == http.MethodGet && r.URL.Path == "/api/tasks/reference-catalog":
					_, _ = io.WriteString(w, tc.catalog)
				case r.Method == http.MethodPost && r.URL.Path == "/tasks/t-1/worktree/pull-request":
					posts++
					if tc.toastStatus != "" {
						w.Header().Set("HX-Trigger", fmt.Sprintf(`{"openvibelyToast":{"message":%q,"status":%q}}`, tc.toastMessage, tc.toastStatus))
					}
					w.WriteHeader(tc.statusCode)
					_, _ = io.WriteString(w, tc.body)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			c, _ := client.New(srv.URL)
			var out bytes.Buffer
			err := RunCLI(c, &out, "demo", []string{"tasks", "pull-request", "Task"}, false, false)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.wantError)) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantError)
			}
			if strings.Contains(strings.ToLower(out.String()), "pull request created") || strings.Contains(strings.ToLower(out.String()), "pull request reused") {
				t.Fatalf("failure wrote success output: %q", out.String())
			}
			wantPosts := 0
			if tc.wantMutation {
				wantPosts = 1
			}
			if posts != wantPosts {
				t.Fatalf("PR POST count = %d, want %d", posts, wantPosts)
			}
		})
	}
}

func cliWorktreePanel(branch, target, status string) string {
	label := map[string]string{"pending": "Pending Merge", "merged": "Merged", "conflict": "Conflicts", "failed": "Merge Failed"}[status]
	return fmt.Sprintf(`<div id="worktree-info-panel"><span class="badge badge-warning">%s</span><div><span>Branch:</span><code>%s</code></div><div><span>Target:</span><code>%s</code></div></div>`, label, branch, target)
}
