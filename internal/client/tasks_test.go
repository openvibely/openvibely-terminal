package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/html"
)

// boardHTML mirrors the real kanban markup: every card opens with a kebab menu
// ("Run"/"Cancel"/"Edit") and a close button, so the card's first line of text
// is NOT its title — the title lives in the card's own task link.
const boardHTML = `<html><body>
<div class="kanban">
  <div class="card" data-task-id="t-111" data-task-status="pending" data-task-category="backlog" data-display-order="2">
    <div class="dropdown">
      <ul><li><button hx-post="/tasks/t-111/run">Run</button></li>
          <li><a hx-get="/tasks/t-111?tab=details&amp;from=tasks">Edit</a></li></ul>
    </div>
    <button hx-delete="/tasks/t-111">X</button>
    <div class="card-body">
      <a href="/tasks/t-111?from=tasks" title="Refactor the API">Refactor the API</a>
      <p class="text-sm opacity-60 mt-1 line-clamp-3 sm:line-clamp-2">Split the handler package</p>
      <div><span class="badge badge-sm badge-primary">Goal</span>
           <span class="badge badge-sm badge-outline">Sonnet</span></div>
    </div>
  </div>
  <div class="card" data-task-id="t-222" data-task-status="running" data-task-category="active" data-display-order="0">
    <div class="dropdown"><ul><li><button hx-post="/tasks/t-222/cancel">Cancel</button></li></ul></div>
    <div class="card-body">
      <a href="/tasks/t-222?from=tasks" title="Write docs">Write docs</a>
      <p class="text-sm line-clamp-2">README overhaul</p>
    </div>
  </div>
  <div class="card" data-task-id="t-333" data-task-status="completed" data-task-category="completed" data-display-order="1">
    <div class="card-body">
      <a href="/tasks/t-333?from=tasks" title="Fix login bug">Fix login bug</a>
    </div>
  </div>
</div></body></html>`

func TestListTasksScrapesBoard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tasks" {
			t.Errorf("path = %s, want /tasks", r.URL.Path)
		}
		if got := r.URL.Query().Get("project_id"); got != "p1" {
			t.Errorf("project_id = %q, want p1", got)
		}
		if r.Header.Get("HX-Request") != "true" {
			t.Error("expected HX-Request header")
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(boardHTML))
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := c.ListTasks(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Fatalf("got %d tasks, want 3: %+v", len(tasks), tasks)
	}
	first := tasks[0]
	if first.ID != "t-111" || first.Title != "Refactor the API" {
		t.Errorf("first task = %+v", first)
	}
	if first.Category != "backlog" || first.Status != "pending" || first.DisplayOrder != 2 {
		t.Errorf("first task metadata = %+v", first)
	}
	if first.Prompt != "Split the handler package" {
		t.Errorf("prompt = %q", first.Prompt)
	}
	if tasks[1].Category != "active" || tasks[2].Category != "completed" {
		t.Errorf("categories = %q %q", tasks[1].Category, tasks[2].Category)
	}
}

// The kebab menu ("Run"/"Cancel"/"Edit") is rendered before the title in the
// DOM, so taking the card's first line of text yields a menu label instead of
// the task title. Titles must come from the card's own task link.
func TestListTasksTitleIsNotKebabMenuText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(boardHTML))
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	tasks, err := c.ListTasks(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"t-111": "Refactor the API",
		"t-222": "Write docs",
		"t-333": "Fix login bug",
	}
	for _, task := range tasks {
		if got := want[task.ID]; task.Title != got {
			t.Errorf("task %s title = %q, want %q", task.ID, task.Title, got)
		}
		for _, bad := range []string{"Run", "Cancel", "Edit", "X"} {
			if task.Title == bad {
				t.Errorf("task %s title picked up menu text %q", task.ID, bad)
			}
		}
		if task.Title == "" {
			t.Errorf("task %s has an empty title", task.ID)
		}
	}
}

// A card's badges (Goal, model, tag) should be captured for display.
func TestListTasksCapturesBadges(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(boardHTML))
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	tasks, _ := c.ListTasks(context.Background(), "p1")
	if len(tasks) == 0 {
		t.Fatal("no tasks")
	}
	got := strings.Join(tasks[0].Badges, ",")
	if !strings.Contains(got, "Goal") || !strings.Contains(got, "Sonnet") {
		t.Errorf("badges = %q, want Goal and Sonnet", got)
	}
	if len(tasks[2].Badges) != 0 {
		t.Errorf("task without badges got %v", tasks[2].Badges)
	}
}

// A swarm parent card embeds links to its children; those must not be mistaken
// for the parent's own title link.
func TestListTasksIgnoresSwarmChildLinks(t *testing.T) {
	const swarmHTML = `<div>
	  <div class="card" data-task-id="parent-1" data-task-status="running" data-task-category="active">
	    <div class="card-body">
	      <a href="/tasks/parent-1?from=tasks" title="Swarm parent">Swarm parent</a>
	      <details>
	        <a href="/tasks/child-9?from=tasks" title="Child worker">Child worker</a>
	      </details>
	    </div>
	  </div>
	</div>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(swarmHTML))
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	tasks, _ := c.ListTasks(context.Background(), "p1")
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1 (children are not cards): %+v", len(tasks), tasks)
	}
	if tasks[0].Title != "Swarm parent" {
		t.Errorf("title = %q, want Swarm parent", tasks[0].Title)
	}
}

func TestListTasksUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/login")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	if _, err := c.ListTasks(context.Background(), ""); err == nil ||
		!strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("err = %v, want unauthorized", err)
	}
}

func TestGetTaskForProjectExactRejectsMismatchedMetadataBeforeLazyLoads(t *testing.T) {
	const taskID = "0123456789abcdef0123456789abcdef"

	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "foreign project",
			body: `<div data-task-id="` + taskID + `" data-project-id="p2"><h2 class="font-bold">Foreign title</h2></div>`,
		},
		{
			name: "different task",
			body: `<div data-task-id="fedcba9876543210fedcba9876543210" data-project-id="p1"><h2 class="font-bold">Other title</h2></div>`,
		},
		{
			name: "missing identity",
			body: `<div data-project-id="p1"><h2 class="font-bold">Unverified title</h2></div>`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != "/tasks/"+taskID {
					t.Fatalf("unexpected lazy request %s", r.URL.RequestURI())
				}
				if got := r.URL.Query().Get("project_id"); got != "p1" {
					t.Fatalf("project_id = %q, want p1", got)
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c, err := New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			detail, err := c.GetTaskForProjectExact(context.Background(), taskID, "p1")
			if err == nil || detail != nil {
				t.Fatalf("GetTaskForProjectExact = (%#v, %v), want nil detail and error", detail, err)
			}
			if strings.Contains(err.Error(), "Foreign title") || strings.Contains(err.Error(), "Other title") || strings.Contains(err.Error(), "p2") {
				t.Fatalf("error leaked foreign metadata: %v", err)
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("requests = %d, want only the detail request", got)
			}
		})
	}
}

func TestGetTaskForProjectExactPreservesScopedDetailAndCancellation(t *testing.T) {
	const taskID = "0123456789abcdef0123456789abcdef"
	var boardRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tasks" {
			boardRequests.Add(1)
			t.Fatal("exact detail lookup must not request the board")
		}
		if got := r.URL.Query().Get("project_id"); got != "p1" {
			t.Fatalf("%s project_id = %q, want p1", r.URL.Path, got)
		}
		switch r.URL.Path {
		case "/tasks/" + taskID:
			_, _ = w.Write([]byte(`<div data-task-id="` + taskID + `" data-project-id="p1" data-task-status="running" data-task-category="active"><h2 class="font-bold">Exact task</h2><div id="tab-details">details</div><div id="tab-chat"></div><div id="tab-changes"></div><div id="tab-lifecycle">life</div></div>`))
		case "/tasks/" + taskID + "/thread":
			_, _ = w.Write([]byte(`<div>thread</div>`))
		case "/tasks/" + taskID + "/changes":
			_, _ = w.Write([]byte(`<div>changes</div>`))
		default:
			t.Fatalf("unexpected request %s", r.URL.RequestURI())
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := c.GetTaskForProjectExact(context.Background(), taskID, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Task.ID != taskID || detail.Task.ProjectID != "p1" || detail.Task.Title != "Exact task" || detail.Thread != "thread" || detail.Changes != "changes" {
		t.Fatalf("detail = %#v", detail)
	}
	if got := boardRequests.Load(); got != 0 {
		t.Fatalf("board requests = %d, want 0", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.GetTaskForProjectExact(ctx, taskID, "p1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v, want context.Canceled", err)
	}
}

func TestGetTaskMetadataForProjectExactParsesRealDetailMarkup(t *testing.T) {
	const taskID = "0123456789abcdef0123456789abcdef"
	prompt := "  alpha  \n\t beta   " + strings.Repeat("界", 300) + "  TAIL"
	promptRunes := []rune(prompt)
	boardRoot, err := html.Parse(strings.NewReader(`<div data-task-id="` + taskID + `" data-task-status="running" data-task-category="active"><a href="/tasks/` + taskID + `" title="Exact task">Exact task</a><p class="line-clamp-2">` + string(promptRunes[:300]) + `</p></div>`))
	if err != nil {
		t.Fatal(err)
	}
	boardTasks := parseTaskCards(boardRoot, "p1")
	if len(boardTasks) != 1 {
		t.Fatalf("board tasks = %#v", boardTasks)
	}
	boardPrompt := boardTasks[0].Prompt
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/tasks/"+taskID || r.URL.Query().Get("project_id") != "p1" {
			t.Fatalf("unexpected request %s", r.URL.RequestURI())
		}
		_, _ = io.WriteString(w, `<div id="task-detail-content">
			<span class="hidden" data-openvibely-page-title="Exact task - OpenVibely"></span>
			<a id="task-back-btn" data-project-id="p1">Tasks</a>
			<div data-breadcrumb-selector><button data-breadcrumb-selector-button><span>Exact task</span></button></div>
			<div id="tab-details"><div id="task-detail-view">
				<div id="task-detail-metrics" data-task-status="running">
					<div><span>Category:</span><span class="badge">active</span></div>
					<div><span>Tag:</span><span class="badge">Bug</span></div>
					<div><span>Priority:</span><span class="badge">High</span></div>
					<div><span>Model:</span><span class="badge">Default model</span></div>
					<div><span>Agent:</span><span class="badge">Planner</span></div>
				</div>
				<div class="card"><h3>Swarm Overview</h3></div>
				<div id="task-prompt-panel"><div>Prompt</div><div class="textarea">`+prompt+`</div></div>
				<div id="task-goal-panel" data-task-id="`+taskID+`"><span class="badge">Active</span></div>
			</div>
			<form><input name="title" value="Exact task"><select name="category"><option value="active" selected>active</option></select>
				<select name="priority"><option value="3" selected>High</option></select>
				<select name="tag"><option value="bug" selected>Bug</option></select>
				<textarea name="prompt">`+prompt+`</textarea>
				<select name="agent_id"><option value="" selected>Use Default Model</option><option value="model-1">Claude Sonnet (Default)</option></select>
				<select name="agent_definition_id"><option value="">No Agent</option><option value="planner" selected>Planner</option></select>
			</form>
			<form><input type="checkbox" name="chain_enabled" checked></form>
			</div></div>`)
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := c.GetTaskMetadataForProjectExact(context.Background(), taskID, "p1")
	if err != nil {
		t.Fatal(err)
	}
	want := Task{
		ID: taskID, ProjectID: "p1", Title: "Exact task", Prompt: boardPrompt,
		Category: "active", Status: "running", DisplayOrder: 0,
		Badges: []string{"Chain", "Goal", "Swarm", "Claude Sonnet", "Planner", "Bug", "High"},
	}
	if !reflect.DeepEqual(detail.Task, want) {
		t.Fatalf("task = %#v, want %#v", detail.Task, want)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want only scoped detail", got)
	}
}

func TestGetTaskCollectsTabs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		// Mirrors the real task detail page: an <h2 class="... font-bold">
		// heading plus #tab-<name> panels. The thread and changes panels are
		// empty placeholders that the browser fills via a lazy hx-get.
		case "/tasks/t-1":
			_, _ = w.Write([]byte(`<div data-task-status="running" data-task-category="active">
				<h2 class="text-2xl font-bold truncate">Refactor the API</h2>
				<div id="tab-details"><div id="task-detail-view">prompt goes here</div></div>
				<div id="tab-chat" hx-get="/tasks/t-1/thread"></div>
					<div id="tab-changes" hx-get="/tasks/t-1/changes"></div>
					<div id="tab-review">inline comments</div>
					<div id="tab-schedules">daily at 09:00</div>
				<div id="tab-chaining">chains into Deploy</div>
				<div id="tab-attachments">spec.md</div>
				<div id="tab-lifecycle">pre_task ok</div>
			</div>`))
		case "/tasks/t-1/thread":
			_, _ = w.Write([]byte(`<div>agent: on it</div>`))
		case "/tasks/t-1/changes":
			_, _ = w.Write([]byte(`<div>3 files changed</div>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	d, err := c.GetTask(context.Background(), "t-1")
	if err != nil {
		t.Fatal(err)
	}
	if d.Task.Title != "Refactor the API" {
		t.Errorf("title = %q", d.Task.Title)
	}
	if d.Task.Status != "running" {
		t.Errorf("status = %q", d.Task.Status)
	}
	for name, want := range map[string]string{
		"thread":      "on it",
		"changes":     "3 files changed",
		"review":      "inline comments",
		"schedules":   "daily at 09:00",
		"chaining":    "Deploy",
		"attachments": "spec.md",
		"lifecycle":   "pre_task ok",
	} {
		if got := d.TabText(name); !strings.Contains(got, want) {
			t.Errorf("tab %s = %q, want to contain %q", name, got, want)
		}
	}
	if !strings.Contains(d.Details, "prompt goes here") {
		t.Errorf("details = %q", d.Details)
	}
	if d.Task.Category != "active" {
		t.Errorf("category = %q", d.Task.Category)
	}
}

func TestTaskDetailTabMetadataAliasesMatchTabText(t *testing.T) {
	d := TaskDetail{
		Details:  "details body",
		Thread:   "thread body",
		Changes:  "changes body",
		Review:   "review body",
		Schedule: "schedule body",
		Chaining: "chaining body",
		Attach:   "attach body",
		Life:     "life body",
	}
	aliases := map[string]string{
		"chat":     "thread body",
		"diff":     "changes body",
		"reviews":  "review body",
		"schedule": "schedule body",
		"chain":    "chaining body",
		"attach":   "attach body",
	}
	for alias, want := range aliases {
		if got := d.TabText(alias); got != want {
			t.Errorf("TabText(%q) = %q, want %q", alias, got, want)
		}
		if _, ok := TaskDetailTabByName(alias); !ok {
			t.Errorf("TaskDetailTabByName(%q) did not resolve", alias)
		}
	}
	for _, tab := range TaskDetailTabs() {
		if tab.Name == "" || tab.Label == "" || len(tab.PanelIDs) == 0 {
			t.Errorf("incomplete tab metadata: %#v", tab)
		}
		if got := tab.Text(&d); got == "" {
			t.Errorf("tab %s did not read its TaskDetail field", tab.Name)
		}
	}
}

func TestSendTaskThreadMessageForProjectReturnsExecutionOrQueueIdentity(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want TaskFollowupAccepted
	}{
		{name: "direct", body: `<div data-execution-pair="true" data-exec-id="exec-1" data-exec-status="running"></div>`, want: TaskFollowupAccepted{ExecID: "exec-1"}},
		{name: "queued", body: `<div data-task-id="task-1"><div data-thread-input-id="input-1" data-task-id="task-1" data-input-mode="queued"></div></div>`, want: TaskFollowupAccepted{PendingInputID: "input-1", Queued: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/tasks/task-1/thread" || r.URL.Query().Get("project_id") != "project-2" {
					t.Fatalf("request = %s %s", r.Method, r.URL.String())
				}
				if r.Header.Get("HX-Request") != "true" || r.FormValue("message") != "continue" {
					t.Fatalf("headers/form = %q %q", r.Header.Get("HX-Request"), r.FormValue("message"))
				}
				fmt.Fprint(w, tc.body)
			}))
			got, err := c.SendTaskThreadMessageForProject(context.Background(), "task-1", "project-2", "continue")
			if err != nil {
				t.Fatal(err)
			}
			if *got != tc.want {
				t.Fatalf("accepted = %#v, want %#v", *got, tc.want)
			}
		})
	}
}

func TestTaskMutationsUseWebUIRoutes(t *testing.T) {
	type call struct{ method, path, body string }
	var got call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 512)
		n, _ := r.Body.Read(buf)
		got = call{r.Method, r.URL.Path, string(buf[:n])}
		if r.Header.Get("HX-Request") != "true" {
			t.Error("mutations must send HX-Request")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	ctx := context.Background()

	tests := []struct {
		name   string
		fn     func() error
		method string
		path   string
		body   string
	}{
		{"run", func() error { return c.RunTask(ctx, "t1") }, "POST", "/tasks/t1/run", ""},
		{"cancel", func() error { return c.CancelTask(ctx, "t1") }, "POST", "/tasks/t1/cancel", ""},
		{"delete", func() error { return c.DeleteTask(ctx, "t1") }, "DELETE", "/tasks/t1", ""},
		{"move", func() error { return c.MoveTask(ctx, "t1", "active") }, "PATCH", "/tasks/t1/category", "category=active"},
		{"reorder", func() error { return c.ReorderTask(ctx, "t1", 3) }, "PATCH", "/tasks/t1/reorder", "position=3"},
		{"thread", func() error { return c.SendTaskThreadMessage(ctx, "t1", "hi") }, "POST", "/tasks/t1/thread", "message=hi"},
		{"clear goal", func() error { return c.ClearTaskGoal(ctx, "t1") }, "POST", "/tasks/t1/goal/clear", ""},
		{"activate", func() error { return c.ActivateBacklog(ctx, "p1") }, "POST", "/tasks/backlog/activate", ""},
		{"sweep", func() error { return c.SweepCompletedTasks(ctx, "p1") }, "POST", "/tasks/move-completed", ""},
		{"clear done", func() error { return c.ClearCompletedTasks(ctx, "p1") }, "DELETE", "/tasks/completed", ""},
		{"clear backlog", func() error { return c.ClearBacklogTasks(ctx, "p1") }, "DELETE", "/tasks/backlog", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.fn(); err != nil {
				t.Fatal(err)
			}
			if got.method != tc.method || got.path != tc.path {
				t.Errorf("got %s %s, want %s %s", got.method, got.path, tc.method, tc.path)
			}
			if tc.body != "" && !strings.Contains(got.body, tc.body) {
				t.Errorf("body = %q, want to contain %q", got.body, tc.body)
			}
		})
	}
}

func TestCreateTaskPostsForm(t *testing.T) {
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	err := c.CreateTask(context.Background(), "p1", TaskForm{
		Title: "Ship it", Prompt: "do the thing", Category: "backlog", Priority: 2, Tag: "feature",
	})
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"title": "Ship it", "prompt": "do the thing",
		"category": "backlog", "priority": "2", "tag": "feature",
	} {
		if got := form.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestMutationSurfacesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	err := c.RunTask(context.Background(), "t1")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want to mention boom", err)
	}
}

// GetTask's trailing thread/changes/lifecycle fetches are independent of one
// another, so they must run concurrently: total latency should track the max
// of the three fetch times, not their sum.
func TestGetTaskFetchesTabsConcurrently(t *testing.T) {
	const delay = 150 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/tasks/t-1":
			_, _ = w.Write([]byte(`<div data-task-status="running" data-task-category="active">
				<h2 class="text-2xl font-bold truncate">Slow task</h2>
				<div id="tab-details"><div id="task-detail-view">prompt</div></div>
				<div id="tab-chat" hx-get="/tasks/t-1/thread"></div>
				<div id="tab-changes" hx-get="/tasks/t-1/changes"></div>
				<div id="tab-lifecycle"></div>
			</div>`))
		case "/tasks/t-1/thread":
			time.Sleep(delay)
			_, _ = w.Write([]byte(`<div>agent: on it</div>`))
		case "/tasks/t-1/changes":
			time.Sleep(delay)
			_, _ = w.Write([]byte(`<div>3 files changed</div>`))
		case "/api/tasks/t-1/lifecycle-executions":
			time.Sleep(delay)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"skill_key":"pre_task","when":"before","status":"ok","started_at":"now"}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	start := time.Now()
	d, err := c.GetTask(context.Background(), "t-1")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.Thread, "on it") || !strings.Contains(d.Changes, "3 files changed") ||
		!strings.Contains(d.Life, "pre_task") {
		t.Fatalf("tabs not populated: %+v", d)
	}
	// Sequential fetches would take ~3*delay; concurrent fetches should stay
	// well under 2*delay even with scheduling overhead.
	if elapsed >= 2*delay {
		t.Errorf("GetTask took %v, want well under %v (fetches should run concurrently)", elapsed, 2*delay)
	}
}

// If the thread fetch fails, GetTask must return the partial detail and a
// section-specific error while preserving the changes result.
func TestGetTaskThreadFailsChangesSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/tasks/t-1":
			_, _ = w.Write([]byte(`<div data-task-status="running" data-task-category="active">
				<h2 class="text-2xl font-bold truncate">Task</h2>
				<div id="tab-chat" hx-get="/tasks/t-1/thread">loading thread...</div>
				<div id="tab-changes" hx-get="/tasks/t-1/changes"></div>
			</div>`))
		case "/tasks/t-1/thread":
			w.WriteHeader(http.StatusInternalServerError)
		case "/tasks/t-1/changes":
			_, _ = w.Write([]byte(`<div>3 files changed</div>`))
		case "/api/tasks/t-1/lifecycle-executions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	d, err := c.GetTask(context.Background(), "t-1")
	if err == nil {
		t.Fatal("GetTask returned success after thread failure")
	}
	var loadErr *TaskDetailLoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("error = %T %v, want TaskDetailLoadError", err, err)
	}
	if loadErr.Thread == nil || !strings.Contains(loadErr.Thread.Error(), "500") {
		t.Fatalf("thread load error = %v, want server error", loadErr.Thread)
	}
	if loadErr.Changes != nil || loadErr.Lifecycle != nil {
		t.Fatalf("unexpected independent load errors: %+v", loadErr)
	}
	if !strings.Contains(d.Thread, "loading thread") {
		t.Errorf("partial thread text = %q, want initial placeholder retained", d.Thread)
	}
	if !strings.Contains(d.Changes, "3 files changed") {
		t.Errorf("changes = %q, want fetched content", d.Changes)
	}
}

// Symmetric case: changes fetch fails, thread fetch succeeds, and the
// lifecycle request remains an independent successful empty result.
func TestGetTaskChangesFailsThreadSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/tasks/t-1":
			_, _ = w.Write([]byte(`<div data-task-status="running" data-task-category="active">
				<h2 class="text-2xl font-bold truncate">Task</h2>
				<div id="tab-chat" hx-get="/tasks/t-1/thread"></div>
				<div id="tab-changes" hx-get="/tasks/t-1/changes">loading changes...</div>
			</div>`))
		case "/tasks/t-1/thread":
			_, _ = w.Write([]byte(`<div>agent: on it</div>`))
		case "/tasks/t-1/changes":
			w.WriteHeader(http.StatusInternalServerError)
		case "/api/tasks/t-1/lifecycle-executions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	d, err := c.GetTask(context.Background(), "t-1")
	if err == nil {
		t.Fatal("GetTask returned success after changes failure")
	}
	var loadErr *TaskDetailLoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("error = %T %v, want TaskDetailLoadError", err, err)
	}
	if loadErr.Changes == nil || !strings.Contains(loadErr.Changes.Error(), "500") {
		t.Fatalf("changes load error = %v, want server error", loadErr.Changes)
	}
	if loadErr.Thread != nil || loadErr.Lifecycle != nil {
		t.Fatalf("unexpected independent load errors: %+v", loadErr)
	}
	if !strings.Contains(d.Thread, "on it") {
		t.Errorf("thread = %q, want fetched content", d.Thread)
	}
	if !strings.Contains(d.Changes, "loading changes") {
		t.Errorf("partial changes text = %q, want initial placeholder retained", d.Changes)
	}
}

func TestGetTaskLifecycleFailureReturnsPartialDetail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tasks/t-1":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div data-task-status="running" data-task-category="active">
				<h2 class="font-bold">Task</h2>
				<div id="tab-details">prompt</div>
				<div id="tab-chat" hx-get="/tasks/t-1/thread"></div>
				<div id="tab-changes" hx-get="/tasks/t-1/changes"></div>
			</div>`))
		case "/tasks/t-1/thread":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div>thread loaded</div>`))
		case "/tasks/t-1/changes":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div>changes loaded</div>`))
		case "/api/tasks/t-1/lifecycle-executions":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"lifecycle unavailable"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	d, err := c.GetTask(context.Background(), "t-1")
	if err == nil {
		t.Fatal("GetTask returned success after lifecycle failure")
	}
	var loadErr *TaskDetailLoadError
	if !errors.As(err, &loadErr) || loadErr.Lifecycle == nil {
		t.Fatalf("error = %T %v, want lifecycle TaskDetailLoadError", err, err)
	}
	if !strings.Contains(loadErr.Lifecycle.Error(), "lifecycle unavailable") {
		t.Fatalf("lifecycle load error = %v, want backend detail", loadErr.Lifecycle)
	}
	if loadErr.Thread != nil || loadErr.Changes != nil {
		t.Fatalf("successful sections reported errors: %+v", loadErr)
	}
	for name, got := range map[string]string{"thread": d.Thread, "changes": d.Changes} {
		if !strings.Contains(got, "loaded") {
			t.Errorf("%s = %q, want successful partial output", name, got)
		}
	}
}

func TestGetTaskLazyAuthFailurePreservesTypedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tasks/t-1":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div><h2 class="font-bold">Task</h2><div id="tab-details">details</div><div id="tab-chat"></div><div id="tab-changes"></div></div>`))
		case "/tasks/t-1/thread":
			w.Header().Set("Location", "/login?next=%2Ftasks%2Ft-1%2Fthread")
			w.WriteHeader(http.StatusFound)
		case "/tasks/t-1/changes":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div>changes loaded</div>`))
		case "/api/tasks/t-1/lifecycle-executions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	d, err := c.GetTask(context.Background(), "t-1")
	if err == nil || !IsAuthRequired(err) {
		t.Fatalf("GetTask error = %v, want typed authentication failure", err)
	}
	var loadErr *TaskDetailLoadError
	if !errors.As(err, &loadErr) || loadErr.Thread == nil {
		t.Fatalf("error = %T %v, want thread load error", err, err)
	}
	if !IsAuthRequired(loadErr.Thread) {
		t.Fatalf("thread error = %v, want authentication failure", loadErr.Thread)
	}
	if !strings.Contains(d.Changes, "changes loaded") {
		t.Fatalf("successful changes output was lost: %q", d.Changes)
	}
}

func TestGetTaskLazyTransportFailurePreservesTypedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tasks/t-1":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div><h2 class="font-bold">Task</h2><div id="tab-details">details</div><div id="tab-chat"></div><div id="tab-changes"></div></div>`))
		case "/tasks/t-1/changes":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div>changes loaded</div>`))
		case "/api/tasks/t-1/lifecycle-executions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	c.http.Transport = htmlTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/tasks/t-1/thread" {
			return nil, &url.Error{Op: http.MethodGet, URL: r.URL.String(), Err: errors.New("connection reset")}
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	d, err := c.GetTask(context.Background(), "t-1")
	if err == nil || !IsTransportError(err) {
		t.Fatalf("GetTask error = %v, want typed transport failure", err)
	}
	var loadErr *TaskDetailLoadError
	if !errors.As(err, &loadErr) || loadErr.Thread == nil {
		t.Fatalf("error = %T %v, want thread load error", err, err)
	}
	if !IsTransportError(loadErr.Thread) {
		t.Fatalf("thread error = %v, want transport failure", loadErr.Thread)
	}
	if !strings.Contains(d.Changes, "changes loaded") {
		t.Fatalf("successful changes output was lost: %q", d.Changes)
	}
}

func TestGetTaskSuccessfulEmptyLazyFragmentsRemainEmptyWithoutError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tasks/t-1":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div><h2 class="font-bold">Task</h2><div id="tab-details">details</div><div id="tab-chat"></div><div id="tab-changes"></div></div>`))
		case "/tasks/t-1/thread", "/tasks/t-1/changes":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div></div>`))
		case "/api/tasks/t-1/lifecycle-executions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	d, err := c.GetTask(context.Background(), "t-1")
	if err != nil {
		t.Fatalf("GetTask returned error for successful empty fragments: %v", err)
	}
	if d.Thread != "" || d.Changes != "" || d.Life != "" {
		t.Fatalf("empty lazy fragments were not preserved: thread=%q changes=%q life=%q", d.Thread, d.Changes, d.Life)
	}
	if d.TabError("thread") != nil || d.TabError("changes") != nil || d.TabError("lifecycle") != nil {
		t.Fatal("successful empty fragments were marked as failed")
	}
}

// When the initial page already populated the Lifecycle tab, the lifecycle
// executions endpoint must not be hit at all.
func TestGetTaskSkipsLifecycleFetchWhenTabHasContent(t *testing.T) {
	lifecycleHit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/tasks/t-1":
			_, _ = w.Write([]byte(`<div data-task-status="running" data-task-category="active">
				<h2 class="text-2xl font-bold truncate">Task</h2>
				<div id="tab-chat" hx-get="/tasks/t-1/thread"></div>
				<div id="tab-changes" hx-get="/tasks/t-1/changes"></div>
				<div id="tab-lifecycle">pre_task ok</div>
			</div>`))
		case "/tasks/t-1/thread":
			_, _ = w.Write([]byte(`<div>agent: on it</div>`))
		case "/tasks/t-1/changes":
			_, _ = w.Write([]byte(`<div>3 files changed</div>`))
		case "/api/tasks/t-1/lifecycle-executions":
			lifecycleHit = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	d, err := c.GetTask(context.Background(), "t-1")
	if err != nil {
		t.Fatal(err)
	}
	if lifecycleHit {
		t.Error("lifecycle-executions endpoint was fetched despite tab-lifecycle already having content")
	}
	if !strings.Contains(d.Life, "pre_task ok") {
		t.Errorf("life = %q, want existing tab content preserved", d.Life)
	}
}

// A canceled context must not block GetTask's concurrent fetches; they should
// fail fast rather than hang for the server's response time.
func TestGetTaskConcurrentFetchesRespectCanceledContext(t *testing.T) {
	unblock := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/tasks/t-1":
			_, _ = w.Write([]byte(`<div data-task-status="running" data-task-category="active">
				<h2 class="text-2xl font-bold truncate">Task</h2>
				<div id="tab-chat" hx-get="/tasks/t-1/thread"></div>
				<div id="tab-changes" hx-get="/tasks/t-1/changes"></div>
			</div>`))
		case "/tasks/t-1/thread", "/tasks/t-1/changes":
			<-unblock // never sent in this test; only reached if ctx cancellation is ignored
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	defer close(unblock)

	c, _ := New(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		_, _ = c.GetTask(ctx, "t-1")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("GetTask did not return promptly for a canceled context")
	}
}

const reviewCommentsHTML = `<div id="review-comments-list" data-task-id="t-1" data-comment-count="2">
	<div class="review-comment-item flex" data-comment-id="rc-1" data-file-path="internal/client/tasks.go" data-line-number="42" data-line-type="new" data-state="open">
		<div><div><span>alice</span><span>·</span><span>internal/client/tasks.go:42</span></div><p>Needs error handling</p></div>
	</div>
	<div class="review-comment-item flex" data-comment-id="rc-2" data-file-path="internal/terminal/view.go" data-line-number="17" data-line-type="old" data-resolved="true">
		<div><div><span>bob</span><span>·</span><span>internal/terminal/view.go:17</span></div><p>Resolved note</p></div>
	</div>
</div>`

func TestListTaskReviewsParsesHTMLFragment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/tasks/t-1/reviews" {
			t.Errorf("got %s %s, want GET /tasks/t-1/reviews", r.Method, r.URL.Path)
		}
		if r.Header.Get("HX-Request") != "true" {
			t.Error("expected HX-Request header")
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(reviewCommentsHTML))
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	reviews, err := c.ListTaskReviews(context.Background(), "t-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 2 {
		t.Fatalf("got %d reviews, want 2: %+v", len(reviews), reviews)
	}
	first := reviews[0]
	if first.ID != "rc-1" || first.TaskID != "t-1" || first.FilePath != "internal/client/tasks.go" || first.LineNumber != 42 {
		t.Errorf("first review metadata = %+v", first)
	}
	if first.LineType != "new" || first.CommentText != "Needs error handling" || first.ReviewedBy != "alice" || first.State != "open" {
		t.Errorf("first review fields = %+v", first)
	}
	if !reviews[1].Resolved {
		t.Errorf("second review should be resolved: %+v", reviews[1])
	}
}

func TestReviewCommentResponsesPreserveMultilineText(t *testing.T) {
	const fragment = `<div id="review-comments-list" data-task-id="t-1" data-comment-count="2">
		<div class="review-comment-item" data-comment-id="rc-1" data-file-path="internal/client/tasks.go" data-line-number="42" data-line-type="new" data-state="open">
			<div><span data-author>alice</span><p>
First &amp; second

Third<br>Fourth
			</p></div>
		</div>
		<div class="review-comment-item" data-comment-id="rc-2" data-file-path="empty.go" data-line-number="7"><p>
	 </p></div>
	</div>`

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		method := method
		t.Run(method, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != method {
					t.Fatalf("method = %s, want %s", r.Method, method)
				}
				_, _ = w.Write([]byte(fragment))
			}))
			defer srv.Close()

			c, _ := New(srv.URL)
			var (
				reviews []ReviewComment
				err     error
			)
			if method == http.MethodGet {
				reviews, err = c.ListTaskReviews(context.Background(), "t-1")
			} else {
				reviews, err = c.AddTaskReviewComment(context.Background(), "t-1", ReviewCommentForm{})
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, want := reviews[0].CommentText, "First & second\n\nThird\nFourth"; got != want {
				t.Errorf("comment text = %q, want %q", got, want)
			}
			if got := reviews[1].CommentText; got != "" {
				t.Errorf("empty comment text = %q, want empty", got)
			}
			if got := reviews[0]; got.ID != "rc-1" || got.TaskID != "t-1" || got.FilePath != "internal/client/tasks.go" || got.LineNumber != 42 || got.LineType != "new" || got.State != "open" || got.ReviewedBy != "alice" {
				t.Errorf("review metadata changed: %+v", got)
			}
		})
	}

	root, err := html.Parse(strings.NewReader(`<p>First

Second<br>Third</p>`))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := NodeText(findNode(root, func(n *html.Node) bool { return n.Data == "p" })), "First Second\n\nThird"; got != want {
		t.Errorf("generic NodeText = %q, want unchanged %q", got, want)
	}
}

func TestAddTaskReviewCommentPostsFormAndParsesHTMLFragment(t *testing.T) {
	var method, path string
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_ = r.ParseForm()
		form = r.PostForm
		if r.Header.Get("HX-Request") != "true" {
			t.Error("expected HX-Request header")
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(reviewCommentsHTML))
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	reviews, err := c.AddTaskReviewComment(context.Background(), "t-1", ReviewCommentForm{
		FilePath:    "internal/client/tasks.go",
		LineNumber:  42,
		LineType:    "new",
		CommentText: "Needs error handling",
	})
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/tasks/t-1/reviews" {
		t.Fatalf("got %s %s, want POST /tasks/t-1/reviews", method, path)
	}
	for k, want := range map[string]string{
		"file_path": "internal/client/tasks.go", "line_number": "42", "line_type": "new", "comment_text": "Needs error handling",
	} {
		if got := form.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if len(reviews) != 2 || reviews[0].ID != "rc-1" {
		t.Fatalf("reviews = %+v", reviews)
	}
}

func TestGetTaskThreadStateRequiresOneExplicitActiveTurn(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
		err  string
	}{
		{name: "active", body: `<div data-execution-pair="true" data-exec-id="turn-1" data-exec-status="running"></div>`, want: "turn-1"},
		{name: "none", body: `<div data-execution-pair="true" data-exec-id="turn-1" data-exec-status="completed"></div>`, err: "no error"},
		{name: "multiple", body: `<div data-execution-pair="true" data-exec-id="turn-1" data-exec-status="running"></div><div data-execution-pair="true" data-exec-id="turn-2" data-exec-status="running"></div>`, err: "multiple active responses"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("project_id") != "project/one" {
					t.Fatalf("project_id = %q", r.URL.Query().Get("project_id"))
				}
				_, _ = fmt.Fprint(w, tc.body)
			}))
			state, err := c.GetTaskThreadStateForProject(context.Background(), "task/one", "project/one")
			if tc.err == "no error" {
				if err != nil {
					t.Fatal(err)
				}
				if state.ActiveTurnID != "" {
					t.Fatalf("active turn = %q, want empty", state.ActiveTurnID)
				}
				return
			}
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("error = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if state.ActiveTurnID != tc.want {
				t.Fatalf("active turn = %q, want %q", state.ActiveTurnID, tc.want)
			}
		})
	}
}

func TestSteerTaskThreadForProjectUsesGuardAndEscapedPath(t *testing.T) {
	var requestURI string
	var form url.Values
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURI = r.URL.RequestURI()
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		form = r.PostForm
		_, _ = fmt.Fprint(w, `<div data-thread-input-id="input-1" data-task-id="task/one" data-input-mode="steering"></div>`)
	}))
	accepted, err := c.SteerTaskThreadForProject(context.Background(), "task/one", "project one", "stop now", "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if requestURI != "/tasks/task%2Fone/thread/steer?project_id=project+one" && requestURI != "/tasks/task%2Fone/thread/steer?project_id=project%20one" {
		t.Fatalf("request URI = %q", requestURI)
	}
	if form.Get("message") != "stop now" || form.Get("expected_turn_id") != "turn-1" {
		t.Fatalf("form = %v", form)
	}
	if accepted.PendingInputID != "input-1" {
		t.Fatalf("accepted = %#v", accepted)
	}
}

func TestSteerTaskThreadForProjectRejectsMissingGuardBeforeHTTP(t *testing.T) {
	calls := 0
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	if _, err := c.SteerTaskThreadForProject(context.Background(), "task", "project", "message", ""); err == nil {
		t.Fatal("expected missing expected turn error")
	}
	if calls != 0 {
		t.Fatalf("request count = %d, want zero", calls)
	}
}

func TestGetTaskThreadIsProjectScopedAndOmitsControls(t *testing.T) {
	var requestURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURI = r.URL.RequestURI()
		_, _ = w.Write([]byte(`<div data-task-thread><div class="chat-message">user: investigate</div><div class="chat-message">agent: fixed</div><form><label>Model</label><select><option>Claude</option></select><button>Send</button></form></div>`))
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, err := c.GetTaskThread(context.Background(), "task/one", "project one")
	if err != nil {
		t.Fatal(err)
	}
	if requestURI != "/tasks/task%2Fone/thread?project_id=project+one" && requestURI != "/tasks/task%2Fone/thread?project_id=project%20one" {
		t.Errorf("request URI = %q", requestURI)
	}
	for _, want := range []string{"user: investigate", "agent: fixed"} {
		if !strings.Contains(body, want) {
			t.Errorf("thread body missing %q: %q", want, body)
		}
	}
	for _, unwanted := range []string{"Model", "Claude", "Send"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("thread body contains control text %q: %q", unwanted, body)
		}
	}
}
