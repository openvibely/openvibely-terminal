package tui

// Inline ref selector: when a slash command that needs a <ref> argument is
// invoked without one, the command fetches the candidate list and emits a
// selectorActiveMsg. The model then renders an interactive picker inline in
// the conversation view: ↑/↓ move, Enter confirms, Esc cancels, and typing
// filters the list incrementally. Choosing an item re-dispatches the pending
// command as if the user had typed "/<command> <ref>" directly.

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// selectorFor builds the tea.Cmd a command handler returns when its ref
// argument is missing: it fetches the candidate list and hands it to the
// model as a selectorActiveMsg.
func selectorFor(title, command, emptyHint string, prefill bool, fetch func(ctx context.Context) ([]selectorItem, error)) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		defer cancel()
		items, err := fetch(ctx)
		return selectorActiveMsg{
			title:     title,
			command:   command,
			emptyHint: emptyHint,
			prefill:   prefill,
			items:     items,
			err:       err,
		}
	}
}

// handleSelector applies a selectorActiveMsg: error, empty hint, single-item
// auto-select, or open the interactive picker.
func (m Model) handleSelector(msg selectorActiveMsg) (tea.Model, tea.Cmd) {
	m.busy = false
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
		return m.selectorDispatch(msg.command, msg.prefill, it)
	}
	m.selectorActive = true
	m.selectorTitle = msg.title
	m.selectorItems = msg.items
	m.selectorFilter = ""
	m.selectorCursor = 0
	m.pendingCommand = msg.command
	m.selectorPrefill = msg.prefill
	// Shrink the transcript viewport so the selector is visible on screen.
	// Normal layout reserves 5 rows (header + blank + input + hint + margin).
	// The selector takes up to 12 rows (title + filter + 8 items + overflow +
	// hint), so we reserve 14 rows to leave a small margin.
	if m.height > 0 {
		m.transcript.Height = max(3, m.height-14)
	}
	return m, nil
}

// clearSelector resets all selector state and restores the transcript height.
func (m Model) clearSelector() Model {
	m.selectorActive = false
	m.selectorTitle = ""
	m.selectorItems = nil
	m.selectorFilter = ""
	m.selectorCursor = 0
	m.pendingCommand = ""
	m.selectorPrefill = false
	// Restore the normal transcript height (mirrors the formula in resize()).
	if m.height > 0 {
		m.transcript.Height = max(3, m.height-5)
	}
	return m
}

// filteredSelectorItems returns the items matching the typed filter.
func (m Model) filteredSelectorItems() []selectorItem {
	if m.selectorFilter == "" {
		return m.selectorItems
	}
	f := strings.ToLower(m.selectorFilter)
	var out []selectorItem
	for _, it := range m.selectorItems {
		if strings.Contains(strings.ToLower(it.label), f) ||
			strings.Contains(strings.ToLower(it.detail), f) ||
			strings.Contains(strings.ToLower(it.ref), f) {
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
			m.selectorFilter = string(r[:len(r)-1])
			m.selectorCursor = 0
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
		command, prefill := m.pendingCommand, m.selectorPrefill
		m = m.clearSelector()
		return m.selectorDispatch(command, prefill, it)
	}

	switch {
	case msg.Type == tea.KeySpace:
		m.selectorFilter += " "
	case msg.Type == tea.KeyRunes:
		m.selectorFilter += string(msg.Runes)
	}
	if msg.Type == tea.KeySpace || msg.Type == tea.KeyRunes {
		m.selectorCursor = 0
	}
	return m, nil
}

// selectorDispatch runs the pending command against the chosen item. For
// prefill commands (those that need more piped arguments, like "tasks edit")
// the input is primed with "/<command> <ref> | " instead of executing.
func (m Model) selectorDispatch(command string, prefill bool, it selectorItem) (tea.Model, tea.Cmd) {
	line := "/" + command + " " + it.ref
	if prefill {
		m.input.SetValue(line + " | ")
		m.input.CursorEnd()
		m.refreshMenu()
		m.append(entry{role: "system", text: "selected " + it.label + " — finish the command and press enter"})
		return m, nil
	}
	m.append(entry{role: "you", text: line})
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
