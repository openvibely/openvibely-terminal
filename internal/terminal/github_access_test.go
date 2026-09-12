package terminal

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestGitHubChannelAccessTUIAddListAndCapturedRemoval(t *testing.T) {
	listPath := "/channels/github/runtime-settings"
	removePath := "/channels/github/authorized-actors/actor-1"
	page := channelAccessTestPage("github", channelAccessTestRow{id: "actor-1", name: "Alice Reviewer", identity: "Alice"})

	m, rec := dispatchModel(t, map[string]string{listPath: page})
	m = runLine(t, m, "/channels access github list")
	output := transcript(m)
	if !strings.Contains(output, "@alice") || !strings.Contains(output, "Alice Reviewer") || strings.Contains(output, "channel-access-backend-secret") {
		t.Fatalf("unsafe GitHub list output: %s", output)
	}
	if !rec.saw(http.MethodGet, listPath) || !rec.sawQuery("project_id=p1") {
		t.Fatalf("GitHub list was not scoped: %s", rec.all())
	}

	m, rec = dispatchModel(t, map[string]string{listPath: channelAccessTestPage("github")})
	m = runLine(t, m, `/channels access github add @Alice "Release Reviewer"`)
	if !rec.saw(http.MethodPost, "/channels/github/authorized-actors") || !rec.sawQuery("project_id=p1") || !rec.sawForm("github_login=alice") || !rec.sawForm("display_name=Release+Reviewer") {
		t.Fatalf("GitHub add was not normalized/scoped: calls=%s forms=%v", rec.all(), rec.forms)
	}
	if strings.Contains(transcript(m), "channel-access-backend-secret") || !strings.Contains(transcript(m), "authorized GitHub access") {
		t.Fatalf("unsafe GitHub add output: %s", transcript(m))
	}

	m, rec = dispatchModel(t, map[string]string{listPath: page})
	m = runLine(t, m, "/channels access github remove @Alice")
	if m.pendingConfirmation == nil || rec.saw(http.MethodDelete, removePath) {
		t.Fatalf("GitHub removal did not resolve before confirmation: pending=%v calls=%s", m.pendingConfirmation != nil, rec.all())
	}
	if !strings.Contains(m.pendingConfirmation.message, "@alice") {
		t.Fatalf("GitHub confirmation did not show canonical login: %q", m.pendingConfirmation.message)
	}
	next, _ := m.Update(teaKeyEsc())
	m = next.(Model)
	if rec.saw(http.MethodDelete, removePath) || m.pendingConfirmation != nil {
		t.Fatalf("canceled GitHub removal mutated or stayed pending: calls=%s", rec.all())
	}
}

func TestGitHubChannelAccessCLIValidationDuplicateForceAndSafeJSON(t *testing.T) {
	listPath := "/channels/github/runtime-settings"
	removePath := "/channels/github/authorized-actors/actor-1"
	page := channelAccessTestPage("github", channelAccessTestRow{id: "actor-1", name: "Alice Reviewer", identity: "Alice"})

	t.Run("list and duplicate add", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects, listPath: page})
		var out bytes.Buffer
		if err := RunCLI(c, &out, "demo", []string{"channels", "access", "github", "list"}, false, true); err != nil {
			t.Fatalf("GitHub list: %v", err)
		}
		var users []channelAccessOutputUser
		if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &users); err != nil {
			t.Fatalf("GitHub list JSON = %q: %v", out.String(), err)
		}
		if len(users) != 1 || users[0].Provider != "github" || users[0].Identity != "alice" || users[0].ProjectID != "p1" {
			t.Fatalf("GitHub list JSON = %#v", users)
		}
		for _, forbidden := range []string{"github-secret", "permission", "admin", "github_pat"} {
			if strings.Contains(out.String(), forbidden) {
				t.Fatalf("GitHub JSON leaked %q: %s", forbidden, out.String())
			}
		}

		err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"channels", "access", "github", "add", "@Alice"}, false, false)
		if err == nil || !strings.Contains(err.Error(), "already exists") || rec.saw(http.MethodPost, "/channels/github/authorized-actors") {
			t.Fatalf("duplicate GitHub add was not stopped before mutation: err=%v calls=%s", err, rec.all())
		}
	})

	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "missing add login", args: []string{"channels", "access", "github", "add"}},
		{name: "malformed login", args: []string{"channels", "access", "github", "add", "@bad!"}},
		{name: "empty display name", args: []string{"channels", "access", "github", "add", "alice", " "}},
		{name: "surplus add operands", args: []string{"channels", "access", "github", "add", "alice", "Display", "extra"}},
		{name: "surplus remove operands", args: []string{"channels", "access", "github", "remove", "alice", "extra"}},
		{name: "surplus list operands", args: []string{"channels", "access", "github", "list", "extra"}},
		{name: "unknown action", args: []string{"channels", "access", "github", "show"}},
	} {
		t.Run("local validation "+tc.name, func(t *testing.T) {
			c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects})
			err := RunCLI(c, &bytes.Buffer{}, "demo", tc.args, false, false)
			if err == nil {
				t.Fatal("invalid GitHub access command succeeded")
			}
			if rec.all() != "" {
				t.Fatalf("local validation made backend requests: %s", rec.all())
			}
		})
	}

	t.Run("force gate captures row ID", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects, listPath: page})
		err := RunCLI(c, &bytes.Buffer{}, "demo", []string{"channels", "access", "github", "remove", "@Alice"}, false, false)
		if err == nil || !strings.Contains(err.Error(), "--force") || rec.saw(http.MethodDelete, removePath) {
			t.Fatalf("unforced GitHub removal was not gated: err=%v calls=%s", err, rec.all())
		}

		c, rec = cliServer(t, map[string]string{"/api/projects": cliProjects, listPath: page})
		var out bytes.Buffer
		if err := RunCLI(c, &out, "demo", []string{"channels", "access", "github", "remove", "@Alice"}, true, true); err != nil {
			t.Fatalf("forced GitHub removal: %v", err)
		}
		if !rec.saw(http.MethodDelete, removePath) || !rec.sawQuery("project_id=p1") || rec.saw(http.MethodDelete, "/channels/github/authorized-actors/alice") {
			t.Fatalf("GitHub removal did not use captured row ID: %s", rec.all())
		}
		var action channelAccessActionJSON
		if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &action); err != nil || action.Action != "remove" || action.User.ID != "actor-1" || action.User.Identity != "alice" {
			t.Fatalf("GitHub removal JSON = %q, err=%v", out.String(), err)
		}
	})
}

func TestGitHubChannelAccessTUIStaleTargetDoesNotDelete(t *testing.T) {
	listPath := "/channels/github/runtime-settings"
	deletes := 0
	lists := 0
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != listPath {
			http.NotFound(w, r)
			return
		}
		lists++
		w.Header().Set("Content-Type", "text/html")
		if lists == 1 {
			_, _ = w.Write([]byte(channelAccessTestPage("github", channelAccessTestRow{id: "actor-1", name: "Alice", identity: "alice"})))
			return
		}
		_, _ = w.Write([]byte(channelAccessTestPage("github")))
		if r.Method == http.MethodDelete {
			deletes++
		}
	})
	m = runLine(t, m, "/channels access github remove @alice")
	if m.pendingConfirmation == nil || lists != 1 {
		t.Fatalf("GitHub target was not captured before confirmation: pending=%v lists=%d", m.pendingConfirmation != nil, lists)
	}
	m = runLine(t, m, "yes")
	if deletes != 0 || lists != 2 || !strings.Contains(transcript(m), "does not belong to selected project") {
		t.Fatalf("stale GitHub target was deleted: deletes=%d lists=%d output=%s", deletes, lists, transcript(m))
	}
}

// teaKeyEsc keeps this focused test independent of the model's input text.
func teaKeyEsc() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEsc} }
