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
	"sync/atomic"
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

var projectRequestSequence uint64

func nextProjectRequestID() uint64 {
	return atomic.AddUint64(&projectRequestSequence, 1)
}

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

	transcriptContent     string
	transcriptBlocks      []string
	transcriptRenderWidth int
	transcriptReady       bool

	// command menu (shown while the input starts with "/")
	menu    []command
	menuSel int

	// input history (↑/↓ when the input is empty or navigating)
	history []string
	histPos int

	// connection state
	connected            bool
	connChecked          bool
	authRequired         bool
	connectionGeneration int
	sessionGeneration    uint64
	projectGeneration    uint64
	connErr              string
	capacity             *client.GlobalCapacity
	auth                 *client.AuthStatus

	// interactive cookie-session sign-in. Password text is held only while the
	// form is active and is cleared from the input before the request starts.
	loginActive             bool
	loginPassword           bool
	loginSubmitting         bool
	loginUsername           string
	loginRestorePrompt      string
	loginRestorePlaceholder string
	loginRestoreEchoMode    textinput.EchoMode
	loginResumeSSE          bool

	// projects
	projects     []client.Project
	selectedID   string
	selectedName string
	// wantProject is a project requested up front (-project flag) and resolved
	// once the project list arrives.
	wantProject string

	// projectRequestID identifies the newest in-flight project load or creation.
	// Async project messages with an older token are stale and must not replace
	// newer selection/list state.
	projectRequestID uint64

	// task thread focus: when set, typed messages go to this task's thread
	// instead of the project agent ("/tasks open <ref>" enters, "/chat" exits).
	threadID    string
	threadTitle string

	// in-flight chat
	pendingMsgID                string
	pendingMsgProjectID         string
	pendingMsgProjectGeneration uint64
	busy                        bool

	// operational counts cached by /status
	pendingAlertCount int
	activeTaskCount   int
	queuedTaskCount   int

	// pendingConfirmation holds a destructive command awaiting explicit
	// confirmation ("yes" + Enter executes it; Esc or anything else cancels).
	pendingConfirmation *pendingCmd

	// inline ref selector (opened when a command needing a <ref> is run
	// without one): key input is routed to the picker while active.
	selectorActive        bool
	selectorTitle         string
	selectorItems         []selectorItem
	selectorSearch        []string
	selectorFilter        string
	selectorFiltered      []selectorItem
	selectorFilteredFor   string
	selectorCursor        int
	pendingCommand        string // e.g. "tasks open"; re-dispatched with the chosen ref
	selectorPrefill       bool   // prime the input instead of dispatching
	selectorPrefillSuffix string // appended after the chosen ref when priming input

	// live events
	showEvents           bool // stream events into the transcript
	sseConnected         bool
	sseBackoff           time.Duration
	sseCancel            context.CancelFunc
	sseEvents            <-chan client.Event
	sseErrs              <-chan error
	sseGeneration        int
	sseRetryAfterProject bool

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
		// Generation one is the initial cookie/session epoch. Login and accepted
		// auth transitions advance it so older asynchronous command results cannot
		// mutate the new session's state.
		sessionGeneration: 1,
		// Generation one belongs to the initial health check created by Init.
		// Later checks advance it before their commands are launched.
		connectionGeneration: 1,
		// Generation one identifies the initial project context. Explicit project
		// transitions advance it before replacement work is launched.
		projectGeneration: 1,
	}
	m.log = []entry{{
		role: "system",
		text: "Connecting to " + c.BaseURL() + "\nType /help for commands while connection checks run.",
	}}
	return m
}

// WithProject requests that a project be selected once the list loads. The
// reference may be a name, ID or unique prefix.
func (m Model) WithProject(ref string) Model {
	m.wantProject = ref
	return m
}

// Init kicks off the initial connection check and project load. The first SSE
// stream is opened by the project-load response after it installs a selected
// project, rather than racing that response with an unscoped stream.
func (m Model) Init() tea.Cmd {
	_, projectLoad := m.beginProjectLoadWithSSE(false, m.wantProject, true)
	return tea.Batch(
		m.checkConnection(),
		projectLoad,
		m.tick(),
		m.spin.Tick,
		textinput.Blink,
	)
}

// --- async commands ---

func (m Model) checkConnection() tea.Cmd {
	generation := m.connectionGeneration
	if generation == 0 {
		generation = 1
	}
	return m.checkConnectionWithGeneration(generation)
}

// beginConnectionCheck invalidates every older health result before launching
// a new check. The initial check uses generation one from New; subsequent
// retries and periodic checks advance this value on the model instance that
// will receive their result.
func (m *Model) beginConnectionCheck() tea.Cmd {
	return m.checkConnectionWithGeneration(m.advanceConnectionGeneration())
}

func (m *Model) invalidateConnectionChecks() {
	m.advanceConnectionGeneration()
}

func (m *Model) advanceConnectionGeneration() int {
	if m.connectionGeneration == 0 {
		m.connectionGeneration = 1
	} else {
		m.connectionGeneration++
	}
	return m.connectionGeneration
}

func (m Model) checkConnectionWithGeneration(generation int) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		var capacity *client.GlobalCapacity
		var capErr error
		var auth *client.AuthStatus
		var authErr error

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); capacity, capErr = c.GetGlobalCapacity(ctx) }()
		go func() { defer wg.Done(); auth, authErr = c.AuthMe(ctx) }()
		wg.Wait()

		// A single unauthorized response is enough to prove the backend is
		// reachable. Prefer it over a concurrent transport error so the TUI
		// offers sign-in instead of incorrectly reporting the server offline.
		if client.IsAuthRequired(capErr) {
			return connCheckedMsg{generation: generation, capacity: capacity, auth: auth, err: capErr}
		}
		if client.IsAuthRequired(authErr) {
			return connCheckedMsg{generation: generation, capacity: capacity, auth: auth, err: authErr}
		}
		if capErr != nil {
			return connCheckedMsg{generation: generation, capacity: capacity, auth: auth, err: capErr}
		}
		// AuthMe is supplementary when the health endpoint is healthy. Preserve
		// the prior behavior of treating a non-auth AuthMe failure as non-fatal.
		return connCheckedMsg{generation: generation, capacity: capacity, auth: auth}
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
	sessionGeneration := sessionGenerationOf(m)
	projectGeneration := projectGenerationOf(m)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		var (
			pendingAlerts int
			activeTasks   int
			queuedTasks   int
			alertsErr     error
			tasksErr      error
		)

		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			alerts, err := c.ListAlerts(ctx, pid)
			if err != nil {
				alertsErr = err
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
				tasksErr = err
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
		counts := statusCountsMsg{
			sessionGeneration: sessionGeneration,
			projectGeneration: projectGeneration,
			pendingAlerts:     pendingAlerts,
			activeTasks:       activeTasks,
			queuedTasks:       queuedTasks,
		}
		// Counts are best-effort, but an auth failure proves the session is no
		// longer usable and must enter sign-in recovery instead of being hidden.
		if client.IsAuthRequired(alertsErr) {
			counts.err = alertsErr
		} else if client.IsAuthRequired(tasksErr) {
			counts.err = tasksErr
		}
		return counts
	}
}

func (m Model) beginProjectLoad(echo bool, selectName string) (Model, tea.Cmd) {
	return m.beginProjectLoadWithSSE(echo, selectName, m.sseRetryAfterProject)
}

func (m Model) beginProjectLoadWithSSE(echo bool, selectName string, startSSE bool) (Model, tea.Cmd) {
	requestID := nextProjectRequestID()
	m.projectRequestID = requestID
	return m, m.loadProjectsWithIDAndSSE(requestID, echo, selectName, startSSE)
}

// loadProjects is retained as a direct command helper for tests and callers
// that do not need to update the model before receiving the response. Runtime
// command paths use beginProjectLoad so a newer request immediately invalidates
// older responses.
func (m Model) loadProjects(echo bool, selectName string) tea.Cmd {
	return m.loadProjectsWithIDAndSSE(nextProjectRequestID(), echo, selectName, false)
}

func (m Model) loadProjectsWithID(requestID uint64, echo bool, selectName string) tea.Cmd {
	return m.loadProjectsWithIDAndSSE(requestID, echo, selectName, false)
}

func (m Model) loadProjectsWithIDAndSSE(requestID uint64, echo bool, selectName string, startSSE bool) tea.Cmd {
	c := m.client
	sessionGeneration := sessionGenerationOf(m)
	projectGeneration := projectGenerationOf(m)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		var wg sync.WaitGroup
		var projects []client.Project
		var err error
		var caps []client.ProjectCapacity
		var capsErr error

		wg.Add(2)
		go func() {
			defer wg.Done()
			projects, err = c.ListProjects(ctx)
		}()
		go func() {
			defer wg.Done()
			caps, capsErr = c.GetProjectCapacities(ctx)
		}()
		wg.Wait()

		if client.IsAuthRequired(err) {
			return projectsLoadedMsg{sessionGeneration: sessionGeneration, projectGeneration: projectGeneration, requestID: requestID, err: err, echo: echo, startSSE: startSSE}
		}
		if client.IsAuthRequired(capsErr) {
			return projectsLoadedMsg{sessionGeneration: sessionGeneration, projectGeneration: projectGeneration, requestID: requestID, err: capsErr, echo: echo, startSSE: startSSE}
		}
		if err != nil {
			return projectsLoadedMsg{sessionGeneration: sessionGeneration, projectGeneration: projectGeneration, requestID: requestID, err: err, echo: echo, startSSE: startSSE}
		}
		return projectsLoadedMsg{sessionGeneration: sessionGeneration, projectGeneration: projectGeneration, requestID: requestID, projects: projects, capacities: caps, echo: echo, selectName: selectName, startSSE: startSSE}
	}
}

func (m *Model) acceptsProjectResponse(requestID uint64) bool {
	// Zero is reserved for hand-built messages in tests and older callers. All
	// runtime project requests carry a non-zero token.
	if requestID == 0 {
		return true
	}
	if m.projectRequestID != 0 && m.projectRequestID != requestID {
		return false
	}
	m.projectRequestID = requestID
	return true
}

func (m *Model) acceptsSessionGeneration(generation uint64) bool {
	// Zero is reserved for hand-built messages in older tests. Every runtime
	// non-health command message carries a non-zero session generation.
	if generation == 0 {
		return true
	}
	if m.sessionGeneration == 0 {
		m.sessionGeneration = generation
		return true
	}
	return m.sessionGeneration == generation
}

func sessionGenerationOf(m Model) uint64 {
	if m.sessionGeneration == 0 {
		return 1
	}
	return m.sessionGeneration
}

func (m *Model) advanceSessionGeneration() uint64 {
	if m.sessionGeneration == 0 {
		m.sessionGeneration = 1
	} else {
		m.sessionGeneration++
	}
	return m.sessionGeneration
}

func (m *Model) acceptsProjectGeneration(generation uint64) bool {
	// Zero is reserved for hand-built messages in older tests. Every runtime
	// project-sensitive command message carries a non-zero project generation.
	if generation == 0 {
		return true
	}
	if m.projectGeneration == 0 {
		m.projectGeneration = generation
		return true
	}
	return m.projectGeneration == generation
}

func projectGenerationOf(m Model) uint64 {
	if m.projectGeneration == 0 {
		return 1
	}
	return m.projectGeneration
}

func (m *Model) advanceProjectGeneration() uint64 {
	if m.projectGeneration == 0 {
		m.projectGeneration = 1
	} else {
		m.projectGeneration++
	}
	return m.projectGeneration
}

// setActiveProject installs the selected project and invalidates all work tied
// to the previous project before any replacement stream or command is started.
func (m *Model) setActiveProject(project client.Project) bool {
	changed := m.selectedID != project.ID
	if changed {
		m.advanceProjectGeneration()
		m.pendingMsgID = ""
		m.pendingMsgProjectID = ""
		m.pendingMsgProjectGeneration = 0
		m.busy = false
		m.pendingConfirmation = nil
		if m.selectorActive {
			*m = m.clearSelector()
		}
		m.threadID, m.threadTitle = "", ""
		m.input.Placeholder = defaultPlaceholder
		m.invalidateSSE()
	}
	m.selectedID = project.ID
	m.selectedName = project.Name
	return changed
}

func (m *Model) acceptsConnectionResponse(generation int) bool {
	// Zero is reserved for hand-built messages in older tests. Every runtime
	// health command carries a non-zero generation.
	if generation == 0 {
		return true
	}
	if m.connectionGeneration == 0 {
		m.connectionGeneration = generation
		return true
	}
	return m.connectionGeneration == generation
}

func (m *Model) acceptsSSEGeneration(generation int) bool {
	// Zero is reserved for hand-built messages in older tests. Every runtime
	// stream message carries a non-zero generation.
	if generation == 0 {
		return true
	}
	if m.sseGeneration == 0 {
		m.sseGeneration = generation
		return true
	}
	return m.sseGeneration == generation
}

func (m Model) acceptsSSEEvent(ev client.Event) bool {
	projectID := sseEventProjectID(ev)
	if projectID == "" {
		return true // older/single-project event payloads may omit their scope
	}
	return m.selectedID != "" && projectID == m.selectedID
}

func sseEventProjectID(ev client.Event) string {
	var payload struct {
		ProjectID string `json:"project_id"`
	}
	if json.Unmarshal(ev.Data, &payload) != nil {
		return ""
	}
	return strings.TrimSpace(payload.ProjectID)
}

func (m Model) login(username, password string) tea.Cmd {
	c := m.client
	sessionGeneration := sessionGenerationOf(m)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		// Do not include either credential in the returned message or any error
		// text. Client.Login also avoids decoding the login response body.
		return loginResultMsg{sessionGeneration: sessionGeneration, err: c.Login(ctx, username, password)}
	}
}

func (m Model) sendChat(projectID, message string) tea.Cmd {
	c := m.client
	sessionGeneration := sessionGenerationOf(m)
	projectGeneration := projectGenerationOf(m)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		accepted, err := c.SendChatMessage(ctx, projectID, message)
		return chatSentMsg{sessionGeneration: sessionGeneration, projectGeneration: projectGeneration, projectID: projectID, accepted: accepted, err: err}
	}
}

func (m Model) doChatStatus(messageID, projectID string, projectGeneration uint64) tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	status, err := m.client.GetChatStatus(ctx, messageID)
	return chatStatusMsg{
		sessionGeneration: sessionGenerationOf(m),
		projectGeneration: projectGeneration,
		messageID:         messageID,
		projectID:         projectID,
		status:            status,
		err:               err,
	}
}

func (m Model) pollChat(messageID, projectID string, projectGeneration uint64) tea.Cmd {
	return tea.Tick(chatPollInterval, func(time.Time) tea.Msg {
		return m.doChatStatus(messageID, projectID, projectGeneration)
	})
}

// fetchChatStatus issues an immediate (no-tick) GetChatStatus call.
func (m Model) fetchChatStatus(messageID string) tea.Cmd {
	projectID := m.pendingMsgProjectID
	if projectID == "" {
		projectID = m.selectedID
	}
	projectGeneration := m.pendingMsgProjectGeneration
	if projectGeneration == 0 {
		projectGeneration = projectGenerationOf(m)
	}
	return func() tea.Msg { return m.doChatStatus(messageID, projectID, projectGeneration) }
}

// withMessageGeneration tags asynchronous messages produced by a command. The
// wrapper also handles batches so each child keeps the same session and
// project epochs.
func withMessageGeneration(cmd tea.Cmd, sessionGeneration, projectGeneration uint64) tea.Cmd {
	if cmd == nil {
		return nil
	}
	if sessionGeneration == 0 {
		sessionGeneration = 1
	}
	if projectGeneration == 0 {
		projectGeneration = 1
	}
	return func() tea.Msg {
		return tagMessage(cmd(), sessionGeneration, projectGeneration)
	}
}

func tagMessage(msg tea.Msg, sessionGeneration, projectGeneration uint64) tea.Msg {
	switch typed := msg.(type) {
	case tea.BatchMsg:
		batch := make(tea.BatchMsg, len(typed))
		for i, child := range typed {
			batch[i] = withMessageGeneration(child, sessionGeneration, projectGeneration)
		}
		return batch
	case projectsLoadedMsg:
		typed.sessionGeneration = sessionGeneration
		typed.projectGeneration = projectGeneration
		return typed
	case projectCreatedMsg:
		typed.sessionGeneration = sessionGeneration
		typed.projectGeneration = projectGeneration
		return typed
	case loginResultMsg:
		typed.sessionGeneration = sessionGeneration
		return typed
	case chatSentMsg:
		typed.sessionGeneration = sessionGeneration
		typed.projectGeneration = projectGeneration
		return typed
	case chatStatusMsg:
		typed.sessionGeneration = sessionGeneration
		typed.projectGeneration = projectGeneration
		return typed
	case resultMsg:
		typed.sessionGeneration = sessionGeneration
		typed.projectGeneration = projectGeneration
		return typed
	case threadOpenedMsg:
		typed.sessionGeneration = sessionGeneration
		typed.projectGeneration = projectGeneration
		return typed
	case selectorActiveMsg:
		typed.sessionGeneration = sessionGeneration
		typed.projectGeneration = projectGeneration
		return typed
	case statusCountsMsg:
		typed.sessionGeneration = sessionGeneration
		typed.projectGeneration = projectGeneration
		return typed
	default:
		return msg
	}
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
	m.sseGeneration++
	generation := m.sseGeneration
	events, errs := m.client.StreamEvents(ctx, m.selectedID)
	m.sseEvents = events
	m.sseErrs = errs
	return tea.Batch(
		func() tea.Msg { return sseConnectedMsg{generation: generation} },
		m.waitForSSE(generation, events, errs),
	)
}

// waitForSSE blocks on one specific stream and forwards one tagged message.
// Capturing the channels and generation here prevents a later reconnect from
// making a delayed result look like it came from the current stream.
func (m Model) waitForSSE(generation int, events <-chan client.Event, errs <-chan error) tea.Cmd {
	return func() tea.Msg {
		select {
		case ev, ok := <-events:
			if !ok {
				if err, ok := <-errs; ok && err != nil {
					return sseDisconnectedMsg{generation: generation, err: err}
				}
				return sseDisconnectedMsg{generation: generation}
			}
			return sseEventMsg{generation: generation, event: ev}
		case err := <-errs:
			return sseDisconnectedMsg{generation: generation, err: err}
		}
	}
}

func (m Model) waitForCurrentSSE(generation int) tea.Cmd {
	return m.waitForSSE(generation, m.sseEvents, m.sseErrs)
}

func (m Model) tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m Model) scheduleReconnect(generation int) tea.Cmd {
	backoff := m.sseBackoff
	return tea.Tick(backoff, func(time.Time) tea.Msg { return reconnectTickMsg{generation: generation} })
}

// Cleanup releases the SSE stream; called on shutdown.
func (m *Model) Cleanup() {
	if m.sseCancel != nil {
		m.sseCancel()
	}
}

// invalidateSSE cancels the current stream and advances ownership so all
// messages already queued from it become stale before a replacement starts.
func (m *Model) invalidateSSE() {
	if m.sseCancel != nil {
		m.sseCancel()
	}
	m.sseCancel = nil
	m.sseEvents = nil
	m.sseErrs = nil
	m.sseConnected = false
	m.sseGeneration++
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
		if !m.acceptsConnectionResponse(msg.generation) {
			return m, nil // stale health result from an older check
		}
		wasConnected := m.connected
		wasAuthRequired := m.authRequired
		m.connChecked = true
		m.capacity = msg.capacity
		m.auth = msg.auth
		if client.IsAuthRequired(msg.err) {
			m.markAuthRequired()
			return m, nil
		}
		if msg.err != nil {
			if wasConnected {
				m.append(entry{role: "error", text: "lost connection: " + offlineRecoveryMessage(m.client.BaseURL(), msg.err)})
			}
			m.connected = false
			if !wasAuthRequired {
				m.authRequired = false
			}
			m.connErr = msg.err.Error()
		} else {
			// Capacity proves that the backend is reachable, but it does not
			// establish that the current cookie session is authenticated. Once the
			// model is sign-in-required, retain that state until AuthMe confirms the
			// current session or Login succeeds.
			if wasAuthRequired && (msg.auth == nil || !msg.auth.Authenticated) {
				m.connected = false
				m.authRequired = true
				m.connErr = ""
				return m, nil
			}
			m.connected = true
			m.authRequired = false
			m.connErr = ""
			if !wasConnected {
				m.append(entry{role: "system", text: "Connected to " + m.client.BaseURL() + "."})
			}
			if wasAuthRequired && m.selectedID != "" && (m.sseCancel != nil || m.sseRetryAfterProject) {
				m.sseRetryAfterProject = false
				return m, m.connectSSE()
			}
		}
		return m, nil
	case statusCountsMsg:
		if !m.acceptsSessionGeneration(msg.sessionGeneration) || !m.acceptsProjectGeneration(msg.projectGeneration) {
			return m, nil // stale status counts from an older session or project
		}
		if client.IsAuthRequired(msg.err) {
			m.markAuthRequired()
			return m, nil
		}
		m.pendingAlertCount = msg.pendingAlerts
		m.activeTaskCount = msg.activeTasks
		m.queuedTaskCount = msg.queuedTasks
		return m, nil

	case projectsLoadedMsg:
		if !m.acceptsSessionGeneration(msg.sessionGeneration) || !m.acceptsProjectGeneration(msg.projectGeneration) {
			return m, nil // stale project response from an older session or project
		}
		if !m.acceptsProjectResponse(msg.requestID) {
			return m, nil // stale list from an older project request
		}
		if msg.err != nil {
			if client.IsAuthRequired(msg.err) {
				m.markAuthRequired()
				return m, nil
			}
			m.connected = false
			m.connChecked = true
			m.connErr = msg.err.Error()
			m.append(entry{role: "error", text: "loading projects: " + offlineRecoveryMessage(m.client.BaseURL(), msg.err)})
			return m, nil
		}
		if msg.startSSE {
			m.sseRetryAfterProject = true
		}
		// Project data alone cannot establish an authenticated session. Keep
		// authRequired set until a current health check or login succeeds.
		m.projects = msg.projects
		// An explicit request resolves on its own; defaulting to the first
		// project first would leave a wrong project selected when the name is
		// ambiguous or unknown.
		if msg.selectName != "" {
			return m.pickProject(msg.selectName)
		}
		if m.selectedID == "" && len(m.projects) > 0 {
			m.setActiveProject(m.projects[0])
		}
		var reconnect tea.Cmd
		if !m.authRequired && m.selectedID != "" && (m.sseCancel != nil || m.sseRetryAfterProject) {
			m.sseRetryAfterProject = false
			reconnect = m.connectSSE()
		}
		if msg.echo {
			m.append(entry{role: "result", head: "Projects", text: renderProjects(m.projects, msg.capacities, m.selectedID)})
		}
		return m, reconnect

	case projectCreatedMsg:
		if !m.acceptsSessionGeneration(msg.sessionGeneration) || !m.acceptsProjectGeneration(msg.projectGeneration) {
			return m, nil // stale creation response from an older session or project
		}
		if !m.acceptsProjectResponse(msg.requestID) {
			return m, nil // stale creation after a newer selection or list request
		}
		m.busy = false
		if msg.err != nil {
			if m.handleAuthError(msg.err) {
				return m, nil
			}
			if m.handleTransportError(msg.err) {
				return m, nil
			}
			m.append(entry{role: "error", text: msg.err.Error()})
			return m, nil
		}
		shouldReconnect := m.sseCancel != nil || m.sseRetryAfterProject
		m.setActiveProject(msg.project)
		m.projects = append(m.projects, msg.project)

		if jsonMode {
			body, err := marshalJSON(msg.project)
			if err != nil {
				m.append(entry{role: "error", text: err.Error()})
				return m, nil
			}
			m.append(entry{role: "result", head: "Project", text: body})
		} else if cliMode {
			m.append(entry{
				role: "result",
				head: "Project",
				text: fmt.Sprintf("created project %q at %q\nproject ID: %s\nnext: select it on the next CLI command with -project %s, for example: openvibely-tui -project %s tasks", msg.project.Name, msg.project.Path, msg.project.ID, msg.project.ID, msg.project.ID),
			})
		} else {
			m.append(entry{
				role: "result",
				head: "Project",
				text: fmt.Sprintf("created project %q at %q — active project selected\nnext: send a message or run %sprojects to inspect it", msg.project.Name, msg.project.Path, cmdPrefix),
			})
		}
		if !m.authRequired && m.selectedID != "" && shouldReconnect {
			m.sseRetryAfterProject = false
			return m, m.connectSSE()
		}
		return m, nil

	case resultMsg:
		if !m.acceptsSessionGeneration(msg.sessionGeneration) || !m.acceptsProjectGeneration(msg.projectGeneration) {
			return m, nil // stale command response from an older session or project
		}
		m.busy = false
		if msg.err != nil {
			if m.handleAuthError(msg.err) {
				return m, nil
			}
			if m.handleTransportError(msg.err) {
				return m, nil
			}
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
		if !m.acceptsSessionGeneration(msg.sessionGeneration) || !m.acceptsProjectGeneration(msg.projectGeneration) {
			return m, nil // stale chat acknowledgement from an older session or project
		}
		if msg.projectID != "" && msg.projectID != m.selectedID {
			return m, nil // stale acknowledgement from a different project
		}
		if msg.err != nil {
			m.busy = false
			if m.handleAuthError(msg.err) {
				return m, nil
			}
			if m.handleTransportError(msg.err) {
				return m, nil
			}
			m.append(entry{role: "error", text: "send failed: " + msg.err.Error()})
			return m, nil
		}
		m.pendingMsgID = msg.accepted.MessageID
		m.pendingMsgProjectID = msg.projectID
		if m.pendingMsgProjectID == "" {
			m.pendingMsgProjectID = m.selectedID
		}
		m.pendingMsgProjectGeneration = msg.projectGeneration
		if m.pendingMsgProjectGeneration == 0 {
			m.pendingMsgProjectGeneration = projectGenerationOf(m)
		}
		if msg.accepted.Queued {
			m.append(entry{role: "system", text: "queued behind an active chat turn…"})
		}
		return m, m.pollChat(m.pendingMsgID, m.pendingMsgProjectID, m.pendingMsgProjectGeneration)

	case chatStatusMsg:
		if !m.acceptsSessionGeneration(msg.sessionGeneration) || !m.acceptsProjectGeneration(msg.projectGeneration) {
			return m, nil // stale chat status from an older session or project
		}
		if msg.projectID != "" && msg.projectID != m.selectedID {
			return m, nil // stale status from a different project
		}
		if m.pendingMsgID == "" {
			return m, nil
		}
		if msg.messageID != "" && msg.messageID != m.pendingMsgID {
			return m, nil // status for a different message cannot settle this chat
		}
		if msg.err != nil {
			if m.handleAuthError(msg.err) {
				m.busy = false
				return m, nil
			}
			if m.handleTransportError(msg.err) {
				m.busy = false
				return m, m.pollChat(m.pendingMsgID, m.pendingMsgProjectID, m.pendingMsgProjectGeneration)
			}
			return m, m.pollChat(m.pendingMsgID, m.pendingMsgProjectID, m.pendingMsgProjectGeneration) // transient; keep polling
		}
		switch msg.status.Status {
		case "completed":
			m.pendingMsgID = ""
			m.pendingMsgProjectID = ""
			m.pendingMsgProjectGeneration = 0
			m.busy = false
			m.append(entry{role: "agent", text: msg.status.Response})
			if len(msg.status.TaskIDs) > 0 {
				m.append(entry{role: "system", text: "created tasks: " + strings.Join(msg.status.TaskIDs, ", ")})
			}
			return m, nil
		case "failed", "cancelled":
			m.pendingMsgID = ""
			m.pendingMsgProjectID = ""
			m.pendingMsgProjectGeneration = 0
			m.busy = false
			text := msg.status.Status
			if msg.status.Error != "" {
				text += ": " + msg.status.Error
			}
			m.append(entry{role: "error", text: text})
			return m, nil
		default:
			return m, m.pollChat(m.pendingMsgID, m.pendingMsgProjectID, m.pendingMsgProjectGeneration)
		}

	case threadOpenedMsg:
		if !m.acceptsSessionGeneration(msg.sessionGeneration) || !m.acceptsProjectGeneration(msg.projectGeneration) {
			return m, nil // stale thread response from an older session or project
		}
		m.busy = false
		if msg.err != nil {
			if m.handleAuthError(msg.err) {
				return m, nil
			}
			if m.handleTransportError(msg.err) {
				return m, nil
			}
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

	case loginResultMsg:
		if !m.loginActive || !m.acceptsSessionGeneration(msg.sessionGeneration) {
			return m, nil // stale or canceled login attempt
		}
		m.loginSubmitting = false
		if msg.err != nil {
			m.busy = false
			m.input.SetValue("")
			m.loginPassword = true
			m.input.EchoMode = textinput.EchoPassword
			m.input.Prompt = "password: "
			m.input.Placeholder = "password"
			m.input.Focus()
			if client.IsLoginTransportError(msg.err) {
				m.connected = false
				m.connChecked = true
				m.connErr = msg.err.Error()
				m.auth = nil
				m.sseConnected = false
				m.append(entry{role: "error", text: offlineRecoveryMessage(m.client.BaseURL(), msg.err)})
			} else {
				m.append(entry{role: "error", text: loginFailureText(m.client.BaseURL(), msg.err)})
			}
			return m, nil
		}

		m.invalidateSSE()
		m.advanceSessionGeneration()
		m.finishLogin()
		m.busy = false
		m.authRequired = false
		m.connected = false
		m.connChecked = false
		m.connErr = ""
		m.auth = nil
		m.sseConnected = false
		m.sseRetryAfterProject = true
		m.append(entry{role: "system", text: "signed in; retrying connection and project loading"})
		var projectLoad tea.Cmd
		m, projectLoad = m.beginProjectLoad(false, m.wantProject)
		cmds := []tea.Cmd{m.beginConnectionCheck(), projectLoad}
		if m.pendingMsgID != "" {
			cmds = append(cmds, m.fetchChatStatus(m.pendingMsgID))
		}
		return m, tea.Batch(cmds...)

	case sseConnectedMsg:
		if !m.acceptsSSEGeneration(msg.generation) {
			return m, nil // stale lifecycle message from a canceled stream
		}
		m.sseConnected = true
		m.sseBackoff = time.Second
		return m, nil

	case sseEventMsg:
		if !m.acceptsSSEGeneration(msg.generation) {
			return m, nil // stale event from a canceled stream
		}
		if !m.acceptsSSEEvent(msg.event) {
			return m, m.waitForCurrentSSE(msg.generation)
		}
		m.handleSSEEvent(msg.event)
		if msg.event.Name == "chat_response_done" && m.pendingMsgID != "" {
			var ce client.ChatEvent
			if err := json.Unmarshal(msg.event.Data, &ce); err != nil || ce.ExecID == "" || ce.ExecID != m.pendingMsgID {
				// A project stream carries completions for multiple chats. An
				// event without the pending execution ID must not settle this
				// chat or trigger a status fetch for the wrong execution.
				return m, m.waitForCurrentSSE(msg.generation)
			}
			// Ignore events from a foreign project. Allow empty ProjectID
			// for single-project servers that omit the field.
			if ce.ProjectID != "" && ce.ProjectID != m.selectedID {
				return m, m.waitForCurrentSSE(msg.generation)
			}
			// Fast path: if the backend populated CompletedOutput in the SSE
			// payload, display it immediately without an extra HTTP round-trip.
			if ce.CompletedOutput != "" {
				m.pendingMsgID = ""
				m.pendingMsgProjectID = ""
				m.pendingMsgProjectGeneration = 0
				m.busy = false
				m.append(entry{role: "agent", text: ce.CompletedOutput})
				return m, m.waitForCurrentSSE(msg.generation)
			}
			// Otherwise issue an immediate status fetch instead of waiting for
			// the next 1500ms poll tick.
			return m, tea.Batch(m.waitForCurrentSSE(msg.generation), m.fetchChatStatus(m.pendingMsgID))
		}
		return m, m.waitForCurrentSSE(msg.generation)
	case sseDisconnectedMsg:
		if !m.acceptsSSEGeneration(msg.generation) {
			return m, nil // stale disconnect from a canceled stream
		}
		// The stream is terminal now. Advance ownership before scheduling the
		// retry so an optimistic connected message from the same attempt cannot
		// arrive afterward and mark the dead stream live again.
		m.sseGeneration++
		reconnectGeneration := m.sseGeneration
		m.sseConnected = false
		if client.IsAuthRequired(msg.err) {
			m.markAuthRequired()
			return m, nil
		}
		if m.authRequired {
			return m, nil
		}
		cmd := m.scheduleReconnect(reconnectGeneration)
		if m.sseBackoff < 30*time.Second {
			m.sseBackoff *= 2
		}
		return m, cmd

	case reconnectTickMsg:
		if !m.acceptsSSEGeneration(msg.generation) {
			return m, nil // stale retry from a canceled stream
		}
		if m.authRequired || m.loginActive {
			return m, nil
		}
		return m, m.connectSSE()
	case tickMsg:
		if m.loginActive {
			// Keep the periodic timer alive, but do not start a health check while
			// a login attempt owns the form. A current auth result would otherwise
			// advance the session epoch and invalidate the in-flight login result.
			return m, m.tick()
		}
		return m, tea.Batch(m.beginConnectionCheck(), m.tick())

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m Model) beginLogin() (Model, tea.Cmd) {
	if m.loginActive {
		return m, nil
	}
	m.advanceSessionGeneration()
	m.invalidateConnectionChecks()
	m.loginResumeSSE = m.sseCancel != nil && m.connected && !m.authRequired && m.selectedID != ""
	m.invalidateSSE()
	m.loginRestorePrompt = m.input.Prompt
	m.loginRestorePlaceholder = m.input.Placeholder
	m.loginRestoreEchoMode = m.input.EchoMode
	m.loginActive = true
	m.loginPassword = false
	m.loginSubmitting = false
	m.loginUsername = ""
	m.busy = false
	m.menu = nil
	m.input.SetValue("")
	m.input.EchoMode = textinput.EchoNormal
	m.input.Prompt = "username: "
	m.input.Placeholder = "username"
	m.input.Focus()
	m.append(entry{role: "system", text: "sign-in: enter username, then password. Esc cancels."})
	return m, nil
}

func (m *Model) finishLogin() {
	m.loginActive = false
	m.loginPassword = false
	m.loginSubmitting = false
	m.loginUsername = ""
	m.loginResumeSSE = false
	m.input.SetValue("")
	m.input.Prompt = m.loginRestorePrompt
	m.input.Placeholder = m.loginRestorePlaceholder
	m.input.EchoMode = m.loginRestoreEchoMode
	m.input.Focus()
	m.menu = nil
}

func (m *Model) cancelLogin() tea.Cmd {
	resumeSSE := m.loginResumeSSE
	m.loginActive = false
	m.loginPassword = false
	m.loginSubmitting = false
	m.loginUsername = ""
	m.loginResumeSSE = false
	m.busy = false
	m.input.SetValue("")
	m.input.Prompt = m.loginRestorePrompt
	m.input.Placeholder = m.loginRestorePlaceholder
	m.input.EchoMode = m.loginRestoreEchoMode
	m.input.Focus()
	m.menu = nil
	m.append(entry{role: "system", text: "sign-in cancelled"})
	if resumeSSE {
		return m.connectSSE()
	}
	return nil
}

func (m Model) handleLoginKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "ctrl+d":
		m.quitting = true
		m.Cleanup()
		return m, tea.Quit
	case "esc":
		if !m.loginSubmitting {
			return m, m.cancelLogin()
		}
		return m, nil
	case "enter":
		if m.loginSubmitting {
			return m, nil
		}
		if !m.loginPassword {
			username := strings.TrimSpace(m.input.Value())
			if username == "" {
				m.append(entry{role: "error", text: "username is required"})
				return m, nil
			}
			m.loginUsername = username
			m.loginPassword = true
			m.input.SetValue("")
			m.input.EchoMode = textinput.EchoPassword
			m.input.Prompt = "password: "
			m.input.Placeholder = "password"
			return m, nil
		}

		password := m.input.Value()
		if password == "" {
			m.append(entry{role: "error", text: "password is required"})
			return m, nil
		}
		username := m.loginUsername
		m.input.SetValue("")
		m.input.Blur()
		m.loginSubmitting = true
		m.busy = true
		return m, m.login(username, password)
	}

	if m.loginSubmitting {
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func loginFailureText(baseURL string, err error) string {
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "invalid credentials") {
		return "sign-in failed: invalid credentials; try again or press Esc to cancel."
	}
	return "sign-in failed for " + baseURL + "; check the credentials and backend, then try again or press Esc to cancel."
}

// handleKey routes keys; the input owns almost everything.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.loginActive {
		return m.handleLoginKey(msg)
	}
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
			m.input.SetValue(completeSlashInput(m.input.Value(), c))
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
	return withMessageGeneration(run("Thread · "+title, cmdTimeout, func(ctx context.Context) (string, error) {
		if err := c.SendTaskThreadMessage(ctx, taskID, text); err != nil {
			return "", err
		}
		d, err := c.GetTask(ctx, taskID)
		if err != nil {
			return "sent", nil
		}
		return renderThread(d), nil
	}), sessionGenerationOf(m), projectGenerationOf(m))
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
	width := m.effectiveTranscriptWidth()
	canAppend := m.transcriptReady && m.transcriptRenderWidth == width && len(m.transcriptBlocks) == len(m.log)
	block := ""
	if canAppend {
		block = renderTranscriptEntry(e, transcriptWrap(width))
	}

	m.log = append(m.log, e)
	dropped := 0
	if len(m.log) > maxTranscript {
		dropped = len(m.log) - maxTranscript
		m.log = m.log[dropped:]
	}
	if !canAppend || dropped > len(m.transcriptBlocks) {
		m.refreshTranscript()
		return
	}

	if dropped > 0 {
		cut := 0
		for _, old := range m.transcriptBlocks[:dropped] {
			cut += len(old)
		}
		if cut > len(m.transcriptContent) {
			m.refreshTranscript()
			return
		}
		m.transcriptContent = m.transcriptContent[cut:]
		m.transcriptBlocks = m.transcriptBlocks[dropped:]
	}
	m.transcriptBlocks = append(m.transcriptBlocks, block)
	m.transcriptContent += block
	m.transcript.SetContent(m.transcriptContent)
	m.transcript.GotoBottom()
}

func (m Model) transcriptHeight() int {
	// Normal layout reserves 5 rows (header + blank + input/menu + help + margin).
	reserve := 5
	if m.selectorActive {
		// The selector takes up to 12 rows (title + filter + 8 items + overflow +
		// hint), so reserve 14 rows to leave a small margin.
		reserve = 14
	}
	h := m.height - reserve
	if h < 3 {
		h = 3
	}
	return h
}

func (m *Model) resize() {
	m.transcript.Width = m.width
	m.transcript.Height = m.transcriptHeight()
	m.input.Width = m.width - 4
	m.refreshTranscript()
}

func (m *Model) refreshTranscript() {
	width := m.effectiveTranscriptWidth()
	wrap := transcriptWrap(width)

	var b strings.Builder
	blocks := make([]string, 0, len(m.log))
	for _, e := range m.log {
		block := renderTranscriptEntry(e, wrap)
		b.WriteString(block)
		blocks = append(blocks, block)
	}
	m.transcriptContent = b.String()
	m.transcriptBlocks = blocks
	m.transcriptRenderWidth = width
	m.transcriptReady = true
	m.transcript.SetContent(m.transcriptContent)
	m.transcript.GotoBottom()
}

func (m Model) effectiveTranscriptWidth() int {
	width := m.transcript.Width
	if width <= 0 {
		width = 80
	}
	return width
}

func transcriptWrap(width int) lipgloss.Style {
	return lipgloss.NewStyle().Width(width - 2)
}

func renderTranscriptEntry(e entry, wrap lipgloss.Style) string {
	var b strings.Builder
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
	return b.String()
}

// handleSSEEvent formats a live event; shown only when /events is on.
func (m *Model) handleSSEEvent(ev client.Event) {
	if !m.acceptsSSEEvent(ev) || !m.showEvents {
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

func (m *Model) markAuthRequired() {
	wasRequired := m.authRequired
	m.authRequired = true
	m.connected = false
	m.connChecked = true
	m.connErr = ""
	// An accepted auth failure starts a new session epoch. This invalidates
	// project, command, chat, selector, and thread results launched before the
	// backend reported that the session was unauthorized.
	if !wasRequired {
		m.advanceSessionGeneration()
		m.invalidateSSE()
	}
	// An auth failure can arrive from project/SSE work while a health check is
	// still running. Invalidate every in-flight check before it can clear the
	// sign-in-required state.
	m.invalidateConnectionChecks()
	if !wasRequired {
		m.append(entry{role: "error", text: authRecoveryMessage(m.client.BaseURL())})
	}
}

func (m *Model) handleTransportError(err error) bool {
	if !client.IsTransportError(err) {
		return false
	}
	wasOffline := !m.connected && m.connErr != ""
	m.connected = false
	m.connChecked = true
	m.connErr = err.Error()
	// Preserve known auth-required precedence while also retaining the network
	// details needed to explain a temporary offline condition.
	if !wasOffline {
		m.append(entry{role: "error", text: offlineRecoveryMessage(m.client.BaseURL(), err)})
	}
	return true
}

func (m *Model) handleAuthError(err error) bool {
	if !client.IsAuthRequired(err) {
		return false
	}
	m.markAuthRequired()
	return true
}

func authRecoveryMessage(baseURL string) string {
	return fmt.Sprintf("OpenVibely backend at %s requires sign-in.\nUse /login to enter credentials in the TUI. For CLI runs, use OPENVIBELY_AUTH_USERNAME and OPENVIBELY_AUTH_PASSWORD (or the existing -user/-pass flags). Credentials are not displayed or saved.", baseURL)
}

func offlineRecoveryMessage(baseURL string, err error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Unable to reach the OpenVibely backend at %s.", baseURL)
	b.WriteString("\nTry:\n")
	b.WriteString("  - Start or check your local OpenVibely backend, then run /status.\n")
	b.WriteString("  - Use -server <url> or OPENVIBELY_SERVER_URL to point at a running backend.")
	if err != nil {
		b.WriteString("\nDetails: ")
		b.WriteString(err.Error())
	}
	return b.String()
}
