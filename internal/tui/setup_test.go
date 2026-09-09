package tui

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/openvibely/openvibely-tui/internal/client"
)

func TestCLISetupIsReadOnlyAndBackendIndependent(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"setup"}, false, false); err != nil {
		t.Fatalf("setup failed without a backend: %v", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("setup made %d backend requests, want none", got)
	}

	guidance := out.String()
	for _, want := range []string{
		"Setup is read-only",
		"does not install OpenVibely",
		"does not start processes",
		"does not create projects",
		"does not authenticate",
		"does not modify files",
		"openvibely-tui status",
		"OPENVIBELY_SERVER_URL",
	} {
		if !strings.Contains(guidance, want) {
			t.Errorf("setup guidance missing %q:\n%s", want, guidance)
		}
	}
}

func TestSetupGuidanceUsesAuthoritativePlatformInstructions(t *testing.T) {
	for _, tc := range []struct {
		platform string
		want     []string
		unwanted []string
	}{
		{
			platform: "darwin",
			want: []string{
				"On macOS or Linux",
				"curl -fsSL https://openvibely.ai/install.sh | bash -s -- --variant binary",
				"./start.sh",
				"/status",
				"openvibely-tui status",
				"OPENVIBELY_SERVER_URL=<url> openvibely-tui status",
			},
			unwanted: []string{"install.ps1", "$env:OPENVIBELY_SERVER_URL"},
		},
		{
			platform: "linux",
			want: []string{
				"On macOS or Linux",
				"curl -fsSL https://openvibely.ai/install.sh | bash -s -- --variant binary",
				"./start.sh",
				"/status",
				"openvibely-tui status",
				"OPENVIBELY_SERVER_URL=<url> openvibely-tui status",
			},
			unwanted: []string{"install.ps1", "$env:OPENVIBELY_SERVER_URL"},
		},
		{
			platform: "windows",
			want: []string{
				"In PowerShell",
				"& ([scriptblock]::Create((irm https://openvibely.ai/install.ps1))) -Variant binary",
				"./start.sh",
				"/status",
				"openvibely-tui status",
				"$env:OPENVIBELY_SERVER_URL = \"<url>\"; openvibely-tui status",
			},
			unwanted: []string{"install.sh | bash", "OPENVIBELY_SERVER_URL=<url>"},
		},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			guidance := setupGuidance(tc.platform, "http://localhost:3001")
			for _, want := range tc.want {
				if !strings.Contains(guidance, want) {
					t.Errorf("setup guidance missing %q:\n%s", want, guidance)
				}
			}
			for _, unwanted := range tc.unwanted {
				if strings.Contains(guidance, unwanted) {
					t.Errorf("setup guidance unexpectedly contains %q:\n%s", unwanted, guidance)
				}
			}
		})
	}
}

func TestOfflineRecoveryRecommendsSetupAndPrioritizesRemoteURL(t *testing.T) {
	local := OfflineRecoveryMessage("http://localhost:3001", errors.New("dial refused"))
	for _, want := range []string{
		"Run /setup in the TUI",
		"openvibely-tui setup in a shell",
		"Start or check your local OpenVibely backend",
		"/status",
	} {
		if !strings.Contains(local, want) {
			t.Errorf("local recovery missing %q:\n%s", want, local)
		}
	}

	remote := OfflineRecoveryMessage("https://ops.example:3001", errors.New("dial refused"))
	firstAction := strings.Index(remote, "  - ")
	if firstAction < 0 {
		t.Fatalf("remote recovery has no action:\n%s", remote)
	}
	firstLine := remote[firstAction:]
	if end := strings.IndexByte(firstLine, '\n'); end >= 0 {
		firstLine = firstLine[:end]
	}
	for _, want := range []string{"Check or correct the configured remote server URL first", "-server <url>", "OPENVIBELY_SERVER_URL", "/setup"} {
		if !strings.Contains(remote, want) {
			t.Errorf("remote recovery missing %q:\n%s", want, remote)
		}
	}
	if !strings.Contains(firstLine, "remote server URL") {
		t.Errorf("remote URL correction was not the first action: %q", firstLine)
	}
	if strings.Contains(remote, "Start or check your local OpenVibely backend") {
		t.Errorf("remote recovery suggests local startup:\n%s", remote)
	}
}

func TestSetupKeepsAuthAndUnhealthyDiagnosticsDistinct(t *testing.T) {
	auth := authRecoveryMessage("https://auth.example")
	if !strings.Contains(auth, "requires sign-in") || strings.Contains(strings.ToLower(auth), "offline") {
		t.Fatalf("auth diagnostic was changed to offline recovery:\n%s", auth)
	}

	unhealthy := ReachableBackendErrorMessage("https://health.example", errors.New("server error (503)"))
	for _, want := range []string{"responded but is unhealthy", "server error (503)", "/status"} {
		if !strings.Contains(unhealthy, want) {
			t.Errorf("unhealthy diagnostic missing %q:\n%s", want, unhealthy)
		}
	}
	for _, unwanted := range []string{"Unable to reach", "Start or check your local OpenVibely backend"} {
		if strings.Contains(unhealthy, unwanted) {
			t.Errorf("unhealthy diagnostic contains offline recovery %q:\n%s", unwanted, unhealthy)
		}
	}
}

func TestSetupIsDiscoverableAndBypassesProjectPreflight(t *testing.T) {
	cmd := lookupCommand("setup")
	if cmd == nil {
		t.Fatal("setup command is not registered")
	}
	if cmd.needsBackend() || cmd.cliProjectScoped(nil) || cmd.needsProjectLoad([]string{"setup"}) {
		t.Fatal("setup must not require backend or project discovery")
	}

	interactiveHelp := renderHelp()
	if !strings.Contains(interactiveHelp, "/setup") {
		t.Fatalf("interactive generated help omits setup:\n%s", interactiveHelp)
	}
	interactiveDetail := renderCommandHelp(*cmd)
	for _, want := range []string{"/setup", "read-only backend setup and recovery steps"} {
		if !strings.Contains(interactiveDetail, want) {
			t.Errorf("interactive setup help missing %q:\n%s", want, interactiveDetail)
		}
	}

	c, err := client.New("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	var cliHelp bytes.Buffer
	if err := RunCLI(c, &cliHelp, "", []string{"help", "setup"}, false, false); err != nil {
		t.Fatalf("CLI setup help should be backend independent: %v", err)
	}
	for _, want := range []string{"setup", "read-only backend setup and recovery steps"} {
		if !strings.Contains(cliHelp.String(), want) {
			t.Errorf("CLI generated setup help missing %q:\n%s", want, cliHelp.String())
		}
	}

	m := newTestModel(t)
	m.connected = false
	m.connChecked = true
	m.connErr = "dial refused"
	status := stripANSI(m.renderStatus() + "\n" + m.hint())
	for _, want := range []string{"/setup", "openvibely-tui setup", "/status"} {
		if !strings.Contains(status, want) {
			t.Errorf("offline status view missing %q:\n%s", want, status)
		}
	}
}

func TestOfflineRemoteStatusPrioritizesURLCorrection(t *testing.T) {
	c, err := client.New("https://ops.example:3001")
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	m.connChecked = true
	m.connErr = "dial refused"
	status := stripANSI(m.renderStatus() + "\n" + m.hint())
	urlAction := strings.Index(status, "check or correct the configured remote server URL")
	setupAction := strings.Index(status, "run /setup or openvibely-tui setup")
	if urlAction < 0 || setupAction < 0 {
		t.Fatalf("remote status omitted URL or setup guidance:\n%s", status)
	}
	if urlAction > setupAction {
		t.Fatalf("remote status puts setup before URL correction:\n%s", status)
	}
	if strings.Contains(status, "start/check your local backend") {
		t.Fatalf("remote status suggests local startup:\n%s", status)
	}
}

func TestAuthRequiredRemoteOfflineStatusPrioritizesURLCorrection(t *testing.T) {
	c, err := client.New("https://ops.example:3001")
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	m.authRequired = true
	m.connChecked = true
	m.connErr = "dial refused"
	status := stripANSI(m.renderStatus())

	remoteAction := strings.Index(status, "check or correct the configured remote server URL")
	loginAction := strings.Index(status, "use /login to enter credentials")
	if remoteAction < 0 || loginAction < 0 {
		t.Fatalf("auth-required remote status omitted recovery guidance:\n%s", status)
	}
	if remoteAction > loginAction {
		t.Fatalf("auth-required remote status does not prioritize URL correction:\n%s", status)
	}
	if strings.Contains(status, "start/check your local backend") {
		t.Fatalf("auth-required remote status suggests local startup:\n%s", status)
	}

	hint := m.hint()
	remoteHintAction := strings.Index(hint, "check/correct -server or OPENVIBELY_SERVER_URL")
	signInHint := strings.Index(hint, "sign-in required")
	if remoteHintAction < 0 || signInHint < 0 {
		t.Fatalf("auth-required remote hint omitted recovery guidance: %q", hint)
	}
	if remoteHintAction > signInHint {
		t.Fatalf("auth-required remote hint does not prioritize URL correction: %q", hint)
	}
}

func TestSetupGuidanceIsTerminalSafe(t *testing.T) {
	const secret = "very-secret-password"
	baseURL := "https://user:" + secret + "@remote.example:3001/path?token=also-secret#fragment\x1b[31m"
	guidance := setupGuidance("linux", baseURL)
	recovery := OfflineRecoveryMessage(baseURL, errors.New("dial refused"))
	for _, text := range []string{guidance, recovery} {
		if strings.Contains(text, secret) || strings.Contains(text, "also-secret") {
			t.Errorf("server credentials or query leaked into terminal output:\n%s", text)
		}
		if strings.Contains(text, "\x1b") || strings.Contains(text, "\nremote.example") {
			t.Errorf("unsafe terminal control or injected line in output:\n%q", text)
		}
		if !strings.Contains(text, "remote.example:3001/path") {
			t.Errorf("safe remote host/path missing from output:\n%s", text)
		}
	}
}

func TestConfiguredMixedCaseServerURLsRemainUsableAndTerminalSafe(t *testing.T) {
	for _, tc := range []struct {
		name     string
		server   string
		username string
		password string
		token    string
		fragment string
	}{
		{
			name:     "server flag uppercase scheme",
			server:   "HTTPS://flag-user:flag-password-must-not-appear@remote.example:3001/base?token=flag-token-must-not-appear#flag-fragment-must-not-appear\x1b[8m",
			username: "flag-user",
			password: "flag-password-must-not-appear",
			token:    "flag-token-must-not-appear",
			fragment: "flag-fragment-must-not-appear",
		},
		{
			name:     "environment mixed case scheme",
			server:   "hTtPs://environment-user:environment-password-must-not-appear@remote.example:3001/base?token=environment-token-must-not-appear#environment-fragment-must-not-appear\x1b[8m",
			username: "environment-user",
			password: "environment-password-must-not-appear",
			token:    "environment-token-must-not-appear",
			fragment: "environment-fragment-must-not-appear",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := client.New(tc.server)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(c.BaseURL(), "https://") {
				t.Fatalf("BaseURL did not preserve an HTTPS endpoint: %q", c.BaseURL())
			}

			m := New(c)
			m.connChecked = true
			m.connErr = "dial refused"
			rawStatus := m.renderStatus()
			outputs := []string{
				m.log[0].text,
				setupGuidance("linux", c.BaseURL()),
				OfflineRecoveryMessage(c.BaseURL(), errors.New("dial refused")),
				stripANSI(rawStatus),
			}
			for _, output := range outputs {
				for _, secret := range []string{tc.username, tc.password, tc.token, tc.fragment} {
					if strings.Contains(output, secret) {
						t.Errorf("configured server value leaked %q:\n%s", secret, output)
					}
				}
				if strings.ContainsAny(output, "\x1b\r") {
					t.Errorf("configured server value retained terminal control text: %q", output)
				}
				if !strings.Contains(output, "remote.example:3001/base") {
					t.Errorf("configured server display lost its safe endpoint: %s", output)
				}
			}
			// /status intentionally contains renderer-owned ANSI styles. Check the
			// exact injected sequence without stripping that raw output first.
			if strings.Contains(rawStatus, "\x1b[8m") {
				t.Errorf("raw status retained injected configured-server control sequence: %q", rawStatus)
			}
		})
	}
}

func TestSetupREADMEParity(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile(filepath.Join(wd, "..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(readme)
	for _, want := range []string{
		"`/setup`",
		"`openvibely-tui setup`",
		"do **not** install",
		"curl -fsSL https://openvibely.ai/install.sh | bash -s -- --variant binary",
		"& ([scriptblock]::Create((irm https://openvibely.ai/install.ps1))) -Variant binary",
		"./start.sh",
		"`openvibely-tui status`",
		"`-server <url>`",
		"`OPENVIBELY_SERVER_URL`",
		backendInstallationGuideURL,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("README setup guidance missing %q", want)
		}
	}
}

func TestConnectionDiagnosticsAreTerminalSafeAndBounded(t *testing.T) {
	const (
		urlPassword = "url-password-must-not-appear"
		queryToken  = "query-token-must-not-appear"
		urlFragment = "fragment-must-not-appear"
		apiToken    = "api-token-must-not-appear"
	)
	transport := fmt.Errorf("loading projects: %w", &url.Error{
		Op:  "Get",
		URL: "HTTPS://configured-user:" + urlPassword + "@remote.example:3001/api/projects?diagnostic=" + queryToken + "#" + urlFragment,
		Err: &net.OpError{
			Op:  "dial",
			Net: "tcp",
			Err: errors.New("connection refused\n\x1b[31minjected"),
		},
	})
	if !client.IsTransportError(transport) {
		t.Fatalf("wrapped transport error was not classified as transport: %v", transport)
	}
	reachable := fmt.Errorf("loading capacity: %w", &client.HTTPStatusError{
		StatusCode: http.StatusServiceUnavailable,
		Message:    "backend endpoint hTtPs://backend-user:" + urlPassword + "@remote.example:3001/health?diagnostic=" + queryToken + "#" + urlFragment + " token=" + apiToken + "\n\x1b[31m" + strings.Repeat("backend diagnostic ", 40),
	})

	for _, tc := range []struct {
		name   string
		err    error
		format func(string, error) string
		want   string
	}{
		{name: "transport", err: transport, format: OfflineRecoveryMessage, want: "dial tcp"},
		{name: "reachable", err: reachable, format: ReachableBackendErrorMessage, want: "server error (503)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := tc.format("https://display-user:"+urlPassword+"@remote.example:3001?token="+queryToken, tc.err)
			detailsAt := strings.LastIndex(output, "\nDetails: ")
			if detailsAt < 0 {
				t.Fatalf("diagnostic omitted details:\n%s", output)
			}
			details := output[detailsAt+len("\nDetails: "):]
			for _, secret := range []string{urlPassword, queryToken, urlFragment, apiToken} {
				if strings.Contains(output, secret) {
					t.Errorf("diagnostic leaked %q:\n%s", secret, output)
				}
			}
			if strings.ContainsAny(details, "\n\r\x1b") {
				t.Errorf("diagnostic contains terminal control or a line break: %q", details)
			}
			if width := lipgloss.Width(details); width > maxConnectionDiagnosticWidth {
				t.Errorf("diagnostic is not bounded (%d cells): %q", width, details)
			}
			if !strings.Contains(details, tc.want) {
				t.Errorf("diagnostic lost useful context %q: %q", tc.want, details)
			}
		})
	}

	c, err := client.New("https://display-user:" + urlPassword + "@remote.example:3001?token=" + queryToken)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		err       error
		reachable bool
	}{
		{name: "transport", err: transport},
		{name: "reachable", err: reachable, reachable: true},
	} {
		t.Run("status "+tc.name, func(t *testing.T) {
			m := New(c)
			m.connChecked = true
			m.connErr = tc.err.Error()
			m.connReachableError = tc.reachable
			rawStatus := m.renderStatus()
			status := stripANSI(rawStatus)
			for _, secret := range []string{urlPassword, queryToken, urlFragment, apiToken} {
				if strings.Contains(status, secret) {
					t.Errorf("status diagnostic leaked %q:\n%s", secret, status)
				}
			}
			if strings.ContainsAny(status, "\x1b\r") {
				t.Errorf("status retained terminal control text: %q", status)
			}
			for _, unsafe := range []string{"\x1b[31m", "\n\x1b[31minjected"} {
				if strings.Contains(rawStatus, unsafe) {
					t.Errorf("raw status retained injected diagnostic control text %q: %q", unsafe, rawStatus)
				}
			}
		})
	}
}
