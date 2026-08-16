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
	if err := RunCLI(c, &out, "", []string{"help"}); err != nil {
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
	if err := RunCLI(c, &out, "demo", []string{"tasks"}); err != nil {
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
	if err := RunCLI(c, &out, "other", []string{"tasks"}); err != nil {
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
	err := RunCLI(c, &out, "nope", []string{"tasks"})
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
	err := RunCLI(c, &out, "", []string{"frobnicate"})
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("err = %v, want unknown command", err)
	}
}

func TestCLINoArgsFails(t *testing.T) {
	c, _ := cliServer(t, nil)
	if err := RunCLI(c, &bytes.Buffer{}, "", nil); err == nil {
		t.Fatal("expected an error with no command")
	}
}

// Leading slashes are accepted so TUI lines can be pasted into the shell.
func TestCLIAcceptsLeadingSlash(t *testing.T) {
	c, _ := client.New("http://127.0.0.1:1")
	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"/help"}); err != nil {
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
	if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"tasks"}); err == nil {
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
	if err := RunCLI(c, &out, "demo", []string{"chat", "ship the docs"}); err != nil {
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
	if err := RunCLI(c, &out, "demo", []string{"tasks", "run", "Refactor"}); err != nil {
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
	if err := RunCLI(c, &out, "demo", []string{"automations", "pause", "Native"}); err != nil {
		t.Fatalf("pause failed: %v", err)
	}
	if !rec.saw("POST", "/automations/au-1/pause") {
		t.Fatalf("no pause call, calls:\n%s", rec.all())
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
	if err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"automations", "pause", "Native"}); err == nil {
		t.Fatal("expected a nonzero exit on backend failure")
	}
}
