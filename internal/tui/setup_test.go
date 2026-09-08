package tui

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

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
