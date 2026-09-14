package terminal

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

// Fixtures with two items each, so the no-arg path must open the interactive
// selector rather than auto-selecting or showing the empty-state hint.
const (
	selTasksHTML = `<div>
  <div class="card" data-task-id="t-1" data-task-status="pending" data-task-category="backlog" data-display-order="0">
    <div class="card-body"><a href="/tasks/t-1?from=tasks" title="Refactor the API">Refactor the API</a></div>
  </div>
  <div class="card" data-task-id="t-2" data-task-status="running" data-task-category="active" data-display-order="1">
    <div class="card-body"><a href="/tasks/t-2?from=tasks" title="Ship the docs">Ship the docs</a></div>
  </div>
</div>`

	selAlertsHTML = `<div>
  <div data-alert-id="a-1" data-alert-scroll-anchor="a-1"><p class="font-semibold">Add retry logic</p></div>
  <div data-alert-id="a-2" data-alert-scroll-anchor="a-2"><p class="font-semibold">Refactor database</p></div>
</div>`

	selSkillsHTML = `<div>
  <div data-skill-handle="retry-logic" data-skill-name="Retry Logic"
       data-skill-enabled="true" data-skill-always-use="false" data-skill-scope="project"></div>
  <div data-skill-handle="rate-limiter" data-skill-name="Rate Limiter"
       data-skill-enabled="true" data-skill-always-use="false" data-skill-scope="project"></div>
</div>`

	selAgentsHTML = `<div>
  <div data-agent-id="ag-1" data-agent-key="reviewer" data-agent-name="Reviewer"
       data-agent-description="reviews code" data-agent-model="claude" data-agent-scope="project"></div>
  <div data-agent-id="ag-2" data-agent-key="builder" data-agent-name="Builder"
       data-agent-description="builds things" data-agent-model="gpt" data-agent-scope="project"></div>
</div>`

	selModelsHTML = `<div>
  <div data-model-id="mo-1" data-model-name="GPT-4o" data-model-provider="openai" data-model-model="gpt-4o"></div>
  <div data-model-id="mo-2" data-model-name="Claude" data-model-provider="anthropic" data-model-model="claude-sonnet"></div>
</div>`

	selAutomationsHTML = `<div>
  <div data-automation-url="/automations/au-1">
    <button type="button" data-automation-card-delete="au-1" data-automation-name="Nightly sweep"></button>
  </div>
  <div data-automation-url="/automations/au-2">
    <button type="button" data-automation-card-delete="au-2" data-automation-name="Weekly report"></button>
  </div>
</div>`

	selScheduleHTML = `<div id="schedule-content">
	  <div data-task-id="t-1" data-schedule-id="s-1">Nightly build — daily 02:00</div>
	  <div data-task-id="t-1" data-schedule-id="s-2">Weekly report — weekly mon</div>
	</div>`

	selPersonalitiesHTML = `<div id="personality-section" data-selected-personality="reviewer">
  <div data-personality-key="reviewer" data-personality-name="Reviewer"
       data-personality-description="reviews code" data-personality-preview="You review code."
       data-personality-is-preset="false" data-personality-has-custom="false"></div>
  <div data-personality-key="builder" data-personality-name="Builder"
       data-personality-description="builds things" data-personality-preview="You build things."
       data-personality-is-preset="false" data-personality-has-custom="false"></div>
</div>`
)

// selFixtures maps backend paths to two-item list fixtures for every area.
func selFixtures() map[string]string {
	return map[string]string{
		"/tasks":       selTasksHTML,
		"/tasks/t-1":   `<div data-task-id="t-1" data-project-id="p1"><h2 class="font-bold">Refactor the API</h2><div data-task-status="pending"></div></div>`,
		"/alerts":      selAlertsHTML,
		"/skills":      selSkillsHTML,
		"/agents":      selAgentsHTML,
		"/models":      selModelsHTML,
		"/automations": selAutomationsHTML,
		"/channels":    webhookCardsHTML,
		"/schedule":    selScheduleHTML,
		"/personality": selPersonalitiesHTML,
	}
}

func TestTasksLifecycleTaskSelectorSelectionRendersOneItemPageWithoutImplicitEvents(t *testing.T) {
	const tasks = `<div>
		<div data-task-id="t-1" data-task-status="completed" data-task-category="completed"><a href="/tasks/t-1" title="Refactor the API">Refactor the API</a></div>
		<div data-task-id="t-2" data-task-status="pending" data-task-category="backlog"><a href="/tasks/t-2" title="Write docs">Write docs</a></div>
	</div>`
	m, rec := dispatchModel(t, map[string]string{
		"/tasks":                              tasks,
		"/api/tasks/t-1/lifecycle-executions": `{"items":[{"id":"exec-1","skill_key":"router","status":"completed","summary":"routing complete"}],"has_more":true,"next_cursor":"older-cursor"}`,
	})
	m = runLine(t, m, "/tasks lifecycle")
	if !m.selectorActive || m.pendingCommand != "tasks lifecycle" {
		t.Fatalf("missing-ref lifecycle did not open the task selector: active=%t pending=%q", m.selectorActive, m.pendingCommand)
	}

	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.selectorActive {
		t.Fatal("task selector remained open after lifecycle selection")
	}
	if !rec.sawQuery("GET /api/tasks/t-1/lifecycle-executions?project_id=p1") {
		t.Fatalf("selected lifecycle request lost project scope:\n%s", rec.all())
	}
	if rec.saw("GET", "/api/lifecycle-executions/exec-1/events") {
		t.Fatalf("task-only selector dispatch implicitly fetched event traces:\n%s", rec.all())
	}
	out := stripANSI(transcript(m))
	for _, want := range []string{"exec-1", "router", "completed", "routing complete", "has more: true", "older-cursor"} {
		if !strings.Contains(out, want) {
			t.Errorf("selected lifecycle page missing %q:\n%s", want, out)
		}
	}
}

// TestNoArgOpensSelectorPerArea is the table-driven per-command-area check:
// every ref-required subcommand invoked with no argument must enter selector
// mode (selectorActive with the right pendingCommand) instead of erroring.
func TestNoArgOpensSelectorPerArea(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want string // expected pendingCommand
	}{
		// tasks
		{"tasks_open", "/tasks open", "tasks open"},
		{"tasks_show", "/tasks show", "tasks show"},
		{"tasks_reviews", "/tasks reviews", "tasks reviews"},
		{"tasks_reviews_add", "/tasks reviews add", "tasks reviews add"},
		{"tasks_edit", "/tasks edit", "tasks edit"},
		{"tasks_run", "/tasks run", "tasks run"},
		{"tasks_stop", "/tasks stop", "tasks stop"},
		{"tasks_delete", "/tasks delete", "tasks delete"},
		{"tasks_move", "/tasks move", "tasks move"},
		{"tasks_order", "/tasks order", "tasks order"},
		{"tasks_goal", "/tasks goal", "tasks goal"},
		{"tasks_goal_pause", "/tasks goal pause", "tasks goal pause"},
		{"tasks_goal_resume", "/tasks goal resume", "tasks goal resume"},
		{"tasks_reply", "/tasks reply", "tasks reply"},
		{"tasks_lifecycle", "/tasks lifecycle", "tasks lifecycle"},
		{"tasks_logs", "/tasks logs", "tasks logs"},
		// alerts
		{"alerts_read", "/alerts read", "alerts read"},
		{"alerts_approve", "/alerts approve", "alerts approve"},
		{"alerts_reject", "/alerts reject", "alerts reject"},
		{"alerts_dismiss", "/alerts dismiss", "alerts dismiss"},
		{"alerts_delete", "/alerts delete", "alerts delete"},
		// skills
		{"skills_show", "/skills show", "skills show"},
		{"skills_edit", "/skills edit", "skills edit"},
		{"skills_delete", "/skills delete", "skills delete"},
		{"skills_enable", "/skills enable", "skills enable"},
		{"skills_disable", "/skills disable", "skills disable"},
		{"skills_always", "/skills always", "skills always"},
		// agents
		{"agents_edit", "/agents edit", "agents edit"},
		{"agents_delete", "/agents delete", "agents delete"},
		// models
		{"models_edit", "/models edit", "models edit"},
		{"models_default", "/models default", "models default"},
		{"models_delete", "/models delete", "models delete"},
		// automations
		{"automations_show", "/automations show", "automations show"},
		{"automations_open", "/automations open", "automations open"},
		{"automations_edit", "/automations edit", "automations edit"},
		{"automations_run", "/automations run", "automations run"},
		{"automations_pause", "/automations pause", "automations pause"},
		{"automations_resume", "/automations resume", "automations resume"},
		{"automations_delete", "/automations delete", "automations delete"},
		// channels
		{"channels_show", "/channels show", "channels show"},
		{"channels_add", "/channels add", "channels add"},
		{"channels_connect", "/channels connect", "channels connect"},
		{"channels_edit", "/channels edit", "channels edit"},
		{"channels_test", "/channels test", "channels test"},
		{"channels_remove", "/channels remove", "channels remove"},
		{"channels_disconnect", "/channels disconnect", "channels disconnect"},
		{"channels_webhooks_show", "/channels webhooks show", "channels webhooks show"},
		{"channels_webhooks_edit", "/channels webhooks edit", "channels webhooks edit"},
		{"channels_webhooks_test", "/channels webhooks test", "channels webhooks test"},
		{"channels_webhooks_rotate", "/channels webhooks rotate", "channels webhooks rotate"},
		{"channels_webhooks_delete", "/channels webhooks delete", "channels webhooks delete"},
		// inbound webhooks
		{"webhooks_show", "/webhooks show", "channels webhooks show"},
		{"webhooks_edit", "/webhooks edit", "channels webhooks edit"},
		{"webhooks_test", "/webhooks test", "channels webhooks test"},
		{"webhooks_rotate", "/webhooks rotate", "channels webhooks rotate"},
		{"webhooks_delete", "/webhooks delete", "channels webhooks delete"},
		// schedule
		{"schedule_show", "/schedule show", "schedule show"},
		{"schedule_open", "/schedule open", "schedule open"},
		{"schedule_add", "/schedule add", "schedule add"}, {"schedule_edit", "/schedule edit", "schedule edit"},
		{"schedule_delete", "/schedule delete", "schedule delete"},
		{"schedule_toggle", "/schedule toggle", "schedule toggle"},
		// personality
		{"personality_show", "/personality show", "personality show"},
		{"personality_edit", "/personality edit", "personality edit"},
		{"personality_set", "/personality set", "personality set"},
		{"personality_delete", "/personality delete", "personality delete"},
		// projects
		{"projects_delete", "/projects delete", "projects delete"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			m, _ := dispatchModel(t, selFixtures())
			if tc.want == "projects delete" {
				m.projects = []client.Project{{ID: "p1", Name: "Project one"}, {ID: "p2", Name: "Project two"}}
			}
			m = runLine(t, m, tc.cmd)
			if !m.selectorActive {
				t.Fatalf("expected selector mode for %s, transcript:\n%s", tc.cmd, transcript(m))
			}
			if m.pendingCommand != tc.want {
				t.Errorf("pendingCommand = %q, want %q", m.pendingCommand, tc.want)
			}
			if len(m.selectorItems) < 2 {
				t.Errorf("selector should list the fixture items, got %d", len(m.selectorItems))
			}
			if strings.Contains(strings.ToLower(transcript(m)), "usage") {
				t.Errorf("must not show a usage error:\n%s", transcript(m))
			}
		})
	}
}

func TestOmittedTypedOperandsOpenRegistryOptionSelectors(t *testing.T) {
	cases := []struct {
		name       string
		line       string
		pending    string
		wantValues []string
	}{
		{name: "worker numeric", line: "/workers limit", pending: "workers limit", wantValues: []string{"0", "1", "2", "4"}},
		{name: "task enum", line: "/tasks move t-1", pending: "tasks move t-1", wantValues: []string{"backlog", "active", "completed"}},
		{name: "destructive enum", line: "/tasks clear", pending: "tasks clear", wantValues: []string{"backlog", "completed"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := dispatchModel(t, selFixtures())
			m = runLine(t, m, tc.line)
			if !m.selectorActive || m.pendingCommand != tc.pending {
				t.Fatalf("selector state = active:%v pending:%q\n%s", m.selectorActive, m.pendingCommand, transcript(m))
			}
			var got []string
			for _, item := range m.selectorItems {
				got = append(got, item.ref)
			}
			for _, want := range tc.wantValues {
				if !containsString(got, want) {
					t.Fatalf("options = %v, missing %q", got, want)
				}
			}
			if m.pendingConfirmation != nil {
				t.Fatal("confirmation opened before an option was selected")
			}
		})
	}
}

func TestStructurallyBoundedPartialOperandsOpenFilteredOptionPickers(t *testing.T) {
	cases := []struct {
		name        string
		line        string
		wantPending string
		wantFilter  string
		wantOption  string
	}{
		{
			name:        "task detail through root alias",
			line:        `/task show "Refactor the API" rev`,
			wantPending: `tasks show "Refactor the API"`,
			wantFilter:  "rev",
			wantOption:  "review",
		},
		{
			name:        "schedule repeat through root alias",
			line:        `/schedules add "Refactor the API" 2026-01-20T09:00 mon`,
			wantPending: `schedule add "Refactor the API" 2026-01-20T09:00`,
			wantFilter:  "mon",
			wantOption:  "monthly",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := dispatchModel(t, selFixtures())
			m = runLine(t, m, tc.line)
			if !m.selectorActive || m.pendingCommand != tc.wantPending {
				t.Fatalf("selector state = active:%v pending:%q\n%s", m.selectorActive, m.pendingCommand, transcript(m))
			}
			if m.selectorFilter != tc.wantFilter {
				t.Fatalf("selector filter = %q, want %q", m.selectorFilter, tc.wantFilter)
			}
			items := m.filteredSelectorItems()
			if len(items) != 1 || items[0].ref != tc.wantOption {
				t.Fatalf("filtered options = %+v, want %q", items, tc.wantOption)
			}
		})
	}
}

func TestExactStructurallyBoundedOperandsDispatchNormally(t *testing.T) {
	for _, line := range []string{
		`/tasks show "Refactor the API" review`,
		`/tasks move "Refactor the API" active`,
		`/schedule add "Refactor the API" 2026-01-20T09:00 monthly`,
	} {
		t.Run(line, func(t *testing.T) {
			m, _ := dispatchModel(t, selFixtures())
			next, cmd := m.runCommand(line)
			got := next.(Model)
			if cmd == nil {
				t.Fatal("valid exact operand did not dispatch")
			}
			if got.selectorActive {
				t.Fatal("valid exact operand reopened an option picker")
			}
		})
	}
}

func TestAmbiguousUnquotedPartialOperandsDoNotOpenOptionPickers(t *testing.T) {
	for _, line := range []string{
		`/tasks show Fix rev`,
		`/schedule add Daily report mon`,
		`/schedule add Daily report not-a-date mon`,
	} {
		t.Run(line, func(t *testing.T) {
			m, _ := dispatchModel(t, selFixtures())
			m = runLine(t, m, line)
			if m.selectorActive && m.selectorTitle == "Options" {
				t.Fatalf("ambiguous input opened an operand option picker: pending=%q", m.pendingCommand)
			}
		})
	}
}

func TestPartialTaskMoveOptionSelectionReplacesEnumPrefix(t *testing.T) {
	m, rec := dispatchModel(t, selFixtures())
	m = runLine(t, m, "/tasks move t-1 ac")
	if !m.selectorActive || m.pendingCommand != "tasks move t-1" {
		t.Fatalf("selector state = active:%v pending:%q\n%s", m.selectorActive, m.pendingCommand, transcript(m))
	}

	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyDown}) // active
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !rec.saw("PATCH", "/tasks/t-1/category") || !rec.sawForm("category=active") {
		t.Fatalf("selected option was not dispatched as a replacement:\nrequests:\n%s\nforms: %v", rec.all(), rec.forms)
	}
	if strings.Contains(rec.all(), "ac active") {
		t.Fatalf("partial option was appended instead of replaced:\n%s", rec.all())
	}
}

func TestNonEnumTaskTitleWordIsPreservedWhenSelectingMoveOption(t *testing.T) {
	m, _ := dispatchModel(t, selFixtures())
	m = runLine(t, m, "/tasks move Fix login")
	if !m.selectorActive || m.pendingCommand != "tasks move Fix login" {
		t.Fatalf("selector state = active:%v pending:%q\n%s", m.selectorActive, m.pendingCommand, transcript(m))
	}
}

func TestWorksAliasOpensWorkerLimitOptions(t *testing.T) {
	m, _ := dispatchModel(t, selFixtures())
	m = runLine(t, m, "/works limit")
	if !m.selectorActive || m.pendingCommand != "workers limit" {
		t.Fatalf("selector state = active:%v pending:%q\n%s", m.selectorActive, m.pendingCommand, transcript(m))
	}
}

func TestAttachmentAliasesOfferNestedResourceSelectorsOnTab(t *testing.T) {
	for _, alias := range []string{"attach", "attachment"} {
		t.Run(alias, func(t *testing.T) {
			m, _ := dispatchModel(t, selFixtures())
			m.input.SetValue("/tasks " + alias + " add ")
			m.input.CursorEnd()
			m.refreshMenu()

			next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
			m = next.(Model)
			if cmd == nil {
				t.Fatalf("Tab did not request a task selector for %q", alias)
			}
			next, _ = m.Update(cmd())
			m = next.(Model)
			if !m.selectorActive || m.pendingCommand != "tasks attachments add" {
				t.Fatalf("selector state = active:%v pending:%q\n%s", m.selectorActive, m.pendingCommand, transcript(m))
			}
		})
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestTabOnOmittedResourceOpensRegistrySelector(t *testing.T) {
	cases := []struct {
		name           string
		line           string
		pendingCommand string
	}{
		{name: "model default", line: "/models default ", pendingCommand: "models default"},
		{name: "canonical automation run", line: "/automations run ", pendingCommand: "automations run"},
		{name: "legacy automation run-now", line: "/automations run-now ", pendingCommand: "automations run"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := dispatchModel(t, selFixtures())
			m.input.SetValue(tc.line)
			m.input.CursorEnd()
			m.refreshMenu()

			next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
			m = next.(Model)
			if cmd == nil {
				t.Fatalf("Tab did not request the resource selector for %q", tc.line)
			}
			next, _ = m.Update(cmd())
			m = next.(Model)
			if !m.selectorActive || m.pendingCommand != tc.pendingCommand {
				t.Fatalf("selector state = active:%v pending:%q, want pending %q\n%s", m.selectorActive, m.pendingCommand, tc.pendingCommand, transcript(m))
			}
		})
	}
}

func TestTabOnPartialResourceOpensFilteredSelectorWithoutMutation(t *testing.T) {
	m, rec := dispatchModel(t, selFixtures())
	m.input.SetValue("/models default clau")
	m.input.CursorEnd()
	m.refreshMenu()

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("Tab did not request the filtered model selector")
	}
	next, _ = m.Update(cmd())
	m = next.(Model)
	if !m.selectorActive || m.selectorFilter != "clau" || len(m.filteredSelectorItems()) != 1 {
		t.Fatalf("filtered selector = active:%v filter:%q items:%d\n%s", m.selectorActive, m.selectorFilter, len(m.filteredSelectorItems()), transcript(m))
	}
	if rec.count(http.MethodPost, "/models/default") != 0 {
		t.Fatalf("Tab completion mutated the model selection:\n%s", rec.all())
	}
}

func TestDestructiveOptionSelectionStillRequiresConfirmation(t *testing.T) {
	m, rec := dispatchModel(t, selFixtures())
	m = runLine(t, m, "/tasks clear")
	if !m.selectorActive {
		t.Fatalf("expected task-column selector:\n%s", transcript(m))
	}
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.pendingConfirmation == nil {
		t.Fatalf("selecting a destructive option bypassed confirmation:\n%s", transcript(m))
	}
	if rec.count(http.MethodDelete, "/tasks/clear") != 0 {
		t.Fatalf("clear request occurred before confirmation:\n%s", rec.all())
	}
}

func TestWebhookShowSelectorDispatchesResolvedItemWithoutSecondList(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{
		"/channels":             webhookCardsHTML,
		"/channels/webhooks/w1": webhookDetailJSON,
	})
	m = runLine(t, m, "/channels webhooks show")
	if !m.selectorActive {
		t.Fatalf("expected webhook selector:\n%s", transcript(m))
	}
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.selectorActive {
		t.Fatal("selector remained open after selecting a webhook")
	}
	if rec.count(http.MethodGet, "/channels") != 1 || rec.count(http.MethodGet, "/channels/webhooks/w1") != 1 {
		t.Fatalf("selector dispatch repeated webhook discovery:\n%s", rec.all())
	}
	if !rec.sawQuery("GET /channels/webhooks/w1?project_id=p1") {
		t.Fatalf("selector detail request lost project scope:\n%s", rec.all())
	}
}

func TestAutomationEditSelectorOpensInteractiveEditor(t *testing.T) {
	builder := `<div id="automation-builder"><form id="automation-design-form" action="/automations/au-1/builder?project_id=p1"></form><textarea name="automation_yaml">schema_version: 1
name: Nightly sweep
</textarea></div>`
	m, rec := dispatchModel(t, map[string]string{"/automations": selAutomationsHTML, "/automations/au-1/builder": builder})
	m = runLine(t, m, "/automations edit")
	if !m.selectorActive {
		t.Fatalf("expected automation selector:\n%s", transcript(m))
	}
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.automationEditActive || m.selectorActive {
		t.Fatalf("selection did not open editor: editor=%v selector=%v", m.automationEditActive, m.selectorActive)
	}
	if rec.count(http.MethodGet, "/automations") != 1 || rec.count(http.MethodGet, "/automations/au-1/builder") != 1 || rec.count(http.MethodPost, "/automations/au-1/builder") != 0 {
		t.Fatalf("selector request sequence is unsafe: %s", rec.all())
	}
}

func TestAutomationShowSelectorDispatchesResolvedItemWithoutSecondList(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{
		"/automations":      selAutomationsHTML,
		"/automations/au-1": automationDetailHTML("au-1", "p1", "Nightly sweep"),
	})
	m = runLine(t, m, "/automations show")
	if !m.selectorActive {
		t.Fatalf("expected automation selector:\n%s", transcript(m))
	}
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.selectorActive {
		t.Fatal("selector remained open after selecting an automation")
	}
	if rec.count("GET", "/automations") != 1 || rec.count("GET", "/automations/au-1") != 1 {
		t.Fatalf("selector dispatch requests =\n%s", rec.all())
	}
	if !rec.sawQuery("GET /automations/au-1?project_id=p1") {
		t.Fatalf("selector detail request lost project scope:\n%s", rec.all())
	}
	if !strings.Contains(transcript(m), "Automation: Nightly sweep") {
		t.Fatalf("selector detail output missing:\n%s", transcript(m))
	}
}

func TestPersonalitySelectorSetUsesResolvedItemWithoutSecondCatalogLookup(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{
		"/personality": selPersonalitiesHTML,
	})
	m = runLine(t, m, "/personality set")
	if !m.selectorActive {
		t.Fatalf("expected personality selector:\n%s", transcript(m))
	}
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.selectorActive {
		t.Fatal("selector remained open after selecting a personality")
	}
	if got := rec.count(http.MethodGet, "/personality"); got != 1 {
		t.Fatalf("selector set made %d catalog requests, want exactly one:\n%s", got, rec.all())
	}
	if got := rec.count(http.MethodPost, "/personality/save"); got != 1 {
		t.Fatalf("selector set made %d save requests, want exactly one:\n%s", got, rec.all())
	}
	if !rec.sawQuery("POST /personality/save?project_id=p1") {
		t.Fatalf("selector set lost project scope:\n%s", rec.all())
	}
	if !rec.sawForm("POST /personality/save?personality=reviewer") {
		t.Fatalf("selector set did not use the canonical key:\n%s", rec.all())
	}
}

func TestPersonalitySelectorsExcludeBaseForEditAndDelete(t *testing.T) {
	const personalitiesHTML = `<div id="personality-section" data-selected-personality="">
		<div data-personality-key="" data-personality-name="Base" data-personality-description="standard"
			data-personality-is-preset="true" data-personality-has-custom="false"></div>
		<div data-personality-key="custom" data-personality-name="Custom" data-personality-description="custom"
			data-personality-is-preset="false" data-personality-has-custom="true"></div>
		<div data-personality-key="reviewer" data-personality-name="Reviewer" data-personality-description="reviews"
			data-personality-is-preset="false" data-personality-has-custom="false"></div>
	</div>`
	for _, action := range []string{"edit", "delete"} {
		t.Run(action, func(t *testing.T) {
			m, rec := dispatchModel(t, map[string]string{"/personality": personalitiesHTML})
			m = runLine(t, m, "/personality "+action)
			if !m.selectorActive {
				t.Fatalf("expected %s selector:\n%s", action, transcript(m))
			}
			if len(m.selectorItems) != 2 {
				t.Fatalf("%s selector items = %d, want two custom items: %+v", action, len(m.selectorItems), m.selectorItems)
			}
			for i, wantRef := range []string{"custom", "reviewer"} {
				if item := m.selectorItems[i]; item.ref != wantRef || item.label == "Base" {
					t.Fatalf("%s selector item %d = %+v, want non-Base personality %q", action, i, item, wantRef)
				}
			}
			if rec.count(http.MethodGet, "/personality") != 1 {
				t.Fatalf("%s selector made unexpected catalog requests:\n%s", action, rec.all())
			}
		})
	}
}

func TestPersonalitySelectorRendersKinds(t *testing.T) {
	m, _ := dispatchModel(t, map[string]string{
		"/personality": `<div id="personality-section" data-selected-personality="override">
  <div data-personality-key="custom" data-personality-name="Custom"
       data-personality-description="custom description" data-personality-is-preset="false" data-personality-has-custom="true"></div>
  <div data-personality-key="override" data-personality-name="Override"
       data-personality-description="override description" data-personality-is-preset="true" data-personality-has-custom="true"></div>
  <div data-personality-key="builtin" data-personality-name="Built-in"
       data-personality-description="built-in description" data-personality-is-preset="true" data-personality-has-custom="false"></div>
</div>`,
	})
	m = runLine(t, m, "/personality show")
	if !m.selectorActive {
		t.Fatalf("expected personality selector:\n%s", transcript(m))
	}

	want := []string{
		"custom · custom description",
		"override · override description",
		"built-in · built-in description",
	}
	if len(m.selectorItems) != len(want) {
		t.Fatalf("selector items = %d, want %d: %+v", len(m.selectorItems), len(want), m.selectorItems)
	}
	for i, item := range m.selectorItems {
		if item.detail != want[i] {
			t.Errorf("selector item %d detail = %q, want %q", i, item.detail, want[i])
		}
	}
}

// TestAutomationsWithoutProjectSkipsSelector verifies that the automation
// command guard runs before selector resolution when no project is selected.
func TestAutomationsWithoutProjectSkipsSelector(t *testing.T) {
	cases := []string{
		"/automations",
		"/automations show au-1",
		"/automations open au-1",
		"/automations run au-1",
		"/automations run-now au-1",
		"/automations pause au-1",
		"/automations resume au-1",
		"/automations delete au-1",
		"/automations delete",
	}
	for _, line := range cases {
		line := line
		t.Run(line, func(t *testing.T) {
			m, rec := dispatchModel(t, selFixtures())
			m.selectedID = ""
			m.selectedName = ""

			m = runLine(t, m, line)
			out := transcript(m)
			if !strings.Contains(out, "no project selected") {
				t.Fatalf("expected no-project error for %s:\n%s", line, out)
			}
			if calls := rec.all(); calls != "" {
				t.Fatalf("%s must not make backend requests:\n%s", line, calls)
			}
			if m.selectorActive {
				t.Errorf("%s must not open the selector", line)
			}
			if m.pendingConfirmation != nil {
				t.Errorf("%s must not set pending confirmation", line)
			}
		})
	}
}

// TestProjectNoArgOpensSelector verifies /project with no argument opens a
// project picker when more than one project is known, and that choosing one
// switches the active project.
func TestProjectNoArgOpensSelector(t *testing.T) {
	m, _ := dispatchModel(t, nil)
	m.projects = []client.Project{
		{ID: "p1", Name: "demo"},
		{ID: "p2", Name: "beta"},
	}
	m.projectsLoaded = true
	m = runLine(t, m, "/project")
	if !m.selectorActive {
		t.Fatalf("expected the project selector:\n%s", transcript(m))
	}
	if m.pendingCommand != "project" {
		t.Errorf("pendingCommand = %q, want %q", m.pendingCommand, "project")
	}
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.selectedID != "p2" || m.selectedName != "beta" {
		t.Errorf("selected = %s/%s, want p2/beta", m.selectedID, m.selectedName)
	}
}

func TestProjectPickerRowsMatchAcrossEntryPoints(t *testing.T) {
	projects := []client.Project{
		{ID: "project-first", Name: "First Project", Path: "/workspace/projects/first/with/a/very/long/path"},
		{ID: "project-later", Name: "Later Project", Path: "/workspace/projects/later"},
		{ID: "project-third", Name: "Third Project", Path: "/workspace/projects/third"},
	}
	wantItems := []selectorItem{
		{ref: projects[0].ID, label: projects[0].Name, detail: truncate(projects[0].Path, 40)},
		{ref: projects[1].ID, label: projects[1].Name, detail: truncate(projects[1].Path, 40)},
		{ref: projects[2].ID, label: projects[2].Name, detail: truncate(projects[2].Path, 40)},
	}
	cases := []struct {
		name           string
		line           string
		wantCommand    string
		wantPrefill    bool
		wantPrefillSfx string
	}{
		{name: "project", line: "/project", wantCommand: "project"},
		{name: "show", line: "/projects show", wantCommand: "projects show"},
		{name: "edit", line: "/projects edit", wantCommand: "projects edit", wantPrefill: true, wantPrefillSfx: " "},
		{name: "delete", line: "/projects delete", wantCommand: "projects delete"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			m, _ := dispatchModel(t, nil)
			m.projects = projects
			m.projectsLoaded = true

			m, cmd := typeLine(t, m, tc.line)
			if cmd == nil {
				t.Fatal("picker command is nil")
			}
			pickerMsg := cmd()
			msg, ok := pickerMsg.(selectorActiveMsg)
			if !ok {
				t.Fatalf("picker command returned %T, want selectorActiveMsg", pickerMsg)
			}
			if msg.command != tc.wantCommand {
				t.Errorf("command = %q, want %q", msg.command, tc.wantCommand)
			}
			if msg.emptyHint != "no projects" {
				t.Errorf("empty hint = %q, want %q", msg.emptyHint, "no projects")
			}
			if msg.prefill != tc.wantPrefill || msg.prefillSuffix != tc.wantPrefillSfx {
				t.Errorf("prefill = %t/%q, want %t/%q", msg.prefill, msg.prefillSuffix, tc.wantPrefill, tc.wantPrefillSfx)
			}
			if !reflect.DeepEqual(msg.items, wantItems) {
				t.Fatalf("picker rows = %+v, want %+v", msg.items, wantItems)
			}

			next, followUp := m.Update(msg)
			m = next.(Model)
			if followUp != nil || !m.selectorActive {
				t.Fatalf("selector state = active:%t follow-up:%v", m.selectorActive, followUp != nil)
			}
			if m.pendingCommand != tc.wantCommand {
				t.Errorf("pending command = %q, want %q", m.pendingCommand, tc.wantCommand)
			}
			if !reflect.DeepEqual(m.selectorItems, wantItems) {
				t.Errorf("active picker rows = %+v, want %+v", m.selectorItems, wantItems)
			}
		})
	}
}

func TestProjectPickerFilterDispatchesOriginalLaterProjectID(t *testing.T) {
	m, _ := dispatchModel(t, nil)
	defer m.Cleanup()
	m.projects = []client.Project{
		{ID: "project-first", Name: "First Project", Path: "/workspace/first"},
		{ID: "project-middle", Name: "Middle Project", Path: "/workspace/middle"},
		{ID: "project-later", Name: "Later Project", Path: "/workspace/later"},
	}
	m.projectsLoaded = true
	m = runLine(t, m, "/project")
	if !m.selectorActive {
		t.Fatalf("expected project selector:\n%s", transcript(m))
	}

	m = typeSelectorRunes(t, m, "later")
	if got := m.selectorFilteredCount(); got != 1 {
		t.Fatalf("filtered project count = %d, want 1", got)
	}
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.selectedID != "project-later" || m.selectedName != "Later Project" {
		t.Fatalf("selected project = %q/%q, want project-later/Later Project", m.selectedID, m.selectedName)
	}
}

func TestProjectPickerDeleteConfirmsBeforeMutation(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{
		"/api/projects": `{"projects":[]}`,
	})
	defer m.Cleanup()
	m.projects = []client.Project{
		{ID: "project-first", Name: "First Project"},
		{ID: "project-later", Name: "Later Project"},
	}
	m.projectsLoaded = true
	m = runLine(t, m, "/projects delete")
	if !m.selectorActive {
		t.Fatalf("expected project delete selector:\n%s", transcript(m))
	}
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.pendingConfirmation == nil {
		t.Fatal("project picker delete did not open confirmation")
	}
	if got := rec.count(http.MethodDelete, "/projects/project-later"); got != 0 {
		t.Fatalf("project delete ran before confirmation: %d requests", got)
	}

	m = runLine(t, m, "yes")
	if got := rec.count(http.MethodDelete, "/projects/project-later"); got != 1 {
		t.Fatalf("confirmed project delete requests = %d, want 1\n%s", got, rec.all())
	}
}

func TestProjectPickersRemainNonInteractiveInCLI(t *testing.T) {
	oldCLI, oldForce := cliMode, forceMode
	cliMode = true
	forceMode = false
	defer func() {
		cliMode, forceMode = oldCLI, oldForce
	}()

	cases := []struct {
		name       string
		line       string
		wantOutput string
	}{
		{name: "project", line: "/project", wantOutput: "Projects"},
		{name: "show", line: "/projects show", wantOutput: "usage"},
		{name: "edit", line: "/projects edit", wantOutput: "usage"},
		{name: "delete", line: "/projects delete", wantOutput: "usage"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, nil)
			m.projects = []client.Project{
				{ID: "project-first", Name: "First Project"},
				{ID: "project-later", Name: "Later Project"},
			}
			m.projectsLoaded = true
			m = runLine(t, m, tc.line)
			if m.selectorActive || m.pendingCommand != "" || m.pendingConfirmation != nil {
				t.Fatalf("CLI picker state = active:%t pending:%q confirmation:%v", m.selectorActive, m.pendingCommand, m.pendingConfirmation != nil)
			}
			if out := strings.ToLower(transcript(m)); !strings.Contains(out, strings.ToLower(tc.wantOutput)) {
				t.Fatalf("CLI output missing %q:\n%s", tc.wantOutput, transcript(m))
			}
			if calls := rec.all(); calls != "" {
				t.Fatalf("CLI picker made backend requests:\n%s", calls)
			}
		})
	}
}

// key helpers for driving the selector.
func selKey(t *testing.T, m Model, key tea.KeyMsg) Model {
	t.Helper()
	next, cmd := m.Update(key)
	m = next.(Model)
	// follow one level of command chaining (dispatch after Enter)
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			break
		}
		if _, ok := msg.(tea.BatchMsg); ok {
			break
		}
		next, follow := m.Update(msg)
		m = next.(Model)
		cmd = follow
	}
	return m
}

func typeSelectorRunes(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		m = selKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

func TestSelectorFilterPreservesOrderAndMatchCounts(t *testing.T) {
	items := []selectorItem{
		{ref: "task-1", label: "Alpha task", detail: "ordinary"},
		{ref: "task-2", label: "Beta task", detail: "Needle in detail"},
		{ref: "NEEDLE-ref", label: "Gamma task", detail: "ordinary"},
		{ref: "task-4", label: "Delta", detail: "ordinary"},
	}
	cases := []struct {
		filter string
		refs   []string
	}{
		{filter: "absent", refs: []string{}},
		{filter: "alpha", refs: []string{"task-1"}},
		{filter: "needle", refs: []string{"task-2", "NEEDLE-ref"}},
		{filter: "TASK", refs: []string{"task-1", "task-2", "NEEDLE-ref", "task-4"}},
	}

	for _, tc := range cases {
		t.Run(tc.filter, func(t *testing.T) {
			m, _ := dispatchModel(t, nil)
			updated, _ := m.handleSelector(selectorActiveMsg{
				title:       "Tasks",
				command:     "tasks open",
				forcePicker: true,
				items:       items,
			})
			m = updated.(Model).setSelectorFilter(tc.filter)
			gotItems := m.filteredSelectorItems()
			got := make([]string, 0, len(gotItems))
			for _, item := range gotItems {
				got = append(got, item.ref)
			}
			if !reflect.DeepEqual(got, tc.refs) {
				t.Fatalf("filter %q refs = %v, want %v", tc.filter, got, tc.refs)
			}
			if gotCount := m.selectorFilteredCount(); gotCount != len(tc.refs) {
				t.Fatalf("filter %q count = %d, want %d", tc.filter, gotCount, len(tc.refs))
			}
		})
	}
}

func TestSelectorLaterMatchCursorAndEnterSelection(t *testing.T) {
	selected := ""
	items := make([]selectorItem, 12)
	for i := range items {
		ref := fmt.Sprintf("task-%02d", i)
		items[i] = selectorItem{
			ref:   ref,
			label: fmt.Sprintf("Matching task %02d", i),
			dispatch: func(m Model) (Model, tea.Cmd) {
				selected = ref
				return m, nil
			},
		}
	}
	m, _ := dispatchModel(t, nil)
	updated, _ := m.handleSelector(selectorActiveMsg{
		title:       "Tasks",
		command:     "tasks open",
		forcePicker: true,
		items:       items,
	})
	m = updated.(Model)
	for i := 0; i < 9; i++ {
		m = selKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	}
	if m.selectorCursor != 9 || m.selectorFilteredCount() != len(items) {
		t.Fatalf("cursor/count = %d/%d, want 9/%d", m.selectorCursor, m.selectorFilteredCount(), len(items))
	}
	if view := stripANSI(m.renderSelector()); !strings.Contains(view, "showing 8 of 12") || !strings.Contains(view, "Matching task 09") {
		t.Fatalf("later cursor was not rendered correctly:\n%s", view)
	}
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if selected != "task-09" {
		t.Fatalf("Enter selected %q, want task-09", selected)
	}
}

func TestSelectorResolvedTaskIdentitySurvivesFilteredSelection(t *testing.T) {
	tasks := make([]*client.Task, 12)
	items := make([]selectorItem, 12)
	for i := range items {
		ref := fmt.Sprintf("task-%02d", i)
		tasks[i] = &client.Task{ID: ref, Title: fmt.Sprintf("Matching task %02d", i)}
		items[i] = selectorItem{
			ref:          ref,
			label:        tasks[i].Title,
			resolvedTask: tasks[i],
		}
	}
	m, _ := dispatchModel(t, nil)
	updated, _ := m.handleSelector(selectorActiveMsg{
		title:         "Tasks",
		command:       "tasks reviews add",
		prefill:       true,
		prefillSuffix: " | ",
		forcePicker:   true,
		items:         items,
	})
	m = updated.(Model)
	m = m.setSelectorFilter("matching task 0")
	if got := m.selectorFilteredCount(); got != 10 {
		t.Fatalf("filtered match count = %d, want 10", got)
	}
	for i := 0; i < 9; i++ {
		m = selKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.selectorActive {
		t.Fatal("selector remained active after selecting a task")
	}
	if m.reviewPrefillTask != tasks[9] {
		t.Fatalf("resolved task pointer = %p, want %p", m.reviewPrefillTask, tasks[9])
	}
	if got, want := m.input.Value(), "/tasks reviews add task-09 | "; got != want {
		t.Fatalf("review prefill = %q, want %q", got, want)
	}
}

func TestSelectorItemReplacementInvalidatesFilterCache(t *testing.T) {
	oldItems := []selectorItem{
		{ref: "old-1", label: "Old one"},
		{ref: "old-2", label: "Old two"},
	}
	newItems := []selectorItem{
		{ref: "new-1", label: "New one"},
		{ref: "new-2", label: "New two"},
	}
	m, _ := dispatchModel(t, nil)
	updated, _ := m.handleSelector(selectorActiveMsg{title: "Items", command: "items open", forcePicker: true, items: oldItems})
	m = updated.(Model).setSelectorFilter("old")
	if got := m.filteredSelectorItems(); len(got) != 2 {
		t.Fatalf("old filter count = %d, want 2", len(got))
	}

	oldSearch := m.selectorSearch
	oldItemsBacking := m.selectorItems

	updated, _ = m.handleSelector(selectorActiveMsg{title: "Items", command: "items open", forcePicker: true, items: newItems})
	m = updated.(Model)
	if m.selectorSearch == nil || &m.selectorSearch[0] == &oldSearch[0] {
		t.Fatal("replacement retained the old selector search backing array")
	}
	if len(m.selectorItems) == 0 || &m.selectorItems[0] == &oldItemsBacking[0] {
		t.Fatal("replacement retained the old selector item backing array")
	}
	if m.selectorFilter != "" || m.selectorFilteredFor != "" || m.selectorMatchIndexes != nil {
		t.Fatalf("replacement retained filter cache: filter=%q cached=%q indexes=%v", m.selectorFilter, m.selectorFilteredFor, m.selectorMatchIndexes)
	}
	got := m.filteredSelectorItems()
	if len(got) != 2 || got[0].ref != "new-1" || got[1].ref != "new-2" {
		t.Fatalf("replacement returned stale items: %+v", got)
	}
	m = m.setSelectorFilter("new")
	if got := m.filteredSelectorItems(); len(got) != 2 || got[0].ref != "new-1" || got[1].ref != "new-2" {
		t.Fatalf("replacement filter returned stale items: %+v", got)
	}
}

func TestSelectorRepeatedFilterChangesKeepBoundedMatchCache(t *testing.T) {
	m := benchmarkSelectorFilterModel(10000)
	filters := []string{"s", "sy", "synthetic", "synthetic task", "group-099", "not-present", ""}
	for cycle := 0; cycle < 25; cycle++ {
		for _, filter := range filters {
			m = m.setSelectorFilter(filter)
			if cap(m.selectorMatchIndexes) > len(m.selectorItems) {
				t.Fatalf("filter %q grew match cache beyond item count: len=%d cap=%d", filter, len(m.selectorMatchIndexes), cap(m.selectorMatchIndexes))
			}
			for _, index := range m.selectorMatchIndexes {
				if index < 0 || index >= len(m.selectorItems) {
					t.Fatalf("filter %q retained invalid match index %d", filter, index)
				}
			}
		}
	}
	m = m.clearSelector()
	if m.selectorItems != nil || m.selectorSearch != nil || m.selectorMatchIndexes != nil || m.selectorFilteredFor != "" {
		t.Fatalf("clear retained selector state: items=%d search=%d indexes=%d cached=%q",
			len(m.selectorItems), len(m.selectorSearch), len(m.selectorMatchIndexes), m.selectorFilteredFor)
	}
}

func TestSelectorRepeatedFilterChangesDoNotGrowHeap(t *testing.T) {
	m := benchmarkSelectorFilterModel(10000)
	filters := []string{"s", "sy", "synthetic", "synthetic task", "group-099", "not-present", ""}
	for _, filter := range filters {
		m = m.setSelectorFilter(filter)
	}

	runtime.GC()
	runtime.KeepAlive(m)
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	for cycle := 0; cycle < 100; cycle++ {
		for _, filter := range filters {
			m = m.setSelectorFilter(filter)
		}
	}

	runtime.GC()
	runtime.KeepAlive(m)
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	const maxRetainedHeapGrowth = 4 << 20
	if growth := int64(after.HeapAlloc) - int64(before.HeapAlloc); growth > maxRetainedHeapGrowth {
		t.Fatalf("repeated filter changes retained %d bytes, want no more than %d", growth, maxRetainedHeapGrowth)
	}
}

// TestSelectorFilterNarrowsItems verifies incremental text filtering.
func TestSelectorFilterNarrowsItems(t *testing.T) {
	m, _ := dispatchModel(t, selFixtures())
	m = runLine(t, m, "/tasks run")
	if !m.selectorActive {
		t.Fatalf("selector not active:\n%s", transcript(m))
	}
	if got := len(m.filteredSelectorItems()); got != 2 {
		t.Fatalf("unfiltered items = %d, want 2", got)
	}
	m = typeSelectorRunes(t, m, "docs")
	items := m.filteredSelectorItems()
	if len(items) != 1 || items[0].ref != "t-2" {
		t.Fatalf("filter 'docs' should leave only t-2, got %+v", items)
	}
	// Backspace widens again.
	for range "docs" {
		m = selKey(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	if got := len(m.filteredSelectorItems()); got != 2 {
		t.Errorf("after backspace items = %d, want 2", got)
	}
}

func TestSelectorInitialAndTypedFilteringEquivalent(t *testing.T) {
	items := []selectorItem{
		{ref: "task-label", label: "Mixed CASE Label", detail: "unrelated"},
		{ref: "task-detail", label: "unrelated", detail: "Mixed Case Detail"},
		{ref: "MIXED CASE ref", label: "unrelated", detail: "unrelated"},
		{ref: "task-no-match", label: "other", detail: "other"},
	}

	for _, filter := range []string{"mIxEd CaSe", "label", "detail", "case ref", "absent"} {
		t.Run(filter, func(t *testing.T) {
			initialModel, _ := dispatchModel(t, nil)
			updated, _ := initialModel.handleSelector(selectorActiveMsg{
				title:         "Tasks",
				command:       "tasks open",
				initialFilter: filter,
				forcePicker:   true,
				items:         items,
			})
			initial := updated.(Model)

			typedModel, _ := dispatchModel(t, nil)
			updated, _ = typedModel.handleSelector(selectorActiveMsg{
				title:       "Tasks",
				command:     "tasks open",
				forcePicker: true,
				items:       items,
			})
			typed := updated.(Model).setSelectorFilter(filter)

			if got, want := initial.filteredSelectorItems(), typed.filteredSelectorItems(); !reflect.DeepEqual(got, want) {
				t.Fatalf("initial filter results differ from typed filter:\ninitial: %+v\ntyped:   %+v", got, want)
			}
			if initial.selectorFilter != filter || initial.selectorFilteredFor != filter {
				t.Fatalf("initial filter cache = %q/%q, want %q/%q",
					initial.selectorFilter, initial.selectorFilteredFor, filter, filter)
			}
		})
	}
}

func TestSelectorEmptyInitialFilterPreservesOriginalSlice(t *testing.T) {
	items := []selectorItem{
		{ref: "first", label: "First"},
		{ref: "second", label: "Second"},
	}
	m, _ := dispatchModel(t, nil)
	updated, _ := m.handleSelector(selectorActiveMsg{
		title:       "Tasks",
		command:     "tasks open",
		forcePicker: true,
		items:       items,
	})
	m = updated.(Model)

	filtered := m.filteredSelectorItems()
	if len(filtered) != len(items) || &filtered[0] != &items[0] {
		t.Fatalf("empty initial filter did not preserve the original item slice")
	}
	if m.selectorFilter != "" || m.selectorFilteredFor != "" {
		t.Fatalf("empty initial filter cache = %q/%q, want empty", m.selectorFilter, m.selectorFilteredFor)
	}
}

func TestSelectorInitialFilterAutoSelectHonorsForcePicker(t *testing.T) {
	items := []selectorItem{
		{ref: "task-1", label: "First task", detail: "backlog"},
		{ref: "task-2", label: "Second task", detail: "active"},
	}
	msg := selectorActiveMsg{
		title:         "Tasks",
		command:       "tasks edit",
		prefill:       true,
		prefillSuffix: " | ",
		initialFilter: "ACTIVE",
		items:         items,
		warnings:      []string{"partial task list"},
	}

	m, _ := dispatchModel(t, nil)
	updated, cmd := m.handleSelector(msg)
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("prefill auto-selection returned an unexpected command")
	}
	if m.selectorActive {
		t.Fatal("unique initial match should auto-select")
	}
	if got, want := m.input.Value(), "/tasks edit task-2 | "; got != want {
		t.Fatalf("auto-selection prefill = %q, want %q", got, want)
	}
	if out := transcript(m); !strings.Contains(out, "partial task list") || !strings.Contains(out, "only one match") {
		t.Fatalf("auto-selection did not preserve warnings and selection output:\n%s", out)
	}

	forced, _ := dispatchModel(t, nil)
	msg.forcePicker = true
	updated, cmd = forced.handleSelector(msg)
	forced = updated.(Model)
	if cmd != nil {
		t.Fatal("forced picker returned an unexpected command")
	}
	if !forced.selectorActive {
		t.Fatal("forcePicker should keep a unique initial match in the picker")
	}
	if got := forced.filteredSelectorItems(); len(got) != 1 || got[0].ref != "task-2" {
		t.Fatalf("forced picker matches = %+v, want task-2", got)
	}
	if forced.pendingCommand != msg.command || !forced.selectorPrefill || forced.selectorPrefillSuffix != msg.prefillSuffix || forced.selectorCursor != 0 {
		t.Fatalf("forced picker state changed: command=%q prefill=%v suffix=%q cursor=%d",
			forced.pendingCommand, forced.selectorPrefill, forced.selectorPrefillSuffix, forced.selectorCursor)
	}
	if !reflect.DeepEqual(forced.selectorWarnings, msg.warnings) {
		t.Fatalf("forced picker warnings = %v, want %v", forced.selectorWarnings, msg.warnings)
	}
}

func TestSelectorCachedAndFallbackFilteringEquivalent(t *testing.T) {
	items := []selectorItem{
		{ref: "task-label", label: "Mixed CASE Label", detail: "unrelated"},
		{ref: "task-detail", label: "unrelated", detail: "Mixed Case Detail"},
		{ref: "MIXED CASE ref", label: "unrelated", detail: "unrelated"},
		{ref: "task-no-match", label: "other", detail: "other"},
	}
	m := Model{
		selectorItems:  items,
		selectorSearch: selectorSearchTexts(items),
	}
	m = m.setSelectorFilter("mIxEd CaSe")
	cached := m.filteredSelectorItems()

	m.selectorFilteredFor = "stale"
	fallback := m.filteredSelectorItems()

	if !reflect.DeepEqual(cached, fallback) {
		t.Fatalf("cached selector results differ from fallback:\ncached:   %+v\nfallback: %+v", cached, fallback)
	}
	if !reflect.DeepEqual(cached, items[:3]) {
		t.Fatalf("matching items = %+v, want %+v", cached, items[:3])
	}
}

// TestSelectorFilterSpaceTypesExactlyOneSpace verifies that pressing the spacebar
// while typing a filter appends a single space, not two. Multi-word resource
// names (e.g. "Refactor the API") must be findable when the user types a space
// in their search term.
func TestSelectorFilterSpaceTypesExactlyOneSpace(t *testing.T) {
	m, _ := dispatchModel(t, selFixtures())
	m = runLine(t, m, "/tasks run")
	if !m.selectorActive {
		t.Fatalf("selector not active:\n%s", transcript(m))
	}

	// Type "the" + space + "api" to match "Refactor the API" but not "Ship the docs".
	m = typeSelectorRunes(t, m, "the")
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = typeSelectorRunes(t, m, "api")

	// The filter must be exactly "the api" (one space, not two).
	if m.selectorFilter != "the api" {
		t.Errorf("selectorFilter = %q, want %q", m.selectorFilter, "the api")
	}

	items := m.filteredSelectorItems()
	if len(items) != 1 {
		t.Fatalf("expected 1 filtered item after 'the api', got %d: %+v", len(items), items)
	}
	if items[0].ref != "t-1" {
		t.Errorf("filtered item ref = %q, want %q", items[0].ref, "t-1")
	}
}

// TestSelectorEnterDispatchesPendingCommand verifies that choosing an item
// re-dispatches "/<pendingCommand> <ref>" exactly as if typed.
func TestSelectorEnterDispatchesPendingCommand(t *testing.T) {
	m, rec := dispatchModel(t, selFixtures())
	m = runLine(t, m, "/tasks run")
	if !m.selectorActive {
		t.Fatalf("selector not active:\n%s", transcript(m))
	}
	// Move to the second item and confirm.
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.selectorActive {
		t.Error("selector should close after Enter")
	}
	if !rec.saw("POST", "/tasks/t-2/run") {
		t.Errorf("expected run call for the chosen task, calls:\n%s", rec.all())
	}
	if !strings.Contains(transcript(m), "/tasks run t-2") {
		t.Errorf("dispatched line missing from transcript:\n%s", transcript(m))
	}
}

// TestSelectorEscCancels verifies Esc closes the picker with a cancelled note
// and no backend mutation.
func TestSelectorEscCancels(t *testing.T) {
	m, rec := dispatchModel(t, selFixtures())
	m = runLine(t, m, "/tasks delete")
	if !m.selectorActive {
		t.Fatalf("selector not active:\n%s", transcript(m))
	}
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.selectorActive {
		t.Error("selector should close on Esc")
	}
	if !strings.Contains(transcript(m), "cancelled") {
		t.Errorf("expected a cancelled note:\n%s", transcript(m))
	}
	rec.mu.Lock()
	for _, c := range rec.calls {
		if strings.HasPrefix(c, "DELETE ") {
			t.Errorf("no delete expected after cancel, saw %s", c)
		}
	}
	rec.mu.Unlock()
}

// TestPickerActionsUseSelectedResourceWithoutResolutionFetch verifies that
// picker selections execute against the resource parsed for the selected row.
// Most actions retain an initial list fetch and post-action refresh. Alert delete
// parses its refreshed list directly from the DELETE response instead.
func TestPickerActionsUseSelectedResourceWithoutResolutionFetch(t *testing.T) {
	cases := []struct {
		name          string
		command       string
		method        string
		path          string
		listPath      string
		destructive   bool
		wantOutput    string
		wantListCalls int
	}{
		{
			name:       "alerts read",
			command:    "/alerts read",
			method:     "POST",
			path:       "/alerts/a-1/read",
			listPath:   "/alerts",
			wantOutput: "read: Add retry logic",
		},
		{
			name:       "alerts approve",
			command:    "/alerts approve",
			method:     "POST",
			path:       "/alerts/a-1/approve",
			listPath:   "/alerts",
			wantOutput: "approve: Add retry logic",
		},
		{
			name:       "alerts reject",
			command:    "/alerts reject",
			method:     "POST",
			path:       "/alerts/a-1/reject",
			listPath:   "/alerts",
			wantOutput: "reject: Add retry logic",
		},
		{
			name:       "alerts dismiss",
			command:    "/alerts dismiss",
			method:     "POST",
			path:       "/alerts/a-1/dismiss",
			listPath:   "/alerts",
			wantOutput: "dismiss: Add retry logic",
		},
		{
			name:          "alerts delete",
			command:       "/alerts delete",
			method:        "DELETE",
			path:          "/alerts/a-1",
			listPath:      "/alerts",
			destructive:   true,
			wantOutput:    "delete: Add retry logic",
			wantListCalls: 1,
		},
		{
			name:       "schedule toggle",
			command:    "/schedule toggle",
			method:     "POST",
			path:       "/api/schedules/s-1/toggle",
			listPath:   "/schedule",
			wantOutput: "toggled schedule",
		},
		{
			name:        "schedule delete",
			command:     "/schedule delete",
			method:      "DELETE",
			path:        "/schedules/s-1",
			listPath:    "/schedule",
			destructive: true,
			wantOutput:  "deleted schedule",
		},
		{
			name:        "agents delete",
			command:     "/agents delete",
			method:      "DELETE",
			path:        "/agents/ag-1",
			listPath:    "/agents",
			destructive: true,
			wantOutput:  "deleted Reviewer",
		},
		{
			name:       "models default",
			command:    "/models default",
			method:     "POST",
			path:       "/models/mo-1/set-default",
			listPath:   "/models",
			wantOutput: "default: GPT-4o",
		},
		{
			name:        "models delete",
			command:     "/models delete",
			method:      "DELETE",
			path:        "/models/mo-1",
			listPath:    "/models",
			destructive: true,
			wantOutput:  "delete: GPT-4o",
		},
		{
			name:       "automations run",
			command:    "/automations run",
			method:     "POST",
			path:       "/automations/au-1/run-now",
			listPath:   "/automations",
			wantOutput: "run: Nightly sweep",
		},
		{
			name:       "automations pause",
			command:    "/automations pause",
			method:     "POST",
			path:       "/automations/au-1/pause",
			listPath:   "/automations",
			wantOutput: "pause: Nightly sweep",
		},
		{
			name:       "automations resume",
			command:    "/automations resume",
			method:     "POST",
			path:       "/automations/au-1/resume",
			listPath:   "/automations",
			wantOutput: "resume: Nightly sweep",
		},
		{
			name:        "automations delete",
			command:     "/automations delete",
			method:      "POST",
			path:        "/automations/au-1/delete",
			listPath:    "/automations",
			destructive: true,
			wantOutput:  "delete: Nightly sweep",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, selFixtures())
			m = runLine(t, m, tc.command)
			if !m.selectorActive {
				t.Fatalf("expected selector for %s, transcript:\n%s", tc.command, transcript(m))
			}

			m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
			if tc.destructive {
				if m.pendingConfirmation == nil {
					t.Fatalf("expected confirmation for %s, transcript:\n%s", tc.command, transcript(m))
				}
				m = runLine(t, m, "yes")
			}

			if !rec.saw(tc.method, tc.path) {
				t.Errorf("expected selected resource action %s %s, calls:\n%s", tc.method, tc.path, rec.all())
			}
			wantListCalls := tc.wantListCalls
			if wantListCalls == 0 {
				wantListCalls = 2
			}
			if got := selectorCallCount(rec, "GET", tc.listPath); got != wantListCalls {
				t.Errorf("%s list calls = %d, want %d; calls:\n%s", tc.command, got, wantListCalls, rec.all())
			}
			if out := transcript(m); strings.Contains(out, "error:") {
				t.Fatalf("unexpected error after %s:\n%s", tc.command, out)
			} else if !strings.Contains(out, tc.wantOutput) {
				t.Errorf("output after %s missing %q:\n%s", tc.command, tc.wantOutput, out)
			}
		})
	}
}

func TestAlertDeletePickerCancellation(t *testing.T) {
	m, rec := dispatchModel(t, selFixtures())
	m = runLine(t, m, "/alerts delete")
	if !m.selectorActive {
		t.Fatalf("delete picker did not open:\n%s", transcript(m))
	}

	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.selectorActive || m.pendingConfirmation == nil {
		t.Fatalf("delete selection state = active:%t confirmation:%v\n%s", m.selectorActive, m.pendingConfirmation != nil, transcript(m))
	}
	if prompt := m.pendingConfirmation.message; !strings.Contains(prompt, `Delete alert "Add retry logic"?`) {
		t.Fatalf("delete confirmation prompt = %q", prompt)
	}

	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.pendingConfirmation != nil {
		t.Fatalf("delete confirmation remained after Esc:\n%s", transcript(m))
	}
	if rec.saw(http.MethodDelete, "/alerts/a-1") {
		t.Fatalf("cancelled delete made a request:\n%s", rec.all())
	}
	if !strings.Contains(transcript(m), "cancelled") {
		t.Fatalf("delete cancellation was not reported:\n%s", transcript(m))
	}
}

func TestAlertPickerAndTypedActionsHaveEquivalentResolvedOutput(t *testing.T) {
	const deleteResponse = `<div data-alert-id="a-remaining" data-alert-scroll-anchor="a-remaining"><p class="font-semibold">Remaining alert</p></div>`

	actions := []struct {
		action string
		method string
		path   string
	}{
		{action: "read", method: http.MethodPost, path: "/alerts/a-1/read"},
		{action: "approve", method: http.MethodPost, path: "/alerts/a-1/approve"},
		{action: "reject", method: http.MethodPost, path: "/alerts/a-1/reject"},
		{action: "dismiss", method: http.MethodPost, path: "/alerts/a-1/dismiss"},
		{action: "delete", method: http.MethodDelete, path: "/alerts/a-1"},
	}

	for _, action := range actions {
		action := action
		t.Run(action.action, func(t *testing.T) {
			runAction := func(t *testing.T, picker bool) (string, *recorder) {
				t.Helper()
				m, rec := dispatchModel(t, map[string]string{
					"/alerts":            selAlertsHTML,
					"DELETE /alerts/a-1": deleteResponse,
				})
				command := "/alerts " + action.action
				if picker {
					m = runLine(t, m, command)
					m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
					if action.action == "delete" {
						m = runLine(t, m, "yes")
					}
				} else if action.action == "delete" {
					m = confirmDestructive(t, m, command+" a-1")
				} else {
					m = runLine(t, m, command+" a-1")
				}
				if !rec.saw(action.method, action.path) {
					t.Fatalf("%s path missing from %s dispatch:\n%s", action.path, map[bool]string{true: "picker", false: "typed"}[picker], rec.all())
				}
				if !rec.sawQuery(action.method + " " + action.path + "?project_id=p1") {
					t.Fatalf("%s action lost project scope:\n%s", map[bool]string{true: "picker", false: "typed"}[picker], strings.Join(rec.urlsSnapshot(), "\n"))
				}
				if len(m.log) == 0 {
					t.Fatal("alert action produced no result")
				}
				return stripANSI(m.log[len(m.log)-1].text), rec
			}

			typedOutput, _ := runAction(t, false)
			pickerOutput, pickerRec := runAction(t, true)
			if typedOutput != pickerOutput {
				t.Fatalf("typed and picker output differ:\ntyped:\n%s\npicker:\n%s", typedOutput, pickerOutput)
			}
			if !strings.Contains(typedOutput, action.action+": Add retry logic") {
				t.Fatalf("action status missing from output:\n%s", typedOutput)
			}
			if action.action == "delete" {
				if !strings.Contains(typedOutput, "Remaining alert") {
					t.Fatalf("delete did not render its DELETE response:\n%s", typedOutput)
				}
				if got := selectorCallCount(pickerRec, http.MethodGet, "/alerts"); got != 1 {
					t.Fatalf("picker delete list calls = %d, want only its initial selection fetch:\n%s", got, pickerRec.all())
				}
			} else if got := selectorCallCount(pickerRec, http.MethodGet, "/alerts"); got != 2 {
				t.Fatalf("picker %s list calls = %d, want selection and refresh only:\n%s", action.action, got, pickerRec.all())
			}
		})
	}
}

func TestAlertSelectorItemsShareRowsAndCaptureAlerts(t *testing.T) {
	alerts := []client.Alert{
		{ID: "title-id", Title: "Title label", Message: "message label", Text: "text label", Badges: []string{"custom", "pending"}},
		{ID: "message-id", Message: "Message label", Text: "text fallback", Badges: []string{"approved"}},
		{ID: "text-id", Text: "Text label", Badges: []string{"rejected", "unclaimed"}},
		{ID: "123456789", Badges: []string{"dismissed", "completed"}},
	}
	wantRows := []selectorItem{
		{ref: "title-id", label: "Title label", detail: "custom pending"},
		{ref: "message-id", label: "Message label", detail: "approved"},
		{ref: "text-id", label: "Text label", detail: "rejected unclaimed"},
		{ref: "123456789", label: "12345678", detail: "dismissed completed"},
	}

	captured := func() (*[]string, func(client.Alert) selectorItemDispatch) {
		ids := make([]string, 0, len(alerts))
		return &ids, func(alert client.Alert) selectorItemDispatch {
			return func(m Model) (Model, tea.Cmd) {
				ids = append(ids, alert.ID)
				return m, nil
			}
		}
	}
	showIDs, showDispatch := captured()
	actionIDs, actionDispatch := captured()
	showItems := alertSelectorItems(alerts, showDispatch)
	actionItems := alertSelectorItems(alerts, actionDispatch)

	if len(showItems) != len(wantRows) || len(actionItems) != len(wantRows) {
		t.Fatalf("selector row counts = show:%d action:%d, want %d", len(showItems), len(actionItems), len(wantRows))
	}
	for i, want := range wantRows {
		for name, items := range map[string][]selectorItem{"show": showItems, "action": actionItems} {
			if items[i].ref != want.ref || items[i].label != want.label || items[i].detail != want.detail {
				t.Errorf("%s row %d = {%q, %q, %q}, want {%q, %q, %q}", name, i,
					items[i].ref, items[i].label, items[i].detail, want.ref, want.label, want.detail)
			}
			if items[i].dispatch == nil {
				t.Errorf("%s row %d has no dispatch callback", name, i)
			}
		}
	}

	for i := len(showItems) - 1; i >= 0; i-- {
		if _, cmd := showItems[i].dispatch(Model{}); cmd != nil {
			t.Fatalf("show row %d returned unexpected command", i)
		}
		if _, cmd := actionItems[i].dispatch(Model{}); cmd != nil {
			t.Fatalf("action row %d returned unexpected command", i)
		}
	}
	wantCaptured := []string{"123456789", "text-id", "message-id", "title-id"}
	if !reflect.DeepEqual(*showIDs, wantCaptured) || !reflect.DeepEqual(*actionIDs, wantCaptured) {
		t.Fatalf("captured IDs = show:%v action:%v, want %v", *showIDs, *actionIDs, wantCaptured)
	}
}

func TestAlertShowPickerLoadsDetailWithoutMutation(t *testing.T) {
	const alertsHTML = `<div data-alert-id="a-first" data-alert-scroll-anchor="a-first"><p class="font-semibold">First alert</p></div>
		<div data-alert-id="a-second" data-alert-scroll-anchor="a-second"><p class="font-semibold">Second alert</p></div>`
	const detailHTML = `<div data-alert-detail-loaded><div data-alert-markdown data-raw-content="Second alert full detail"></div></div>`
	m, rec := dispatchModel(t, map[string]string{
		"/alerts":                  alertsHTML,
		"/alerts/a-second/details": detailHTML,
	})

	m = runLine(t, m, "/alerts show")
	if !m.selectorActive || m.pendingCommand != "alerts show" {
		t.Fatalf("show picker state = active:%t command:%q\n%s", m.selectorActive, m.pendingCommand, transcript(m))
	}
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.selectorActive || m.pendingCommand != "" {
		t.Fatalf("show picker remained active after selection: active:%t command:%q", m.selectorActive, m.pendingCommand)
	}
	if !rec.saw(http.MethodGet, "/alerts/a-second/details") {
		t.Fatalf("selected alert detail was not loaded:\n%s", rec.all())
	}
	if got := selectorCallCount(rec, http.MethodGet, "/alerts"); got != 1 {
		t.Fatalf("show picker list calls = %d, want 1:\n%s", got, rec.all())
	}
	if rec.saw(http.MethodPost, "/alerts/a-second/read") || rec.saw(http.MethodPost, "/alerts/a-second/approve") || rec.saw(http.MethodDelete, "/alerts/a-second") {
		t.Fatalf("show picker performed a mutation:\n%s", rec.all())
	}
	if out := stripANSI(transcript(m)); !strings.Contains(out, "Second alert full detail") {
		t.Fatalf("show picker omitted selected detail:\n%s", out)
	}
}

func TestAlertPickerLifecycleForEmptySingleMultiAndFilteredLists(t *testing.T) {
	const oneAlert = `<div data-alert-id="a-one" data-alert-scroll-anchor="a-one"><p class="font-semibold">Only alert</p></div>`

	t.Run("empty", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/alerts": `<div></div>`})
		m = runLine(t, m, "/alerts read")
		if m.selectorActive || m.pendingCommand != "" {
			t.Fatalf("empty picker state = active:%t command:%q", m.selectorActive, m.pendingCommand)
		}
		if rec.saw(http.MethodPost, "/alerts/a-one/read") || !strings.Contains(stripANSI(transcript(m)), "no pending alerts in the current project") {
			t.Fatalf("empty picker output or mutation incorrect:\n%s", transcript(m))
		}
	})

	t.Run("single auto-selects", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{
			"/alerts":                 oneAlert,
			"POST /alerts/a-one/read": ``,
		})
		m = runLineWithFollowUp(t, m, "/alerts read")
		if m.selectorActive || m.pendingCommand != "" || !rec.saw(http.MethodPost, "/alerts/a-one/read") {
			t.Fatalf("single picker did not dispatch: active:%t command:%q calls:\n%s", m.selectorActive, m.pendingCommand, rec.all())
		}
	})

	t.Run("multi cancels without mutation", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/alerts": selAlertsHTML})
		m = runLine(t, m, "/alerts read")
		if !m.selectorActive || len(m.selectorItems) != 2 {
			t.Fatalf("multi picker state = active:%t items:%d", m.selectorActive, len(m.selectorItems))
		}
		m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
		if m.selectorActive || rec.saw(http.MethodPost, "/alerts/a-1/read") {
			t.Fatalf("cancelled multi picker mutated or remained active:\n%s", rec.all())
		}
	})

	t.Run("filter selects matching row", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{"/alerts": selAlertsHTML})
		m = runLine(t, m, "/alerts approve")
		m = typeSelectorRunes(t, m, "refactor")
		items := m.filteredSelectorItems()
		if len(items) != 1 || items[0].ref != "a-2" {
			t.Fatalf("filtered rows = %+v, want only a-2", items)
		}
		m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
		if !rec.saw(http.MethodPost, "/alerts/a-2/approve") {
			t.Fatalf("filtered selection used wrong action target:\n%s", rec.all())
		}
	})
}

func TestAlertPickerLoadsLaterPageInSourceOrder(t *testing.T) {
	var requests int
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/alerts" {
			http.NotFound(w, r)
			return
		}
		requests++
		if r.URL.Query().Get("project_id") != "p1" {
			t.Errorf("picker request lost project scope: %s", r.URL.RequestURI())
		}
		w.Header().Set("Content-Type", "text/html")
		if requests == 1 {
			w.Header().Set("X-OpenVibely-Card-Page-Has-More", "true")
			_, _ = io.WriteString(w, `<div data-card-pagination-root data-card-pagination-card-selector="[data-alert-id]" data-card-pagination-key="data-alert-id" data-card-pagination-has-more="true"><div data-alert-id="a-first" data-alert-scroll-anchor="a-first"><p class="font-semibold">First alert</p></div></div>`)
			return
		}
		if r.URL.Query().Get("card_page") != "1" || r.URL.Query().Get("offset") != "1" {
			t.Errorf("later-page request = %s", r.URL.RequestURI())
		}
		w.Header().Set("X-OpenVibely-Card-Page-Has-More", "false")
		_, _ = io.WriteString(w, `<div data-card-pagination-root data-card-pagination-card-selector="[data-alert-id]" data-card-pagination-key="data-alert-id" data-card-pagination-has-more="false"><div data-alert-id="a-later" data-alert-scroll-anchor="a-later"><p class="font-semibold">Later alert</p></div></div>`)
	})
	m.selectedID, m.selectedName = "p1", "demo"
	m = runLine(t, m, "/alerts approve")
	if !m.selectorActive || len(m.selectorItems) != 2 || m.selectorItems[0].ref != "a-first" || m.selectorItems[1].ref != "a-later" {
		t.Fatalf("later-page picker rows = %+v", m.selectorItems)
	}
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.selectorActive || requests != 2 {
		t.Fatalf("later-page picker lifecycle = active:%t requests:%d", m.selectorActive, requests)
	}
}

func TestScheduleShowPickerSelectionAndCancellation(t *testing.T) {
	m, rec := dispatchModel(t, selFixtures())
	m = runLine(t, m, "/schedule open")
	if !m.selectorActive || m.pendingCommand != "schedule open" {
		t.Fatalf("expected schedule open selector: active=%t command=%q\n%s", m.selectorActive, m.pendingCommand, transcript(m))
	}
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	out := stripANSI(transcript(m))
	for _, want := range []string{"Weekly report", "Schedule ID: s-2", "Task ID: t-1", "/tasks open t-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("selected schedule output missing %q:\n%s", want, out)
		}
	}
	if got := selectorCallCount(rec, http.MethodGet, "/schedule"); got != 1 {
		t.Errorf("picker schedule GETs = %d, want 1; calls:\n%s", got, rec.all())
	}

	m, rec = dispatchModel(t, selFixtures())
	m = runLine(t, m, "/schedule show")
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.selectorActive {
		t.Fatal("escape did not cancel schedule selector")
	}
	if rec.saw(http.MethodGet, "/tasks") {
		t.Fatalf("cancelled selector looked up bound task; calls:\n%s", rec.all())
	}
	if out := stripANSI(transcript(m)); !strings.Contains(out, "cancelled") {
		t.Fatalf("cancel output missing:\n%s", out)
	}
}

func TestScheduleSelectorListsBothIDsAndTargetsSecondSchedule(t *testing.T) {
	cases := []struct {
		action string
		method string
		path   string
	}{
		{action: "toggle", method: http.MethodPost, path: "/api/schedules/s-2/toggle"},
		{action: "delete", method: http.MethodDelete, path: "/schedules/s-2"},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			m, rec := dispatchModel(t, selFixtures())
			m = runLine(t, m, "/schedule "+tc.action)
			if !m.selectorActive {
				t.Fatalf("expected schedule selector:\n%s", transcript(m))
			}
			if len(m.selectorItems) != 2 {
				t.Fatalf("selector items = %d, want 2: %+v", len(m.selectorItems), m.selectorItems)
			}
			if got := []string{m.selectorItems[0].ref, m.selectorItems[1].ref}; !reflect.DeepEqual(got, []string{"s-1", "s-2"}) {
				t.Fatalf("selector refs = %v, want [s-1 s-2]", got)
			}

			m = selKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
			m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
			if tc.action == "delete" {
				if m.pendingConfirmation == nil {
					t.Fatalf("expected delete confirmation:\n%s", transcript(m))
				}
				m = runLine(t, m, "yes")
			}
			if !rec.saw(tc.method, tc.path) {
				t.Fatalf("expected second schedule action %s %s, calls:\n%s", tc.method, tc.path, rec.all())
			}
		})
	}
}

func TestScheduleListRendersBothSchedulesForOneTask(t *testing.T) {
	m, _ := dispatchModel(t, selFixtures())
	m = runLine(t, m, "/schedule")
	out := stripANSI(transcript(m))
	for _, want := range []string{"s-1", "s-2", "Nightly build", "Weekly report"} {
		if !strings.Contains(out, want) {
			t.Errorf("schedule output missing %q:\n%s", want, out)
		}
	}
}

func selectorCallCount(rec *recorder, method, path string) int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	want := method + " " + path
	count := 0
	for _, call := range rec.calls {
		if call == want {
			count++
		}
	}
	return count
}

// TestSelectorSingleItemAutoSelects verifies a one-item list skips the picker
// and proceeds with a note.
func TestSelectorSingleItemAutoSelects(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{"/tasks": taskBoardHTML}) // one task
	// Drain fully: the selector msg chains into the auto-dispatched command.
	m2, cmd := typeLine(t, m, "/tasks run")
	m = m2
	for i := 0; cmd != nil && i < 5; i++ {
		msg := cmd()
		if msg == nil {
			break
		}
		if _, ok := msg.(tea.BatchMsg); ok {
			break
		}
		next, follow := m.Update(msg)
		m = next.(Model)
		cmd = follow
	}
	if m.selectorActive {
		t.Error("selector must not open for a single-item list")
	}
	out := transcript(m)
	if !strings.Contains(out, "only one match") {
		t.Errorf("expected auto-select note:\n%s", out)
	}
	if !rec.saw("POST", "/tasks/t-1/run") {
		t.Errorf("expected the run call, calls:\n%s", rec.all())
	}
}

func TestSelectorSingleItemReviewAddPrefills(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{
		"/tasks": taskBoardHTML,
	})
	m = runLine(t, m, "/tasks reviews add")
	if m.selectorActive {
		t.Fatal("single task should auto-select instead of opening the picker")
	}
	if got, want := m.input.Value(), "/tasks reviews add t-1 "; got != want {
		t.Fatalf("prefilled input = %q, want %q", got, want)
	}
	if got := rec.count("POST", "/tasks/t-1/reviews"); got != 0 {
		t.Fatalf("auto-selection must not submit before operands, got %d POSTs", got)
	}
}

// TestSelectorEmptyListShowsHint verifies an empty backend list renders the
// existing empty-state hint instead of a selector.
func TestSelectorEmptyListShowsHint(t *testing.T) {
	m, _ := dispatchModel(t, nil)
	m = runLine(t, m, "/skills show")
	if m.selectorActive {
		t.Error("selector must not open for an empty list")
	}
	if !strings.Contains(transcript(m), "no skills yet") {
		t.Errorf("expected the empty-state hint:\n%s", transcript(m))
	}
}

// TestResourceEmptyStateHintsMatchPageAndSelectors keeps the actionable empty
// state shown by each page identical to the hint emitted by its ref-less
// selector. The automation cases cover both selector branches.
func TestResourceEmptyStateHintsMatchPageAndSelectors(t *testing.T) {
	cases := []struct {
		name    string
		command string
		page    string
		bodies  map[string]string
	}{
		{
			name:    "tasks",
			command: "/tasks run",
			page:    stripANSI(renderBoard(nil, "")),
		},
		{
			name:    "attachments",
			command: "/tasks attachments delete Refactor",
			page:    stripANSI(renderTaskAttachments(nil)),
			bodies: map[string]string{
				"/tasks":     taskBoardHTML,
				"/tasks/t-1": `<div id="attachment-list" data-project-id="p1"></div>`,
			},
		},
		{
			name:    "schedule",
			command: "/schedule delete",
			page:    stripANSI(renderSchedule(nil, "")),
		},
		{
			name:    "skills",
			command: "/skills show",
			page:    stripANSI(renderSkills(nil, "")),
		},
		{
			name:    "automations show",
			command: "/automations show",
			page:    stripANSI(renderAutomations(nil, "")),
		},
		{
			name:    "automations mutation",
			command: "/automations pause",
			page:    stripANSI(renderAutomations(nil, "")),
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			m, _ := dispatchModel(t, tc.bodies)
			m = runLine(t, m, tc.command)
			if m.selectorActive {
				t.Fatalf("empty selector should not remain active:\n%s", transcript(m))
			}
			if got := stripANSI(transcript(m)); !strings.Contains(got, tc.page) {
				t.Fatalf("selector empty-state guidance does not match page output\npage: %q\nselector transcript: %q", tc.page, got)
			}
		})
	}
}

// TestSelectorPrefillPrimesInput verifies that piped commands (edit/goal/
// reply) prime the input with "/<command> <ref> | " instead of dispatching.
func TestSelectorPrefillPrimesInput(t *testing.T) {
	m, rec := dispatchModel(t, selFixtures())
	m = runLine(t, m, "/tasks edit")
	if !m.selectorActive {
		t.Fatalf("selector not active:\n%s", transcript(m))
	}
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.selectorActive {
		t.Error("selector should close after Enter")
	}
	if got := m.input.Value(); got != "/tasks edit t-1 | " {
		t.Errorf("input = %q, want %q", got, "/tasks edit t-1 | ")
	}
	rec.mu.Lock()
	for _, c := range rec.calls {
		if strings.HasPrefix(c, "PUT ") || strings.HasPrefix(c, "POST ") {
			t.Errorf("prefill must not mutate, saw %s", c)
		}
	}
	rec.mu.Unlock()
}

func TestTaskSelectorCompactMetadataSurvivesPrefill(t *testing.T) {
	const board = `<div data-task-id="t-1" data-task-status="pending" data-task-category="backlog" data-display-order="4">
		<a href="/tasks/t-1" title="Refactor the API">Refactor the API</a>
		<p class="line-clamp-2">Preserve this prompt for edit.</p>
		<span class="badge">Goal</span><span class="badge">Sonnet</span>
	</div>
	<div data-task-id="t-2" data-task-status="running" data-task-category="active">
		<a href="/tasks/t-2" title="Ship the docs">Ship the docs</a>
	</div>`
	m, rec := dispatchModel(t, map[string]string{"/tasks": board})
	m = runLine(t, m, "/tasks edit")
	if !m.selectorActive || len(m.selectorItems) != 2 {
		t.Fatalf("task selector = active:%t items:%d\n%s", m.selectorActive, len(m.selectorItems), transcript(m))
	}
	if got, want := m.selectorItems[0].detail, "backlog · pending"; got != want {
		t.Fatalf("selector detail = %q, want %q", got, want)
	}
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.reviewPrefillTask == nil {
		t.Fatal("selector did not retain its resolved task")
	}
	if got := m.reviewPrefillTask.Prompt; got != "Preserve this prompt for edit." {
		t.Fatalf("prefilled prompt = %q", got)
	}
	if got := m.reviewPrefillTask.Category; got != "backlog" || m.reviewPrefillTask.Status != "pending" {
		t.Fatalf("prefilled state = category:%q status:%q", got, m.reviewPrefillTask.Status)
	}
	if !reflect.DeepEqual(m.reviewPrefillTask.Badges, []string{"Goal", "Sonnet"}) {
		t.Fatalf("prefilled badges = %#v", m.reviewPrefillTask.Badges)
	}
	if rec.count("GET", "/api/tasks/reference-catalog") != 1 || rec.count("GET", "/tasks") != 0 {
		t.Fatalf("selector requests = %s", rec.all())
	}
}

func TestSelectorSpacePrefillPrimesInput(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want string
	}{
		{"tasks_move", "/tasks move", "/tasks move t-1 "},
		{"tasks_order", "/tasks order", "/tasks order t-1 "},
		{"schedule_add", "/schedule add", "/schedule add t-1 "},
		{"tasks_reviews_add", "/tasks reviews add", "/tasks reviews add t-1 "},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			m, rec := dispatchModel(t, selFixtures())
			m = runLine(t, m, tc.cmd)
			if !m.selectorActive {
				t.Fatalf("selector not active:\n%s", transcript(m))
			}
			m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
			if m.selectorActive {
				t.Error("selector should close after Enter")
			}
			if got := m.input.Value(); got != tc.want {
				t.Errorf("input = %q, want %q", got, tc.want)
			}
			rec.mu.Lock()
			for _, c := range rec.calls {
				if strings.HasPrefix(c, "PUT ") || strings.HasPrefix(c, "POST ") {
					t.Errorf("prefill must not mutate, saw %s", c)
				}
			}
			rec.mu.Unlock()
		})
	}
}

func TestMultiArgSelectorCLIKeepsUsageError(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
	}{
		{"tasks_move", "/tasks move"},
		{"tasks_order", "/tasks order"},
		{"schedule_add", "/schedule add"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cliMode = true
			defer func() { cliMode = false }()
			m, rec := dispatchModel(t, selFixtures())
			m = runLine(t, m, tc.cmd)
			if m.selectorActive {
				t.Error("selector must not activate in CLI mode")
			}
			if out := transcript(m); !strings.Contains(strings.ToLower(out), "usage") {
				t.Errorf("expected usage error in CLI mode, got:\n%s", out)
			}
			rec.mu.Lock()
			nCalls := len(rec.calls)
			rec.mu.Unlock()
			if nCalls != 0 {
				t.Errorf("expected zero backend calls, got %d:\n%s", nCalls, rec.all())
			}
		})
	}
}

// TestSelectorViewRendersFilterAndCursor verifies the inline rendering shows
// the filter prompt and highlights the cursor row.
func TestSelectorViewRendersFilterAndCursor(t *testing.T) {
	m, _ := dispatchModel(t, selFixtures())
	m = runLine(t, m, "/tasks open")
	if !m.selectorActive {
		t.Fatalf("selector not active:\n%s", transcript(m))
	}
	m = typeSelectorRunes(t, m, "ref")
	view := m.View()
	if !strings.Contains(view, "> ref") {
		t.Errorf("filter prompt missing from view:\n%s", view)
	}
	if !strings.Contains(view, "Refactor the API") {
		t.Errorf("matching item missing from view:\n%s", view)
	}
	if strings.Contains(view, "Ship the docs") {
		t.Errorf("non-matching item should be filtered out:\n%s", view)
	}
}

// TestSelectorFetchErrorShowsError verifies that when the backend fetch fails
// (e.g. HTTP 500), an error entry is appended and the selector stays closed.
func TestSelectorFetchErrorShowsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tasks" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.selectedID = "p1"
	m.selectedName = "demo"

	m = runLine(t, m, "/tasks run")

	if m.selectorActive {
		t.Error("selector must not activate when the fetch fails")
	}
	out := transcript(m)
	if !strings.Contains(out, "500") && !strings.Contains(strings.ToLower(out), "error") {
		t.Errorf("expected an error message in the transcript:\n%s", out)
	}
}

// TestSelectorShrinksTranscriptViewport verifies that opening the selector
// shrinks the transcript viewport so the picker is visible on screen, and that
// closing the selector (Esc or Enter) restores the normal height.
func TestSelectorShrinksTranscriptViewport(t *testing.T) {
	m, _ := dispatchModel(t, selFixtures())
	// dispatchModel sets Height=30 via WindowSizeMsg; normal height = 30-5 = 25.
	normalHeight := m.transcript.Height

	m = runLine(t, m, "/tasks open")
	if !m.selectorActive {
		t.Fatalf("selector not active:\n%s", transcript(m))
	}
	if m.transcript.Height >= normalHeight {
		t.Errorf("transcript height should shrink while selector is open: got %d, normal %d",
			m.transcript.Height, normalHeight)
	}

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updated.(Model)
	if !m.selectorActive {
		t.Fatal("selector should remain active after resize")
	}
	if got, want := m.transcript.Height, 26; got != want {
		t.Errorf("transcript height after resize while selector active = %d, want %d", got, want)
	}
	normalHeight = 35

	// Esc should restore normal height.
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.selectorActive {
		t.Error("selector should be closed after Esc")
	}
	if m.transcript.Height != normalHeight {
		t.Errorf("transcript height after Esc = %d, want %d", m.transcript.Height, normalHeight)
	}
}

// TestSelectorCursorBoundaries verifies that pressing ↑ at the top keeps the
// cursor at 0, and pressing ↓ at the last item keeps it there.
func TestSelectorCursorBoundaries(t *testing.T) {
	m, _ := dispatchModel(t, selFixtures())
	m = runLine(t, m, "/tasks open")
	if !m.selectorActive {
		t.Fatalf("selector not active:\n%s", transcript(m))
	}
	// Up at index 0 must stay at 0.
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if m.selectorCursor != 0 {
		t.Errorf("cursor at top after ↑ = %d, want 0", m.selectorCursor)
	}
	// Move to last item (index 1 for our two-item fixture).
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if m.selectorCursor != 1 {
		t.Errorf("cursor after ↓ = %d, want 1", m.selectorCursor)
	}
	// Down at last must stay at last.
	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if m.selectorCursor != 1 {
		t.Errorf("cursor at bottom after ↓ = %d, want 1", m.selectorCursor)
	}
}

func TestSelectorCacheClearsBetweenSelectors(t *testing.T) {
	m, _ := dispatchModel(t, selFixtures())
	m = runLine(t, m, "/tasks run")
	if !m.selectorActive {
		t.Fatalf("selector not active:\n%s", transcript(m))
	}
	m = typeSelectorRunes(t, m, "docs")
	if items := m.filteredSelectorItems(); len(items) != 1 || items[0].ref != "t-2" {
		t.Fatalf("filter 'docs' should cache only t-2, got %+v", items)
	}

	m = selKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.selectorSearch != nil || m.selectorMatchIndexes != nil || m.selectorFilteredFor != "" {
		t.Fatalf("selector cache should clear on close: search=%d indexes=%d for=%q",
			len(m.selectorSearch), len(m.selectorMatchIndexes), m.selectorFilteredFor)
	}

	m = runLine(t, m, "/skills show")
	if !m.selectorActive {
		t.Fatalf("skills selector not active:\n%s", transcript(m))
	}
	if m.selectorFilter != "" {
		t.Fatalf("new selector reused old filter %q", m.selectorFilter)
	}
	items := m.filteredSelectorItems()
	if len(items) != 2 {
		t.Fatalf("new selector should show all skills, got %+v", items)
	}
	if items[0].ref != "retry-logic" || items[1].ref != "rate-limiter" {
		t.Fatalf("new selector reused stale task results, got %+v", items)
	}
}

var selectorBenchmarkSink string

func benchmarkSelectorFilterModel(n int) Model {
	items := make([]selectorItem, n)
	for i := range items {
		items[i] = selectorItem{
			ref:    fmt.Sprintf("task-%05d", i),
			label:  fmt.Sprintf("Synthetic Task %05d", i),
			detail: fmt.Sprintf("backlog detail group-%03d common", i%100),
		}
	}
	m := Model{
		width:  120,
		height: 40,
	}
	updated, _ := m.handleSelector(selectorActiveMsg{
		title:   "Synthetic Tasks",
		command: "tasks open",
		items:   items,
	})
	return updated.(Model)
}

func benchmarkSelectorModel(n int) Model {
	return benchmarkSelectorFilterModel(n).setSelectorFilter("common")
}

func BenchmarkSelectorFilterChanges(b *testing.B) {
	cases := []struct {
		name   string
		filter string
		prefix bool
	}{
		{name: "high_match", filter: "task"},
		{name: "low_match", filter: "group-099"},
		{name: "no_match", filter: "not-present"},
		{name: "character_by_character_high_match", filter: "Synthetic Task", prefix: true},
	}

	for _, n := range []int{1000, 10000} {
		for _, tc := range cases {
			tc := tc
			b.Run(fmt.Sprintf("items_%d/%s", n, tc.name), func(b *testing.B) {
				initial := benchmarkSelectorFilterModel(n)
				filters := []string{tc.filter}
				if tc.prefix {
					filters = make([]string, 0, len([]rune(tc.filter)))
					for i := range []rune(tc.filter) {
						filters = append(filters, tc.filter[:i+1])
					}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					m := initial
					for _, filter := range filters {
						m = m.setSelectorFilter(filter)
						selectorBenchmarkSink = m.selectorFilter
					}
				}
			})
		}
	}
}

func BenchmarkSelectorFilteredRender(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("items_%d", n), func(b *testing.B) {
			m := benchmarkSelectorModel(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				selectorBenchmarkSink = m.renderSelector()
			}
		})
	}
}

func BenchmarkSelectorDownArrow(b *testing.B) {
	key := tea.KeyMsg{Type: tea.KeyDown}
	for _, n := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("items_%d", n), func(b *testing.B) {
			m := benchmarkSelectorModel(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if m.selectorCursor >= len(m.filteredSelectorItems())-1 {
					m.selectorCursor = 0
				}
				next, _ := m.handleSelectorKey(key)
				m = next.(Model)
			}
		})
	}
}
