package terminal

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	help := renderCommandHelp(*cmd)
	for _, want := range []string{
		"agents plugins [list]",
		"agents plugins marketplaces add <source>",
		"agents plugins marketplaces sync <marketplace>",
		"agents plugins marketplaces remove <marketplace>",
		"agents plugins install <plugin-id> [agent]",
		"agents plugins uninstall <plugin-id>",
		"agents plugins enable <agent> <plugin-id>",
		"agents plugins disable <agent> <plugin-id>",
		"agents plugins install stagehand@official reviewer",
		"agents plugins enable reviewer playwright@official",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("help missing %q:\n%s", want, help)
		}
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
			t.Fatalf("missing %s in calls: %v", want, rec.calls)
		}
	}
	if got := rec.jsonBodies["POST /agents/plugins/marketplaces"]["source"]; got != "github.com/example/plugins" {
		t.Fatalf("marketplace source = %q", got)
	}
	if got := rec.jsonBodies["POST /agents/plugins/marketplaces"]["scope"]; got != "user" {
		t.Fatalf("marketplace scope = %q", got)
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
	body := rec.jsonBodies["POST /agents/plugins/install"]
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
		t.Fatalf("unforced uninstall mutated: %v", rec.calls)
	}
	out.Reset()
	if err := RunCLI(c, &out, "demo", []string{"agents", "plugins", "uninstall", "playwright@official"}, true, false); err != nil {
		t.Fatalf("forced uninstall: %v\n%s", err, out.String())
	}
	if !rec.sawCall("POST /agents/plugins/uninstall") || rec.jsonBodies["POST /agents/plugins/uninstall"]["plugin_id"] != "playwright@official" {
		t.Fatalf("forced uninstall calls=%v bodies=%#v", rec.calls, rec.jsonBodies)
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
				t.Fatalf("failure mutated: puts=%d calls=%v", put, rec.calls)
			}
		})
	}
}

type agentPluginRecorder struct {
	calls      []string
	jsonBodies map[string]map[string]string
}

func (r *agentPluginRecorder) sawCall(call string) bool {
	for _, got := range r.calls {
		if got == call {
			return true
		}
	}
	return false
}

func agentPluginCommandServer(t *testing.T, state, agentsHTML, agentJSON string, onPut func(*http.Request)) (*httptest.Server, *agentPluginRecorder) {
	t.Helper()
	rec := &agentPluginRecorder{jsonBodies: map[string]map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.calls = append(rec.calls, r.Method+" "+r.URL.EscapedPath())
		if r.Method == http.MethodPost || r.Method == http.MethodDelete || r.Method == http.MethodPut {
			var body map[string]string
			if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body != nil {
					rec.jsonBodies[r.Method+" "+r.URL.EscapedPath()] = body
				}
			}
		}
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
