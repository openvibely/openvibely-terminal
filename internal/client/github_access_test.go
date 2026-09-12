package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const githubAuthorizedActorsFragment = `<div id="github-runtime-settings"><div class="space-y-2 mb-3"><div class="flex items-center"><span class="text-sm font-medium truncate">Alice Reviewer</span><span class="text-xs opacity-50 truncate">@alice</span><input name="permission" value="admin"><input name="github_pat" value="github-secret"></div><button type="button" hx-delete="/channels/github/authorized-actors/actor-1?project_id=project%2Ftwo">remove</button></div><div id="github-authorized-actors-add-controls"><input name="project_id" value="project/two"><button hx-post="/channels/github/authorized-actors">Add</button></div></div>`

func TestGitHubAuthorizedActorClientContractUsesScopedRoutesAndSafeStructuredOutput(t *testing.T) {
	const projectID = "project/two"
	var requests []string
	postCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/channels/github/runtime-settings":
			if r.URL.Query().Get("project_id") != projectID {
				t.Errorf("list project_id = %q, want %q", r.URL.Query().Get("project_id"), projectID)
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, githubAuthorizedActorsFragment)
		case r.Method == http.MethodPost && r.URL.Path == "/channels/github/authorized-actors":
			postCount++
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
			}
			if got := r.PostForm.Get("project_id"); got != projectID {
				t.Errorf("add project_id = %q, want %q", got, projectID)
			}
			wantLogin := "alice"
			wantDisplay := "Review Team"
			if postCount > 1 {
				wantLogin = "bob"
				wantDisplay = ""
			}
			if got := r.PostForm.Get("github_login"); got != wantLogin {
				t.Errorf("github_login = %q, want %q", got, wantLogin)
			}
			if got := r.PostForm.Get("display_name"); got != wantDisplay {
				t.Errorf("display_name = %q, want %q", got, wantDisplay)
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && r.URL.Path == "/channels/github/authorized-actors/actor-1":
			if got := r.URL.Query().Get("project_id"); got != projectID {
				t.Errorf("delete project_id = %q, want %q", got, projectID)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	actors, err := c.ListGitHubAuthorizedActors(ctx, projectID)
	if err != nil {
		t.Fatalf("ListGitHubAuthorizedActors: %v", err)
	}
	if len(actors) != 1 || actors[0].ID != "actor-1" || actors[0].Identity != "alice" || actors[0].DisplayName != "Alice Reviewer" || actors[0].ProjectID != projectID {
		t.Fatalf("actors = %#v", actors)
	}
	if !actors[0].MatchesIdentity("@ALICE") || actors[0].MatchesIdentity("Alice Reviewer") {
		t.Fatalf("GitHub identity aliases = %#v", actors[0])
	}
	encoded, err := json.Marshal(actors)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"github-secret", "permission", "admin"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("GitHub actor output leaked %q: %s", secret, encoded)
		}
	}

	if err := c.AddGitHubAuthorizedActor(ctx, projectID, "@Alice", "Review Team"); err != nil {
		t.Fatalf("AddGitHubAuthorizedActor with display name: %v", err)
	}
	if err := c.AddGitHubAuthorizedActor(ctx, projectID, "@Bob", ""); err != nil {
		t.Fatalf("AddGitHubAuthorizedActor without display name: %v", err)
	}
	if err := c.RemoveGitHubAuthorizedActor(ctx, projectID, actors[0].ID); err != nil {
		t.Fatalf("RemoveGitHubAuthorizedActor: %v", err)
	}
	if want := []string{
		"GET /channels/github/runtime-settings?project_id=project%2Ftwo",
		"POST /channels/github/authorized-actors",
		"POST /channels/github/authorized-actors",
		"GET /channels/github/runtime-settings?project_id=project%2Ftwo",
		"DELETE /channels/github/authorized-actors/actor-1?project_id=project%2Ftwo",
	}; strings.Join(requests, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests = %#v, want %#v", requests, want)
	}
}

func TestGitHubAuthorizedActorClientRejectsMissingScopeAndMalformedFragments(t *testing.T) {
	c := htmlServer(t, githubAuthorizedActorsFragment)
	err := c.AddGitHubAuthorizedActor(context.Background(), "", "@Alice", "")
	if err == nil || err.Error() != "project ID is required" {
		t.Fatalf("missing project error = %v", err)
	}

	cases := []struct {
		name string
		body string
	}{
		{name: "missing container", body: `<div id="other"></div>`},
		{name: "missing login", body: `<div id="github-runtime-settings"><div><span class="text-sm font-medium">Alice</span><button hx-delete="/channels/github/authorized-actors/actor-1?project_id=p1"></button></div></div>`},
		{name: "malformed login", body: `<div id="github-runtime-settings"><div><span class="text-sm font-medium">Alice</span><span class="text-xs opacity-50">@bad!</span><button hx-delete="/channels/github/authorized-actors/actor-1?project_id=p1"></button></div></div>`},
		{name: "foreign page scope", body: `<div id="github-runtime-settings"><div><span class="text-sm font-medium">Alice</span><span class="text-xs opacity-50">@alice</span><button hx-delete="/channels/github/authorized-actors/actor-1?project_id=p2"></button></div></div>`},
		{name: "duplicate row ID", body: `<div id="github-runtime-settings"><div><span class="text-sm font-medium">Alice</span><span class="text-xs opacity-50">@alice</span><button hx-delete="/channels/github/authorized-actors/actor-1?project_id=p1"></button></div><div><span class="text-sm font-medium">Bob</span><span class="text-xs opacity-50">@bob</span><button hx-delete="/channels/github/authorized-actors/actor-1?project_id=p1"></button></div></div>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := htmlServer(t, tc.body)
			actors, err := c.ListGitHubAuthorizedActors(context.Background(), "p1")
			if err == nil || err.Error() != "authorized channel access list unavailable" || actors != nil {
				t.Fatalf("actors = %#v, error = %v", actors, err)
			}
		})
	}

	empty := htmlServer(t, `<div id="github-runtime-settings"><p>No authorized users configured.</p></div>`)
	actors, err := empty.ListGitHubAuthorizedActors(context.Background(), "p1")
	if err != nil || actors == nil || len(actors) != 0 {
		t.Fatalf("empty actors = %#v, error = %v; want non-nil empty list", actors, err)
	}
}

func TestGitHubAuthorizedActorClientPreservesAuthAndTransportClassification(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/login")
		w.WriteHeader(http.StatusFound)
	}))
	defer auth.Close()
	c, err := New(auth.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListGitHubAuthorizedActors(context.Background(), "p1"); err == nil || !IsAuthRequired(err) {
		t.Fatalf("auth error = %v, want authentication required", err)
	}

	transport := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	transportURL := transport.URL
	transport.Close()
	c, err = New(transportURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListGitHubAuthorizedActors(context.Background(), "p1"); err == nil || !IsTransportError(err) {
		t.Fatalf("transport error = %v, want transport classification", err)
	}
}
