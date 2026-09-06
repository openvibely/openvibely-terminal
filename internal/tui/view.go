package tui

// Rendering: the chat window chrome plus the block renderers that slash
// commands emit into the transcript.

import (
	"context"
	"encoding"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/openvibely/openvibely-tui/internal/client"
)

// Shared resource empty-state guidance keeps page and selector wording in sync.
const (
	taskEmptyStateHint       = "no tasks yet — /tasks new <title> creates one"
	attachmentEmptyStateHint = "no attachments yet — /tasks attachments add <task> <file> uploads one"
	scheduleEmptyStateHint   = "nothing scheduled — /schedule add <task> <2006-01-02T15:04> daily"
	skillEmptyStateHint      = "no skills yet — /skills add <name> creates one"
	automationEmptyStateHint = "no automations yet — create one via the web UI"
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
	connectionPhaseUnhealthy
)

func (m Model) connectionPhase() connectionPhase {
	switch {
	case m.connected && !m.authRequired:
		return connectionPhaseOnline
	case m.connReachableError && (m.connChecked || m.connErr != ""):
		return connectionPhaseUnhealthy
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
	} else if m.projectsLoaded && len(m.projects) == 0 && m.connectionPhase() == connectionPhaseOnline {
		project = "no projects"
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
		case connectionPhaseUnhealthy:
			conn = statusErrStyle.Render("● backend error")
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
	if m.authRequired {
		return "sign-in required: /login · help works without backend"
	}
	switch phase {
	case connectionPhaseOffline:
		if m.connErr != "" {
			return "offline: start/check backend · set -server or OPENVIBELY_SERVER_URL · /status"
		}
	case connectionPhaseUnhealthy:
		return "backend error: backend responded but is unhealthy · check backend logs or /status"
	case connectionPhaseConnecting:
		return "connecting: /help works offline · check -server or OPENVIBELY_SERVER_URL if this stays here"
	}
	if m.threadID != "" {
		return "in task thread · messages reply to this task · /chat to exit · / for commands"
	}
	if m.selectedID == "" && m.projectsLoaded && len(m.projects) == 0 {
		return "no projects yet · " + projectCreationCommand()
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
			if m.connReachableError {
				row("network", statusErrStyle.Render("backend error (unhealthy)"))
				row("error", m.connErr)
				row("try", "check backend logs, then run /status")
			} else {
				row("network", statusErrStyle.Render("offline"))
				row("error", m.connErr)
				row("try", "start/check your local backend, then run /status")
			}
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
		case connectionPhaseUnhealthy:
			row("server", statusErrStyle.Render("backend error (unhealthy)")+dimStyle.Render(" "+m.client.BaseURL()))
			if m.connErr != "" {
				row("error", m.connErr)
			}
			row("try", "backend responded but is unhealthy; check backend logs, then run /status")
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
	if m.statusProjectsUnavailable {
		row("projects", statusErrStyle.Render("unavailable")+dimStyle.Render(" (partial failure)"))
	} else if m.selectedName != "" {
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
		return dimStyle.Render(taskEmptyStateHint)
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
	title := sanitizeAutomationDetailText(firstNonEmpty(t.Title, shortID(t.ID)))
	fmt.Fprintf(&b, "%s  %s\n", sectionStyle.Render(title), statusMark(sanitizeAutomationDetailText(t.Status)))
	fmt.Fprintf(&b, "%s\n\n", dimStyle.Render(fmt.Sprintf("id %s · review", sanitizeAutomationDetailText(t.ID))))
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
		if lineType := sanitizeAutomationDetailText(r.LineType); lineType != "" {
			line += " " + lineType
		}
		state := reviewState(r)
		comment := sanitizeMemoryText(r.CommentText)
		if reviewedBy := sanitizeAutomationDetailText(r.ReviewedBy); reviewedBy != "" {
			comment = reviewedBy + ": " + comment
		}
		commentLines := strings.Split(comment, "\n")
		for i := range commentLines {
			commentLines[i] = truncate(commentLines[i], 72)
		}
		comment = strings.Join(commentLines, "\n")
		rows = append(rows, []string{truncate(sanitizeAutomationDetailText(r.FilePath), 32), line, state, comment})
	}
	b.WriteString(table(rows))
	return b.String()
}

func reviewState(r client.ReviewComment) string {
	state := sanitizeAutomationDetailText(r.State)
	if r.Resolved {
		if state != "" {
			return statusOKStyle.Render("resolved " + state)
		}
		return statusOKStyle.Render("resolved")
	}
	if state != "" {
		return statusMark(state)
	}
	return dimStyle.Render("—")
}

func renderTaskAttachments(attachments []client.Attachment) string {
	if len(attachments) == 0 {
		return dimStyle.Render(attachmentEmptyStateHint)
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

func renderLifecycleTaskHeading(task client.Task) string {
	title := firstNonEmpty(task.Title, shortID(task.ID))
	return fmt.Sprintf("%s  (id %s)\n", sectionStyle.Render(title), task.ID)
}

func renderLifecycleExecutions(task client.Task, executions []client.LifecycleExecution) string {
	var b strings.Builder
	b.WriteString(renderLifecycleTaskHeading(task))
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
	b.WriteString(renderLifecycleTaskHeading(task))
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
	preview := lifecycleJSONPreview{limit: 96}
	if err := preview.appendValue(payload, 0); err != nil {
		return "<unavailable>"
	}
	return preview.result()
}

const (
	lifecyclePreviewMaxDepth             = 10_000
	lifecyclePreviewMaxBytes             = 512
	lifecyclePreviewMaxKeyBytes          = 512
	lifecyclePreviewMaxRetainedKeyBytes  = 128 << 10
	lifecyclePreviewMaxValidationMapKeys = 512
)

// lifecycleJSONPreview emits the same bytes as encoding/json for ordinary
// values, but retains only a bounded canonical prefix for oversized values.
type lifecycleJSONPreview struct {
	buf          strings.Builder
	limit        int
	asciiCells   int
	stopped      bool
	byteCapped   bool
	indirections int
	active       map[lifecyclePreviewVisit]bool
}

type lifecyclePreviewVisit struct {
	typeOf  reflect.Type
	pointer uintptr
	length  int
}

// append tracks printable JSON ASCII incrementally. Non-ASCII text is retained
// only up to a fixed byte ceiling; final display-width truncation handles wide
// and combining clusters exactly once.
func (p *lifecycleJSONPreview) append(s string) {
	for len(s) > 0 && !p.stopped {
		if p.buf.Len() >= lifecyclePreviewMaxBytes {
			p.stopped = true
			p.byteCapped = true
			return
		}
		if s[0] < utf8.RuneSelf {
			p.buf.WriteByte(s[0])
			p.asciiCells++
			s = s[1:]
			if p.asciiCells > p.limit {
				p.stopped = true
			}
			continue
		}
		_, size := utf8.DecodeRuneInString(s)
		if p.buf.Len()+size > lifecyclePreviewMaxBytes {
			p.stopped = true
			p.byteCapped = true
			return
		}
		p.buf.WriteString(s[:size])
		s = s[size:]
	}
}

func (p *lifecycleJSONPreview) result() string {
	result := p.buf.String()
	if p.byteCapped {
		result += "…"
	}
	return truncate(result, p.limit)
}

func (p *lifecycleJSONPreview) appendStringAnyMap(value map[string]any, depth int) error {
	if value == nil {
		p.append("null")
		return nil
	}
	mapValue := reflect.ValueOf(value)
	leave, err := p.enterReference(mapValue)
	if err != nil {
		return err
	}
	if leave != nil {
		defer leave()
	}

	keys := make([]lifecyclePreviewMapKey, 0, min(len(value), p.limit+1))
	var validationKeys []lifecyclePreviewMapKey
	for key, item := range value {
		candidate := lifecyclePreviewMapKey{textString: key, nativeValue: item}
		keys = lifecycleInsertPreviewMapKey(keys, candidate, p.limit+1)
		if lifecycleAnyMayMarshalError(item) {
			validationKeys = append(validationKeys, candidate)
		}
	}
	p.append("{")
	for i, key := range keys {
		if !p.stopped {
			if i > 0 {
				p.append(",")
			}
			p.appendJSONString(key.textString)
			p.append(":")
		}
		if err := p.appendValue(key.nativeValue, depth+1); err != nil {
			return err
		}
	}
	if len(keys) > 0 && len(validationKeys) > 0 {
		cursor := keys[len(keys)-1]
		sort.Slice(validationKeys, func(i, j int) bool {
			return lifecyclePreviewMapKeyLess(validationKeys[i], validationKeys[j])
		})
		for _, key := range validationKeys {
			if !lifecyclePreviewMapKeyLess(cursor, key) {
				continue
			}
			if err := p.appendValue(key.nativeValue, depth+1); err != nil {
				return err
			}
		}
	}
	p.append("}")
	return nil
}

func (p *lifecycleJSONPreview) appendValue(value any, depth int) error {
	if depth >= lifecyclePreviewMaxDepth {
		return fmt.Errorf("lifecycle payload nesting exceeds JSON limit")
	}
	if p.stopped {
		switch value := value.(type) {
		case nil, bool, string,
			int, int8, int16, int32, int64,
			uint, uint8, uint16, uint32, uint64, uintptr:
			return nil
		case float64:
			if math.IsInf(value, 0) || math.IsNaN(value) {
				return fmt.Errorf("unsupported lifecycle payload float value")
			}
			return nil
		case float32:
			if math.IsInf(float64(value), 0) || math.IsNaN(float64(value)) {
				return fmt.Errorf("unsupported lifecycle payload float value")
			}
			return nil
		}
	}

	switch value := value.(type) {
	case nil:
		p.append("null")
	case bool:
		if value {
			p.append("true")
		} else {
			p.append("false")
		}
	case string:
		p.appendJSONString(value)
	case map[string]any:
		return p.appendStringAnyMap(value, depth)
	case []any:
		return p.appendReflectValue(reflect.ValueOf(value), depth)
	case float64, float32,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, uintptr,
		json.Number:
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		p.append(string(encoded))
	default:
		return p.appendReflected(value, depth)
	}
	return nil
}

func (p *lifecycleJSONPreview) appendReflected(value any, depth int) error {
	return p.appendReflectValue(reflect.ValueOf(value), depth)
}

func (p *lifecycleJSONPreview) appendReflectValue(value reflect.Value, depth int) error {
	if !value.IsValid() {
		p.append("null")
		return nil
	}
	if depth >= lifecyclePreviewMaxDepth {
		return fmt.Errorf("lifecycle payload nesting exceeds JSON limit")
	}
	if (value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer) && value.IsNil() {
		p.append("null")
		return nil
	}
	if value.Kind() == reflect.Interface {
		if p.indirections >= lifecyclePreviewMaxDepth {
			return fmt.Errorf("lifecycle payload indirection exceeds preview limit")
		}
		p.indirections++
		defer func() { p.indirections-- }()
		return p.appendReflectValue(value.Elem(), depth)
	}
	if value.Type() == reflect.TypeFor[json.Number]() {
		return p.appendMarshaled(value.Interface())
	}

	marshalValue := value
	if value.CanAddr() && value.Addr().CanInterface() {
		addressType := value.Addr().Type()
		if addressType.Implements(reflect.TypeFor[json.Marshaler]()) {
			return p.appendJSONMarshaler(value.Addr().Interface().(json.Marshaler))
		}
		if addressType.Implements(reflect.TypeFor[encoding.TextMarshaler]()) {
			return p.appendTextMarshaler(value.Addr().Interface().(encoding.TextMarshaler))
		}
	}
	if value.CanInterface() {
		if value.Type().Implements(reflect.TypeFor[json.Marshaler]()) {
			return p.appendJSONMarshaler(value.Interface().(json.Marshaler))
		}
		if value.Type().Implements(reflect.TypeFor[encoding.TextMarshaler]()) {
			return p.appendTextMarshaler(value.Interface().(encoding.TextMarshaler))
		}
	}

	if p.stopped {
		switch value.Kind() {
		case reflect.String, reflect.Bool,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			return nil
		case reflect.Float32, reflect.Float64:
			float := value.Float()
			if math.IsInf(float, 0) || math.IsNaN(float) {
				return fmt.Errorf("unsupported lifecycle payload float value")
			}
			return nil
		}
	}

	leave, err := p.enterReference(value)
	if err != nil {
		return err
	}
	if leave != nil {
		defer leave()
	}

	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if p.indirections >= lifecyclePreviewMaxDepth {
			return fmt.Errorf("lifecycle payload indirection exceeds preview limit")
		}
		p.indirections++
		defer func() { p.indirections-- }()
		return p.appendReflectValue(value.Elem(), depth)
	case reflect.String:
		p.appendJSONString(value.String())
		return nil
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		if !value.CanInterface() {
			return fmt.Errorf("unsupported lifecycle payload value %s", value.Type())
		}
		return p.appendMarshaled(value.Interface())
	case reflect.Array, reflect.Slice:
		if value.Kind() == reflect.Slice && lifecycleUsesByteSliceEncoding(value.Type()) {
			if value.IsNil() {
				p.append("null")
				return nil
			}
			return p.appendBytes(value.Bytes())
		}
		if value.Kind() == reflect.Slice && value.IsNil() {
			p.append("null")
			return nil
		}
		p.append("[")
		mayError := lifecycleTypeMayMarshalError(value.Type().Elem())
		for i := 0; i < value.Len(); i++ {
			if p.stopped && !mayError {
				break
			}
			if !p.stopped && i > 0 {
				p.append(",")
			}
			if err := p.appendReflectValue(value.Index(i), depth+1); err != nil {
				return err
			}
		}
		p.append("]")
		return nil
	case reflect.Map:
		return p.appendReflectMap(value, depth)
	case reflect.Struct:
		return p.appendStruct(value, depth)
	default:
		if !marshalValue.CanInterface() {
			return fmt.Errorf("unsupported lifecycle payload value %s", value.Type())
		}
		return p.appendMarshaled(marshalValue.Interface())
	}
}

func (p *lifecycleJSONPreview) enterReference(value reflect.Value) (func(), error) {
	switch value.Kind() {
	case reflect.Map, reflect.Pointer, reflect.Slice:
	default:
		return nil, nil
	}
	pointer := uintptr(value.UnsafePointer())
	if pointer == 0 {
		return nil, nil
	}
	visit := lifecyclePreviewVisit{typeOf: value.Type(), pointer: pointer}
	if value.Kind() == reflect.Slice {
		visit.length = value.Len()
	}
	if p.active[visit] {
		return nil, fmt.Errorf("lifecycle payload contains a cycle")
	}
	if p.active == nil {
		p.active = make(map[lifecyclePreviewVisit]bool)
	}
	p.active[visit] = true
	return func() { delete(p.active, visit) }, nil
}

type lifecycleStructField struct {
	name      string
	index     []int
	omitEmpty bool
	omitZero  bool
	quoted    bool
	tagged    bool
}

var lifecycleStructFieldsCache sync.Map // map[reflect.Type][]lifecycleStructField

func (p *lifecycleJSONPreview) appendStruct(value reflect.Value, depth int) error {
	p.append("{")
	written := 0
	for _, field := range lifecycleStructFields(value.Type()) {
		fieldValue, ok := lifecycleFieldByIndex(value, field.index)
		if !ok || (field.omitEmpty && lifecycleJSONEmptyValue(fieldValue)) ||
			(field.omitZero && lifecycleJSONZeroValue(fieldValue)) {
			continue
		}
		if !p.stopped {
			if written > 0 {
				p.append(",")
			}
			p.appendJSONString(field.name)
			p.append(":")
		}
		if field.quoted && lifecycleJSONShouldQuote(fieldValue) {
			if err := p.appendQuotedReflectValue(fieldValue); err != nil {
				return err
			}
		} else if err := p.appendReflectValue(fieldValue, depth+1); err != nil {
			return err
		}
		written++
	}
	p.append("}")
	return nil
}

func lifecycleStructFields(typeOf reflect.Type) []lifecycleStructField {
	if cached, ok := lifecycleStructFieldsCache.Load(typeOf); ok {
		return cached.([]lifecycleStructField)
	}
	type queued struct {
		typeOf reflect.Type
		index  []int
		seen   map[reflect.Type]bool
	}
	queue := []queued{{typeOf: typeOf, seen: map[reflect.Type]bool{typeOf: true}}}
	var candidates []lifecycleStructField
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for i := 0; i < current.typeOf.NumField(); i++ {
			field := current.typeOf.Field(i)
			if !field.IsExported() && !field.Anonymous {
				continue
			}
			tag := field.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, options, _ := strings.Cut(tag, ",")
			validTaggedName := name != ""
			if name != "" && !lifecycleJSONTagNameValid(name) {
				name = ""
				validTaggedName = false
			}
			fieldType := field.Type
			if fieldType.Kind() == reflect.Pointer {
				fieldType = fieldType.Elem()
			}
			index := append(append([]int(nil), current.index...), i)
			if name != "" || !field.Anonymous || fieldType.Kind() != reflect.Struct {
				if field.IsExported() || (field.Anonymous && fieldType.Kind() == reflect.Struct) {
					if name == "" {
						name = field.Name
					}
					candidates = append(candidates, lifecycleStructField{
						name: name, index: index, tagged: validTaggedName,
						omitEmpty: lifecycleJSONTagOption(options, "omitempty"),
						omitZero:  lifecycleJSONTagOption(options, "omitzero"),
						quoted:    lifecycleJSONTagOption(options, "string") && lifecycleJSONCanQuote(field.Type),
					})
				}
				continue
			}
			if !current.seen[fieldType] {
				seen := make(map[reflect.Type]bool, len(current.seen)+1)
				for seenType := range current.seen {
					seen[seenType] = true
				}
				seen[fieldType] = true
				queue = append(queue, queued{typeOf: fieldType, index: index, seen: seen})
			}
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.name != right.name {
			return left.name < right.name
		}
		if len(left.index) != len(right.index) {
			return len(left.index) < len(right.index)
		}
		if left.tagged != right.tagged {
			return left.tagged
		}
		return lifecycleIndexLess(left.index, right.index)
	})
	selected := make([]lifecycleStructField, 0, len(candidates))
	for start := 0; start < len(candidates); {
		end := start + 1
		for end < len(candidates) && candidates[end].name == candidates[start].name {
			end++
		}
		minimumDepth := len(candidates[start].index)
		atDepth := candidates[start:end]
		count := 0
		taggedIndex, taggedCount := -1, 0
		for i, candidate := range atDepth {
			if len(candidate.index) != minimumDepth {
				break
			}
			count++
			if candidate.tagged {
				taggedIndex = i
				taggedCount++
			}
		}
		if taggedCount == 1 {
			selected = append(selected, atDepth[taggedIndex])
		} else if taggedCount == 0 && count == 1 {
			selected = append(selected, atDepth[0])
		}
		start = end
	}
	sort.Slice(selected, func(i, j int) bool { return lifecycleIndexLess(selected[i].index, selected[j].index) })
	lifecycleStructFieldsCache.Store(typeOf, selected)
	return selected
}

func lifecycleFieldByIndex(value reflect.Value, index []int) (reflect.Value, bool) {
	for _, fieldIndex := range index {
		if value.Kind() == reflect.Pointer {
			if value.IsNil() {
				return reflect.Value{}, false
			}
			value = value.Elem()
		}
		value = value.Field(fieldIndex)
	}
	return value, true
}

func lifecycleIndexLess(left, right []int) bool {
	for i := 0; i < min(len(left), len(right)); i++ {
		if left[i] != right[i] {
			return left[i] < right[i]
		}
	}
	return len(left) < len(right)
}

func lifecycleJSONTagOption(options, wanted string) bool {
	for options != "" {
		option, rest, _ := strings.Cut(options, ",")
		if option == wanted {
			return true
		}
		options = rest
	}
	return false
}

func lifecycleJSONTagNameValid(name string) bool {
	if name == "" {
		return false
	}
	for _, char := range name {
		if !unicode.IsLetter(char) && !unicode.IsDigit(char) && !strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", char) {
			return false
		}
	}
	return true
}

func lifecycleJSONCanQuote(typeOf reflect.Type) bool {
	// Match encoding/json's field planning: only one unnamed pointer layer is
	// stripped before deciding whether the ,string option applies.
	if typeOf.Name() == "" && typeOf.Kind() == reflect.Pointer {
		typeOf = typeOf.Elem()
	}
	return typeOf.Kind() == reflect.Bool || typeOf.Kind() == reflect.String ||
		(typeOf.Kind() >= reflect.Int && typeOf.Kind() <= reflect.Int64) ||
		(typeOf.Kind() >= reflect.Uint && typeOf.Kind() <= reflect.Uintptr) ||
		typeOf.Kind() == reflect.Float32 || typeOf.Kind() == reflect.Float64
}

func lifecycleJSONShouldQuote(value reflect.Value) bool {
	jsonMarshalerType := reflect.TypeFor[json.Marshaler]()
	textMarshalerType := reflect.TypeFor[encoding.TextMarshaler]()
	for {
		if value.CanAddr() && value.Addr().CanInterface() &&
			(value.Addr().Type().Implements(jsonMarshalerType) || value.Addr().Type().Implements(textMarshalerType)) {
			return false
		}
		if value.CanInterface() &&
			(value.Type().Implements(jsonMarshalerType) || value.Type().Implements(textMarshalerType)) {
			return false
		}
		if value.Kind() != reflect.Pointer || value.IsNil() {
			return true
		}
		value = value.Elem()
	}
}

func lifecycleJSONEmptyValue(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return value.Len() == 0
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.Interface, reflect.Pointer:
		return value.IsZero()
	}
	return false
}

type lifecycleJSONIsZeroer interface {
	IsZero() bool
}

func lifecycleJSONZeroValue(value reflect.Value) bool {
	isZeroerType := reflect.TypeFor[lifecycleJSONIsZeroer]()
	typeOf := value.Type()
	switch {
	case typeOf.Kind() == reflect.Interface && typeOf.Implements(isZeroerType):
		return value.IsNil() ||
			(value.Elem().Kind() == reflect.Pointer && value.Elem().IsNil()) ||
			value.Interface().(lifecycleJSONIsZeroer).IsZero()
	case typeOf.Kind() == reflect.Pointer && typeOf.Implements(isZeroerType):
		return value.IsNil() || value.Interface().(lifecycleJSONIsZeroer).IsZero()
	case typeOf.Implements(isZeroerType):
		return value.Interface().(lifecycleJSONIsZeroer).IsZero()
	case reflect.PointerTo(typeOf).Implements(isZeroerType):
		if !value.CanAddr() {
			boxed := reflect.New(typeOf).Elem()
			boxed.Set(value)
			value = boxed
		}
		return value.Addr().Interface().(lifecycleJSONIsZeroer).IsZero()
	default:
		return value.IsZero()
	}
}

func (p *lifecycleJSONPreview) appendQuotedReflectValue(value reflect.Value) error {
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			p.append("null")
			return nil
		}
		value = value.Elem()
	}
	if value.Type() == reflect.TypeFor[json.Number]() {
		if _, err := json.Marshal(value.Interface()); err != nil {
			return err
		}
		p.appendJSONString(value.String())
		return nil
	}
	if value.Kind() == reflect.String {
		inner := lifecycleJSONPreview{limit: lifecyclePreviewMaxBytes}
		inner.appendJSONString(value.String())
		p.appendJSONString(inner.buf.String())
		return nil
	}
	if !value.CanInterface() {
		return fmt.Errorf("unsupported lifecycle quoted value %s", value.Type())
	}
	encoded, err := json.Marshal(value.Interface())
	if err != nil {
		return err
	}
	p.appendJSONString(string(encoded))
	return nil
}

func (p *lifecycleJSONPreview) appendReflectMap(value reflect.Value, depth int) error {
	if value.IsNil() {
		p.append("null")
		return nil
	}
	keyType := value.Type().Key()
	switch keyType.Kind() {
	case reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
	default:
		if !keyType.Implements(reflect.TypeFor[encoding.TextMarshaler]()) {
			return fmt.Errorf("unsupported lifecycle payload map key %s", keyType)
		}
	}
	keys := make([]lifecyclePreviewMapKey, 0, min(value.Len(), p.limit+1))
	var validationKeys []lifecyclePreviewMapKey
	orderingUnavailable := false
	iterator := value.MapRange()
	sourceOrder := 0
	retainedKeyBytes := 0
	for iterator.Next() {
		if orderingUnavailable {
			// Continue the single key-method pass so key call cardinality and errors
			// remain compatible, but retain nothing once fallback is certain.
			if _, err := lifecyclePreviewKey(iterator.Key(), -1); err != nil {
				return err
			}
			continue
		}
		key, err := lifecyclePreviewKey(iterator.Key(), max(0, lifecyclePreviewMaxRetainedKeyBytes-retainedKeyBytes))
		if err != nil {
			return err
		}
		retainedKeyBytes += len(key.text)
		key.sourceOrder = sourceOrder
		sourceOrder++
		key.mapValue = iterator.Value()
		mayError := lifecycleReflectValueMayMarshalError(key.mapValue)
		if key.textTruncated {
			for _, retained := range keys {
				if lifecyclePreviewMapKeyOrderAmbiguous(retained, key) {
					// These keys may differ only beyond a discarded suffix, so their
					// canonical output order cannot be determined within the bound.
					orderingUnavailable = true
					break
				}
			}
		}
		keys = lifecycleInsertPreviewMapKey(keys, key, p.limit+1)
		if mayError {
			for _, retained := range validationKeys {
				if lifecyclePreviewMapKeyOrderAmbiguous(retained, key) {
					// Error-capable values must be invoked in exact canonical key
					// order. Distinct retained prefixes establish that order without
					// requiring the discarded suffixes.
					orderingUnavailable = true
					break
				}
			}
			if len(validationKeys) >= lifecyclePreviewMaxValidationMapKeys {
				orderingUnavailable = true
				continue
			}
			validationKeys = append(validationKeys, key)
		}
	}
	if orderingUnavailable {
		return fmt.Errorf("lifecycle payload map key ordering exceeds preview bounds")
	}
	p.append("{")
	for i, key := range keys {
		if !p.stopped {
			if i > 0 {
				p.append(",")
			}
			key.appendTo(p)
			p.append(":")
		}
		if err := p.appendReflectValue(key.mapValue, depth+1); err != nil {
			return err
		}
	}
	if len(keys) > 0 && len(validationKeys) > 0 {
		cursor := keys[len(keys)-1]
		sort.Slice(validationKeys, func(i, j int) bool {
			return lifecyclePreviewMapKeyLess(validationKeys[i], validationKeys[j])
		})
		for _, key := range validationKeys {
			if !lifecyclePreviewMapKeyLess(cursor, key) {
				continue
			}
			if err := p.appendReflectValue(key.mapValue, depth+1); err != nil {
				return err
			}
		}
	}
	p.append("}")
	return nil
}

func lifecycleInsertPreviewMapKey(keys []lifecyclePreviewMapKey, key lifecyclePreviewMapKey, limit int) []lifecyclePreviewMapKey {
	index := sort.Search(len(keys), func(i int) bool { return lifecyclePreviewMapKeyLess(key, keys[i]) })
	if len(keys) >= limit && index >= limit {
		return keys
	}
	if len(keys) < limit {
		keys = append(keys, lifecyclePreviewMapKey{})
	}
	copy(keys[index+1:], keys[index:len(keys)-1])
	keys[index] = key
	return keys
}

type lifecyclePreviewMapKey struct {
	sourceKey     reflect.Value
	sourceOrder   int
	mapValue      reflect.Value
	nativeValue   any
	text          []byte
	textLength    int
	textMarshaled bool
	textTruncated bool
	textString    string
}

func lifecyclePreviewKey(value reflect.Value, retainedBudget int) (lifecyclePreviewMapKey, error) {
	if value.Kind() == reflect.String {
		return lifecyclePreviewMapKey{sourceKey: value, textString: value.String()}, nil
	}
	if value.Kind() == reflect.Pointer && value.IsNil() && value.Type().Implements(reflect.TypeFor[encoding.TextMarshaler]()) {
		// Match encoding/json: nil pointer TextMarshaler map keys encode as
		// the empty key without invoking MarshalText on the nil receiver.
		return lifecyclePreviewMapKey{sourceKey: value, textString: ""}, nil
	}
	if value.CanInterface() {
		if marshaler, ok := value.Interface().(encoding.TextMarshaler); ok {
			text, err := marshaler.MarshalText()
			if err != nil {
				return lifecyclePreviewMapKey{}, err
			}
			// MarshalText implementations may reuse mutable scratch storage. Keep
			// complete output only while the map-wide retention budget permits exact
			// sorting; otherwise copy just enough prefix to render or detect that the
			// discarded suffix makes canonical ordering unknowable. A negative budget
			// means fallback is already certain, so invoke the method but retain none.
			if retainedBudget < 0 {
				return lifecyclePreviewMapKey{}, nil
			}
			textLength := len(text)
			if textLength > retainedBudget {
				textLength = min(textLength, lifecyclePreviewMaxKeyBytes+1)
			}
			retained := append([]byte(nil), text[:textLength]...)
			return lifecyclePreviewMapKey{
				sourceKey: value, text: retained, textLength: len(retained),
				textMarshaled: true, textTruncated: len(text) > len(retained),
			}, nil
		}
	}
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return lifecyclePreviewMapKey{sourceKey: value, textString: strconv.FormatInt(value.Int(), 10)}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return lifecyclePreviewMapKey{sourceKey: value, textString: strconv.FormatUint(value.Uint(), 10)}, nil
	}
	return lifecyclePreviewMapKey{}, fmt.Errorf("unsupported lifecycle payload map key %s", value.Type())
}

func (key lifecyclePreviewMapKey) appendTo(preview *lifecycleJSONPreview) {
	if key.textMarshaled {
		preview.appendJSONStringBytes(key.text)
	} else {
		preview.appendJSONString(key.textString)
	}
}

func lifecyclePreviewMapKeyOrderAmbiguous(left, right lifecyclePreviewMapKey) bool {
	leftLength, rightLength := left.length(), right.length()
	for i := 0; i < min(leftLength, rightLength); i++ {
		if left.byteAt(i) != right.byteAt(i) {
			return false
		}
	}
	if !left.textTruncated && !right.textTruncated {
		return false
	}
	if left.textTruncated && right.textTruncated {
		return true
	}
	if left.textTruncated {
		return rightLength > leftLength
	}
	return leftLength > rightLength
}

func lifecyclePreviewMapKeyLess(left, right lifecyclePreviewMapKey) bool {
	leftLength, rightLength := left.length(), right.length()
	for i := 0; i < min(leftLength, rightLength); i++ {
		leftByte, rightByte := left.byteAt(i), right.byteAt(i)
		if leftByte != rightByte {
			return leftByte < rightByte
		}
	}
	if leftLength != rightLength {
		return leftLength < rightLength
	}
	if lifecyclePreviewSourceKeyLess(left.sourceKey, right.sourceKey) {
		return true
	}
	if lifecyclePreviewSourceKeyLess(right.sourceKey, left.sourceKey) {
		return false
	}
	return left.sourceOrder < right.sourceOrder
}

func lifecyclePreviewSourceKeyLess(left, right reflect.Value) bool {
	if !left.IsValid() || !right.IsValid() || left.Kind() != right.Kind() {
		return false
	}
	switch left.Kind() {
	case reflect.String:
		return left.String() < right.String()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return left.Int() < right.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return left.Uint() < right.Uint()
	}
	return false
}

func lifecycleAnyMayMarshalError(value any) bool {
	if value == nil {
		return false
	}
	return lifecycleReflectValueMayMarshalError(reflect.ValueOf(value))
}

func lifecycleReflectValueMayMarshalError(value reflect.Value) bool {
	for value.IsValid() && value.Kind() == reflect.Interface {
		if value.IsNil() {
			return false
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		return false
	}
	typeOf := value.Type()
	if typeOf == reflect.TypeFor[json.Number]() ||
		typeOf.Implements(reflect.TypeFor[json.Marshaler]()) ||
		typeOf.Implements(reflect.TypeFor[encoding.TextMarshaler]()) {
		return true
	}
	switch value.Kind() {
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return false
	case reflect.Float32, reflect.Float64:
		float := value.Float()
		return math.IsInf(float, 0) || math.IsNaN(float)
	default:
		return lifecycleTypeMayMarshalError(typeOf)
	}
}

func (key lifecyclePreviewMapKey) length() int {
	if key.textMarshaled {
		return key.textLength
	}
	return len(key.textString)
}

func (key lifecyclePreviewMapKey) byteAt(index int) byte {
	if key.textMarshaled {
		return key.text[index]
	}
	return key.textString[index]
}

func lifecycleUsesByteSliceEncoding(typeOf reflect.Type) bool {
	if typeOf.Kind() != reflect.Slice || typeOf.Elem().Kind() != reflect.Uint8 {
		return false
	}
	element := typeOf.Elem()
	pointer := reflect.PointerTo(element)
	return !element.Implements(reflect.TypeFor[json.Marshaler]()) &&
		!element.Implements(reflect.TypeFor[encoding.TextMarshaler]()) &&
		!pointer.Implements(reflect.TypeFor[json.Marshaler]()) &&
		!pointer.Implements(reflect.TypeFor[encoding.TextMarshaler]())
}

func lifecycleTypeMayMarshalError(typeOf reflect.Type) bool {
	return lifecycleTypeMayMarshalErrorSeen(typeOf, make(map[reflect.Type]bool))
}

func lifecycleTypeMayMarshalErrorSeen(typeOf reflect.Type, seen map[reflect.Type]bool) bool {
	if seen[typeOf] {
		// Recursive pointer/container types may contain runtime cycles even when
		// their scalar leaves cannot otherwise fail JSON encoding.
		return true
	}
	seen[typeOf] = true
	if typeOf == reflect.TypeFor[json.Number]() ||
		typeOf.Implements(reflect.TypeFor[json.Marshaler]()) ||
		typeOf.Implements(reflect.TypeFor[encoding.TextMarshaler]()) ||
		(typeOf.Kind() != reflect.Pointer && (reflect.PointerTo(typeOf).Implements(reflect.TypeFor[json.Marshaler]()) || reflect.PointerTo(typeOf).Implements(reflect.TypeFor[encoding.TextMarshaler]()))) {
		return true
	}
	switch typeOf.Kind() {
	case reflect.Interface, reflect.Float32, reflect.Float64, reflect.Chan, reflect.Complex64, reflect.Complex128, reflect.Func, reflect.UnsafePointer:
		return true
	case reflect.Pointer, reflect.Array, reflect.Slice:
		return lifecycleTypeMayMarshalErrorSeen(typeOf.Elem(), seen)
	case reflect.Map:
		return lifecycleTypeMayMarshalErrorSeen(typeOf.Key(), seen) || lifecycleTypeMayMarshalErrorSeen(typeOf.Elem(), seen)
	case reflect.Struct:
		for _, field := range lifecycleStructFields(typeOf) {
			fieldType := typeOf.FieldByIndex(field.index).Type
			if lifecycleTypeMayMarshalErrorSeen(fieldType, seen) {
				return true
			}
		}
	}
	return false
}

func (p *lifecycleJSONPreview) appendBytes(value []byte) error {
	p.append(`"`)
	const sourceLimit = lifecyclePreviewMaxBytes / 4 * 3
	prefix := value
	if len(prefix) > sourceLimit {
		prefix = prefix[:sourceLimit]
	}
	var encoded [lifecyclePreviewMaxBytes]byte
	written := base64.StdEncoding.EncodedLen(len(prefix))
	base64.StdEncoding.Encode(encoded[:written], prefix)
	p.append(string(encoded[:written]))
	p.append(`"`)
	return nil
}

func (p *lifecycleJSONPreview) appendJSONMarshaler(marshaler json.Marshaler) error {
	encoded, err := marshaler.MarshalJSON()
	if err != nil {
		return err
	}
	if !json.Valid(encoded) {
		return fmt.Errorf("invalid JSON from lifecycle payload marshaler")
	}
	p.appendCompactJSON(encoded)
	return nil
}

func (p *lifecycleJSONPreview) appendTextMarshaler(marshaler encoding.TextMarshaler) error {
	text, err := marshaler.MarshalText()
	if err != nil {
		return err
	}
	p.appendJSONStringBytes(text)
	return nil
}

// appendCompactJSON mirrors encoding/json's compaction for the retained prefix.
// The complete input is validated once above; after the prefix closes, no more
// output processing is needed.
func (p *lifecycleJSONPreview) appendCompactJSON(encoded []byte) {
	inString, escaped := false, false
	for i := 0; i < len(encoded) && !p.stopped; i++ {
		char := encoded[i]
		if !inString {
			if char == ' ' || char == '\t' || char == '\r' || char == '\n' {
				continue
			}
			p.append(string(char))
			if char == '"' {
				inString = true
			}
			continue
		}
		if escaped {
			p.append(string(char))
			escaped = false
			continue
		}
		if char == '\\' {
			p.append(`\`)
			escaped = true
			continue
		}
		if char == '"' {
			p.append(`"`)
			inString = false
			continue
		}
		switch char {
		case '<', '>', '&':
			p.append(fmt.Sprintf(`\u%04x`, char))
		default:
			if i+2 < len(encoded) && char == 0xe2 && encoded[i+1] == 0x80 && (encoded[i+2] == 0xa8 || encoded[i+2] == 0xa9) {
				p.append(fmt.Sprintf(`\u202%c`, '8'+encoded[i+2]-0xa8))
				i += 2
			} else if char >= utf8.RuneSelf {
				_, size := utf8.DecodeRune(encoded[i:])
				p.append(string(encoded[i : i+size]))
				i += size - 1
			} else {
				p.append(string(char))
			}
		}
	}
}

func (p *lifecycleJSONPreview) appendMarshaled(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	p.append(string(encoded))
	return nil
}

func (p *lifecycleJSONPreview) appendJSONStringBytes(value []byte) {
	p.append(`"`)
	for len(value) > 0 && !p.stopped {
		plain := 0
		for plain < len(value) && plain <= p.limit {
			char := value[plain]
			if char >= utf8.RuneSelf || char < 0x20 || char == '\\' || char == '"' || char == '<' || char == '>' || char == '&' {
				break
			}
			plain++
		}
		if plain > 0 {
			p.append(string(value[:plain]))
			value = value[plain:]
			continue
		}
		if value[0] < utf8.RuneSelf {
			char := value[0]
			value = value[1:]
			switch char {
			case '\\', '"':
				p.append("\\" + string(char))
			case '\b':
				p.append(`\b`)
			case '\f':
				p.append(`\f`)
			case '\n':
				p.append(`\n`)
			case '\r':
				p.append(`\r`)
			case '\t':
				p.append(`\t`)
			case '<', '>', '&':
				p.append(fmt.Sprintf(`\u%04x`, char))
			default:
				if char < 0x20 {
					p.append(fmt.Sprintf(`\u%04x`, char))
				} else {
					p.append(string(char))
				}
			}
			continue
		}

		r, size := utf8.DecodeRune(value)
		if r == utf8.RuneError && size == 1 {
			p.append(`\ufffd`)
			value = value[1:]
			continue
		}
		if r == '\u2028' || r == '\u2029' {
			p.append(fmt.Sprintf(`\u%04x`, r))
		} else {
			p.append(string(value[:size]))
		}
		value = value[size:]
	}
	p.append(`"`)
}

func (p *lifecycleJSONPreview) appendJSONString(value string) {
	p.append(`"`)
	for len(value) > 0 && !p.stopped {
		plain := 0
		for plain < len(value) && plain <= p.limit {
			char := value[plain]
			if char >= utf8.RuneSelf || char < 0x20 || char == '\\' || char == '"' || char == '<' || char == '>' || char == '&' {
				break
			}
			plain++
		}
		if plain > 0 {
			p.append(value[:plain])
			value = value[plain:]
			continue
		}
		if value[0] < utf8.RuneSelf {
			char := value[0]
			value = value[1:]
			switch char {
			case '\\', '"':
				p.append("\\" + string(char))
			case '\b':
				p.append(`\b`)
			case '\f':
				p.append(`\f`)
			case '\n':
				p.append(`\n`)
			case '\r':
				p.append(`\r`)
			case '\t':
				p.append(`\t`)
			case '<', '>', '&':
				p.append(fmt.Sprintf(`\u%04x`, char))
			default:
				if char < 0x20 {
					p.append(fmt.Sprintf(`\u%04x`, char))
				} else {
					p.append(string(char))
				}
			}
			continue
		}

		r, size := utf8.DecodeRuneInString(value)
		if r == utf8.RuneError && size == 1 {
			p.append(`\ufffd`)
			value = value[1:]
			continue
		}
		if r == '\u2028' || r == '\u2029' {
			p.append(fmt.Sprintf(`\u%04x`, r))
		} else {
			p.append(value[:size])
		}
		value = value[size:]
	}
	p.append(`"`)
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
		return dimStyle.Render(scheduleEmptyStateHint)
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
		return dimStyle.Render(automationEmptyStateHint)
	}
	return table(rows) + "\n\n" +
		dimStyle.Render("/automations run|pause|resume|delete <id|name>")
}

func sanitizeAutomationDetailForTerminal(detail client.AutomationDetail) client.AutomationDetail {
	sanitize := sanitizeAutomationDetailText
	a := &detail.Automation
	a.ID, a.ProjectID, a.StableKey = sanitize(a.ID), sanitize(a.ProjectID), sanitize(a.StableKey)
	a.Name, a.Description, a.AutomationType = sanitize(a.Name), sanitize(a.Description), sanitize(a.AutomationType)
	a.LifecycleState, a.HealthState, a.HealthReason = sanitize(a.LifecycleState), sanitize(a.HealthState), sanitize(a.HealthReason)

	v := &detail.Version
	v.ID, v.State, v.Source, v.AdapterKey = sanitize(v.ID), sanitize(v.State), sanitize(v.Source), sanitize(v.AdapterKey)

	detail.Nodes = append([]client.AutomationLiveNode(nil), detail.Nodes...)
	detail.UnmatchedNodeDetails = append([]client.AutomationLiveNode(nil), detail.UnmatchedNodeDetails...)
	for _, nodes := range [][]client.AutomationLiveNode{detail.Nodes, detail.UnmatchedNodeDetails} {
		for i := range nodes {
			n := &nodes[i]
			n.ID, n.NodeKey, n.Name = sanitize(n.ID), sanitize(n.NodeKey), sanitize(n.Name)
			n.NodeType, n.Role, n.DisplayState = sanitize(n.NodeType), sanitize(n.Role), sanitize(n.DisplayState)
		}
	}

	detail.Edges = append([]client.AutomationLiveEdge(nil), detail.Edges...)
	detail.UnmatchedEdgeDetails = append([]client.AutomationLiveEdge(nil), detail.UnmatchedEdgeDetails...)
	for _, edges := range [][]client.AutomationLiveEdge{detail.Edges, detail.UnmatchedEdgeDetails} {
		for i := range edges {
			e := &edges[i]
			e.ID, e.EdgeKey, e.SourceNodeID = sanitize(e.ID), sanitize(e.EdgeKey), sanitize(e.SourceNodeID)
			e.TargetNodeID, e.Label = sanitize(e.TargetNodeID), sanitize(e.Label)
			e.SourceName, e.TargetName = sanitize(e.SourceName), sanitize(e.TargetName)
		}
	}

	detail.Resources = append([]client.AutomationResourceSummary(nil), detail.Resources...)
	for i := range detail.Resources {
		r := &detail.Resources[i]
		r.NodeID, r.NodeKey, r.ResourceType = sanitize(r.NodeID), sanitize(r.NodeKey), sanitize(r.ResourceType)
		r.ResourceID, r.Relation, r.Name, r.Status = sanitize(r.ResourceID), sanitize(r.Relation), sanitize(r.Name), sanitize(r.Status)
	}
	detail.ExternalState.Status = sanitize(detail.ExternalState.Status)
	detail.ExternalState.LastUpdatedAt = sanitize(detail.ExternalState.LastUpdatedAt)
	detail.Warnings = append([]string(nil), detail.Warnings...)
	for i := range detail.Warnings {
		detail.Warnings[i] = sanitize(detail.Warnings[i])
	}
	return detail
}

func sanitizeAutomationDetailText(value string) string {
	value = ansi.Strip(value)
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case unicode.IsControl(r) || unicode.In(r, unicode.Cf):
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func renderAutomationDetail(detail client.AutomationDetail) string {
	detail = sanitizeAutomationDetailForTerminal(detail)
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
		if (detail.NodesAvailable && len(detail.Nodes) > 0) || len(detail.UnmatchedNodeDetails) > 0 {
			renderAutomationDetailOnlyNodes(&b, detail)
		}
		if len(detail.Edges) > 0 || len(detail.UnmatchedEdgeDetails) > 0 {
			renderAutomationDetailRetainedEdges(&b, detail)
		}
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
		warnings := append([]string(nil), detail.Warnings...)
		sort.Strings(warnings)
		for _, warning := range warnings {
			if strings.TrimSpace(warning) != "" {
				b.WriteString(dimStyle.Render("  "+warning) + "\n")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderAutomationDetailNodes(b *strings.Builder, detail client.AutomationDetail) {
	renderAutomationDetailNodeTable(b, detail, "Nodes")
	renderAutomationDetailUnmatchedNodeDetails(b, detail)
}

func renderAutomationDetailOnlyNodes(b *strings.Builder, detail client.AutomationDetail) {
	if detail.NodesAvailable && len(detail.Nodes) > 0 {
		title := "Detail-only nodes"
		if detail.GraphNodesPresent {
			title = "Retained graph nodes"
		}
		renderAutomationDetailNodeTable(b, detail, title)
	}
	renderAutomationDetailUnmatchedNodeDetails(b, detail)
}

func renderAutomationDetailNodeTable(b *strings.Builder, detail client.AutomationDetail, title string) {
	b.WriteString("  " + sectionStyle.Render(title) + "\n")
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
	legacyNodeCounts := detail.NodeCountsAvailable && len(detail.UnmatchedNodeDetails) == 0 && !automationDetailHasNodeCountAvailability(detail)
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
}

func renderAutomationDetailUnmatchedNodeDetails(b *strings.Builder, detail client.AutomationDetail) {
	if len(detail.UnmatchedNodeDetails) == 0 {
		return
	}
	b.WriteString("  " + sectionStyle.Render("Unmatched node details") + "\n")
	b.WriteString(dimStyle.Render("    correlation unavailable; records retained separately") + "\n")
	details := append([]client.AutomationLiveNode(nil), detail.UnmatchedNodeDetails...)
	sort.SliceStable(details, func(i, j int) bool {
		return automationDetailNodeSortKey(details[i]) < automationDetailNodeSortKey(details[j])
	})
	detailRows := [][]string{{"NODE", "KEY", "ROLE", "TYPE", "RUN", "WAIT", "BLOCK", "FAIL", "RECENT"}}
	for _, node := range details {
		counts := node.Counts
		detailRows = append(detailRows, []string{
			firstNonEmpty(node.Name, node.NodeKey, node.ID, "—"),
			firstNonEmpty(node.NodeKey, node.ID, "—"),
			firstNonEmpty(node.Role, "—"),
			firstNonEmpty(node.NodeType, "—"),
			automationDetailTableCount(counts.Running, counts.RunningAvailable),
			automationDetailTableCount(counts.Waiting, counts.WaitingAvailable),
			automationDetailTableCount(counts.Blocked, counts.BlockedAvailable),
			automationDetailTableCount(counts.Failed, counts.FailedAvailable),
			automationDetailTableCount(counts.CompletedRecently, counts.CompletedRecentlyAvailable),
		})
	}
	b.WriteString(indentAutomationDetailTable(table(detailRows)))
	b.WriteByte('\n')
}

func renderAutomationDetailEdges(b *strings.Builder, detail client.AutomationDetail) {
	renderAutomationDetailEdgeTable(b, detail, "Edges", true)
	renderAutomationDetailUnmatchedEdgeDetails(b, detail)
}

func renderAutomationDetailRetainedEdges(b *strings.Builder, detail client.AutomationDetail) {
	if len(detail.Edges) > 0 {
		b.WriteString("  " + sectionStyle.Render("Retained edge records") + "\n")
		b.WriteString(dimStyle.Render("    graph unavailable; records retained for diagnostics") + "\n")
		renderAutomationDetailEdgeRows(b, detail.Edges, detail.EdgeCountsAvailable)
	}
	renderAutomationDetailUnmatchedEdgeDetails(b, detail)
}

func renderAutomationDetailUnmatchedEdgeDetails(b *strings.Builder, detail client.AutomationDetail) {
	if len(detail.UnmatchedEdgeDetails) == 0 {
		return
	}
	b.WriteString("  " + sectionStyle.Render("Unmatched edge details") + "\n")
	b.WriteString(dimStyle.Render("    correlation unavailable; topology records retained separately") + "\n")
	renderAutomationDetailEdgeRows(b, detail.UnmatchedEdgeDetails, false)
}

func renderAutomationDetailEdgeTable(b *strings.Builder, detail client.AutomationDetail, title string, requireAvailable bool) {
	b.WriteString("  " + sectionStyle.Render(title) + "\n")
	if requireAvailable && !detail.EdgesAvailable {
		b.WriteString(dimStyle.Render("    unavailable — edge section was not returned") + "\n")
		return
	}
	if len(detail.Edges) == 0 {
		b.WriteString(dimStyle.Render("    (empty)") + "\n")
		return
	}
	renderAutomationDetailEdgeRows(b, detail.Edges, detail.EdgeCountsAvailable)
}

func renderAutomationDetailEdgeRows(b *strings.Builder, edges []client.AutomationLiveEdge, legacyCountsAvailable bool) {
	edges = append([]client.AutomationLiveEdge(nil), edges...)
	sort.SliceStable(edges, func(i, j int) bool {
		return automationDetailEdgeSortKey(edges[i]) < automationDetailEdgeSortKey(edges[j])
	})
	rows := [][]string{{"FROM", "TO", "LABEL", "TRANSITIONS", "RECENT"}}
	legacyEdgeCounts := legacyCountsAvailable && !automationEdgesHaveCountAvailability(edges)
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

func automationEdgesHaveCountAvailability(edges []client.AutomationLiveEdge) bool {
	for _, edge := range edges {
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
	counts := node.Counts
	primary := strings.ToLower(firstNonEmpty(node.NodeKey, node.Name, node.ID))
	return strings.Join([]string{
		primary,
		node.NodeKey,
		node.Name,
		node.ID,
		node.ProjectID,
		node.AutomationID,
		node.VersionID,
		node.NodeType,
		node.Role,
		node.ConfigJSON,
		fmt.Sprintf("%.17g", node.PositionX),
		fmt.Sprintf("%.17g", node.PositionY),
		node.DisplayState,
		fmt.Sprintf("%d:%t", counts.Running, counts.RunningAvailable),
		fmt.Sprintf("%d:%t", counts.Waiting, counts.WaitingAvailable),
		fmt.Sprintf("%d:%t", counts.Blocked, counts.BlockedAvailable),
		fmt.Sprintf("%d:%t", counts.Failed, counts.FailedAvailable),
		fmt.Sprintf("%d:%t", counts.CompletedRecently, counts.CompletedRecentlyAvailable),
	}, "\x00")
}

func automationDetailEdgeSortKey(edge client.AutomationLiveEdge) string {
	primary := strings.ToLower(strings.Join([]string{
		firstNonEmpty(edge.SourceName, edge.SourceNodeID),
		firstNonEmpty(edge.TargetName, edge.TargetNodeID),
		firstNonEmpty(edge.Label, edge.EdgeKey),
		edge.ID,
	}, "\x00"))
	return strings.Join([]string{
		primary,
		edge.SourceName,
		edge.TargetName,
		edge.SourceNodeID,
		edge.TargetNodeID,
		edge.Label,
		edge.EdgeKey,
		edge.ID,
		edge.ProjectID,
		edge.AutomationID,
		edge.VersionID,
		edge.ConditionJSON,
		fmt.Sprintf("%d", edge.DisplayOrder),
		fmt.Sprintf("%d:%t", edge.TransitionCount, edge.TransitionCountAvailable),
		fmt.Sprintf("%d:%t", edge.RecentTransitionCount, edge.RecentTransitionCountAvailable),
		fmt.Sprintf("%t", edge.Highlighted),
	}, "\x00")
}

func automationDetailResourceSortKey(resource client.AutomationResourceSummary) string {
	raw := strings.Join([]string{
		resource.NodeID,
		resource.NodeKey,
		resource.ResourceType,
		resource.ResourceID,
		resource.Relation,
		resource.Name,
		resource.Status,
		resource.URL,
	}, "\x00")
	return strings.ToLower(raw) + "\x00" + raw
}

func indentAutomationDetailTable(value string) string {
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		lines[i] = "    " + line
	}
	return strings.Join(lines, "\n")
}

// --- alerts ---

func alertSummaryFor(a client.Alert, projectID string) client.AlertSummary {
	badges := append([]string{}, a.Badges...)
	return client.AlertSummary{
		ID:              a.ID,
		ProjectID:       firstNonEmpty(a.ProjectID, projectID),
		Scope:           firstNonEmpty(a.Scope, "project"),
		Type:            a.Type,
		Severity:        a.Severity,
		Title:           a.Title,
		Message:         a.Message,
		Source:          a.Source,
		DecisionState:   a.DecisionState,
		ProcessingState: a.ProcessingState,
		Text:            a.Text,
		Badges:          badges,
		Read:            a.Read,
	}
}

func renderAlertInspection(inspection client.AlertInspection) string {
	summary := inspection.Summary
	var b strings.Builder
	title := firstNonEmpty(sanitizeAlertText(summary.Title), sanitizeAlertText(summary.Message), sanitizeAlertText(summary.ID), "(untitled alert)")
	fmt.Fprintf(&b, "%s\n", sectionStyle.Render(title))
	fmt.Fprintf(&b, "%s\n", dimStyle.Render("id: "+firstNonEmpty(sanitizeAlertText(summary.ID), "(unknown)")))
	fmt.Fprintf(&b, "%s\n", dimStyle.Render("type: "+alertDisplayValue(summary.Type)+" · severity: "+alertDisplayValue(summary.Severity)))
	fmt.Fprintf(&b, "%s\n", dimStyle.Render("decision: "+alertDisplayValue(summary.DecisionState)+" · processing: "+alertDisplayValue(summary.ProcessingState)))
	if message := sanitizeAlertText(summary.Message); message != "" && message != title {
		fmt.Fprintf(&b, "%s\n", dimStyle.Render("message: "+message))
	}
	if projectID := sanitizeAlertText(summary.ProjectID); projectID != "" {
		fmt.Fprintf(&b, "%s\n", dimStyle.Render("project_id: "+projectID))
	}

	body := sanitizeAlertText(inspection.Detail.Body)
	metadata := inspection.Detail.Metadata
	if metadata == nil {
		metadata = make(map[string]any)
	}
	metadataJSON, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		metadataJSON = []byte("{}")
	}

	b.WriteString("\n" + sectionStyle.Render("Body") + "\n")
	if body == "" {
		b.WriteString(dimStyle.Render("(empty body)"))
	} else {
		b.WriteString(strings.TrimRight(body, "\n"))
	}
	b.WriteString("\n\n" + sectionStyle.Render("Metadata") + "\n")
	if len(metadata) == 0 {
		b.WriteString(dimStyle.Render("(empty metadata)"))
	} else {
		b.Write(metadataJSON)
	}
	if body == "" && len(metadata) == 0 {
		b.WriteString("\n\n" + dimStyle.Render("No additional detail."))
	}
	return strings.TrimRight(b.String(), "\n")
}

func alertDisplayValue(value string) string {
	value = sanitizeAlertText(strings.TrimSpace(value))
	if value == "" {
		return "(unknown)"
	}
	return value
}

func sanitizeAlertText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return sanitizeMemoryText(value)
}

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
		return dimStyle.Render(skillEmptyStateHint)
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

// --- memory ---

const (
	memoryListFileWidth     = 48
	memoryDocumentMaxLines  = 80
	memoryDocumentLineWidth = 240
	memoryDisplayValueWidth = 240
	memorySearchQueryWidth  = 120
)

func renderMemoryList(list client.MemoryList) string {
	return renderMemoryListForFilter(list, "")
}

func renderMemoryListForFilter(list client.MemoryList, filter string) string {
	var b strings.Builder
	if len(list.Memories) == 0 {
		if strings.TrimSpace(filter) != "" {
			fmt.Fprintf(&b, "%s", dimStyle.Render(fmt.Sprintf("no memory matches for %q", truncate(sanitizeMemoryText(filter), memorySearchQueryWidth))))
		} else {
			b.WriteString(dimStyle.Render("no indexed project memory — .openvibely/memories/MEMORIES.md has no topic files"))
		}
	} else {
		rows := [][]string{{"FILE", "TITLE", "SUMMARY"}}
		for _, memory := range list.Memories {
			file := sanitizeMemoryText(memory.File)
			title := sanitizeMemoryText(firstNonEmpty(memory.Title, memory.File))
			summary := sanitizeMemoryText(memory.Summary)
			rows = append(rows, []string{
				truncate(file, memoryListFileWidth),
				truncate(title, 32),
				truncate(summary, 58),
			})
		}
		b.WriteString(table(rows))
	}
	if len(list.Warnings) > 0 {
		b.WriteString("\n\n" + renderMemoryWarnings(list.Warnings))
	}
	b.WriteString("\n\n" + dimStyle.Render("/memory show <file|title> · /memory search <query>"))
	return strings.TrimRight(b.String(), "\n")
}

func renderMemoryDocument(document client.MemoryDocument) string {
	var b strings.Builder
	if strings.TrimSpace(document.File) == "" {
		b.WriteString(dimStyle.Render("memory file is unavailable"))
	} else {
		file := sanitizeMemoryText(document.File)
		title := sanitizeMemoryText(firstNonEmpty(document.Title, document.File))
		summary := sanitizeMemoryText(document.Summary)
		fmt.Fprintf(&b, "%s\n", sectionStyle.Render(truncate(title, memoryDisplayValueWidth)))
		fmt.Fprintf(&b, "%s", dimStyle.Render("file "+truncate(file, memoryDisplayValueWidth)))
		if strings.TrimSpace(summary) != "" {
			fmt.Fprintf(&b, "\n\n%s", truncate(summary, memoryDisplayValueWidth))
		}
		available := document.Available
		if !available {
			b.WriteString("\n\n" + dimStyle.Render("(memory file unavailable)"))
		} else {
			body := renderMemoryBody(document.Body)
			if body == "" {
				b.WriteString("\n\n" + dimStyle.Render("(empty memory file)"))
			} else {
				b.WriteString("\n\n" + body)
			}
		}
	}
	if len(document.Warnings) > 0 {
		b.WriteString("\n\n" + renderMemoryWarnings(document.Warnings))
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderMemoryBody(body string) string {
	body = sanitizeMemoryText(strings.TrimRight(body, "\n"))
	if body == "" {
		return ""
	}
	lines := strings.Split(body, "\n")
	moreLines := 0
	if len(lines) > memoryDocumentMaxLines {
		moreLines = len(lines) - memoryDocumentMaxLines
		lines = lines[:memoryDocumentMaxLines]
	}
	for i := range lines {
		lines[i] = truncate(lines[i], memoryDocumentLineWidth)
	}
	result := strings.Join(lines, "\n")
	if moreLines > 0 {
		result += "\n" + dimStyle.Render(fmt.Sprintf("… %d more lines", moreLines))
	}
	return result
}

func renderMemorySearch(result client.MemorySearch) string {
	query := truncate(sanitizeMemoryText(result.Query), memorySearchQueryWidth)
	var b strings.Builder
	if len(result.Memories) == 0 {
		fmt.Fprintf(&b, "%s", dimStyle.Render(fmt.Sprintf("no memory matches for %q", query)))
	} else {
		fmt.Fprintf(&b, "%s\n\n", sectionStyle.Render(fmt.Sprintf("Memory search: %q", query)))
		rows := [][]string{{"FILE", "TITLE", "MATCH"}}
		for _, memory := range result.Memories {
			file := sanitizeMemoryText(memory.File)
			title := sanitizeMemoryText(firstNonEmpty(memory.Title, memory.File))
			match := sanitizeMemoryText(firstNonEmpty(memory.Snippet, memory.Summary, "match"))
			rows = append(rows, []string{
				truncate(file, memoryListFileWidth),
				truncate(title, 32),
				truncate(match, 64),
			})
		}
		b.WriteString(table(rows))
	}
	if len(result.Warnings) > 0 {
		b.WriteString("\n\n" + renderMemoryWarnings(result.Warnings))
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderMemoryWarnings(warnings []string) string {
	var b strings.Builder
	b.WriteString(noticeStyle.Render("warnings:"))
	cleaned := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		warning = truncate(sanitizeMemoryText(warning), memoryDisplayValueWidth)
		if strings.TrimSpace(warning) != "" {
			cleaned = append(cleaned, warning)
		}
	}
	const maxDisplayedWarnings = 8
	shown := len(cleaned)
	if shown > maxDisplayedWarnings {
		shown = maxDisplayedWarnings
	}
	for _, warning := range cleaned[:shown] {
		b.WriteString("\n" + dimStyle.Render("  "+warning))
	}
	if len(cleaned) > shown {
		b.WriteString("\n" + dimStyle.Render(fmt.Sprintf("  … %d more warnings", len(cleaned)-shown)))
	}
	return b.String()
}

func sanitizeMemoryText(value string) string {
	value = ansi.Strip(value)
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case r == '\t' || r == '\r':
			b.WriteByte(' ')
		case unicode.IsControl(r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
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

// renderVoteRecords renders every agent vote for one parallel workflow step.
// Records are copied before sorting so the client response remains unchanged.
func renderVoteRecords(stepExecID string, records []client.VoteRecord) string {
	stepExecID = truncate(compactProviderText(stepExecID), 80)
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", sectionStyle.Render("Workflow votes"))
	fmt.Fprintf(&b, "%s\n\n", dimStyle.Render("step execution: "+stepExecID))
	if len(records) == 0 {
		b.WriteString(dimStyle.Render("no vote records available for this step execution"))
		return b.String()
	}

	ordered := append([]client.VoteRecord(nil), records...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := ordered[i], ordered[j]
		if left.AgentConfigID != right.AgentConfigID {
			return left.AgentConfigID < right.AgentConfigID
		}
		if left.Vote != right.Vote {
			return left.Vote < right.Vote
		}
		if left.Confidence != right.Confidence {
			return left.Confidence < right.Confidence
		}
		if left.Reasoning != right.Reasoning {
			return left.Reasoning < right.Reasoning
		}
		if left.StepExecutionID != right.StepExecutionID {
			return left.StepExecutionID < right.StepExecutionID
		}
		return left.ID < right.ID
	})

	rows := [][]string{{"AGENT", "VOTE", "CONFIDENCE", "REASONING"}}
	for _, record := range ordered {
		reasoning := truncate(compactProviderText(record.Reasoning), 72)
		if reasoning == "" {
			reasoning = "—"
		}
		rows = append(rows, []string{
			firstNonEmpty(compactProviderText(record.AgentConfigID), "—"),
			firstNonEmpty(compactProviderText(record.Vote), "—"),
			fmt.Sprintf("%.2f", record.Confidence),
			reasoning,
		})
	}
	b.WriteString(table(rows))
	return b.String()
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
		if len(list) == 0 {
			return dimStyle.Render("no models configured — add a model via the web UI or API")
		}
		safeFilter := truncate(compactProviderText(sanitizeMemoryText(filter)), 80)
		return dimStyle.Render(fmt.Sprintf("no models match %q", safeFilter))
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

type workerCapacityRow struct {
	Scope   string `json:"scope"`
	Name    string `json:"name"`
	Running int    `json:"running"`
	Queue   int    `json:"queue"`
	Limit   *int   `json:"limit"`
	Status  string `json:"status"`
}

type modelWorkerCapacityRow struct {
	Name    string `json:"name"`
	Model   string `json:"model"`
	Running int    `json:"running"`
	Limit   int    `json:"limit"`
	Status  string `json:"status"`
}

type workersOverview struct {
	Global          *client.GlobalCapacity   `json:"-"`
	Projects        []client.ProjectCapacity `json:"-"`
	Models          []client.ModelCapacity   `json:"-"`
	Workers         []workerCapacityRow      `json:"workers"`
	ModelRows       []modelWorkerCapacityRow `json:"models"`
	ModelsAvailable bool                     `json:"models_available"`
	Warnings        []string                 `json:"warnings"`
}

func newWorkersOverview(global *client.GlobalCapacity, projects []client.ProjectCapacity, models []client.ModelCapacity, warnings []string, modelsAvailable bool) workersOverview {
	overview := workersOverview{
		Global: global, Projects: projects, Models: models,
		Workers:         make([]workerCapacityRow, 0, len(projects)+1),
		ModelRows:       make([]modelWorkerCapacityRow, 0, len(models)),
		ModelsAvailable: modelsAvailable,
		Warnings:        append([]string(nil), warnings...),
	}
	if overview.Warnings == nil {
		overview.Warnings = []string{}
	}
	if global != nil {
		limit := global.MaxWorkers
		overview.Workers = append(overview.Workers, workerCapacityRow{
			Scope: "global", Name: "All Projects", Running: global.TotalRunning,
			Queue: global.QueueSize, Limit: &limit, Status: workerCapacityStatus(global.TotalRunning, &limit),
		})
	}
	for _, project := range projects {
		overview.Workers = append(overview.Workers, workerCapacityRow{
			Scope: "project", Name: project.Name, Running: project.Running,
			Queue: project.QueueSize, Limit: project.MaxWorkers, Status: workerCapacityStatus(project.Running, project.MaxWorkers),
		})
	}
	for _, model := range models {
		overview.ModelRows = append(overview.ModelRows, modelWorkerCapacityRow{
			Name: model.Name, Model: model.Model, Running: model.Running,
			Limit: model.MaxWorkers, Status: modelWorkerCapacityStatus(model.Running, model.MaxWorkers),
		})
	}
	return overview
}

func workerCapacityStatus(running int, limit *int) string {
	if limit != nil && *limit > 0 && running >= *limit {
		return "at_capacity"
	}
	if running > 0 {
		return "active"
	}
	return "idle"
}

func modelWorkerCapacityStatus(running, limit int) string {
	if running >= limit {
		return "at_capacity"
	}
	if running > 0 {
		return "active"
	}
	return "idle"
}

func workerStatusLabel(status string) string {
	switch status {
	case "at_capacity":
		return "At capacity"
	case "active":
		return "Active"
	default:
		return "Idle"
	}
}

func workerLimitLabel(scope string, limit *int) string {
	if limit == nil || *limit == 0 {
		if scope == "global" {
			return "Unlimited"
		}
		return "No limit"
	}
	return strconv.Itoa(*limit)
}

func renderWorkers(overview workersOverview) string {
	if overview.Workers == nil || overview.ModelRows == nil || overview.Warnings == nil {
		overview = newWorkersOverview(overview.Global, overview.Projects, overview.Models, overview.Warnings, overview.ModelsAvailable)
	}

	rows := [][]string{{"SCOPE", "NAME", "RUNNING", "QUEUE", "LIMIT", "STATUS"}}
	for _, worker := range overview.Workers {
		running := strconv.Itoa(worker.Running)
		if worker.Limit != nil && *worker.Limit > 0 {
			running = fmt.Sprintf("%d / %d", worker.Running, *worker.Limit)
		}
		rows = append(rows, []string{
			strings.Title(worker.Scope), truncate(worker.Name, 32), running,
			strconv.Itoa(worker.Queue), workerLimitLabel(worker.Scope, worker.Limit), workerStatusLabel(worker.Status),
		})
	}

	var b strings.Builder
	b.WriteString(sectionStyle.Render("Worker capacity") + "\n")
	b.WriteString(table(rows))
	b.WriteString("\n\n" + sectionStyle.Render("Per-model worker pools") + "\n")
	if !overview.ModelsAvailable {
		b.WriteString("  " + dimStyle.Render("model worker capacity unavailable"))
	} else if len(overview.ModelRows) == 0 {
		b.WriteString("  " + dimStyle.Render("no dedicated model worker pools"))
	} else {
		modelRows := [][]string{{"MODEL", "RUNNING", "LIMIT", "STATUS"}}
		for _, model := range overview.ModelRows {
			name := model.Name
			if model.Model != "" && model.Model != model.Name {
				name += " (" + model.Model + ")"
			}
			modelRows = append(modelRows, []string{
				truncate(name, 40), fmt.Sprintf("%d / %d", model.Running, model.Limit),
				strconv.Itoa(model.Limit), workerStatusLabel(model.Status),
			})
		}
		b.WriteString(table(modelRows))
	}
	for _, warning := range overview.Warnings {
		b.WriteString("\n" + dimStyle.Render("warning: "+warning))
	}
	b.WriteString("\n\n" + dimStyle.Render("/workers limit <n> sets the global cap"))
	return b.String()
}

// --- projects ---

func renderProjects(projects []client.Project, caps []client.ProjectCapacity, selectedID string) string {
	if len(projects) == 0 {
		return dimStyle.Render(noProjectsGuidance())
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
		if len(c.aliases) > 0 {
			aliases := make([]string, 0, len(c.aliases))
			for _, alias := range c.aliases {
				aliases = append(aliases, cmdPrefix+alias)
			}
			fmt.Fprintf(&b, "  %-*s  %s\n", width, "", dimStyle.Render("aliases: "+strings.Join(aliases, ", ")))
		}
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
