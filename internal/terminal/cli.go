package terminal

// Non-interactive (CLI) mode.
//
// The same slash-command registry that drives the chat window also works
// headlessly, so every command available in the TUI is available as a
// subcommand:
//
//	openvibely-terminal tasks
//	openvibely-terminal tasks run refactor
//	openvibely-terminal -project demo chat "ship the docs"
//
// A command is executed by driving the Bubble Tea model synchronously: the
// returned tea.Cmd is invoked, its message fed back into Update, and so on
// until the command settles. The transcript entries it produced are then
// written to stdout.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

// cliDeadline bounds a headless command, including chat polling.
const cliDeadline = 10 * time.Minute

// cliMode is true while RunCLI is executing. It is used by registry.go to
// apply the --force gate rather than a TUI confirmation prompt.
var cliMode bool

// forceMode is true when the CLI --force flag was provided. Destructive
// commands execute without a confirmation prompt only when this is set.
var forceMode bool

// jsonMode is true when the CLI --json flag was provided. List, show, and
// supported mutation commands emit machine-readable JSON to stdout instead of
// styled text.
var jsonMode bool

// RunCLI executes one command without starting the interactive UI and writes
// its output to out. It returns an error when the command reported one.
// force corresponds to the --force CLI flag: when true destructive commands
// skip their guard and execute immediately.
// json corresponds to the --json CLI flag: when true list/show commands emit
// raw JSON instead of styled text.
func RunCLI(c *client.Client, out io.Writer, projectRef string, args []string, force bool, json bool) error {
	return RunCLIWithInput(c, out, nil, projectRef, args, force, json)
}

// RunCLIWithInput executes one command with an optional non-echoing credential
// source for commands that explicitly request it. Callers must not pass secrets
// as command arguments.
func RunCLIWithInput(c *client.Client, out io.Writer, input io.Reader, projectRef string, args []string, force bool, json bool) error {
	return RunCLIContextWithInput(context.Background(), c, out, input, projectRef, args, force, json)
}

// RunCLIContext is RunCLI with a caller-owned lifetime. Long-running commands
// such as the foreground events stream use this context for cancellation.
func RunCLIContext(ctx context.Context, c *client.Client, out io.Writer, projectRef string, args []string, force bool, json bool) error {
	return RunCLIContextWithInput(ctx, c, out, nil, projectRef, args, force, json)
}

// RunCLIContextWithInput is RunCLIContext with an optional credential reader.
func RunCLIContextWithInput(ctx context.Context, c *client.Client, out io.Writer, input io.Reader, projectRef string, args []string, force bool, json bool) error {
	if len(args) == 0 {
		return errors.New("no command given")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Activate CLI mode so destructive commands apply --force gating instead
	// of a TUI confirmation prompt.
	cliMode = true
	forceMode = force
	jsonMode = json
	prevPrefix := cmdPrefix
	defer func() {
		cliMode = false
		forceMode = false
		jsonMode = false
		cmdPrefix = prevPrefix
	}()
	// In CLI mode commands are shell subcommands, so help should print them
	// without the chat window's leading slash.
	cmdPrefix = ""
	fields := append([]string(nil), args...)
	fields[0] = strings.TrimPrefix(strings.TrimSpace(fields[0]), "/")
	name := fields[0]

	cmdDef := lookupCommand(name)
	if cmdDef == nil {
		return fmt.Errorf("unknown command %q — run \"help\" to list commands", name)
	}
	if cmdDef.validateArgs != nil {
		if err := cmdDef.validateArgs(fields[1:]); err != nil {
			return err
		}
	}

	m := New(c)
	m.cliContext = ctx
	m.cliSecretInput = input
	m.width, m.height = 100, 40
	m.transcript.Width = m.width
	m.log = nil // drop the interactive banner

	var eventsOn bool
	if cmdDef.name == "events" {
		var err error
		eventsOn, err = parseCLIEventsAction(fields[1:])
		if err != nil {
			return err
		}
		if !eventsOn {
			return errors.New(cliEventsOffMessage)
		}
	}

	var statusProjectLoadErr error
	var statusCheckResult <-chan tea.Msg
	var statusCountsResult <-chan tea.Msg
	// Only commands that talk to the backend need a project or connection
	// state; /help and friends should stay instant and work offline.
	if cmdDef.needsProjectLoad(args) {
		var projectLoad tea.Cmd
		m, projectLoad = m.beginProjectLoad(false, projectRef)
		if cmdDef.needsStatus() {
			// Project discovery and the global capacity/auth checks are independent.
			// Execute both immediately, but apply their messages below in the same
			// projects-then-connection order used by the serialized path.
			projectResult := make(chan tea.Msg, 1)
			statusResult := make(chan tea.Msg, 1)
			statusCheck := m.checkConnection()
			go func() { projectResult <- projectLoad() }()
			go func() { statusResult <- statusCheck() }()
			statusCheckResult = statusResult
			m = drain(m, func() tea.Msg { return <-projectResult })
		} else {
			m = drain(m, projectLoad)
		}
		if ctx.Err() != nil {
			return cliContextResult(ctx)
		}
		// An unknown or ambiguous project must fail loudly rather than run the
		// command against whichever project happened to be selected. Status is
		// global-first, so a failed project listing becomes a visible partial
		// failure while health, auth, and capacity checks still run.
		if err := firstError(m); err != nil {
			if cmdDef.needsStatus() {
				statusProjectLoadErr = err
				m.statusProjectsUnavailable = true
			} else if m.connErr != "" {
				if client.IsInvalidServerURL(err) || m.connInvalidServerURL {
					return errors.New(InvalidServerURLMessage(c.BaseURL()))
				}
				if m.connReachableError {
					return errors.New(ReachableBackendErrorMessage(c.BaseURL(), errors.New(m.connErr)))
				}
				return errors.New(OfflineRecoveryMessage(c.BaseURL(), errors.New(m.connErr)))
			} else {
				return err
			}
		}
	}

	if err := cliProjectPreflight(*cmdDef, fields[1:], projectRef, m); err != nil {
		return err
	}
	if cmdDef.needsStatus() && statusProjectLoadErr == nil && m.selectedID != "" {
		// The scoped counts depend only on the selected project, not on the
		// global capacity/auth result. Start them as soon as project
		// discovery succeeds, while preserving the deterministic connection-
		// then-counts application order below.
		countResult := make(chan tea.Msg, 1)
		countFetch := m.fetchStatusCounts()
		go func() { countResult <- countFetch() }()
		statusCountsResult = countResult
	}
	implicitProject, hasImplicitProject := cliImplicitProject(*cmdDef, fields[1:], projectRef, m)

	// A one-shot events command owns its stream directly. The interactive model
	// stream is intentionally not started during CLI project preloading, and
	// feeding a long-lived command through drain would delay line output until
	// the stream ended.
	if cmdDef.name == "events" {
		return runCLIEvents(ctx, c, out, m.selectedID, eventsOn, json)
	}

	// Streaming commands own their output directly so each model delta is visible
	// immediately instead of being buffered in the Bubble Tea transcript.
	if handled, err := runCLIStreamingCommand(ctx, c, out, m, *cmdDef, fields, json, implicitProject, hasImplicitProject); handled {
		return err
	}

	if cmdDef.needsStatus() {
		if statusCheckResult != nil {
			m = drain(m, func() tea.Msg { return <-statusCheckResult })
		} else {
			m = drain(m, m.checkConnection())
		}
		if statusCountsResult != nil {
			m = drain(m, func() tea.Msg {
				msg := <-statusCountsResult
				// A concurrent auth failure may advance the session generation
				// while these counts are in flight. They belong to this same
				// one-shot status operation, so apply them to the current model
				// generation instead of discarding a successful partial result as
				// stale.
				if counts, ok := msg.(statusCountsMsg); ok {
					counts.sessionGeneration = sessionGenerationOf(m)
					counts.projectGeneration = projectGenerationOf(m)
					return counts
				}
				return msg
			})
		} else {
			m = drain(m, m.fetchStatusCounts())
		}
	}

	if ctx.Err() != nil {
		return cliContextResult(ctx)
	}

	start := len(m.log)
	next, cmd := m.runCommandFields(fields)
	m = next.(Model)
	m = drain(m, cmd)

	if ctx.Err() != nil {
		return cliContextResult(ctx)
	}

	commandErr := firstError(m)
	if commandErr == nil && hasImplicitProject {
		if jsonMode {
			writeScopedJSONEntries(out, m.log[start:], implicitProject)
		} else {
			writeScopedEntries(out, m.log[start:], implicitProject)
		}
	} else if jsonMode {
		writeJSONEntries(out, m.log[start:])
	} else {
		writeEntries(out, m.log[start:])
	}
	if statusProjectLoadErr != nil {
		return fmt.Errorf("status partial failure: %w", statusProjectLoadErr)
	}
	return commandErr
}

const cliEventsOffMessage = "events off cannot disable a foreground stream owned by another process; press Ctrl-C in that monitoring process (interactive /events off only hides events in that TUI)"

const cliStreamReconnectLimit = 8

// cliExecutionRecord is emitted as NDJSON for streaming commands. One record is
// written per incremental delta and one terminal record closes the stream.
type cliExecutionRecord struct {
	Type      string `json:"type"`
	ProjectID string `json:"project_id"`
	TaskID    string `json:"task_id,omitempty"`
	ExecID    string `json:"exec_id"`
	Offset    int    `json:"offset"`
	Delta     string `json:"delta,omitempty"`
	Status    string `json:"status,omitempty"`
	Error     string `json:"error,omitempty"`
}

func runCLIStreamingCommand(ctx context.Context, c *client.Client, out io.Writer, m Model, def command, fields []string, jsonOutput bool, implicit client.Project, hasImplicit bool) (bool, error) {
	if def.name == "chat" && len(fields) > 1 {
		if hasImplicit && !jsonOutput {
			writeScopedEntries(out, nil, implicit)
		}
		streamCtx, cancel := context.WithTimeout(ctx, cliDeadline)
		defer cancel()
		accepted, err := c.SendChatMessage(streamCtx, m.selectedID, strings.Join(fields[1:], " "))
		if err != nil {
			return true, cliStreamDiagnostic(c, err)
		}
		if accepted == nil || strings.TrimSpace(accepted.MessageID) == "" {
			return true, errors.New("send failed: empty acknowledgement")
		}
		originalID := accepted.MessageID
		execID := originalID
		status := func() (*client.ChatStatus, error) { return c.GetChatStatus(streamCtx, originalID) }
		if accepted.Queued {
			execID, err = waitForCLIChatExecution(streamCtx, status, originalID)
			if err != nil {
				return true, cliStreamDiagnostic(c, err)
			}
		}
		return true, streamCLIExecution(streamCtx, c, out, m.selectedID, "", execID, jsonOutput, status)
	}

	if def.name != "tasks" || len(fields) < 2 || !strings.EqualFold(fields[1], "reply") {
		return false, nil
	}
	if len(fields) < 3 {
		return false, nil // retain registry usage errors for malformed invocations
	}
	target, message := splitPipe(strings.Join(fields[2:], " "))
	if target == "" || message == "" {
		return false, nil
	}
	streamCtx, cancel := context.WithTimeout(ctx, cliDeadline)
	defer cancel()
	task, err := resolveTask(streamCtx, c, m.selectedID, target)
	if err != nil {
		return true, cliStreamDiagnostic(c, err)
	}
	accepted, err := c.SendTaskThreadMessageForProject(streamCtx, task.ID, m.selectedID, message)
	if err != nil {
		return true, cliStreamDiagnostic(c, err)
	}
	if accepted == nil {
		return true, errors.New("task reply failed: empty acknowledgement")
	}
	execID := strings.TrimSpace(accepted.ExecID)
	acceptedID := firstNonEmpty(strings.TrimSpace(accepted.PendingInputID), execID)
	if acceptedID == "" {
		if hasImplicit && !jsonOutput {
			writeScopedEntries(out, nil, implicit)
		}
		if jsonOutput {
			return true, writeCLIExecutionRecord(out, cliExecutionRecord{
				Type:      "accepted",
				ProjectID: m.selectedID,
				TaskID:    task.ID,
				Status:    "accepted",
			}, true)
		}
		if _, err := fmt.Fprintf(out, "sent to thread of %s\n", task.Title); err != nil {
			return true, fmt.Errorf("writing task reply confirmation: %w", err)
		}
		return true, nil
	}
	status := func() (*client.ChatStatus, error) { return c.GetChatStatus(streamCtx, acceptedID) }
	if execID == "" {
		execID, err = waitForCLIChatExecution(streamCtx, status, acceptedID)
		if err != nil {
			return true, cliStreamDiagnostic(c, err)
		}
	}
	if hasImplicit && !jsonOutput {
		writeScopedEntries(out, nil, implicit)
	}
	return true, streamCLIExecution(streamCtx, c, out, m.selectedID, task.ID, execID, jsonOutput, status)
}

func waitForCLIChatExecution(ctx context.Context, status func() (*client.ChatStatus, error), originalID string) (string, error) {
	for {
		if err := ctx.Err(); err != nil {
			return "", cliContextResult(ctx)
		}
		current, err := status()
		if err != nil {
			return "", err
		}
		if current != nil {
			switch current.Status {
			case "completed":
				return firstNonEmpty(current.MessageID, originalID), nil
			case "failed", "cancelled":
				return "", cliTerminalError(current.Status, current.Error)
			}
			if id := strings.TrimSpace(current.MessageID); id != "" && id != originalID {
				return id, nil
			}
		}
		if !waitCLIStreamRetry(ctx, 1) {
			return "", cliContextResult(ctx)
		}
	}
}

func streamCLIExecution(ctx context.Context, c *client.Client, out io.Writer, projectID, taskID, execID string, jsonOutput bool, status func() (*client.ChatStatus, error)) error {
	if strings.TrimSpace(execID) == "" {
		return nil
	}
	offset := 0
	reconnects := 0
	plainWrote := false
	plainEndsNewline := false
	finishPlain := func() error {
		if jsonOutput || !plainWrote || plainEndsNewline {
			return nil
		}
		if _, err := io.WriteString(out, "\n"); err != nil {
			return fmt.Errorf("writing streamed output: %w", err)
		}
		plainEndsNewline = true
		return nil
	}
	finishForContext := func() error {
		if err := finishPlain(); err != nil {
			return err
		}
		return cliContextResult(ctx)
	}
	for {
		if ctx.Err() != nil {
			return finishForContext()
		}
		events, errs := c.StreamExecution(ctx, execID, offset)
		terminal := false
		var streamErr error
		for events != nil || errs != nil {
			select {
			case <-ctx.Done():
				return finishForContext()
			case event, ok := <-events:
				if !ok {
					events = nil
					continue
				}
				switch event.Type {
				case client.ExecutionDelta:
					if event.Offset <= offset {
						continue
					}
					delta := event.Data
					start := event.Offset - len([]byte(delta))
					if start < offset {
						delta = string([]byte(delta)[offset-start:])
					}
					if err := writeCLIExecutionRecord(out, cliExecutionRecord{Type: client.ExecutionDelta, ProjectID: projectID, TaskID: taskID, ExecID: execID, Offset: event.Offset, Delta: delta}, jsonOutput); err != nil {
						return err
					}
					if !jsonOutput && delta != "" {
						plainWrote = true
						plainEndsNewline = strings.HasSuffix(delta, "\n")
					}
					offset = event.Offset
					reconnects = 0
				case client.ExecutionDone, client.ExecutionError:
					// Terminal stream frames are acceleration signals only. The
					// status endpoint owns final output, status, and error details.
					terminal = true
				}
			case err, ok := <-errs:
				if !ok {
					errs = nil
					continue
				}
				errs = nil
				streamErr = err
			}
		}
		if status != nil {
			current, err := status()
			if err == nil && current != nil {
				resolved := strings.TrimSpace(current.MessageID)
				if resolved != "" && resolved != execID && current.Status != "completed" {
					execID = resolved
					offset = 0
				}
				if current.Status == "completed" {
					previousOffset := offset
					if err := writeAuthoritativeCLISuffix(out, projectID, taskID, execID, &offset, current.Response, jsonOutput); err != nil {
						return err
					}
					if !jsonOutput && offset > previousOffset {
						plainWrote = true
						plainEndsNewline = strings.HasSuffix(current.Response, "\n")
					}
					if err := writeCLIExecutionRecord(out, cliExecutionRecord{Type: client.ExecutionDone, ProjectID: projectID, TaskID: taskID, ExecID: execID, Offset: offset, Status: "completed"}, jsonOutput); err != nil {
						return err
					}
					return finishPlain()
				}
				if current.Status == "failed" || current.Status == "cancelled" {
					_ = writeCLIExecutionRecord(out, cliExecutionRecord{Type: client.ExecutionError, ProjectID: projectID, TaskID: taskID, ExecID: execID, Offset: offset, Status: current.Status, Error: current.Error}, jsonOutput)
					if err := finishPlain(); err != nil {
						return err
					}
					return cliTerminalError(current.Status, current.Error)
				}
			} else if err != nil {
				streamErr = err
			}
		} else if terminal {
			if err := finishPlain(); err != nil {
				return err
			}
			return errors.New("execution ended without authoritative status")
		}
		if streamErr != nil && (client.IsAuthRequired(streamErr) || (!errors.Is(streamErr, client.ErrEventStreamClosed) && !client.IsTransportError(streamErr) && streamErr.Error() != "timeout" && streamErr.Error() != "execution not found")) {
			if err := finishPlain(); err != nil {
				return err
			}
			return cliStreamDiagnostic(c, streamErr)
		}
		reconnects++
		if reconnects > cliStreamReconnectLimit {
			if err := finishPlain(); err != nil {
				return err
			}
			return cliStreamDiagnostic(c, firstNonNil(streamErr, client.ErrEventStreamClosed))
		}
		if !waitCLIStreamRetry(ctx, reconnects) {
			return finishForContext()
		}
	}
}

func writeAuthoritativeCLISuffix(out io.Writer, projectID, taskID, execID string, offset *int, response string, jsonOutput bool) error {
	responseBytes := []byte(response)
	if len(responseBytes) <= *offset {
		return nil
	}
	delta := string(responseBytes[*offset:])
	*offset = len(responseBytes)
	return writeCLIExecutionRecord(out, cliExecutionRecord{Type: client.ExecutionDelta, ProjectID: projectID, TaskID: taskID, ExecID: execID, Offset: *offset, Delta: delta}, jsonOutput)
}

func writeCLIExecutionRecord(out io.Writer, record cliExecutionRecord, jsonOutput bool) error {
	if !jsonOutput {
		if record.Type != client.ExecutionDelta || record.Delta == "" {
			return nil
		}
		if _, err := io.WriteString(out, record.Delta); err != nil {
			return fmt.Errorf("writing streamed output: %w", err)
		}
		return nil
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encoding streamed output: %w", err)
	}
	if _, err := fmt.Fprintln(out, string(encoded)); err != nil {
		return fmt.Errorf("writing streamed output: %w", err)
	}
	return nil
}

func cliContextResult(ctx context.Context) error {
	if ctx == nil || ctx.Err() == nil || errors.Is(ctx.Err(), context.Canceled) {
		return nil
	}
	return ctx.Err()
}

func waitCLIStreamRetry(ctx context.Context, attempt int) bool {
	delay := time.Duration(attempt) * 25 * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func cliTerminalError(status, detail string) error {
	if strings.TrimSpace(detail) == "" {
		return errors.New(status)
	}
	return fmt.Errorf("%s: %s", status, detail)
}

func cliStreamDiagnostic(c *client.Client, err error) error {
	if err == nil {
		return nil
	}
	if client.IsAuthRequired(err) {
		return errors.New(authRecoveryMessage(c.BaseURL()))
	}
	if client.IsInvalidServerURL(err) {
		return errors.New(InvalidServerURLMessage(c.BaseURL()))
	}
	if client.IsTransportError(err) {
		return errors.New(OfflineRecoveryMessage(c.BaseURL(), err))
	}
	return err
}

func firstNonNil(values ...error) error {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

// cliEventRecord is the stable machine-readable envelope for one foreground
// event. The raw payload is retained in data so newer backend fields remain
// inspectable without changing the top-level fields consumed by scripts.
type cliEventRecord struct {
	Event           string          `json:"event"`
	Type            string          `json:"type"`
	ProjectID       string          `json:"project_id"`
	TaskID          string          `json:"task_id"`
	TaskName        string          `json:"task_name"`
	Status          string          `json:"status"`
	Category        string          `json:"category"`
	Message         string          `json:"message"`
	ExecID          string          `json:"exec_id"`
	Source          string          `json:"source"`
	AgentName       string          `json:"agent_name"`
	CompletedOutput string          `json:"completed_output"`
	Queued          bool            `json:"queued"`
	Data            json.RawMessage `json:"data,omitempty"`
}

type cliEventRawPayload struct {
	Message         json.RawMessage
	CompletedOutput json.RawMessage
}

// cliEventJSONRecord keeps potentially large string values in their validated
// raw form so JSON mode does not decode and re-encode them unnecessarily.
type cliEventJSONRecord struct {
	Event           string          `json:"event"`
	Type            string          `json:"type"`
	ProjectID       string          `json:"project_id"`
	TaskID          string          `json:"task_id"`
	TaskName        string          `json:"task_name"`
	Status          string          `json:"status"`
	Category        string          `json:"category"`
	Message         json.RawMessage `json:"message"`
	ExecID          string          `json:"exec_id"`
	Source          string          `json:"source"`
	AgentName       string          `json:"agent_name"`
	CompletedOutput json.RawMessage `json:"completed_output"`
	Queued          bool            `json:"queued"`
	Data            json.RawMessage `json:"data,omitempty"`
}

// cliEventPayload covers the fields used by the backend's task and chat SSE
// payloads. Unknown fields are preserved in cliEventRecord.Data.
type cliEventPayload struct {
	Type            string `json:"type"`
	ProjectID       string `json:"project_id"`
	TaskID          string `json:"task_id"`
	TaskName        string `json:"task_name"`
	Status          string `json:"status"`
	Category        string `json:"category"`
	Message         string `json:"message"`
	ExecID          string `json:"exec_id"`
	Source          string `json:"source"`
	AgentName       string `json:"agent_name"`
	CompletedOutput string `json:"completed_output"`
	Queued          bool   `json:"queued"`
}

// scanCLIEventPayload validates one JSON value and extracts only the fields
// needed by the CLI envelope. Unknown values are skipped without decoding them;
// malformed input is handled by the compatibility fallback in formatCLIEventBytes.
func scanCLIEventPayload(raw []byte) (cliEventPayload, cliEventRawPayload, bool) {
	var payload cliEventPayload
	var rawPayload cliEventRawPayload
	pos := skipCLIJSONSpace(raw, 0)
	if pos >= len(raw) {
		return payload, rawPayload, false
	}
	if raw[pos] != '{' {
		end, ok := scanCLIJSONValue(raw, pos, 0)
		return payload, rawPayload, ok && skipCLIJSONSpace(raw, end) == len(raw)
	}

	pos++
	pos = skipCLIJSONSpace(raw, pos)
	if pos < len(raw) && raw[pos] == '}' {
		return payload, rawPayload, skipCLIJSONSpace(raw, pos+1) == len(raw)
	}
	for {
		keyStart := pos
		keyEnd, ok := scanCLIJSONString(raw, pos)
		if !ok {
			return payload, rawPayload, false
		}
		var key string
		if err := json.Unmarshal(raw[keyStart:keyEnd], &key); err != nil {
			return payload, rawPayload, false
		}
		pos = skipCLIJSONSpace(raw, keyEnd)
		if pos >= len(raw) || raw[pos] != ':' {
			return payload, rawPayload, false
		}
		valueStart := skipCLIJSONSpace(raw, pos+1)
		valueEnd, ok := scanCLIJSONValue(raw, valueStart, 0)
		if !ok {
			return payload, rawPayload, false
		}
		value := raw[valueStart:valueEnd]
		switch {
		case strings.EqualFold(key, "type"):
			_ = json.Unmarshal(value, &payload.Type)
		case strings.EqualFold(key, "project_id"):
			_ = json.Unmarshal(value, &payload.ProjectID)
		case strings.EqualFold(key, "task_id"):
			_ = json.Unmarshal(value, &payload.TaskID)
		case strings.EqualFold(key, "task_name"):
			_ = json.Unmarshal(value, &payload.TaskName)
		case strings.EqualFold(key, "status"):
			_ = json.Unmarshal(value, &payload.Status)
		case strings.EqualFold(key, "category"):
			_ = json.Unmarshal(value, &payload.Category)
		case strings.EqualFold(key, "message"):
			if len(value) > 0 && value[0] == '"' {
				if !isCLIJSONCanonicalString(value) {
					return payload, rawPayload, false
				}
				rawPayload.Message = value
			}
		case strings.EqualFold(key, "exec_id"):
			_ = json.Unmarshal(value, &payload.ExecID)
		case strings.EqualFold(key, "source"):
			_ = json.Unmarshal(value, &payload.Source)
		case strings.EqualFold(key, "agent_name"):
			_ = json.Unmarshal(value, &payload.AgentName)
		case strings.EqualFold(key, "completed_output"):
			if len(value) > 0 && value[0] == '"' {
				if !isCLIJSONCanonicalString(value) {
					return payload, rawPayload, false
				}
				rawPayload.CompletedOutput = value
			}
		case strings.EqualFold(key, "queued"):
			_ = json.Unmarshal(value, &payload.Queued)
		}
		pos = skipCLIJSONSpace(raw, valueEnd)
		if pos >= len(raw) {
			return payload, rawPayload, false
		}
		switch raw[pos] {
		case ',':
			pos = skipCLIJSONSpace(raw, pos+1)
		case '}':
			return payload, rawPayload, skipCLIJSONSpace(raw, pos+1) == len(raw)
		default:
			return payload, rawPayload, false
		}
	}
}

func skipCLIJSONSpace(raw []byte, pos int) int {
	for pos < len(raw) {
		switch raw[pos] {
		case 0x20, 0x09, 0x0a, 0x0d:
			pos++
		default:
			return pos
		}
	}
	return pos
}

func scanCLIJSONString(raw []byte, pos int) (int, bool) {
	if pos >= len(raw) || raw[pos] != '"' {
		return pos, false
	}
	for pos++; pos < len(raw); pos++ {
		switch raw[pos] {
		case '"':
			return pos + 1, true
		case '\\':
			pos++
			if pos >= len(raw) {
				return pos, false
			}
			switch raw[pos] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				if pos+4 >= len(raw) || !isCLIJSONHex(raw[pos+1]) || !isCLIJSONHex(raw[pos+2]) ||
					!isCLIJSONHex(raw[pos+3]) || !isCLIJSONHex(raw[pos+4]) {
					return pos, false
				}
				pos += 4
			default:
				return pos, false
			}
		default:
			if raw[pos] < 0x20 {
				return pos, false
			}
		}
	}
	return pos, false
}

func isCLIJSONCanonicalString(raw []byte) bool {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return false
	}
	// A string without escapes is already in the spelling emitted by
	// encoding/json; RawMessage embedding will handle HTML and line-separator
	// escaping while it writes the value.
	if bytes.IndexByte(raw, 0x5c) < 0 {
		return true
	}
	if !utf8.Valid(raw) {
		return false
	}
	for i := 1; i < len(raw)-1; i++ {
		switch raw[i] {
		case '"':
			return false
		case 0x5c:
			i++
			if i >= len(raw)-1 {
				return false
			}
			switch raw[i] {
			case '"', 0x5c, 'b', 'f', 'n', 'r', 't':
			case 'u':
				if i+4 >= len(raw) || !isCLIJSONHex(raw[i+1]) || !isCLIJSONHex(raw[i+2]) ||
					!isCLIJSONHex(raw[i+3]) || !isCLIJSONHex(raw[i+4]) {
					return false
				}
				code := uint16(cliJSONHexValue(raw[i+1])<<12 | cliJSONHexValue(raw[i+2])<<8 |
					cliJSONHexValue(raw[i+3])<<4 | cliJSONHexValue(raw[i+4]))
				if !isCLIJSONCanonicalUnicodeEscape(code) {
					return false
				}
				i += 4
			default:
				return false
			}
		}
	}
	return true
}

func cliJSONHexValue(c byte) uint16 {
	switch {
	case c >= '0' && c <= '9':
		return uint16(c - '0')
	case c >= 'a' && c <= 'f':
		return uint16(c-'a') + 10
	default:
		return uint16(c-'A') + 10
	}
}

func isCLIJSONCanonicalUnicodeEscape(code uint16) bool {
	if code <= 0x1f {
		switch code {
		case 0x08, 0x09, 0x0a, 0x0c, 0x0d:
			return false
		default:
			return true
		}
	}
	return code == 0x26 || code == 0x3c || code == 0x3e || code == 0x2028 || code == 0x2029
}

func isCLIJSONHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func scanCLIJSONValue(raw []byte, pos, depth int) (int, bool) {
	if depth > 1000 || pos >= len(raw) {
		return pos, false
	}
	switch raw[pos] {
	case '"':
		return scanCLIJSONString(raw, pos)
	case '{':
		return scanCLIJSONObject(raw, pos, depth+1)
	case '[':
		return scanCLIJSONArray(raw, pos, depth+1)
	case 't':
		return pos + 4, pos+4 <= len(raw) && raw[pos+1] == 'r' && raw[pos+2] == 'u' && raw[pos+3] == 'e'
	case 'f':
		return pos + 5, pos+5 <= len(raw) && raw[pos+1] == 'a' && raw[pos+2] == 'l' && raw[pos+3] == 's' && raw[pos+4] == 'e'
	case 'n':
		return pos + 4, pos+4 <= len(raw) && raw[pos+1] == 'u' && raw[pos+2] == 'l' && raw[pos+3] == 'l'
	default:
		if raw[pos] == '-' || raw[pos] >= '0' && raw[pos] <= '9' {
			return scanCLIJSONNumber(raw, pos)
		}
	}
	return pos, false
}

func scanCLIJSONObject(raw []byte, pos, depth int) (int, bool) {
	pos = skipCLIJSONSpace(raw, pos+1)
	if pos < len(raw) && raw[pos] == '}' {
		return pos + 1, true
	}
	for {
		var ok bool
		pos, ok = scanCLIJSONString(raw, pos)
		if !ok {
			return pos, false
		}
		pos = skipCLIJSONSpace(raw, pos)
		if pos >= len(raw) || raw[pos] != ':' {
			return pos, false
		}
		var end int
		end, ok = scanCLIJSONValue(raw, skipCLIJSONSpace(raw, pos+1), depth)
		if !ok {
			return end, false
		}
		pos = skipCLIJSONSpace(raw, end)
		if pos >= len(raw) {
			return pos, false
		}
		if raw[pos] == '}' {
			return pos + 1, true
		}
		if raw[pos] != ',' {
			return pos, false
		}
		pos = skipCLIJSONSpace(raw, pos+1)
	}
}

func scanCLIJSONArray(raw []byte, pos, depth int) (int, bool) {
	pos = skipCLIJSONSpace(raw, pos+1)
	if pos < len(raw) && raw[pos] == ']' {
		return pos + 1, true
	}
	for {
		end, ok := scanCLIJSONValue(raw, pos, depth)
		if !ok {
			return end, false
		}
		pos = skipCLIJSONSpace(raw, end)
		if pos >= len(raw) {
			return pos, false
		}
		if raw[pos] == ']' {
			return pos + 1, true
		}
		if raw[pos] != ',' {
			return pos, false
		}
		pos = skipCLIJSONSpace(raw, pos+1)
	}
}

func scanCLIJSONNumber(raw []byte, pos int) (int, bool) {
	start := pos
	if raw[pos] == '-' {
		pos++
		if pos >= len(raw) {
			return pos, false
		}
	}
	if raw[pos] == '0' {
		pos++
	} else if raw[pos] >= '1' && raw[pos] <= '9' {
		for pos < len(raw) && raw[pos] >= '0' && raw[pos] <= '9' {
			pos++
		}
	} else {
		return pos, false
	}
	if pos < len(raw) && raw[pos] == '.' {
		pos++
		fractionStart := pos
		for pos < len(raw) && raw[pos] >= '0' && raw[pos] <= '9' {
			pos++
		}
		if fractionStart == pos {
			return pos, false
		}
	}
	if pos < len(raw) && (raw[pos] == 'e' || raw[pos] == 'E') {
		pos++
		if pos < len(raw) && (raw[pos] == '+' || raw[pos] == '-') {
			pos++
		}
		exponentStart := pos
		for pos < len(raw) && raw[pos] >= '0' && raw[pos] <= '9' {
			pos++
		}
		if exponentStart == pos {
			return pos, false
		}
	}
	return pos, pos > start
}

// runCLIEvents consumes exactly one project-scoped stream. eventsOn is the
// normalized result of parseCLIEventsAction; argument validation belongs to
// RunCLIContext. EOF is a clean foreground termination; transport and
// authentication failures remain nonzero and use the same safe recovery text
// as other CLI operations.
func runCLIEvents(ctx context.Context, c *client.Client, out io.Writer, projectID string, eventsOn bool, jsonOutput bool) error {
	if strings.TrimSpace(projectID) == "" {
		return errors.New("no project selected — use -project <name|id> and choose a project with live events")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return nil
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events, errs := c.StreamEvents(streamCtx, projectID)
	var terminalErr error
	for events != nil || errs != nil {
		if ctx.Err() != nil {
			cancel()
			drainCLIEventChannels(events, errs)
			return nil
		}
		select {
		case <-ctx.Done():
			cancel()
			drainCLIEventChannels(events, errs)
			return nil
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			line, include, formatErr := formatCLIEventBytes(ev, projectID, jsonOutput)
			if formatErr != nil {
				cancel()
				drainCLIEventChannels(events, errs)
				return formatErr
			}
			if !include {
				continue
			}
			if _, writeErr := out.Write(line); writeErr != nil {
				cancel()
				drainCLIEventChannels(events, errs)
				return fmt.Errorf("writing live event output: %w", writeErr)
			}
			if _, writeErr := io.WriteString(out, "\n"); writeErr != nil {
				cancel()
				drainCLIEventChannels(events, errs)
				return fmt.Errorf("writing live event output: %w", writeErr)
			}
		case streamErr, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			errs = nil
			if streamErr != nil {
				terminalErr = streamErr
			}
		}
	}

	if ctx.Err() != nil {
		return nil
	}
	if terminalErr == nil || errors.Is(terminalErr, client.ErrEventStreamClosed) {
		return nil
	}
	if client.IsAuthRequired(terminalErr) {
		return errors.New(authRecoveryMessage(c.BaseURL()))
	}
	if client.IsInvalidServerURL(terminalErr) {
		return errors.New(InvalidServerURLMessage(c.BaseURL()))
	}
	if client.IsTransportError(terminalErr) {
		return errors.New(OfflineRecoveryMessage(c.BaseURL(), terminalErr))
	}
	return fmt.Errorf("live events: %w", terminalErr)
}

func parseCLIEventsAction(args []string) (bool, error) {
	if len(args) > 1 {
		return false, errors.New("usage: events [on|off]")
	}
	if len(args) == 0 {
		return true, nil
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "on", "true":
		return true, nil
	case "off", "false":
		return false, nil
	default:
		return false, errors.New("usage: events [on|off]")
	}
}

func drainCLIEventChannels(events <-chan client.Event, errs <-chan error) {
	for events != nil || errs != nil {
		select {
		case _, ok := <-events:
			if !ok {
				events = nil
			}
		case _, ok := <-errs:
			if !ok {
				errs = nil
			}
		}
	}
}

func formatCLIEvent(ev client.Event, projectID string, jsonOutput bool) (string, bool, error) {
	encoded, include, err := formatCLIEventBytes(ev, projectID, jsonOutput)
	if err != nil {
		return "", false, err
	}
	return string(encoded), include, nil
}

func formatCLIEventBytes(ev client.Event, projectID string, jsonOutput bool) ([]byte, bool, error) {
	var payload cliEventPayload
	var rawPayload cliEventRawPayload
	var payloadErr error
	fastPayload := false
	if jsonOutput && len(ev.Data) > 4096 {
		var parsed bool
		payload, rawPayload, parsed = scanCLIEventPayload(ev.Data)
		fastPayload = parsed
		if !parsed {
			payload = cliEventPayload{}
			rawPayload = cliEventRawPayload{}
			payloadErr = json.Unmarshal(ev.Data, &payload)
		}
	} else {
		payloadErr = json.Unmarshal(ev.Data, &payload)
	}
	payload.ProjectID = strings.TrimSpace(payload.ProjectID)
	if strings.TrimSpace(payload.TaskID) != "" && payload.ProjectID == "" {
		return nil, false, nil
	}
	if payload.ProjectID != "" && payload.ProjectID != projectID {
		return nil, false, nil
	}
	if payload.ProjectID == "" {
		payload.ProjectID = projectID
	}

	record := cliEventRecord{
		Event:           strings.TrimSpace(ev.Name),
		Type:            strings.TrimSpace(payload.Type),
		ProjectID:       payload.ProjectID,
		TaskID:          payload.TaskID,
		TaskName:        payload.TaskName,
		Status:          payload.Status,
		Category:        payload.Category,
		Message:         payload.Message,
		ExecID:          payload.ExecID,
		Source:          payload.Source,
		AgentName:       payload.AgentName,
		CompletedOutput: payload.CompletedOutput,
		Queued:          payload.Queued,
	}
	if record.Event == "" {
		record.Event = record.Type
	}
	if record.Type == "" {
		record.Type = record.Event
	}
	if jsonOutput {
		if fastPayload && payloadErr == nil {
			emptyString := json.RawMessage{'"', '"'}
			message := rawPayload.Message
			if len(message) == 0 {
				message = emptyString
			}
			completedOutput := rawPayload.CompletedOutput
			if len(completedOutput) == 0 {
				completedOutput = emptyString
			}
			jsonRecord := cliEventJSONRecord{
				Event:           record.Event,
				Type:            record.Type,
				ProjectID:       record.ProjectID,
				TaskID:          record.TaskID,
				TaskName:        record.TaskName,
				Status:          record.Status,
				Category:        record.Category,
				Message:         message,
				ExecID:          record.ExecID,
				Source:          record.Source,
				AgentName:       record.AgentName,
				CompletedOutput: completedOutput,
				Queued:          record.Queued,
			}
			if len(message) <= 4096 && len(completedOutput) <= 4096 {
				encoded, err := formatCLIEventJSONRecord(jsonRecord, ev.Data)
				if err != nil {
					return nil, false, fmt.Errorf("encoding live event: %w", err)
				}
				return encoded, true, nil
			}
			jsonRecord.Data = ev.Data
			encoded, err := json.Marshal(jsonRecord)
			if err != nil {
				return nil, false, fmt.Errorf("encoding live event: %w", err)
			}
			return encoded, true, nil
		}
		if payloadErr == nil || json.Valid(ev.Data) {
			record.Data = ev.Data
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			return nil, false, fmt.Errorf("encoding live event: %w", err)
		}
		return encoded, true, nil
	}

	parts := []string{"event=" + strconv.Quote(record.Event)}
	add := func(key, value string) {
		if value != "" {
			parts = append(parts, key+"="+strconv.Quote(value))
		}
	}
	add("type", record.Type)
	add("project_id", record.ProjectID)
	add("task_id", record.TaskID)
	add("task_name", record.TaskName)
	add("status", record.Status)
	add("category", record.Category)
	add("message", record.Message)
	add("exec_id", record.ExecID)
	add("source", record.Source)
	add("agent_name", record.AgentName)
	if record.Queued {
		parts = append(parts, "queued=true")
	}
	add("completed_output", record.CompletedOutput)
	if len(parts) == 1 || (len(parts) == 2 && record.Type == record.Event) {
		if compact := compactJSON(ev.Data); compact != nil {
			parts = append(parts, "data="+strconv.Quote(string(compact)))
		} else if payloadErr != nil && strings.TrimSpace(string(ev.Data)) != "" {
			parts = append(parts, "data="+strconv.Quote(string(ev.Data)))
		}
	}
	return []byte(strings.Join(parts, " ")), true, nil
}

func formatCLIEventJSONRecord(record cliEventJSONRecord, raw []byte) ([]byte, error) {
	record.Data = nil
	metadata, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	encoded := make([]byte, 0, len(metadata)+len(raw)+len(`,"data":`))
	encoded = append(encoded, metadata[:len(metadata)-1]...)
	encoded = append(encoded, `,"data":`...)
	if cliJSONNeedsNormalization(raw) {
		encoded = appendCompactCLIJSON(encoded, raw)
	} else {
		encoded = append(encoded, raw...)
	}
	return append(encoded, '}'), nil
}

func cliJSONNeedsNormalization(raw []byte) bool {
	return bytes.IndexByte(raw, 0x20) >= 0 ||
		bytes.IndexByte(raw, 0x09) >= 0 ||
		bytes.IndexByte(raw, 0x0a) >= 0 ||
		bytes.IndexByte(raw, 0x0d) >= 0 ||
		bytes.IndexByte(raw, '<') >= 0 ||
		bytes.IndexByte(raw, '>') >= 0 ||
		bytes.IndexByte(raw, '&') >= 0 ||
		bytes.IndexByte(raw, 0xe2) >= 0
}

// appendCompactCLIJSON matches encoding/json's RawMessage embedding behavior
// for the valid JSON values passed by formatCLIEventBytes.
func appendCompactCLIJSON(dst, raw []byte) []byte {
	inString := false
	escaped := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if inString {
			switch {
			case escaped:
				dst = append(dst, c)
				escaped = false
			case c == 0x5c:
				dst = append(dst, c)
				escaped = true
			case c == '"':
				dst = append(dst, c)
				inString = false
			case c == '<':
				dst = append(dst, '\\', 'u', '0', '0', '3', 'c')
			case c == '>':
				dst = append(dst, '\\', 'u', '0', '0', '3', 'e')
			case c == '&':
				dst = append(dst, '\\', 'u', '0', '0', '2', '6')
			case c == 0xe2 && i+2 < len(raw) && raw[i+1] == 0x80 && raw[i+2]&^1 == 0xa8:
				if raw[i+2] == 0xa8 {
					dst = append(dst, '\\', 'u', '2', '0', '2', '8')
				} else {
					dst = append(dst, '\\', 'u', '2', '0', '2', '9')
				}
				i += 2
			default:
				dst = append(dst, c)
			}
			continue
		}

		switch c {
		case 0x20, 0x09, 0x0a, 0x0d:
			continue
		case '"':
			inString = true
		}
		dst = append(dst, c)
	}
	return dst
}

func compactJSON(raw []byte) json.RawMessage {
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil
	}
	return json.RawMessage(compact.Bytes())
}

// CommandSummary lists every command as "name  actions  description" rows for
// the -h/--help output, so the flag usage isn't the only thing shown there.
func CommandSummary() string {
	prev := cmdPrefix
	cmdPrefix = ""
	defer func() { cmdPrefix = prev }()

	width := 0
	for _, c := range commands {
		if c.hidden {
			continue
		}
		if n := len(c.name); n > width {
			width = n
		}
	}
	var b strings.Builder
	for _, c := range commands {
		if c.hidden {
			continue
		}
		fmt.Fprintf(&b, "  %-*s  %s\n", width, c.name, c.desc)
	}
	return b.String()
}

// CLIProjectSelectionHint is the static help text for headless project scope.
func CLIProjectSelectionHint() string { return cliProjectSelectionHint }

const cliProjectSelectionHint = "Project-scoped CLI commands use the only backend project automatically; when multiple projects exist, pass -project <name|id>. Global commands such as projects list/create/show/edit and help do not require a separate -project reference."

// cliProjectScoped reports whether this invocation can read or mutate a
// project-scoped endpoint. Global actions remain usable without selecting a
// project, even though some of their commands also load the project list.
func (c command) cliProjectScoped(args []string) bool {
	switch c.name {
	case "help", "quit", "clear", "login", "setup", "project", "projects", "status":
		return false
	case "chat":
		return len(args) > 0
	case "agents":
		action, _ := splitAction(c.actions, args)
		return action != "metrics"
	case "models":
		action, _ := splitAction(c.actions, args)
		return action == "capacity" || action == "edit" || action == "default" || action == "delete"
	case "workers":
		action, _ := splitAction(c.actions, args)
		return action == "project"
	default:
		return true
	}
}

// cliProjectPreflight runs after project loading and before command dispatch so
// no project-scoped endpoint can inherit a backend-selected first project.
func cliProjectPreflight(c command, args []string, projectRef string, m Model) error {
	if !c.cliProjectScoped(args) {
		return nil
	}
	if strings.TrimSpace(projectRef) == "" && len(m.projects) > 1 {
		return fmt.Errorf("multiple projects found; choose one with -project <name|id> before running %s", c.name)
	}
	if m.selectedID == "" {
		if c.name == "models" {
			return errors.New("no project selected — models capacity, edit, default, and delete require -project <name|id>; create one first with projects create <name> <path>")
		}
		return errors.New("no project selected — use /project <name>")
	}
	return nil
}

func cliImplicitProject(c command, args []string, projectRef string, m Model) (client.Project, bool) {
	if !c.cliProjectScoped(args) || strings.TrimSpace(projectRef) != "" || len(m.projects) != 1 || m.selectedID == "" {
		return client.Project{}, false
	}
	project := m.projects[0]
	if project.ID == "" {
		project.ID = m.selectedID
	}
	if project.Name == "" {
		project.Name = m.selectedName
	}
	return project, true
}

// needsBackend reports whether the command requires a selected project.
func (c command) needsBackend() bool {
	switch c.name {
	case "help", "quit", "clear", "login", "setup":
		return false
	}
	return true
}

// needsProjectLoad reports whether CLI startup should resolve a selected
// project before running the command. Project creation is intentionally
// independent of the existing project list, so first-run creation works even
// when the backend has no projects yet.
func (c command) needsProjectLoad(args []string) bool {
	if c.name == "projects" && len(args) > 1 && strings.EqualFold(args[1], "create") {
		return false
	}
	if c.name == "models" && len(args) > 0 && !c.cliProjectScoped(args[1:]) {
		return false
	}
	if c.name == "workers" {
		action, _ := splitAction(c.actions, args[1:])
		if action == "" || action == "show" || action == "limit" {
			return false
		}
	}
	return c.needsBackend()
}

// needsStatus reports whether the command renders connection state.
func (c command) needsStatus() bool { return c.name == "status" }

// drain runs cmd and feeds every resulting message back into the model until
// the command settles (nil message, quit, or the deadline elapses).
func drain(m Model, cmd tea.Cmd) Model {
	deadline := time.Now().Add(cliDeadline)
	queue := []tea.Cmd{cmd}

	for len(queue) > 0 && time.Now().Before(deadline) {
		cur := queue[0]
		queue = queue[1:]
		if cur == nil {
			continue
		}
		msg := cur()
		if msg == nil {
			continue
		}
		switch typed := msg.(type) {
		case tea.BatchMsg:
			queue = append(queue, typed...)
			continue
		case tea.QuitMsg:
			return m
		}
		next, follow := m.Update(msg)
		m = next.(Model)
		if follow != nil {
			queue = append(queue, follow)
		}
	}
	return m
}

// writeEntries prints transcript entries as plain blocks.
func writeEntries(out io.Writer, entries []entry) {
	for _, e := range entries {
		switch e.role {
		case "error":
			continue // reported through the exit status instead
		case "result":
			if e.head != "" {
				fmt.Fprintln(out, e.head)
			}
		case "agent", "you":
			// no prefix: the output is the answer
		}
		text := strings.TrimRight(e.text, "\n")
		if text != "" {
			fmt.Fprintln(out, text)
		}
	}
}

// normalizedJSONEntryTexts returns transcript text eligible for either JSON
// output path, preserving entry order and omitting housekeeping or empty
// entries.
func normalizedJSONEntryTexts(entries []entry) []string {
	texts := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.role == "error" || e.role == "system" {
			continue // errors via exit status; system messages are housekeeping noise
		}
		text := strings.TrimRight(e.text, "\n")
		if text == "" {
			continue
		}
		texts = append(texts, text)
	}
	return texts
}

// writeJSONEntries prints result entries as raw text, omitting styled headers.
// This is used in --json mode so the caller receives the bare JSON payload.
func writeJSONEntries(out io.Writer, entries []entry) {
	for _, text := range normalizedJSONEntryTexts(entries) {
		fmt.Fprintln(out, text)
	}
}

// cliScopedJSONOutput keeps implicit project scope alongside the command result
// without changing the JSON shape of commands that already received an explicit
// project reference.
type cliScopedJSONOutput struct {
	ProjectID   string          `json:"project_id"`
	ProjectName string          `json:"project_name"`
	Data        json.RawMessage `json:"data"`
}

func writeScopedEntries(out io.Writer, entries []entry, project client.Project) {
	name := strings.TrimSpace(project.Name)
	id := strings.TrimSpace(project.ID)
	if name == "" {
		name = id
	}
	if id == "" {
		fmt.Fprintf(out, "project: %s\n", name)
	} else {
		fmt.Fprintf(out, "project: %s (project_id=%s)\n", name, id)
	}
	writeEntries(out, entries)
}

func writeScopedJSONEntries(out io.Writer, entries []entry, project client.Project) {
	texts := normalizedJSONEntryTexts(entries)
	values := make([]json.RawMessage, 0, len(texts))
	for _, text := range texts {
		if json.Valid([]byte(strings.TrimSpace(text))) {
			values = append(values, json.RawMessage(strings.TrimSpace(text)))
			continue
		}
		encoded, _ := json.Marshal(text)
		values = append(values, encoded)
	}
	if len(values) == 0 {
		return
	}

	data := values[0]
	if len(values) > 1 {
		data, _ = json.Marshal(values)
	}
	encoded, err := json.Marshal(cliScopedJSONOutput{
		ProjectID:   strings.TrimSpace(project.ID),
		ProjectName: strings.TrimSpace(project.Name),
		Data:        data,
	})
	if err == nil {
		fmt.Fprintln(out, string(encoded))
	}
}

// firstError returns the first error logged during the run.
func firstError(m Model) error {
	for _, e := range m.log {
		if e.role == "error" {
			return errors.New(e.text)
		}
	}
	return nil
}
