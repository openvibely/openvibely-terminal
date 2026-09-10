// The openvibely-terminal command is a terminal UI and CLI client for the
// OpenVibely backend.
//
// It connects over HTTP/SSE to a running openvibely server (cmd/server or
// cmd/desktop in the openvibely repo).
//
// With no arguments it starts the interactive UI: one chat window where plain
// text talks to the project agent and a line starting with "/" runs a command
// (/tasks, /alerts, /skills, /models, /agents, /schedule, /workers, /channels,
// /personality, /pulse, /reflection, /grades, /insights, /analytics, …).
//
// With arguments it runs that same command once and prints the result, so
// every TUI command doubles as a CLI subcommand:
//
//	openvibely-terminal tasks
//	openvibely-terminal tasks run "api refactor"
//	openvibely-terminal -project demo chat "ship the docs"
//	openvibely-terminal projects create demo /Users/me/src/demo
//	openvibely-terminal -project demo events on
//	openvibely-terminal help
//
// Configuration (flags override environment variables):
//
//	-server  / OPENVIBELY_SERVER_URL      backend base URL (default http://localhost:3001)
//	-user    / OPENVIBELY_AUTH_USERNAME   username when server auth is enabled
//	-pass    / OPENVIBELY_AUTH_PASSWORD   password when server auth is enabled
//	-project / OPENVIBELY_PROJECT         project to select (name, ID or unique prefix)
//
// When an interactive server requires authentication and no credentials were
// configured, use /login in the TUI. Login credentials are kept in the
// in-terminal form and the resulting cookie session is reused for retries.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-terminal/internal/client"
	"github.com/openvibely/openvibely-terminal/internal/terminal"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	serverURL := flag.String("server", envOr("OPENVIBELY_SERVER_URL", "http://localhost:3001"), "OpenVibely server base URL")
	username := flag.String("user", os.Getenv("OPENVIBELY_AUTH_USERNAME"), "username (only needed when server auth is enabled)")
	password := flag.String("pass", "", "password (only needed when server auth is enabled)")
	project := flag.String("project", os.Getenv("OPENVIBELY_PROJECT"), "project to select: name, ID or unique prefix")
	force := flag.Bool("force", false, "skip confirmation prompt for destructive CLI commands (delete, clear)")
	flag.BoolVar(force, "f", false, "shorthand for -force")
	json := flag.Bool("json", false, "emit machine-readable JSON output for supported list, show, lifecycle, workflow-vote, project creation, and events commands")
	flag.Usage = usage
	if err := parseInterspersedFlags(flag.CommandLine, os.Args[1:]); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}

	configuredPassword := *password
	passwordFlagSet := false
	flag.CommandLine.Visit(func(f *flag.Flag) {
		if f.Name == "pass" {
			passwordFlagSet = true
		}
	})
	if !passwordFlagSet {
		configuredPassword = os.Getenv("OPENVIBELY_AUTH_PASSWORD")
	}

	c, err := client.New(*serverURL)
	if err != nil {
		return err
	}

	args := flag.Args()
	// Static help and read-only setup must remain available even when configured
	// credentials are present and the backend is unreachable.
	if isStaticHelpCommand(args) {
		return terminal.RunCLI(c, os.Stdout, *project, args, *force, *json)
	}

	// If credentials were provided, establish a cookie session up front. An
	// interactive run without them can use /login after a reachable auth failure.
	if err := loginWithConfiguredCredentials(c, *username, configuredPassword); err != nil {
		return err
	}

	// Arguments after the flags mean "run this one command and exit".
	if len(args) > 0 {
		return runCLI(c, *project, args, *force, *json)
	}

	return runTUI(c, *project)
}

type boolFlag interface {
	IsBoolFlag() bool
}

// parseInterspersedFlags lets registered global flags appear anywhere before
// an explicit -- boundary. It preserves every other token in its original
// order so subcommands remain responsible for their own flags and operands.
func parseInterspersedFlags(fs *flag.FlagSet, args []string) error {
	flags := make([]string, 0, len(args))
	operands := make([]string, 0, len(args))
	seenOperand := false

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			operands = append(operands, args[i+1:]...)
			break
		}

		name, hasValue, isFlag := flagToken(arg)
		registered := fs.Lookup(name)
		isHelp := name == "h" || name == "help"
		if !isFlag || (registered == nil && !isHelp && seenOperand) {
			operands = append(operands, arg)
			seenOperand = true
			continue
		}

		flags = append(flags, arg)
		if registered == nil || hasValue {
			continue
		}
		if bf, ok := registered.Value.(boolFlag); ok && bf.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}

	return fs.Parse(append(flags, operands...))
}

func flagToken(arg string) (name string, hasValue, ok bool) {
	if len(arg) < 2 || arg[0] != '-' || arg == "-" {
		return "", false, false
	}
	name = arg[1:]
	if strings.HasPrefix(name, "-") {
		name = name[1:]
	}
	if name == "" {
		return "", false, false
	}
	if before, _, found := strings.Cut(name, "="); found {
		return before, true, true
	}
	return name, false, true
}

func runCLI(c *client.Client, project string, args []string, force, jsonOutput bool) error {
	if !isForegroundCLICommand(args) {
		// Preserve the original CLI behavior for short-lived commands: Ctrl-C keeps
		// its normal process-interrupt semantics instead of being consumed by a
		// context that those commands do not use.
		return terminal.RunCLIWithInput(c, os.Stdout, cliCredentialInput(), project, args, force, jsonOutput)
	}

	// Live events, chat, and task follow-ups own long-lived streaming requests.
	// Let Ctrl-C cancel them so active SSE response bodies close promptly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return terminal.RunCLIContext(ctx, c, os.Stdout, project, args, force, jsonOutput)
}

func cliCredentialInput() *os.File {
	info, err := os.Stdin.Stat()
	if err != nil || !canUseCLISecretInput(info.Mode()) {
		return nil
	}
	return os.Stdin
}

func canUseCLISecretInput(mode os.FileMode) bool {
	return mode&os.ModeCharDevice == 0
}

func isForegroundCLICommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	name := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(args[0]), "/"))
	switch name {
	case "events", "stream", "log":
		return true
	case "chat", "back", "leave":
		return len(args) > 1
	case "tasks", "task":
		return len(args) > 1 && strings.EqualFold(strings.TrimSpace(args[1]), "reply")
	default:
		return false
	}
}

func isStaticHelpCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	name := strings.TrimPrefix(strings.TrimSpace(args[0]), "/")
	switch strings.ToLower(name) {
	case "help", "?", "commands", "setup":
		return true
	default:
		return false
	}
}

type configuredLoginTransportError struct {
	message string
	cause   error
}

func (e *configuredLoginTransportError) Error() string { return e.message }
func (e *configuredLoginTransportError) Unwrap() error { return e.cause }

func loginWithConfiguredCredentials(c *client.Client, username, password string) error {
	if username == "" && password == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := c.Login(ctx, username, password); err != nil {
		if client.IsLoginTransportError(err) {
			return &configuredLoginTransportError{
				message: terminal.OfflineRecoveryMessage(c.BaseURL(), err),
				cause:   err,
			}
		}
		return fmt.Errorf("authenticating with %s: %w", terminal.ServerURLDisplay(c.BaseURL()), err)
	}
	return nil
}

func runTUI(c *client.Client, project string) error {
	model := terminal.New(c).WithProject(project)
	p := tea.NewProgram(model, tea.WithAltScreen())
	finalModel, err := p.Run()
	if m, ok := finalModel.(terminal.Model); ok {
		m.Cleanup()
	}
	if err != nil {
		return fmt.Errorf("running TUI: %w", err)
	}
	return nil
}

func usage() {
	out := flag.CommandLine.Output()
	fmt.Fprintf(out, `openvibely-terminal — terminal UI and CLI for OpenVibely

usage:
  openvibely-terminal [flags]                 start the interactive chat UI
  openvibely-terminal [flags] <command> [flags] ...   run one command and print the result

commands:
%s
project selection:
  %s
examples:
  openvibely-terminal
  openvibely-terminal tasks
  openvibely-terminal tasks run "api refactor"
  openvibely-terminal -project demo chat "ship the docs"
  openvibely-terminal chat -project demo "ship the docs"
  openvibely-terminal -project demo events on
  openvibely-terminal help tasks              full syntax of one command

flags:
`, terminal.CommandSummary(), terminal.CLIProjectSelectionHint())
	flag.PrintDefaults()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
