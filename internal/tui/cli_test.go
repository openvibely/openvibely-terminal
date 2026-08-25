package tui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely-tui/internal/client"
)

// cliServer stubs the backend for headless runs.
func cliServer(t *testing.T, bodies map[string]string) (*client.Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.recordURL(r.Method, r.URL.RequestURI())
		_ = r.ParseForm()
		rec.mu.Lock()
		rec.forms = append(rec.forms, r.Method+" "+r.URL.Path+"?"+r.PostForm.Encode())
		rec.mu.Unlock()
		if body, ok := bodies[r.URL.Path]; ok {
			if strings.HasPrefix(strings.TrimSpace(body), "{") ||
				strings.HasPrefix(strings.TrimSpace(body), "[") {
				w.Header().Set("Content-Type", "application/json")
			} else {
				w.Header().Set("Content-Type", "text/html")
			}
			_, _ = w.Write([]byte(body))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c, rec
}

const cliProjects = `{"projects":[{"id":"p1","name":"demo"},{"id":"p2","name":"other"}]}`

func TestCLIHelpWorksOffline(t *testing.T) {
	c, err := client.New("http://127.0.0.1:1") // nothing listening
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"help"}, false, false); err != nil {
		t.Fatalf("help failed: %v", err)
	}
	// CLI help lists bare subcommands, since that is how they are invoked
	// from a shell; the leading slash belongs to the chat window.
	got := out.String()
	if !strings.Contains(got, "tasks") {
		t.Errorf("help output missing commands:\n%s", got)
	}
	if strings.Contains(got, "/tasks") {
		t.Errorf("CLI help should not use slash form:\n%s", got)
	}
	// Every registered command must be reachable from help.
	for _, c := range commands {
		if !strings.Contains(got, c.name) {
			t.Errorf("CLI help omits %q", c.name)
		}
	}
}

func TestCLIBackendRequiredFailureIncludesRecoveryGuidance(t *testing.T) {
	c, err := client.New("http://127.0.0.1:1") // nothing listening
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err = RunCLI(c, &out, "", []string{"tasks"}, false, false)
	if err == nil {
		t.Fatal("expected backend-required command to fail")
	}
	got := err.Error()
	for _, want := range []string{
		"Unable to reach the OpenVibely backend",
		"Start or check your local OpenVibely backend",
		"-server <url>",
		"OPENVIBELY_SERVER_URL",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("error missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "Details:") || !strings.Contains(got, "GET /api/projects") {
		t.Fatalf("error should keep concise transport details after guidance:\n%s", got)
	}
}

func TestCLIRunsCommandAndPrintsResult(t *testing.T) {
	const board = `<div data-task-id="t-1" data-task-status="running" data-task-category="active">
		<a href="/tasks/t-1?from=tasks" title="Refactor the API">Refactor the API</a>
	</div>`
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/tasks":        board,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks"}, false, false); err != nil {
		t.Fatalf("tasks failed: %v", err)
	}
	if !rec.saw("GET", "/tasks") {
		t.Fatalf("no board fetch, calls:\n%s", rec.all())
	}
	if !strings.Contains(out.String(), "Refactor the API") {
		t.Errorf("output missing task title:\n%s", out.String())
	}
}

func TestCLIStatusRendersPrefetchedCounts(t *testing.T) {
	const alertsHTML = `<div data-alert-id="a-1" data-alert-scroll-anchor="a-1">
		<p class="font-semibold">Needs approval</p>
		<span class="badge">pending</span>
	</div>`
	const tasksHTML = `<div>
		<div class="card" data-task-id="t-1" data-task-status="running" data-task-category="active" data-display-order="0">
			<div class="card-body"><a href="/tasks/t-1" title="Task A">Task A</a></div>
		</div>
		<div class="card" data-task-id="t-2" data-task-status="queued" data-task-category="active" data-display-order="1">
			<div class="card-body"><a href="/tasks/t-2" title="Task B">Task B</a></div>
		</div>
	</div>`
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/alerts":       alertsHTML,
		"/tasks":        tasksHTML,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"status"}, false, false); err != nil {
		t.Fatalf("status failed: %v", err)
	}
	got := out.String()
	if !rec.saw("GET", "/alerts") {
		t.Errorf("expected alerts fetch during CLI status:\n%s", rec.all())
	}
	if !rec.saw("GET", "/tasks") {
		t.Errorf("expected tasks fetch during CLI status:\n%s", rec.all())
	}
	if got := rec.count("GET", "/alerts"); got != 1 {
		t.Errorf("CLI status made %d /alerts requests, want exactly 1:\n%s", got, rec.all())
	}
	if got := rec.count("GET", "/tasks"); got != 1 {
		t.Errorf("CLI status made %d /tasks requests, want exactly 1:\n%s", got, rec.all())
	}
	if !strings.Contains(got, "1 pending approvals") {
		t.Errorf("status missing pending alert count:\n%s", got)
	}
	if !strings.Contains(got, "2 active, 1 queued") {
		t.Errorf("status missing task counts:\n%s", got)
	}
	if strings.Contains(got, "none pending") || strings.Contains(got, "none active") {
		t.Errorf("status rendered zero-count placeholders despite mocked counts:\n%s", got)
	}
}

func TestCLIStatusUsesOneDelayedCountWave(t *testing.T) {
	const countDelay = 100 * time.Millisecond
	const alertsHTML = `<div data-alert-id="a-1" data-alert-scroll-anchor="a-1"><span class="badge">pending</span></div>`
	const tasksHTML = `<div><div data-task-id="t-1" data-task-status="running" data-task-category="active"><a href="/tasks/t-1" title="Task A">Task A</a></div></div>`

	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.recordURL(r.Method, r.URL.RequestURI())
		switch r.URL.Path {
		case "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(cliProjects))
		case "/alerts":
			time.Sleep(countDelay)
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(alertsHTML))
		case "/tasks":
			time.Sleep(countDelay)
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(tasksHTML))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"status"}, false, false); err != nil {
		t.Fatalf("status failed: %v", err)
	}
	elapsed := time.Since(start)

	if got := rec.count("GET", "/alerts"); got != 1 {
		t.Fatalf("delayed CLI status made %d /alerts requests, want 1:\n%s", got, rec.all())
	}
	if got := rec.count("GET", "/tasks"); got != 1 {
		t.Fatalf("delayed CLI status made %d /tasks requests, want 1:\n%s", got, rec.all())
	}
	if elapsed >= 2*countDelay {
		t.Fatalf("CLI status took %s; expected one delayed count-refresh wave, not two", elapsed)
	}
}

// A CLI command must run against the requested project, not the first one.
func TestCLISelectsRequestedProject(t *testing.T) {
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/tasks":        `<div></div>`,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "other", []string{"tasks"}, false, false); err != nil {
		t.Fatalf("tasks failed: %v", err)
	}
	if !rec.sawQuery("project_id=p2") {
		t.Fatalf("command was not scoped to the requested project:\n%s",
			strings.Join(rec.urls, "\n"))
	}
}

func TestCLIUnknownProjectFails(t *testing.T) {
	c, _ := cliServer(t, map[string]string{"/api/projects": cliProjects})

	var out bytes.Buffer
	err := RunCLI(c, &out, "nope", []string{"tasks"}, false, false)
	if err == nil {
		t.Fatal("expected an error for an unknown project")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("err = %v", err)
	}
}

func TestCLIUnknownCommandFails(t *testing.T) {
	c, _ := cliServer(t, nil)

	var out bytes.Buffer
	err := RunCLI(c, &out, "", []string{"frobnicate"}, false, false)
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("err = %v, want unknown command", err)
	}
}

func TestCLINoArgsFails(t *testing.T) {
	c, _ := cliServer(t, nil)
	if err := RunCLI(c, &bytes.Buffer{}, "", nil, false, false); err == nil {
		t.Fatal("expected an error with no command")
	}
}

// Leading slashes are accepted so TUI lines can be pasted into the shell.
func TestCLIAcceptsLeadingSlash(t *testing.T) {
	c, _ := client.New("http://127.0.0.1:1")
	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"/help"}, false, false); err != nil {
		t.Fatalf("/help failed: %v", err)
	}
	if !strings.Contains(out.String(), "tasks") {
		t.Errorf("output:\n%s", out.String())
	}
}

// A command that fails on the backend must exit non-zero.
func TestCLIReportsBackendErrors(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.Method, r.URL.Path)
		if r.URL.Path == "/api/projects" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(cliProjects))
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, _ := client.New(srv.URL)
	if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"tasks"}, false, false); err == nil {
		t.Fatal("expected a backend error")
	}
}

// Chat in CLI mode sends the message and prints the agent's reply.
func TestCLIChatSendsAndPrintsReply(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.Method, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/projects":
			_, _ = w.Write([]byte(cliProjects))
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/message":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"message_id":"m1","status":"processing"}`))
		case strings.HasPrefix(r.URL.Path, "/api/chat/message/"):
			_, _ = w.Write([]byte(`{"status":"completed","response":"docs shipped"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	c, _ := client.New(srv.URL)
	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"chat", "ship the docs"}, false, false); err != nil {
		t.Fatalf("chat failed: %v", err)
	}
	if !rec.saw("POST", "/api/chat/message") {
		t.Fatalf("no chat post, calls:\n%s", rec.all())
	}
	if !strings.Contains(out.String(), "docs shipped") {
		t.Errorf("output missing reply:\n%s", out.String())
	}
}

// Mutating commands work headlessly too.
func TestCLIRunsTaskMutation(t *testing.T) {
	const board = `<div data-task-id="t-1" data-task-status="pending" data-task-category="backlog">
		<a href="/tasks/t-1?from=tasks" title="Refactor the API">Refactor the API</a>
	</div>`
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/tasks":        board,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks", "run", "Refactor"}, false, false); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if !rec.saw("POST", "/tasks/t-1/run") {
		t.Fatalf("no run call, calls:\n%s", rec.all())
	}
}

// One-shot CLI mode works headlessly for the new automations actions,
// exiting cleanly on success and nonzero on a backend failure.
func TestCLIRunsAutomationsPause(t *testing.T) {
	const automationsHTML = `<div class="card" data-automation-url="/automations/au-1?project_id=p1">
		<div class="card-body relative">
			<span class="badge badge-outline badge-sm">active</span>
			<button type="button" data-automation-card-delete="au-1" data-automation-name="Native SDLC"></button>
		</div>
	</div>`
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/automations":  automationsHTML,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"automations", "pause", "Native"}, false, false); err != nil {
		t.Fatalf("pause failed: %v", err)
	}
	if !rec.saw("POST", "/automations/au-1/pause") {
		t.Fatalf("no pause call, calls:\n%s", rec.all())
	}
}

// One-shot CLI mode works headlessly for the new channels actions,
// exiting cleanly on success and nonzero on a backend failure.
func TestCLIRunsChannelsTest(t *testing.T) {
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"channels", "test", "email"}, false, false); err != nil {
		t.Fatalf("channels test failed: %v", err)
	}
	if !rec.saw("POST", "/channels/email/test") {
		t.Fatalf("no channel test call, calls:\n%s", rec.all())
	}
}

func TestCLIChannelsTestFailsOnBackendError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(cliProjects))
		case r.URL.Path == "/channels/telegram/test":
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"channels", "test", "telegram"}, false, false); err == nil {
		t.Fatal("expected a nonzero exit on backend failure")
	}
}

func TestCLIAutomationsPauseFailsOnBackendError(t *testing.T) {
	const automationsHTML = `<div class="card" data-automation-url="/automations/au-1?project_id=p1">
		<div class="card-body relative">
			<span class="badge badge-outline badge-sm">active</span>
			<button type="button" data-automation-card-delete="au-1" data-automation-name="Native SDLC"></button>
		</div>
	</div>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(cliProjects))
		case r.URL.Path == "/automations/au-1/pause":
			http.Error(w, "boom", http.StatusInternalServerError)
		case r.URL.Path == "/automations":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(automationsHTML))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"automations", "pause", "Native"}, false, false); err == nil {
		t.Fatal("expected a nonzero exit on backend failure")
	}
}

// TestCLIDestructiveCommandsRequireForce verifies the --force gate on every
// destructive one-shot CLI command: exit nonzero without the flag, exit zero
// with it, and only call the backend when --force is present.
func TestCLIDestructiveCommandsRequireForce(t *testing.T) {
	const taskBoard = `<div data-task-id="t-1" data-task-status="pending" data-task-category="backlog">
		<a href="/tasks/t-1?from=tasks" title="Refactor the API">Refactor the API</a>
	</div>`

	t.Run("tasks_delete/without_force_exits_nonzero", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{
			"/api/projects": cliProjects,
			"/tasks":        taskBoard,
		})
		err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"tasks", "delete", "Refactor"}, false, false)
		if err == nil {
			t.Fatal("expected nonzero exit without --force")
		}
		if !strings.Contains(err.Error(), "--force") {
			t.Errorf("error should mention --force, got: %v", err)
		}
		if rec.saw("DELETE", "/tasks/t-1") {
			t.Error("must not call backend without --force")
		}
	})

	t.Run("tasks_delete/with_force_calls_backend", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{
			"/api/projects": cliProjects,
			"/tasks":        taskBoard,
		})
		if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"tasks", "delete", "Refactor"}, true, false); err != nil {
			t.Fatalf("expected zero exit with --force: %v", err)
		}
		if !rec.saw("DELETE", "/tasks/t-1") {
			t.Errorf("expected backend call with --force:\n%s", rec.all())
		}
	})

	t.Run("tasks_clear/without_force_exits_nonzero", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
		err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"tasks", "clear", "completed"}, false, false)
		if err == nil {
			t.Fatal("expected nonzero exit without --force")
		}
		if !strings.Contains(err.Error(), "--force") {
			t.Errorf("error should mention --force, got: %v", err)
		}
		if rec.saw("DELETE", "/tasks/completed") {
			t.Error("must not call backend without --force")
		}
	})

	t.Run("tasks_clear/with_force_calls_backend", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
		if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"tasks", "clear", "completed"}, true, false); err != nil {
			t.Fatalf("expected zero exit with --force: %v", err)
		}
		if !rec.saw("DELETE", "/tasks/completed") {
			t.Errorf("expected backend call with --force:\n%s", rec.all())
		}
	})

	t.Run("alerts_clear/without_force_exits_nonzero", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
		err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"alerts", "clear"}, false, false)
		if err == nil {
			t.Fatal("expected nonzero exit without --force")
		}
		if !strings.Contains(err.Error(), "--force") {
			t.Errorf("error should mention --force, got: %v", err)
		}
		if rec.saw("DELETE", "/alerts") {
			t.Error("must not call backend without --force")
		}
	})

	t.Run("alerts_clear/with_force_calls_backend", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
		if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"alerts", "clear"}, true, false); err != nil {
			t.Fatalf("expected zero exit with --force: %v", err)
		}
		if !rec.saw("DELETE", "/alerts") {
			t.Errorf("expected backend call with --force:\n%s", rec.all())
		}
	})

	t.Run("agents_delete/without_force_exits_nonzero", func(t *testing.T) {
		const agentsHTML = `<div data-agent-id="ag-1" data-agent-key="reviewer"
			data-agent-name="Reviewer" data-agent-description="reviews code"
			data-agent-model="claude" data-agent-scope="project"></div>`
		c, rec := cliServer(t, map[string]string{
			"/api/projects": cliProjects,
			"/agents":       agentsHTML,
		})
		err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"agents", "delete", "Reviewer"}, false, false)
		if err == nil {
			t.Fatal("expected nonzero exit without --force")
		}
		if !strings.Contains(err.Error(), "--force") {
			t.Errorf("error should mention --force, got: %v", err)
		}
		if rec.saw("DELETE", "/agents/ag-1") {
			t.Error("must not call backend without --force")
		}
	})

	t.Run("agents_delete/with_force_calls_backend", func(t *testing.T) {
		const agentsHTML = `<div data-agent-id="ag-1" data-agent-key="reviewer"
			data-agent-name="Reviewer" data-agent-description="reviews code"
			data-agent-model="claude" data-agent-scope="project"></div>`
		c, rec := cliServer(t, map[string]string{
			"/api/projects": cliProjects,
			"/agents":       agentsHTML,
		})
		if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"agents", "delete", "Reviewer"}, true, false); err != nil {
			t.Fatalf("expected zero exit with --force: %v", err)
		}
		if !rec.saw("DELETE", "/agents/ag-1") {
			t.Errorf("expected backend call with --force:\n%s", rec.all())
		}
	})

	t.Run("channels_remove/without_force_exits_nonzero", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
		err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"channels", "remove", "email"}, false, false)
		if err == nil {
			t.Fatal("expected nonzero exit without --force")
		}
		if !strings.Contains(err.Error(), "--force") {
			t.Errorf("error should mention --force, got: %v", err)
		}
		if rec.saw("POST", "/channels/email/remove") {
			t.Error("must not call backend without --force")
		}
	})

	t.Run("channels_remove/with_force_calls_backend", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{
			"/api/projects": cliProjects,
			"/channels":     `<html><body>refreshed channels page</body></html>`,
		})
		if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"channels", "remove", "email"}, true, false); err != nil {
			t.Fatalf("expected zero exit with --force: %v", err)
		}
		if !rec.saw("POST", "/channels/email/remove") {
			t.Errorf("expected backend call with --force:\n%s", rec.all())
		}
	})
}

// --- JSON output mode tests ---

func TestCLICreatesProjectAndSupportsJSON(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.Method, r.URL.Path)
		if r.Method != http.MethodPost || r.URL.Path != "/projects" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if r.FormValue("repo_source") != "local" {
			t.Errorf("repo_source = %q", r.FormValue("repo_source"))
		}
		if r.FormValue("name") == "My Project" && r.FormValue("repo_path") != `C:\Users\me\repo` {
			t.Errorf("unexpected platform path: %q", r.FormValue("repo_path"))
		}
		id := "created-project"
		if r.FormValue("name") == "JSON Project" {
			id = "json-project"
		}
		w.Header().Set("HX-Redirect", "/tasks?project_id="+id)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"projects", "create", "My", "Project", "|", `C:\Users\me\repo`}, false, false); err != nil {
		t.Fatalf("projects create failed: %v", err)
	}
	plain := out.String()
	for _, want := range []string{"My Project", "project ID: created-project", "-project created-project", "openvibely-tui -project created-project tasks"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("plain creation output missing %q:\n%s", want, plain)
		}
	}
	if rec.saw("GET", "/api/projects") {
		t.Fatalf("creation should not require a project-list preflight:\n%s", rec.all())
	}

	out.Reset()
	if err := RunCLI(c, &out, "", []string{"projects", "create", "JSON", "Project", "/tmp/json-project"}, false, true); err != nil {
		t.Fatalf("projects create --json failed: %v", err)
	}
	var project client.Project
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &project); err != nil {
		t.Fatalf("JSON output is invalid: %v\noutput: %s", err, out.String())
	}
	if project.ID != "json-project" || project.Name != "JSON Project" || project.Path != "/tmp/json-project" {
		t.Fatalf("unexpected JSON project: %+v", project)
	}
}

func TestCLICreateProjectValidationAndBackendFailure(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.Method, r.URL.Path)
		if r.Method == http.MethodPost && r.URL.Path == "/projects" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"backend rejected project"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := RunCLI(c, &bytes.Buffer{}, "", []string{"projects", "create"}, false, false); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("missing-name validation error = %v", err)
	}
	if err := RunCLI(c, &bytes.Buffer{}, "", []string{"projects", "create", "demo"}, false, false); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("missing-path validation error = %v", err)
	}
	if rec.saw("POST", "/projects") {
		t.Fatal("validation should not call the backend")
	}

	if err := RunCLI(c, &bytes.Buffer{}, "", []string{"projects", "create", "demo", "/tmp/demo"}, false, false); err == nil || !strings.Contains(err.Error(), "backend rejected project") {
		t.Fatalf("backend failure error = %v", err)
	}
}

func TestCLIJSONTasksList(t *testing.T) {
	const board = `<div data-task-id="t-1" data-task-status="running" data-task-category="active">
		<a href="/tasks/t-1?from=tasks" title="Refactor the API">Refactor the API</a>
	</div>`
	c, _ := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/tasks":        board,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks"}, false, true); err != nil {
		t.Fatalf("tasks --json failed: %v", err)
	}
	got := out.String()
	var tasks []client.Task
	if err := json.Unmarshal([]byte(strings.TrimSpace(got)), &tasks); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, got)
	}
	if len(tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].ID != "t-1" {
		t.Errorf("unexpected task ID: %s", tasks[0].ID)
	}
}

func TestCLIJSONAlertsList(t *testing.T) {
	const alertsHTML = `<div data-alert-id="a-1" data-alert-scroll-anchor="1" data-search-text="bug pending">
		<p class="font-semibold">Login broken</p>
		<p class="text-sm opacity-60">OAuth redirect failure</p>
	</div>`
	c, _ := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/alerts":       alertsHTML,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"alerts"}, false, true); err != nil {
		t.Fatalf("alerts --json failed: %v", err)
	}
	got := out.String()
	var alerts []client.Alert
	if err := json.Unmarshal([]byte(strings.TrimSpace(got)), &alerts); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, got)
	}
	if len(alerts) != 1 {
		t.Errorf("expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Title != "Login broken" {
		t.Errorf("unexpected alert title: %s", alerts[0].Title)
	}
}

func TestCLIJSONAutomationsList(t *testing.T) {
	const automationsHTML = `<div>
		<div class="card" data-automation-url="/automations/au-1?project_id=p1">
			<span class="badge badge-outline">active</span>
			<button data-automation-card-delete="au-1" data-automation-name="Native SDLC"></button>
		</div>
		<div class="card" data-automation-url="/automations/au-2?project_id=p1">
			<span class="badge badge-outline">paused</span>
			<button data-automation-card-delete="au-2" data-automation-name="GitHub SDLC"></button>
		</div>
	</div>`
	c, _ := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/automations":  automationsHTML,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"automations"}, false, true); err != nil {
		t.Fatalf("automations --json failed: %v", err)
	}
	var automations []client.Automation
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &automations); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out.String())
	}
	if len(automations) != 2 || automations[0].ID != "au-1" || automations[0].Name != "Native SDLC" || automations[0].State != "active" {
		t.Fatalf("automations = %+v", automations)
	}
}

func TestCLIJSONAutomationsEmptyListIsArray(t *testing.T) {
	c, _ := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/automations":  `<div></div>`,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"automations"}, false, true); err != nil {
		t.Fatalf("empty automations --json failed: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "[]" {
		t.Fatalf("empty automations JSON = %q, want []", got)
	}
}

func TestCLIJSONProjectsList(t *testing.T) {
	c, _ := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"projects"}, false, true); err != nil {
		t.Fatalf("projects --json failed: %v", err)
	}
	got := out.String()
	var projects []client.Project
	if err := json.Unmarshal([]byte(strings.TrimSpace(got)), &projects); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, got)
	}
	if len(projects) != 2 {
		t.Errorf("expected 2 projects, got %d", len(projects))
	}
}

func TestCLIJSONTasksShow(t *testing.T) {
	const board = `<div data-task-id="t-1" data-task-status="running" data-task-category="active">
		<a href="/tasks/t-1?from=tasks" title="Refactor the API">Refactor the API</a>
	</div>`
	c, _ := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/tasks":        board,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks", "show", "t-1"}, false, true); err != nil {
		t.Fatalf("tasks show --json failed: %v", err)
	}
	got := out.String()
	var task client.Task
	if err := json.Unmarshal([]byte(strings.TrimSpace(got)), &task); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, got)
	}
	if task.ID != "t-1" {
		t.Errorf("unexpected task ID: %s", task.ID)
	}
}

func TestCLILifecycleListsMultipleExecutionsPlainText(t *testing.T) {
	const executions = `[
		{"id":"exec-1","skill_key":"router","when":"post_task","status":"completed","started_at":"2026-01-20T10:00:00Z"},
		{"id":"exec-2","skill_key":"reviewer","when":"post_task","status":"failed","started_at":"2026-01-20T11:00:00Z"}
	]`
	c, rec := cliServer(t, map[string]string{
		"/api/projects":                       cliProjects,
		"/tasks":                              `<div data-task-id="t-1" data-task-status="completed" data-task-category="completed"><a href="/tasks/t-1" title="Refactor the API">Refactor the API</a></div>`,
		"/api/tasks/t-1/lifecycle-executions": executions,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks", "lifecycle", "t-1"}, false, false); err != nil {
		t.Fatalf("tasks lifecycle failed: %v", err)
	}
	got := out.String()
	for _, want := range []string{"exec-1", "exec-2", "router", "reviewer", "completed", "failed"} {
		if !strings.Contains(got, want) {
			t.Errorf("plain lifecycle output missing %q:\n%s", want, got)
		}
	}
	if rec.saw("GET", "/api/lifecycle-executions/exec-1/events") || rec.saw("GET", "/api/lifecycle-executions/exec-2/events") {
		t.Error("CLI execution listing must not fetch event traces for multiple executions")
	}
}

func TestCLILifecycleUsesRequestedProjectScope(t *testing.T) {
	const executions = `[{"id":"exec-1","skill_key":"router","status":"completed"}]`
	const events = `[{"id":"event-1","seq":1,"event_type":"completed","payload":{"ok":true},"created_at":"2026-01-20T10:00:00Z"}]`
	c, rec := cliServer(t, map[string]string{
		"/api/projects":                           cliProjects,
		"/tasks":                                  `<div data-task-id="t-1" data-task-status="completed" data-task-category="completed"><a href="/tasks/t-1" title="Refactor the API">Refactor the API</a></div>`,
		"/api/tasks/t-1/lifecycle-executions":     executions,
		"/api/lifecycle-executions/exec-1/events": events,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "other", []string{"tasks", "lifecycle", "t-1", "exec-1"}, false, true); err != nil {
		t.Fatalf("tasks lifecycle for selected project failed: %v", err)
	}
	for _, want := range []string{
		"GET /tasks?project_id=p2",
		"GET /api/tasks/t-1/lifecycle-executions?project_id=p2",
		"GET /api/lifecycle-executions/exec-1/events?project_id=p2",
	} {
		if !rec.sawQuery(want) {
			t.Errorf("missing selected-project request %q; calls:\n%s", want, rec.all())
		}
	}

	var decoded []client.LifecycleEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &decoded); err != nil {
		t.Fatalf("output is not lifecycle event JSON: %v\noutput: %s", err, out.String())
	}
	if len(decoded) != 1 || decoded[0].EventType != "completed" {
		t.Fatalf("decoded events = %+v", decoded)
	}
}

func TestCLILifecycleJSONEventsUseSnakeCase(t *testing.T) {
	const executions = `[{"id":"exec-1","skill_key":"router","status":"completed"}]`
	const events = `[{"id":"event-1","seq":1,"event_type":"started","payload":{"message":"ok"},"created_at":"2026-01-20T10:00:00Z"}]`
	c, _ := cliServer(t, map[string]string{
		"/api/projects":                           cliProjects,
		"/tasks":                                  `<div data-task-id="t-1" data-task-status="completed" data-task-category="completed"><a href="/tasks/t-1" title="Refactor the API">Refactor the API</a></div>`,
		"/api/tasks/t-1/lifecycle-executions":     executions,
		"/api/lifecycle-executions/exec-1/events": events,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks", "lifecycle", "t-1", "exec-1"}, false, true); err != nil {
		t.Fatalf("tasks lifecycle --json failed: %v", err)
	}
	got := strings.TrimSpace(out.String())
	var decoded []client.LifecycleEvent
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("output is not lifecycle event JSON: %v\noutput: %s", err, got)
	}
	if len(decoded) != 1 || decoded[0].Seq != 1 || decoded[0].EventType != "started" {
		t.Fatalf("decoded events = %+v", decoded)
	}
	for _, want := range []string{`"event_type"`, `"created_at"`, `"payload"`} {
		if !strings.Contains(got, want) {
			t.Errorf("JSON output missing %s: %s", want, got)
		}
	}
	if strings.Contains(got, `"EventType"`) || strings.Contains(got, `"CreatedAt"`) {
		t.Errorf("JSON output used Go field names: %s", got)
	}
}
func TestCLIJSONNonJSONModeUnchanged(t *testing.T) {
	const board = `<div data-task-id="t-1" data-task-status="running" data-task-category="active">
		<a href="/tasks/t-1?from=tasks" title="Refactor the API">Refactor the API</a>
	</div>`
	c, _ := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/tasks":        board,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks"}, false, false); err != nil {
		t.Fatalf("tasks without --json failed: %v", err)
	}
	got := out.String()
	// Without --json the output should contain the task title as styled text,
	// not a JSON array.
	if !strings.Contains(got, "Refactor the API") {
		t.Errorf("expected task title in non-JSON output:\n%s", got)
	}
	// Non-JSON output should not be a JSON array (starts with "[")
	if strings.HasPrefix(strings.TrimSpace(got), "[") {
		t.Errorf("non-JSON mode should not produce JSON array:\n%s", got)
	}
}

func TestCLIJSONTaskReviewsList(t *testing.T) {
	const board = `<div data-task-id="t-1" data-task-status="running" data-task-category="active">
		<a href="/tasks/t-1?from=tasks" title="Refactor the API">Refactor the API</a>
	</div>`
	c, _ := cliServer(t, map[string]string{
		"/api/projects":      cliProjects,
		"/tasks":             board,
		"/tasks/t-1/reviews": taskReviewHTML,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks", "reviews", "t-1"}, false, true); err != nil {
		t.Fatalf("tasks reviews --json failed: %v", err)
	}
	var reviews []client.ReviewComment
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &reviews); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out.String())
	}
	if len(reviews) != 1 || reviews[0].FilePath != "internal/client/tasks.go" || reviews[0].LineNumber != 42 {
		t.Fatalf("reviews = %+v", reviews)
	}
}

func TestCLIJSONTaskReviewsAdd(t *testing.T) {
	const board = `<div data-task-id="t-1" data-task-status="running" data-task-category="active">
		<a href="/tasks/t-1?from=tasks" title="Refactor the API">Refactor the API</a>
	</div>`
	c, rec := cliServer(t, map[string]string{
		"/api/projects":      cliProjects,
		"/tasks":             board,
		"/tasks/t-1/reviews": taskReviewHTML,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks", "reviews", "add", "Refactor", "internal/client/tasks.go:42", "Needs", "error", "handling"}, false, true); err != nil {
		t.Fatalf("tasks reviews add --json failed: %v", err)
	}
	if !rec.saw("POST", "/tasks/t-1/reviews") {
		t.Fatalf("expected add review call, calls:\n%s", rec.all())
	}
	if !rec.sawForm("comment_text=Needs+error+handling") {
		t.Fatalf("posted form missing comment text: %v", rec.forms)
	}
	var review client.ReviewComment
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &review); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out.String())
	}
	if review.ID != "rc-1" || review.CommentText != "Needs error handling" {
		t.Fatalf("review = %+v", review)
	}
}
