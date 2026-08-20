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
	"errors"
	"fmt"
	"io"
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

// jsonMode is true when the CLI --json flag was provided. List and show
// commands emit machine-readable JSON to stdout instead of styled text.
var jsonMode bool

// RunCLI executes one command without starting the interactive UI and writes
// its output to out. It returns an error when the command reported one.
// force corresponds to the --force CLI flag: when true destructive commands
// skip their guard and execute immediately.
// json corresponds to the --json CLI flag: when true list/show commands emit
// raw JSON instead of styled text.
func RunCLI(c *client.Client, out io.Writer, projectRef string, args []string, force bool, json bool) error {
	if len(args) == 0 {
		return errors.New("no command given")
	}
	// Activate CLI mode so destructive commands apply --force gating instead
	// of a TUI confirmation prompt.
	cliMode = true
	forceMode = force
	jsonMode = json
	defer func() {
		cliMode = false
		forceMode = false
		jsonMode = false
	}()
	// In CLI mode commands are shell subcommands, so help should print them
	// without the chat window's leading slash.
	cmdPrefix = ""
	line := strings.Join(args, " ")
	if !strings.HasPrefix(line, "/") {
		line = "/" + line
	}
	name := strings.TrimPrefix(strings.Fields(line)[0], "/")

	cmdDef := lookupCommand(name)
	if cmdDef == nil {
		return fmt.Errorf("unknown command %q — run \"help\" to list commands", name)
	}

	m := New(c)
	m.width, m.height = 100, 40
	m.transcript.Width = m.width
	m.log = nil // drop the interactive banner

	// Only commands that talk to the backend need a project or connection
	// state; /help and friends should stay instant and work offline.
	if cmdDef.needsBackend() {
		m = drain(m, m.loadProjects(false, projectRef))
		// An unknown or ambiguous project must fail loudly rather than run the
		// command against whichever project happened to be selected.
		if err := firstError(m); err != nil {
			if m.connErr != "" {
				return errors.New(offlineRecoveryMessage(c.BaseURL(), errors.New(m.connErr)))
			}
			return err
		}
	}
	if cmdDef.needsStatus() {
		m = drain(m, m.checkConnection())
		m = drain(m, m.fetchStatusCounts())
	}

	start := len(m.log)
	next, cmd := m.runCommand(line)
	m = next.(Model)
	m = drain(m, cmd)

	if jsonMode {
		writeJSONEntries(out, m.log[start:])
	} else {
		writeEntries(out, m.log[start:])
	}
	return firstError(m)
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

// needsBackend reports whether the command requires a selected project.
func (c command) needsBackend() bool {
	switch c.name {
	case "help", "quit", "clear":
		return false
	}
	return true
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

// writeJSONEntries prints result entries as raw text, omitting styled headers.
// This is used in --json mode so the caller receives the bare JSON payload.
func writeJSONEntries(out io.Writer, entries []entry) {
	for _, e := range entries {
		if e.role == "error" || e.role == "system" {
			continue // errors via exit status; system messages are housekeeping noise
		}
		text := strings.TrimRight(e.text, "\n")
		if text != "" {
			fmt.Fprintln(out, text)
		}
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
