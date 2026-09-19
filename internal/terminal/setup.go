package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

const (
	backendInstallationGuideURL  = "https://docs.openvibely.ai/installation"
	backendInstallerUnixURL      = "https://openvibely.ai/install.sh"
	backendInstallerWindowsURL   = "https://openvibely.ai/install.ps1"
	maxConnectionDiagnosticWidth = 240
)

var (
	connectionDiagnosticURL    = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)
	connectionDiagnosticSecret = regexp.MustCompile(`(?i)\b(?:access[_-]?token|api[_-]?key|authorization|cookie|credential|password|secret|token)\b\s*(?:=|:)\s*(?:"[^"]*"|'[^']*'|(?:(?:bearer|basic)\s+)?[^\s,;]+)`)

	setupLookPath          = exec.LookPath
	setupStat              = os.Stat
	setupExecCommand       = exec.CommandContext
	setupRunInstaller      = runLocalBackendInstaller
	setupStartProcess      = startLocalBackendProcess
	setupHealthWaitTimeout = 30 * time.Second
	setupHealthPoll        = time.Second
)

// InvalidServerURLMessage formats recovery guidance for a malformed configured
// server URL. It deliberately does not include the raw value because malformed
// input cannot be safely parsed for userinfo, query, fragment, or control data.
func InvalidServerURLMessage(string) string {
	return "Invalid configured server URL.\nCorrect -server or OPENVIBELY_SERVER_URL, then run /status.\nThe URL was not sent to a backend."
}

// setupGuidance renders only instructions that the user may choose to run. It
// intentionally performs no backend, project, authentication, process, or file
// operation so it remains useful on a first launch and when offline.
func setupGuidance(platform, baseURL string) string {
	var b strings.Builder
	b.WriteString("Setup is read-only: it gives you commands to choose and run yourself. It does not install OpenVibely. It does not clone repositories. It does not start processes. It does not create projects. It does not authenticate. It does not modify files.\n\n")
	if !client.IsValidServerURL(baseURL) {
		b.WriteString(InvalidServerURLMessage(baseURL))
		b.WriteString("\n\n  openvibely-terminal -server <url> status\n")
		if platform == "windows" {
			b.WriteString("  $env:OPENVIBELY_SERVER_URL = \"<url>\"; openvibely-terminal status\n")
		} else {
			b.WriteString("  OPENVIBELY_SERVER_URL=<url> openvibely-terminal status\n")
		}
		return strings.TrimRight(b.String(), "\n")
	}
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

	b.WriteString("Opt-in local lifecycle\n")
	b.WriteString("  /setup check                    check local prerequisites only; no changes\n")
	b.WriteString("  /setup start                    confirm, start a local backend, then wait for health\n")
	b.WriteString("  /setup bootstrap --install      confirm, install if needed, start, then wait for health\n")
	b.WriteString("  openvibely-terminal setup check\n")
	b.WriteString("  openvibely-terminal --force setup bootstrap [--install]\n\n")

	b.WriteString("Verify health\n")
	b.WriteString("  /status                         in the TUI\n")
	b.WriteString("  openvibely-terminal status            in a shell\n")
	b.WriteString("A healthy local server normally listens at http://localhost:3001.\n\n")

	b.WriteString("Remote backend\n")
	if isRemoteServerURL(baseURL) {
		fmt.Fprintf(&b, "The configured server is remote: %s. Check or correct that URL before starting a local server.\n", serverURLDisplay(baseURL))
	}
	b.WriteString("  openvibely-terminal -server <url> status\n")
	if platform == "windows" {
		b.WriteString("  $env:OPENVIBELY_SERVER_URL = \"<url>\"; openvibely-terminal status\n")
	} else {
		b.WriteString("  OPENVIBELY_SERVER_URL=<url> openvibely-terminal status\n")
	}
	b.WriteString("`-server` and `OPENVIBELY_SERVER_URL` only select a running backend; bare setup never changes them, and setup start/bootstrap refuse remote server URLs.\n")
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

type setupOptions struct {
	action  string
	install bool
}

type setupCommandSpec struct {
	Name        string
	Args        []string
	Display     string
	Description string
	Found       bool
}

type setupCheckResult struct {
	BaseURL       string
	Platform      string
	CWD           string
	Remote        bool
	InvalidURL    bool
	Install       bool
	Start         setupCommandSpec
	InstallSteps  []setupCommandSpec
	Missing       []string
	Disclosures   []string
	RemoteMessage string
}

func setupCommand() command {
	return command{
		name:    "setup",
		actions: []string{"check", "start", "bootstrap"},
		desc:    "read-only guidance plus explicit opt-in local backend check/start/bootstrap",
		usage: []string{
			"setup                                      show read-only backend setup and recovery steps",
			"setup check [--install]                    check local backend prerequisites without changes",
			"setup start                                confirm, start a local backend, then wait for health",
			"setup bootstrap [--install]                confirm, optionally install, start, then wait for health",
		},
		examples: []string{
			"setup",
			"setup check",
			"setup start",
			"setup bootstrap --install",
		},
		run: func(m Model, args []string) (Model, tea.Cmd) {
			m.busy = false
			opts, err := parseSetupArgs(args)
			if err != nil {
				return m, errCmd(err.Error())
			}
			if opts.action == "" {
				m.append(entry{role: "result", head: "Setup", text: setupGuidance(runtime.GOOS, m.client.BaseURL())})
				return m, nil
			}

			check := inspectLocalBackendSetup(runtime.GOOS, m.client.BaseURL(), opts.install)
			if opts.action == "check" {
				m.append(entry{role: "result", head: "Setup", text: renderSetupCheck(check)})
				return m, nil
			}
			if check.InvalidURL {
				return m, errCmd(InvalidServerURLMessage(m.client.BaseURL()))
			}
			if check.Remote {
				return m, errCmd(check.RemoteMessage)
			}
			if missing := setupBlockingMissing(check, opts); len(missing) > 0 {
				return m, errCmd("setup " + opts.action + " cannot continue:\n" + strings.Join(prefixLines(missing, "  - "), "\n") + "\nRun setup check for details.")
			}

			cmd := m.run("Setup", setupHealthWaitTimeout+30*time.Second, func(ctx context.Context) (string, error) {
				return runSetupBootstrap(ctx, m.client, check, opts)
			})
			return confirmOr(m, setupConfirmationMessage(check, opts), "use --force to confirm setup "+opts.action+setupInstallSuffix(opts), cmd)
		},
	}
}

func parseSetupArgs(args []string) (setupOptions, error) {
	if len(args) == 0 {
		return setupOptions{}, nil
	}
	action := strings.ToLower(strings.TrimSpace(args[0]))
	if action != "check" && action != "start" && action != "bootstrap" {
		return setupOptions{}, errors.New(commandUsage("setup", ""))
	}
	opts := setupOptions{action: action}
	for _, arg := range args[1:] {
		switch strings.ToLower(strings.TrimSpace(arg)) {
		case "--install":
			opts.install = true
		default:
			return setupOptions{}, errors.New(commandUsage("setup", action))
		}
	}
	if opts.install && action == "start" {
		return setupOptions{}, errors.New("usage: " + cmdPrefix + "setup bootstrap [--install]")
	}
	return opts, nil
}

func setupInstallSuffix(opts setupOptions) string {
	if opts.install {
		return " --install"
	}
	return ""
}

func setupBlockingMissing(check setupCheckResult, opts setupOptions) []string {
	if opts.action == "bootstrap" && opts.install {
		var missing []string
		for _, step := range check.InstallSteps {
			if !step.Found {
				missing = append(missing, step.Description)
			}
		}
		return missing
	}
	return check.Missing
}

func inspectLocalBackendSetup(platform, baseURL string, install bool) setupCheckResult {
	cwd, _ := os.Getwd()
	check := setupCheckResult{
		BaseURL:  baseURL,
		Platform: platform,
		CWD:      sanitizeAutomationDetailText(cwd),
		Install:  install,
		Disclosures: []string{
			"filesystem: checks the current directory for a local start script and PATH for backend tools",
			"credentials: no credentials are read, written, or sent",
		},
	}
	if !client.IsValidServerURL(baseURL) {
		check.InvalidURL = true
		check.Missing = append(check.Missing, "correct -server or OPENVIBELY_SERVER_URL before local setup actions")
		return check
	}
	if isRemoteServerURL(baseURL) {
		check.Remote = true
		check.RemoteMessage = fmt.Sprintf("Configured backend is remote: %s. Correct -server or OPENVIBELY_SERVER_URL before running local setup start/bootstrap. No local process was started.", serverURLDisplay(baseURL))
		check.Disclosures = append(check.Disclosures, "process: no local backend process will be started for a remote server URL")
		return check
	}

	check.Start = findLocalBackendStartCommand(platform)
	if !check.Start.Found {
		check.Missing = append(check.Missing, check.Start.Description)
	}
	if install {
		check.InstallSteps = findLocalBackendInstallCommands(platform)
		for _, step := range check.InstallSteps {
			if !step.Found {
				check.Missing = append(check.Missing, step.Description)
			}
		}
	}
	check.Disclosures = append(check.Disclosures,
		"process: setup start/bootstrap starts a local backend process using only the command shown below",
		"network: bootstrap polls the configured local backend health endpoint until it responds or times out",
	)
	if install {
		check.Disclosures = append(check.Disclosures,
			"network: --install downloads the documented OpenVibely installer over HTTPS",
			"filesystem: --install may create or replace files according to the backend installer",
		)
	} else {
		check.Disclosures = append(check.Disclosures, "filesystem: setup start/bootstrap without --install does not create files")
	}
	return check
}

func findLocalBackendStartCommand(platform string) setupCommandSpec {
	if platform != "windows" {
		if info, err := setupStat("./start.sh"); err == nil && !info.IsDir() {
			if info.Mode()&0111 != 0 {
				return setupCommandSpec{Name: "./start.sh", Display: "./start.sh", Description: "found source checkout start script ./start.sh", Found: true}
			}
			return setupCommandSpec{Display: "./start.sh", Description: "./start.sh exists but is not executable"}
		}
	}
	if path, err := setupLookPath("openvibely"); err == nil && strings.TrimSpace(path) != "" {
		return setupCommandSpec{Name: path, Display: "openvibely", Description: "found installed openvibely backend binary", Found: true}
	}
	if platform == "windows" {
		return setupCommandSpec{Display: "openvibely", Description: "missing installed openvibely backend binary on PATH"}
	}
	return setupCommandSpec{Display: "./start.sh or openvibely", Description: "missing executable ./start.sh in the current directory or installed openvibely backend binary on PATH"}
}

func findLocalBackendInstallCommands(platform string) []setupCommandSpec {
	if platform == "windows" {
		step := setupCommandSpec{Display: "powershell.exe -NoProfile -ExecutionPolicy Bypass -Command <documented installer>", Description: "missing powershell.exe for documented Windows installer"}
		if path, err := setupLookPath("powershell.exe"); err == nil && strings.TrimSpace(path) != "" {
			step.Name = path
			step.Found = true
			step.Description = "found powershell.exe for documented Windows installer"
		}
		return []setupCommandSpec{step}
	}
	steps := []setupCommandSpec{
		{Display: "curl -fsSL " + backendInstallerUnixURL, Description: "missing curl for documented installer download"},
		{Display: "bash -s -- --variant binary", Description: "missing bash for documented installer execution"},
	}
	if path, err := setupLookPath("curl"); err == nil && strings.TrimSpace(path) != "" {
		steps[0].Name = path
		steps[0].Found = true
		steps[0].Description = "found curl for documented installer download"
	}
	if path, err := setupLookPath("bash"); err == nil && strings.TrimSpace(path) != "" {
		steps[1].Name = path
		steps[1].Found = true
		steps[1].Description = "found bash for documented installer execution"
	}
	return steps
}

func renderSetupCheck(check setupCheckResult) string {
	var b strings.Builder
	b.WriteString("Setup check is read-only: it does not install, start processes, create projects, authenticate, modify files, or change server configuration.\n\n")
	fmt.Fprintf(&b, "Configured server: %s\n", serverURLDisplay(check.BaseURL))
	fmt.Fprintf(&b, "Platform: %s\n", sanitizeAutomationDetailText(check.Platform))
	if check.CWD != "" {
		fmt.Fprintf(&b, "Current directory: %s\n", check.CWD)
	}
	if check.InvalidURL {
		b.WriteString("Status: blocked by invalid server URL.\n")
		b.WriteString(InvalidServerURLMessage(check.BaseURL))
		return strings.TrimRight(b.String(), "\n")
	}
	if check.Remote {
		b.WriteString("Status: remote server configuration.\n")
		b.WriteString(check.RemoteMessage)
		return strings.TrimRight(b.String(), "\n")
	}
	b.WriteString("\nLocal prerequisites\n")
	writeSetupStep(&b, check.Start)
	for _, step := range check.InstallSteps {
		writeSetupStep(&b, step)
	}
	if len(check.Missing) == 0 {
		b.WriteString("\nStatus: ready for explicit setup start/bootstrap.\n")
	} else {
		b.WriteString("\nMissing\n")
		for _, missing := range check.Missing {
			fmt.Fprintf(&b, "  - %s\n", sanitizeAutomationDetailText(missing))
		}
	}
	b.WriteString("\nDisclosures\n")
	for _, disclosure := range check.Disclosures {
		fmt.Fprintf(&b, "  - %s\n", sanitizeAutomationDetailText(disclosure))
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeSetupStep(b *strings.Builder, step setupCommandSpec) {
	status := "missing"
	if step.Found {
		status = "ok"
	}
	fmt.Fprintf(b, "  - %s: %s (%s)\n", status, sanitizeAutomationDetailText(step.Display), sanitizeAutomationDetailText(step.Description))
}

func setupConfirmationMessage(check setupCheckResult, opts setupOptions) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Run setup %s%s for local backend %s? ", opts.action, setupInstallSuffix(opts), serverURLDisplay(check.BaseURL))
	b.WriteString("Effects: ")
	if opts.install {
		b.WriteString("download and run the documented installer, which may create or replace backend files; ")
	}
	b.WriteString("start process `")
	b.WriteString(sanitizeAutomationDetailText(check.Start.Display))
	b.WriteString("`; poll local backend health; no credentials sent; no projects created; server URL unchanged. Type 'yes' to confirm or Esc to cancel.")
	return b.String()
}

func runSetupBootstrap(ctx context.Context, c *client.Client, check setupCheckResult, opts setupOptions) (string, error) {
	var b strings.Builder
	b.WriteString("Local backend setup started after confirmation.\n")
	b.WriteString("Disclosed effects:\n")
	for _, disclosure := range check.Disclosures {
		fmt.Fprintf(&b, "  - %s\n", sanitizeAutomationDetailText(disclosure))
	}
	if opts.install {
		b.WriteString("\nInstalling local backend with documented installer...\n")
		if err := setupRunInstaller(ctx, check.Platform, check.InstallSteps); err != nil {
			return b.String(), err
		}
		b.WriteString("Installer completed.\n")
		check.Start = findLocalBackendStartCommand(check.Platform)
		if !check.Start.Found {
			return b.String(), fmt.Errorf("setup bootstrap could not find a local backend start command after installer completed: %s", check.Start.Description)
		}
	}
	fmt.Fprintf(&b, "\nStarting local backend with: %s\n", sanitizeAutomationDetailText(check.Start.Display))
	if err := setupStartProcess(ctx, check.Start); err != nil {
		return b.String(), err
	}
	b.WriteString("Start command launched. Waiting for backend health...\n")
	next, err := waitForSetupHealth(ctx, c, setupHealthWaitTimeout, setupHealthPoll)
	if err != nil {
		return b.String(), err
	}
	b.WriteString("Backend health check succeeded.\n")
	b.WriteString(next)
	return strings.TrimRight(b.String(), "\n"), nil
}

func runLocalBackendInstaller(ctx context.Context, platform string, steps []setupCommandSpec) error {
	if platform == "windows" {
		if len(steps) == 0 || !steps[0].Found {
			return errors.New("installer prerequisite missing: powershell.exe")
		}
		cmd := setupExecCommand(ctx, steps[0].Name, "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", "& ([scriptblock]::Create((irm "+backendInstallerWindowsURL+"))) -Variant binary")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("installer failed: %s", firstNonEmpty(safeConnectionDiagnosticText(stderr.String()), safeConnectionDiagnostic(err)))
		}
		return nil
	}
	if len(steps) < 2 || !steps[0].Found || !steps[1].Found {
		return errors.New("installer prerequisites missing: curl and bash are required")
	}
	curlCmd := setupExecCommand(ctx, steps[0].Name, "-fsSL", backendInstallerUnixURL)
	bashCmd := setupExecCommand(ctx, steps[1].Name, "-s", "--", "--variant", "binary")
	reader, writer := io.Pipe()
	var stderr bytes.Buffer
	curlCmd.Stdout = writer
	curlCmd.Stderr = &stderr
	bashCmd.Stdin = reader
	bashCmd.Stderr = &stderr
	if err := bashCmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return fmt.Errorf("starting installer shell: %w", err)
	}
	if err := curlCmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		_ = bashCmd.Process.Kill()
		_, _ = bashCmd.Process.Wait()
		return fmt.Errorf("starting installer download: %w", err)
	}
	curlErr := curlCmd.Wait()
	_ = writer.Close()
	bashErr := bashCmd.Wait()
	_ = reader.Close()
	if curlErr != nil {
		return fmt.Errorf("installer download failed: %s", firstNonEmpty(safeConnectionDiagnosticText(stderr.String()), safeConnectionDiagnostic(curlErr)))
	}
	if bashErr != nil {
		return fmt.Errorf("installer failed: %s", firstNonEmpty(safeConnectionDiagnosticText(stderr.String()), safeConnectionDiagnostic(bashErr)))
	}
	return nil
}

func startLocalBackendProcess(ctx context.Context, spec setupCommandSpec) error {
	if strings.TrimSpace(spec.Name) == "" {
		return errors.New("no start command available")
	}
	name := spec.Name
	if spec.Display == "./start.sh" {
		name = filepath.Clean("./start.sh")
	}
	cmd := setupExecCommand(ctx, name, spec.Args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting local backend: %w", err)
	}
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}
	return nil
}

func waitForSetupHealth(ctx context.Context, c *client.Client, timeout, interval time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = setupHealthWaitTimeout
	}
	if interval <= 0 {
		interval = setupHealthPoll
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := c.GetGlobalCapacity(probeCtx)
		cancel()
		if err == nil {
			return "Next: run /projects to select an existing project, or /projects create <name> <path> to create one.", nil
		}
		if client.IsAuthRequired(err) {
			return "Next: run /login to authenticate, then /projects to select or create a project.", nil
		}
		lastErr = err
		if !time.Now().Before(deadline) {
			return "", fmt.Errorf("backend did not become healthy at %s: %s", serverURLDisplay(c.BaseURL()), safeConnectionDiagnostic(lastErr))
		}
		wait := interval
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(wait):
		}
	}
}

func prefixLines(lines []string, prefix string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, prefix+sanitizeAutomationDetailText(line))
	}
	return out
}
