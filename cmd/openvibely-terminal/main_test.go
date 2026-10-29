package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/openvibely/openvibely-terminal/internal/client"
	"github.com/openvibely/openvibely-terminal/internal/terminal"
	"github.com/openvibely/openvibely-terminal/internal/testutil"
)

func TestCLISecretInputRejectsEchoingTerminal(t *testing.T) {
	if canUseCLISecretInput(os.ModeCharDevice) {
		t.Fatal("character-device stdin must not be used for a one-shot credential")
	}
	if !canUseCLISecretInput(os.ModeNamedPipe) {
		t.Fatal("piped stdin must be accepted as a non-echoing credential source")
	}
	if !canUseCLISecretInput(0) {
		t.Fatal("redirected regular-file stdin must be accepted as a credential source")
	}
}

func TestParseInterspersedFlags(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantProject string
		wantJSON    bool
		wantArgs    []string
		wantErr     string
	}{
		{name: "globals before command", args: []string{"--project", "openvibely", "chat", "tell me a story about a deer"}, wantProject: "openvibely", wantArgs: []string{"chat", "tell me a story about a deer"}},
		{name: "globals after command", args: []string{"chat", "--project", "openvibely", "tell me a story about a deer"}, wantProject: "openvibely", wantArgs: []string{"chat", "tell me a story about a deer"}},
		{name: "globals around subcommand", args: []string{"tasks", "--project=openvibely", "show", "--json", "Task with spaces"}, wantProject: "openvibely", wantJSON: true, wantArgs: []string{"tasks", "show", "Task with spaces"}},
		{name: "single dash json after operands", args: []string{"tasks", "show", "Task with spaces", "-json"}, wantJSON: true, wantArgs: []string{"tasks", "show", "Task with spaces"}},
		{name: "post-command flag belongs to command", args: []string{"chat", "--command-local", "quoted argument"}, wantArgs: []string{"chat", "--command-local", "quoted argument"}},
		{name: "option boundary", args: []string{"chat", "--", "--json", "literal"}, wantArgs: []string{"chat", "--json", "literal"}},
		{name: "outer boundary preserves json project name", args: []string{"--", "projects", "edit", "--json", "|", "--description", "changed"}, wantArgs: []string{"projects", "edit", "--json", "|", "--description", "changed"}},
		{name: "outer boundary preserves force project name", args: []string{"--", "projects", "edit", "--force", "|", "--description", "changed"}, wantArgs: []string{"projects", "edit", "--force", "|", "--description", "changed"}},
		{name: "outer boundary preserves project flag project name", args: []string{"--", "projects", "edit", "--project", "|", "--description", "changed"}, wantArgs: []string{"projects", "edit", "--project", "|", "--description", "changed"}},
		{name: "project edit reference separator", args: []string{"projects", "edit", "Alpha", "--name", "Beta", "|", "--description", "changed"}, wantArgs: []string{"projects", "edit", "Alpha", "--name", "Beta", "|", "--description", "changed"}},
		{name: "attachment separator preserves option-like filename and output option", args: []string{"--project", "demo", "tasks", "attachments", "download", "Refactor", "|", "--output", "--output", "/tmp/out"}, wantProject: "demo", wantArgs: []string{"tasks", "attachments", "download", "Refactor", "|", "--output", "--output", "/tmp/out"}},
		{name: "attachment separator preserves short option-like filename and output option", args: []string{"tasks", "attachments", "download", "Refactor", "|", "-o", "-o", "/tmp/out"}, wantArgs: []string{"tasks", "attachments", "download", "Refactor", "|", "-o", "-o", "/tmp/out"}},

		{name: "local project directory option after pipe path", args: []string{"projects", "create", "My", "Project", "|", `C:\Users\me\new repo`, "--create-directory"}, wantArgs: []string{"projects", "create", "My", "Project", "|", `C:\Users\me\new repo`, "--create-directory"}}, {name: "unknown global", args: []string{"--unknown", "chat", "hello"}, wantErr: "flag provided but not defined"},
		{name: "malformed global", args: []string{"--=value", "chat", "hello"}, wantErr: "bad flag syntax"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			project := fs.String("project", "", "")
			jsonOutput := fs.Bool("json", false, "")

			err := parseInterspersedFlags(fs, tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseInterspersedFlags() error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseInterspersedFlags() error = %v", err)
			}
			if *project != tc.wantProject || *jsonOutput != tc.wantJSON {
				t.Fatalf("flags = project %q, json %v; want project %q, json %v", *project, *jsonOutput, tc.wantProject, tc.wantJSON)
			}
			if got := fs.Args(); !slices.Equal(got, tc.wantArgs) {
				t.Fatalf("arguments = %#v, want %#v", got, tc.wantArgs)
			}
		})
	}
}

func TestInterspersedGlobalFlagsDispatch(t *testing.T) {
	var chatRequests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/projects":
			_, _ = io.WriteString(w, `{"projects":[{"id":"p1","name":"openvibely","path":"/tmp/openvibely"}]}`)
		case r.URL.Path == "/events/ws":
			live := testutil.AcceptLive(t, w, r)
			if live == nil {
				return
			}
			defer live.Close()
			if live.Command.ProjectID != "p1" || live.Command.ExecID != "m1" {
				t.Errorf("command = %+v", live.Command)
			}
			live.Delta("a deer story")
			live.Send("done", "completed")
			live.Wait()
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/message":
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
			}
			chatRequests = append(chatRequests, r.FormValue("project_id")+"|"+r.FormValue("message"))
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"message_id":"m1","status":"processing"}`)
		case strings.HasPrefix(r.URL.Path, "/api/chat/message/"):
			_, _ = io.WriteString(w, `{"status":"completed","response":"a deer story"}`)
		case r.URL.Path == "/api/tasks/reference-catalog":
			if got := r.URL.Query().Get("project_id"); got != "p1" {
				t.Errorf("task catalog project_id = %q, want p1", got)
			}
			_, _ = io.WriteString(w, `{"tasks":[{"id":"t-1","project_id":"p1","title":"Task with spaces","category":"backlog","status":"pending"}]}`)
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dispatch := func(t *testing.T, rawArgs []string) string {
		t.Helper()
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		project := fs.String("project", "", "")
		jsonOutput := fs.Bool("json", false, "")
		if err := parseInterspersedFlags(fs, rawArgs); err != nil {
			t.Fatalf("parseInterspersedFlags: %v", err)
		}
		c, err := client.New(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := terminal.RunCLIContext(ctx, c, &out, *project, fs.Args(), false, *jsonOutput); err != nil {
			t.Fatalf("RunCLI(%#v): %v", rawArgs, err)
		}
		return out.String()
	}

	message := "tell me a story about a deer"
	for _, args := range [][]string{
		{"--project", "openvibely", "chat", message},
		{"chat", "--project", "openvibely", message},
		{"chat", message, "--project=openvibely"},
	} {
		if out := dispatch(t, args); !strings.Contains(out, "a deer story") {
			t.Fatalf("chat output = %q", out)
		}
	}
	if want := []string{"p1|" + message, "p1|" + message, "p1|" + message}; !slices.Equal(chatRequests, want) {
		t.Fatalf("chat requests = %#v, want %#v", chatRequests, want)
	}

	out := dispatch(t, []string{"tasks", "--project", "openvibely", "--json"})
	if !json.Valid([]byte(out)) || !strings.Contains(out, `"title":"Task with spaces"`) {
		t.Fatalf("interspersed task JSON output = %q", out)
	}
}

func TestExecutableAttachmentDownloadDelimiterAndOptionLikeNames(t *testing.T) {
	const attachmentRows = `<div id="attachment-list" data-project-id="p1">
		<div class="attachment-row"><div><p class="text-sm font-medium">--output</p><p class="text-xs">12 B</p></div><button hx-delete="/attachments/att-output?project_id=p1"></button></div>
		<div class="attachment-row"><div><p class="text-sm font-medium">-o</p><p class="text-xs">6 B</p></div><button hx-delete="/attachments/att-short-o?project_id=p1"></button></div>
	</div>`
	payloads := map[string]string{
		"/attachments/att-output":  "binary from --output attachment",
		"/attachments/att-short-o": "binary from -o attachment",
	}
	var mu sync.Mutex
	var downloads []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/projects":
			_, _ = io.WriteString(w, `{"projects":[{"id":"p1","name":"demo"}]}`)
		case "/api/tasks/reference-catalog":
			_, _ = io.WriteString(w, `{"tasks":[{"id":"t-1","project_id":"p1","title":"Refactor","category":"backlog","status":"pending"}]}`)
		case "/tasks/t-1":
			if got := r.URL.Query().Get("project_id"); got != "p1" {
				t.Errorf("attachment list project_id = %q, want p1", got)
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, attachmentRows)
		case "/attachments/att-output", "/attachments/att-short-o":
			if got := r.URL.Query().Get("project_id"); got != "p1" {
				t.Errorf("download project_id = %q, want p1", got)
			}
			mu.Lock()
			downloads = append(downloads, r.Method+" "+r.URL.RequestURI())
			mu.Unlock()
			_, _ = io.WriteString(w, payloads[r.URL.Path])
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("OPENVIBELY_AUTH_USERNAME", "")
	t.Setenv("OPENVIBELY_AUTH_PASSWORD", "")

	binary := filepath.Join(t.TempDir(), "openvibely-terminal")
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelBuild()
	build := exec.CommandContext(buildCtx, "go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building executable: %v\n%s", err, output)
	}

	for _, tc := range []struct {
		name, reference, outputOption string
	}{
		{name: "long option filename", reference: "--output", outputOption: "--output"},
		{name: "short option filename", reference: "-o", outputOption: "-o"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outPath := filepath.Join(t.TempDir(), "download.bin")
			args := []string{"--server", srv.URL, "--project", "demo", "tasks", "attachments", "download", "Refactor", "|", tc.reference, tc.outputOption, outPath}
			cmd := exec.CommandContext(t.Context(), binary, args...)
			cmd.Env = os.Environ()
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("executable download failed: %v\n%s", err, output)
			}
			got, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatalf("reading executable download: %v", err)
			}
			want := payloads[map[string]string{"--output": "/attachments/att-output", "-o": "/attachments/att-short-o"}[tc.reference]]
			if string(got) != want {
				t.Fatalf("executable downloaded %q, want %q", got, want)
			}
		})
	}
	mu.Lock()
	defer mu.Unlock()
	if len(downloads) != 2 || downloads[0] != "GET /attachments/att-output?project_id=p1" || downloads[1] != "GET /attachments/att-short-o?project_id=p1" {
		t.Fatalf("executable download requests = %#v", downloads)
	}
}

func TestRunKeepsConfiguredLoginSynchronousForCLICommands(t *testing.T) {
	const password = "synchronous-cli-password"
	var loginFinished atomic.Bool
	var projectsRequest atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
			}
			if r.FormValue("username") != "cli-user" || r.FormValue("password") != password {
				t.Errorf("configured CLI credentials were not passed to /login")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			time.Sleep(100 * time.Millisecond)
			http.SetCookie(w, &http.Cookie{Name: "ov_session", Value: "cli-session"})
			w.Header().Set("Location", "/")
			w.WriteHeader(http.StatusFound)
			loginFinished.Store(true)
		case "/api/projects":
			projectsRequest.Store(true)
			if !loginFinished.Load() {
				t.Error("CLI project request started before configured login completed")
				http.Error(w, "login incomplete", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"projects":[{"id":"p1","name":"demo"}]}`)
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	t.Setenv("OPENVIBELY_SERVER_URL", srv.URL)
	t.Setenv("OPENVIBELY_AUTH_USERNAME", "cli-user")
	t.Setenv("OPENVIBELY_AUTH_PASSWORD", password)

	oldArgs, oldCommandLine, oldStdout := os.Args, flag.CommandLine, os.Stdout
	defer func() {
		os.Args, flag.CommandLine, os.Stdout = oldArgs, oldCommandLine, oldStdout
	}()
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	os.Stdout = devNull
	flag.CommandLine = flag.NewFlagSet("openvibely-terminal", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	os.Args = []string{"openvibely-terminal", "projects", "list"}

	if err := run(); err != nil {
		t.Fatalf("run CLI command: %v", err)
	}
	if !loginFinished.Load() || !projectsRequest.Load() {
		t.Fatalf("CLI execution did not complete login and project request: login=%v projects=%v", loginFinished.Load(), projectsRequest.Load())
	}
}

func TestLoginWithConfiguredCredentialsReusesCookieSession(t *testing.T) {
	const password = "cli-password-that-must-not-be-printed"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			if r.FormValue("username") != "cli-user" || r.FormValue("password") != password {
				w.Header().Set("Location", "/login")
				w.WriteHeader(http.StatusFound)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "ov_session", Value: "cli-session"})
			w.Header().Set("Location", "/")
			w.WriteHeader(http.StatusFound)
		case "/auth/me":
			cookie, err := r.Cookie("ov_session")
			if err != nil || cookie.Value != "cli-session" {
				t.Errorf("configured login cookie missing: %v", err)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(client.AuthStatus{Authenticated: true, Username: "cli-user"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := loginWithConfiguredCredentials(c, "cli-user", password); err != nil {
		t.Fatalf("loginWithConfiguredCredentials: %v", err)
	}
	status, err := c.AuthMe(context.Background())
	if err != nil {
		t.Fatalf("AuthMe after configured login: %v", err)
	}
	if !status.Authenticated || status.Username != "cli-user" {
		t.Fatalf("unexpected auth status: %+v", status)
	}

	// Authentication failures must remain safe to print from CLI error paths.
	if err := loginWithConfiguredCredentials(c, "cli-user", "wrong"); err == nil {
		t.Fatal("expected the test server to reject the second login")
	} else if strings.Contains(err.Error(), password) || strings.Contains(err.Error(), "cli-user") || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("CLI authentication error exposed credentials: %v", err)
	}
}

func TestConfiguredLoginFailureRedactsConfiguredServerURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	for _, scheme := range []string{"HTTP", "hTtP"} {
		t.Run(scheme, func(t *testing.T) {
			server := scheme + "://" + strings.TrimPrefix(srv.URL, "http://") + "/tenant"
			c, err := client.New(server)
			if err != nil {
				t.Fatal(err)
			}
			err = loginWithConfiguredCredentials(c, "configured-user", "configured-password")
			if err == nil {
				t.Fatal("expected configured login failure")
			}
			for _, secret := range []string{"configured-user", "configured-password"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("configured login failure leaked %q: %v", secret, err)
				}
			}
			if !strings.Contains(err.Error(), "authenticating with http://") || !strings.Contains(err.Error(), "/tenant") {
				t.Errorf("configured login failure omitted safe server context: %v", err)
			}
		})
	}
}

func TestConfiguredCredentialTransportFailureIncludesOfflineRecovery(t *testing.T) {
	const secret = "configured-secret-that-must-not-appear"
	c, err := client.New("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}

	err = loginWithConfiguredCredentials(c, "configured-user", secret)
	if err == nil {
		t.Fatal("expected configured credential transport failure")
	}
	for _, want := range []string{
		"Unable to reach the OpenVibely backend",
		"Start or check your local OpenVibely backend",
		"-server <url>",
		"OPENVIBELY_SERVER_URL",
		"Details:",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
	detailsMarker := "\nDetails: "
	detailsAt := strings.Index(err.Error(), detailsMarker)
	if detailsAt < 0 {
		t.Fatalf("configured transport error missing details marker: %v", err)
	}
	got := err.Error()
	details := got[detailsAt+len(detailsMarker):]
	if want := terminal.OfflineRecoveryMessage(c.BaseURL(), errors.New(details)); got != want {
		t.Fatalf("configured login did not use the shared recovery formatter:\n got: %s\nwant: %s", got, want)
	}
	if !client.IsLoginTransportError(err) {
		t.Fatalf("configured transport error lost its login classification: %T %v", err, err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "configured-user") {
		t.Fatalf("configured credential transport error exposed credentials: %v", err)
	}
}

func TestRunProjectEditGlobalFlagShapedNameAfterBoundary(t *testing.T) {
	for _, projectName := range []string{"--json", "--force", "--project"} {
		t.Run(projectName, func(t *testing.T) {
			puts := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/api/projects":
					_, _ = fmt.Fprintf(w, `{"projects":[{"id":"p1","name":%q,"path":"/tmp/repo"}]}`, projectName)
				case r.Method == http.MethodGet && r.URL.Path == "/api/projects/p1/settings":
					_, _ = fmt.Fprintf(w, `{"id":"p1","name":%q,"description":"old","repository_source":"local","repository_path":"/tmp/repo","github_url":"","default_agent_id":"","max_workers":null,"local_repository_paths_enabled":true,"available_agents":[]}`, projectName)
				case r.Method == http.MethodPut && r.URL.Path == "/api/projects/p1/settings":
					puts++
					w.WriteHeader(http.StatusNoContent)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			oldArgs, oldCommandLine, oldStdout := os.Args, flag.CommandLine, os.Stdout
			defer func() {
				os.Args, flag.CommandLine, os.Stdout = oldArgs, oldCommandLine, oldStdout
			}()
			devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer devNull.Close()
			os.Stdout = devNull
			flag.CommandLine = flag.NewFlagSet("openvibely-terminal", flag.ContinueOnError)
			flag.CommandLine.SetOutput(io.Discard)
			os.Args = []string{"openvibely-terminal", "--server", srv.URL, "--", "projects", "edit", projectName, "|", "--description", "changed"}

			if err := run(); err != nil {
				t.Fatalf("run: %v", err)
			}
			if puts != 1 {
				t.Fatalf("PUTs = %d", puts)
			}
		})
	}
}

func TestMalformedEnvironmentServerURLUsesInvalidCLIRecovery(t *testing.T) {
	t.Setenv("OPENVIBELY_SERVER_URL", "https://env-user:env-password-must-not-appear@ops.example/%zz?token=env-token-must-not-appear#env-fragment-must-not-appear\x1b[6m")
	t.Setenv("OPENVIBELY_AUTH_USERNAME", "")
	t.Setenv("OPENVIBELY_AUTH_PASSWORD", "")

	oldArgs, oldCommandLine, oldStdout := os.Args, flag.CommandLine, os.Stdout
	defer func() {
		os.Args, flag.CommandLine, os.Stdout = oldArgs, oldCommandLine, oldStdout
	}()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	flag.CommandLine = flag.NewFlagSet("openvibely-terminal", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	os.Args = []string{"openvibely-terminal", "status"}
	os.Stdout = write
	runErr := run()
	_ = write.Close()
	var out bytes.Buffer
	_, _ = io.Copy(&out, read)

	if runErr == nil || !strings.Contains(strings.ToLower(runErr.Error()), "invalid configured server url") {
		t.Fatalf("environment server URL error = %v, want invalid configured server URL guidance", runErr)
	}
	output := strings.ToLower(ansi.Strip(out.String()))
	for _, want := range []string{"invalid configured server url", "-server <url>", "openvibely_server_url"} {
		if !strings.Contains(output, strings.ToLower(want)) {
			t.Errorf("environment server URL output missing %q:\n%s", want, output)
		}
	}
	for _, unwanted := range []string{"offline", "backend responded", "start/check your local backend", "start or check your local openvibely backend"} {
		if strings.Contains(output, unwanted) {
			t.Errorf("environment server URL output contains misleading text %q:\n%s", unwanted, output)
		}
	}
	for _, secret := range []string{"env-user", "env-password-must-not-appear", "env-token-must-not-appear", "env-fragment-must-not-appear"} {
		if strings.Contains(output, secret) || strings.Contains(runErr.Error(), secret) {
			t.Errorf("environment server URL leaked %q:\noutput=%s\nerror=%v", secret, output, runErr)
		}
	}
}

func TestConfiguredCredentialsDoNotBlockStaticHelp(t *testing.T) {
	const secret = "help-secret-that-must-not-appear"
	t.Setenv("OPENVIBELY_SERVER_URL", "http://127.0.0.1:1")
	t.Setenv("OPENVIBELY_AUTH_USERNAME", "configured-user")
	t.Setenv("OPENVIBELY_AUTH_PASSWORD", secret)

	oldArgs := os.Args
	oldCommandLine := flag.CommandLine
	defer func() {
		os.Args = oldArgs
		flag.CommandLine = oldCommandLine
	}()
	flag.CommandLine = flag.NewFlagSet("openvibely-terminal", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	os.Args = []string{"openvibely-terminal", "help"}

	if err := run(); err != nil {
		t.Fatalf("help with configured credentials failed: %v", err)
	}
}

func TestConfiguredCredentialsDoNotBlockSetup(t *testing.T) {
	const secret = "setup-secret-that-must-not-be-used"
	t.Setenv("OPENVIBELY_SERVER_URL", "http://127.0.0.1:1")
	t.Setenv("OPENVIBELY_AUTH_USERNAME", "configured-user")
	t.Setenv("OPENVIBELY_AUTH_PASSWORD", secret)

	oldArgs, oldCommandLine, oldStdout := os.Args, flag.CommandLine, os.Stdout
	defer func() {
		os.Args, flag.CommandLine, os.Stdout = oldArgs, oldCommandLine, oldStdout
	}()
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	os.Stdout = devNull
	flag.CommandLine = flag.NewFlagSet("openvibely-terminal", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	os.Args = []string{"openvibely-terminal", "setup"}

	if err := run(); err != nil {
		t.Fatalf("setup with configured credentials failed: %v", err)
	}
}

func TestFlagHelpDoesNotPrintConfiguredPasswordOrContactBackend(t *testing.T) {
	const secret = "flag-help-secret-that-must-not-appear"
	t.Setenv("OPENVIBELY_SERVER_URL", "http://127.0.0.1:1")
	t.Setenv("OPENVIBELY_AUTH_USERNAME", "configured-user")
	t.Setenv("OPENVIBELY_AUTH_PASSWORD", secret)

	oldArgs := os.Args
	oldCommandLine := flag.CommandLine
	defer func() {
		os.Args = oldArgs
		flag.CommandLine = oldCommandLine
	}()

	for _, helpArg := range []string{"-h", "--help"} {
		t.Run(helpArg, func(t *testing.T) {
			var out bytes.Buffer
			flag.CommandLine = flag.NewFlagSet("openvibely-terminal", flag.ContinueOnError)
			flag.CommandLine.SetOutput(&out)
			os.Args = []string{"openvibely-terminal", helpArg}

			if err := run(); err != nil {
				t.Fatalf("%s with configured credentials failed: %v\noutput:\n%s", helpArg, err, out.String())
			}
			if strings.Contains(out.String(), secret) {
				t.Fatalf("%s printed configured password:\n%s", helpArg, out.String())
			}
			if !strings.Contains(out.String(), "openvibely-terminal") {
				t.Fatalf("%s did not print flag help:\n%s", helpArg, out.String())
			}
		})
	}
}

func TestLoginWithConfiguredCredentialsSkipsEmptyConfiguration(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := loginWithConfiguredCredentials(c, "", ""); err != nil {
		t.Fatalf("empty configured credentials: %v", err)
	}
	if called {
		t.Fatal("empty credential configuration made a login request")
	}
}

func TestServerFlagOverridesEnvironmentForCLI(t *testing.T) {
	var envHits, flagHits atomic.Int32
	handler := func(hits *atomic.Int32) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"projects":[{"id":"p1","name":"demo"}]}`)
		}
	}
	envSrv := httptest.NewServer(handler(&envHits))
	defer envSrv.Close()
	flagSrv := httptest.NewServer(handler(&flagHits))
	defer flagSrv.Close()
	t.Setenv("OPENVIBELY_SERVER_URL", envSrv.URL)
	t.Setenv("OPENVIBELY_AUTH_USERNAME", "")
	t.Setenv("OPENVIBELY_AUTH_PASSWORD", "")

	oldArgs, oldCommandLine, oldStdout := os.Args, flag.CommandLine, os.Stdout
	defer func() {
		os.Args, flag.CommandLine, os.Stdout = oldArgs, oldCommandLine, oldStdout
	}()
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	os.Stdout = devNull

	for _, tc := range []struct {
		args          []string
		wantEnv, want int32
	}{
		{args: []string{"projects", "list"}, wantEnv: 1},
		{args: []string{"-server", flagSrv.URL, "projects", "list"}, want: 1},
	} {
		envHits.Store(0)
		flagHits.Store(0)
		flag.CommandLine = flag.NewFlagSet("openvibely-terminal", flag.ContinueOnError)
		flag.CommandLine.SetOutput(io.Discard)
		os.Args = append([]string{"openvibely-terminal"}, tc.args...)
		if err := run(); err != nil {
			t.Fatalf("run %v: %v", tc.args, err)
		}
		if envHits.Load() < tc.wantEnv || (tc.wantEnv == 0 && envHits.Load() != 0) {
			t.Errorf("%v: env server hits = %d", tc.args, envHits.Load())
		}
		if flagHits.Load() < tc.want || (tc.want == 0 && flagHits.Load() != 0) {
			t.Errorf("%v: flag server hits = %d", tc.args, flagHits.Load())
		}
	}
}
