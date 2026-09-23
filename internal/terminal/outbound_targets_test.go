package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

func terminalOutboundTargetPolicy(projectID string, allowed bool) string {
	checked := ""
	if allowed {
		checked = " checked"
	}
	return `<div id="outbound-targets-section" data-project-id="` + projectID + `"><form id="outbound-targets-policy-form"><input type="hidden" name="project_id" value="` + projectID + `"><input type="checkbox" name="enabled" value="true"` + checked + `></form></div>`
}

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

func TestOutboundTargetParserAcceptsDocumentedAliases(t *testing.T) {
	target, err := parseOutboundTargetAdd([]string{
		"--platform", "email",
		"--destination", "person@example.com",
		"--target-kind", "email",
		"--name", "client",
		"--topic-id", "42",
		"--subject", "Hello",
		"--is-home",
	})
	if err != nil {
		t.Fatal(err)
	}
	if target.Platform != "email" || target.Destination != "person@example.com" || target.TargetKind != "email" || target.Name != "client" || target.ThreadID != "42" || target.DefaultSubject != "Hello" || !target.Home {
		t.Fatalf("target = %#v", target)
	}

	_, values, err := parseOutboundTargetEdit([]string{"client", "--no-home"})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := values["home"]; !ok || got != "false" {
		t.Fatalf("--no-home values = %#v, want home=false", values)
	}
}

func TestOutboundTargetEditRejectsUnsupportedPlatform(t *testing.T) {
	_, _, err := parseOutboundTargetEdit([]string{"client", "--platform", "mastodon"})
	if err == nil || !strings.Contains(err.Error(), outboundTargetPlatformError) {
		t.Fatalf("parseOutboundTargetEdit error = %v, want %q", err, outboundTargetPlatformError)
	}
}

func TestOutboundTargetsCLIAddJSONUsesCanonicalRefreshedTarget(t *testing.T) {
	canonical := client.OutboundTarget{
		ID: "target-email-1", ProjectID: "p1", Platform: "email", TargetKind: "email",
		Name: "person", Destination: "person@example.com",
	}
	saveCalls := 0
	refreshCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, cliProjects)
		case r.Method == http.MethodGet && r.URL.Path == "/channels/outbound-targets":
			refreshCalls++
			w.Header().Set("Content-Type", "text/html")
			if refreshCalls == 1 {
				_, _ = io.WriteString(w, terminalOutboundTargetPage("p1", nil, false))
			} else {
				_, _ = io.WriteString(w, terminalOutboundTargetPage("p1", []client.OutboundTarget{canonical}, false))
			}
		case r.Method == http.MethodPost && r.URL.Path == "/channels/send-message-explicit-targets":
			saveCalls++
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse save form: %v", err)
			}
			if got := r.PostForm.Get("target_target_id"); got != "PERSON@EXAMPLE.COM" {
				t.Errorf("saved destination = %q, want raw submitted destination", got)
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<div>saved</div>`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"channels", "targets", "add", "email", "PERSON@EXAMPLE.COM", "--kind", "email", "--name", "person"}, false, true); err != nil {
		t.Fatal(err)
	}

	var action outboundTargetActionJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &action); err != nil {
		t.Fatalf("JSON output = %q: %v", out.String(), err)
	}
	if action.Action != "add" || action.Target.Destination != canonical.Destination {
		t.Fatalf("add action = %#v, want canonical destination %q", action, canonical.Destination)
	}
	if saveCalls != 1 || refreshCalls != 2 {
		t.Fatalf("save/refresh calls = %d/%d, want 1/2", saveCalls, refreshCalls)
	}
}

func TestOutboundTargetsCLIAddJSONUsesCanonicalOtherDestination(t *testing.T) {
	canonical := client.OutboundTarget{
		ID: "target-slack-1", ProjectID: "p1", Platform: "slack", TargetKind: "channel",
		Name: "ops", Destination: "c123",
	}
	refreshCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, cliProjects)
		case r.Method == http.MethodGet && r.URL.Path == "/channels/outbound-targets":
			refreshCalls++
			w.Header().Set("Content-Type", "text/html")
			if refreshCalls == 1 {
				_, _ = io.WriteString(w, terminalOutboundTargetPage("p1", nil, false))
			} else {
				_, _ = io.WriteString(w, terminalOutboundTargetPage("p1", []client.OutboundTarget{canonical}, false))
			}
		case r.Method == http.MethodPost && r.URL.Path == "/channels/send-message-explicit-targets":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<div>saved</div>`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"channels", "targets", "add", "slack", "C123", "--kind", "channel", "--name", "ops"}, false, true); err != nil {
		t.Fatal(err)
	}
	var action outboundTargetActionJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &action); err != nil {
		t.Fatalf("JSON output = %q: %v", out.String(), err)
	}
	if action.Target.Destination != canonical.Destination {
		t.Fatalf("add action = %#v, want canonical destination %q", action.Target, canonical.Destination)
	}
	if refreshCalls != 2 {
		t.Fatalf("refresh calls = %d, want 2", refreshCalls)
	}
}

func TestOutboundTargetsPlainAddListAndShowUseCanonicalDestination(t *testing.T) {
	canonical := client.OutboundTarget{
		ID: "target-email-2", ProjectID: "p1", Platform: "email", TargetKind: "email",
		Name: "person", Destination: "person@example.com",
	}
	var saved []client.OutboundTarget
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, cliProjects)
		case r.Method == http.MethodGet && r.URL.Path == "/channels/outbound-targets":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, terminalOutboundTargetPage("p1", saved, false))
		case r.Method == http.MethodPost && r.URL.Path == "/channels/send-message-explicit-targets":
			saved = []client.OutboundTarget{canonical}
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<div>saved</div>`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var plain bytes.Buffer
	if err := RunCLI(c, &plain, "demo", []string{"channels", "targets", "add", "email", "PERSON@EXAMPLE.COM", "--kind", "email"}, false, false); err != nil {
		t.Fatal(err)
	}
	if got := plain.String(); !strings.Contains(got, canonical.Destination) || strings.Contains(got, "PERSON@EXAMPLE.COM") {
		t.Fatalf("plain add output = %q, want only canonical destination", got)
	}

	var list bytes.Buffer
	if err := RunCLI(c, &list, "demo", []string{"channels", "targets", "list"}, false, true); err != nil {
		t.Fatal(err)
	}
	var listed []client.OutboundTarget
	if err := json.Unmarshal([]byte(strings.TrimSpace(list.String())), &listed); err != nil {
		t.Fatalf("list JSON = %q: %v", list.String(), err)
	}
	if len(listed) != 1 || listed[0].Destination != canonical.Destination {
		t.Fatalf("list targets = %#v, want canonical target", listed)
	}

	var show bytes.Buffer
	if err := RunCLI(c, &show, "demo", []string{"channels", "targets", "show", "person"}, false, true); err != nil {
		t.Fatal(err)
	}
	var shown client.OutboundTarget
	if err := json.Unmarshal([]byte(strings.TrimSpace(show.String())), &shown); err != nil {
		t.Fatalf("show JSON = %q: %v", show.String(), err)
	}
	if shown.Destination != canonical.Destination {
		t.Fatalf("show target = %#v, want canonical destination %q", shown, canonical.Destination)
	}
}

func TestOutboundTargetsAmbiguousNormalizedRefreshDoesNotRebindAdd(t *testing.T) {
	first := client.OutboundTarget{
		ID: "target-email-first", ProjectID: "p1", Platform: "email", TargetKind: "email",
		Name: "existing-first", Destination: "person@example.com",
	}
	second := client.OutboundTarget{
		ID: "target-email-second", ProjectID: "p1", Platform: "email", TargetKind: "email",
		Name: "existing-second", Destination: "PERSON@EXAMPLE.COM",
	}
	refreshCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, cliProjects)
		case r.Method == http.MethodGet && r.URL.Path == "/channels/outbound-targets":
			refreshCalls++
			w.Header().Set("Content-Type", "text/html")
			if refreshCalls == 1 {
				_, _ = io.WriteString(w, terminalOutboundTargetPage("p1", nil, false))
			} else {
				_, _ = io.WriteString(w, terminalOutboundTargetPage("p1", []client.OutboundTarget{first, second}, false))
			}
		case r.Method == http.MethodPost && r.URL.Path == "/channels/send-message-explicit-targets":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<div>saved</div>`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"channels", "targets", "add", "email", "PERSON@EXAMPLE.COM", "--kind", "email", "--name", "new-target"}, false, true); err != nil {
		t.Fatal(err)
	}
	var action outboundTargetActionJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &action); err != nil {
		t.Fatalf("JSON output = %q: %v", out.String(), err)
	}
	if action.Target.Name != "new-target" || action.Target.Destination != "PERSON@EXAMPLE.COM" {
		t.Fatalf("ambiguous add rebound to a refreshed row: %#v", action.Target)
	}
	if refreshCalls != 2 {
		t.Fatalf("refresh calls = %d, want 2", refreshCalls)
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

func TestOutboundTargetsCLIPolicyShowUsesPolicyEndpointOnly(t *testing.T) {
	var noisyRows strings.Builder
	for i := 0; i < 200; i++ {
		noisyRows.WriteString(`<tr data-outbound-target-draft-key="dup"><td>broken</td></tr>`)
		noisyRows.WriteString(`<div data-outbound-target-draft-key="dup"><input name="target_platform" value="slack"></div>`)
	}
	body := strings.Replace(terminalOutboundTargetPolicy("p1", true), `</div>`, noisyRows.String()+`</div>`, 1)
	c, rec := cliServer(t, map[string]string{"/api/projects": cliProjects, "/channels/send-message-explicit-targets": body})
	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"channels", "targets", "policy", "show"}, false, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "explicit unsaved targets: allowed") {
		t.Fatalf("policy output = %q", out.String())
	}
	if !rec.sawQuery("GET /channels/send-message-explicit-targets?project_id=p1") {
		t.Fatalf("policy show did not use policy endpoint:\n%s", rec.all())
	}
	if rec.saw("GET", "/channels/outbound-targets") {
		t.Fatalf("policy show fetched saved targets:\n%s", rec.all())
	}

	c, rec = cliServer(t, map[string]string{"/api/projects": cliProjects, "/channels/send-message-explicit-targets": terminalOutboundTargetPolicy("p1", false)})
	out.Reset()
	if err := RunCLI(c, &out, "demo", []string{"channels", "targets", "policy", "show"}, false, true); err != nil {
		t.Fatal(err)
	}
	var policy outboundTargetPolicyJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &policy); err != nil {
		t.Fatalf("policy JSON = %q: %v", out.String(), err)
	}
	if policy.ExplicitUnsavedTargetsAllowed {
		t.Fatalf("policy JSON = %#v, want blocked", policy)
	}
	if rec.saw("GET", "/channels/outbound-targets") {
		t.Fatalf("JSON policy show fetched saved targets:\n%s", rec.all())
	}
}

func TestOutboundTargetPolicyOutputRefreshFailureKeepsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/channels/send-message-explicit-targets" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		http.Error(w, `<html><body>backend secret and raw markup</body></html>`, http.StatusBadGateway)
	}))
	defer srv.Close()
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	previousJSON := jsonMode
	defer func() { jsonMode = previousJSON }()
	jsonMode = false
	out, err := outboundTargetPolicyOutput(context.Background(), c, "p1", "updated outbound target policy", true)
	if err != nil {
		t.Fatalf("plain refresh failure changed successful policy write into error: %v", err)
	}
	if out != "updated outbound target policy" || strings.Contains(out, "backend secret") || strings.Contains(out, "<html") {
		t.Fatalf("plain fallback = %q", out)
	}
	jsonMode = true
	out, err = outboundTargetPolicyOutput(context.Background(), c, "p1", "updated outbound target policy", true)
	jsonMode = previousJSON
	if err != nil {
		t.Fatalf("JSON refresh failure changed successful policy write into error: %v", err)
	}
	var policy outboundTargetPolicyJSON
	if err := json.Unmarshal([]byte(out), &policy); err != nil {
		t.Fatalf("policy JSON fallback = %q: %v", out, err)
	}
	if !policy.ExplicitUnsavedTargetsAllowed || strings.Contains(out, "backend secret") || strings.Contains(out, "<html") {
		t.Fatalf("JSON fallback = %q", out)
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
		if r.URL.Path == "/channels/send-message-explicit-targets" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, terminalOutboundTargetPolicy("p1", savedPolicy))
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
			for key := range r.PostForm {
				if strings.HasPrefix(key, "target_") {
					postForm["targetFields"] = "present"
				}
			}
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
	postForm = make(map[string]string)
	m = runLine(t, m, `/channels targets policy on`)
	if postForm["enabled"] != "true" || postForm["targetFields"] != "" {
		t.Fatalf("policy form did not submit only policy fields: %#v", postForm)
	}
	m = runLine(t, m, `/channels targets test draft email person@example.com --name draft`)
	if !strings.Contains(stripANSI(transcript(m)), `outbound target test for "draft": sent`) {
		t.Fatalf("draft test output missing sent result:\n%s", transcript(m))
	}
	if strings.Contains(stripANSI(transcript(m)), "<span") {
		t.Fatal("draft test exposed raw HTML")
	}
}

func TestOutboundTargetsTUIEditRejectsUnsupportedPlatformBeforeSave(t *testing.T) {
	target := client.OutboundTarget{ID: "target-a", ProjectID: "p1", Platform: "email", TargetKind: "email", Name: "client", Destination: "person@example.com"}
	postCalls := 0
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/channels/outbound-targets" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, terminalOutboundTargetPage("p1", []client.OutboundTarget{target}, false))
			return
		}
		if r.URL.Path == "/channels/send-message-explicit-targets" && r.Method == http.MethodPost {
			postCalls++
			http.Error(w, "unexpected save", http.StatusInternalServerError)
			return
		}
		http.NotFound(w, r)
	})

	m = runLine(t, m, "/channels targets edit client --platform mastodon")
	if postCalls != 0 {
		t.Fatalf("unsupported platform edit posted save request %d times", postCalls)
	}
	if !strings.Contains(stripANSI(transcript(m)), outboundTargetPlatformError) {
		t.Fatalf("validation error missing from transcript:\n%s", transcript(m))
	}
}

func TestOutboundTargetsCLIEditPlatformCanonicalizesAndPreservesFields(t *testing.T) {
	existing := client.OutboundTarget{
		ID: "target-a", ProjectID: "p1", Platform: "email", TargetKind: "email", Name: "client",
		Destination: "person@example.com", ThreadID: "thread-1", Home: true, DefaultSubject: "Hello",
	}
	postForm := make(map[string]string)
	postCalls := 0
	refreshCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, cliProjects)
		case r.Method == http.MethodGet && r.URL.Path == "/channels/outbound-targets":
			refreshCalls++
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, terminalOutboundTargetPage("p1", []client.OutboundTarget{existing}, false))
		case r.Method == http.MethodPost && r.URL.Path == "/channels/send-message-explicit-targets":
			postCalls++
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse edit form: %v", err)
			}
			for _, key := range []string{"target_row_id", "target_platform", "target_kind", "target_name", "target_target_id", "target_thread_id", "target_is_home", "target_default_subject"} {
				postForm[key] = r.PostForm.Get(key)
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<div>saved</div>`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"channels", "targets", "edit", "client", "--platform", "Email"}, false, false); err != nil {
		t.Fatal(err)
	}
	if postCalls != 1 || refreshCalls != 2 {
		t.Fatalf("post/refresh calls = %d/%d, want 1/2", postCalls, refreshCalls)
	}
	want := map[string]string{
		"target_row_id": "target-a", "target_platform": "email", "target_kind": "email", "target_name": "client",
		"target_target_id": "person@example.com", "target_thread_id": "thread-1", "target_is_home": "true", "target_default_subject": "Hello",
	}
	for key, value := range want {
		if postForm[key] != value {
			t.Fatalf("posted %s = %q, want %q; form=%#v", key, postForm[key], value, postForm)
		}
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

func TestOutboundTargetMutationOutputJSONRefreshFailureKeepsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/channels/outbound-targets" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		http.Error(w, `<html><body>backend secret and raw markup</body></html>`, http.StatusBadGateway)
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	previousJSON := jsonMode
	jsonMode = true
	defer func() { jsonMode = previousJSON }()

	submitted := client.OutboundTarget{
		Platform: "email", TargetKind: "email", Name: "person", Destination: "PERSON@EXAMPLE.COM",
	}
	out, err := outboundTargetMutationOutput(context.Background(), c, "p1", "add", "added outbound target person", submitted)
	if err != nil {
		t.Fatalf("refresh failure changed successful mutation into error: %v", err)
	}
	var action outboundTargetActionJSON
	if err := json.Unmarshal([]byte(out), &action); err != nil {
		t.Fatalf("JSON output = %q: %v", out, err)
	}
	if action.Action != "add" || action.Target.Destination != submitted.Destination {
		t.Fatalf("refresh fallback action = %#v, want submitted target", action)
	}
	if strings.Contains(out, "backend secret") || strings.Contains(out, "<html") || strings.Contains(out, "raw markup") {
		t.Fatalf("refresh fallback exposed backend response: %q", out)
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

func TestOutboundTargetsCLICancellationPropagatesToBlockedRemovalLookup(t *testing.T) {
	lookupStarted := make(chan struct{})
	lookupCanceled := make(chan struct{})
	var postMu sync.Mutex
	var postCalls int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"projects":[{"id":"p1","name":"demo"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/channels/outbound-targets":
			close(lookupStarted)
			<-r.Context().Done()
			close(lookupCanceled)
		case r.Method == http.MethodPost && r.URL.Path == "/channels/send-message-explicit-targets":
			postMu.Lock()
			postCalls++
			postMu.Unlock()
			http.Error(w, "unexpected mutation", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- RunCLIContext(ctx, c, io.Discard, "demo", []string{"channels", "targets", "remove", "target-a"}, true, false)
	}()

	select {
	case <-lookupStarted:
	case <-time.After(time.Second):
		t.Fatal("outbound target removal lookup did not start")
	}
	cancel()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("canceled outbound target removal returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled outbound target removal did not return promptly")
	}
	select {
	case <-lookupCanceled:
	case <-time.After(time.Second):
		t.Fatal("outbound target removal lookup did not observe caller cancellation")
	}
	postMu.Lock()
	gotPosts := postCalls
	postMu.Unlock()
	if gotPosts != 0 {
		t.Fatalf("canceled outbound target removal sent %d replacement mutations", gotPosts)
	}
}
