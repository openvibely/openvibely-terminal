package tui

import (
	"github.com/openvibely/openvibely-tui/internal/client"
)

// Messages produced by async commands and delivered to the Bubble Tea model.

// connCheckedMsg reports the result of a server health/auth check.
type connCheckedMsg struct {
	generation int
	capacity   *client.GlobalCapacity
	auth       *client.AuthStatus
	err        error
}

// projectsLoadedMsg carries the project list (with capacities when available).
type projectsLoadedMsg struct {
	requestID  uint64
	projects   []client.Project
	capacities []client.ProjectCapacity
	// echo, when set, renders the project list into the transcript (i.e. the
	// load was triggered by /projects rather than by startup).
	echo bool
	// selectName, when set, selects the matching project after loading.
	selectName string
	// startSSE requests that the model open a stream only after a project has
	// been installed. Startup and post-login loads use this to avoid an
	// unscoped stream racing project selection.
	startSSE bool
	err      error
}

// projectCreatedMsg carries the backend-created project so the TUI can select
// it without maintaining a separate local project store.
type projectCreatedMsg struct {
	requestID uint64
	project   client.Project
	err       error
}

// loginResultMsg reports the outcome of an interactive cookie-session login.
// Credentials are intentionally not carried in this message.
type loginResultMsg struct {
	err error
}

// chatSentMsg reports the accepted async chat message.
type chatSentMsg struct {
	accepted *client.ChatAccepted
	err      error
}

// chatStatusMsg carries the polled status of an in-flight chat message.
type chatStatusMsg struct {
	status *client.ChatStatus
	err    error
}

// resultMsg is the generic outcome of a slash command: a rendered block to
// append to the transcript, or an error.
type resultMsg struct {
	title string // optional heading
	body  string
	err   error
}

// threadOpenedMsg enters task-thread mode: subsequent plain-text input is
// posted as a follow-up on this task rather than to the project agent.
type threadOpenedMsg struct {
	projectID string
	taskID    string
	title     string
	body      string // rendered thread to show on entry
	err       error
}

// sseEventMsg delivers one live event from the SSE stream.
type sseEventMsg struct {
	generation int
	event      client.Event
}

// sseDisconnectedMsg indicates the SSE stream ended and reconnection is needed.
type sseDisconnectedMsg struct {
	generation int
	err        error
}

// sseConnectedMsg indicates a new SSE stream was established.
type sseConnectedMsg struct {
	generation int
}

// selectorItem is one choice in the inline ref selector.
type selectorItem struct {
	ref      string               // dispatched as the command argument (ID/handle/type)
	label    string               // primary display text (name/title)
	detail   string               // dimmed secondary text (status, description…)
	dispatch selectorItemDispatch // optional direct action for an already-resolved item
}

// selectorActiveMsg asks the model to open the inline ref selector for a
// command that was invoked without its <ref> argument. The command handler
// fetches the candidate list and the model decides what to do with it:
// error, empty hint, auto-select a single item, or open the picker.
type selectorActiveMsg struct {
	title   string // transcript heading for notes/empty hints
	command string // pending command verb, e.g. "tasks open"
	// emptyHint is shown (dimmed) when there is nothing to select.
	emptyHint string
	// prefill, when set, puts "/<command> <ref><prefillSuffix>" into the
	// input instead of dispatching immediately — for commands that need more
	// arguments after the selected ref.
	prefill       bool
	prefillSuffix string
	items         []selectorItem
	err           error
}

// tickMsg drives periodic refresh (status re-check).
type tickMsg struct{}

// reconnectTickMsg fires when the SSE backoff timer elapses. The generation
// identifies the stream whose disconnect scheduled this retry, so a queued
// retry from a canceled stream cannot reopen it after a project switch or
// successful login.
type reconnectTickMsg struct {
	generation int
}

// statusCountsMsg carries the operational counts fetched for the /status command.
type statusCountsMsg struct {
	pendingAlerts int
	activeTasks   int
	queuedTasks   int
}
