package terminal

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

func terminalOutboundTargetPage(projectID string, targets []client.OutboundTarget, allowed bool) string {
	var b strings.Builder
	b.WriteString(`<div id="outbound-targets-section" data-project-id="` + projectID + `"><form id="outbound-targets-policy-form"><input type="hidden" name="project_id" value="` + projectID + `"><input type="checkbox" name="enabled" value="true"`)
	if allowed {
		b.WriteString(` checked`)
	}
	b.WriteString(`></form><table><tbody>`)
	for _, target := range targets {
		b.WriteString(`<tr data-outbound-target-draft-key="` + target.ID + `"><td>` + target.Platform + `</td></tr>`)
	}
	b.WriteString(`</tbody></table><div id="outbound-targets-draft-fields">`)
	for _, target := range targets {
		b.WriteString(`<div data-outbound-target-draft-key="` + target.ID + `">`)
		for name, value := range map[string]string{
			"target_row_id": target.ID, "target_platform": target.Platform, "target_kind": target.TargetKind,
			"target_name": target.Name, "target_target_id": target.Destination, "target_thread_id": target.ThreadID,
			"target_is_home": boolString(target.Home), "target_default_subject": target.DefaultSubject,
		} {
			b.WriteString(`<input type="hidden" name="` + name + `" value="` + value + `">`)
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`</div></div>`)
	return b.String()
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func TestOutboundTargetAddAcceptsNegativeTelegramChatIDs(t *testing.T) {
	target, err := parseOutboundTargetAdd([]string{"telegram", "-1001234567890", "--kind", "chat", "--topic-id", "7"})
	if err != nil {
		t.Fatal(err)
	}
	if target.Platform != "telegram" || target.Destination != "-1001234567890" || target.ThreadID != "7" {
		t.Fatalf("target = %#v", target)
	}
}

func TestOutboundTargetsTUIListShowEmptyAndNoProject(t *testing.T) {
	target := client.OutboundTarget{ID: "target-a", Platform: "slack", TargetKind: "channel", Name: "ops", Destination: "C123", ThreadID: "42", Home: true, DefaultSubject: "Deploy"}
	m, rec := dispatchModel(t, map[string]string{"/channels/outbound-targets": terminalOutboundTargetPage("p1", []client.OutboundTarget{target}, true)})
	m = runLine(t, m, "/channels targets list")
	out := stripANSI(transcript(m))
	for _, want := range []string{"PLATFORM", "slack", "channel", "ops", "C123", "42", "Home", "Deploy"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "target-a") || strings.Contains(out, "project_id") || strings.Contains(out, "<div") {
		t.Fatalf("list output exposed internal or HTML data:\n%s", out)
	}
	m = runLine(t, m, "/channels targets show ops")
	if strings.Count(rec.all(), "GET /channels/outbound-targets") != 2 {
		t.Fatalf("list/show requests =\n%s", rec.all())
	}
	if !strings.Contains(stripANSI(transcript(m)), "C123") {
		t.Fatal("show output omitted destination")
	}

	m, rec = dispatchModel(t, map[string]string{"/channels/outbound-targets": terminalOutboundTargetPage("p1", nil, false)})
	m = runLine(t, m, "/channels targets list")
	if !strings.Contains(stripANSI(transcript(m)), "No saved outbound targets") {
		t.Fatalf("empty output missing guidance:\n%s", transcript(m))
	}
	m.selectedID = ""
	m = runLine(t, m, "/channels targets list")
	if rec.count("GET", "/channels/outbound-targets") != 1 {
		t.Fatalf("no-project command made a request:\n%s", rec.all())
	}
	if !strings.Contains(stripANSI(transcript(m)), "no project selected") {
		t.Fatalf("no-project guidance missing:\n%s", transcript(m))
	}
}

func TestOutboundTargetsTUIAddEditPolicyAndDraftTest(t *testing.T) {
	old := client.OutboundTarget{ID: "target-a", Platform: "slack", TargetKind: "channel", Name: "ops", Destination: "C123"}
	newTarget := client.OutboundTarget{ID: "target-b", Platform: "email", TargetKind: "email", Name: "client", Destination: "person@example.com", DefaultSubject: "Hello"}
	var savedTargets []client.OutboundTarget
	var savedPolicy bool
	var postForm = make(map[string]string)
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/channels/outbound-targets" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/html")
			if savedTargets == nil {
				_, _ = io.WriteString(w, terminalOutboundTargetPage("p1", []client.OutboundTarget{old}, savedPolicy))
			} else {
				_, _ = io.WriteString(w, terminalOutboundTargetPage("p1", savedTargets, savedPolicy))
			}
			return
		}
		if r.URL.Path == "/channels/send-message-explicit-targets" && r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse save form: %v", err)
			}
			last := func(name string) string {
				values := r.PostForm[name]
				if len(values) == 0 {
					return ""
				}
				return values[len(values)-1]
			}
			postForm["platform"] = last("target_platform")
			postForm["destination"] = last("target_target_id")
			postForm["name"] = last("target_name")
			postForm["thread"] = last("target_thread_id")
			postForm["home"] = last("target_is_home")
			postForm["enabled"] = last("enabled")
			savedPolicy = last("enabled") == "true"
			if strings.Contains(last("target_target_id"), "person@example.com") {
				savedTargets = []client.OutboundTarget{old, newTarget}
			} else {
				savedTargets = []client.OutboundTarget{old}
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<div>saved</div>`)
			return
		}
		if r.URL.Path == "/channels/outbound-targets/test-draft" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<span class="text-success">Sent</span>`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	})

	m = runLine(t, m, `/channels targets add email person@example.com --name client --default-subject Hello`)
	if postForm["platform"] != "email" || postForm["destination"] != "person@example.com" || postForm["name"] != "client" {
		t.Fatalf("add form = %#v", postForm)
	}
	if !strings.Contains(stripANSI(transcript(m)), "added outbound target") || !strings.Contains(stripANSI(transcript(m)), "person@example.com") {
		t.Fatalf("add output missing result:\n%s", transcript(m))
	}

	postForm = make(map[string]string)
	m = runLine(t, m, `/channels targets edit client --thread-id 9 --home`)
	if postForm["thread"] != "9" || postForm["home"] != "true" {
		t.Fatalf("edit form = %#v", postForm)
	}
	m = runLine(t, m, `/channels targets policy on`)
	if postForm["enabled"] != "true" {
		t.Fatalf("policy form did not enable explicit targets: %#v", postForm)
	}
	m = runLine(t, m, `/channels targets test draft email person@example.com --name draft`)
	if !strings.Contains(stripANSI(transcript(m)), `outbound target test for "draft": sent`) {
		t.Fatalf("draft test output missing sent result:\n%s", transcript(m))
	}
	if strings.Contains(stripANSI(transcript(m)), "<span") {
		t.Fatal("draft test exposed raw HTML")
	}
}

func TestOutboundTargetsMutationRefreshFailureAndTestFailure(t *testing.T) {
	target := client.OutboundTarget{ID: "target-a", Platform: "slack", TargetKind: "channel", Name: "ops", Destination: "C123"}
	getCalls := 0
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/channels/outbound-targets" && r.Method == http.MethodGet:
			getCalls++
			if getCalls == 1 {
				w.Header().Set("Content-Type", "text/html")
				_, _ = io.WriteString(w, terminalOutboundTargetPage("p1", nil, false))
				return
			}
			if getCalls == 2 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `<html><body>backend secret and raw markup</body></html>`)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, terminalOutboundTargetPage("p1", []client.OutboundTarget{target}, false))
		case r.URL.Path == "/channels/send-message-explicit-targets" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<div>saved</div>`)
		case r.URL.Path == "/channels/outbound-targets/target-a/test" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<span class="text-error" title="provider secret">Failed</span>`)
		default:
			http.NotFound(w, r)
		}
	})

	m = runLine(t, m, `/channels targets add slack C123 --name ops`)
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "added outbound target") || strings.Contains(out, "backend secret") || strings.Contains(out, "<html") || strings.Contains(out, "raw markup") {
		t.Fatalf("refresh failure output leaked markup or omitted status:\n%s", out)
	}

	m = runLine(t, m, "/channels targets test target-a")
	out = stripANSI(transcript(m))
	if !strings.Contains(out, `outbound target test for "ops": failed`) || strings.Contains(out, "<span") || strings.Contains(out, "provider secret") {
		t.Fatalf("test failure output was unsafe or unclear:\n%s", out)
	}
}

func TestOutboundTargetsRemoveConfirmationStaleAndAmbiguous(t *testing.T) {
	first := client.OutboundTarget{ID: "target-a", Platform: "slack", TargetKind: "channel", Name: "ops-one", Destination: "C1"}
	second := client.OutboundTarget{ID: "target-b", Platform: "slack", TargetKind: "channel", Name: "ops-two", Destination: "C2"}
	current := []client.OutboundTarget{first, second}
	posts := 0
	gets := 0
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/channels/outbound-targets" && r.Method == http.MethodGet {
			gets++
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, terminalOutboundTargetPage("p1", current, false))
			return
		}
		if r.URL.Path == "/channels/send-message-explicit-targets" {
			posts++
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse remove form: %v", err)
			}
			current = []client.OutboundTarget{second}
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<div>saved</div>`)
			return
		}
		http.NotFound(w, r)
	})

	m = runLine(t, m, "/channels targets remove foreign-row")
	if m.pendingConfirmation != nil || posts != 0 {
		t.Fatalf("foreign remove changed state: pending=%v posts=%d", m.pendingConfirmation != nil, posts)
	}
	if !strings.Contains(stripANSI(transcript(m)), "nothing matches") {
		t.Fatalf("foreign remove was not rejected:\n%s", transcript(m))
	}

	m = runLine(t, m, "/channels targets remove ops")
	if m.pendingConfirmation != nil || posts != 0 {
		t.Fatalf("ambiguous remove changed state: pending=%v posts=%d", m.pendingConfirmation != nil, posts)
	}
	if !strings.Contains(stripANSI(transcript(m)), `"ops" is ambiguous`) {
		t.Fatalf("ambiguous remove was not reported:\n%s", transcript(m))
	}
	m = runLine(t, m, "/channels targets remove ops-one")
	if m.pendingConfirmation == nil || posts != 0 {
		t.Fatalf("remove did not wait for confirmation: pending=%v posts=%d", m.pendingConfirmation != nil, posts)
	}
	m = runLine(t, m, "esc")
	if m.pendingConfirmation != nil || posts != 0 {
		t.Fatalf("canceled remove mutated: pending=%v posts=%d", m.pendingConfirmation != nil, posts)
	}
	m = runLine(t, m, "/channels targets remove ops-one")
	m = runLine(t, m, "yes")
	if posts != 1 {
		t.Fatalf("confirmed remove posts=%d, want one", posts)
	}
	if gets < 3 {
		t.Fatalf("remove did not revalidate and refresh: gets=%d", gets)
	}

	// A reference that was captured before the row disappeared must not submit
	// the replacement form after confirmation.
	current = []client.OutboundTarget{first}
	m = runLine(t, m, "/channels targets remove ops-one")
	current = nil
	before := posts
	m = runLine(t, m, "yes")
	if posts != before {
		t.Fatalf("stale remove submitted a mutation: posts=%d before=%d", posts, before)
	}
	if !strings.Contains(stripANSI(transcript(m)), "stale") {
		t.Fatalf("stale remove error missing:\n%s", transcript(m))
	}

	// A row that keeps its ID but changes canonical fields after capture is also
	// stale and must not be removed.
	current = []client.OutboundTarget{first}
	m = runLine(t, m, "/channels targets remove ops-one")
	changed := first
	changed.Destination = "C99"
	current = []client.OutboundTarget{changed}
	before = posts
	m = runLine(t, m, "yes")
	if posts != before {
		t.Fatalf("changed stale remove submitted a mutation: posts=%d before=%d", posts, before)
	}
	if !strings.Contains(stripANSI(transcript(m)), "stale") {
		t.Fatalf("changed stale remove error missing:\n%s", transcript(m))
	}
}

func TestOutboundTargetsCLIJSONAndForceSafety(t *testing.T) {
	target := client.OutboundTarget{ID: "target-a", Platform: "discord", TargetKind: "user", Name: "reviewer", Destination: "123456789", Home: true}
	page := terminalOutboundTargetPage("p1", []client.OutboundTarget{target}, false)
	c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects, "/channels/outbound-targets": page})
	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"channels", "targets", "list"}, false, true); err != nil {
		t.Fatal(err)
	}
	var targets []client.OutboundTarget
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &targets); err != nil {
		t.Fatalf("JSON output = %q: %v", out.String(), err)
	}
	if len(targets) != 1 || targets[0].Destination != "123456789" {
		t.Fatalf("JSON targets = %#v", targets)
	}
	if strings.Contains(out.String(), "target-a") || strings.Contains(out.String(), "project_id") {
		t.Fatalf("CLI JSON leaked internal target fields: %s", out.String())
	}
	if !rec.sawQuery("GET /channels/outbound-targets?project_id=p1") {
		t.Fatalf("CLI list was not project scoped:\n%s", rec.all())
	}

	c, rec = cliServer(t, map[string]string{"/api/projects": cliProjects, "/channels/outbound-targets": page})
	out.Reset()
	err := RunCLI(c, &out, "demo", []string{"channels", "targets", "remove", "reviewer"}, false, false)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("unforced remove error = %v", err)
	}
	if rec.saw("POST", "/channels/send-message-explicit-targets") {
		t.Fatal("unforced CLI remove mutated")
	}
}

func TestOutboundTargetsCLIEmptyJSON(t *testing.T) {
	c, _ := cliServer(t, map[string]string{"/api/projects": cliProjects, "/channels/outbound-targets": terminalOutboundTargetPage("p1", nil, false)})
	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"channels", "targets", "list"}, false, true); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "[]" {
		t.Fatalf("empty CLI JSON = %q, want []", got)
	}
}
