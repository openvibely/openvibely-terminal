// Command tui is a terminal UI and CLI client for the OpenVibely backend.
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
//	openvibely-tui tasks
//	openvibely-tui tasks run "api refactor"
//	openvibely-tui -project demo chat "ship the docs"
//	openvibely-tui projects create demo /Users/me/src/demo
//	openvibely-tui help
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
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-tui/internal/client"
	"github.com/openvibely/openvibely-tui/internal/tui"
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
	json := flag.Bool("json", false, "emit machine-readable JSON output for supported list, show, lifecycle, and project creation commands")
	flag.Usage = usage
	if err := flag.CommandLine.Parse(os.Args[1:]); err != nil {
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
	// Static help must remain available even when configured credentials are
	// present and the backend is unreachable.
	if isStaticHelpCommand(args) {
		return tui.RunCLI(c, os.Stdout, *project, args, *force, *json)
	}

	// If credentials were provided, establish a cookie session up front. An
	// interactive run without them can use /login after a reachable auth failure.
	if err := loginWithConfiguredCredentials(c, *username, configuredPassword); err != nil {
		return err
	}

	// Arguments after the flags mean "run this one command and exit".
	if len(args) > 0 {
		return tui.RunCLI(c, os.Stdout, *project, args, *force, *json)
	}

	return runTUI(c, *project)
}

func isStaticHelpCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	name := strings.TrimPrefix(strings.TrimSpace(args[0]), "/")
	switch strings.ToLower(name) {
	case "help", "?", "commands":
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
				message: tui.OfflineRecoveryMessage(c.BaseURL(), err),
				cause:   err,
			}
		}
		return fmt.Errorf("authenticating with %s: %w", c.BaseURL(), err)
	}
	return nil
}

func runTUI(c *client.Client, project string) error {
	model := tui.New(c).WithProject(project)
	p := tea.NewProgram(model, tea.WithAltScreen())
	finalModel, err := p.Run()
	if m, ok := finalModel.(tui.Model); ok {
		m.Cleanup()
	}
	if err != nil {
		return fmt.Errorf("running TUI: %w", err)
	}
	return nil
}

func usage() {
	out := flag.CommandLine.Output()
	fmt.Fprintf(out, `openvibely-tui — terminal UI and CLI for OpenVibely

usage:
  openvibely-tui [flags]                 start the interactive chat UI
  openvibely-tui [flags] <command> ...   run one command and print the result

commands:
%s
examples:
  openvibely-tui
  openvibely-tui tasks
  openvibely-tui tasks run "api refactor"
  openvibely-tui -project demo chat "ship the docs"
  openvibely-tui help tasks              full syntax of one command

flags:
`, tui.CommandSummary())
	flag.PrintDefaults()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
