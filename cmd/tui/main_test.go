package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/openvibely/openvibely-tui/internal/client"
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
	} else if strings.Contains(err.Error(), password) {
		t.Fatalf("CLI authentication error exposed password: %v", err)
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
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("configured credential transport error exposed password: %v", err)
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
