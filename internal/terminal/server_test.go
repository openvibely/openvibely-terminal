package terminal

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

type switchBackend struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls []string
}

func (b *switchBackend) record(r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, r.Method+" "+r.URL.RequestURI())
}

func (b *switchBackend) snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.calls...)
}

func (b *switchBackend) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = nil
}

func (b *switchBackend) countContaining(part string) int {
	n := 0
	for _, call := range b.snapshot() {
		if strings.Contains(call, part) {
			n++
		}
	}
	return n
}

func newSwitchBackend(t *testing.T, projectsJSON, taskTitle string, requireAuth bool) *switchBackend {
	t.Helper()
	b := &switchBackend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.record(r)
		if requireAuth {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/capacity/global":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case "/auth/me":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"authenticated":true}`))
		case "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(projectsJSON))
		case "/api/tasks/reference-catalog":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"tasks": []map[string]string{{
				"id": "t-" + taskTitle, "project_id": r.URL.Query().Get("project_id"),
				"title": taskTitle, "category": "backlog", "status": "pending",
			}}})
		case "/tasks":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div class="task-card" data-task-id="t-` + taskTitle + `"><span class="task-title">` + taskTitle + `</span></div>`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func connectedSwitchModel(t *testing.T, b *switchBackend, projects []client.Project) Model {
	t.Helper()
	c, err := client.New(b.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(Model)
	next, _ = m.Update(projectsLoadedMsg{projects: projects})
	m = next.(Model)
	next, _ = m.Update(connCheckedMsg{generation: m.connectionGeneration, auth: &client.AuthStatus{Authenticated: true}})
	m = next.(Model)
	if !m.connected || m.selectedID == "" {
		t.Fatalf("setup: connected=%v selected=%q", m.connected, m.selectedID)
	}
	t.Cleanup(func() { m.Cleanup() })
	return m
}

func switchServer(t *testing.T, m Model, line string) Model {
	t.Helper()
	var cmd tea.Cmd
	if m.pendingConfirmation != nil || m.selectorActive {
		next, c := m.runCommand(line)
		m, cmd = next.(Model), c
	} else {
		m, cmd = typeLine(t, m, line)
	}
	if cmd == nil {
		return m
	}
	msg := cmd()
	next, _ := m.Update(msg)
	m = next.(Model)
	t.Cleanup(func() { m.Cleanup() })
	return m
}

func TestServerCommandShowsCurrentServer(t *testing.T) {
	local := newSwitchBackend(t, `{"projects":[{"id":"p1","name":"local"}]}`, "x", false)
	m := connectedSwitchModel(t, local, []client.Project{{ID: "p1", Name: "local"}})
	m = runLine(t, m, "/server")
	out := transcript(m)
	if !strings.Contains(out, local.srv.URL) || !strings.Contains(out, "connected") {
		t.Fatalf("server output missing URL/state:\n%s", out)
	}
	if lookupCommand("connect") == nil || lookupCommand("connect").name != "server" {
		t.Fatal("connect alias not registered for /server")
	}
}

func TestServerSwitchLocalToRemoteRoutesProjectCommandsToNewBackend(t *testing.T) {
	local := newSwitchBackend(t, `{"projects":[{"id":"p-local","name":"local"}]}`, "localtask", false)
	remote := newSwitchBackend(t, `{"projects":[{"id":"p-remote","name":"remote"}]}`, "remotetask", false)
	m := connectedSwitchModel(t, local, []client.Project{{ID: "p-local", Name: "local"}})

	m = switchServer(t, m, "/server "+remote.srv.URL)
	if m.client.BaseURL() != remote.srv.URL {
		t.Fatalf("client = %s, want %s", m.client.BaseURL(), remote.srv.URL)
	}
	if m.selectedID != "p-remote" || !m.connected || !m.projectsLoaded {
		t.Fatalf("after switch selected=%q connected=%v loaded=%v", m.selectedID, m.connected, m.projectsLoaded)
	}
	if !strings.Contains(transcript(m), "Switched from") {
		t.Fatalf("missing switch confirmation:\n%s", transcript(m))
	}

	local.reset()
	remote.reset()
	m = runLine(t, m, "/tasks")
	if !strings.Contains(transcript(m), "remotetask") || strings.Contains(transcript(m), "localtask") {
		t.Fatalf("task command did not show only the new backend's task:\n%s", transcript(m))
	}
	if got := remote.countContaining("/api/tasks/reference-catalog?project_id=p-remote"); got == 0 {
		t.Fatalf("remote /tasks calls = %d; calls=%v", got, remote.snapshot())
	}
	if calls := local.snapshot(); len(calls) != 0 {
		t.Fatalf("old backend received requests after switch: %v", calls)
	}
}

func TestServerSwitchRemoteToLocalRefreshesProjectsAndTaskContext(t *testing.T) {
	remote := newSwitchBackend(t, `{"projects":[{"id":"r1","name":"remote"}]}`, "remotetask", false)
	local := newSwitchBackend(t, `{"projects":[{"id":"l1","name":"alpha"},{"id":"l2","name":"beta"}]}`, "localtask", false)
	m := connectedSwitchModel(t, remote, []client.Project{{ID: "r1", Name: "remote"}})
	m.threadID, m.threadTitle = "task-r", "remote task"
	m.activeTaskCount = 7

	m = switchServer(t, m, "/connect "+local.srv.URL)
	if m.client.BaseURL() != local.srv.URL {
		t.Fatalf("client = %s", m.client.BaseURL())
	}
	if len(m.projects) != 2 || m.selectedID != "l1" || m.selectedName != "alpha" {
		t.Fatalf("projects=%v selected=%q/%q", m.projects, m.selectedID, m.selectedName)
	}
	if m.threadID != "" || m.activeTaskCount != 0 {
		t.Fatalf("task context not reset: thread=%q active=%d", m.threadID, m.activeTaskCount)
	}
	if strings.Contains(transcript(m), "remote task") {
		t.Fatalf("old transcript retained:\n%s", transcript(m))
	}
}

func TestServerSwitchMalformedURLKeepsCurrentSession(t *testing.T) {
	local := newSwitchBackend(t, `{"projects":[{"id":"p1","name":"local"}]}`, "localtask", false)
	m := connectedSwitchModel(t, local, []client.Project{{ID: "p1", Name: "local"}})
	before := m.sessionGeneration

	for _, bad := range []string{
		"ftp://example.com",
		"https://user:supersecret@example.com",
		"https://example.com/?token=supersecret",
		"http://:99999",
	} {
		m = switchServer(t, m, "/server "+bad)
		if m.client.BaseURL() != local.srv.URL || m.selectedID != "p1" || !m.connected {
			t.Fatalf("%s replaced session: client=%s selected=%q", bad, m.client.BaseURL(), m.selectedID)
		}
		if m.sessionGeneration != before {
			t.Fatalf("%s advanced session generation", bad)
		}
	}
	out := transcript(m) + strings.Join(m.history, "\n")
	if strings.Contains(out, "supersecret") {
		t.Fatalf("secret echoed:\n%s", out)
	}
	if !strings.Contains(out, "invalid server URL") || !strings.Contains(out, "Still using") {
		t.Fatalf("missing guidance:\n%s", out)
	}

	local.reset()
	m = runLine(t, m, "/tasks")
	if local.countContaining("/api/tasks/reference-catalog?project_id=p1") == 0 {
		t.Fatalf("session unusable after malformed URL: %v", local.snapshot())
	}
}

func TestServerSwitchUnreachableKeepsCurrentSession(t *testing.T) {
	local := newSwitchBackend(t, `{"projects":[{"id":"p1","name":"local"}]}`, "localtask", false)
	m := connectedSwitchModel(t, local, []client.Project{{ID: "p1", Name: "local"}})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "http://" + ln.Addr().String()
	ln.Close()

	m = switchServer(t, m, "/server "+dead)
	if m.client.BaseURL() != local.srv.URL || m.selectedID != "p1" || !m.connected || m.busy {
		t.Fatalf("unreachable URL replaced session: client=%s selected=%q busy=%v", m.client.BaseURL(), m.selectedID, m.busy)
	}
	if out := transcript(m); !strings.Contains(out, "could not reach") || !strings.Contains(out, "Still using") {
		t.Fatalf("missing unreachable guidance:\n%s", out)
	}
	local.reset()
	m = runLine(t, m, "/tasks")
	if local.countContaining("/api/tasks/reference-catalog?project_id=p1") == 0 {
		t.Fatalf("session unusable after unreachable URL: %v", local.snapshot())
	}
}

func TestServerSwitchAuthenticatedBackendShowsSignInGuidance(t *testing.T) {
	local := newSwitchBackend(t, `{"projects":[{"id":"p1","name":"local"}]}`, "localtask", false)
	protected := newSwitchBackend(t, "", "", true)
	m := connectedSwitchModel(t, local, []client.Project{{ID: "p1", Name: "local"}})

	m = switchServer(t, m, "/server "+protected.srv.URL)
	if m.client.BaseURL() != local.srv.URL || m.selectedID != "p1" || m.authRequired {
		t.Fatalf("protected server replaced session without --login: client=%s", m.client.BaseURL())
	}
	out := transcript(m)
	if !strings.Contains(out, "requires sign-in") || !strings.Contains(out, "--login") {
		t.Fatalf("missing sign-in guidance:\n%s", out)
	}

	m = switchServer(t, m, "/server "+protected.srv.URL+" --login")
	if m.client.BaseURL() != protected.srv.URL || !m.authRequired || !m.loginActive {
		t.Fatalf("--login did not switch into sign-in: client=%s auth=%v login=%v", m.client.BaseURL(), m.authRequired, m.loginActive)
	}
	if m.selectedID != "" || len(m.projects) != 0 {
		t.Fatalf("old project state kept: %q %v", m.selectedID, m.projects)
	}

	m = typeInput(t, m, "alice")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	m = typeInput(t, m, "hunter2-secret")
	if strings.Contains(transcript(m)+strings.Join(m.history, "\n"), "hunter2-secret") {
		t.Fatal("password exposed in transcript or history")
	}
}

func TestServerSwitchSameProjectIDDoesNotCarryState(t *testing.T) {
	local := newSwitchBackend(t, `{"projects":[{"id":"shared","name":"local"}]}`, "localtask", false)
	remote := newSwitchBackend(t, `{"projects":[{"id":"shared","name":"remote"}]}`, "remotetask", false)
	m := connectedSwitchModel(t, local, []client.Project{{ID: "shared", Name: "local"}})
	m.append(entry{role: "agent", text: "old local transcript"})
	m.threadID, m.threadTitle = "local-thread", "local thread"
	m.pendingMsgID = "msg-local"
	m.pendingMsgProjectID = "shared"
	m.busy = true
	m.selectorActive = true
	m.selectorItems = []selectorItem{{}}
	m.pendingConfirmation = &pendingCmd{message: "confirm local"}
	m.showEvents = true
	m.connectSSE()
	oldSSE := m.sseGeneration
	oldSession := m.sessionGeneration
	oldProject := m.projectGeneration

	m = switchServer(t, m, "/server "+remote.srv.URL)
	if m.selectedID != "shared" || m.selectedName != "remote" {
		t.Fatalf("selected=%q/%q", m.selectedID, m.selectedName)
	}
	if strings.Contains(transcript(m), "old local transcript") {
		t.Fatalf("transcript carried across servers:\n%s", transcript(m))
	}
	if m.threadID != "" || m.pendingMsgID != "" || m.selectorActive || m.pendingConfirmation != nil {
		t.Fatalf("project state carried: thread=%q pending=%q selector=%v confirm=%v", m.threadID, m.pendingMsgID, m.selectorActive, m.pendingConfirmation != nil)
	}
	if m.sseGeneration == oldSSE || m.sessionGeneration == oldSession || m.projectGeneration == oldProject {
		t.Fatal("stream/session/project generations not advanced")
	}

	stale := resultMsg{title: "Tasks", body: "stale local result", sessionGeneration: oldSession, projectGeneration: oldProject}
	next, _ := m.Update(stale)
	m = next.(Model)
	staleSSE := sseEventMsg{generation: oldSSE, event: client.Event{Name: "task_status_changed", Data: []byte(`{"project_id":"shared"}`)}}
	next, _ = m.Update(staleSSE)
	m = next.(Model)
	if strings.Contains(transcript(m), "stale local result") {
		t.Fatal("stale result from old backend applied")
	}
	if !m.showEvents {
		t.Fatal("events display preference should persist")
	}
}

func TestServerSwitchIgnoresSupersededProbe(t *testing.T) {
	local := newSwitchBackend(t, `{"projects":[{"id":"p1","name":"local"}]}`, "x", false)
	remote := newSwitchBackend(t, `{"projects":[{"id":"r1","name":"remote"}]}`, "y", false)
	m := connectedSwitchModel(t, local, []client.Project{{ID: "p1", Name: "local"}})
	m, first := typeLine(t, m, "/server "+remote.srv.URL)
	m, _ = typeLine(t, m, "/server "+remote.srv.URL+"/other")
	next, _ := m.Update(first())
	m = next.(Model)
	if m.client.BaseURL() != local.srv.URL {
		t.Fatalf("superseded probe installed %s", m.client.BaseURL())
	}
}

func TestServerSwitchRejectedInCLI(t *testing.T) {
	c, err := client.New("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	err = RunCLI(c, &out, "", []string{"server", "http://example.com"}, false, false)
	if err == nil || !strings.Contains(err.Error(), "OPENVIBELY_SERVER_URL") {
		t.Fatalf("CLI server switch err = %v", err)
	}
	out.Reset()
	if err := RunCLI(c, &out, "", []string{"server"}, false, false); err != nil {
		t.Fatalf("CLI server show: %v", err)
	}
	if !strings.Contains(out.String(), "http://127.0.0.1:1") {
		t.Fatalf("CLI server show output:\n%s", out.String())
	}
}

func TestRedactServerCommandSecrets(t *testing.T) {
	got := redactServerCommandSecrets("/server https://u:pw-secret@host.example/x?token=t-secret#f")
	if strings.Contains(got, "secret") {
		t.Fatalf("redaction leaked: %q", got)
	}
	if got := redactServerCommandSecrets("/tasks a@b"); got != "/tasks a@b" {
		t.Fatalf("non-server command changed: %q", got)
	}
}

func TestServerQuotedSecretsNeverReachTranscriptOrHistory(t *testing.T) {
	for _, root := range []string{`"/server"`, `'/connect'`, `/"server"`, `/con'nect'`, `"/ser"ver`} {
		for _, operand := range []string{
			`https://user:userinfo-secret@example.com/x?token=query-secret#fragment-secret`,
			`"https://user:userinfo-secret@example.com/x?token=query-secret#fragment-secret"`,
			`'https://example.com/?token=query-secret#fragment-secret'`,
			`"https://user:userinfo-secret@example.com/?token=query-secret#fragment-secret`,
		} {
			t.Run(root+operand, func(t *testing.T) {
				m := newTestModel(t)
				original := m.client
				m = runLine(t, m, root+" "+operand)
				output := transcript(m) + strings.Join(m.history, "\n") + m.View()
				for _, secret := range []string{"userinfo-secret", "query-secret", "fragment-secret"} {
					if strings.Contains(output, secret) {
						t.Fatalf("secret reached displayed/stored output: %s", output)
					}
				}
				if m.client != original {
					t.Fatal("invalid URL replaced the current client")
				}
			})
		}
	}
}

func TestDelayedServerSwitchClearsCredentialWizards(t *testing.T) {
	for _, protected := range []bool{false, true} {
		for _, wizard := range []string{"channel", "model", "model-edit"} {
			t.Run(fmt.Sprintf("%s/protected=%v", wizard, protected), func(t *testing.T) {
				local := newSwitchBackend(t, `{"projects":[{"id":"shared","name":"local"}]}`, "local", false)
				remote := newSwitchBackend(t, `{"projects":[{"id":"shared","name":"remote"}]}`, "remote", protected)
				m := connectedSwitchModel(t, local, []client.Project{{ID: "shared", Name: "local"}})
				prompt := m.input.Prompt
				line := "/server " + remote.srv.URL
				if protected {
					line += " --login"
				}
				m, probe := typeLine(t, m, line)
				if probe == nil {
					t.Fatal("missing pending probe")
				}
				// Open a wizard while the probe response is outstanding.
				switch wizard {
				case "channel":
					m, _ = m.beginChannelWizard("add", client.Channel{Type: "telegram", Name: "old channel"})
					m.channelWizard.form.Set("token", "saved-credential-secret")
				case "model":
					m, _ = m.beginModelWizard()
					m.modelWizard.spec.APIKey = "saved-credential-secret"
				case "model-edit":
					m, _ = m.beginModelEditAPIKeyPrompt(modelEditSpec{})
				}
				m.input.SetValue("unsubmitted-credential-secret")
				next, _ := m.Update(probe())
				m = next.(Model)
				defer m.Cleanup()
				if m.client.BaseURL() != remote.srv.URL || m.channelWizard != nil || m.modelWizard != nil || m.modelEditWizard != nil {
					t.Fatal("server installation retained an old credential wizard")
				}
				if m.input.Value() != "" || m.input.EchoMode != textinput.EchoNormal {
					t.Fatal("server installation retained credential input or masking")
				}
				if !protected && m.input.Prompt != prompt {
					t.Fatalf("prompt was not restored: %q", m.input.Prompt)
				}
				if protected && !m.loginActive {
					t.Fatal("new backend login was not opened")
				}
				remote.reset()
				local.reset()
				next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
				m = next.(Model)
				if cmd != nil {
					m = drain(m, cmd)
				}
				if len(remote.snapshot()) != 0 || len(local.snapshot()) != 0 {
					t.Fatal("Enter submitted old wizard data after switching")
				}
				if strings.Contains(transcript(m)+strings.Join(m.history, "\n")+m.View(), "credential-secret") {
					t.Fatal("old credential leaked into new session")
				}
			})
		}
	}
}
