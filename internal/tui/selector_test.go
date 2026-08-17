package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-tui/internal/client"
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
  <div data-task-id="t-2" data-schedule-id="s-2">Weekly report — weekly mon</div>
</div>`
)

// selFixtures maps backend paths to two-item list fixtures for every area.
func selFixtures() map[string]string {
	return map[string]string{
		"/tasks":       selTasksHTML,
		"/alerts":      selAlertsHTML,
		"/skills":      selSkillsHTML,
		"/agents":      selAgentsHTML,
		"/models":      selModelsHTML,
		"/automations": selAutomationsHTML,
		"/schedule":    selScheduleHTML,
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
		{"tasks_edit", "/tasks edit", "tasks edit"},
		{"tasks_run", "/tasks run", "tasks run"},
		{"tasks_stop", "/tasks stop", "tasks stop"},
		{"tasks_delete", "/tasks delete", "tasks delete"},
		{"tasks_goal", "/tasks goal", "tasks goal"},
		{"tasks_reply", "/tasks reply", "tasks reply"},
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
		{"agents_delete", "/agents delete", "agents delete"},
		// models
		{"models_default", "/models default", "models default"},
		{"models_delete", "/models delete", "models delete"},
		// automations
		{"automations_run-now", "/automations run-now", "automations run-now"},
		{"automations_pause", "/automations pause", "automations pause"},
		{"automations_resume", "/automations resume", "automations resume"},
		{"automations_delete", "/automations delete", "automations delete"},
		// channels
		{"channels_test", "/channels test", "channels test"},
		{"channels_remove", "/channels remove", "channels remove"},
		// schedule
		{"schedule_delete", "/schedule delete", "schedule delete"},
		{"schedule_toggle", "/schedule toggle", "schedule toggle"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			m, _ := dispatchModel(t, selFixtures())
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

// TestProjectNoArgOpensSelector verifies /project with no argument opens a
// project picker when more than one project is known, and that choosing one
// switches the active project.
func TestProjectNoArgOpensSelector(t *testing.T) {
	m, _ := dispatchModel(t, nil)
	m.projects = []client.Project{
		{ID: "p1", Name: "demo"},
		{ID: "p2", Name: "beta"},
	}
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
