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
	"slices"
	"strings"
	"testing"

	"github.com/openvibely/openvibely-terminal/internal/client"
	"github.com/openvibely/openvibely-terminal/internal/terminal"
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
		{name: "unknown global", args: []string{"--unknown", "chat", "hello"}, wantErr: "flag provided but not defined"},
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
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/message":
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
			}
			chatRequests = append(chatRequests, r.FormValue("project_id")+"|"+r.FormValue("message"))
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"message_id":"m1","status":"processing"}`)
		case strings.HasPrefix(r.URL.Path, "/api/chat/message/"):
			_, _ = io.WriteString(w, `{"status":"completed","response":"a deer story"}`)
		case r.URL.Path == "/tasks":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<div data-task-id="t-1" data-task-status="pending" data-task-category="backlog"><a href="/tasks/t-1?from=tasks" title="Task with spaces">Task with spaces</a></div>`)
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
		if err := terminal.RunCLI(c, &out, *project, fs.Args(), false, *jsonOutput); err != nil {
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
	const (
		username = "configured-url-user"
		password = "configured-url-password-must-not-appear"
		token    = "configured-url-token-must-not-appear"
		fragment = "configured-url-fragment-must-not-appear"
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	for _, scheme := range []string{"HTTP", "hTtP"} {
		t.Run(scheme, func(t *testing.T) {
			server := scheme + "://" + username + ":" + password + "@" + strings.TrimPrefix(srv.URL, "http://") + "?token=" + token + "#" + fragment
			c, err := client.New(server)
			if err != nil {
				t.Fatal(err)
			}
			err = loginWithConfiguredCredentials(c, "configured-user", "configured-password")
			if err == nil {
				t.Fatal("expected configured login failure")
			}
			for _, secret := range []string{username, password, token, fragment} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("configured login failure leaked %q: %v", secret, err)
				}
			}
			if !strings.Contains(err.Error(), "authenticating with http://") {
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
				case r.Method == http.MethodGet && r.URL.Path == "/projects/p1/edit":
					_, _ = fmt.Fprintf(w, `<dialog id="edit_project_modal" data-local-repo-path-enabled="true"><form hx-put="/projects/p1"><input name="name" value=%q><textarea name="description">old</textarea><select name="repo_source"><option value="local" selected>Local</option></select><input name="repo_path" value="/tmp/repo"><input name="repo_url" value=""><select name="default_agent_config_id"><option value="" selected>Global</option></select><input name="max_workers" value=""></form></dialog>`, projectName)
				case r.Method == http.MethodPut && r.URL.Path == "/projects/p1":
					puts++
					w.Header().Set("HX-Refresh", "true")
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

func TestStreamingForegroundCommandsUseInterruptContext(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "events", args: []string{"events"}, want: true},
		{name: "events with slash", args: []string{"/events", "on"}, want: true},
		{name: "stream alias", args: []string{"stream", "on"}, want: true},
		{name: "log alias case insensitive", args: []string{"LOG", "off"}, want: true},
		{name: "tasks run remains ordinary", args: []string{"tasks", "run", "task"}, want: false},
		{name: "task reply streams", args: []string{"tasks", "reply", "task", "|", "go"}, want: true},
		{name: "chat streams", args: []string{"chat", "wait"}, want: true},
		{name: "chat alias streams", args: []string{"LEAVE", "wait"}, want: true},
		{name: "bare chat remains ordinary", args: []string{"chat"}, want: false},
		{name: "help events remains ordinary", args: []string{"help", "events"}, want: false},
		{name: "empty arguments", args: nil, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isForegroundCLICommand(tc.args); got != tc.want {
				t.Fatalf("isForegroundCLICommand(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}
