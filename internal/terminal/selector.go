package terminal

// Inline ref selector: when a slash command that needs a <ref> argument is
// invoked without one, the command fetches the candidate list and emits a
// selectorActiveMsg. The model then renders an interactive picker inline in
// the conversation view: ↑/↓ move, Enter confirms, Esc cancels, and typing
// filters the list incrementally. Choosing an item invokes its direct action
// when one is available; otherwise it re-dispatches the pending command as if
// the user had typed "/<command> <ref>" directly.

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	selectorTitleDisplayWidth     = 80
	selectorLabelDisplayWidth     = 96
	selectorDetailDisplayWidth    = 160
	selectorReferenceDisplayWidth = 48
	selectorEchoWidth             = 240
)

func selectorDisplay(value string, width int) string {
	return truncate(sanitizeMemoryText(value), width)
}

// selectorItemDispatch runs an action against the resource already loaded for
// a selector item. Items without one retain the text re-dispatch path.
type selectorItemDispatch func(Model) (Model, tea.Cmd)

// selectorFor builds the tea.Cmd a command handler returns when its ref
// argument is missing: it fetches the candidate list and hands it to the
// model as a selectorActiveMsg.
func selectorFor(title, command, emptyHint string, prefill bool, fetch func(ctx context.Context) ([]selectorItem, error)) tea.Cmd {
	prefillSuffix := ""
	if prefill {
		prefillSuffix = " | "
	}
	return selectorForWithSuffix(title, command, emptyHint, prefillSuffix, fetch)
}

func selectorForWithSuffix(title, command, emptyHint, prefillSuffix string, fetch func(ctx context.Context) ([]selectorItem, error)) tea.Cmd {
	return selectorForWithWarningsSuffix(title, command, emptyHint, prefillSuffix,
		func(ctx context.Context) ([]selectorItem, []string, error) {
			items, err := fetch(ctx)
			return items, nil, err
		})
}

// selectorForWithWarnings is the selector variant for list sources that can
// return usable items alongside safe diagnostics about partial or empty state.
func selectorForWithWarnings(title, command, emptyHint string, fetch func(context.Context) ([]selectorItem, []string, error)) tea.Cmd {
	return selectorForWithWarningsSuffix(title, command, emptyHint, "", fetch)
}

func selectorForWithWarningsSuffix(title, command, emptyHint, prefillSuffix string, fetch func(context.Context) ([]selectorItem, []string, error)) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		defer cancel()
		items, warnings, err := fetch(ctx)
		return selectorActiveMsg{
			title:         title,
			command:       command,
			emptyHint:     emptyHint,
			prefill:       prefillSuffix != "",
			prefillSuffix: prefillSuffix,
			items:         items,
			warnings:      warnings,
			err:           err,
		}
	}
}

func optionSelector(m Model, title, command, usage string, values []string) (Model, tea.Cmd) {
	return selectorOr(m, usage, selectorFor(title, command, "no options available", false,
		func(context.Context) ([]selectorItem, error) {
			items := make([]selectorItem, 0, len(values))
			for _, value := range values {
				items = append(items, selectorItem{ref: value, label: value})
			}
			return items, nil
		}))
}

func optionSelectorWithFilter(m Model, title, command, usage string, values []string, filter string) (Model, tea.Cmd) {
	next, cmd := optionSelector(m, title, command, usage, values)
	if cmd == nil {
		return next, nil
	}
	return next, func() tea.Msg {
		msg := cmd()
		if active, ok := msg.(selectorActiveMsg); ok {
			active.initialFilter = filter
			active.forcePicker = true
			return active
		}
		return msg
	}
}

// handleSelector applies a selectorActiveMsg: error, empty hint, single-item
// auto-select, or open the interactive picker.
func (m Model) handleSelector(msg selectorActiveMsg) (tea.Model, tea.Cmd) {
	if !m.acceptsSessionGeneration(msg.sessionGeneration) || !m.acceptsProjectGeneration(msg.projectGeneration) {
		return m, nil // stale selector response from an older session or project
	}
	m.busy = false
	m = m.clearSelector()
	warnings := append([]string(nil), msg.warnings...)
	if msg.err != nil {
		if len(warnings) > 0 {
			m.append(entry{role: "result", head: selectorDisplay(msg.title, selectorTitleDisplayWidth), text: renderMemoryWarnings(warnings)})
		}
		if m.handleAuthError(msg.err) {
			return m, nil
		}
		if m.handleTransportError(msg.err) {
			return m, nil
		}
		m.append(entry{role: "error", text: selectorDisplay(msg.err.Error(), selectorEchoWidth)})
		return m, nil
	}
	if len(msg.items) == 0 {
		text := dimStyle.Render(selectorDisplay(msg.emptyHint, selectorDetailDisplayWidth))
		if len(warnings) > 0 {
			text += "\n\n" + renderMemoryWarnings(warnings)
		}
		m.append(entry{role: "result", head: selectorDisplay(msg.title, selectorTitleDisplayWidth), text: text})
		return m, nil
	}
	matchingItems := msg.items
	var search []string
	if msg.initialFilter != "" {
		search = selectorSearchTexts(msg.items)
		matchingItems = matchSelectorItems(msg.items, search, msg.initialFilter)
	}
	if len(matchingItems) == 1 && !msg.forcePicker {
		if len(warnings) > 0 {
			m.append(entry{role: "result", head: selectorDisplay(msg.title, selectorTitleDisplayWidth), text: renderMemoryWarnings(warnings)})
		}
		it := matchingItems[0]
		m.append(entry{role: "system", text: "only one match — selected " + selectorDisplay(it.label, selectorLabelDisplayWidth)})
		return m.selectorDispatch(msg.command, msg.prefill, msg.prefillSuffix, it)
	}
	m.selectorActive = true
	m.selectorTitle = msg.title
	m.selectorItems = msg.items
	m.selectorWarnings = warnings
	if search == nil {
		search = selectorSearchTexts(msg.items)
	}
	m.selectorSearch = search
	m.selectorFilter = msg.initialFilter
	m.selectorFiltered = matchingItems
	m.selectorFilteredFor = msg.initialFilter
	m.selectorCursor = 0
	m.pendingCommand = msg.command
	m.selectorPrefill = msg.prefill
	m.selectorPrefillSuffix = msg.prefillSuffix
	if m.height > 0 {
		m.transcript.Height = m.transcriptHeight()
	}
	return m, nil
}

// clearSelector resets all selector state and restores the transcript height.
func (m Model) clearSelector() Model {
	m.selectorActive = false
	m.selectorTitle = ""
	m.selectorItems = nil
	m.selectorSearch = nil
	m.selectorFilter = ""
	m.selectorFiltered = nil
	m.selectorFilteredFor = ""
	m.selectorWarnings = nil
	m.selectorCursor = 0
	m.pendingCommand = ""
	m.selectorPrefill = false
	m.selectorPrefillSuffix = ""
	m = m.clearReviewPrefill()
	if m.height > 0 {
		m.transcript.Height = m.transcriptHeight()
	}
	return m
}

func (m Model) clearReviewPrefill() Model {
	m.reviewPrefillTask = nil
	m.reviewPrefillTaskRef = ""
	m.reviewPrefillProjectID = ""
	m.reviewPrefillInputPrefix = ""
	return m
}

func (m Model) invalidateReviewPrefill() Model {
	if m.reviewPrefillTask != nil && !strings.HasPrefix(m.input.Value(), m.reviewPrefillInputPrefix) {
		return m.clearReviewPrefill()
	}
	return m
}

func selectorSearchTexts(items []selectorItem) []string {
	search := make([]string, len(items))
	for i, it := range items {
		search[i] = strings.ToLower(it.label) + "\x00" + strings.ToLower(it.detail) + "\x00" + strings.ToLower(it.ref)
	}
	return search
}

func matchSelectorItems(items []selectorItem, search []string, filter string) []selectorItem {
	needle := strings.ToLower(filter)
	matches := make([]selectorItem, 0, len(items))
	for i, item := range items {
		if strings.Contains(search[i], needle) {
			matches = append(matches, item)
		}
	}
	return matches
}

func (m Model) setSelectorFilter(filter string) Model {
	if filter == m.selectorFilter && m.selectorFilteredFor == filter {
		return m
	}
	m.selectorFilter = filter
	m.selectorCursor = 0
	return m.rebuildSelectorFilterCache()
}

func (m Model) rebuildSelectorFilterCache() Model {
	m.selectorFilteredFor = m.selectorFilter
	if m.selectorFilter == "" {
		m.selectorFiltered = m.selectorItems
		return m
	}
	if len(m.selectorSearch) != len(m.selectorItems) {
		m.selectorSearch = selectorSearchTexts(m.selectorItems)
	}
	m.selectorFiltered = matchSelectorItems(m.selectorItems, m.selectorSearch, m.selectorFilter)
	return m
}

// filteredSelectorItems returns the cached items matching the typed filter.
func (m Model) filteredSelectorItems() []selectorItem {
	if m.selectorFilter == "" {
		return m.selectorItems
	}
	if m.selectorFilteredFor == m.selectorFilter && m.selectorFiltered != nil {
		return m.selectorFiltered
	}
	if len(m.selectorSearch) != len(m.selectorItems) {
		m.selectorSearch = selectorSearchTexts(m.selectorItems)
	}
	return matchSelectorItems(m.selectorItems, m.selectorSearch, m.selectorFilter)
}

// handleSelectorKey routes key input while the selector is open.
func (m Model) handleSelectorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "ctrl+d":
		m.quitting = true
		m.Cleanup()
		return m, tea.Quit

	case "esc":
		m = m.clearSelector()
		m.append(entry{role: "system", text: "cancelled"})
		return m, nil

	case "up", "ctrl+p":
		if m.selectorCursor > 0 {
			m.selectorCursor--
		}
		return m, nil

	case "down", "ctrl+n":
		if n := len(m.filteredSelectorItems()); m.selectorCursor < n-1 {
			m.selectorCursor++
		}
		return m, nil

	case "backspace":
		if m.selectorFilter != "" {
			r := []rune(m.selectorFilter)
			m = m.setSelectorFilter(string(r[:len(r)-1]))
		}
		return m, nil

	case "enter":
		items := m.filteredSelectorItems()
		if len(items) == 0 {
			return m, nil
		}
		cur := m.selectorCursor
		if cur >= len(items) {
			cur = len(items) - 1
		}
		it := items[cur]
		command, prefill, prefillSuffix := m.pendingCommand, m.selectorPrefill, m.selectorPrefillSuffix
		m = m.clearSelector()
		return m.selectorDispatch(command, prefill, prefillSuffix, it)
	}

	switch {
	case msg.Type == tea.KeySpace:
		m = m.setSelectorFilter(m.selectorFilter + " ")
	case msg.Type == tea.KeyRunes:
		m = m.setSelectorFilter(m.selectorFilter + string(msg.Runes))
	}
	return m, nil
}

// selectorDispatch runs the pending command against the chosen item. For
// prefill commands (those that need more piped arguments, like "tasks edit")
// the input is primed with "/<command> <ref> | " instead of executing. For
// direct-action items, the already-resolved resource is passed to its callback.
func (m Model) selectorDispatch(command string, prefill bool, prefillSuffix string, it selectorItem) (tea.Model, tea.Cmd) {
	line := "/" + command + " " + it.ref
	if prefill {
		m = m.clearReviewPrefill()
		if prefillSuffix == "" {
			prefillSuffix = " "
		}
		if command == "tasks reviews add" && it.resolvedTask != nil {
			m.reviewPrefillTask = it.resolvedTask
			m.reviewPrefillTaskRef = it.ref
			m.reviewPrefillProjectID = m.selectedID
			m.reviewPrefillInputPrefix = line + prefillSuffix
		}
		m.input.SetValue(line + prefillSuffix)
		m.input.CursorEnd()
		m.refreshMenu()
		m.append(entry{role: "system", text: "selected " + selectorDisplay(it.label, selectorLabelDisplayWidth) + " — finish the command and press enter"})
		return m, nil
	}
	m.append(entry{role: "you", text: selectorDisplay(line, selectorEchoWidth)})
	if it.dispatch != nil {
		next, cmd := it.dispatch(m)
		return next, withMessageGeneration(cmd, sessionGenerationOf(next), projectGenerationOf(next))
	}
	return m.runCommand(line)
}

// renderSelector draws the inline picker: a filter prompt plus the matching
// items with the cursor highlighted.
func (m Model) renderSelector() string {
	var rows []string
	rows = append(rows, sectionStyle.Render("▸ "+selectorDisplay(m.selectorTitle, selectorTitleDisplayWidth)))
	rows = append(rows, "> "+selectorDisplay(m.selectorFilter, selectorDetailDisplayWidth)+"█")

	items := m.filteredSelectorItems()
	if len(items) == 0 {
		rows = append(rows, dimStyle.Render("  no matches — backspace to widen"))
		if len(m.selectorWarnings) > 0 {
			rows = append(rows, renderMemoryWarnings(m.selectorWarnings))
		}
		return paletteStyle.Render(strings.Join(rows, "\n"))
	}

	cur := m.selectorCursor
	if cur >= len(items) {
		cur = len(items) - 1
	}
	const maxRows = 8
	start := 0
	if len(items) > maxRows && cur >= maxRows {
		start = cur - maxRows + 1
	}
	end := start + maxRows
	if end > len(items) {
		end = len(items)
	}
	for i := start; i < end; i++ {
		it := items[i]
		line := selectorDisplay(it.label, selectorLabelDisplayWidth)
		if detail := selectorDisplay(it.detail, selectorDetailDisplayWidth); detail != "" {
			line += dimStyle.Render("  " + detail)
		}
		line += dimStyle.Render("  " + selectorDisplay(shortID(it.ref), selectorReferenceDisplayWidth))
		if i == cur {
			rows = append(rows, paletteSelStyle.Render("▸ ")+line)
		} else {
			rows = append(rows, dimStyle.Render("  ")+line)
		}
	}
	if end < len(items) || start > 0 {
		rows = append(rows, dimStyle.Render(fmt.Sprintf("  … showing %d of %d", end-start, len(items))))
	}
	if len(m.selectorWarnings) > 0 {
		rows = append(rows, renderMemoryWarnings(m.selectorWarnings))
	}
	return paletteStyle.Render(strings.Join(rows, "\n"))
}
