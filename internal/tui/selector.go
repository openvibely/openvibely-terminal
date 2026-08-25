package tui

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
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		defer cancel()
		items, err := fetch(ctx)
		return selectorActiveMsg{
			title:         title,
			command:       command,
			emptyHint:     emptyHint,
			prefill:       prefillSuffix != "",
			prefillSuffix: prefillSuffix,
			items:         items,
			err:           err,
		}
	}
}

// handleSelector applies a selectorActiveMsg: error, empty hint, single-item
// auto-select, or open the interactive picker.
func (m Model) handleSelector(msg selectorActiveMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	m = m.clearSelector()
	if msg.err != nil {
		m.append(entry{role: "error", text: msg.err.Error()})
		return m, nil
	}
	if len(msg.items) == 0 {
		m.append(entry{role: "result", head: msg.title, text: dimStyle.Render(msg.emptyHint)})
		return m, nil
	}
	if len(msg.items) == 1 {
		it := msg.items[0]
		m.append(entry{role: "system", text: "only one match — selected " + it.label})
		return m.selectorDispatch(msg.command, msg.prefill, msg.prefillSuffix, it)
	}
	m.selectorActive = true
	m.selectorTitle = msg.title
	m.selectorItems = msg.items
	m.selectorSearch = selectorSearchTexts(msg.items)
	m.selectorFilter = ""
	m.selectorFiltered = msg.items
	m.selectorFilteredFor = ""
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
	m.selectorCursor = 0
	m.pendingCommand = ""
	m.selectorPrefill = false
	m.selectorPrefillSuffix = ""
	if m.height > 0 {
		m.transcript.Height = m.transcriptHeight()
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
	f := strings.ToLower(m.selectorFilter)
	out := make([]selectorItem, 0, len(m.selectorItems))
	for i, it := range m.selectorItems {
		if strings.Contains(m.selectorSearch[i], f) {
			out = append(out, it)
		}
	}
	m.selectorFiltered = out
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
	f := strings.ToLower(m.selectorFilter)
	out := make([]selectorItem, 0, len(m.selectorItems))
	for i, it := range m.selectorItems {
		if strings.Contains(m.selectorSearch[i], f) {
			out = append(out, it)
		}
	}
	return out
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
		if prefillSuffix == "" {
			prefillSuffix = " "
		}
		m.input.SetValue(line + prefillSuffix)
		m.input.CursorEnd()
		m.refreshMenu()
		m.append(entry{role: "system", text: "selected " + it.label + " — finish the command and press enter"})
		return m, nil
	}
	m.append(entry{role: "you", text: line})
	if it.dispatch != nil {
		return it.dispatch(m)
	}
	return m.runCommand(line)
}

// renderSelector draws the inline picker: a filter prompt plus the matching
// items with the cursor highlighted.
func (m Model) renderSelector() string {
	var rows []string
	rows = append(rows, sectionStyle.Render("▸ "+m.selectorTitle))
	rows = append(rows, "> "+m.selectorFilter+"█")

	items := m.filteredSelectorItems()
	if len(items) == 0 {
		rows = append(rows, dimStyle.Render("  no matches — backspace to widen"))
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
		line := it.label
		if it.detail != "" {
			line += dimStyle.Render("  " + it.detail)
		}
		line += dimStyle.Render("  " + shortID(it.ref))
		if i == cur {
			rows = append(rows, paletteSelStyle.Render("▸ ")+line)
		} else {
			rows = append(rows, dimStyle.Render("  ")+line)
		}
	}
	if end < len(items) || start > 0 {
		rows = append(rows, dimStyle.Render(fmt.Sprintf("  … showing %d of %d", end-start, len(items))))
	}
	return paletteStyle.Render(strings.Join(rows, "\n"))
}
