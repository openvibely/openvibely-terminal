package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
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
