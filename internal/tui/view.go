package tui

// Rendering: the chat window chrome plus the block renderers that slash
// commands emit into the transcript.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"

	"github.com/openvibely/openvibely-tui/internal/client"
)

// View renders header, transcript, command menu and input.
func (m Model) View() string {
	if m.quitting {
		return dimStyle.Render("bye.\n")
	}
	if m.width == 0 {
		return "starting…"
	}

	var b strings.Builder
	b.WriteString(m.renderHeader() + "\n")
	b.WriteString(m.transcript.View() + "\n")
	if m.selectorActive {
		b.WriteString(m.renderSelector() + "\n")
		b.WriteString(helpStyle.Render(m.hint()))
		return b.String()
	}
	if menu := m.renderMenu(); menu != "" {
		b.WriteString(menu + "\n")
	}
	b.WriteString(m.input.View() + "\n")
	if m.pendingConfirmation != nil {
		b.WriteString(noticeStyle.Render("⚠  " + m.pendingConfirmation.message))
	} else {
		b.WriteString(helpStyle.Render(m.hint()))
	}
	return b.String()
}

func (m Model) renderHeader() string {
	left := titleStyle.Render("OpenVibely")

	project := "no project"
	if m.selectedName != "" {
		project = m.selectedName
	}

	conn := statusErrStyle.Render("● offline")
	if m.connected {
		conn = statusOKStyle.Render("● online")
	}
	stream := ""
	if m.showEvents {
		if m.sseConnected {
			stream = statusOKStyle.Render(" ⚡live")
		} else {
			stream = noticeStyle.Render(" ⚡reconnecting")
		}
	}
	right := conn + stream

	middle := screenTitleStyle.Render(" · " + project)
	if m.threadID != "" {
		middle += sectionStyle.Render(" ▸ " + truncate(m.threadTitle, 30))
	}
	if m.busy || m.pendingMsgID != "" {
		middle += " " + m.spin.View()
	}

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(middle) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + middle + strings.Repeat(" ", gap) + right
}

// renderMenu draws the inline completion menu while a "/" command is typed.
func (m Model) renderMenu() string {
	if len(m.menu) == 0 {
		return ""
	}
	const maxRows = 6
	menu := m.menu
	start := 0
	if len(menu) > maxRows {
		if m.menuSel >= maxRows {
			start = m.menuSel - maxRows + 1
		}
		end := start + maxRows
		if end > len(menu) {
			end = len(menu)
		}
		menu = menu[start:end]
	}

	var rows []string
	for i, c := range menu {
		label := c.label()
		line := fmt.Sprintf("%-34s %s", label, c.desc)
		if start+i == m.menuSel {
			rows = append(rows, paletteSelStyle.Render("▸ "+line))
		} else {
			rows = append(rows, dimStyle.Render("  "+line))
		}
	}
	if len(m.menu) > maxRows {
		rows = append(rows, dimStyle.Render(fmt.Sprintf("  … %d more", len(m.menu)-maxRows)))
	}
	return paletteStyle.Render(strings.Join(rows, "\n"))
}

func (m Model) hint() string {
	if m.selectorActive {
		return "type to filter · ↑↓ choose · enter select · esc cancel"
	}
	if len(m.menu) > 0 {
		return "tab complete · ↑↓ choose · enter run · esc close"
	}
	if !m.connected && m.connErr != "" {
		return "offline: " + truncate(m.connErr, m.width-12)
	}
	if m.threadID != "" {
		return "in task thread · messages reply to this task · /chat to exit · / for commands"
	}
	return "type to chat · / for commands · ↑↓ history · pgup/pgdn scroll · ctrl+l clear · ctrl+c quit"
}

// renderStatus is the /status block.
func (m Model) renderStatus() string {
	var b strings.Builder
	row := func(k, v string) { fmt.Fprintf(&b, "  %-16s %s\n", k, v) }

	if m.connected {
		row("server", statusOKStyle.Render("connected")+dimStyle.Render(" "+m.client.BaseURL()))
	} else {
		row("server", statusErrStyle.Render("offline")+dimStyle.Render(" "+m.client.BaseURL()))
		if m.connErr != "" {
			row("error", m.connErr)
		}
	}
	switch {
	case m.auth == nil:
		row("auth", dimStyle.Render("unknown"))
	case m.auth.Authenticated:
		name := m.auth.Display
		if name == "" {
			name = m.auth.Username
		}
		row("auth", statusOKStyle.Render("signed in as "+name))
	default:
		row("auth", dimStyle.Render("disabled or anonymous"))
	}
	if m.selectedName != "" {
		row("project", m.selectedName)
	}
	if c := m.capacity; c != nil {
		row("workers", fmt.Sprintf("%d running / %d max, %d queued, %d free",
			c.TotalRunning, c.MaxWorkers, c.QueueSize, c.AvailableSlots))
	}
	if m.selectedID != "" {
		if m.pendingAlertCount > 0 {
			row("alerts", noticeStyle.Render(fmt.Sprintf("%d pending approvals", m.pendingAlertCount)))
		} else {
			row("alerts", dimStyle.Render("none pending"))
		}
		if m.activeTaskCount > 0 || m.queuedTaskCount > 0 {
			var taskParts []string
			if m.activeTaskCount > 0 {
				taskParts = append(taskParts, fmt.Sprintf("%d active", m.activeTaskCount))
			}
			if m.queuedTaskCount > 0 {
				taskParts = append(taskParts, fmt.Sprintf("%d queued", m.queuedTaskCount))
			}
			row("tasks", noticeStyle.Render(strings.Join(taskParts, ", ")))
		} else {
			row("tasks", dimStyle.Render("none active"))
		}
	}
	if m.showEvents {
		if m.sseConnected {
			row("events", statusOKStyle.Render("streaming"))
		} else {
			row("events", noticeStyle.Render("reconnecting…"))
		}
	} else {
		row("events", dimStyle.Render("off (/events on)"))
	}
	row("projects", fmt.Sprintf("%d", len(m.projects)))
	return strings.TrimRight(b.String(), "\n")
}

// --- generic table helper ---

// table renders aligned rows; the first row is treated as a header.
//
// Column widths are measured with lipgloss.Width so that styled cells (which
// carry invisible ANSI escapes) and multi-byte titles still line up. Padding is
// appended manually for the same reason: %-*s would count escape bytes.
func table(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	cols := 0
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	widths := make([]int, cols)
	for _, r := range rows {
		for i, cell := range r {
			if w := lipgloss.Width(cell); w > widths[i] {
				widths[i] = w
			}
		}
	}
	var b strings.Builder
	for ri, r := range rows {
		var line strings.Builder
		for i, cell := range r {
			if i == len(r)-1 {
				line.WriteString(cell)
				break
			}
			line.WriteString(cell)
			line.WriteString(strings.Repeat(" ", widths[i]-lipgloss.Width(cell)+2))
		}
		text := strings.TrimRight(line.String(), " ")
		if ri == 0 {
			b.WriteString(headerStyle.Render(text) + "\n")
		} else {
			b.WriteString(text + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// bar draws a proportional ASCII bar.
func bar(value, max float64, width int) string {
	if max <= 0 || width <= 0 {
		return ""
	}
	n := int(value / max * float64(width))
	if n < 0 {
		n = 0
	}
	if n > width {
		n = width
	}
	if n == 0 && value > 0 {
		n = 1
	}
	return strings.Repeat("█", n) + strings.Repeat("·", width-n)
}

func filterMatch(filter string, fields ...string) bool {
	if filter == "" {
		return true
	}
	f := strings.ToLower(filter)
	for _, s := range fields {
		if strings.Contains(strings.ToLower(s), f) {
			return true
		}
	}
	return false
}

// shortID trims an ID for display.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// statusMark colours a task/execution status.
func statusMark(status string) string {
	switch strings.ToLower(status) {
	case "completed", "success", "done":
		return statusOKStyle.Render("✓ " + status)
	case "failed", "error", "cancelled":
		return statusErrStyle.Render("✗ " + status)
	case "running", "in_progress":
		return noticeStyle.Render("▶ " + status)
	case "":
		return dimStyle.Render("—")
	default:
		return dimStyle.Render("· " + status)
	}
}

// --- tasks ---

func renderBoard(tasks []client.Task, filter string) string {
	columns := []struct {
		key   string
		label string
	}{
		{"backlog", "Backlog"},
		{"active", "Active"},
		{"completed", "Completed"},
	}

	var b strings.Builder
	total := 0
	for _, col := range columns {
		var rows [][]string
		for _, t := range tasks {
			if !strings.EqualFold(t.Category, col.key) {
				continue
			}
			if !filterMatch(filter, t.Title, t.ID, t.Prompt) {
				continue
			}
			title := t.Title
			if title == "" {
				title = dimStyle.Render("(untitled)")
			} else {
				title = truncate(title, 52)
			}
			if badges := renderBadges(t.Badges); badges != "" {
				title += "  " + badges
			}
			rows = append(rows, []string{shortID(t.ID), statusMark(t.Status), title})
		}
		total += len(rows)
		fmt.Fprintf(&b, "%s %s\n", sectionStyle.Render(col.label), dimStyle.Render(fmt.Sprintf("(%d)", len(rows))))
		if len(rows) == 0 {
			b.WriteString(dimStyle.Render("  —") + "\n\n")
			continue
		}
		b.WriteString(indent(table(append([][]string{{"ID", "STATUS", "TITLE"}}, rows...)), "  ") + "\n\n")
	}
	if total == 0 {
		if filter != "" {
			return dimStyle.Render("no tasks match " + filter)
		}
		return dimStyle.Render("no tasks yet — /tasks new <title> creates one")
	}
	b.WriteString(dimStyle.Render("/tasks show <id|title> · /tasks open <id> enters its thread · /tasks run <id>"))
	return b.String()
}

// renderBadges renders card badges compactly, dropping noisy ones.
func renderBadges(badges []string) string {
	// Badges that are constant across rows carry no information in a list.
	noise := map[string]bool{
		"no model": true, "project scoped": true, "operational": true,
	}
	var keep []string
	for _, s := range badges {
		if s == "" || noise[strings.ToLower(s)] {
			continue
		}
		keep = append(keep, s)
	}
	if len(keep) == 0 {
		return ""
	}
	if len(keep) > 3 {
		keep = keep[:3]
	}
	return badgeStyle.Render("[" + strings.Join(keep, "] [") + "]")
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func renderTaskDetail(t client.Task, d *client.TaskDetail, tab string) string {
	var b strings.Builder
	title := t.Title
	if title == "" {
		title = d.Task.Title
	}
	fmt.Fprintf(&b, "%s  %s\n", sectionStyle.Render(title), statusMark(firstNonEmpty(d.Task.Status, t.Status)))
	fmt.Fprintf(&b, "%s\n\n", dimStyle.Render(fmt.Sprintf("id %s · %s", t.ID, t.Category)))

	if tab != "" {
		label := titleFor(tab)
		body := d.TabText(tab)
		if meta, ok := client.TaskDetailTabByName(tab); ok {
			label = meta.Label
			body = meta.Text(d)
		}
		fmt.Fprintf(&b, "%s\n%s\n", sectionStyle.Render(label), textOrDash(body))
		return b.String()
	}

	for _, meta := range client.TaskDetailTabs() {
		body := strings.TrimSpace(meta.Text(d))
		if body == "" {
			continue
		}
		fmt.Fprintf(&b, "%s\n%s\n\n", sectionStyle.Render("▸ "+meta.Label), clamp(body, 40))
	}
	b.WriteString(dimStyle.Render("/tasks show <id> <" + detailTabHintList() + ">"))
	return b.String()
}

func renderTaskReviews(t client.Task, reviews []client.ReviewComment) string {
	var b strings.Builder
	title := firstNonEmpty(t.Title, shortID(t.ID))
	fmt.Fprintf(&b, "%s  %s\n", sectionStyle.Render(title), statusMark(t.Status))
	fmt.Fprintf(&b, "%s\n\n", dimStyle.Render(fmt.Sprintf("id %s · review", t.ID)))
	if len(reviews) == 0 {
		b.WriteString(dimStyle.Render("no review comments yet — /tasks reviews add <task> <file>:<line> <comment>"))
		return b.String()
	}

	rows := [][]string{{"FILE", "LINE", "STATE", "COMMENT"}}
	for _, r := range reviews {
		line := "—"
		if r.LineNumber > 0 {
			line = fmt.Sprintf("%d", r.LineNumber)
		}
		if r.LineType != "" {
			line += " " + r.LineType
		}
		state := reviewState(r)
		comment := r.CommentText
		if r.ReviewedBy != "" {
			comment = r.ReviewedBy + ": " + comment
		}
		rows = append(rows, []string{truncate(r.FilePath, 32), line, state, truncate(comment, 72)})
	}
	b.WriteString(table(rows))
	return b.String()
}

func reviewState(r client.ReviewComment) string {
	if r.Resolved {
		if r.State != "" {
			return statusOKStyle.Render("resolved " + r.State)
		}
		return statusOKStyle.Render("resolved")
	}
	if r.State != "" {
		return statusMark(r.State)
	}
	return dimStyle.Render("—")
}

// renderThread shows a task's conversation, falling back to the details tab
// when the thread has no messages yet.
func renderThread(d *client.TaskDetail) string {
	if body := strings.TrimSpace(d.Thread); body != "" {
		return clamp(body, 60)
	}
	if body := strings.TrimSpace(d.Details); body != "" {
		return dimStyle.Render("(no messages yet — showing details)") + "\n" + clamp(body, 30)
	}
	return dimStyle.Render("(no messages yet)")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func textOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return dimStyle.Render("  (empty)")
	}
	return clamp(strings.TrimSpace(s), 60)
}

// clamp limits a block to n lines.
func clamp(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + "\n" + dimStyle.Render(fmt.Sprintf("… %d more lines", len(lines)-n))
}

// --- schedule ---

func renderSchedule(entries []client.ScheduleEntry, summary string) string {
	if len(entries) == 0 {
		if strings.TrimSpace(summary) != "" {
			return clamp(strings.TrimSpace(summary), 40)
		}
		return dimStyle.Render("nothing scheduled — /schedule add <task> <2006-01-02T15:04> daily")
	}
	rows := [][]string{{"SCHEDULE", "TASK", "WHEN"}}
	for _, e := range entries {
		rows = append(rows, []string{shortID(e.ScheduleID), shortID(e.TaskID), truncate(e.Text, 60)})
	}
	return table(rows)
}

// --- alerts ---

func renderAlerts(alerts []client.Alert, filter string) string {
	rows := [][]string{{"ID", "", "ALERT", "STATE"}}
	unread := 0
	for _, a := range alerts {
		if !filterMatch(filter, a.Title, a.Text, a.Message, a.ID) {
			continue
		}
		mark := noticeStyle.Render("●") // unread
		if a.Read {
			mark = dimStyle.Render("·")
		} else {
			unread++
		}
		title := truncate(firstNonEmpty(a.Title, a.Message, a.Text), 56)
		if a.Message != "" && a.Title != "" {
			title += dimStyle.Render(" — " + truncate(a.Message, 30))
		}
		rows = append(rows, []string{shortID(a.ID), mark, title, renderBadges(a.Badges)})
	}
	if len(rows) == 1 {
		if filter != "" {
			return dimStyle.Render("no alerts match " + filter)
		}
		return dimStyle.Render("no alerts — /alerts approve|reject|dismiss <id> acts on pending ones")
	}
	out := table(rows)
	if unread > 0 {
		out = noticeStyle.Render(fmt.Sprintf("%d unread", unread)) + "\n" + out
	}
	return out + "\n\n" +
		dimStyle.Render("/alerts approve|reject|dismiss|delete <id> · /alerts read-all")
}

// --- skills ---

func renderSkills(skills []client.Skill, filter string) string {
	var rows [][]string
	for _, s := range skills {
		if !filterMatch(filter, s.Handle, s.Name, s.Description) {
			continue
		}
		state := statusOKStyle.Render("on")
		if !s.Enabled {
			state = dimStyle.Render("off")
		}
		if s.AlwaysUse {
			state += noticeStyle.Render(" always")
		}
		rows = append(rows, []string{s.Handle, state, s.Scope, truncate(s.Description, 50)})
	}
	if len(rows) == 0 {
		return dimStyle.Render("no skills yet — /skills add <name> creates one")
	}
	return table(append([][]string{{"HANDLE", "STATE", "SCOPE", "DESCRIPTION"}}, rows...)) + "\n\n" +
		dimStyle.Render("/skills show <handle> · /skills enable|disable|always|delete <handle>")
}

func renderSkillDetail(s client.Skill) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", sectionStyle.Render(firstNonEmpty(s.Name, s.Handle)))
	fmt.Fprintf(&b, "%s\n\n", dimStyle.Render(fmt.Sprintf("handle %s · scope %s · source %s · enabled %t · always %t",
		s.Handle, s.Scope, s.Source, s.Enabled, s.AlwaysUse)))
	if s.Description != "" {
		b.WriteString(s.Description + "\n\n")
	}
	if s.Content != "" {
		b.WriteString(clamp(s.Content, 60))
	}
	return strings.TrimRight(b.String(), "\n")
}

// --- agents ---

func renderAgents(agents []client.AgentDef, filter string) string {
	var rows [][]string
	for _, a := range agents {
		if !filterMatch(filter, a.Name, a.Key, a.Description) {
			continue
		}
		rows = append(rows, []string{truncate(a.Name, 24), a.Scope, truncate(a.Model, 22), truncate(a.Description, 40)})
	}
	if len(rows) == 0 {
		return dimStyle.Render("no agent definitions — /agents generate <description> creates one")
	}
	return table(append([][]string{{"NAME", "SCOPE", "MODEL", "DESCRIPTION"}}, rows...)) + "\n\n" +
		dimStyle.Render("/agents metrics · /agents generate <description> · /agents delete <name>")
}

func renderAgentMetrics(metrics []client.AgentMetric, best, cheapest *client.AgentRecommendation) string {
	var b strings.Builder
	if len(metrics) == 0 {
		b.WriteString(dimStyle.Render("no agent performance data yet") + "\n")
	} else {
		rows := [][]string{{"AGENT", "TASK TYPE", "OK", "FAIL", "AVG MS", "QUALITY"}}
		for _, mt := range metrics {
			rows = append(rows, []string{
				shortID(mt.AgentConfigID), truncate(mt.TaskType, 20),
				fmt.Sprint(mt.SuccessCount), fmt.Sprint(mt.FailureCount),
				fmt.Sprint(mt.AvgDurationMs), fmt.Sprintf("%.2f", mt.AvgQualityScore),
			})
		}
		b.WriteString(table(rows) + "\n")
	}
	if best != nil && best.AgentName() != "" {
		fmt.Fprintf(&b, "\n%s %s", sectionStyle.Render("best:"), best.AgentName())
	}
	if cheapest != nil && cheapest.AgentName() != "" {
		fmt.Fprintf(&b, "\n%s %s", sectionStyle.Render("cheapest:"), cheapest.AgentName())
	}
	return strings.TrimRight(b.String(), "\n")
}

// --- models ---

func renderModels(list []client.LLMModel, filter string) string {
	var rows [][]string
	for _, mo := range list {
		if !filterMatch(filter, mo.Name, mo.Model, mo.Provider) {
			continue
		}
		rows = append(rows, []string{truncate(mo.Name, 26), mo.Provider, truncate(mo.Model, 30)})
	}
	if len(rows) == 0 {
		return dimStyle.Render("no models configured — add a model via the web UI or API")
	}
	return table(append([][]string{{"NAME", "PROVIDER", "MODEL"}}, rows...)) + "\n\n" +
		dimStyle.Render("/models default <name> · /models delete <name> · /models capacity")
}

func renderModelCapacity(caps []client.ModelCapacity) string {
	if len(caps) == 0 {
		return dimStyle.Render("no model capacity data")
	}
	rows := [][]string{{"MODEL", "RUNNING", "MAX", "FREE", "LOAD"}}
	for _, c := range caps {
		load := ""
		if c.MaxWorkers > 0 {
			load = bar(float64(c.Running), float64(c.MaxWorkers), 16)
		}
		rows = append(rows, []string{
			truncate(firstNonEmpty(c.Name, c.Model), 26),
			fmt.Sprint(c.Running), fmt.Sprint(c.MaxWorkers), fmt.Sprint(c.AvailableSlots), load,
		})
	}
	return table(rows)
}

// --- workers ---

func renderWorkers(capacity *client.GlobalCapacity, page string) string {
	var b strings.Builder
	if capacity != nil {
		fmt.Fprintf(&b, "%s\n", sectionStyle.Render("Pool"))
		fmt.Fprintf(&b, "  %d running / %d max · %d queued · %d free\n  %s\n\n",
			capacity.TotalRunning, capacity.MaxWorkers, capacity.QueueSize, capacity.AvailableSlots,
			bar(float64(capacity.TotalRunning), float64(capacity.MaxWorkers), 30))
	}
	if s := strings.TrimSpace(page); s != "" {
		b.WriteString(clamp(s, 40) + "\n\n")
	}
	b.WriteString(dimStyle.Render("/workers limit <n> sets the global cap"))
	return b.String()
}

// --- projects ---

func renderProjects(projects []client.Project, caps []client.ProjectCapacity, selectedID string) string {
	if len(projects) == 0 {
		return dimStyle.Render("no projects")
	}
	byID := map[string]client.ProjectCapacity{}
	for _, c := range caps {
		byID[c.ID] = c
	}
	rows := [][]string{{"", "NAME", "RUNNING", "QUEUED", "PATH"}}
	for _, p := range projects {
		mark := " "
		if p.ID == selectedID {
			mark = statusOKStyle.Render("●")
		}
		running, queued := "—", "—"
		if c, ok := byID[p.ID]; ok {
			running, queued = fmt.Sprint(c.Running), fmt.Sprint(c.QueueSize)
		}
		rows = append(rows, []string{mark, truncate(p.Name, 28), running, queued, truncate(p.Path, 40)})
	}
	return table(rows) + "\n\n" + dimStyle.Render("/project <name> switches the active project")
}

// --- help ---

func renderHelp() string {
	var b strings.Builder
	b.WriteString(dimStyle.Render("Type a message to talk to the project agent. Lines starting with / are commands.") + "\n\n")

	// Command and description in aligned columns, with the full action list on
	// a second indented line. Inlining actions as a third column makes the
	// table ~190 columns wide; truncating them would hide real commands.
	width := 0
	for _, c := range commands {
		if n := lipgloss.Width(c.summary()); n > width {
			width = n
		}
	}
	const actionsWidth = 74
	for _, c := range commands {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, c.summary(), c.desc)
		for _, line := range wrapJoin(c.actions, " · ", actionsWidth) {
			fmt.Fprintf(&b, "  %-*s  %s\n", width, "", dimStyle.Render(line))
		}
	}
	b.WriteString("\n\n" + dimStyle.Render("keys: tab complete · ↑↓ history · pgup/pgdn scroll · ctrl+l clear · ctrl+c quit"))
	b.WriteString("\n" + dimStyle.Render(cmdPrefix+"help <command> shows the full syntax of one command"))
	return b.String()
}

// wrapJoin joins items with sep, breaking into lines no wider than width.
func wrapJoin(items []string, sep string, width int) []string {
	var lines []string
	cur := ""
	for _, it := range items {
		switch {
		case cur == "":
			cur = it
		case lipgloss.Width(cur+sep+it) <= width:
			cur += sep + it
		default:
			lines = append(lines, cur)
			cur = it
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

func renderCommandHelp(c command) string {
	var b strings.Builder
	b.WriteString(c.desc + "\n\n")

	// Concrete per-action syntax when we have it; the action list alone isn't
	// enough to actually use a command like "/tasks move".
	if len(c.usage) > 0 {
		for _, u := range c.usage {
			fmt.Fprintf(&b, "  %s%s\n", cmdPrefix, u)
		}
	} else {
		fmt.Fprintf(&b, "  %s\n", c.label())
	}
	if len(c.examples) > 0 {
		fmt.Fprintf(&b, "\n%s\n", dimStyle.Render("examples:"))
		for _, ex := range c.examples {
			fmt.Fprintf(&b, "  %s\n", dimStyle.Render(cmdPrefix+ex))
		}
	}
	if len(c.aliases) > 0 {
		fmt.Fprintf(&b, "\n  aliases: %s\n", strings.Join(c.aliases, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// --- analytics ---

// loadAnalytics fetches and renders one analytics section (or all of them).
func loadAnalytics(ctx context.Context, c *client.Client, projectID, section string) (string, error) {
	want := func(name string) bool { return section == "" || section == name }

	type slot struct {
		name string
		out  string
		err  error
	}

	names := []string{"usage", "rates", "agents", "trends", "frequent", "failures", "skills"}
	slots := make([]slot, len(names))
	for i, n := range names {
		slots[i].name = n
	}

	var wg sync.WaitGroup
	for i, name := range names {
		if !want(name) {
			continue
		}
		i, name := i, name
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch name {
			case "usage":
				if u, err := c.GetUsageAnalytics(ctx, projectID); err == nil {
					slots[i].out = renderUsage(u) + "\n\n"
				} else {
					slots[i].err = err
				}
			case "rates":
				if r, err := c.GetSuccessFailureRates(ctx, projectID); err == nil {
					slots[i].out = renderRates(r) + "\n\n"
				} else {
					slots[i].err = err
				}
			case "agents":
				if a, err := c.GetAvgExecutionTimeByAgent(ctx, projectID); err == nil {
					slots[i].out = renderExecTimes("Avg execution time by agent", a) + "\n\n"
				} else {
					slots[i].err = err
				}
			case "trends":
				if t, err := c.GetAvgExecutionTimeByTask(ctx, projectID); err == nil {
					slots[i].out = renderExecTimes("Avg execution time by task", t) + "\n\n"
				} else {
					slots[i].err = err
				}
			case "frequent":
				if f, err := c.GetMostFrequentTasks(ctx, projectID); err == nil {
					slots[i].out = renderFrequent(f) + "\n\n"
				} else {
					slots[i].err = err
				}
			case "failures":
				if f, err := c.GetFailedTaskPatterns(ctx, projectID); err == nil {
					slots[i].out = renderFailures(f) + "\n\n"
				} else {
					slots[i].err = err
				}
			case "skills":
				if s, err := c.GetSkillAnalytics(ctx, projectID); err == nil {
					slots[i].out = renderSkillAnalytics(s) + "\n\n"
				} else {
					slots[i].err = err
				}
			}
		}()
	}
	wg.Wait()

	var b strings.Builder
	for _, s := range slots {
		if !want(s.name) {
			continue
		}
		if s.err != nil {
			if section == s.name {
				return "", s.err
			}
			continue
		}
		b.WriteString(s.out)
	}

	out := strings.TrimRight(b.String(), "\n")
	if out == "" {
		return "", fmt.Errorf("no analytics available")
	}
	return out, nil
}

func renderUsage(u *client.UsageAnalytics) string {
	var b strings.Builder
	b.WriteString(sectionStyle.Render("Usage & cost") + "\n")
	t := u.Totals
	fmt.Fprintf(&b, "  %d calls · %s in / %s out · %s total tokens",
		t.CallCount, humanInt(t.InputTokens), humanInt(t.OutputTokens), humanInt(t.TotalTokens))
	if t.CostAvailable {
		fmt.Fprintf(&b, " · $%.2f", t.CostUSD)
	}
	b.WriteString("\n")

	if len(u.ModelBreakdown) > 0 {
		b.WriteString("\n" + dimStyle.Render("  by model") + "\n")
		maxTok := 0.0
		for _, mp := range u.ModelBreakdown {
			if float64(mp.TotalTokens) > maxTok {
				maxTok = float64(mp.TotalTokens)
			}
		}
		rows := [][]string{{"MODEL", "CALLS", "TOKENS", "COST", "SHARE"}}
		for _, mp := range u.ModelBreakdown {
			rows = append(rows, []string{
				truncate(mp.Model, 26), fmt.Sprint(mp.CallCount), humanInt(mp.TotalTokens),
				fmt.Sprintf("$%.2f", mp.CostUSD),
				bar(float64(mp.TotalTokens), maxTok, 14) + fmt.Sprintf(" %5.1f%%", mp.Percent),
			})
		}
		b.WriteString(indent(table(rows), "  ") + "\n")
	}

	for _, a := range u.AccountLimits {
		fmt.Fprintf(&b, "\n  %s %s\n", sectionStyle.Render(a.Provider), dimStyle.Render(a.PlanType+" "+a.StatusLabel))
		if a.Error != "" {
			fmt.Fprintf(&b, "    %s\n", statusErrStyle.Render(a.Error))
		}
		for _, l := range a.Limits {
			fmt.Fprintf(&b, "    %-22s %s %5.1f%%  %s\n",
				truncate(l.Label, 22), bar(l.UsedPercent, 100, 16), l.UsedPercent, dimStyle.Render(l.ResetsAt))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderRates(rates []client.SuccessFailureRate) string {
	if len(rates) == 0 {
		return sectionStyle.Render("Success / failure") + "\n  " + dimStyle.Render("no data")
	}
	var b strings.Builder
	b.WriteString(sectionStyle.Render("Success / failure by period") + "\n")
	for _, r := range rates {
		ok := 0
		if r.TotalCount > 0 {
			ok = int(float64(r.SuccessCount) / float64(r.TotalCount) * 20)
		}
		gauge := statusOKStyle.Render(strings.Repeat("█", ok)) +
			statusErrStyle.Render(strings.Repeat("█", 20-ok))
		fmt.Fprintf(&b, "  %-12s %s  %5.1f%%  %s\n",
			r.Period, gauge, r.SuccessRate,
			dimStyle.Render(fmt.Sprintf("%d ok / %d fail", r.SuccessCount, r.FailureCount)))
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderExecTimes(title string, times []client.AvgExecutionTime) string {
	if len(times) == 0 {
		return sectionStyle.Render(title) + "\n  " + dimStyle.Render("no data")
	}
	sorted := append([]client.AvgExecutionTime(nil), times...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].AvgMs > sorted[j].AvgMs })
	if len(sorted) > 12 {
		sorted = sorted[:12]
	}
	maxMs := sorted[0].AvgMs

	var b strings.Builder
	b.WriteString(sectionStyle.Render(title) + "\n")
	for _, t := range sorted {
		fmt.Fprintf(&b, "  %-26s %s %8s %s\n",
			truncate(firstNonEmpty(t.Name, t.ID), 26), bar(t.AvgMs, maxMs, 18),
			humanMs(t.AvgMs), dimStyle.Render(fmt.Sprintf("n=%d", t.Count)))
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderFrequent(tasks []client.TaskFrequency) string {
	if len(tasks) == 0 {
		return sectionStyle.Render("Most frequent tasks") + "\n  " + dimStyle.Render("no data")
	}
	maxCount := 0
	for _, t := range tasks {
		if t.ExecutionCount > maxCount {
			maxCount = t.ExecutionCount
		}
	}
	var b strings.Builder
	b.WriteString(sectionStyle.Render("Most frequent tasks") + "\n")
	for _, t := range tasks {
		fmt.Fprintf(&b, "  %-34s %s %4d\n",
			truncate(t.TaskTitle, 34), bar(float64(t.ExecutionCount), float64(maxCount), 18), t.ExecutionCount)
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderFailures(patterns []client.FailedTaskPattern) string {
	if len(patterns) == 0 {
		return sectionStyle.Render("Failure patterns") + "\n  " + statusOKStyle.Render("no failing tasks")
	}
	var b strings.Builder
	b.WriteString(sectionStyle.Render("Failure patterns") + "\n")
	for _, p := range patterns {
		fmt.Fprintf(&b, "  %s %s\n", statusErrStyle.Render(fmt.Sprintf("%dx", p.FailureCount)), truncate(p.TaskTitle, 50))
		if p.LastError != "" {
			fmt.Fprintf(&b, "      %s\n", dimStyle.Render(truncate(p.LastError, 70)))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderSkillAnalytics(s *client.SkillAnalytics) string {
	var b strings.Builder
	b.WriteString(sectionStyle.Render("Skill usage") + "\n")
	if len(s.TopSkills) == 0 {
		b.WriteString("  " + dimStyle.Render("no skill activity") + "\n")
	} else {
		maxAct := 0
		for _, t := range s.TopSkills {
			if t.ActivityCount > maxAct {
				maxAct = t.ActivityCount
			}
		}
		for _, t := range s.TopSkills {
			fmt.Fprintf(&b, "  %-26s %s %4d  %s\n",
				truncate(t.SkillHandle, 26), bar(float64(t.ActivityCount), float64(maxAct), 16),
				t.ActivityCount, dimStyle.Render(fmt.Sprintf("%.0f%% follow-through", t.FollowThroughRate*100)))
		}
	}
	if len(s.Underused) > 0 {
		b.WriteString("\n" + dimStyle.Render("  underused") + "\n")
		for _, u := range s.Underused {
			fmt.Fprintf(&b, "    %-26s %s\n", truncate(u.SkillHandle, 26),
				dimStyle.Render(fmt.Sprintf("%d uses", u.ActivityCount)))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func humanInt(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprint(n)
	}
}

func humanMs(ms float64) string {
	switch {
	case ms >= 60_000:
		return fmt.Sprintf("%.1fm", ms/60_000)
	case ms >= 1_000:
		return fmt.Sprintf("%.1fs", ms/1_000)
	default:
		return fmt.Sprintf("%.0fms", ms)
	}
}
