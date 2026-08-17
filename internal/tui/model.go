// Package tui implements the OpenVibely terminal UI on top of Bubble Tea.
//
// The TUI is a single chat window. Everything happens in it: plain text is
// sent to the project agent, and a line starting with "/" is a slash command
// whose output is rendered back into the same conversation.
//
// Commands are resource + action chains, e.g.
//
//	/tasks                     list the kanban board
//	/tasks run <id|title>      run a task
//	/alerts delete <id>        delete an alert
//	/skills add <name>         create a skill
//
// Type "/" to see the command menu with completion; /help lists everything.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/openvibely/openvibely-tui/internal/client"
)

const (
	refreshInterval  = 30 * time.Second
	chatPollInterval = 1500 * time.Millisecond
	maxTranscript    = 500
	maxHistory       = 200

	defaultPlaceholder = "Message the agent, or / for a command"
)

// entry is one block in the conversation transcript.
type entry struct {
	role string // "you", "agent", "system", "error", "event", "result"
	head string // optional heading for result blocks
	text string
}

// pendingCmd holds a destructive command that is waiting for explicit user
// confirmation before it is allowed to execute.
type pendingCmd struct {
	message string  // confirmation prompt shown in the status area
	cmd     tea.Cmd // executed when the user types "yes" and presses Enter
}

// Model is the root Bubble Tea model: one chat transcript plus one input.
type Model struct {
	client *client.Client

	transcript viewport.Model
	input      textinput.Model
	spin       spinner.Model
	width      int
	height     int

	log []entry

	// command menu (shown while the input starts with "/")
	menu    []command
	menuSel int

	// input history (↑/↓ when the input is empty or navigating)
	history []string
	histPos int

	// connection state
	connected bool
	connErr   string
	capacity  *client.GlobalCapacity
	auth      *client.AuthStatus

	// projects
	projects     []client.Project
	selectedID   string
	selectedName string
	// wantProject is a project requested up front (-project flag) and resolved
	// once the project list arrives.
	wantProject string

	// task thread focus: when set, typed messages go to this task's thread
	// instead of the project agent ("/tasks open <ref>" enters, "/chat" exits).
	threadID    string
	threadTitle string

	// in-flight chat
	pendingMsgID string
	busy         bool

	// operational counts cached by /status
	pendingAlertCount int
	activeTaskCount   int
	queuedTaskCount   int

	// pendingConfirmation holds a destructive command awaiting explicit
	// confirmation ("yes" + Enter executes it; Esc or anything else cancels).
	pendingConfirmation *pendingCmd

	// inline ref selector (opened when a command needing a <ref> is run
	// without one): key input is routed to the picker while active.
	selectorActive  bool
	selectorTitle   string
	selectorItems   []selectorItem
	selectorFilter  string
	selectorCursor  int
	pendingCommand  string // e.g. "tasks open"; re-dispatched with the chosen ref
	selectorPrefill bool   // prime the input instead of dispatching (piped commands)

	// live events
	showEvents    bool // stream events into the transcript
	sseConnected  bool
	sseBackoff    time.Duration
	sseCancel     context.CancelFunc
	sseEvents     <-chan client.Event
	sseErrs       <-chan error
	sseGeneration int

	quitting bool
}

// New builds the initial model.
func New(c *client.Client) Model {
	ti := textinput.New()
	ti.Placeholder = defaultPlaceholder
	ti.CharLimit = 8000
	ti.Prompt = "❯ "
	ti.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colorPrimary)

	m := Model{
		client:     c,
		input:      ti,
		spin:       sp,
		transcript: viewport.New(0, 0),
		sseBackoff: time.Second,
		histPos:    -1,
	}
	m.log = []entry{{
		role: "system",
		text: "Connected to " + c.BaseURL() + "\nType a message to chat, or /help for commands.",
	}}
	return m
}

// WithProject requests that a project be selected once the list loads. The
// reference may be a name, ID or unique prefix.
func (m Model) WithProject(ref string) Model {
	m.wantProject = ref
	return m
}

// Init kicks off the initial connection check, project load, and SSE stream.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.checkConnection(),
		m.loadProjects(false, m.wantProject),
		func() tea.Msg { return reconnectTickMsg{} },
		m.tick(),
		m.spin.Tick,
		textinput.Blink,
	)
}

// --- async commands ---

func (m Model) checkConnection() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		var capacity *client.GlobalCapacity
		var capErr error
		var auth *client.AuthStatus

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); capacity, capErr = c.GetGlobalCapacity(ctx) }()
		go func() { defer wg.Done(); auth, _ = c.AuthMe(ctx) }()
		wg.Wait()

		if capErr != nil {
			return connCheckedMsg{err: capErr}
		}
		return connCheckedMsg{capacity: capacity, auth: auth}
	}
}

// fetchStatusCounts concurrently fetches the pending-alert count and
// active/queued task counts for the selected project. It is a no-op when no
// project is selected.
func (m Model) fetchStatusCounts() tea.Cmd {
	if m.selectedID == "" {
		return nil
	}
	c, pid := m.client, m.selectedID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		var (
			pendingAlerts int
			activeTasks   int
			queuedTasks   int
		)

		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			alerts, err := c.ListAlerts(ctx, pid)
			if err != nil {
				return
			}
			for _, a := range alerts {
				for _, b := range a.Badges {
					if strings.Contains(strings.ToLower(b), "pending") {
						pendingAlerts++
						break
					}
				}
			}
		}()

		go func() {
			defer wg.Done()
			tasks, err := c.ListTasks(ctx, pid)
			if err != nil {
				return
			}
			for _, t := range tasks {
				if t.Category == "active" {
					activeTasks++
				}
				if t.Status == "queued" {
					queuedTasks++
				}
			}
		}()

		wg.Wait()
		return statusCountsMsg{
			pendingAlerts: pendingAlerts,
			activeTasks:   activeTasks,
			queuedTasks:   queuedTasks,
		}
	}
}

func (m Model) loadProjects(echo bool, selectName string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		var wg sync.WaitGroup
		var projects []client.Project
		var err error
		var caps []client.ProjectCapacity

		wg.Add(2)
		go func() {
			defer wg.Done()
			projects, err = c.ListProjects(ctx)
		}()
		go func() {
			defer wg.Done()
			caps, _ = c.GetProjectCapacities(ctx)
		}()
		wg.Wait()

		if err != nil {
			return projectsLoadedMsg{err: err, echo: echo}
		}
		return projectsLoadedMsg{projects: projects, capacities: caps, echo: echo, selectName: selectName}
	}
}

func (m Model) sendChat(projectID, message string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		accepted, err := c.SendChatMessage(ctx, projectID, message)
		return chatSentMsg{accepted: accepted, err: err}
	}
}

func (m Model) doChatStatus(messageID string) tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	status, err := m.client.GetChatStatus(ctx, messageID)
	return chatStatusMsg{status: status, err: err}
}

func (m Model) pollChat(messageID string) tea.Cmd {
	return tea.Tick(chatPollInterval, func(time.Time) tea.Msg {
		return m.doChatStatus(messageID)
	})
}

// fetchChatStatus issues an immediate (no-tick) GetChatStatus call.
func (m Model) fetchChatStatus(messageID string) tea.Cmd {
	return func() tea.Msg { return m.doChatStatus(messageID) }
}

// run executes fn against the backend and turns its output into a resultMsg.
func run(title string, timeout time.Duration, fn func(ctx context.Context) (string, error)) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		body, err := fn(ctx)
		return resultMsg{title: title, body: body, err: err}
	}
}

// connectSSE opens the live event stream. Any previous stream is cancelled.
func (m *Model) connectSSE() tea.Cmd {
	if m.sseCancel != nil {
		m.sseCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.sseCancel = cancel
	events, errs := m.client.StreamEvents(ctx, m.selectedID)
	m.sseEvents = events
	m.sseErrs = errs
	m.sseGeneration++
	return tea.Batch(
		func() tea.Msg { return sseConnectedMsg{} },
		m.waitForSSE(),
	)
}

// waitForSSE blocks on the current stream's channels and forwards one message.
func (m Model) waitForSSE() tea.Cmd {
	events, errs := m.sseEvents, m.sseErrs
	return func() tea.Msg {
		select {
		case ev, ok := <-events:
			if !ok {
				if err, ok := <-errs; ok && err != nil {
					return sseDisconnectedMsg{err: err}
				}
				return sseDisconnectedMsg{}
			}
			return sseEventMsg{event: ev}
		case err := <-errs:
			return sseDisconnectedMsg{err: err}
		}
	}
}

func (m Model) tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m Model) scheduleReconnect() tea.Cmd {
	backoff := m.sseBackoff
	return tea.Tick(backoff, func(time.Time) tea.Msg { return reconnectTickMsg{} })
}

// Cleanup releases the SSE stream; called on shutdown.
func (m *Model) Cleanup() {
	if m.sseCancel != nil {
		m.sseCancel()
	}
}

// --- update ---

// Update handles all incoming messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case selectorActiveMsg:
		return m.handleSelector(msg)

	case connCheckedMsg:
		if msg.err != nil {
			if m.connected {
				m.append(entry{role: "error", text: "lost connection: " + msg.err.Error()})
			}
			m.connected = false
			m.connErr = msg.err.Error()
		} else {
			m.connected = true
			m.connErr = ""
			m.capacity = msg.capacity
			m.auth = msg.auth
		}
		return m, nil

	case statusCountsMsg:
		m.pendingAlertCount = msg.pendingAlerts
		m.activeTaskCount = msg.activeTasks
		m.queuedTaskCount = msg.queuedTasks
		return m, nil

	case projectsLoadedMsg:
		if msg.err != nil {
			m.append(entry{role: "error", text: "loading projects: " + msg.err.Error()})
			return m, nil
		}
		m.projects = msg.projects
		// An explicit request resolves on its own; defaulting to the first
		// project first would leave a wrong project selected when the name is
		// ambiguous or unknown.
		if msg.selectName != "" {
			return m.pickProject(msg.selectName)
		}
		if m.selectedID == "" && len(m.projects) > 0 {
			m.selectedID = m.projects[0].ID
			m.selectedName = m.projects[0].Name
		}
		if msg.echo {
			m.append(entry{role: "result", head: "Projects", text: renderProjects(m.projects, msg.capacities, m.selectedID)})
		}
		return m, nil

	case resultMsg:
		m.busy = false
		if msg.err != nil {
			m.append(entry{role: "error", text: msg.err.Error()})
			return m, nil
		}
		body := msg.body
		if strings.TrimSpace(body) == "" {
			body = dimStyle.Render("(no results)")
		}
		m.append(entry{role: "result", head: msg.title, text: body})
		return m, nil

	case chatSentMsg:
		if msg.err != nil {
			m.busy = false
			m.append(entry{role: "error", text: "send failed: " + msg.err.Error()})
			return m, nil
		}
		m.pendingMsgID = msg.accepted.MessageID
		if msg.accepted.Queued {
			m.append(entry{role: "system", text: "queued behind an active chat turn…"})
		}
		return m, m.pollChat(m.pendingMsgID)

	case chatStatusMsg:
		if m.pendingMsgID == "" {
			return m, nil
		}
		if msg.err != nil {
			return m, m.pollChat(m.pendingMsgID) // transient; keep polling
		}
		switch msg.status.Status {
		case "completed":
			m.pendingMsgID = ""
			m.busy = false
			m.append(entry{role: "agent", text: msg.status.Response})
			if len(msg.status.TaskIDs) > 0 {
				m.append(entry{role: "system", text: "created tasks: " + strings.Join(msg.status.TaskIDs, ", ")})
			}
			return m, nil
		case "failed", "cancelled":
			m.pendingMsgID = ""
			m.busy = false
			text := msg.status.Status
			if msg.status.Error != "" {
				text += ": " + msg.status.Error
			}
			m.append(entry{role: "error", text: text})
			return m, nil
		default:
			return m, m.pollChat(m.pendingMsgID)
		}

	case threadOpenedMsg:
		m.busy = false
		if msg.err != nil {
			m.append(entry{role: "error", text: msg.err.Error()})
			return m, nil
		}
		if msg.projectID != "" && msg.projectID != m.selectedID {
			return m, nil // stale — project switched while fetch was in flight
		}
		m.threadID = msg.taskID
		m.threadTitle = msg.title
		m.append(entry{role: "result", head: "Thread · " + msg.title, text: msg.body})
		m.append(entry{role: "system", text: "in task thread — messages go to this task. /chat returns to project chat."})
		m.input.Placeholder = "Reply to " + truncate(msg.title, 40) + " (/chat to exit)"
		return m, nil

	case sseConnectedMsg:
		m.sseConnected = true
		m.sseBackoff = time.Second
		return m, nil

	case sseEventMsg:
		m.handleSSEEvent(msg.event)
		if msg.event.Name == "chat_response_done" && m.pendingMsgID != "" {
			var ce client.ChatEvent
			if json.Unmarshal(msg.event.Data, &ce) == nil {
				// Ignore events from a foreign project. Allow empty ProjectID
				// for single-project servers that omit the field.
				if ce.ProjectID != "" && ce.ProjectID != m.selectedID {
					return m, m.waitForSSE()
				}
				// Fast path: if the backend populated CompletedOutput in the SSE
				// payload, display it immediately without an extra HTTP round-trip.
				if ce.CompletedOutput != "" {
					m.pendingMsgID = ""
					m.busy = false
					m.append(entry{role: "agent", text: ce.CompletedOutput})
					return m, m.waitForSSE()
				}
			}
			// Otherwise issue an immediate status fetch instead of waiting for
			// the next 1500ms poll tick.
			return m, tea.Batch(m.waitForSSE(), m.fetchChatStatus(m.pendingMsgID))
		}
		return m, m.waitForSSE()

	case sseDisconnectedMsg:
		m.sseConnected = false
		cmd := m.scheduleReconnect()
		if m.sseBackoff < 30*time.Second {
			m.sseBackoff *= 2
		}
		return m, cmd

	case reconnectTickMsg:
		return m, m.connectSSE()

	case tickMsg:
		return m, tea.Batch(m.checkConnection(), m.tick())

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	}

	return m, nil
}

// handleKey routes keys; the input owns almost everything.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.selectorActive {
		return m.handleSelectorKey(msg)
	}
	switch msg.String() {
	case "ctrl+c", "ctrl+d":
		m.quitting = true
		m.Cleanup()
		return m, tea.Quit

	case "esc":
		if m.pendingConfirmation != nil {
			m.pendingConfirmation = nil
			m.input.SetValue("")
			m.append(entry{role: "system", text: "cancelled"})
			return m, nil
		}
		if len(m.menu) > 0 {
			m.menu = nil
			return m, nil
		}
		m.input.SetValue("")
		return m, nil

	case "enter":
		if m.pendingConfirmation != nil {
			text := strings.TrimSpace(m.input.Value())
			pending := m.pendingConfirmation
			m.pendingConfirmation = nil
			m.input.SetValue("")
			m.menu = nil
			if text == "yes" {
				m.busy = true
				return m, pending.cmd
			}
			m.append(entry{role: "system", text: "cancelled"})
			return m, nil
		}
		return m.submit()

	case "tab":
		if len(m.menu) > 0 {
			c := m.menu[m.menuSel]
			value := "/" + c.name
			if c.args != "" || len(c.actions) > 0 {
				value += " "
			}
			m.input.SetValue(value)
			m.input.CursorEnd()
			m.refreshMenu()
		}
		return m, nil

	case "up":
		if len(m.menu) > 0 {
			if m.menuSel > 0 {
				m.menuSel--
			}
			return m, nil
		}
		return m.historyPrev(), nil

	case "down":
		if len(m.menu) > 0 {
			if m.menuSel < len(m.menu)-1 {
				m.menuSel++
			}
			return m, nil
		}
		return m.historyNext(), nil

	case "pgup":
		m.transcript.HalfViewUp()
		return m, nil
	case "pgdown":
		m.transcript.HalfViewDown()
		return m, nil
	case "ctrl+l":
		m.log = nil
		m.refreshTranscript()
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.refreshMenu()
	return m, cmd
}

// submit handles Enter: either run a slash command or send a chat message.
func (m Model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return m, nil
	}
	// Enter with the menu open and only a command prefix typed accepts the
	// highlighted suggestion instead of running a partial name.
	if len(m.menu) > 0 && !strings.Contains(text, " ") {
		if lookupCommand(strings.TrimPrefix(text, "/")) == nil {
			c := m.menu[m.menuSel]
			m.input.SetValue("/" + c.name + " ")
			m.input.CursorEnd()
			m.refreshMenu()
			return m, nil
		}
	}

	m.input.SetValue("")
	m.menu = nil
	m.pushHistory(text)

	if strings.HasPrefix(text, "/") {
		m.append(entry{role: "you", text: text})
		return m.runCommand(text)
	}

	m.append(entry{role: "you", text: text})

	// Inside a task thread, plain text is a follow-up on that task.
	if m.threadID != "" {
		m.busy = true
		return m, m.sendThreadMessage(m.threadID, m.threadTitle, text)
	}

	if m.selectedID == "" {
		m.append(entry{role: "error", text: "no project selected — use /project <name>"})
		return m, nil
	}
	m.busy = true
	return m, m.sendChat(m.selectedID, text)
}

// sendThreadMessage posts a follow-up into a task thread and echoes the
// refreshed thread back into the transcript.
func (m Model) sendThreadMessage(taskID, title, text string) tea.Cmd {
	c := m.client
	return run("Thread · "+title, cmdTimeout, func(ctx context.Context) (string, error) {
		if err := c.SendTaskThreadMessage(ctx, taskID, text); err != nil {
			return "", err
		}
		d, err := c.GetTask(ctx, taskID)
		if err != nil {
			return "sent", nil
		}
		return renderThread(d), nil
	})
}

// --- history ---

func (m *Model) pushHistory(s string) {
	if len(m.history) == 0 || m.history[len(m.history)-1] != s {
		m.history = append(m.history, s)
		if len(m.history) > maxHistory {
			m.history = m.history[len(m.history)-maxHistory:]
		}
	}
	m.histPos = -1
}

func (m Model) historyPrev() Model {
	if len(m.history) == 0 {
		return m
	}
	if m.histPos == -1 {
		m.histPos = len(m.history) - 1
	} else if m.histPos > 0 {
		m.histPos--
	}
	m.input.SetValue(m.history[m.histPos])
	m.input.CursorEnd()
	return m
}

func (m Model) historyNext() Model {
	if m.histPos == -1 {
		return m
	}
	m.histPos++
	if m.histPos >= len(m.history) {
		m.histPos = -1
		m.input.SetValue("")
		return m
	}
	m.input.SetValue(m.history[m.histPos])
	m.input.CursorEnd()
	return m
}

// --- transcript ---

func (m *Model) append(e entry) {
	m.log = append(m.log, e)
	if len(m.log) > maxTranscript {
		m.log = m.log[len(m.log)-maxTranscript:]
	}
	m.refreshTranscript()
}

func (m *Model) resize() {
	m.transcript.Width = m.width
	// header (1) + blank (1) + menu/hint + input (1) + help (1)
	h := m.height - 5
	if h < 3 {
		h = 3
	}
	m.transcript.Height = h
	m.input.Width = m.width - 4
	m.refreshTranscript()
}

func (m *Model) refreshTranscript() {
	width := m.transcript.Width
	if width <= 0 {
		width = 80
	}
	wrap := lipgloss.NewStyle().Width(width - 2)

	var b strings.Builder
	for _, e := range m.log {
		switch e.role {
		case "you":
			b.WriteString(chatUserStyle.Render("❯ you") + "\n")
			b.WriteString(wrap.Render(e.text) + "\n\n")
		case "agent":
			b.WriteString(chatAgentStyle.Render("● agent") + "\n")
			b.WriteString(wrap.Render(e.text) + "\n\n")
		case "result":
			if e.head != "" {
				b.WriteString(sectionStyle.Render("▸ "+e.head) + "\n")
			}
			b.WriteString(e.text + "\n\n")
		case "error":
			b.WriteString(statusErrStyle.Render("✗ "+e.text) + "\n\n")
		case "event":
			b.WriteString(dimStyle.Render(e.text) + "\n")
		default:
			b.WriteString(dimStyle.Render("· "+e.text) + "\n\n")
		}
	}
	m.transcript.SetContent(b.String())
	m.transcript.GotoBottom()
}

// handleSSEEvent formats a live event; shown only when /events is on.
func (m *Model) handleSSEEvent(ev client.Event) {
	if !m.showEvents {
		return
	}
	ts := time.Now().Format("15:04:05")

	var line string
	if strings.HasPrefix(ev.Name, "chat_") {
		var ce client.ChatEvent
		if json.Unmarshal(ev.Data, &ce) == nil {
			line = fmt.Sprintf("[%s] %s %s", ts, ev.Name, truncate(ce.Message, 70))
		}
	}
	if line == "" {
		var te client.TaskEvent
		if json.Unmarshal(ev.Data, &te) == nil && te.Type != "" {
			line = fmt.Sprintf("[%s] %s %s %s", ts, te.Type, te.Status, truncate(te.TaskName, 50))
		} else {
			line = fmt.Sprintf("[%s] %s %s", ts, ev.Name, truncate(string(ev.Data), 70))
		}
	}
	m.append(entry{role: "event", text: line})
}

// truncate shortens s to n display cells, measuring runes rather than bytes so
// non-ASCII titles aren't cut mid-character or truncated far too early.
func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	runes := []rune(s)
	width, cut := 0, len(runes)
	for i, r := range runes {
		w := lipgloss.Width(string(r))
		if width+w > n-1 {
			cut = i
			break
		}
		width += w
	}
	return strings.TrimRight(string(runes[:cut]), " ") + "…"
}
