package tui

import (
	"bytes"
	"io"
	"net/http"
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
		want               string
	}{
		{"syntax", `/agents edit reviewer enabled maybe`, agentEditJSON, http.StatusOK, "must be true or false"},
		{"unknown reference", `/agents edit missing enabled false`, agentEditJSON, http.StatusOK, "nothing matches"},
		{"protected", `/agents edit reviewer enabled false`, strings.Replace(agentEditJSON, `"user_edited"`, `"protected"`, 1), http.StatusOK, "protected system agent"},
		{"detail", `/agents edit reviewer enabled false`, `{"error":"detail failed"}`, http.StatusInternalServerError, "detail failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			puts := 0
			m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/agents":
					_, _ = io.WriteString(w, agentEditListHTML)
				case r.Method == http.MethodGet && r.URL.Path == "/agents/ag-1/json":
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
			if out := stripANSI(transcript(m)); !strings.Contains(out, tc.want) {
				t.Fatalf("want %q in %s", tc.want, out)
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
	if !strings.Contains(text, `"enabled":false`) || !strings.Contains(text, `"system_prompt":"prompt"`) || strings.Contains(text, "updated agent") {
		t.Fatalf("JSON output: %s", text)
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
