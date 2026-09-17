package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentPluginStateAndMutationsUseBackendRoutes(t *testing.T) {
	type seenRequest struct {
		Method string
		Path   string
		Body   map[string]string
	}
	var seen []seenRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entry := seenRequest{Method: r.Method, Path: r.URL.EscapedPath()}
		if r.Body != nil && r.Method != http.MethodGet {
			_ = json.NewDecoder(r.Body).Decode(&entry.Body)
		}
		seen = append(seen, entry)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/agents/plugins/state":
			_, _ = w.Write([]byte(`{"marketplaces":[{"name":"official","source":"anthropics/official"}],"installed":[{"id":"playwright@official","version":"1.0.0","scope":"user","enabled":true}],"available":[{"pluginId":"stagehand@official","name":"stagehand","description":"browser","marketplaceName":"official"}],"runtime":[{"name":"playwright-mcp","plugin_id":"playwright@official","status":"failed","error":"port busy"}],"error":"partial discovery failed"}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"enabled_for_agent":true,"warning":"runtime warning"}`))
		}
	}))
	defer srv.Close()
	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	state, err := c.GetAgentPluginState(context.Background())
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if len(state.Marketplaces) != 1 || len(state.Installed) != 1 || len(state.Available) != 1 || len(state.Runtime) != 1 || state.Error != "partial discovery failed" {
		t.Fatalf("state = %#v", state)
	}
	if state.Available[0].PluginID != "stagehand@official" || state.Runtime[0].PluginID != "playwright@official" {
		t.Fatalf("state IDs = %#v", state)
	}

	if err := c.AddAgentPluginMarketplace(context.Background(), "github.com/example/plugins", "user"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := c.UpdateAgentPluginMarketplace(context.Background(), "official"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := c.DeleteAgentPluginMarketplace(context.Background(), "official"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := c.ResetAgentPluginMarketplaces(context.Background()); err != nil {
		t.Fatalf("reset: %v", err)
	}
	resp, err := c.InstallAgentPlugin(context.Background(), "stagehand@official", "user", "agent-1")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !resp.OK || !resp.EnabledForAgent || resp.Warning != "runtime warning" {
		t.Fatalf("install response = %#v", resp)
	}
	if err := c.UninstallAgentPlugin(context.Background(), "stagehand@official"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	want := []seenRequest{
		{Method: http.MethodGet, Path: "/agents/plugins/state"},
		{Method: http.MethodPost, Path: "/agents/plugins/marketplaces", Body: map[string]string{"source": "github.com/example/plugins", "scope": "user"}},
		{Method: http.MethodPost, Path: "/agents/plugins/marketplaces/official/update"},
		{Method: http.MethodDelete, Path: "/agents/plugins/marketplaces/official"},
		{Method: http.MethodPost, Path: "/agents/plugins/marketplaces/reset-defaults"},
		{Method: http.MethodPost, Path: "/agents/plugins/install", Body: map[string]string{"plugin_id": "stagehand@official", "scope": "user", "agent_id": "agent-1"}},
		{Method: http.MethodPost, Path: "/agents/plugins/uninstall", Body: map[string]string{"plugin_id": "stagehand@official"}},
	}
	if len(seen) != len(want) {
		t.Fatalf("requests = %#v, want %#v", seen, want)
	}
	for i := range want {
		if seen[i].Method != want[i].Method || seen[i].Path != want[i].Path {
			t.Fatalf("request %d = %#v, want %#v", i, seen[i], want[i])
		}
		for key, value := range want[i].Body {
			if seen[i].Body[key] != value {
				t.Fatalf("request %d body[%s] = %q, want %q (body %#v)", i, key, seen[i].Body[key], value, seen[i].Body)
			}
		}
	}
}

func TestAgentPluginMutationsValidateRequiredInputsBeforeHTTP(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer srv.Close()
	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	checks := []func() error{
		func() error { return c.AddAgentPluginMarketplace(context.Background(), "", "user") },
		func() error { return c.UpdateAgentPluginMarketplace(context.Background(), "") },
		func() error { return c.DeleteAgentPluginMarketplace(context.Background(), "") },
		func() error { _, err := c.InstallAgentPlugin(context.Background(), "", "user", ""); return err },
		func() error { return c.UninstallAgentPlugin(context.Background(), "") },
	}
	for i, check := range checks {
		if err := check(); err == nil {
			t.Fatalf("check %d succeeded, want validation error", i)
		}
	}
	if requests != 0 {
		t.Fatalf("validation made %d HTTP requests", requests)
	}
}
