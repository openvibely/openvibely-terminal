package tui

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-tui/internal/client"
)

const agentEditListHTML = `<div data-agent-id="ag-1" data-agent-key="reviewer" data-agent-name="Code Reviewer" data-agent-description="old" data-agent-model="inherit" data-agent-scope="project"></div>`
const agentEditJSON = `{"id":"ag-1","name":"Code Reviewer","description":"old","system_prompt":"prompt","model":"inherit","tools":[],"tool_config":{},"plugins":[],"mcp_servers":[],"skills":[],"key":"reviewer","scope":"project","project_id":"p1","selectable_as_primary":true,"enabled":true,"permission_defaults":{},"generated_status":"user_edited","source_refs":[]}`

func TestAgentsEditQuotedValuesAndRoundTrip(t *testing.T) {
	var puts, lists int
	var formDescription, formPrompt, formEnabled string
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("project_id") != "p1" {
			http.Error(w, "wrong scope", http.StatusNotFound)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/agents":
			lists++
			_, _ = io.WriteString(w, agentEditListHTML)
		case r.Method == http.MethodGet && r.URL.Path == "/agents/ag-1/json":
			_, _ = io.WriteString(w, agentEditJSON)
		case r.Method == http.MethodGet && r.URL.Path == "/agents/ag-1/lifecycle-hooks":
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodPut && r.URL.Path == "/agents/ag-1":
			puts++
			_ = r.ParseForm()
			formDescription = r.PostForm.Get("description")
			formPrompt = r.PostForm.Get("system_prompt")
			formEnabled = r.PostForm.Get("enabled")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	m = runLine(t, m, `/agents edit "Code Reviewer" description "reviews Go and SQL" system-prompt "" enabled false`)
	if puts != 1 || lists != 2 {
		t.Fatalf("puts=%d lists=%d output=%s", puts, lists, transcript(m))
	}
	if formDescription != "reviews Go and SQL" || formPrompt != "" || formEnabled != "false" {
		t.Fatalf("form description=%q prompt=%q enabled=%q", formDescription, formPrompt, formEnabled)
	}
	if out := stripANSI(transcript(m)); !strings.Contains(out, "updated agent Code Reviewer") {
		t.Fatalf("output: %s", out)
	}
}

func TestAgentsEditFailuresNeverMutate(t *testing.T) {
	for _, tc := range []struct {
		name, line, detail string
		detailStatus       int
		models             string
		want               string
	}{
		{"syntax", `/agents edit reviewer enabled maybe`, agentEditJSON, http.StatusOK, "", "must be true or false"},
		{"invalid model", `/agents edit reviewer model typo-model`, agentEditJSON, http.StatusOK, `<div data-model-id="cfg-1" data-model-name="Configured" data-model-model="valid-model"></div>`, "unknown agent model"},
		{"unknown reference", `/agents edit missing enabled false`, agentEditJSON, http.StatusOK, "", "nothing matches"},
		{"protected", `/agents edit reviewer enabled false`, strings.Replace(agentEditJSON, `"user_edited"`, `"protected"`, 1), http.StatusOK, "", "protected system agent"},
		{"detail", `/agents edit reviewer enabled false`, `{"error":"detail failed"}`, http.StatusInternalServerError, "", "detail failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			puts, details := 0, 0
			m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/agents":
					_, _ = io.WriteString(w, agentEditListHTML)
				case r.Method == http.MethodGet && r.URL.Path == "/models":
					_, _ = io.WriteString(w, tc.models)
				case r.Method == http.MethodGet && r.URL.Path == "/agents/ag-1/json":
					details++
					w.WriteHeader(tc.detailStatus)
					_, _ = io.WriteString(w, tc.detail)
				case r.Method == http.MethodPut:
					puts++
					w.WriteHeader(http.StatusNoContent)
				default:
					_, _ = io.WriteString(w, `[]`)
				}
			})
			m = runLine(t, m, tc.line)
			if puts != 0 {
				t.Fatalf("PUTs=%d", puts)
			}
			if tc.name == "invalid model" && details != 0 {
				t.Fatalf("invalid model made %d detail requests", details)
			}
			if out := stripANSI(transcript(m)); !strings.Contains(out, tc.want) {
				t.Fatalf("want %q in %s", tc.want, out)
			}
		})
	}
}

func TestMatchAgentRefSemantics(t *testing.T) {
	agents := []client.AgentDef{
		{ID: "ag-code", Name: "Code Reviewer", Key: "reviewer"},
		{ID: "ag-plus", Name: "Code Reviewer Plus", Key: "reviewer-plus"},
		{ID: "ag-docs", Name: "Docs Writer", Key: "writer"},
	}
	for _, tc := range []struct {
		name, ref, wantID, wantErr string
	}{
		{"canonical ID", "ag-docs", "ag-docs", ""},
		{"exact name precedence", "Code Reviewer", "ag-code", ""},
		{"exact key", "reviewer", "ag-code", ""},
		{"unique prefix", "Docs", "ag-docs", ""},
		{"unique substring", "Writer", "ag-docs", ""},
		{"ambiguous prefix", "Code", "", "ambiguous"},
		{"ambiguous key prefix", "reviewer-", "ag-plus", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := matchAgentRef(agents, tc.ref)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got.ID != tc.wantID {
				t.Fatalf("got %#v, err %v; want %s", got, err, tc.wantID)
			}
		})
	}
}

func TestAgentsEditHeadlessJSON(t *testing.T) {
	c, rec := cliServer(t, map[string]string{
		"/api/projects":                cliProjects,
		"/agents":                      agentEditListHTML,
		"/agents/ag-1/json":            agentEditJSON,
		"/agents/ag-1/lifecycle-hooks": `[]`,
		"/agents/ag-1":                 agentEditListHTML,
	})
	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"agents", "edit", "reviewer", "enabled", "false"}, false, true); err != nil {
		t.Fatal(err)
	}
	if !rec.saw("PUT", "/agents/ag-1") || !rec.sawQuery("project_id=p1") {
		t.Fatalf("requests: %s", rec.all())
	}
	text := out.String()
	if rec.count("GET", "/agents/ag-1/json") != 2 {
		t.Fatalf("authoritative detail requests: %s", rec.all())
	}
	// The refresh fixture remains enabled, proving JSON uses persisted backend
	// state rather than the locally attempted enabled=false definition.
	if !strings.Contains(text, `"enabled":true`) || !strings.Contains(text, `"system_prompt":"prompt"`) || strings.Contains(text, "updated agent") {
		t.Fatalf("JSON output: %s", text)
	}
}

func TestAgentsEditHeadlessJSONUsesBackendNormalizedDefinition(t *testing.T) {
	detailReads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/projects":
			_, _ = io.WriteString(w, cliProjects)
		case r.URL.Path == "/agents":
			_, _ = io.WriteString(w, agentEditListHTML)
		case r.URL.Path == "/agents/ag-1/json":
			detailReads++
			if detailReads == 1 {
				_, _ = io.WriteString(w, strings.Replace(agentEditJSON, `"generated_status":"user_edited"`, `"generated_status":"generated"`, 1))
				return
			}
			_, _ = io.WriteString(w, strings.Replace(agentEditJSON, `"source_refs":[]`, `"source_refs":[],"updated_at":"2026-09-08T12:34:56Z"`, 1))
		case r.URL.Path == "/agents/ag-1/lifecycle-hooks":
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodPut && r.URL.Path == "/agents/ag-1":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"agents", "edit", "reviewer", "model", "INHERIT"}, false, true); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{`"model":"inherit"`, `"generated_status":"user_edited"`, `"updated_at":"2026-09-08T12:34:56Z"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("authoritative JSON missing %s: %s", want, text)
		}
	}
	if strings.Contains(text, `"generated_status":"generated"`) || detailReads != 2 {
		t.Fatalf("JSON used stale definition after %d detail reads: %s", detailReads, text)
	}
}

func TestAgentsEditHeadlessJSONRefreshFailureIsPartialSuccess(t *testing.T) {
	detailReads, puts := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/projects":
			_, _ = io.WriteString(w, cliProjects)
		case r.URL.Path == "/agents":
			_, _ = io.WriteString(w, agentEditListHTML)
		case r.URL.Path == "/agents/ag-1/json":
			detailReads++
			if detailReads > 1 {
				http.Error(w, "authoritative refresh failed", http.StatusInternalServerError)
				return
			}
			_, _ = io.WriteString(w, agentEditJSON)
		case r.URL.Path == "/agents/ag-1/lifecycle-hooks":
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodPut && r.URL.Path == "/agents/ag-1":
			puts++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"agents", "edit", "reviewer", "enabled", "false"}, false, true); err != nil {
		t.Fatalf("saved edit must remain successful: %v", err)
	}
	text := out.String()
	if puts != 1 || !strings.Contains(text, `"saved":true`) || !strings.Contains(text, `"refresh_error"`) || !strings.Contains(text, "authoritative refresh failed") {
		t.Fatalf("puts=%d JSON partial output: %s", puts, text)
	}
	if strings.Contains(text, `"agent"`) || strings.Contains(text, `"enabled":false`) {
		t.Fatalf("partial output must not claim unverified persisted state: %s", text)
	}
}

func TestAgentsEditHelpCompletionAndStaleness(t *testing.T) {
	cmd := lookupCommand("agents")
	if cmd == nil {
		t.Fatal("agents command missing")
	}
	help := renderCommandHelp(*cmd)
	for _, want := range []string{"agents edit <agent> <field> <value> [...]", `agents edit reviewer description "Reviews Go changes" enabled true`} {
		if !strings.Contains(help, want) {
			t.Errorf("help missing %q:\n%s", want, help)
		}
	}
	if got := completeSlashInput(`/agents ed`, *cmd); got != `/agents edit ` {
		t.Errorf("action completion = %q", got)
	}
	if got := completeSlashInput(`/agents edit reviewer en`, *cmd); got != `/agents edit reviewer enabled ` {
		t.Errorf("field completion = %q", got)
	}

	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agents":
			_, _ = io.WriteString(w, agentEditListHTML)
		case "/agents/ag-1/json":
			_, _ = io.WriteString(w, agentEditJSON)
		case "/agents/ag-1/lifecycle-hooks":
			_, _ = io.WriteString(w, `[]`)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	m, pending := typeLine(t, m, `/agents edit reviewer enabled false`)
	before := len(m.log)
	m.setActiveProject(client.Project{ID: "p2", Name: "other"})
	msg := pending()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, sub := range batch {
			if sub != nil {
				msg = sub()
				break
			}
		}
	}
	next, _ := m.Update(msg)
	m = next.(Model)
	if len(m.log) != before || m.selectedID != "p2" {
		t.Fatalf("stale edit result changed model: selected=%q output=%s", m.selectedID, transcript(m))
	}
}

func TestAgentsEditPlainOutputIsTerminalSafe(t *testing.T) {
	unsafeName := "Name\x1b[31m Red\x1b[0m\a\nBreak"
	unsafeScope := "project\x1b[2J\a\nScope"
	unsafeModel := "model\x1b]0;owned\a\nModel"
	unsafeDescription := "description\x1b[1m\a\nDescription"
	listReads := 0
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/agents":
			listReads++
			if listReads == 1 {
				_, _ = io.WriteString(w, agentEditListHTML)
				return
			}
			_, _ = io.WriteString(w, `<div data-agent-id="ag-1" data-agent-key="reviewer" data-agent-name="`+unsafeName+`" data-agent-description="`+unsafeDescription+`" data-agent-model="`+unsafeModel+`" data-agent-scope="`+unsafeScope+`"></div>`)
		case r.URL.Path == "/agents/ag-1/json":
			_, _ = io.WriteString(w, strings.Replace(agentEditJSON, `"Code Reviewer"`, `"Name\\u001b[31m Red\\u001b[0m\\u0007\\nBreak"`, 1))
		case r.URL.Path == "/agents/ag-1/lifecycle-hooks":
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodPut:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	_, cmd := typeLine(t, m, `/agents edit reviewer enabled false`)
	rawMsg := cmd()
	msg, ok := rawMsg.(resultMsg)
	if !ok {
		t.Fatalf("message type = %T", rawMsg)
	}
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	raw := msg.body
	for _, forbidden := range []string{"\x1b[31m", "\x1b[2J", "\x1b]0;owned", "\x1b[1m", "\a", "Name\nBreak", "project\nScope", "model\nModel", "description\nDescription"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("plain output retained %q: %q", forbidden, raw)
		}
	}
	for _, readable := range []string{"updated agent Name Red Break", "Name Red Break", "project Scope", "model Model", "description Description"} {
		if !strings.Contains(stripANSI(raw), readable) {
			t.Fatalf("plain output missing %q: %q", readable, raw)
		}
	}
}

func TestRenderAgentsSanitizesEveryField(t *testing.T) {
	raw := renderAgents([]client.AgentDef{{
		Name: "name\x1b[31m\a\nrow", Scope: "scope\x1b[2J\a\nrow",
		Model: "model\x1b]0;title\a\nrow", Description: "description\x1b[1m\a\nrow",
	}}, "")
	for _, forbidden := range []string{"\x1b[31m", "\x1b[2J", "\x1b]0;title", "\x1b[1m", "\a", "name\nrow", "scope\nrow", "model\nrow", "description\nrow"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("render retained %q: %q", forbidden, raw)
		}
	}
}

func TestAgentsEditRefreshFailureIsPartialSuccess(t *testing.T) {
	lists, puts := 0, 0
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/agents":
			lists++
			if lists > 1 {
				http.Error(w, "refresh failed", 500)
			} else {
				_, _ = io.WriteString(w, agentEditListHTML)
			}
		case r.URL.Path == "/agents/ag-1/json":
			_, _ = io.WriteString(w, agentEditJSON)
		case r.URL.Path == "/agents/ag-1/lifecycle-hooks":
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodPut:
			puts++
			w.WriteHeader(http.StatusNoContent)
		}
	})
	m = runLine(t, m, `/agents edit reviewer description updated`)
	out := stripANSI(transcript(m))
	if puts != 1 || !strings.Contains(out, "saved; refresh failed") || strings.Contains(out, "error:") {
		t.Fatalf("puts=%d output=%s", puts, out)
	}
}
