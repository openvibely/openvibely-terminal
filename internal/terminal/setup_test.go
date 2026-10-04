package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
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

func TestSetupGuidanceAndInstallerExecutionShareAuthoritativeURLs(t *testing.T) {
	oldUnixURL, oldWindowsURL := backendInstallerUnixURL, backendInstallerWindowsURL
	backendInstallerUnixURL = "https://installer.example.test/custom-unix.sh"
	backendInstallerWindowsURL = "https://installer.example.test/custom-windows.ps1"
	t.Cleanup(func() {
		backendInstallerUnixURL, backendInstallerWindowsURL = oldUnixURL, oldWindowsURL
	})

	testCases := []struct {
		platform string
		wantLine string
		steps    []setupCommandSpec
	}{
		{
			platform: "linux",
			wantLine: "curl -fsSL " + backendInstallerUnixURL + " | bash -s -- --variant binary",
			steps: []setupCommandSpec{
				{Name: "curl", Found: true},
				{Name: "bash", Found: true},
			},
		},
		{
			platform: "windows",
			wantLine: "& ([scriptblock]::Create((irm " + backendInstallerWindowsURL + "))) -Variant binary",
			steps:    []setupCommandSpec{{Name: "powershell.exe", Found: true}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.platform, func(t *testing.T) {
			guidance := setupGuidance(tc.platform, "http://localhost:3001")
			if !strings.Contains(guidance, tc.wantLine) {
				t.Fatalf("setup guidance missing authoritative installer command %q:\n%s", tc.wantLine, guidance)
			}

			type invocation struct {
				name string
				args []string
			}
			var invocations []invocation
			oldExecCommand := setupExecCommand
			setupExecCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
				invocations = append(invocations, invocation{name: name, args: append([]string(nil), args...)})
				role := name
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSetupInstallerExecutionHelper$")
				cmd.Env = append(os.Environ(), "OPENVIBELY_INSTALLER_HELPER=1", "OPENVIBELY_INSTALLER_ROLE="+role)
				return cmd
			}
			t.Cleanup(func() { setupExecCommand = oldExecCommand })

			if err := runLocalBackendInstaller(context.Background(), tc.platform, tc.steps); err != nil {
				t.Fatalf("runLocalBackendInstaller() error = %v", err)
			}

			if tc.platform == "windows" {
				wantArgs := []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", tc.wantLine}
				if len(invocations) != 1 || invocations[0].name != "powershell.exe" || strings.Join(invocations[0].args, "\x00") != strings.Join(wantArgs, "\x00") {
					t.Fatalf("installer invocation = %#v, want powershell.exe %#v", invocations, wantArgs)
				}
				return
			}

			if len(invocations) != 2 || invocations[0].name != "curl" || strings.Join(invocations[0].args, "\x00") != strings.Join([]string{"-fsSL", backendInstallerUnixURL}, "\x00") {
				t.Fatalf("Unix installer download invocation = %#v, want curl -fsSL %q", invocations, backendInstallerUnixURL)
			}
			if invocations[1].name != "bash" || strings.Join(invocations[1].args, "\x00") != strings.Join([]string{"-s", "--", "--variant", "binary"}, "\x00") {
				t.Fatalf("Unix installer shell invocation = %#v, want bash -s -- --variant binary", invocations[1])
			}
		})
	}
}

func TestSetupInstallerExecutionHelper(t *testing.T) {
	if os.Getenv("OPENVIBELY_INSTALLER_HELPER") != "1" {
		return
	}
	switch os.Getenv("OPENVIBELY_INSTALLER_ROLE") {
	case "curl":
		_, _ = io.WriteString(os.Stdout, "installer contents")
	case "bash":
		_, _ = io.Copy(io.Discard, os.Stdin)
	}
	os.Exit(0)
}

func TestSetupLifecycleGuidanceCoversStopReconnectAndUpdate(t *testing.T) {
	guidance := setupGuidance("linux", "http://localhost:3001")
	for _, want := range []string{
		"Stop:",
		"Reconnect:",
		"Update:",
		"lsof -nP -iTCP:3001 -sTCP:LISTEN",
		"openvibely-terminal -server http://localhost:3001 status",
		"run the documented installer again",
	} {
		if !strings.Contains(guidance, want) {
			t.Errorf("setup lifecycle guidance missing %q:\n%s", want, guidance)
		}
	}
}

func TestReadmeDocumentsSetupLifecycleGuidance(t *testing.T) {
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
		"Stop a setup-started local backend",
		"Reconnect to a running backend",
		"Update a local backend",
		"openvibely-terminal -server http://localhost:3001 status",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("README setup lifecycle guidance missing %q", want)
		}
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
	startMarker, c := setupStartFixtureBecomesHealthyAfterStart(t)

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
	for _, want := range []string{
		"Start command launched",
		"Backend health check succeeded",
		"Stop:",
		"Reconnect:",
		"Update:",
		"openvibely-terminal -server",
		"Next: run /projects",
	} {
		if !strings.Contains(forced.String(), want) {
			t.Errorf("forced setup output missing %q:\n%s", want, forced.String())
		}
	}
}

func TestSetupStartReportsHealthyBackendWithoutStartingProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix executable fixture")
	}
	startMarker, c := setupStartFixture(t, http.StatusOK)

	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"setup", "start"}, true, false); err != nil {
		t.Fatalf("setup start against healthy backend failed: %v\n%s", err, out.String())
	}
	assertFileMissing(t, startMarker)
	for _, want := range []string{"already running", "Backend health check succeeded", "Next: run /projects"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("healthy setup start output missing %q:\n%s", want, out.String())
		}
	}
}

func TestSetupStartReportsHealthyBackendWithoutStartPrerequisite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix executable fixture")
	}
	startMarker, c := setupStartFixture(t, http.StatusOK)
	withoutSetupStartScript(t)
	oldLookPath := setupLookPath
	setupLookPath = func(name string) (string, error) {
		if name == "openvibely" {
			return "", errors.New("openvibely is not installed")
		}
		return oldLookPath(name)
	}
	t.Cleanup(func() { setupLookPath = oldLookPath })

	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"setup", "start"}, true, false); err != nil {
		t.Fatalf("setup start against healthy backend without start prerequisite failed: %v\n%s", err, out.String())
	}
	assertFileMissing(t, startMarker)
	for _, want := range []string{"already running", "Backend health check succeeded"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("healthy setup start output missing %q:\n%s", want, out.String())
		}
	}
}

func TestSetupStartTreatsAuthRequiredAsReachable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix executable fixture")
	}
	startMarker, c := setupStartFixture(t, http.StatusUnauthorized)

	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"setup", "start"}, true, false); err != nil {
		t.Fatalf("setup start against auth-required backend failed: %v\n%s", err, out.String())
	}
	assertFileMissing(t, startMarker)
	for _, want := range []string{"already running", "authenticate", "/login", "/projects"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("auth-required setup output missing %q:\n%s", want, out.String())
		}
	}
}

func TestSetupBootstrapInstallSkipsInstallerAndStartWhenHealthy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix executable fixture")
	}
	startMarker, c := setupStartFixture(t, http.StatusOK)
	installMarker := filepath.Join(t.TempDir(), "installed")
	fakeSetupToolPaths(t, map[string]string{"curl": filepath.Join(t.TempDir(), "curl"), "bash": filepath.Join(t.TempDir(), "bash")})
	oldInstaller := setupRunInstaller
	setupRunInstaller = func(context.Context, string, []setupCommandSpec) error {
		return os.WriteFile(installMarker, []byte("installed"), 0644)
	}
	t.Cleanup(func() { setupRunInstaller = oldInstaller })

	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"setup", "bootstrap", "--install"}, true, false); err != nil {
		t.Fatalf("setup bootstrap --install against healthy backend failed: %v\n%s", err, out.String())
	}
	assertFileMissing(t, installMarker)
	assertFileMissing(t, startMarker)
	if !strings.Contains(out.String(), "already running") {
		t.Fatalf("healthy bootstrap did not report already running:\n%s", out.String())
	}
}

func TestSetupBootstrapInstallSkipsMissingInstallerPrerequisitesWhenHealthy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix executable fixture")
	}
	startMarker, c := setupStartFixture(t, http.StatusOK)
	installMarker := filepath.Join(t.TempDir(), "installed")
	oldLookPath := setupLookPath
	setupLookPath = func(name string) (string, error) {
		if name == "curl" || name == "bash" {
			return "", errors.New(name + " is not installed")
		}
		return oldLookPath(name)
	}
	t.Cleanup(func() { setupLookPath = oldLookPath })
	oldInstaller := setupRunInstaller
	setupRunInstaller = func(context.Context, string, []setupCommandSpec) error {
		return os.WriteFile(installMarker, []byte("installed"), 0644)
	}
	t.Cleanup(func() { setupRunInstaller = oldInstaller })

	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"setup", "bootstrap", "--install"}, true, false); err != nil {
		t.Fatalf("setup bootstrap --install against healthy backend without installer prerequisites failed: %v\n%s", err, out.String())
	}
	assertFileMissing(t, installMarker)
	assertFileMissing(t, startMarker)
	if !strings.Contains(out.String(), "already running") {
		t.Fatalf("healthy bootstrap did not report already running:\n%s", out.String())
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

func TestSetupGuidanceForRemoteServerDoesNotRenderLocalLifecycleActions(t *testing.T) {
	guidance := setupGuidance("linux", "https://ops.example:3001")
	for _, unwanted := range []string{"Stop:", "lsof -nP", "kill <pid>", "taskkill /PID", "openvibely-terminal -server https://ops.example:3001 status"} {
		if strings.Contains(guidance, unwanted) {
			t.Errorf("remote setup guidance contains local lifecycle action %q:\n%s", unwanted, guidance)
		}
	}
	for _, want := range []string{"The configured server is remote", "setup start/bootstrap refuse remote server URLs"} {
		if !strings.Contains(guidance, want) {
			t.Errorf("remote setup guidance missing %q:\n%s", want, guidance)
		}
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
	startMarker, c := setupStartFixtureBecomesHealthyAfterStart(t)
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
	for _, want := range []string{"Installing local backend", "Installer completed", "Backend health check succeeded", "Stop:", "Reconnect:", "Update:"} {
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
	setupStartProcess = func(_ context.Context, spec setupCommandSpec) (setupBackendProcess, error) {
		if spec.Name != backendPath {
			return nil, fmt.Errorf("unexpected start command %q", spec.Name)
		}
		if err := os.WriteFile(startMarker, []byte("started"), 0644); err != nil {
			return nil, err
		}
		return &testSetupBackendProcess{stop: func() { _ = os.Remove(startMarker) }}, nil
	}
	t.Cleanup(func() { setupStartProcess = oldStart })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/capacity/global" {
			http.NotFound(w, r)
			return
		}
		if _, err := os.Stat(startMarker); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
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
	assertFileMissing(t, startMarker)
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
	for _, want := range []string{"/setup", "read-only backend setup and recovery steps", "stop/reconnect/update guidance", "lsof -nP -iTCP:3001 -sTCP:LISTEN", "openvibely-terminal --force setup bootstrap --install"} {
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
	for _, want := range []string{"setup", "read-only backend setup and recovery steps", "stop/reconnect/update guidance", "lsof -nP -iTCP:3001 -sTCP:LISTEN", "openvibely-terminal --force setup bootstrap --install"} {
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

func TestOfflineStatusUsesConsistentRecoveryHints(t *testing.T) {
	cases := []struct {
		name      string
		serverURL string
		wantHints []string
		unwanted  []string
	}{
		{
			name:      "local",
			serverURL: "http://127.0.0.1:3001",
			wantHints: []string{
				"run /setup or openvibely-terminal setup for read-only setup steps",
				"start/check your local backend, then run /status",
				"set -server <url> or OPENVIBELY_SERVER_URL",
			},
			unwanted: []string{"check or correct the configured remote server URL"},
		},
		{
			name:      "remote",
			serverURL: "https://ops.example:3001",
			wantHints: []string{
				"check or correct the configured remote server URL, then run /status",
				"set -server <url> or OPENVIBELY_SERVER_URL",
				"run /setup or openvibely-terminal setup for read-only connection guidance",
			},
			unwanted: []string{"start/check your local backend"},
		},
	}

	statusTryRows := func(status string) []string {
		var rows []string
		for _, line := range strings.Split(status, "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "try" {
				rows = append(rows, strings.Join(fields[1:], " "))
			}
		}
		return rows
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := client.New(tc.serverURL)
			if err != nil {
				t.Fatal(err)
			}
			normal := New(c)
			normal.connChecked = true
			normal.connErr = "dial refused"
			normalStatus := stripANSI(normal.renderStatus())

			authRequired := normal
			authRequired.authRequired = true
			authStatus := stripANSI(authRequired.renderStatus())
			filterAuthRows := func(rows []string) []string {
				var hints []string
				for _, row := range rows {
					if row != "use /login to enter credentials" && row != "help remains available without a backend" {
						hints = append(hints, row)
					}
				}
				return hints
			}
			normalHints := statusTryRows(normalStatus)
			authHints := filterAuthRows(statusTryRows(authStatus))
			if strings.Join(authHints, "\n") != strings.Join(normalHints, "\n") {
				t.Errorf("auth-required offline hints differ from ordinary offline hints:\nnormal:\n%s\nauth-required:\n%s", strings.Join(normalHints, "\n"), strings.Join(authHints, "\n"))
			}
			if len(normalHints) != len(tc.wantHints) {
				t.Fatalf("offline try rows = %q, want %q", normalHints, tc.wantHints)
			}
			for i, want := range tc.wantHints {
				if normalHints[i] != want {
					t.Errorf("offline hint %d = %q, want %q", i, normalHints[i], want)
				}
			}
			for _, unwanted := range tc.unwanted {
				if strings.Contains(normalStatus, unwanted) || strings.Contains(authStatus, unwanted) {
					t.Errorf("offline status for %s server contains %q", tc.name, unwanted)
				}
			}
			for _, want := range []string{"sign-in required · /login", "use /login to enter credentials", "help remains available without a backend"} {
				if !strings.Contains(authStatus, want) {
					t.Errorf("auth-required status missing %q:\n%s", want, authStatus)
				}
			}
		})
	}

	t.Run("backend unhealthy remains specific", func(t *testing.T) {
		m := newTestModel(t)
		m.connChecked = true
		m.connReachableError = true
		m.connErr = "health check failed"
		status := stripANSI(m.renderStatus())
		for _, want := range []string{"backend error (unhealthy)", "backend responded but is unhealthy; check backend logs, then run /status"} {
			if !strings.Contains(status, want) {
				t.Errorf("unhealthy status missing %q:\n%s", want, status)
			}
		}
		if strings.Contains(status, "start/check your local backend") || strings.Contains(status, "check or correct the configured remote server URL") {
			t.Fatalf("unhealthy status contains offline recovery guidance:\n%s", status)
		}

		m.authRequired = true
		authStatus := stripANSI(m.renderStatus())
		for _, want := range []string{"sign-in required · /login", "backend error (unhealthy)", "check backend logs, then run /status"} {
			if !strings.Contains(authStatus, want) {
				t.Errorf("auth-required unhealthy status missing %q:\n%s", want, authStatus)
			}
		}
		if strings.Contains(authStatus, "start/check your local backend") || strings.Contains(authStatus, "check or correct the configured remote server URL") {
			t.Fatalf("auth-required unhealthy status contains offline recovery guidance:\n%s", authStatus)
		}
	})

	for _, state := range []struct {
		name string
		edit func(*Model)
	}{
		{name: "connecting"},
		{name: "connected", edit: func(m *Model) { m.connected = true }},
	} {
		t.Run(state.name+" omits offline recovery", func(t *testing.T) {
			c, err := client.New("https://ops.example:3001")
			if err != nil {
				t.Fatal(err)
			}
			m := New(c)
			if state.edit != nil {
				state.edit(&m)
			}
			status := stripANSI(m.renderStatus())
			for _, unwanted := range []string{
				"check or correct the configured remote server URL",
				"start/check your local backend",
				"run /setup or openvibely-terminal setup for read-only",
			} {
				if strings.Contains(status, unwanted) {
					t.Errorf("%s status contains offline recovery hint %q:\n%s", state.name, unwanted, status)
				}
			}
		})
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
		"Stop a setup-started local backend",
		"Reconnect to a running backend",
		"Update a local backend",
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

func setupStartFixtureBecomesHealthyAfterStart(t *testing.T) (string, *client.Client) {
	t.Helper()
	startMarker := setupFakeBackendCommand(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/capacity/global" {
			http.NotFound(w, r)
			return
		}
		if _, err := os.Stat(startMarker); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
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
	return startMarker, c
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

type testSetupBackendProcess struct {
	stop      func()
	handedOff bool
}

func (p *testSetupBackendProcess) Handoff() error {
	p.handedOff = true
	return nil
}

func (p *testSetupBackendProcess) Stop() {
	if p != nil && !p.handedOff && p.stop != nil {
		p.stop()
	}
}

func TestSetupStartedBackendSurvivesSetupContextCancellationInCLI(t *testing.T) {
	marker, c, observed := setupRealBackendFixture(t, setupBackendHealthHealthy)
	var out bytes.Buffer
	if err := RunCLI(c, &out, "", []string{"setup", "start"}, true, false); err != nil {
		t.Fatalf("CLI setup start failed: %v\n%s", err, out.String())
	}
	assertHelperProcessRunning(t, marker)
	if _, err := c.GetGlobalCapacity(context.Background()); err != nil {
		t.Fatalf("backend was not reachable after CLI setup returned: %v", err)
	}
	select {
	case <-observed:
	default:
		t.Fatal("health check did not observe the started backend")
	}
}

func TestSetupStartedBackendSurvivesSetupContextCancellationInTUI(t *testing.T) {
	marker, c, _ := setupRealBackendFixture(t, setupBackendHealthHealthy)
	m := New(c)
	m, cmd := confirmSetupCommand(t, m, "/setup start")
	msg := cmd()
	result, ok := msg.(resultMsg)
	if !ok || result.err != nil {
		t.Fatalf("TUI setup start result = %#v", msg)
	}
	next, _ := m.Update(msg)
	m = next.(Model)
	if !strings.Contains(transcript(m), "Backend health check succeeded") {
		t.Fatalf("TUI setup result omitted health success:\n%s", transcript(m))
	}
	assertHelperProcessRunning(t, marker)
	if _, err := c.GetGlobalCapacity(context.Background()); err != nil {
		t.Fatalf("backend was not reachable after TUI setup returned: %v", err)
	}
}

func TestSetupCancellationBeforeHandoffStopsBackendProcess(t *testing.T) {
	marker, c, observed := setupRealBackendFixture(t, setupBackendHealthBlocksUntilCanceled)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- RunCLIContext(ctx, c, io.Discard, "", []string{"setup", "start"}, true, false)
	}()
	select {
	case <-observed:
	case <-time.After(3 * time.Second):
		t.Fatal("setup did not reach backend health polling")
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("canceled CLI setup returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("CLI setup did not stop after cancellation")
	}
	assertHelperProcessStopped(t, marker)
}

func TestSetupHealthTimeoutInTUIStopsBackendProcess(t *testing.T) {
	marker, c, observed := setupRealBackendFixture(t, setupBackendHealthUnhealthy)
	m := New(c)
	m, cmd := confirmSetupCommand(t, m, "/setup start")
	msg := cmd()
	result, ok := msg.(resultMsg)
	if !ok || result.err == nil || !strings.Contains(result.err.Error(), "backend did not become healthy") {
		t.Fatalf("TUI setup timeout result = %#v", msg)
	}
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("health polling did not observe the started backend")
	}
	assertHelperProcessStopped(t, marker)
}

func TestStartLocalBackendRejectsCanceledContextAndReportsStartFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix helper command")
	}
	marker, _, _ := setupRealBackendFixture(t, setupBackendHealthHealthy)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := startLocalBackendProcess(ctx, setupCommandSpec{Name: "unused"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled process start error = %v, want context canceled", err)
	}
	assertFileMissing(t, marker)

	_, err = startLocalBackendProcess(context.Background(), setupCommandSpec{Name: filepath.Join(t.TempDir(), "missing-backend")})
	if err == nil || !strings.Contains(err.Error(), "starting local backend") {
		t.Fatalf("missing backend start error = %v, want startup failure", err)
	}
}

func confirmSetupCommand(t *testing.T, m Model, line string) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.runCommand(line)
	m = next.(Model)
	if cmd != nil || m.pendingConfirmation == nil {
		t.Fatalf("setup command did not wait for confirmation: cmd=%v pending=%v", cmd != nil, m.pendingConfirmation != nil)
	}
	m.input.SetValue("yes")
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("confirmed setup command returned no command")
	}
	return m, cmd
}

type setupBackendHealthMode int

const (
	setupBackendHealthHealthy setupBackendHealthMode = iota
	setupBackendHealthUnhealthy
	setupBackendHealthBlocksUntilCanceled
)

func setupRealBackendFixture(t *testing.T, mode setupBackendHealthMode) (string, *client.Client, <-chan struct{}) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix executable helper")
	}
	oldTimeout := setupHealthWaitTimeout
	oldPoll := setupHealthPoll
	setupHealthWaitTimeout = 3 * time.Second
	setupHealthPoll = 10 * time.Millisecond
	t.Cleanup(func() {
		setupHealthWaitTimeout = oldTimeout
		setupHealthPoll = oldPoll
	})
	withoutSetupStartScript(t)
	tmp := t.TempDir()
	marker := filepath.Join(tmp, "backend-helper")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENVIBELY_SETUP_HELPER_BINARY", binary)
	t.Setenv("OPENVIBELY_SETUP_HELPER_MODE", "1")
	t.Setenv("OPENVIBELY_SETUP_HELPER_MARKER", marker)
	launcher := filepath.Join(tmp, "openvibely")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec \"$OPENVIBELY_SETUP_HELPER_BINARY\" '-test.run=^TestSetupBackendHelperProcess$'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	fakeSetupToolPaths(t, map[string]string{"openvibely": launcher})

	observed := make(chan struct{})
	var observedOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/capacity/global" {
			http.NotFound(w, r)
			return
		}
		_, statErr := os.Stat(marker)
		started := statErr == nil
		if started {
			observedOnce.Do(func() { close(observed) })
		}
		if started && mode == setupBackendHealthBlocksUntilCanceled {
			<-r.Context().Done()
			return
		}
		if started && mode == setupBackendHealthHealthy {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"max_workers":1,"available_slots":1,"has_capacity":true}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		data, err := os.ReadFile(marker)
		if err != nil {
			return
		}
		fields := strings.Fields(string(data))
		if len(fields) < 2 {
			return
		}
		pid, err := strconv.Atoi(fields[1])
		if err == nil {
			if process, err := os.FindProcess(pid); err == nil {
				_ = process.Kill()
			}
		}
	})
	return marker, c, observed
}

func TestSetupBackendHelperProcess(t *testing.T) {
	if os.Getenv("OPENVIBELY_SETUP_HELPER_MODE") != "1" {
		return
	}
	marker := os.Getenv("OPENVIBELY_SETUP_HELPER_MARKER")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("helper listen: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = server.Serve(listener) }()
	if err := os.WriteFile(marker, []byte(fmt.Sprintf("%s %d", listener.Addr().String(), os.Getpid())), 0600); err != nil {
		t.Fatalf("write helper marker: %v", err)
	}
	select {}
}

func assertHelperProcessRunning(t *testing.T, marker string) {
	t.Helper()
	address, _ := readHelperMarker(t, marker)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("helper process at %s did not remain alive", address)
}

func assertHelperProcessStopped(t *testing.T, marker string) {
	t.Helper()
	address, _ := readHelperMarker(t, marker)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("helper process at %s remained alive after setup stopped it", address)
}

func readHelperMarker(t *testing.T, marker string) (string, int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(marker)
		if err == nil {
			fields := strings.Fields(string(data))
			if len(fields) == 2 {
				pid, parseErr := strconv.Atoi(fields[1])
				if parseErr == nil {
					return fields[0], pid
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("helper process did not write marker %s", marker)
	return "", 0
}

func setupFakeBackendCommand(t *testing.T) string {
	t.Helper()
	withoutSetupStartScript(t)
	binDir := t.TempDir()
	marker := filepath.Join(binDir, "started")
	backendPath := filepath.Join(binDir, "openvibely")
	fakeSetupToolPaths(t, map[string]string{"openvibely": backendPath})
	oldStart := setupStartProcess
	setupStartProcess = func(_ context.Context, spec setupCommandSpec) (setupBackendProcess, error) {
		if spec.Name != backendPath {
			return nil, fmt.Errorf("unexpected start command %q", spec.Name)
		}
		if err := os.WriteFile(marker, []byte("started"), 0644); err != nil {
			return nil, err
		}
		return &testSetupBackendProcess{stop: func() { _ = os.Remove(marker) }}, nil
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
