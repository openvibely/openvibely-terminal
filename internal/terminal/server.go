package terminal

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

const serverSwitchTimeout = 15 * time.Second

const serverUsage = "usage: /server [<url> [--login]]"

// serverSwitchMsg carries the result of probing a candidate backend. The
// candidate client is installed only when the probe proves it usable.
type serverSwitchMsg struct {
	requestID uint64
	candidate *client.Client
	login     bool
	capacity  *client.GlobalCapacity
	auth      *client.AuthStatus
	projects  []client.Project
	err       error
}

func serverCommand() command {
	return command{
		name:    "server",
		aliases: []string{"connect"},
		args:    "[url] [--login]",
		desc:    "show the backend URL, or switch to another server without restarting",
		usage: []string{
			"server                                     show the current server and connection state",
			"server <url>                               check <url>, then switch to it and reload projects",
			"server <url> --login                       switch to a server that requires sign-in and open /login",
		},
		examples: []string{
			"server http://localhost:3001",
			"server https://openvibely.example.com",
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			m.busy = false
			if len(args) == 0 {
				m.append(entry{role: "result", head: "Server", text: m.renderServer()})
				return m, nil
			}
			if cliMode {
				return m, errCmd("switching servers is interactive; use -server <url> or OPENVIBELY_SERVER_URL for CLI runs")
			}
			rawURL, login, err := parseServerArgs(args)
			if err != nil {
				return m, errCmd(err.Error())
			}
			candidate, err := client.New(rawURL)
			if err != nil || !client.IsValidServerURL(candidate.BaseURL()) {
				m.append(entry{role: "error", text: invalidServerSwitchMessage(m.client.BaseURL())})
				return m, nil
			}
			if candidate.BaseURL() == m.client.BaseURL() && m.connected && !m.authRequired && !login {
				m.append(entry{role: "system", text: "already connected to " + serverURLDisplay(candidate.BaseURL())})
				return m, nil
			}
			m.serverSwitchRequestID++
			m.busy = true
			m.append(entry{role: "system", text: "checking " + serverURLDisplay(candidate.BaseURL()) + "…"})
			return m, probeServer(m.serverSwitchRequestID, candidate, login)
		},
	}
}

func parseServerArgs(args []string) (string, bool, error) {
	var rawURL string
	login := false
	for _, arg := range args {
		switch strings.ToLower(arg) {
		case "--login", "-login":
			if login {
				return "", false, fmt.Errorf("%s", serverUsage)
			}
			login = true
		default:
			if rawURL != "" {
				return "", false, fmt.Errorf("%s", serverUsage)
			}
			rawURL = arg
		}
	}
	if rawURL == "" {
		return "", false, fmt.Errorf("%s", serverUsage)
	}
	return rawURL, login, nil
}

func probeServer(requestID uint64, c *client.Client, login bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), serverSwitchTimeout)
		defer cancel()
		msg := serverSwitchMsg{requestID: requestID, candidate: c, login: login}

		var capErr, authErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); msg.capacity, capErr = c.GetGlobalCapacity(ctx) }()
		go func() { defer wg.Done(); msg.auth, authErr = c.AuthMe(ctx) }()
		wg.Wait()
		switch {
		case client.IsAuthRequired(capErr):
			msg.err = capErr
		case client.IsAuthRequired(authErr):
			msg.err = authErr
		case capErr != nil:
			msg.err = capErr
		}
		if msg.err != nil {
			return msg
		}
		msg.projects, msg.err = c.ListProjects(ctx)
		return msg
	}
}

func (m Model) handleServerSwitch(msg serverSwitchMsg) (Model, tea.Cmd) {
	if msg.requestID != m.serverSwitchRequestID || m.loginActive {
		return m, nil
	}
	m.busy = false
	current := serverURLDisplay(m.client.BaseURL())
	target := serverURLDisplay(msg.candidate.BaseURL())
	keep := "Still using " + current + "."
	switch {
	case msg.err == nil:
	case client.IsAuthRequired(msg.err) && msg.login:
		m = m.installServer(msg.candidate)
		m.log = nil
		m.refreshTranscript()
		m.append(entry{role: "system", text: "Switched to " + target + "; sign-in is required."})
		m.markAuthRequiredQuiet()
		return m.beginLogin()
	case client.IsAuthRequired(msg.err):
		m.append(entry{role: "error", text: target + " requires sign-in. " + keep +
			"\nRun /server " + target + " --login to switch and sign in, or restart with -server and -user/OPENVIBELY_AUTH_USERNAME."})
		return m, nil
	case client.IsInvalidServerURL(msg.err):
		m.append(entry{role: "error", text: invalidServerSwitchMessage(m.client.BaseURL())})
		return m, nil
	case client.IsReachableError(msg.err):
		m.append(entry{role: "error", text: target + " responded but is not a healthy OpenVibely backend: " +
			safeConnectionDiagnostic(msg.err) + "\n" + keep + " Check the URL and backend logs, then try /server again."})
		return m, nil
	default:
		m.append(entry{role: "error", text: "could not reach " + target + ": " + safeConnectionDiagnostic(msg.err) +
			"\n" + keep + " Check that the server is running and the URL is correct, then try /server again."})
		return m, nil
	}

	m = m.installServer(msg.candidate)
	m.log = nil
	m.refreshTranscript()
	m.connected = true
	m.connChecked = true
	m.capacity = msg.capacity
	m.auth = msg.auth
	m.projects = msg.projects
	m.projectsLoaded = true
	m.append(entry{role: "system", text: "Switched from " + current + " to " + target + "."})
	if len(m.projects) > 0 {
		m.setActiveProject(m.projects[0])
	}
	m.append(entry{role: "result", head: "Projects", text: renderProjects(m.projects, nil, m.selectedID, m.client.BaseURL())})
	if m.selectedID == "" {
		return m, nil
	}
	return m, m.connectSSE()
}

// installServer replaces the active client and drops every piece of state that
// belongs to the previous backend, including state keyed by project ID, since
// another server may reuse the same IDs for unrelated projects.
func (m Model) installServer(c *client.Client) Model {
	// A probe can finish while a credential prompt opened on the old
	// backend is active. Drop both its saved values and its input buffer
	// before installing the new client.
	m.resetChannelWizard()
	m.resetModelWizard()
	m.resetModelEditWizard()
	m.input.SetValue("")
	m.menu = nil
	m.resetProjectScopedState()
	m.clearPendingChat()
	m.advanceSessionGeneration()
	m.invalidateConnectionChecks()
	m.threadOpenRequestID++
	m.threadRefreshRequestID++
	m.projectRequestID = nextProjectRequestID()
	m.client = c
	m.projects = nil
	m.projectsLoaded = false
	m.selectedID = ""
	m.selectedName = ""
	m.wantProject = ""
	m.startupLogin = nil
	m.connected = false
	m.connChecked = false
	m.authRequired = false
	m.auth = nil
	m.capacity = nil
	m.connErr = ""
	m.connInvalidServerURL = false
	m.connReachableError = false
	m.statusProjectsUnavailable = false
	m.sseBackoff = time.Second
	m.sseRetryAfterProject = false
	return m
}

func (m Model) renderServer() string {
	var b strings.Builder
	state := "connecting"
	switch {
	case m.authRequired:
		state = "sign-in required"
	case m.connectionPhase() == connectionPhaseOnline:
		state = "connected"
	case m.connectionPhase() == connectionPhaseInvalid:
		state = "invalid server URL"
	case m.connectionPhase() == connectionPhaseUnhealthy:
		state = "backend error"
	case m.connectionPhase() == connectionPhaseOffline:
		state = "offline"
	}
	fmt.Fprintf(&b, "  %-8s %s\n", "server", serverURLDisplay(m.client.BaseURL()))
	fmt.Fprintf(&b, "  %-8s %s\n", "state", state)
	fmt.Fprintf(&b, "  %-8s %s", "switch", cmdPrefix+"server <url>  (checks the server before replacing this connection)")
	return b.String()
}

func invalidServerSwitchMessage(currentBaseURL string) string {
	return "invalid server URL; use http(s)://host[:port][/path] without credentials, query, or fragment. " +
		"The URL was not contacted. Still using " + serverURLDisplay(currentBaseURL) + "."
}

// redactServerCommandSecrets strips URL userinfo, query, and fragment from
// /server arguments before the line is echoed or stored in history.
func redactServerCommandSecrets(commandLine string) string {
	fields, err := tokenizeCommand(commandLine)
	if err != nil {
		// Malformed quotes are reported only after submit records the line.
		// Recognize the root conservatively and suppress all operands rather
		// than attempting to recover a possibly secret-bearing URL.
		fallback := strings.Fields(strings.NewReplacer(`"`, "", "'", "").Replace(commandLine))
		if len(fallback) > 0 {
			root := strings.ToLower(strings.TrimLeft(fallback[0], "/"))
			if root == "server" || root == "connect" {
				return "/" + root + " <redacted invalid arguments>"
			}
		}
		return commandLine
	}
	if len(fields) < 2 {
		return commandLine
	}
	root := strings.ToLower(strings.TrimLeft(fields[0], "/"))
	if root != "server" && root != "connect" {
		return commandLine
	}
	changed := false
	for i := 1; i < len(fields); i++ {
		if strings.ContainsAny(fields[i], "@?#") {
			fields[i] = serverURLDisplay(fields[i])
			changed = true
		}
	}
	if !changed {
		return commandLine
	}
	return strings.Join(fields, " ")
}
