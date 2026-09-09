package tui

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	backendInstallationGuideURL  = "https://docs.openvibely.ai/installation"
	maxConnectionDiagnosticWidth = 240
)

var (
	connectionDiagnosticURL    = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)
	connectionDiagnosticSecret = regexp.MustCompile(`(?i)\b(?:access[_-]?token|api[_-]?key|authorization|cookie|credential|password|secret|token)\b\s*(?:=|:)\s*(?:"[^"]*"|'[^']*'|(?:bearer\s+)?[^\s,;]+)`)
)

// setupGuidance renders only instructions that the user may choose to run. It
// intentionally performs no backend, project, authentication, process, or file
// operation so it remains useful on a first launch and when offline.
func setupGuidance(platform, baseURL string) string {
	var b strings.Builder
	b.WriteString("Setup is read-only: it gives you commands to choose and run yourself. It does not install OpenVibely. It does not clone repositories. It does not start processes. It does not create projects. It does not authenticate. It does not modify files.\n\n")
	b.WriteString("Local backend\n")
	switch platform {
	case "windows":
		b.WriteString("In PowerShell, explicitly install the server using the backend's documented Windows command:\n")
		b.WriteString("  & ([scriptblock]::Create((irm https://openvibely.ai/install.ps1))) -Variant binary\n")
		b.WriteString("For a source checkout in a shell that supports it, start the server with:\n")
		b.WriteString("  ./start.sh\n")
	default:
		b.WriteString("On macOS or Linux, explicitly install the server using the backend's documented command:\n")
		b.WriteString("  curl -fsSL https://openvibely.ai/install.sh | bash -s -- --variant binary\n")
		b.WriteString("From an OpenVibely source checkout, start the server with:\n")
		b.WriteString("  ./start.sh\n")
	}
	fmt.Fprintf(&b, "See %s for installed-binary launch details.\n\n", backendInstallationGuideURL)

	b.WriteString("Verify health\n")
	b.WriteString("  /status                         in the TUI\n")
	b.WriteString("  openvibely-tui status            in a shell\n")
	b.WriteString("A healthy local server normally listens at http://localhost:3001.\n\n")

	b.WriteString("Remote backend\n")
	if isRemoteServerURL(baseURL) {
		fmt.Fprintf(&b, "The configured server is remote: %s. Check or correct that URL before starting a local server.\n", serverURLDisplay(baseURL))
	}
	b.WriteString("  openvibely-tui -server <url> status\n")
	if platform == "windows" {
		b.WriteString("  $env:OPENVIBELY_SERVER_URL = \"<url>\"; openvibely-tui status\n")
	} else {
		b.WriteString("  OPENVIBELY_SERVER_URL=<url> openvibely-tui status\n")
	}
	b.WriteString("`-server` and `OPENVIBELY_SERVER_URL` only select a running backend; setup never changes them.\n")
	return strings.TrimRight(b.String(), "\n")
}

// ServerURLDisplay returns a terminal-safe configured server URL without
// userinfo, query, fragment, or terminal control characters.
func ServerURLDisplay(baseURL string) string {
	raw := sanitizeAutomationDetailText(strings.TrimSpace(baseURL))
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "configured server URL"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return sanitizeAutomationDetailText(u.String())
}

func serverURLDisplay(baseURL string) string {
	return ServerURLDisplay(baseURL)
}

// safeConnectionDiagnostic returns one bounded terminal-safe line from a
// transport or backend error. URLs and common credential assignments are
// redacted because net/http and backend errors can include user-controlled
// endpoint URLs or response text.
func safeConnectionDiagnostic(err error) string {
	if err == nil {
		return ""
	}
	return safeConnectionDiagnosticText(err.Error())
}

func safeConnectionDiagnosticText(value string) string {
	value = sanitizeAutomationDetailText(strings.TrimSpace(value))
	value = connectionDiagnosticURL.ReplaceAllStringFunc(value, serverURLDisplay)
	value = connectionDiagnosticSecret.ReplaceAllStringFunc(value, func(match string) string {
		if separator := strings.IndexAny(match, "=:"); separator >= 0 {
			return match[:separator+1] + "[redacted]"
		}
		return "[redacted]"
	})
	return truncate(strings.TrimSpace(value), maxConnectionDiagnosticWidth)
}

func isRemoteServerURL(baseURL string) bool {
	u, err := url.Parse(sanitizeAutomationDetailText(strings.TrimSpace(baseURL)))
	if err != nil {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || host == "localhost" || host == "0.0.0.0" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback() && !ip.IsUnspecified()
	}
	return true
}

func setupCommand() command {
	return command{
		name: "setup",
		desc: "read-only backend installation, startup, health, and remote-connection guidance",
		usage: []string{
			"setup                                      show read-only backend setup and recovery steps",
		},
		examples: []string{
			"setup",
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			m.busy = false
			if len(args) != 0 {
				return m, errCmd(commandUsage("setup", ""))
			}
			m.append(entry{role: "result", head: "Setup", text: setupGuidance(runtime.GOOS, m.client.BaseURL())})
			return m, nil
		},
	}
}
