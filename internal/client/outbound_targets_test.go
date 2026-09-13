package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

const outboundTargetsFixture = `<div id="outbound-targets-section" data-project-id="project-2">
<form id="outbound-targets-policy-form"><input type="hidden" name="project_id" value="project-2"><input type="checkbox" name="enabled" value="true" checked></form>
<table><tbody>
<tr data-outbound-target-draft-key="target-a"><td>slack</td></tr>
<tr data-outbound-target-draft-key="target-b"><td>email</td></tr>
</tbody></table>
<div id="outbound-targets-draft-fields">
<div data-outbound-target-draft-key="target-a"><input type="hidden" name="target_row_id" value="target-a"><input type="hidden" name="target_platform" value="slack"><input type="hidden" name="target_kind" value="channel"><input type="hidden" name="target_name" value="ops"><input type="hidden" name="target_target_id" value="C123"><input type="hidden" name="target_thread_id" value="42"><input type="hidden" name="target_is_home" value="true"><input type="hidden" name="target_default_subject" value="Deploy"></div>
<div data-outbound-target-draft-key="target-b"><input type="hidden" name="target_row_id" value="target-b"><input type="hidden" name="target_platform" value="email"><input type="hidden" name="target_kind" value="email"><input type="hidden" name="target_name" value="client"><input type="hidden" name="target_target_id" value="person@example.com"><input type="hidden" name="target_thread_id" value=""><input type="hidden" name="target_is_home" value="false"><input type="hidden" name="target_default_subject" value="Hello"></div>
</div></div>`

const emptyOutboundTargetsFixture = `<div id="outbound-targets-section" data-project-id="project-2"><form><input type="hidden" name="project_id" value="project-2"><input type="checkbox" name="enabled" value="true"></form><table><tbody><tr id="outbound-targets-empty-row"><td>No outbound targets saved yet.</td></tr></tbody></table><div id="outbound-targets-draft-fields"></div></div>`

func TestGetOutboundTargetsParsesScopedSafeRowsAndPolicy(t *testing.T) {
	var request string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request = r.Method + " " + r.URL.RequestURI()
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, outboundTargetsFixture)
	}))
	defer srv.Close()
	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	page, err := c.GetOutboundTargets(context.Background(), "project-2")
	if err != nil {
		t.Fatal(err)
	}
	if request != "GET /channels/outbound-targets?project_id=project-2" {
		t.Fatalf("request = %q", request)
	}
	want := []OutboundTarget{
		{ID: "target-a", ProjectID: "project-2", Platform: "slack", TargetKind: "channel", Name: "ops", Destination: "C123", TargetID: "C123", ThreadID: "42", Home: true, DefaultSubject: "Deploy"},
		{ID: "target-b", ProjectID: "project-2", Platform: "email", TargetKind: "email", Name: "client", Destination: "person@example.com", TargetID: "person@example.com", Home: false, DefaultSubject: "Hello"},
	}
	if !reflect.DeepEqual(page.Targets, want) || !page.ExplicitUnsavedTargetsAllowed {
		t.Fatalf("page = %#v, want %#v with policy enabled", page, want)
	}
	encoded, err := json.Marshal(page.Targets)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "target-a") || strings.Contains(string(encoded), "project-2") || strings.Contains(string(encoded), "target_id") {
		t.Fatalf("safe JSON leaked internal fields: %s", encoded)
	}
	if wantJSON := `[ {"platform":"slack"}`; strings.HasPrefix(string(encoded), wantJSON) {
		t.Fatalf("unexpected JSON spacing: %s", encoded)
	}
}

func TestListOutboundTargetsEmptyUsesNonNilJSONArray(t *testing.T) {
	c := htmlServer(t, emptyOutboundTargetsFixture)
	targets, err := c.ListOutboundTargets(context.Background(), "project-2")
	if err != nil {
		t.Fatal(err)
	}
	if targets == nil || len(targets) != 0 {
		t.Fatalf("targets = %#v, want non-nil empty slice", targets)
	}
	encoded, err := json.Marshal(targets)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "[]" {
		t.Fatalf("empty JSON = %s, want []", encoded)
	}
}

func TestOutboundTargetsRejectMalformedAndDuplicateRows(t *testing.T) {
	cases := []string{
		`<div id="outbound-targets-section" data-project-id="p"><form><input name="project_id" value="p"></form><tr data-outbound-target-draft-key="a"></tr><div data-outbound-target-draft-key="a"><input name="target_row_id" value="other"><input name="target_platform" value="slack"><input name="target_kind" value="channel"><input name="target_name" value="a"><input name="target_target_id" value="C1"><input name="target_thread_id" value=""><input name="target_is_home" value="false"><input name="target_default_subject" value=""></div></div>`,
		`<div id="outbound-targets-section" data-project-id="p"><form><input name="project_id" value="p"></form><tr data-outbound-target-draft-key="a"></tr><tr data-outbound-target-draft-key="a"></tr><div data-outbound-target-draft-key="a"><input name="target_platform" value="slack"><input name="target_kind" value="channel"><input name="target_name" value="a"><input name="target_target_id" value="C1"><input name="target_thread_id" value=""><input name="target_is_home" value="false"><input name="target_default_subject" value=""></div></div>`,
		`<div id="outbound-targets-section" data-project-id="p"><form><input name="project_id" value="p"></form><tr data-outbound-target-draft-key="a"></tr><tr data-outbound-target-draft-key="b"></tr><div data-outbound-target-draft-key="a"><input name="target_platform" value="slack"><input name="target_kind" value="channel"><input name="target_name" value="a"><input name="target_target_id" value="C1"><input name="target_thread_id" value=""><input name="target_is_home" value="false"><input name="target_default_subject" value=""></div><div data-outbound-target-draft-key="b"><input name="target_platform" value="slack"><input name="target_kind" value="channel"><input name="target_name" value="b"><input name="target_target_id" value="C1"><input name="target_thread_id" value=""><input name="target_is_home" value="false"><input name="target_default_subject" value=""></div></div>`,
	}
	for _, body := range cases {
		root, err := parseHTML(body)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseOutboundTargetsPage(root, "p"); err == nil {
			t.Fatalf("malformed fixture unexpectedly parsed: %s", body)
		}
	}
}

func TestSaveOutboundTargetsPreservesBackendFormAndSurfacesValidation(t *testing.T) {
	var got url.Values
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		got = r.PostForm
		w.Header().Set("Content-Type", "text/html")
		if calls == 1 {
			w.Header().Set("HX-Trigger", "outbound-targets-save-error")
			_, _ = io.WriteString(w, `<div class="alert alert-success py-2 text-sm mt-3">Duplicate outbound target destination</div>`)
			return
		}
		_, _ = io.WriteString(w, `<div>saved</div>`)
	}))
	defer srv.Close()
	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	target := OutboundTarget{ID: "a", Platform: " Slack ", TargetKind: "channel", Name: "#Ops", Destination: " C123 ", ThreadID: "42", Home: true, DefaultSubject: " Deploy "}
	err = c.SaveOutboundTargets(context.Background(), "project-2", []OutboundTarget{target}, true)
	if err == nil || !strings.Contains(err.Error(), "Duplicate outbound target destination") {
		t.Fatalf("save error = %v", err)
	}
	if got.Get("project_id") != "project-2" || got.Get("enabled") != "true" || got.Get("target_row_id") != "a" || got.Get("target_target_id") != "C123" {
		t.Fatalf("submitted form = %v", got)
	}
	if err := c.SaveOutboundTargets(context.Background(), "project-2", []OutboundTarget{target}, false); err != nil {
		t.Fatal(err)
	}
	if got.Get("enabled") != "" {
		t.Fatalf("disabled policy submitted enabled=%q", got.Get("enabled"))
	}
}

func TestOutboundTargetTestsClassifySentAndFailedWithoutHTML(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/channels/outbound-targets/test-draft" {
			if r.Method != http.MethodPost {
				t.Errorf("draft method = %s", r.Method)
			}
			_, _ = io.WriteString(w, `<span class="text-success">Sent</span>`)
			return
		}
		if calls%2 == 0 {
			_, _ = io.WriteString(w, `<span class="text-error" title="provider secret should not escape">Failed</span>`)
			return
		}
		_, _ = io.WriteString(w, `<span class="text-success">Sent</span>`)
	}))
	defer srv.Close()
	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	target := OutboundTarget{Platform: "email", TargetKind: "email", Destination: "person@example.com"}
	sent, err := c.TestOutboundTarget(context.Background(), "project-2", "target-a")
	if err != nil || !sent {
		t.Fatalf("saved test = %t, %v", sent, err)
	}
	sent, err = c.TestOutboundTarget(context.Background(), "project-2", "target-a")
	if err != nil || sent {
		t.Fatalf("failed saved test = %t, %v", sent, err)
	}
	sent, err = c.TestOutboundTargetDraft(context.Background(), "project-2", target)
	if err != nil || !sent {
		t.Fatalf("draft test = %t, %v", sent, err)
	}
}

func parseHTML(body string) (*html.Node, error) {
	return html.Parse(strings.NewReader(body))
}
