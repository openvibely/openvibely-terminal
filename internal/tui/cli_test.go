package tui

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely-tui/internal/client"
)

// cliServer stubs the backend for headless runs.
func cliServer(t *testing.T, bodies map[string]string) (*client.Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.recordURL(r.Method, r.URL.RequestURI())
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
	if err := RunCLI(c, &out, "", []string{"help"}, false); err != nil {
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

func TestCLIRunsCommandAndPrintsResult(t *testing.T) {
	const board = `<div data-task-id="t-1" data-task-status="running" data-task-category="active">
		<a href="/tasks/t-1?from=tasks" title="Refactor the API">Refactor the API</a>
	</div>`
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/tasks":        board,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks"}, false); err != nil {
		t.Fatalf("tasks failed: %v", err)
	}
	if !rec.saw("GET", "/tasks") {
		t.Fatalf("no board fetch, calls:\n%s", rec.all())
	}
	if !strings.Contains(out.String(), "Refactor the API") {
		t.Errorf("output missing task title:\n%s", out.String())
	}
}

// A CLI command must run against the requested project, not the first one.
func TestCLISelectsRequestedProject(t *testing.T) {
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/tasks":        `<div></div>`,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "other", []string{"tasks"}, false); err != nil {
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
	err := RunCLI(c, &out, "nope", []string{"tasks"}, false)
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
	err := RunCLI(c, &out, "", []string{"frobnicate"}, false)
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("err = %v, want unknown command", err)
	}
}

func TestCLINoArgsFails(t *testing.T) {
	c, _ := cliServer(t, nil)
	if err := RunCLI(c, &bytes.Buffer{}, "", nil, false); err == nil {
		t.Fatal("expected an error with no command")
	}
}

// Leading slashes are accepted so TUI lines can be pasted into the shell.
func TestCLIAcceptsLeadingSlash(t *testing.T) {
	c, _ := client.New("http://127.0.0.1:1")
	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"/help"}, false); err != nil {
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
	if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"tasks"}, false); err == nil {
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
	if err := RunCLI(c, &out, "demo", []string{"chat", "ship the docs"}, false); err != nil {
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
	if err := RunCLI(c, &out, "demo", []string{"tasks", "run", "Refactor"}, false); err != nil {
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
	if err := RunCLI(c, &out, "demo", []string{"automations", "pause", "Native"}, false); err != nil {
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
	if err := RunCLI(c, &out, "demo", []string{"channels", "test", "email"}, false); err != nil {
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
	if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"channels", "test", "telegram"}, false); err == nil {
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
	if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"automations", "pause", "Native"}, false); err == nil {
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
		err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"tasks", "delete", "Refactor"}, false)
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
		if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"tasks", "delete", "Refactor"}, true); err != nil {
			t.Fatalf("expected zero exit with --force: %v", err)
		}
		if !rec.saw("DELETE", "/tasks/t-1") {
			t.Errorf("expected backend call with --force:\n%s", rec.all())
		}
	})

	t.Run("tasks_clear/without_force_exits_nonzero", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
		err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"tasks", "clear", "completed"}, false)
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
		if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"tasks", "clear", "completed"}, true); err != nil {
			t.Fatalf("expected zero exit with --force: %v", err)
		}
		if !rec.saw("DELETE", "/tasks/completed") {
			t.Errorf("expected backend call with --force:\n%s", rec.all())
		}
	})

	t.Run("alerts_clear/without_force_exits_nonzero", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
		err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"alerts", "clear"}, false)
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
		if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"alerts", "clear"}, true); err != nil {
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
		err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"agents", "delete", "Reviewer"}, false)
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
		if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"agents", "delete", "Reviewer"}, true); err != nil {
			t.Fatalf("expected zero exit with --force: %v", err)
		}
		if !rec.saw("DELETE", "/agents/ag-1") {
			t.Errorf("expected backend call with --force:\n%s", rec.all())
		}
	})
}
