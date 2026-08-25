package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// htmlServer serves a fixed HTML body and returns a client pointed at it.
func htmlServer(t *testing.T, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestListAlertsScrapesCards(t *testing.T) {
	// Mirrors the real alertRow markup.
	const page = `<div>
	  <div class="card" data-alert-id="a1" data-alert-scroll-anchor="a1"
	       data-search-card data-search-text="build failed on main">
	    <div class="card-body">
	      <p class="font-semibold font-bold">Build failed</p>
	      <div><span class="badge badge-ghost badge-sm">task_failed</span>
	           <span class="badge badge-warning badge-sm">pending</span></div>
	      <p class="text-sm opacity-60 mt-1">exit status 1 on main</p>
	    </div>
	    <button data-alert-id="a1">delete</button>
	  </div>
	  <div class="card opacity-60" data-alert-id="a2" data-alert-scroll-anchor="a2"
	       data-search-card data-search-text="review requested">
	    <div class="card-body"><p class="font-semibold">Review requested</p></div>
	  </div>
	</div>`
	c := htmlServer(t, page)

	alerts, err := c.ListAlerts(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 2 {
		t.Fatalf("got %d alerts, want 2: %+v", len(alerts), alerts)
	}
	if alerts[0].ID != "a1" || alerts[0].Title != "Build failed" {
		t.Errorf("alert[0] = %+v", alerts[0])
	}
	if alerts[0].Text != "build failed on main" {
		t.Errorf("text = %q", alerts[0].Text)
	}
	if alerts[0].Message != "exit status 1 on main" {
		t.Errorf("message = %q", alerts[0].Message)
	}
	if alerts[0].Read {
		t.Error("alert a1 should be unread")
	}
	if !alerts[1].Read {
		t.Error("alert a2 (opacity-60) should be read")
	}
	if got := strings.Join(alerts[0].Badges, ","); !strings.Contains(got, "task_failed") {
		t.Errorf("badges = %q", got)
	}
}

func TestListSkillsReadsDataAttributes(t *testing.T) {
	const page = `<div>
	  <div data-skill-handle="deploy" data-skill-name="Deploy"
	       data-skill-description="ship to prod" data-skill-scope="project"
	       data-skill-source="repo" data-skill-content="# Deploy"
	       data-skill-enabled="true" data-skill-always-use="false"></div>
	  <div data-skill-handle="review" data-skill-name="Review"
	       data-skill-scope="user" data-skill-enabled="false"
	       data-skill-always-use="true"></div>
	</div>`
	c := htmlServer(t, page)

	skills, err := c.ListSkills(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 2 {
		t.Fatalf("got %d skills, want 2", len(skills))
	}
	s := skills[0]
	if s.Handle != "deploy" || s.Name != "Deploy" || s.Scope != "project" {
		t.Errorf("skill = %+v", s)
	}
	if !s.Enabled || s.AlwaysUse {
		t.Errorf("flags = enabled %t always %t", s.Enabled, s.AlwaysUse)
	}
	if skills[1].Enabled || !skills[1].AlwaysUse {
		t.Errorf("skill[1] flags = %+v", skills[1])
	}
}

func TestListModelsAndAgents(t *testing.T) {
	t.Run("models", func(t *testing.T) {
		c := htmlServer(t, `<div data-model-id="m1" data-model-name="Sonnet"
				data-model-provider="anthropic" data-model-model="claude-sonnet-4">Sonnet model details</div>`)
		list, err := c.ListModels(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].Name != "Sonnet" || list[0].Provider != "anthropic" {
			t.Fatalf("models = %+v", list)
		}
		if list[0].Text != "Sonnet model details" {
			t.Fatalf("model text = %q, want %q", list[0].Text, "Sonnet model details")
		}
	})

	t.Run("agents", func(t *testing.T) {
		c := htmlServer(t, `<div data-agent-id="ag1" data-agent-key="reviewer"
			data-agent-name="Reviewer" data-agent-description="reviews code"
			data-agent-model="sonnet" data-agent-scope="project"></div>`)
		list, err := c.ListAgents(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].Key != "reviewer" || list[0].Model != "sonnet" {
			t.Fatalf("agents = %+v", list)
		}
	})
}

func TestListAutomationsReadsCardMarkup(t *testing.T) {
	// Mirrors the delete-menu button markup on a real automation card, which
	// carries the id/name the TUI resolves references against.
	const page = `<div>
	  <div class="card" data-automation-url="/automations/au1?project_id=p1"
	       data-search-card data-search-text="native sdlc active">
	    <div class="card-body relative">
	      <span class="badge badge-outline badge-sm">active</span>
	      <button type="button" class="text-error" data-automation-card-delete="au1"
	              data-automation-name="Native SDLC"></button>
	    </div>
	  </div>
	  <div class="card" data-automation-url="/automations/au2?project_id=p1"
	       data-search-card data-search-text="github sdlc paused">
	    <div class="card-body relative">
	      <span class="badge badge-outline badge-sm">paused</span>
	      <button type="button" class="text-error" data-automation-card-delete="au2"
	              data-automation-name="GitHub SDLC"></button>
	    </div>
	  </div>
	</div>`
	c := htmlServer(t, page)

	automations, err := c.ListAutomations(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(automations) != 2 {
		t.Fatalf("got %d automations, want 2: %+v", len(automations), automations)
	}
	if automations[0].ID != "au1" || automations[0].Name != "Native SDLC" || automations[0].State != "active" {
		t.Errorf("automation[0] = %+v", automations[0])
	}
	if automations[1].ID != "au2" || automations[1].Name != "GitHub SDLC" || automations[1].State != "paused" {
		t.Errorf("automation[1] = %+v", automations[1])
	}
}

// benchmarkAutomationsPage builds representative cards with nested action markup.
// The nested controls are intentionally noisy because that is the DOM work the
// old whole-page NodeText path normalized even though the list only needs the
// card's ID, name, and lifecycle state.
func benchmarkAutomationsPage(count int) string {
	var b strings.Builder
	b.Grow(count * 700)
	b.WriteString("<!doctype html><html><body><main>")
	for i := 0; i < count; i++ {
		state := "active"
		if i%2 == 1 {
			state = "paused"
		}
		fmt.Fprintf(&b, `<article class="card" data-automation-url="/automations/au-%d?project_id=p1" data-search-card data-search-text="automation-%d %s">`, i, i, state)
		b.WriteString(`<div class="dropdown"><ul>`)
		for action := 0; action < 4; action++ {
			fmt.Fprintf(&b, `<li><button type="button"><span class="icon">action</span><span>control %d</span></button></li>`, action)
		}
		b.WriteString(`</ul></div><div class="card-body relative">`)
		fmt.Fprintf(&b, `<span class="badge badge-outline badge-sm">%s</span>`, state)
		fmt.Fprintf(&b, `<button type="button" class="text-error" data-automation-card-delete="au-%d" data-automation-name="Automation %d"><span>delete</span></button>`, i, i)
		b.WriteString(`<div class="description"><p>long nested action metadata and display prose</p><div><span>not a lifecycle badge</span></div></div></div></article>`)
	}
	b.WriteString("</main></body></html>")
	return b.String()
}

// BenchmarkAutomationsDisplayPath compares the old GetAutomations work
// (parse plus whole-document NodeText) with the structured ListAutomations
// work (the same parse plus only card fields and badge state). The 100- and
// 1,000-card cases model normal and large automation pages; run with
// `go test -bench BenchmarkAutomationsDisplayPath -benchmem ./internal/client`
// to compare ns/op, B/op, and allocs/op.
func BenchmarkAutomationsDisplayPath(b *testing.B) {
	for _, count := range []int{100, 1000} {
		page := benchmarkAutomationsPage(count)
		b.Run(fmt.Sprintf("old_full_text/%d", count), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(page)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				root, err := html.Parse(strings.NewReader(page))
				if err != nil {
					b.Fatal(err)
				}
				_ = NodeText(root)
			}
		})
		b.Run(fmt.Sprintf("new_structured/%d", count), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(page)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				root, err := html.Parse(strings.NewReader(page))
				if err != nil {
					b.Fatal(err)
				}
				_ = parseAutomations(root)
			}
		})
	}
}

// BenchmarkAutomationsDOMWork isolates the changed post-parse operation. HTML
// parsing is deliberately outside the timer because it is identical in both
// production paths; this measures whole-document normalization versus the
// structured card extraction that replaced it.
func BenchmarkAutomationsDOMWork(b *testing.B) {
	for _, count := range []int{100, 1000} {
		page := benchmarkAutomationsPage(count)
		root, err := html.Parse(strings.NewReader(page))
		if err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprintf("old_node_text/%d", count), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = NodeText(root)
			}
		})
		b.Run(fmt.Sprintf("new_structured/%d", count), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = parseAutomations(root)
			}
		})
	}
}

func TestGetScheduleScrapesEntries(t *testing.T) {
	const page = `<div id="schedule-content">
	  <div data-task-id="t1" data-schedule-id="s1">Nightly build — daily 02:00</div>
	  <div data-task-id="t2" data-schedule-id="s2">Weekly report — weekly mon</div>
	</div>`
	c := htmlServer(t, page)

	entries, summary, err := c.GetSchedule(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].ScheduleID != "s1" || entries[0].TaskID != "t1" {
		t.Errorf("entry = %+v", entries[0])
	}
	if entries[0].Text != "Nightly build — daily 02:00" {
		t.Errorf("entry text = %q, want %q", entries[0].Text, "Nightly build — daily 02:00")
	}
	if summary == "" {
		t.Error("expected summary text from #schedule-content")
	}
}

func TestResourceMutationRoutes(t *testing.T) {
	var gotMethod, gotPath string
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotMethod, gotPath, gotForm = r.Method, r.URL.Path, r.PostForm
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	ctx := context.Background()

	tests := []struct {
		name      string
		fn        func() error
		method    string
		path      string
		formKey   string
		formValue string
		checkForm bool
	}{
		{name: "alert approve", fn: func() error { return c.AlertAction(ctx, "a1", "approve", "p1") },
			method: "POST", path: "/alerts/a1/approve"},
		{name: "alert delete", fn: func() error { return c.DeleteAlert(ctx, "a1", "p1") },
			method: "DELETE", path: "/alerts/a1"},
		{name: "alerts read all", fn: func() error { return c.MarkAllAlertsRead(ctx, "p1") },
			method: "POST", path: "/alerts/read-all"},
		{name: "skill delete", fn: func() error { return c.DeleteSkill(ctx, "p1", "deploy") },
			method: "DELETE", path: "/skills/deploy"},
		{name: "model default", fn: func() error { return c.SetDefaultModel(ctx, "m1") },
			method: "POST", path: "/models/m1/set-default"},
		{name: "agent delete", fn: func() error { return c.DeleteAgent(ctx, "ag1") },
			method: "DELETE", path: "/agents/ag1"},
		{name: "schedule delete", fn: func() error { return c.DeleteSchedule(ctx, "s1") },
			method: "DELETE", path: "/schedules/s1"},
		{name: "worker limit", fn: func() error { return c.SetGlobalWorkerLimit(ctx, 7) },
			method: "POST", path: "/workers",
			formKey: "max_workers", formValue: "7", checkForm: true},
		{name: "personality", fn: func() error { return c.SavePersonality(ctx, "p1", "concise") },
			method: "POST", path: "/personality/save",
			formKey: "personality", formValue: "concise", checkForm: true},
		{name: "pulse summary", fn: func() error { return c.GeneratePulseSummary(ctx, "p1") },
			method: "POST", path: "/upcoming/summary"},
		{name: "insights analyze", fn: func() error { return c.RunInsightsAnalysis(ctx, "p1") },
			method: "POST", path: "/insights/analyze"},
		{name: "automation run-now", fn: func() error { return c.AutomationAction(ctx, "au1", "run-now", "p1") },
			method: "POST", path: "/automations/au1/run-now"},
		{name: "automation pause", fn: func() error { return c.AutomationAction(ctx, "au1", "pause", "p1") },
			method: "POST", path: "/automations/au1/pause"},
		{name: "automation resume", fn: func() error { return c.AutomationAction(ctx, "au1", "resume", "p1") },
			method: "POST", path: "/automations/au1/resume"},
		{name: "automation delete", fn: func() error { return c.AutomationAction(ctx, "au1", "delete", "p1") },
			method: "POST", path: "/automations/au1/delete"},
		{name: "channel test telegram", fn: func() error { return c.ChannelAction(ctx, "telegram", "test", "p1") },
			method: "POST", path: "/channels/telegram/test"},
		{name: "channel remove telegram", fn: func() error { return c.ChannelAction(ctx, "telegram", "remove", "p1") },
			method: "POST", path: "/channels/telegram/remove"},
		{name: "channel test discord", fn: func() error { return c.ChannelAction(ctx, "discord", "test", "p1") },
			method: "POST", path: "/channels/discord/test"},
		{name: "channel remove discord", fn: func() error { return c.ChannelAction(ctx, "discord", "remove", "p1") },
			method: "POST", path: "/channels/discord/remove"},
		{name: "channel test email", fn: func() error { return c.ChannelAction(ctx, "email", "test", "p1") },
			method: "POST", path: "/channels/email/test"},
		{name: "channel remove email", fn: func() error { return c.ChannelAction(ctx, "email", "remove", "p1") },
			method: "POST", path: "/channels/email/remove"},
		{name: "channel test slack", fn: func() error { return c.ChannelAction(ctx, "slack", "test", "p1") },
			method: "POST", path: "/channels/slack/test"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.fn(); err != nil {
				t.Fatal(err)
			}
			if gotMethod != tc.method || gotPath != tc.path {
				t.Errorf("got %s %s, want %s %s", gotMethod, gotPath, tc.method, tc.path)
			}
			if tc.checkForm && gotForm.Get(tc.formKey) != tc.formValue {
				t.Errorf("%s = %q, want %q", tc.formKey, gotForm.Get(tc.formKey), tc.formValue)
			}
		})
	}
}

func TestSkillMutationsSendBackendJSONContract(t *testing.T) {
	type capturedRequest struct {
		method      string
		path        string
		projectID   string
		contentType string
		accept      string
		hxRequest   string
		body        map[string]any
	}
	var got capturedRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode JSON request body %q: %v", raw, err)
		}
		got = capturedRequest{
			method:      r.Method,
			path:        r.URL.Path,
			projectID:   r.URL.Query().Get("project_id"),
			contentType: r.Header.Get("Content-Type"),
			accept:      r.Header.Get("Accept"),
			hxRequest:   r.Header.Get("HX-Request"),
			body:        body,
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tests := []struct {
		name     string
		fn       func() error
		method   string
		path     string
		wantBody map[string]any
	}{
		{
			name:   "create",
			fn:     func() error { return c.CreateSkill(ctx, "p1", "retry-logic", "wrap retries", "# Retry") },
			method: http.MethodPost,
			path:   "/skills",
			wantBody: map[string]any{
				"handle":      "retry-logic",
				"name":        "retry-logic",
				"description": "wrap retries",
				"scope":       "project",
				"body":        "# Retry",
			},
		},
		{
			name:     "edit",
			fn:       func() error { return c.UpdateSkill(ctx, "p1", "retry-logic", "new body") },
			method:   http.MethodPut,
			path:     "/skills/retry-logic",
			wantBody: map[string]any{"handle": "retry-logic", "scope": "project", "body": "new body"},
		},
		{
			name:     "enable",
			fn:       func() error { return c.SetSkillEnabled(ctx, "p1", "retry-logic", true) },
			method:   http.MethodPost,
			path:     "/skills/retry-logic/enabled",
			wantBody: map[string]any{"enabled": true, "scope": "project"},
		},
		{
			name:     "disable",
			fn:       func() error { return c.SetSkillEnabled(ctx, "p1", "retry-logic", false) },
			method:   http.MethodPost,
			path:     "/skills/retry-logic/enabled",
			wantBody: map[string]any{"enabled": false, "scope": "project"},
		},
		{
			name:     "always use",
			fn:       func() error { return c.SetSkillAlwaysUse(ctx, "p1", "retry-logic", true) },
			method:   http.MethodPost,
			path:     "/skills/retry-logic/always_use",
			wantBody: map[string]any{"always_use": true, "scope": "project"},
		},
		{
			name:     "remove always use",
			fn:       func() error { return c.SetSkillAlwaysUse(ctx, "p1", "retry-logic", false) },
			method:   http.MethodPost,
			path:     "/skills/retry-logic/always_use",
			wantBody: map[string]any{"always_use": false, "scope": "project"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.fn(); err != nil {
				t.Fatal(err)
			}
			if got.method != tc.method || got.path != tc.path || got.projectID != "p1" {
				t.Fatalf("request = %s %s?project_id=%s, want %s %s?project_id=p1", got.method, got.path, got.projectID, tc.method, tc.path)
			}
			if got.contentType != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got.contentType)
			}
			if got.accept != "text/html, application/json" {
				t.Errorf("Accept = %q, want text/html, application/json", got.accept)
			}
			if got.hxRequest != "true" {
				t.Errorf("HX-Request = %q, want true", got.hxRequest)
			}
			if !reflect.DeepEqual(got.body, tc.wantBody) {
				t.Errorf("JSON body = %#v, want %#v", got.body, tc.wantBody)
			}
		})
	}
}

func TestCreateScheduleSendsRepeat(t *testing.T) {
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		if r.URL.Path != "/tasks/t1/schedule" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	if err := c.CreateSchedule(context.Background(), "t1", "2026-01-02T09:00", "daily", 1); err != nil {
		t.Fatal(err)
	}
	if form.Get("run_at") != "2026-01-02T09:00" || form.Get("repeat_type") != "daily" {
		t.Errorf("form = %v", form)
	}
	if form.Get("repeat_interval") != "1" {
		t.Errorf("repeat_interval = %q", form.Get("repeat_interval"))
	}
}

func TestCreateScheduleTranslatesHourlyToHours(t *testing.T) {
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	if err := c.CreateSchedule(context.Background(), "t1", "2026-01-02T09:00", "hourly", 1); err != nil {
		t.Fatal(err)
	}
	if form.Get("repeat_type") != "hours" {
		t.Errorf("repeat_type = %q, want %q", form.Get("repeat_type"), "hours")
	}
	if form.Get("repeat_interval") != "1" {
		t.Errorf("repeat_interval = %q", form.Get("repeat_interval"))
	}
}

func TestCreateScheduleSendsFastRepeatTypes(t *testing.T) {
	cases := []struct {
		repeat   string
		interval int
	}{
		{"seconds", 1},
		{"minutes", 15},
		{"hours", 4},
	}
	for _, tc := range cases {
		t.Run(tc.repeat, func(t *testing.T) {
			var form url.Values
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				form = r.PostForm
				if r.URL.Path != "/tasks/t1/schedule" {
					t.Errorf("path = %s", r.URL.Path)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			c, _ := New(srv.URL)
			if err := c.CreateSchedule(context.Background(), "t1", "2026-01-02T09:00", tc.repeat, tc.interval); err != nil {
				t.Fatal(err)
			}
			if form.Get("repeat_type") != tc.repeat {
				t.Errorf("repeat_type = %q, want %q", form.Get("repeat_type"), tc.repeat)
			}
			if form.Get("repeat_interval") != strconv.Itoa(tc.interval) {
				t.Errorf("repeat_interval = %q, want %d", form.Get("repeat_interval"), tc.interval)
			}
		})
	}
}

func TestCreateScheduleRejectsInvalidRepeatIntervalsBeforeRequest(t *testing.T) {
	cases := []int{0, -5, 366}
	for _, interval := range cases {
		t.Run(strconv.Itoa(interval), func(t *testing.T) {
			var requests int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			c, _ := New(srv.URL)
			err := c.CreateSchedule(context.Background(), "t1", "2026-01-02T09:00", "minutes", interval)
			if err == nil || !strings.Contains(err.Error(), "repeat interval must be between 1 and 365") {
				t.Fatalf("expected interval validation error, got %v", err)
			}
			if requests != 0 {
				t.Fatalf("expected no request for invalid interval, got %d", requests)
			}
		})
	}
}

func TestPageTextPrefersNamedElement(t *testing.T) {
	c := htmlServer(t, `<html><body><nav>skip me</nav>
		<div id="personality-container">friendly and concise</div></body></html>`)
	text, err := c.GetPersonality(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if text != "friendly and concise" {
		t.Errorf("text = %q", text)
	}
}

func TestGetPersonalityFallsBackToWholeDocument(t *testing.T) {
	c := htmlServer(t, `<html><body>no container here</body></html>`)
	text, err := c.GetPersonality(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if text != "no container here" {
		t.Errorf("text = %q", text)
	}
}

func TestGetWorkerSettingsReturnsPageText(t *testing.T) {
	c := htmlServer(t, `<html><body>worker settings text</body></html>`)
	text, err := c.GetWorkerSettings(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if text != "worker settings text" {
		t.Errorf("text = %q", text)
	}
}

func TestGetChannelsReturnsPageText(t *testing.T) {
	c := htmlServer(t, `<html><body>channels text</body></html>`)
	text, err := c.GetChannels(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if text != "channels text" {
		t.Errorf("text = %q", text)
	}
}

// TestChannelActionSlackRemoteTranslatesToDisconnect verifies that "remove" for
// Slack routes to /channels/slack/disconnect (not /channels/slack/remove),
// matching the backend's OAuth disconnect route.
func TestChannelActionSlackRemoveTranslatesToDisconnect(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c, _ := New(srv.URL)

	if err := c.ChannelAction(context.Background(), "slack", "remove", "p1"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/channels/slack/disconnect" {
		t.Errorf("path = %q, want /channels/slack/disconnect", gotPath)
	}
}
