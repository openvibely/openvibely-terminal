package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/openvibely/openvibely-terminal/internal/client"
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
		"openvibely-terminal status",
		"OPENVIBELY_SERVER_URL",
	} {
		if !strings.Contains(guidance, want) {
			t.Errorf("setup guidance missing %q:\n%s", want, guidance)
		}
	}
}

func TestSetupRemainsAvailableAndSafeForInvalidServerURL(t *testing.T) {
	server := "https://setup-user:setup-password-must-not-appear@ops.example/%zz?token=setup-token-must-not-appear#setup-fragment-must-not-appear\x1b[8m"
	c, err := client.New(server)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"setup"}, false, false); err != nil {
		t.Fatalf("setup failed for invalid server URL: %v", err)
	}
	guidance := out.String()
	for _, want := range []string{"Setup is read-only", "Invalid configured server URL", "-server <url>", "OPENVIBELY_SERVER_URL"} {
		if !strings.Contains(guidance, want) {
			t.Errorf("setup guidance missing %q:\n%s", want, guidance)
		}
	}
	for _, unwanted := range []string{"Local backend", "./start.sh", "Start or check your local OpenVibely backend"} {
		if strings.Contains(guidance, unwanted) {
			t.Errorf("invalid setup guidance contains local startup advice %q:\n%s", unwanted, guidance)
		}
	}
	for _, secret := range []string{"setup-user", "setup-password-must-not-appear", "setup-token-must-not-appear", "setup-fragment-must-not-appear"} {
		if strings.Contains(guidance, secret) {
			t.Errorf("setup guidance leaked %q:\n%s", secret, guidance)
		}
	}
	if strings.Contains(guidance, "\x1b[8m") {
		t.Fatalf("setup guidance retained terminal controls: %q", guidance)
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
				"openvibely-terminal status",
				"OPENVIBELY_SERVER_URL=<url> openvibely-terminal status",
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
				"openvibely-terminal status",
				"OPENVIBELY_SERVER_URL=<url> openvibely-terminal status",
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
				"openvibely-terminal status",
				"$env:OPENVIBELY_SERVER_URL = \"<url>\"; openvibely-terminal status",
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

func TestSetupCheckOnlyReportsMissingPiecesWithoutStateChanges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix PATH/script assumptions")
	}
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	withoutSetupStartScript(t)
	t.Setenv("PATH", t.TempDir())
	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"setup", "check"}, false, false); err != nil {
		t.Fatalf("setup check failed: %v", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("setup check made %d backend requests, want none", got)
	}
	text := out.String()
	for _, want := range []string{"Setup check is read-only", "missing executable ./start.sh", "does not install", "modify files"} {
		if !strings.Contains(text, want) {
			t.Errorf("setup check missing %q:\n%s", want, text)
		}
	}
}

func TestSetupStartRequiresConfirmationOrForce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix executable fixture")
	}
	withFastSetupHealth(t)
	startMarker, c := setupStartFixture(t, http.StatusOK)

	var unforced bytes.Buffer
	if err := RunCLI(c, &unforced, "", []string{"setup", "start"}, false, false); err == nil || !strings.Contains(err.Error(), "use --force to confirm setup start") {
		t.Fatalf("unforced setup start error = %v, output:\n%s", err, unforced.String())
	}
	assertFileMissing(t, startMarker)

	m := New(c)
	next, cmd := m.runCommand("/setup start")
	m = next.(Model)
	if cmd != nil || m.pendingConfirmation == nil {
		t.Fatalf("interactive setup start did not wait for confirmation: cmd=%v pending=%v", cmd, m.pendingConfirmation != nil)
	}
	for _, want := range []string{
		"setup does not create files before starting",
		"backend process may read or write its own data after launch",
		"start process `openvibely`",
		"poll local backend health",
		"no credentials sent",
		"Type 'yes' to confirm",
	} {
		if !strings.Contains(m.pendingConfirmation.message, want) {
			t.Fatalf("setup confirmation did not disclose %q:\n%s", want, m.pendingConfirmation.message)
		}
	}
	assertFileMissing(t, startMarker)

	var forced bytes.Buffer
	if err := RunCLI(c, &forced, "", []string{"setup", "start"}, true, false); err != nil {
		t.Fatalf("forced setup start failed: %v\n%s", err, forced.String())
	}
	assertFileEventuallyContains(t, startMarker, "started")
	for _, want := range []string{"Start command launched", "Backend health check succeeded", "Next: run /projects"} {
		if !strings.Contains(forced.String(), want) {
			t.Errorf("forced setup output missing %q:\n%s", want, forced.String())
		}
	}
}

func TestSetupStartCancellationDoesNotRunProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix executable fixture")
	}
	startMarker, c := setupStartFixture(t, http.StatusOK)
	m := New(c)
	next, cmd := m.runCommand("/setup start")
	m = next.(Model)
	if cmd != nil || m.pendingConfirmation == nil {
		t.Fatalf("setup start did not open confirmation: cmd=%v pending=%v", cmd, m.pendingConfirmation != nil)
	}
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if cmd != nil || m.pendingConfirmation != nil {
		t.Fatalf("Esc did not cancel setup confirmation: cmd=%v pending=%v", cmd, m.pendingConfirmation != nil)
	}
	assertFileMissing(t, startMarker)
	if !strings.Contains(transcript(m), "cancelled") {
		t.Fatalf("cancellation was not rendered:\n%s", transcript(m))
	}
}

func TestSetupRemoteServerFailsClosedWithoutStartingLocalProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix executable fixture")
	}
	startMarker := setupFakeBackendCommand(t)
	c, err := client.New("https://ops.example:3001")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = RunCLI(c, &out, "", []string{"setup", "bootstrap"}, true, false)
	if err == nil || !strings.Contains(err.Error(), "Configured backend is remote") {
		t.Fatalf("remote setup bootstrap error = %v, output:\n%s", err, out.String())
	}
	assertFileMissing(t, startMarker)
}

func TestSetupBootstrapInstallUsesOptInInstallerAndWaitsForHealth(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix executable fixture")
	}
	withFastSetupHealth(t)
	startMarker, c := setupStartFixture(t, http.StatusOK)
	installMarker := filepath.Join(t.TempDir(), "installed")
	fakeSetupToolPaths(t, map[string]string{"curl": filepath.Join(t.TempDir(), "curl"), "bash": filepath.Join(t.TempDir(), "bash")})
	oldInstaller := setupRunInstaller
	setupRunInstaller = func(_ context.Context, _ string, steps []setupCommandSpec) error {
		if len(steps) != 2 || !steps[0].Found || !steps[1].Found {
			return fmt.Errorf("installer prerequisites were not resolved: %+v", steps)
		}
		return os.WriteFile(installMarker, []byte("installed"), 0644)
	}
	t.Cleanup(func() { setupRunInstaller = oldInstaller })

	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"setup", "bootstrap", "--install"}, true, false); err != nil {
		t.Fatalf("setup bootstrap --install failed: %v\n%s", err, out.String())
	}
	assertFileContains(t, installMarker, "installed")
	assertFileEventuallyContains(t, startMarker, "started")
	for _, want := range []string{"Installing local backend", "Installer completed", "Backend health check succeeded"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("bootstrap install output missing %q:\n%s", want, out.String())
		}
	}
}

func TestSetupBootstrapInstallCanProvideMissingStartCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix executable fixture")
	}
	withFastSetupHealth(t)
	withoutSetupStartScript(t)

	var installed atomic.Bool
	tmp := t.TempDir()
	backendPath := filepath.Join(tmp, "openvibely")
	installMarker := filepath.Join(tmp, "installed")
	startMarker := filepath.Join(tmp, "started")

	oldLookPath := setupLookPath
	setupLookPath = func(name string) (string, error) {
		switch name {
		case "curl", "bash":
			return filepath.Join(tmp, name), nil
		case "openvibely":
			if installed.Load() {
				return backendPath, nil
			}
			return "", errors.New("openvibely not installed yet")
		default:
			return oldLookPath(name)
		}
	}
	t.Cleanup(func() { setupLookPath = oldLookPath })

	oldInstaller := setupRunInstaller
	setupRunInstaller = func(_ context.Context, _ string, steps []setupCommandSpec) error {
		if len(steps) != 2 || !steps[0].Found || !steps[1].Found {
			return fmt.Errorf("installer prerequisites were not resolved: %+v", steps)
		}
		installed.Store(true)
		return os.WriteFile(installMarker, []byte("installed"), 0644)
	}
	t.Cleanup(func() { setupRunInstaller = oldInstaller })

	oldStart := setupStartProcess
	setupStartProcess = func(_ context.Context, spec setupCommandSpec) error {
		if spec.Name != backendPath {
			return fmt.Errorf("unexpected start command %q", spec.Name)
		}
		return os.WriteFile(startMarker, []byte("started"), 0644)
	}
	t.Cleanup(func() { setupStartProcess = oldStart })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/capacity/global" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"max_workers":1,"available_slots":1,"has_capacity":true}`))
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	m := New(c)
	next, cmd := m.runCommand("/setup bootstrap --install")
	m = next.(Model)
	if cmd != nil || m.pendingConfirmation == nil {
		t.Fatalf("interactive install bootstrap did not wait for confirmation: cmd=%v pending=%v", cmd, m.pendingConfirmation != nil)
	}
	if !strings.Contains(m.pendingConfirmation.message, "may create or replace backend files") {
		t.Fatalf("install confirmation did not disclose filesystem effects:\n%s", m.pendingConfirmation.message)
	}
	assertFileMissing(t, installMarker)
	assertFileMissing(t, startMarker)

	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"setup", "bootstrap", "--install"}, true, false); err != nil {
		t.Fatalf("setup bootstrap --install failed on fresh machine: %v\n%s", err, out.String())
	}
	assertFileContains(t, installMarker, "installed")
	assertFileContains(t, startMarker, "started")
	for _, want := range []string{"Installer completed", "Starting local backend with: openvibely", "Backend health check succeeded"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("fresh bootstrap output missing %q:\n%s", want, out.String())
		}
	}
}

func TestSetupBootstrapReportsHealthFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix executable fixture")
	}
	withFastSetupHealth(t)
	startMarker, c := setupStartFixture(t, http.StatusServiceUnavailable)
	var out bytes.Buffer
	err := RunCLI(c, &out, "", []string{"setup", "bootstrap"}, true, false)
	if err == nil || !strings.Contains(err.Error(), "backend did not become healthy") {
		t.Fatalf("setup bootstrap health error = %v, output:\n%s", err, out.String())
	}
	assertFileEventuallyContains(t, startMarker, "started")
	if !strings.Contains(out.String(), "Waiting for backend health") {
		t.Fatalf("health wait was not disclosed in output:\n%s", out.String())
	}
}

func TestOfflineRecoveryRecommendsSetupAndPrioritizesRemoteURL(t *testing.T) {
	local := OfflineRecoveryMessage("http://localhost:3001", errors.New("dial refused"))
	for _, want := range []string{
		"Run /setup in the TUI",
		"openvibely-terminal setup in a shell",
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
	for _, want := range []string{"/setup", "openvibely-terminal setup", "/status"} {
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
	setupAction := strings.Index(status, "run /setup or openvibely-terminal setup")
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
	if !strings.Contains(guidance, "Invalid configured server URL") {
		t.Fatalf("unsafe setup URL was not classified as invalid:\n%s", guidance)
	}
	if !strings.Contains(recovery, "remote.example:3001/path") {
		t.Fatalf("safe remote host/path missing from recovery output:\n%s", recovery)
	}
	for _, text := range []string{guidance, recovery} {
		if strings.Contains(text, secret) || strings.Contains(text, "also-secret") {
			t.Errorf("server credentials or query leaked into terminal output:\n%s", text)
		}
		if strings.Contains(text, "\x1b") || strings.Contains(text, "\nremote.example") {
			t.Errorf("unsafe terminal control or injected line in output:\n%q", text)
		}
	}
}

func TestSetupGuidanceRejectsUnsafeServerURLComponents(t *testing.T) {
	for _, tc := range []struct {
		name   string
		server string
		secret string
	}{
		{name: "query", server: "https://remote.example:3001/path?tenant=setup-query-secret", secret: "setup-query-secret"},
		{name: "fragment", server: "https://remote.example:3001/path#setup-fragment-secret", secret: "setup-fragment-secret"},
		{name: "credentials", server: "https://setup-user:setup-password-secret@remote.example:3001/path", secret: "setup-password-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guidance := setupGuidance("linux", tc.server)
			if !strings.Contains(guidance, "Invalid configured server URL") {
				t.Fatalf("setup guidance did not classify %s URL as invalid:\n%s", tc.name, guidance)
			}
			if strings.Contains(guidance, tc.secret) || strings.Contains(guidance, "setup-user") {
				t.Fatalf("setup guidance leaked %s URL data:\n%s", tc.name, guidance)
			}
			if strings.Contains(guidance, "Local backend") || strings.Contains(guidance, "./start.sh") {
				t.Fatalf("setup guidance gave startup advice for invalid %s URL:\n%s", tc.name, guidance)
			}
		})
	}
}

func TestConfiguredMixedCaseServerURLsRemainUsableAndTerminalSafe(t *testing.T) {
	for _, tc := range []struct {
		name   string
		server string
		prefix string
	}{
		{name: "server flag uppercase scheme", server: "HTTPS://remote.example:3001/base", prefix: "https://"},
		{name: "environment mixed case scheme", server: "hTtP://remote.example:3001/base", prefix: "http://"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := client.New(tc.server)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(c.BaseURL(), tc.prefix) {
				t.Fatalf("BaseURL did not normalize scheme: %q", c.BaseURL())
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
				if strings.ContainsAny(output, "\x1b\r") {
					t.Errorf("configured server value retained terminal control text: %q", output)
				}
				if !strings.Contains(output, "remote.example:3001/base") {
					t.Errorf("configured server display lost its safe endpoint: %s", output)
				}
			}
		})
	}
}

func TestSetupUserGuideParity(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	guide, err := os.ReadFile(filepath.Join(wd, "..", "..", "docs", "user-guide.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(guide)
	for _, want := range []string{
		"`/setup`",
		"`openvibely-terminal setup`",
		"do **not** install",
		"`/setup check`",
		"`/setup start`",
		"`/setup bootstrap --install`",
		"openvibely-terminal --force setup bootstrap [--install]",
		"Remote server URLs\nfail closed",
		"curl -fsSL https://openvibely.ai/install.sh | bash -s -- --variant binary",
		"& ([scriptblock]::Create((irm https://openvibely.ai/install.ps1))) -Variant binary",
		"./start.sh",
		"`openvibely-terminal status`",
		"`-server <url>`",
		"`OPENVIBELY_SERVER_URL`",
		backendInstallationGuideURL,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("user guide setup guidance missing %q", want)
		}
	}
}

func withoutSetupStartScript(t *testing.T) {
	t.Helper()
	oldStat := setupStat
	setupStat = func(name string) (os.FileInfo, error) {
		if name == "./start.sh" {
			return nil, os.ErrNotExist
		}
		return oldStat(name)
	}
	t.Cleanup(func() { setupStat = oldStat })
}

func withFastSetupHealth(t *testing.T) {
	t.Helper()
	oldTimeout := setupHealthWaitTimeout
	oldPoll := setupHealthPoll
	setupHealthWaitTimeout = 80 * time.Millisecond
	setupHealthPoll = 10 * time.Millisecond
	t.Cleanup(func() {
		setupHealthWaitTimeout = oldTimeout
		setupHealthPoll = oldPoll
	})
}

func setupStartFixture(t *testing.T, healthStatus int) (string, *client.Client) {
	t.Helper()
	startMarker := setupFakeBackendCommand(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/capacity/global" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(healthStatus)
		if healthStatus >= 200 && healthStatus < 300 {
			_, _ = w.Write([]byte(`{"max_workers":1,"available_slots":1,"has_capacity":true}`))
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return startMarker, c
}

func setupFakeBackendCommand(t *testing.T) string {
	t.Helper()
	withoutSetupStartScript(t)
	binDir := t.TempDir()
	marker := filepath.Join(binDir, "started")
	backendPath := filepath.Join(binDir, "openvibely")
	fakeSetupToolPaths(t, map[string]string{"openvibely": backendPath})
	oldStart := setupStartProcess
	setupStartProcess = func(_ context.Context, spec setupCommandSpec) error {
		if spec.Name != backendPath {
			return fmt.Errorf("unexpected start command %q", spec.Name)
		}
		return os.WriteFile(marker, []byte("started"), 0644)
	}
	t.Cleanup(func() { setupStartProcess = oldStart })
	return marker
}

func fakeSetupToolPaths(t *testing.T, tools map[string]string) {
	t.Helper()
	oldLookPath := setupLookPath
	setupLookPath = func(name string) (string, error) {
		if path, ok := tools[name]; ok {
			return path, nil
		}
		return oldLookPath(name)
	}
	t.Cleanup(func() { setupLookPath = oldLookPath })
}

func assertFileMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("%s exists; expected no setup side effect", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checking %s: %v", path, err)
	}
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}

func assertFileEventuallyContains(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), want) {
			return
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("reading %s: %v", path, lastErr)
	}
	assertFileContains(t, path, want)
}

func TestConnectionDiagnosticsAreTerminalSafeAndBounded(t *testing.T) {
	const (
		urlPassword      = "url-password-must-not-appear"
		queryToken       = "query-token-must-not-appear"
		urlFragment      = "fragment-must-not-appear"
		basicCredential  = "basic-credential-must-not-appear"
		bearerCredential = "bearer-credential-must-not-appear"
		apiToken         = "api-token-must-not-appear"
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
		Message: "backend endpoint hTtPs://backend-user:" + urlPassword + "@remote.example:3001/health?diagnostic=" + queryToken + "#" + urlFragment +
			" Authorization: Basic " + basicCredential + " Authorization: Bearer " + bearerCredential + " token=" + apiToken +
			"\n\x1b[31m" + strings.Repeat("backend diagnostic ", 40),
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
			for _, secret := range []string{urlPassword, queryToken, urlFragment, basicCredential, bearerCredential, apiToken} {
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
			for _, secret := range []string{urlPassword, queryToken, urlFragment, basicCredential, bearerCredential, apiToken} {
				if strings.Contains(rawStatus, secret) {
					t.Errorf("raw status diagnostic leaked %q:\n%s", secret, rawStatus)
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
