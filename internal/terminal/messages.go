package terminal

import (
	"github.com/openvibely/openvibely-terminal/internal/client"
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
	sessionGeneration uint64
	projectGeneration uint64
	requestID         uint64
	projects          []client.Project
	capacities        []client.ProjectCapacity
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
	sessionGeneration uint64
	projectGeneration uint64
	requestID         uint64
	project           client.Project
	// startSSE requests that the selected project receive a scoped stream even
	// when project creation was the first successful backend operation.
	startSSE bool
	err      error
}

// projectUpdatedMsg carries an authoritative post-save settings refresh.
type projectUpdatedMsg struct {
	sessionGeneration uint64
	projectGeneration uint64
	projectID         string
	settings          *client.ProjectSettings
	saved             bool
	err               error
}

// loginResultMsg reports the outcome of an interactive cookie-session login.
// Credentials are intentionally not carried in this message.
type loginResultMsg struct {
	sessionGeneration uint64
	err               error
}

// chatSentMsg reports the accepted async chat message.
type chatSentMsg struct {
	sessionGeneration uint64
	projectGeneration uint64
	projectID         string
	taskID            string // non-empty for an interactive task-thread follow-up
	threadRequestID   uint64
	submissionID      uint64
	accepted          *client.ChatAccepted
	err               error
}

// chatStatusMsg carries the polled status of an in-flight chat message.
type chatStatusMsg struct {
	sessionGeneration uint64
	projectGeneration uint64
	messageID         string
	// submissionID identifies the chat turn whose polling request produced this
	// status. It prevents a delayed status from an earlier turn from settling a
	// newer one that happens to share the same project and session.
	submissionID uint64
	// resolvedMessageID is the authoritative execution ID returned by the
	// status endpoint. Queued inputs keep messageID as their polling key while
	// this field records a promoted execution for SSE correlation.
	resolvedMessageID string
	projectID         string
	status            *client.ChatStatus
	err               error
}

type chatStreamEventMsg struct {
	generation   int
	submissionID uint64
	projectID    string
	execID       string
	event        client.ChatOutputEvent
}

type chatStreamRenderMsg struct {
	generation       int
	renderGeneration uint64
	submissionID     uint64
	projectID        string
	execID           string
}

type chatStreamDisconnectedMsg struct {
	generation   int
	submissionID uint64
	projectID    string
	execID       string
	err          error
}

type chatStreamReconnectMsg struct {
	generation   int
	submissionID uint64
	projectID    string
	execID       string
	offset       int
}

// resultMsg is the generic outcome of a slash command: a rendered block to
// append to the transcript, or an error.
type resultMsg struct {
	sessionGeneration uint64
	projectGeneration uint64
	title             string // optional heading
	body              string
	err               error
}

type agentDeleteTargetMsg struct {
	sessionGeneration uint64
	projectGeneration uint64
	projectID         string
	agent             client.AgentDef
	err               error
}

type scheduleDeleteTargetMsg struct {
	sessionGeneration uint64
	projectGeneration uint64
	projectID         string
	schedule          client.ScheduleEntry
	err               error
}

type webhookMutationTargetMsg struct {
	sessionGeneration uint64
	projectGeneration uint64
	projectID         string
	action            string
	webhook           client.Webhook
	err               error
}

type channelWizardStartMsg struct {
	sessionGeneration uint64
	projectGeneration uint64
	projectID         string
	action            string
	channel           client.Channel
	err               error
}

// alertDeleteTargetMsg carries the project-scoped alert resolved before an
// interactive delete confirmation is shown.
type alertDeleteTargetMsg struct {
	sessionGeneration uint64
	projectGeneration uint64
	projectID         string
	alert             client.Alert
	err               error
}

// alertBulkTargetMsg carries all project-scoped alerts selected for a bulk
// mutation. The resolved IDs are retained so a confirmation cannot rebind to a
// changed alert list.
type alertBulkTargetMsg struct {
	sessionGeneration uint64
	projectGeneration uint64
	projectID         string
	action            string
	alerts            []client.Alert
	err               error
}

// attachmentDeleteTargetMsg carries the project-scoped task and attachment
// resolved before an interactive delete confirmation is shown.
type attachmentDeleteTargetMsg struct {
	sessionGeneration uint64
	projectGeneration uint64
	projectID         string
	task              client.Task
	attachment        client.Attachment
	err               error
}

// threadOpenedMsg enters task-thread mode: subsequent plain-text input is
// posted as a follow-up on this task rather than to the project agent.
type threadOpenedMsg struct {
	sessionGeneration uint64
	projectGeneration uint64
	requestID         uint64
	projectID         string
	taskID            string
	title             string
	status            string
	body              string // rendered thread to show on entry
	err               error
}

// threadUpdatedMsg refreshes the currently open running task conversation after
// a matching project-scoped live event.
type threadUpdatedMsg struct {
	sessionGeneration uint64
	projectGeneration uint64
	requestID         uint64
	projectID         string
	taskID            string
	status            string
	body              string
	err               error
}

// threadReplyMsg carries the mutation and refreshed conversation produced by a
// plain-text follow-up while a task thread is active.
type threadReplyMsg struct {
	sessionGeneration uint64
	projectGeneration uint64
	requestID         uint64
	projectID         string
	taskID            string
	body              string
	refreshed         bool
	err               error
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
	ref          string               // dispatched as the command argument (ID/handle/type)
	label        string               // primary display text (name/title)
	detail       string               // dimmed secondary text (status, description…)
	resolvedTask *client.Task         // optional task record already loaded for selector dispatch
	dispatch     selectorItemDispatch // optional direct action for an already-resolved item
}

// selectorActiveMsg asks the model to open the inline ref selector for a
// command that was invoked without its <ref> argument. The command handler
// fetches the candidate list and the model decides what to do with it:
// error, empty hint, auto-select a single item, or open the picker.
type selectorActiveMsg struct {
	sessionGeneration uint64
	projectGeneration uint64
	title             string // transcript heading for notes/empty hints
	command           string // pending command verb, e.g. "tasks open"
	// emptyHint is shown (dimmed) when there is nothing to select.
	emptyHint string
	// prefill, when set, puts "/<command> <ref><prefillSuffix>" into the
	// input instead of dispatching immediately — for commands that need more
	// arguments after the selected ref.
	prefill       bool
	prefillSuffix string
	initialFilter string
	forcePicker   bool // Tab completion must never auto-execute a unique resource
	items         []selectorItem
	warnings      []string
	err           error
}

type automationEditLoadedMsg struct {
	sessionGeneration uint64
	projectGeneration uint64
	projectID         string
	requestID         uint64
	automation        client.Automation
	definition        *client.AutomationDefinition
	err               error
}

type automationEditSavedMsg struct {
	sessionGeneration uint64
	projectGeneration uint64
	projectID         string
	automationID      string
	name              string
	err               error
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
	sessionGeneration uint64
	projectGeneration uint64
	pendingAlerts     int
	activeTasks       int
	queuedTasks       int
	err               error
}
