package tui

// Non-interactive (CLI) mode.
//
// The same slash-command registry that drives the chat window also works
// headlessly, so every command available in the TUI is available as a
// subcommand:
//
//	openvibely-tui tasks
//	openvibely-tui tasks run refactor
//	openvibely-tui -project demo chat "ship the docs"
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

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-tui/internal/client"
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
	return RunCLIContext(context.Background(), c, out, projectRef, args, force, json)
}

// RunCLIContext is RunCLI with a caller-owned lifetime. Long-running commands
// such as the foreground events stream use this context for cancellation.
func RunCLIContext(ctx context.Context, c *client.Client, out io.Writer, projectRef string, args []string, force bool, json bool) error {
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

	m := New(c)
	m.cliContext = ctx
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

	// Only commands that talk to the backend need a project or connection
	// state; /help and friends should stay instant and work offline.
	if cmdDef.needsProjectLoad(args) {
		var projectLoad tea.Cmd
		m, projectLoad = m.beginProjectLoad(false, projectRef)
		m = drain(m, projectLoad)
		if ctx.Err() != nil {
			return nil
		}
		// An unknown or ambiguous project must fail loudly rather than run the
		// command against whichever project happened to be selected.
		if err := firstError(m); err != nil {
			if m.connErr != "" {
				if m.connReachableError {
					return errors.New(ReachableBackendErrorMessage(c.BaseURL(), errors.New(m.connErr)))
				}
				return errors.New(OfflineRecoveryMessage(c.BaseURL(), errors.New(m.connErr)))
			}
			return err
		}
	}

	if err := cliProjectPreflight(*cmdDef, fields[1:], projectRef, m); err != nil {
		return err
	}
	implicitProject, hasImplicitProject := cliImplicitProject(*cmdDef, fields[1:], projectRef, m)

	// A one-shot events command owns its stream directly. The interactive model
	// stream is intentionally not started during CLI project preloading, and
	// feeding a long-lived command through drain would delay line output until
	// the stream ended.
	if cmdDef.name == "events" {
		return runCLIEvents(ctx, c, out, m.selectedID, eventsOn, json)
	}

	if cmdDef.needsStatus() {
		m = drain(m, m.checkConnection())
		m = drain(m, m.fetchStatusCounts())
	}

	start := len(m.log)
	next, cmd := m.runCommandFields(fields)
	m = next.(Model)
	m = drain(m, cmd)

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
	return commandErr
}

const cliEventsOffMessage = "events off cannot disable a foreground stream owned by another process; press Ctrl-C in that monitoring process (interactive /events off only hides events in that TUI)"

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
			line, include, formatErr := formatCLIEvent(ev, projectID, jsonOutput)
			if formatErr != nil {
				cancel()
				drainCLIEventChannels(events, errs)
				return formatErr
			}
			if !include {
				continue
			}
			if _, writeErr := fmt.Fprintln(out, line); writeErr != nil {
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
	var payload cliEventPayload
	payloadErr := json.Unmarshal(ev.Data, &payload)
	payload.ProjectID = strings.TrimSpace(payload.ProjectID)
	if payload.ProjectID != "" && payload.ProjectID != projectID {
		return "", false, nil
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
	if compact := compactJSON(ev.Data); compact != nil {
		record.Data = compact
	}

	if jsonOutput {
		encoded, err := json.Marshal(record)
		if err != nil {
			return "", false, fmt.Errorf("encoding live event: %w", err)
		}
		return string(encoded), true, nil
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
		if record.Data != nil {
			parts = append(parts, "data="+strconv.Quote(string(record.Data)))
		} else if payloadErr != nil && strings.TrimSpace(string(ev.Data)) != "" {
			parts = append(parts, "data="+strconv.Quote(string(ev.Data)))
		}
	}
	return strings.Join(parts, " "), true, nil
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
		if n := len(c.name); n > width {
			width = n
		}
	}
	var b strings.Builder
	for _, c := range commands {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, c.name, c.desc)
	}
	return b.String()
}

// CLIProjectSelectionHint is the static help text for headless project scope.
func CLIProjectSelectionHint() string { return cliProjectSelectionHint }

const cliProjectSelectionHint = "Project-scoped CLI commands use the only backend project automatically; when multiple projects exist, pass -project <name|id>. Global commands such as projects list/create and help do not require a project reference."

// cliProjectScoped reports whether this invocation can read or mutate a
// project-scoped endpoint. Global actions remain usable without selecting a
// project, even though some of their commands also load the project list.
func (c command) cliProjectScoped(args []string) bool {
	switch c.name {
	case "help", "quit", "clear", "login", "project", "projects", "status":
		return false
	case "chat":
		return len(args) > 0
	case "agents":
		action, _ := splitAction(c.actions, args)
		return action != "metrics"
	case "models":
		action, _ := splitAction(c.actions, args)
		return action == "capacity" || action == "default" || action == "delete"
	case "workers":
		action, _ := splitAction(c.actions, args)
		return action != "limit"
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
			return errors.New("no project selected — models capacity, default, and delete require -project <name|id>; create one first with projects create <name> <path>")
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
	case "help", "quit", "clear", "login":
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
