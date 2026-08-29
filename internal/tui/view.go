package tui

// Rendering: the chat window chrome plus the block renderers that slash
// commands emit into the transcript.

import (
	"context"
	"encoding/json"
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

type connectionPhase uint8

const (
	connectionPhaseConnecting connectionPhase = iota
	connectionPhaseOnline
	connectionPhaseOffline
)

func (m Model) connectionPhase() connectionPhase {
	switch {
	case m.connected:
		return connectionPhaseOnline
	case m.connChecked || m.connErr != "":
		return connectionPhaseOffline
	default:
		return connectionPhaseConnecting
	}
}

func (m Model) renderHeader() string {
	left := titleStyle.Render("OpenVibely")

	project := "no project"
	if m.selectedName != "" {
		project = m.selectedName
	}

	phase := m.connectionPhase()
	conn := noticeStyle.Render("● connecting")
	if m.authRequired {
		conn = noticeStyle.Render("● sign-in required")
	} else {
		switch phase {
		case connectionPhaseOnline:
			conn = statusOKStyle.Render("● online")
		case connectionPhaseOffline:
			conn = statusErrStyle.Render("● offline")
		}
	}
	stream := ""
	if m.showEvents {
		if m.authRequired {
			stream = noticeStyle.Render(" ⚡sign-in")
		} else if m.sseConnected {
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
	if m.loginActive {
		if m.loginSubmitting {
			return "signing in…"
		}
		if m.loginPassword {
			return "enter password · Enter sign in · Esc cancel"
		}
		return "enter username · Enter continue · Esc cancel"
	}
	if m.selectorActive {
		return "type to filter · ↑↓ choose · enter select · esc cancel"
	}
	if len(m.menu) > 0 {
		return "tab complete · ↑↓ choose · enter run · esc close"
	}
	phase := m.connectionPhase()
	if m.authRequired && phase != connectionPhaseOnline {
		return "sign-in required: /login · help works without backend"
	}
	switch phase {
	case connectionPhaseOffline:
		if m.connErr != "" {
			return "offline: start/check backend · set -server or OPENVIBELY_SERVER_URL · /status"
		}
	case connectionPhaseConnecting:
		return "connecting: /help works offline · check -server or OPENVIBELY_SERVER_URL if this stays here"
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

	phase := m.connectionPhase()
	if m.authRequired {
		row("server", noticeStyle.Render("sign-in required")+dimStyle.Render(" "+m.client.BaseURL()))
		if m.connErr != "" {
			row("network", statusErrStyle.Render("offline"))
			row("error", m.connErr)
			row("try", "start/check your local backend, then run /status")
		}
		row("try", "use /login to enter credentials")
		row("try", "help remains available without a backend")
	} else {
		switch phase {
		case connectionPhaseOnline:
			row("server", statusOKStyle.Render("connected")+dimStyle.Render(" "+m.client.BaseURL()))
		case connectionPhaseConnecting:
			row("server", noticeStyle.Render("connecting")+dimStyle.Render(" "+m.client.BaseURL()))
		case connectionPhaseOffline:
			row("server", statusErrStyle.Render("offline")+dimStyle.Render(" "+m.client.BaseURL()))
			if m.connErr != "" {
				row("error", m.connErr)
			}
			row("try", "start/check your local backend, then run /status")
			row("try", "set -server <url> or OPENVIBELY_SERVER_URL")
		}
	}
	switch {
	case m.authRequired:
		row("auth", noticeStyle.Render("sign-in required · /login"))
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
		if m.authRequired {
			row("events", noticeStyle.Render("sign-in required · /login"))
		} else if m.sseConnected {
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
	nonFinalCells := 0
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
		if len(r) > 1 {
			nonFinalCells += len(r) - 1
		}
	}
	widths := make([]int, cols)
	cachedWidths := make([]int, 0, nonFinalCells)
	for _, r := range rows {
		for i, cell := range r {
			w := lipgloss.Width(cell)
			if i < len(r)-1 {
				cachedWidths = append(cachedWidths, w)
			}
			if w > widths[i] {
				widths[i] = w
			}
		}
	}
	var b strings.Builder
	cachedWidth := 0
	for ri, r := range rows {
		var line strings.Builder
		for i, cell := range r {
			if i == len(r)-1 {
				line.WriteString(cell)
				break
			}
			cellWidth := cachedWidths[cachedWidth]
			cachedWidth++
			line.WriteString(cell)
			line.WriteString(strings.Repeat(" ", widths[i]-cellWidth+2))
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
			if err := d.TabError(tab); err != nil {
				body = renderTaskDetailLoadFailure(meta.Label, err)
			} else {
				body = meta.Text(d)
			}
		}
		fmt.Fprintf(&b, "%s\n%s\n", sectionStyle.Render(label), textOrDash(body))
		return b.String()
	}

	for _, meta := range client.TaskDetailTabs() {
		if err := d.TabError(meta.Name); err != nil {
			fmt.Fprintf(&b, "%s\n%s\n\n", sectionStyle.Render("▸ "+meta.Label), renderTaskDetailLoadFailure(meta.Label, err))
			continue
		}
		body := strings.TrimSpace(meta.Text(d))
		if body == "" {
			continue
		}
		fmt.Fprintf(&b, "%s\n%s\n\n", sectionStyle.Render("▸ "+meta.Label), clamp(body, 40))
	}
	b.WriteString(dimStyle.Render("/tasks show <id> <" + detailTabHintList() + ">"))
	return b.String()
}

func renderTaskDetailLoadFailure(label string, err error) string {
	return statusErrStyle.Render(fmt.Sprintf("failed to load %s: %v", strings.ToLower(label), err))
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

func renderTaskAttachments(attachments []client.Attachment) string {
	if len(attachments) == 0 {
		return dimStyle.Render("no attachments yet — /tasks attachments add <task> <file> uploads one")
	}
	rows := [][]string{{"ID", "FILE", "SIZE"}}
	for _, attachment := range attachments {
		rows = append(rows, []string{
			firstNonEmpty(attachment.ID, "—"),
			firstNonEmpty(attachment.FileName, "(unnamed)"),
			attachmentSizeText(attachment.FileSize),
		})
	}
	return table(rows) + "\n\n" + dimStyle.Render("/tasks attachments delete <task> <id|filename>")
}

func attachmentSizeText(size int64) string {
	const unit = int64(1024)
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := unit, 0
	for n := size / unit; n >= unit && exp < 5; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGTPE"[exp])
}

func renderLifecycleExecutions(task client.Task, executions []client.LifecycleExecution) string {
	var b strings.Builder
	title := firstNonEmpty(task.Title, shortID(task.ID))
	fmt.Fprintf(&b, "%s  (id %s)\n", sectionStyle.Render(title), task.ID)
	if len(executions) == 0 {
		b.WriteString(dimStyle.Render("no executions for this task"))
		return b.String()
	}

	rows := [][]string{{"ID", "SKILL", "WHEN", "STATUS", "STARTED"}}
	for _, execution := range executions {
		rows = append(rows, []string{
			firstNonEmpty(execution.ID, "—"),
			firstNonEmpty(execution.SkillKey, "—"),
			firstNonEmpty(execution.When, "—"),
			firstNonEmpty(execution.Status, "—"),
			firstNonEmpty(execution.StartedAt, "—"),
		})
	}
	b.WriteString(table(rows))
	return b.String()
}

func renderLifecycleEvents(task client.Task, execution client.LifecycleExecution, events []client.LifecycleEvent) string {
	var b strings.Builder
	taskTitle := firstNonEmpty(task.Title, shortID(task.ID))
	fmt.Fprintf(&b, "%s  (id %s)\n", sectionStyle.Render(taskTitle), task.ID)
	fmt.Fprintf(&b, "execution %s", firstNonEmpty(execution.ID, "(unnamed)"))
	if execution.SkillKey != "" {
		b.WriteString(" · " + execution.SkillKey)
	}
	if execution.Status != "" {
		b.WriteString(" · " + execution.Status)
	}
	b.WriteString("\n\n")

	if len(events) == 0 {
		b.WriteString(dimStyle.Render("no events for this execution"))
		return b.String()
	}

	ordered := append([]client.LifecycleEvent(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Seq != ordered[j].Seq {
			return ordered[i].Seq < ordered[j].Seq
		}
		if ordered[i].CreatedAt != ordered[j].CreatedAt {
			return ordered[i].CreatedAt < ordered[j].CreatedAt
		}
		return ordered[i].ID < ordered[j].ID
	})

	rows := [][]string{{"SEQ", "TIMESTAMP", "EVENT TYPE", "PAYLOAD"}}
	for _, event := range ordered {
		rows = append(rows, []string{
			fmt.Sprintf("%d", event.Seq),
			firstNonEmpty(event.CreatedAt, "—"),
			firstNonEmpty(event.EventType, "—"),
			lifecyclePayloadSummary(event.Payload),
		})
	}
	b.WriteString(table(rows))
	return b.String()
}

func lifecyclePayloadSummary(payload map[string]any) string {
	if len(payload) == 0 {
		return "—"
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "<unavailable>"
	}
	return truncate(string(encoded), 96)
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

// --- automations ---

func renderAutomations(automations []client.Automation, filter string) string {
	rows := [][]string{{"ID", "NAME", "STATE"}}
	for _, a := range automations {
		if !filterMatch(filter, a.ID, a.Name, a.State) {
			continue
		}
		name := firstNonEmpty(a.Name, "(unnamed)")
		state := firstNonEmpty(a.State, "unknown")
		rows = append(rows, []string{a.ID, truncate(name, 52), state})
	}
	if len(rows) == 1 {
		if filter != "" {
			return dimStyle.Render("no automations match " + filter)
		}
		return dimStyle.Render("no automations yet — create one via the web UI")
	}
	return table(rows) + "\n\n" +
		dimStyle.Render("/automations run-now|pause|resume|delete <id|name>")
}

func renderAutomationDetail(detail client.AutomationDetail) string {
	var b strings.Builder
	name := firstNonEmpty(detail.Automation.Name, detail.Automation.ID, "(unnamed automation)")
	fmt.Fprintf(&b, "%s\n", sectionStyle.Render("Automation: "+name))
	fmt.Fprintf(&b, "%s\n", dimStyle.Render("ID "+firstNonEmpty(detail.Automation.ID, "not reported")))

	b.WriteString("\n" + sectionStyle.Render("Metadata") + "\n")
	automationDetailField(&b, "project", detail.Automation.ProjectID)
	automationDetailField(&b, "stable key", detail.Automation.StableKey)
	automationDetailField(&b, "type", detail.Automation.AutomationType)
	automationDetailField(&b, "lifecycle", detail.Automation.LifecycleState)
	automationDetailField(&b, "health", detail.Automation.HealthState)
	if detail.Automation.HealthReason != "" {
		automationDetailField(&b, "health reason", detail.Automation.HealthReason)
	}
	if detail.Automation.Description != "" {
		automationDetailField(&b, "description", detail.Automation.Description)
	}

	b.WriteString("\n" + sectionStyle.Render("Version") + "\n")
	versionReported := detail.Version.ID != "" || detail.Version.Version != 0 || detail.Version.State != "" ||
		detail.Version.Source != "" || detail.Version.AdapterKey != "" || detail.Version.SchemaVersion != 0 ||
		detail.Version.CreatedAt != "" || detail.Version.PublishedAt != ""
	if !versionReported {
		b.WriteString(dimStyle.Render("  not reported") + "\n")
	} else {
		version := "not reported"
		if detail.Version.Version != 0 {
			version = fmt.Sprintf("v%d", detail.Version.Version)
		}
		if detail.Version.State != "" {
			version += " · " + formatAutomationDetailState(detail.Version.State)
		}
		automationDetailField(&b, "version", version)
		automationDetailField(&b, "version ID", detail.Version.ID)
		automationDetailField(&b, "source", detail.Version.Source)
		automationDetailField(&b, "adapter", detail.Version.AdapterKey)
		if detail.Version.SchemaVersion != 0 {
			automationDetailField(&b, "schema version", fmt.Sprintf("%d", detail.Version.SchemaVersion))
		}
	}

	b.WriteString("\n" + sectionStyle.Render("Graph") + "\n")
	if !detail.GraphAvailable {
		reason := "live graph was not returned"
		if automationDetailIsDraft(detail) {
			reason = "draft automation has no live graph"
		}
		b.WriteString(dimStyle.Render("  unavailable — "+reason) + "\n")
		b.WriteString(dimStyle.Render("  nodes: unavailable · edges: unavailable") + "\n")
	} else {
		nodeSummary := "not reported"
		if detail.NodesAvailable {
			nodeSummary = fmt.Sprintf("%d", len(detail.Nodes))
		}
		edgeSummary := "not reported"
		if detail.EdgesAvailable {
			edgeSummary = fmt.Sprintf("%d", len(detail.Edges))
		}
		fmt.Fprintf(&b, "  %s nodes · %s edges\n", nodeSummary, edgeSummary)
		renderAutomationDetailNodes(&b, detail)
		renderAutomationDetailEdges(&b, detail)
	}

	b.WriteString("\n" + sectionStyle.Render("Runtime") + "\n")
	automationDetailField(&b, "active invocations", automationDetailCount(detail.ActiveInvocations, detail.ActiveInvocationsAvailable))
	automationDetailField(&b, "active work items", automationDetailCount(detail.ActiveWorkItems, detail.ActiveWorkItemsAvailable))

	b.WriteString("\n" + sectionStyle.Render("Resources") + "\n")
	if !detail.ResourcesAvailable {
		b.WriteString(dimStyle.Render("  not reported — optional section unavailable") + "\n")
	} else if len(detail.Resources) == 0 {
		b.WriteString(dimStyle.Render("  (empty)") + "\n")
	} else {
		resources := append([]client.AutomationResourceSummary(nil), detail.Resources...)
		sort.SliceStable(resources, func(i, j int) bool {
			return automationDetailResourceSortKey(resources[i]) < automationDetailResourceSortKey(resources[j])
		})
		rows := [][]string{{"NODE", "TYPE", "RESOURCE", "RELATION", "STATUS"}}
		for _, resource := range resources {
			rows = append(rows, []string{
				firstNonEmpty(resource.NodeKey, resource.NodeID, "—"),
				firstNonEmpty(resource.ResourceType, "—"),
				firstNonEmpty(resource.Name, resource.ResourceID, "—"),
				firstNonEmpty(resource.Relation, "—"),
				firstNonEmpty(resource.Status, "—"),
			})
		}
		b.WriteString(indentAutomationDetailTable(table(rows)))
		b.WriteByte('\n')
	}

	b.WriteString("\n" + sectionStyle.Render("External state") + "\n")
	if !detail.ExternalStateAvailable {
		b.WriteString(dimStyle.Render("  not reported — optional section unavailable") + "\n")
	} else {
		status := "not reported"
		if detail.ExternalState.Status != "" {
			status = formatAutomationDetailState(detail.ExternalState.Status)
		} else if detail.ExternalState.StaleAvailable && !detail.ExternalState.StatusInvalid {
			status = "fresh"
			if detail.ExternalState.Stale {
				status = "stale"
			}
		}
		automationDetailField(&b, "status", status)
		automationDetailField(&b, "tracked resources", automationDetailCount(detail.ExternalState.TrackedResources, detail.ExternalState.TrackedResourcesAvailable))
		automationDetailField(&b, "last updated", detail.ExternalState.LastUpdatedAt)
	}

	if detail.Partial || len(detail.Warnings) > 0 {
		b.WriteString("\n" + sectionStyle.Render("Notes") + "\n")
		if detail.Partial {
			b.WriteString(dimStyle.Render("  partial detail — unavailable optional sections are not treated as empty") + "\n")
		}
		for _, warning := range detail.Warnings {
			if strings.TrimSpace(warning) != "" {
				b.WriteString(dimStyle.Render("  "+warning) + "\n")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderAutomationDetailNodes(b *strings.Builder, detail client.AutomationDetail) {
	b.WriteString("  " + sectionStyle.Render("Nodes") + "\n")
	if !detail.NodesAvailable {
		b.WriteString(dimStyle.Render("    unavailable — node section was not returned") + "\n")
		return
	}
	if len(detail.Nodes) == 0 {
		b.WriteString(dimStyle.Render("    (empty)") + "\n")
		return
	}
	nodes := append([]client.AutomationLiveNode(nil), detail.Nodes...)
	sort.SliceStable(nodes, func(i, j int) bool {
		return automationDetailNodeSortKey(nodes[i]) < automationDetailNodeSortKey(nodes[j])
	})
	rows := [][]string{{"NODE", "STATE", "RUN", "WAIT", "BLOCK", "FAIL", "RECENT"}}
	legacyNodeCounts := detail.NodeCountsAvailable && !automationDetailHasNodeCountAvailability(detail)
	for _, node := range nodes {
		state := formatAutomationNodeState(firstNonEmpty(node.DisplayState, "not reported"))
		counts := node.Counts
		countCells := []string{
			automationDetailTableCount(counts.Running, counts.RunningAvailable || legacyNodeCounts),
			automationDetailTableCount(counts.Waiting, counts.WaitingAvailable || legacyNodeCounts),
			automationDetailTableCount(counts.Blocked, counts.BlockedAvailable || legacyNodeCounts),
			automationDetailTableCount(counts.Failed, counts.FailedAvailable || legacyNodeCounts),
			automationDetailTableCount(counts.CompletedRecently, counts.CompletedRecentlyAvailable || legacyNodeCounts),
		}
		rows = append(rows, append([]string{firstNonEmpty(node.Name, node.NodeKey, node.ID, "—"), state}, countCells...))
	}
	b.WriteString(indentAutomationDetailTable(table(rows)))
	b.WriteByte('\n')
	if len(detail.UnmatchedNodeDetails) > 0 {
		b.WriteString("  " + sectionStyle.Render("Unmatched node details") + "\n")
		b.WriteString(dimStyle.Render("    correlation unavailable; records retained separately") + "\n")
		details := append([]client.AutomationLiveNode(nil), detail.UnmatchedNodeDetails...)
		sort.SliceStable(details, func(i, j int) bool {
			return automationDetailNodeSortKey(details[i]) < automationDetailNodeSortKey(details[j])
		})
		detailRows := [][]string{{"NODE", "KEY", "ROLE", "TYPE"}}
		for _, node := range details {
			detailRows = append(detailRows, []string{
				firstNonEmpty(node.Name, node.NodeKey, node.ID, "—"),
				firstNonEmpty(node.NodeKey, node.ID, "—"),
				firstNonEmpty(node.Role, "—"),
				firstNonEmpty(node.NodeType, "—"),
			})
		}
		b.WriteString(indentAutomationDetailTable(table(detailRows)))
		b.WriteByte('\n')
	}
}

func renderAutomationDetailEdges(b *strings.Builder, detail client.AutomationDetail) {
	b.WriteString("  " + sectionStyle.Render("Edges") + "\n")
	if !detail.EdgesAvailable {
		b.WriteString(dimStyle.Render("    unavailable — edge section was not returned") + "\n")
		return
	}
	if len(detail.Edges) == 0 {
		b.WriteString(dimStyle.Render("    (empty)") + "\n")
		return
	}
	edges := append([]client.AutomationLiveEdge(nil), detail.Edges...)
	sort.SliceStable(edges, func(i, j int) bool {
		return automationDetailEdgeSortKey(edges[i]) < automationDetailEdgeSortKey(edges[j])
	})
	rows := [][]string{{"FROM", "TO", "LABEL", "TRANSITIONS", "RECENT"}}
	legacyEdgeCounts := detail.EdgeCountsAvailable && !automationDetailHasEdgeCountAvailability(detail)
	for _, edge := range edges {
		transitions := automationDetailTableCount(edge.TransitionCount, edge.TransitionCountAvailable || legacyEdgeCounts)
		recent := automationDetailTableCount(edge.RecentTransitionCount, edge.RecentTransitionCountAvailable || legacyEdgeCounts)
		rows = append(rows, []string{
			firstNonEmpty(edge.SourceName, edge.SourceNodeID, "—"),
			firstNonEmpty(edge.TargetName, edge.TargetNodeID, "—"),
			firstNonEmpty(edge.Label, edge.EdgeKey, "(unlabelled)"),
			transitions,
			recent,
		})
	}
	b.WriteString(indentAutomationDetailTable(table(rows)))
	b.WriteByte('\n')
}

func automationDetailField(b *strings.Builder, label, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "not reported"
	}
	fmt.Fprintf(b, "  %-18s %s\n", label+":", value)
}

func automationDetailCount(value int, available bool) string {
	if !available {
		return "not reported"
	}
	return fmt.Sprintf("%d", value)
}

func automationDetailTableCount(value int, available bool) string {
	if !available {
		return "—"
	}
	return fmt.Sprintf("%d", value)
}

func automationDetailHasNodeCountAvailability(detail client.AutomationDetail) bool {
	for _, node := range detail.Nodes {
		counts := node.Counts
		if counts.RunningAvailable || counts.WaitingAvailable || counts.BlockedAvailable || counts.FailedAvailable || counts.CompletedRecentlyAvailable {
			return true
		}
	}
	return false
}

func automationDetailHasEdgeCountAvailability(detail client.AutomationDetail) bool {
	for _, edge := range detail.Edges {
		if edge.TransitionCountAvailable || edge.RecentTransitionCountAvailable {
			return true
		}
	}
	return false
}

func formatAutomationNodeState(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "not reported"
	}
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(value, "_", " "), "-", " "))
	switch normalized {
	case "completed", "recent", "completed recently", "recently completed":
		return "recently completed"
	default:
		return normalized
	}
}

func formatAutomationDetailState(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "not reported"
	}
	return strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(value), "_", " "), "-", " ")
}

func automationDetailIsDraft(detail client.AutomationDetail) bool {
	return strings.EqualFold(detail.Automation.LifecycleState, "draft") || strings.EqualFold(detail.Version.State, "draft")
}

func automationDetailNodeSortKey(node client.AutomationLiveNode) string {
	return strings.ToLower(firstNonEmpty(node.NodeKey, node.Name, node.ID))
}

func automationDetailEdgeSortKey(edge client.AutomationLiveEdge) string {
	return strings.ToLower(strings.Join([]string{
		firstNonEmpty(edge.SourceName, edge.SourceNodeID),
		firstNonEmpty(edge.TargetName, edge.TargetNodeID),
		firstNonEmpty(edge.Label, edge.EdgeKey),
		edge.ID,
	}, "\x00"))
}

func automationDetailResourceSortKey(resource client.AutomationResourceSummary) string {
	return strings.ToLower(strings.Join([]string{
		resource.NodeID,
		resource.NodeKey,
		resource.ResourceType,
		resource.ResourceID,
		resource.Relation,
		resource.Name,
		resource.Status,
		resource.URL,
	}, "\x00"))
}

func indentAutomationDetailTable(value string) string {
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		lines[i] = "    " + line
	}
	return strings.Join(lines, "\n")
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

// --- personalities ---

// personalityKind returns the user-facing type for a personality.
func personalityKind(p client.Personality) string {
	if !p.IsPreset {
		return "custom"
	}
	if p.HasCustom {
		return "override"
	}
	return "built-in"
}

func renderPersonalities(personalities []client.Personality, filter string) string {
	rows := [][]string{{"KEY", "NAME", "TYPE", "DESCRIPTION", "PROMPT PREVIEW", "STATE"}}
	for _, p := range personalities {
		if !filterMatch(filter, p.Key, p.Name, p.Description, p.SystemPromptPreview) {
			continue
		}
		key := p.Key
		if key == "" {
			key = "(base)"
		}
		kind := personalityKind(p)
		state := ""
		if p.Active {
			state = "active"
		}
		rows = append(rows, []string{
			key,
			truncate(firstNonEmpty(p.Name, key), 28),
			kind,
			truncate(p.Description, 42),
			truncate(p.SystemPromptPreview, 56),
			state,
		})
	}
	if len(rows) == 1 {
		if filter != "" {
			return dimStyle.Render("no personalities match " + filter)
		}
		return dimStyle.Render("no personalities yet — /personality add <name> | <system prompt> creates one")
	}
	return table(rows) + "\n\n" +
		dimStyle.Render("/personality show <key|name> · /personality set <key|name> · /personality edit|delete <key|name>")
}

func renderPersonalityDetail(p client.Personality) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", sectionStyle.Render(firstNonEmpty(p.Name, p.Key, "Base")))

	kind := personalityKind(p)
	key := firstNonEmpty(p.Key, "(base)")
	fmt.Fprintf(&b, "%s\n", dimStyle.Render(fmt.Sprintf("key %s · %s%s", key, kind, func() string {
		if p.Active {
			return " · active"
		}
		return ""
	}())))
	if p.ID != "" {
		fmt.Fprintf(&b, "%s\n", dimStyle.Render("ID "+p.ID))
	}
	if p.Description != "" {
		b.WriteString("\n" + p.Description + "\n")
	}
	b.WriteString("\n" + sectionStyle.Render("System prompt") + "\n")
	if strings.TrimSpace(p.SystemPrompt) == "" {
		b.WriteString(dimStyle.Render("(no personality prompt applied.)"))
	} else {
		b.WriteString(strings.TrimRight(p.SystemPrompt, "\n"))
	}
	return strings.TrimRight(b.String(), "\n")
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

// renderModelCapacityWithUsage keeps the worker-capacity table independent from
// provider health. Analytics is best-effort here: a provider/account failure
// must not hide the capacity data that the command was asked to show.
func renderModelCapacityWithUsage(caps []client.ModelCapacity, usage *client.UsageAnalytics) string {
	capacity := renderModelCapacity(caps)
	if usage == nil || len(usage.AccountLimits) == 0 {
		return capacity + "\n\n" + dimStyle.Render("provider limits unavailable — run /analytics usage for details")
	}
	return capacity + "\n\n" + renderProviderLimits(usage.AccountLimits)
}

func renderProviderLimits(accounts []client.AccountUsage) string {
	var b strings.Builder
	b.WriteString(sectionStyle.Render("Provider limits"))
	for _, account := range accounts {
		provider := compactProviderText(firstNonEmpty(account.Provider, "unknown provider"))
		status := compactProviderText(account.StatusLabel)
		if status == "" && account.PrimaryLimit != nil {
			status = compactProviderText(account.PrimaryLimit.Status)
		}
		if status == "" {
			for _, limit := range account.Limits {
				status = compactProviderText(limit.Status)
				if status != "" {
					break
				}
			}
		}
		if status == "" {
			status = "status unavailable"
		}
		if plan := compactProviderText(account.PlanType); plan != "" {
			status += " · " + plan
		}
		fmt.Fprintf(&b, "\n  %-26s %s", truncate(provider, 26), dimStyle.Render(truncate(status, 40)))

		limits := providerLimitRows(account)
		if len(limits) == 0 {
			fmt.Fprintf(&b, "\n    %s", dimStyle.Render("quota unavailable"))
		}
		for _, limit := range limits {
			label := compactProviderText(firstNonEmpty(limit.Label, "quota"))
			reset := compactProviderText(limit.ResetsAt)
			if reset == "" {
				reset = "reset time unavailable"
			}
			fmt.Fprintf(&b, "\n    %-22s %5.1f%% used · reset %s",
				truncate(label, 22), limit.UsedPercent, dimStyle.Render(truncate(reset, 40)))
		}
		if errText := compactProviderText(account.Error); errText != "" {
			fmt.Fprintf(&b, "\n    %s", statusErrStyle.Render("error: "+truncate(errText, 100)))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func providerLimitRows(account client.AccountUsage) []client.AccountLimit {
	limits := make([]client.AccountLimit, 0, len(account.Limits)+1)
	seen := make(map[client.AccountLimit]struct{}, len(account.Limits)+1)
	appendUnique := func(limit client.AccountLimit) {
		if _, ok := seen[limit]; ok {
			return
		}
		seen[limit] = struct{}{}
		limits = append(limits, limit)
	}
	if account.PrimaryLimit != nil {
		appendUnique(*account.PrimaryLimit)
	}
	for _, limit := range account.Limits {
		appendUnique(limit)
	}
	return limits
}

// compactProviderText keeps provider diagnostics on one terminal-safe line.
// AccountDetail is intentionally not rendered: it may contain identifying or
// credential-adjacent data that is not needed for capacity troubleshooting.
func compactProviderText(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		default:
			return r
		}
	}, strings.TrimSpace(s))
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
	b.WriteString("\n\n" + dimStyle.Render(cliProjectSelectionHint))
	b.WriteString("\n" + dimStyle.Render("keys: tab complete · ↑↓ history · pgup/pgdn scroll · ctrl+l clear · ctrl+c quit"))
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
	if len(c.usage) > 0 || len(c.actionUsages) > 0 {
		for _, u := range c.usage {
			fmt.Fprintf(&b, "  %s%s\n", cmdPrefix, u)
		}
		for _, u := range c.actionUsages {
			fmt.Fprintf(&b, "  %s%s\n", cmdPrefix, u.helpLine(c.name))
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
		for _, l := range providerLimitRows(a) {
			fmt.Fprintf(&b, "    %-22s %s %5.1f%%  %s\n",
				truncate(l.Label, 22), bar(l.UsedPercent, 100, 16), l.UsedPercent, dimStyle.Render(l.ResetsAt))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderAnalyticsNoData(title string) string {
	return sectionStyle.Render(title) + "\n  " + dimStyle.Render("no data")
}

func renderRates(rates []client.SuccessFailureRate) string {
	if len(rates) == 0 {
		return renderAnalyticsNoData("Success / failure")
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

const maxExecTimeRows = 12

type execTimeCandidate struct {
	value client.AvgExecutionTime
	index int
}

func execTimeBefore(a, b execTimeCandidate) bool {
	if a.value.AvgMs != b.value.AvgMs {
		return a.value.AvgMs > b.value.AvgMs
	}
	return a.index < b.index
}

func execTimeWorse(a, b execTimeCandidate) bool {
	if a.value.AvgMs != b.value.AvgMs {
		return a.value.AvgMs < b.value.AvgMs
	}
	return a.index > b.index
}

// selectTopExecTimes keeps the highest-valued execution times in a bounded
// candidate slice. Earlier input records win equal-value ties so selection and
// rendering remain deterministic without mutating the caller's slice.
func selectTopExecTimes(times []client.AvgExecutionTime) []execTimeCandidate {
	limit := len(times)
	if limit > maxExecTimeRows {
		limit = maxExecTimeRows
	}
	selected := make([]execTimeCandidate, 0, limit)
	for index, value := range times {
		candidate := execTimeCandidate{value: value, index: index}
		if len(selected) < limit {
			selected = append(selected, candidate)
			continue
		}

		worst := 0
		for i := 1; i < len(selected); i++ {
			if execTimeWorse(selected[i], selected[worst]) {
				worst = i
			}
		}
		if execTimeBefore(candidate, selected[worst]) {
			selected[worst] = candidate
		}
	}

	sort.Slice(selected, func(i, j int) bool {
		return execTimeBefore(selected[i], selected[j])
	})
	return selected
}

func renderExecTimes(title string, times []client.AvgExecutionTime) string {
	if len(times) == 0 {
		return renderAnalyticsNoData(title)
	}
	selected := selectTopExecTimes(times)
	maxMs := selected[0].value.AvgMs

	var b strings.Builder
	b.WriteString(sectionStyle.Render(title) + "\n")
	for _, candidate := range selected {
		t := candidate.value
		fmt.Fprintf(&b, "  %-26s %s %8s %s\n",
			truncate(firstNonEmpty(t.Name, t.ID), 26), bar(t.AvgMs, maxMs, 18),
			humanMs(t.AvgMs), dimStyle.Render(fmt.Sprintf("n=%d", t.Count)))
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderFrequent(tasks []client.TaskFrequency) string {
	if len(tasks) == 0 {
		return renderAnalyticsNoData("Most frequent tasks")
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
