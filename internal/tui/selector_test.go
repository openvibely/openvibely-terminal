package tui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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
		{"tasks_reviews", "/tasks reviews", "tasks reviews"},
		{"tasks_edit", "/tasks edit", "tasks edit"},
		{"tasks_run", "/tasks run", "tasks run"},
		{"tasks_stop", "/tasks stop", "tasks stop"},
		{"tasks_delete", "/tasks delete", "tasks delete"},
		{"tasks_move", "/tasks move", "tasks move"},
		{"tasks_order", "/tasks order", "tasks order"},
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
		{"schedule_add", "/schedule add", "schedule add"},
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

func TestSelectorSpacePrefillPrimesInput(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want string
	}{
		{"tasks_move", "/tasks move", "/tasks move t-1 "},
		{"tasks_order", "/tasks order", "/tasks order t-1 "},
		{"schedule_add", "/schedule add", "/schedule add t-1 "},
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
	if m.selectorSearch != nil || m.selectorFiltered != nil || m.selectorFilteredFor != "" {
		t.Fatalf("selector cache should clear on close: search=%d filtered=%d for=%q",
			len(m.selectorSearch), len(m.selectorFiltered), m.selectorFilteredFor)
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

func benchmarkSelectorModel(n int) Model {
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
	m = updated.(Model)
	m = m.setSelectorFilter("common")
	return m
}

func BenchmarkSelectorFilteredRender(b *testing.B) {
	for _, n := range []int{1000, 10000} {
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
