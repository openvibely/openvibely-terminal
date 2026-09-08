package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestAgentEditContractPreservesAuthoritativeDefinition(t *testing.T) {
	var gotForm url.Values
	requests := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		if r.URL.Query().Get("project_id") != "p selected" {
			http.Error(w, "foreign scope", http.StatusNotFound)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/agents/ag%2F1/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"ag/1","name":"Old","description":"","system_prompt":"prompt","model":"inherit","tools":[],"tool_config":{"skip_default_tools":false,"disable_runtime_worktree":true},"plugins":[],"mcp_servers":[],"skills":[],"key":"old","scope":"project","project_id":"p selected","selectable_as_primary":false,"enabled":false,"permission_defaults":{"read_task_prompt":true},"model_defaults":{"temperature":0.25},"created_by":"agent","generated_status":"generated","source_refs":[]}`)
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/agents/ag%2F1/lifecycle-hooks":
			_, _ = io.WriteString(w, `[{"id":"h1","agent_id":"ag/1","when":"after_complete","skill_key":"learn","blocking":false,"enabled":false,"permissions_json":"{\"read_task_prompt\":true}","run_policy_json":"{}","payload_json":"{\"blocks\":[\"task\"]}"}]`)
		case r.Method == http.MethodPut && r.URL.EscapedPath() == "/agents/ag%2F1":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			gotForm = r.PostForm
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
	agent, err := c.GetAgent(context.Background(), "p selected", "ag/1")
	if err != nil {
		t.Fatal(err)
	}
	agent.Name = "New"
	if err := c.UpdateAgent(context.Background(), "p selected", agent); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"name": "New", "description": "", "system_prompt": "prompt", "model": "inherit", "key": "old", "scope": "project", "project_id": "p selected",
		"enabled": "false", "selectable_as_primary": "false", "tools_json": "[]", "plugins_json": "[]", "skills_json": "[]", "mcp_servers_json": "[]", "source_refs_json": "[]",
	} {
		if got := gotForm.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if gotForm.Has("lifecycle_hooks_json") {
		t.Fatalf("lifecycle hooks must be preserved by omission, got %q", gotForm.Get("lifecycle_hooks_json"))
	}
	if len(agent.LifecycleHooks) != 1 || agent.LifecycleHooks[0].PermissionsJSON == "" || agent.LifecycleHooks[0].PayloadJSON == "" {
		t.Fatalf("loaded hooks = %#v", agent.LifecycleHooks)
	}
	if len(requests) != 3 {
		t.Fatalf("requests = %v", requests)
	}
}

func TestAgentEditProtectedAndDetailFailuresDoNotPut(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"protected", `{"id":"ag1","name":"System","generated_status":"protected"}`, http.StatusOK},
		{"foreign project", `{"id":"ag1","name":"Foreign","scope":"project","project_id":"p2"}`, http.StatusOK},
		{"detail failure", `{"error":"missing"}`, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			puts := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					puts++
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			c, _ := New(srv.URL)
			if _, err := c.GetAgent(context.Background(), "p1", "ag1"); err == nil {
				t.Fatal("expected error")
			}
			if puts != 0 {
				t.Fatalf("PUTs = %d", puts)
			}
		})
	}
}
