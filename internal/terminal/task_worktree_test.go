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

func cliWorktreePanel(branch, target, status string) string {
	label := map[string]string{"pending": "Pending Merge", "merged": "Merged", "conflict": "Conflicts", "failed": "Merge Failed"}[status]
	return fmt.Sprintf(`<div id="worktree-info-panel"><span class="badge badge-warning">%s</span><div><span>Branch:</span><code>%s</code></div><div><span>Target:</span><code>%s</code></div></div>`, label, branch, target)
}
