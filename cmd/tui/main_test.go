package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
