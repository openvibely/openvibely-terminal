package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/openvibely/openvibely-tui/internal/client"
	"github.com/openvibely/openvibely-tui/internal/tui"
)

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
	if want := tui.OfflineRecoveryMessage(c.BaseURL(), errors.New(details)); got != want {
		t.Fatalf("configured login did not use the shared recovery formatter:\n got: %s\nwant: %s", got, want)
	}
	if !client.IsLoginTransportError(err) {
		t.Fatalf("configured transport error lost its login classification: %T %v", err, err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "configured-user") {
		t.Fatalf("configured credential transport error exposed credentials: %v", err)
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
	flag.CommandLine = flag.NewFlagSet("openvibely-tui", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	os.Args = []string{"openvibely-tui", "help"}

	if err := run(); err != nil {
		t.Fatalf("help with configured credentials failed: %v", err)
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
			flag.CommandLine = flag.NewFlagSet("openvibely-tui", flag.ContinueOnError)
			flag.CommandLine.SetOutput(&out)
			os.Args = []string{"openvibely-tui", helpArg}

			if err := run(); err != nil {
				t.Fatalf("%s with configured credentials failed: %v\noutput:\n%s", helpArg, err, out.String())
			}
			if strings.Contains(out.String(), secret) {
				t.Fatalf("%s printed configured password:\n%s", helpArg, out.String())
			}
			if !strings.Contains(out.String(), "openvibely-tui") {
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
