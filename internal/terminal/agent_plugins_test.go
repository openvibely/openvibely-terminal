package terminal

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

const agentPluginStateJSON = `{
  "marketplaces":[{"name":"official","source":"anthropics/official","url":"https://example.invalid/official"}],
  "installed":[{"id":"playwright@official","version":"1.0.0","scope":"user","enabled":true}],
  "available":[{"pluginId":"stagehand@official","name":"stagehand","description":"browser automation","marketplaceName":"official","source":"plugins/stagehand"}],
  "runtime":[{"name":"playwright-mcp","plugin_id":"playwright@official","status":"failed","error":"port busy","tool_count":3}],
  "error":"marketplace discovery warning"
}`

const agentPluginRichJSON = `{"id":"ag-1","name":"Code Reviewer","description":"old","system_prompt":"prompt","model":"inherit","tools":["shell","read_file"],"tool_config":{"scoped_files":[{"directory":"src","permissions":["read"]}],"skip_default_tools":true,"disable_runtime_worktree":true},"plugins":["playwright@official"],"mcp_servers":[{"name":"srv","type":"stdio","command":["cmd"],"env":{"TOKEN":"redacted"},"headers":{"X-Test":"yes"}}],"skills":[{"name":"skill","description":"desc","tools":"tools","content":"body"}],"key":"reviewer","scope":"project","project_id":"p1","selectable_as_primary":true,"enabled":true,"permission_defaults":{"read_task_prompt":true,"write_agents":true},"model_defaults":{"model":"gpt-5","temperature":0.2,"max_tokens":123},"generated_status":"user_edited","source_refs":["repo:agent"],"created_by":"tester"}`

func TestAgentsPluginsHelpDocumentsActions(t *testing.T) {
	cmd := lookupCommand("agents")
	if cmd == nil {
		t.Fatal("agents command missing")
	}
	help := stripANSI(renderCommandHelp(*cmd))
	var gotUsage []string
	for _, line := range cmd.usage {
		if strings.HasPrefix(line, "agents plugins") {
			gotUsage = append(gotUsage, line)
		}
	}
	if want := agentPluginUsageLines(); !reflect.DeepEqual(gotUsage, want) {
		t.Fatalf("plugin usage lines do not match definitions\ngot:  %#v\nwant: %#v", gotUsage, want)
	}
	for _, def := range append(append([]agentPluginActionDefinition{}, agentPluginActions...), agentPluginMarketplaceActions...) {
		for _, name := range def.names {
			if name.help && name.syntax != "" && !strings.Contains(help, name.syntax) {
				t.Fatalf("help missing syntax %q:\n%s", name.syntax, help)
			}
		}
	}
	for _, example := range agentPluginExamples() {
		if !strings.Contains(help, example) {
			t.Fatalf("help missing example %q:\n%s", example, help)
		}
	}
}

func TestAgentsPluginUnknownActionsUseDefinitionSummaries(t *testing.T) {
	c, _ := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
	})
	var out bytes.Buffer
	err := RunCLI(c, &out, "demo", []string{"agents", "plugins", "wat"}, false, false)
	want := "usage: agents plugins " + agentPluginActionSummary(agentPluginActions)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("top-level unknown usage = %v, want %q output=%s", err, want, out.String())
	}

	out.Reset()
	err = RunCLI(c, &out, "demo", []string{"agents", "plugins", "marketplaces", "wat"}, false, false)
	want = "usage: agents plugins marketplaces " + agentPluginActionSummary(agentPluginMarketplaceActions)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("marketplace unknown usage = %v, want %q output=%s", err, want, out.String())
	}
}

func TestAgentsPluginParserAcceptsDocumentedActionWords(t *testing.T) {
	for _, action := range []string{"list", "state", "status"} {
		t.Run("state/"+action, func(t *testing.T) {
			c, _ := cliServer(t, map[string]string{
				"/api/projects":         cliProjects,
				"/agents/plugins/state": agentPluginStateJSON,
			})
			var out bytes.Buffer
			if err := RunCLI(c, &out, "demo", []string{"agents", "plugins", action}, false, false); err != nil {
				t.Fatalf("%s: %v\n%s", action, err, out.String())
			}
		})
	}

	marketplaceCommands := []struct {
		action string
		force  bool
		route  string
	}{
		{action: "add", route: "POST /agents/plugins/marketplaces"},
		{action: "sync", route: "POST /agents/plugins/marketplaces/official/update"},
		{action: "update", route: "POST /agents/plugins/marketplaces/official/update"},
		{action: "remove", force: true, route: "DELETE /agents/plugins/marketplaces/official"},
		{action: "delete", force: true, route: "DELETE /agents/plugins/marketplaces/official"},
		{action: "reset", force: true, route: "POST /agents/plugins/marketplaces/reset-defaults"},
		{action: "reset-defaults", force: true, route: "POST /agents/plugins/marketplaces/reset-defaults"},
	}
	for _, command := range marketplaceCommands {
		t.Run("marketplaces/"+command.action, func(t *testing.T) {
			srv, rec := agentPluginCommandServer(t, agentPluginStateJSON, agentEditListHTML, agentPluginRichJSON, nil)
			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"agents", "plugins", "marketplaces", command.action}
			if command.action == "add" {
				args = append(args, "github.com/example/plugins")
			} else if command.action != "reset" && command.action != "reset-defaults" {
				args = append(args, "official")
			}
			var out bytes.Buffer
			if err := RunCLI(c, &out, "demo", args, command.force, false); err != nil {
				t.Fatalf("%v: %v\n%s", args, err, out.String())
			}
			if !rec.sawCall(command.route) {
				t.Fatalf("%v did not call %s: %v", args, command.route, rec.callsSnapshot())
			}
		})
	}

	pluginCommands := []struct {
		action string
		args   []string
		force  bool
		route  string
	}{
		{action: "install", args: []string{"stagehand@official"}, route: "POST /agents/plugins/install"},
		{action: "uninstall", args: []string{"playwright@official"}, force: true, route: "POST /agents/plugins/uninstall"},
		{action: "remove", args: []string{"playwright@official"}, force: true, route: "POST /agents/plugins/uninstall"},
		{action: "enable", args: []string{"reviewer", "playwright@official"}, route: "PUT /agents/ag-1"},
		{action: "disable", args: []string{"reviewer", "playwright@official"}, route: "PUT /agents/ag-1"},
	}
	for _, command := range pluginCommands {
		t.Run("plugins/"+command.action, func(t *testing.T) {
			srv, rec := agentPluginCommandServer(t, agentPluginStateJSON, agentEditListHTML, agentPluginRichJSON, nil)
			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			args := append([]string{"agents", "plugins", command.action}, command.args...)
			var out bytes.Buffer
			if err := RunCLI(c, &out, "demo", args, command.force, false); err != nil {
				t.Fatalf("%v: %v\n%s", args, err, out.String())
			}
			if !rec.sawCall(command.route) {
				t.Fatalf("%v did not call %s: %v", args, command.route, rec.callsSnapshot())
			}
		})
	}
}

func TestAgentsPluginsStatePlainAndJSON(t *testing.T) {
	c, _ := cliServer(t, map[string]string{
		"/api/projects":         cliProjects,
		"/agents/plugins/state": agentPluginStateJSON,
	})
	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"agents", "plugins"}, false, false); err != nil {
		t.Fatalf("plain plugins: %v", err)
	}
	plain := out.String()
	for _, want := range []string{"marketplace discovery warning", "official", "playwright@official", "stagehand@official", "playwright-mcp", "failed", "port busy"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("plain output missing %q:\n%s", want, plain)
		}
	}

	out.Reset()
	if err := RunCLI(c, &out, "demo", []string{"agents", "plugins", "list"}, false, true); err != nil {
		t.Fatalf("json plugins: %v", err)
	}
	var state client.AgentPluginState
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &state); err != nil {
		t.Fatalf("json output was not parseable: %v\n%s", err, out.String())
	}
	if state.Marketplaces[0].Name != "official" || state.Installed[0].ID != "playwright@official" || state.Available[0].PluginID != "stagehand@official" || state.Runtime[0].Error != "port busy" || state.Error == "" {
		t.Fatalf("state = %#v", state)
	}
}

func TestAgentsPluginMarketplaceCommandsUseExpectedRoutes(t *testing.T) {
	srv, rec := agentPluginCommandServer(t, agentPluginStateJSON, agentEditListHTML, agentPluginRichJSON, nil)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	commands := []struct {
		args  []string
		force bool
	}{
		{args: []string{"agents", "plugins", "marketplaces", "add", "github.com/example/plugins"}},
		{args: []string{"agents", "plugins", "marketplaces", "sync", "official"}},
		{args: []string{"agents", "plugins", "marketplaces", "remove", "official"}, force: true},
		{args: []string{"agents", "plugins", "marketplaces", "reset"}, force: true},
	}
	for _, command := range commands {
		var out bytes.Buffer
		if err := RunCLI(c, &out, "demo", command.args, command.force, false); err != nil {
			t.Fatalf("%v: %v\n%s", command.args, err, out.String())
		}
	}
	for _, want := range []string{
		"POST /agents/plugins/marketplaces",
		"POST /agents/plugins/marketplaces/official/update",
		"DELETE /agents/plugins/marketplaces/official",
		"POST /agents/plugins/marketplaces/reset-defaults",
	} {
		if !rec.sawCall(want) {
			t.Fatalf("missing %s in calls: %v", want, rec.callsSnapshot())
		}
	}
	body := rec.jsonBody("POST /agents/plugins/marketplaces")
	if got := body["source"]; got != "github.com/example/plugins" {
		t.Fatalf("marketplace source = %q", got)
	}
	if got := body["scope"]; got != "user" {
		t.Fatalf("marketplace scope = %q", got)
	}
}

func TestAgentsPluginMarketplaceJSONRefreshFailuresStayParseable(t *testing.T) {
	commands := []struct {
		name  string
		args  []string
		force bool
	}{
		{name: "add", args: []string{"agents", "plugins", "marketplaces", "add", "github.com/example/plugins"}},
		{name: "sync", args: []string{"agents", "plugins", "marketplaces", "sync", "official"}},
		{name: "remove", args: []string{"agents", "plugins", "marketplaces", "remove", "official"}, force: true},
		{name: "reset", args: []string{"agents", "plugins", "marketplaces", "reset"}, force: true},
	}
	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/api/projects":
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, cliProjects)
				case r.Method == http.MethodPost && r.URL.Path == "/agents/plugins/marketplaces":
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"ok":true}`)
				case r.Method == http.MethodPost && r.URL.Path == "/agents/plugins/marketplaces/official/update":
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"ok":true}`)
				case r.Method == http.MethodDelete && r.URL.Path == "/agents/plugins/marketplaces/official":
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"ok":true}`)
				case r.Method == http.MethodPost && r.URL.Path == "/agents/plugins/marketplaces/reset-defaults":
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"ok":true}`)
				case r.Method == http.MethodGet && r.URL.Path == "/agents/plugins/state":
					http.Error(w, "state refresh failed", http.StatusInternalServerError)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)
			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := RunCLI(c, &out, "demo", command.args, command.force, true); err != nil {
				t.Fatalf("marketplace %s: %v\n%s", command.name, err, out.String())
			}
			var body map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &body); err != nil {
				t.Fatalf("marketplace %s emitted non-JSON: %v\n%s", command.name, err, out.String())
			}
			if body["status"] == "" || body["refresh_error"] != "saved; plugin state refresh failed" {
				t.Fatalf("marketplace %s JSON = %#v", command.name, body)
			}
		})
	}
}

func TestAgentsPluginInstallCanIncludeAgent(t *testing.T) {
	srv, rec := agentPluginCommandServer(t, agentPluginStateJSON, agentEditListHTML, agentPluginRichJSON, nil)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"agents", "plugins", "install", "stagehand@official", "Code", "Reviewer"}, false, false); err != nil {
		t.Fatalf("install: %v\n%s", err, out.String())
	}
	body := rec.jsonBody("POST /agents/plugins/install")
	if body["plugin_id"] != "stagehand@official" || body["scope"] != "user" || body["agent_id"] != "ag-1" {
		t.Fatalf("install body = %#v", body)
	}
	plain := out.String()
	if !strings.Contains(plain, "installed stagehand@official") || !strings.Contains(plain, "enabled for Code Reviewer") {
		t.Fatalf("install output: %s", plain)
	}
}

func TestAgentsPluginUninstallRequiresConfirmationOrForce(t *testing.T) {
	srv, rec := agentPluginCommandServer(t, agentPluginStateJSON, agentEditListHTML, agentPluginRichJSON, nil)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = RunCLI(c, &out, "demo", []string{"agents", "plugins", "uninstall", "playwright@official"}, false, false)
	if err == nil || !strings.Contains(err.Error(), "use --force") {
		t.Fatalf("unforced uninstall err = %v output=%s", err, out.String())
	}
	if rec.sawCall("POST /agents/plugins/uninstall") {
		t.Fatalf("unforced uninstall mutated: %v", rec.callsSnapshot())
	}
	out.Reset()
	if err := RunCLI(c, &out, "demo", []string{"agents", "plugins", "uninstall", "playwright@official"}, true, false); err != nil {
		t.Fatalf("forced uninstall: %v\n%s", err, out.String())
	}
	body := rec.jsonBody("POST /agents/plugins/uninstall")
	if !rec.sawCall("POST /agents/plugins/uninstall") || body["plugin_id"] != "playwright@official" {
		t.Fatalf("forced uninstall calls=%v body=%#v", rec.callsSnapshot(), body)
	}
}

func TestAgentsPluginEnableDisablePreservesAgentDefinition(t *testing.T) {
	var putForm map[string][]string
	stateWithStagehandInstalled := strings.Replace(agentPluginStateJSON,
		`"installed":[{"id":"playwright@official","version":"1.0.0","scope":"user","enabled":true}]`,
		`"installed":[{"id":"playwright@official","version":"1.0.0","scope":"user","enabled":true},{"id":"stagehand@official","version":"1.0.0","scope":"user","enabled":true}]`, 1)
	srv, _ := agentPluginCommandServer(t, stateWithStagehandInstalled, agentEditListHTML, agentPluginRichJSON, func(r *http.Request) {
		_ = r.ParseForm()
		putForm = map[string][]string(r.PostForm)
	})
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"agents", "plugins", "enable", "reviewer", "stagehand@official"}, false, false); err != nil {
		t.Fatalf("enable: %v\n%s", err, out.String())
	}
	if got := putFormValue(putForm, "plugins_json"); got != `["playwright@official","stagehand@official"]` {
		t.Fatalf("plugins_json = %s", got)
	}
	for key, want := range map[string]string{
		"tools_json":               `["shell","read_file"]`,
		"tool_config_json":         `{"scoped_files":[{"directory":"src","permissions":["read"]}],"skip_default_tools":true,"disable_runtime_worktree":true}`,
		"mcp_servers_json":         `[{"name":"srv","type":"stdio","command":["cmd"],"env":{"TOKEN":"redacted"},"headers":{"X-Test":"yes"}}]`,
		"skills_json":              `[{"name":"skill","description":"desc","tools":"tools","content":"body"}]`,
		"permission_defaults_json": `{"read_task_prompt":true,"write_agents":true}`,
		"source_refs_json":         `["repo:agent"]`,
		"model":                    `inherit`,
		"key":                      `reviewer`,
		"enabled":                  `true`,
		"selectable_as_primary":    `true`,
	} {
		if got := putFormValue(putForm, key); got != want {
			t.Fatalf("%s = %s, want %s", key, got, want)
		}
	}

	putForm = nil
	out.Reset()
	if err := RunCLI(c, &out, "demo", []string{"agents", "plugins", "disable", "reviewer", "playwright@official"}, false, false); err != nil {
		t.Fatalf("disable: %v\n%s", err, out.String())
	}
	if got := putFormValue(putForm, "plugins_json"); got != `[]` {
		t.Fatalf("disable plugins_json = %s", got)
	}
}

func TestAgentsPluginEnableDisableStartsInitialReadsConcurrently(t *testing.T) {
	const routeDelay = 200 * time.Millisecond

	var mu sync.Mutex
	starts := map[string]time.Time{}
	counts := map[string]int{}
	record := func(path string) {
		mu.Lock()
		defer mu.Unlock()
		if starts[path].IsZero() {
			starts[path] = time.Now()
		}
		counts[path]++
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("project_id") != "" && r.URL.Query().Get("project_id") != "p1" {
			http.Error(w, "wrong project", http.StatusNotFound)
			return
		}
		switch {
		case r.URL.Path == "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, cliProjects)
		case r.Method == http.MethodGet && r.URL.Path == "/agents/plugins/state":
			record(r.URL.Path)
			time.Sleep(routeDelay)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, agentPluginStateJSON)
		case r.Method == http.MethodGet && r.URL.Path == "/agents":
			record(r.URL.Path)
			time.Sleep(routeDelay)
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, agentEditListHTML)
		case r.Method == http.MethodGet && r.URL.Path == "/agents/ag-1/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, agentPluginRichJSON)
		case r.Method == http.MethodGet && r.URL.Path == "/agents/ag-1/lifecycle-hooks":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodPut && r.URL.Path == "/agents/ag-1":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	started := time.Now()
	if err := RunCLI(c, &out, "demo", []string{"agents", "plugins", "disable", "reviewer", "playwright@official"}, false, false); err != nil {
		t.Fatalf("disable: %v\n%s", err, out.String())
	}
	elapsed := time.Since(started)

	mu.Lock()
	stateStart := starts["/agents/plugins/state"]
	agentsStart := starts["/agents"]
	stateCount := counts["/agents/plugins/state"]
	mu.Unlock()
	if stateStart.IsZero() || agentsStart.IsZero() {
		t.Fatalf("missing delayed route starts: %#v", starts)
	}
	if delta := stateStart.Sub(agentsStart); delta > 10*time.Millisecond || delta < -10*time.Millisecond {
		t.Fatalf("initial state and agent-list reads started %s apart, want <= 10ms", delta)
	}
	if stateCount != 1 {
		t.Fatalf("plain enable/disable should not refresh global plugin state after update; state calls = %d", stateCount)
	}
	if threshold := routeDelay*2 + routeDelay/4; elapsed >= threshold {
		t.Fatalf("enable/disable latency = %s, want under %s with delayed initial reads and no final state refresh", elapsed, threshold)
	}
}

func TestAgentsPluginsFailuresDoNotMutate(t *testing.T) {
	tests := []struct {
		name, state, agentDetail string
		args                     []string
		want                     string
	}{
		{name: "unknown plugin", state: agentPluginStateJSON, agentDetail: agentPluginRichJSON, args: []string{"agents", "plugins", "enable", "reviewer", "missing@official"}, want: `plugin "missing@official" is not installed`},
		{name: "unknown agent", state: agentPluginStateJSON, agentDetail: agentPluginRichJSON, args: []string{"agents", "plugins", "enable", "missing", "playwright@official"}, want: "nothing matches"},
		{name: "protected agent", state: agentPluginStateJSON, agentDetail: strings.Replace(agentPluginRichJSON, `"user_edited"`, `"protected"`, 1), args: []string{"agents", "plugins", "enable", "reviewer", "playwright@official"}, want: "protected system agent"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var put int
			srv, rec := agentPluginCommandServer(t, tc.state, agentEditListHTML, tc.agentDetail, func(r *http.Request) { put++ })
			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err = RunCLI(c, &out, "demo", tc.args, false, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q output=%s", err, tc.want, out.String())
			}
			if put != 0 || rec.sawCall("POST /agents/plugins/install") || rec.sawCall("POST /agents/plugins/uninstall") {
				t.Fatalf("failure mutated: puts=%d calls=%v", put, rec.callsSnapshot())
			}
		})
	}
}

type agentPluginRecorder struct {
	mu         sync.Mutex
	calls      []string
	jsonBodies map[string]map[string]string
}

func (r *agentPluginRecorder) recordCall(call string, body map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
	if body != nil {
		r.jsonBodies[call] = body
	}
}

func (r *agentPluginRecorder) sawCall(call string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, got := range r.calls {
		if got == call {
			return true
		}
	}
	return false
}

func (r *agentPluginRecorder) callsSnapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func (r *agentPluginRecorder) jsonBody(call string) map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	body := r.jsonBodies[call]
	if body == nil {
		return nil
	}
	out := make(map[string]string, len(body))
	for key, value := range body {
		out[key] = value
	}
	return out
}

func agentPluginCommandServer(t *testing.T, state, agentsHTML, agentJSON string, onPut func(*http.Request)) (*httptest.Server, *agentPluginRecorder) {
	t.Helper()
	rec := &agentPluginRecorder{jsonBodies: map[string]map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := r.Method + " " + r.URL.EscapedPath()
		var body map[string]string
		if r.Method == http.MethodPost || r.Method == http.MethodDelete || r.Method == http.MethodPut {
			if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
				_ = json.NewDecoder(r.Body).Decode(&body)
			}
		}
		rec.recordCall(call, body)
		agentPluginTestHandler(t, state, agentsHTML, agentJSON, onPut).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func agentPluginTestHandler(t *testing.T, state, agentsHTML, agentJSON string, onPut func(*http.Request)) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("project_id") != "" && r.URL.Query().Get("project_id") != "p1" {
			http.Error(w, "wrong project", http.StatusNotFound)
			return
		}
		switch {
		case r.URL.Path == "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, cliProjects)
		case r.Method == http.MethodGet && r.URL.Path == "/agents/plugins/state":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, state)
		case r.Method == http.MethodPost && r.URL.Path == "/agents/plugins/marketplaces":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true}`)
		case r.Method == http.MethodPost && r.URL.Path == "/agents/plugins/marketplaces/official/update":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/agents/plugins/marketplaces/official":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true}`)
		case r.Method == http.MethodPost && r.URL.Path == "/agents/plugins/marketplaces/reset-defaults":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true}`)
		case r.Method == http.MethodPost && r.URL.Path == "/agents/plugins/install":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true,"enabled_for_agent":true}`)
		case r.Method == http.MethodPost && r.URL.Path == "/agents/plugins/uninstall":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true}`)
		case r.Method == http.MethodGet && r.URL.Path == "/agents":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, agentsHTML)
		case r.Method == http.MethodGet && r.URL.Path == "/agents/ag-1/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, agentJSON)
		case r.Method == http.MethodGet && r.URL.Path == "/agents/ag-1/lifecycle-hooks":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodPut && r.URL.Path == "/agents/ag-1":
			if onPut != nil {
				onPut(r)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
}

func putFormValue(form map[string][]string, key string) string {
	if len(form[key]) == 0 {
		return ""
	}
	return form[key][0]
}
