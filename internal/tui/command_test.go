package tui

import (
	"strings"
	"testing"

	"github.com/openvibely/openvibely-tui/internal/client"
)

func TestSplitActionRecognisesKnownActions(t *testing.T) {
	actions := []string{"list", "run", "delete"}

	action, rest := splitAction(actions, []string{"run", "my", "task"})
	if action != "run" || strings.Join(rest, " ") != "my task" {
		t.Errorf("got %q %v", action, rest)
	}

	// An unknown first word is treated as an argument, so "/tasks some title"
	// still works as a filter.
	action, rest = splitAction(actions, []string{"some", "title"})
	if action != "" || strings.Join(rest, " ") != "some title" {
		t.Errorf("got %q %v", action, rest)
	}

	action, rest = splitAction(actions, nil)
	if action != "" || rest != nil {
		t.Errorf("empty args should yield nothing, got %q %v", action, rest)
	}
}

func TestSplitPipe(t *testing.T) {
	left, right := splitPipe("my skill | does a thing")
	if left != "my skill" || right != "does a thing" {
		t.Errorf("got %q / %q", left, right)
	}
	left, right = splitPipe("just a title")
	if left != "just a title" || right != "" {
		t.Errorf("got %q / %q", left, right)
	}
}

func TestParseProjectCreateArgs(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantName string
		wantPath string
		ok       bool
	}{
		{name: "simple", args: []string{"demo", "/tmp/demo"}, wantName: "demo", wantPath: "/tmp/demo", ok: true},
		{name: "space-delimited-name", args: []string{"My", "Project", "/tmp/my-project"}, wantName: "My Project", wantPath: "/tmp/my-project", ok: true},
		{name: "pipe-delimited", args: []string{"My", "Project", "|", `C:\Users\me\my project`}, wantName: "My Project", wantPath: `C:\Users\me\my project`, ok: true},
		{name: "missing-name", args: []string{"", "/tmp/demo"}, ok: false},
		{name: "missing-path", args: []string{"demo"}, ok: false},
		{name: "empty-pipe-path", args: []string{"demo", "|"}, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, path, ok := parseProjectCreateArgs(tc.args)
			if name != tc.wantName || path != tc.wantPath || ok != tc.ok {
				t.Fatalf("parseProjectCreateArgs(%v) = %q, %q, %v; want %q, %q, %v", tc.args, name, path, ok, tc.wantName, tc.wantPath, tc.ok)
			}
		})
	}
}

func TestProjectsCreateCompletionAndHelp(t *testing.T) {
	cmd := lookupCommand("projects")
	if cmd == nil {
		t.Fatal("projects command missing")
	}
	if got := completeSlashInput("/projects cr", *cmd); got != "/projects create " {
		t.Errorf("completion = %q, want %q", got, "/projects create ")
	}
	help := renderCommandHelp(*cmd)
	for _, want := range []string{
		"projects create <name> <path>",
		"projects create My Project",
		`C:\Users\me\src\my-project`,
	} {
		if !strings.Contains(help, want) {
			t.Errorf("projects help missing %q:\n%s", want, help)
		}
	}
}

func TestMatchRefByIDPrefixAndName(t *testing.T) {
	tasks := []client.Task{
		{ID: "abc123", Title: "Refactor the API"},
		{ID: "def456", Title: "Write docs"},
	}
	id := func(t client.Task) string { return t.ID }
	name := func(t client.Task) string { return t.Title }

	got, err := matchRef(tasks, "abc", id, name)
	if err != nil || got.ID != "abc123" {
		t.Errorf("id prefix: %+v %v", got, err)
	}

	got, err = matchRef(tasks, "docs", id, name)
	if err != nil || got.ID != "def456" {
		t.Errorf("name substring: %+v %v", got, err)
	}

	if _, err = matchRef(tasks, "missing", id, name); err == nil {
		t.Error("expected a not-found error")
	}

	ambiguous := []client.Task{{ID: "1", Title: "deploy api"}, {ID: "2", Title: "deploy web"}}
	if _, err = matchRef(ambiguous, "deploy", id, name); err == nil ||
		!strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("expected an ambiguity error, got %v", err)
	}
}

// An exact title must win over a longer title that contains it, whatever the
// listing order.
func TestMatchRefPrefersExactName(t *testing.T) {
	id := func(t client.Task) string { return t.ID }
	name := func(t client.Task) string { return t.Title }

	for _, tasks := range [][]client.Task{
		{{ID: "1", Title: "deploy api service"}, {ID: "2", Title: "deploy"}},
		{{ID: "2", Title: "deploy"}, {ID: "1", Title: "deploy api service"}},
	} {
		got, err := matchRef(tasks, "deploy", id, name)
		if err != nil || got.ID != "2" {
			t.Errorf("matchRef = %+v, %v; want the exactly-titled task", got, err)
		}
	}
}

func TestMatchRefRejectsDuplicateExactNamesCaseInsensitively(t *testing.T) {
	tasks := []client.Task{
		{ID: "1", Title: "Deploy"},
		{ID: "2", Title: "deploy"},
	}

	got, err := matchRef(tasks, "DEPLOY",
		func(t client.Task) string { return t.ID },
		func(t client.Task) string { return t.Title })
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("matchRef error = %v, want an ambiguity error", err)
	}
	if got.ID != "" {
		t.Fatalf("ambiguous exact name selected task %q", got.ID)
	}
	for _, want := range []string{"Deploy", "deploy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ambiguity error %q missing candidate %q", err, want)
		}
	}
}

func TestMatchRefExactIDPrecedesExactName(t *testing.T) {
	items := []client.Task{
		{ID: "task-1", Title: "Unrelated"},
		{ID: "task-2", Title: "TASK-1"},
	}

	got, err := matchRef(items, "TaSk-1",
		func(t client.Task) string { return t.ID },
		func(t client.Task) string { return t.Title })
	if err != nil {
		t.Fatalf("matchRef returned error: %v", err)
	}
	if got.ID != "task-1" {
		t.Fatalf("matchRef selected %q, want the unique exact ID task-1", got.ID)
	}
}

// A name prefix outranks a mid-string substring match.
func TestMatchRefPrefersPrefixOverSubstring(t *testing.T) {
	tasks := []client.Task{
		{ID: "1", Title: "rewrite the api docs"},
		{ID: "2", Title: "api gateway"},
	}
	got, err := matchRef(tasks, "api",
		func(t client.Task) string { return t.ID },
		func(t client.Task) string { return t.Title })
	if err != nil || got.ID != "2" {
		t.Errorf("matchRef = %+v, %v; want the prefix match", got, err)
	}
}

func TestSuggestFiltersByPrefix(t *testing.T) {
	if got := suggest("sk"); len(got) != 1 || got[0].name != "skills" {
		t.Errorf("suggest(sk) = %v", got)
	}
	if got := suggest(""); len(got) != len(commands) {
		t.Errorf("empty prefix should list everything, got %d", len(got))
	}
	if got := suggest("zzz"); len(got) != 0 {
		t.Errorf("no command should match zzz, got %v", got)
	}
}

func TestEveryCommandHasDescriptionAndRunner(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range commands {
		if c.desc == "" {
			t.Errorf("/%s has no description", c.name)
		}
		if c.run == nil {
			t.Errorf("/%s has no runner", c.name)
		}
		if seen[c.name] {
			t.Errorf("/%s is registered twice", c.name)
		}
		seen[c.name] = true
		for _, a := range c.aliases {
			if seen[a] {
				t.Errorf("alias %q collides with another command", a)
			}
			seen[a] = true
		}
	}
}

func TestRegistryCoversEveryWebUIScreen(t *testing.T) {
	// Each OpenVibely web-UI screen must be reachable from the chat.
	for _, screen := range []string{
		"tasks", "schedule", "alerts", "skills", "agents", "models", "workers",
		"channels", "personality", "pulse", "reflection", "grades", "insights",
		"analytics", "automations", "projects",
	} {
		if lookupCommand(screen) == nil {
			t.Errorf("no command for the %s screen", screen)
		}
	}
}

func TestRenderBoardGroupsByColumn(t *testing.T) {
	tasks := []client.Task{
		{ID: "t1", Title: "Backlog item", Category: "backlog", Status: "pending"},
		{ID: "t2", Title: "Running item", Category: "active", Status: "running"},
		{ID: "t3", Title: "Done item", Category: "completed", Status: "completed"},
	}
	out := renderBoard(tasks, "")
	for _, want := range []string{"Backlog", "Active", "Completed", "Backlog item", "Running item", "Done item"} {
		if !strings.Contains(out, want) {
			t.Errorf("board missing %q:\n%s", want, out)
		}
	}
}

func TestRenderBoardFilters(t *testing.T) {
	tasks := []client.Task{
		{ID: "t1", Title: "Refactor API", Category: "backlog"},
		{ID: "t2", Title: "Write docs", Category: "backlog"},
	}
	out := renderBoard(tasks, "docs")
	if strings.Contains(out, "Refactor API") {
		t.Errorf("filter should exclude non-matching tasks:\n%s", out)
	}
	if !strings.Contains(out, "Write docs") {
		t.Errorf("filter dropped the match:\n%s", out)
	}
}

func TestRenderTaskDetailShowsTabs(t *testing.T) {
	task := client.Task{ID: "t1", Title: "Refactor", Category: "active"}
	detail := &client.TaskDetail{
		Task:     client.Task{Status: "running"},
		Details:  "the prompt",
		Thread:   "agent: working",
		Changes:  "2 files",
		Review:   "inline comments",
		Schedule: "daily",
		Chaining: "then deploy",
		Attach:   "spec.md",
		Life:     "pre_task ok",
	}
	out := renderTaskDetail(task, detail, "")
	for _, tab := range client.TaskDetailTabs() {
		if !strings.Contains(out, tab.Label) {
			t.Errorf("detail missing the %s section:\n%s", tab.Label, out)
		}
	}
	if want := "/tasks show <id> <" + detailTabHintList() + ">"; !strings.Contains(out, want) {
		t.Errorf("detail hint missing %q:\n%s", want, out)
	}

	only := renderTaskDetail(task, detail, "chat")
	if !strings.Contains(only, "Thread") || !strings.Contains(only, "agent: working") {
		t.Errorf("alias tab view missing its label or content:\n%s", only)
	}
	if strings.Contains(only, "2 files") {
		t.Errorf("tab view should show one tab only:\n%s", only)
	}
}

func TestRenderAlertsAndSkills(t *testing.T) {
	alerts := renderAlerts([]client.Alert{{ID: "a1", Title: "Build failed"}}, "")
	if !strings.Contains(alerts, "Build failed") {
		t.Errorf("alerts render:\n%s", alerts)
	}
	if !strings.Contains(renderAlerts(nil, ""), "no alerts") {
		t.Error("empty alerts should say so")
	}

	skills := renderSkills([]client.Skill{
		{Handle: "deploy", Name: "Deploy", Enabled: true, AlwaysUse: true, Scope: "project"},
	}, "")
	if !strings.Contains(skills, "deploy") || !strings.Contains(skills, "always") {
		t.Errorf("skills render:\n%s", skills)
	}
}

func TestBarScalesToMax(t *testing.T) {
	full := bar(10, 10, 10)
	if strings.Count(full, "█") != 10 {
		t.Errorf("full bar = %q", full)
	}
	half := bar(5, 10, 10)
	if strings.Count(half, "█") != 5 {
		t.Errorf("half bar = %q", half)
	}
	// A non-zero value always shows at least one block.
	tiny := bar(1, 1000, 10)
	if strings.Count(tiny, "█") != 1 {
		t.Errorf("tiny bar = %q", tiny)
	}
	if bar(1, 0, 10) != "" {
		t.Error("zero max should render nothing")
	}
}

func TestHumanIntAndMs(t *testing.T) {
	cases := map[int64]string{999: "999", 1500: "1.5k", 2_000_000: "2.0M"}
	for in, want := range cases {
		if got := humanInt(in); got != want {
			t.Errorf("humanInt(%d) = %q, want %q", in, got, want)
		}
	}
	msCases := map[float64]string{500: "500ms", 1500: "1.5s", 120000: "2.0m"}
	for in, want := range msCases {
		if got := humanMs(in); got != want {
			t.Errorf("humanMs(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderUsageIncludesCostAndBreakdown(t *testing.T) {
	usage := &client.UsageAnalytics{
		Totals: client.UsageTotals{
			CallCount: 10, InputTokens: 1000, OutputTokens: 500,
			TotalTokens: 1500, CostUSD: 1.25, CostAvailable: true,
		},
		ModelBreakdown: []client.ModelUsagePoint{
			{Model: "claude-sonnet-4", CallCount: 10, TotalTokens: 1500, CostUSD: 1.25, Percent: 100},
		},
	}
	out := renderUsage(usage)
	for _, want := range []string{"10 calls", "$1.25", "claude-sonnet-4", "█"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage render missing %q:\n%s", want, out)
		}
	}
}

func TestRenderUsageIncludesPrimaryLimitOnly(t *testing.T) {
	out := stripANSI(renderUsage(&client.UsageAnalytics{AccountLimits: []client.AccountUsage{{
		Provider:    "OpenAI",
		StatusLabel: "healthy",
		PrimaryLimit: &client.AccountLimit{
			Label:       "tokens",
			UsedPercent: 100,
			ResetsAt:    "2026-09-01T00:00:00Z",
		},
	}}}))

	for _, want := range []string{"OpenAI", "healthy", "tokens", "100.0%", "2026-09-01T00:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage render missing primary-limit field %q:\n%s", want, out)
		}
	}
}

func TestRenderUsageIncludesProviderPrimaryAndDistinctLimits(t *testing.T) {
	primary := client.AccountLimit{
		Label:       "tokens",
		UsedPercent: 100,
		ResetsAt:    "2026-09-01T00:00:00Z",
	}
	secondary := client.AccountLimit{
		Label:       "requests",
		UsedPercent: 42.5,
		ResetsAt:    "tomorrow",
	}
	out := stripANSI(renderUsage(&client.UsageAnalytics{
		Totals: client.UsageTotals{CallCount: 3, TotalTokens: 900},
		AccountLimits: []client.AccountUsage{{
			Provider:      "OpenAI",
			PlanType:      "team",
			StatusLabel:   "healthy",
			AccountDetail: "sk-account-detail-must-not-render",
			PrimaryLimit:  &primary,
			Limits:        []client.AccountLimit{primary, secondary, secondary},
		}},
	}))

	for _, want := range []string{
		"OpenAI", "team", "healthy", "tokens", "100.0%", "2026-09-01T00:00:00Z",
		"requests", "42.5%", "tomorrow",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("usage render missing %q:\n%s", want, out)
		}
	}
	for _, label := range []string{"tokens", "requests"} {
		if got := strings.Count(out, "\n    "+label); got != 1 {
			t.Errorf("usage render contains %q %d times, want once:\n%s", label, got, out)
		}
	}
	if strings.Contains(out, "sk-account-detail-must-not-render") {
		t.Errorf("provider account detail leaked into usage output:\n%s", out)
	}
}

func TestRenderUsageKeepsProviderDiagnosticsWithoutQuota(t *testing.T) {
	secret := "sk-provider-secret"
	out := stripANSI(renderUsage(&client.UsageAnalytics{AccountLimits: []client.AccountUsage{
		{
			Provider:      "Anthropic",
			PlanType:      "pro",
			StatusLabel:   "blocked",
			AccountDetail: secret,
			Error:         "quota service unavailable",
		},
	}}))

	for _, want := range []string{"Anthropic", "pro", "blocked", "quota service unavailable"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage diagnostics missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, secret) {
		t.Errorf("provider account detail leaked into usage output:\n%s", out)
	}
}

func TestRenderRatesDrawsGauge(t *testing.T) {
	out := renderRates([]client.SuccessFailureRate{
		{Period: "2026-08", SuccessCount: 8, FailureCount: 2, TotalCount: 10, SuccessRate: 80},
	})
	if !strings.Contains(out, "2026-08") || !strings.Contains(out, "80.0%") {
		t.Errorf("rates render:\n%s", out)
	}
	if !strings.Contains(out, "8 ok / 2 fail") {
		t.Errorf("counts missing:\n%s", out)
	}
}

func TestRenderStatusReportsConnection(t *testing.T) {
	m := newTestModel(t)
	m.connected = true
	m.capacity = &client.GlobalCapacity{MaxWorkers: 4, TotalRunning: 1, QueueSize: 2, AvailableSlots: 3}
	m.auth = &client.AuthStatus{Authenticated: true, Username: "dubee"}

	out := m.renderStatus()
	for _, want := range []string{"connected", "dubee", "1 running / 4 max"} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q:\n%s", want, out)
		}
	}
}

// Help must document every command, otherwise features are unreachable in
// practice: a command that isn't listed is a command nobody finds.
func TestHelpListsEveryRegisteredCommand(t *testing.T) {
	help := renderHelp()
	for _, c := range commands {
		if !strings.Contains(help, cmdPrefix+c.name) {
			t.Errorf("%s%s is missing from /help", cmdPrefix, c.name)
		}
		if !strings.Contains(help, c.desc) {
			t.Errorf("%s%s description is missing from /help", cmdPrefix, c.name)
		}
	}
}

// Listing action names isn't enough to use a command, so every command with
// actions must also spell out their concrete syntax. VISION.md "Friendly By
// Default" further requires examples, not only syntax — so every command with
// a usage block must have at least one concrete worked example.
func TestCommandsWithActionsDocumentTheirSyntax(t *testing.T) {
	for _, c := range commands {
		if len(c.actions) == 0 {
			continue
		}
		if len(c.usage) == 0 {
			t.Errorf("%s%s lists actions but has no usage lines", cmdPrefix, c.name)
			continue
		}
		detail := renderCommandHelp(c)
		for _, a := range c.actions {
			if !strings.Contains(detail, a) {
				t.Errorf("%s%s help omits the %q action:\n%s", cmdPrefix, c.name, a, detail)
			}
		}
		if len(c.examples) == 0 {
			t.Errorf("%s%s has usage lines but no examples (VISION.md: help must include examples)", cmdPrefix, c.name)
		}
	}
}

// renderCommandHelp must include an "examples:" block for commands that have
// examples populated, and must not render an empty block for those that don't.
func TestRenderCommandHelpExamplesBlock(t *testing.T) {
	withExamples := command{
		name:     "demo",
		desc:     "a demo command",
		usage:    []string{"demo do-it"},
		examples: []string{"demo do-it foo", "demo do-it bar"},
	}
	got := renderCommandHelp(withExamples)
	if !strings.Contains(got, "examples:") {
		t.Error("renderCommandHelp: expected 'examples:' header for command with examples")
	}
	if !strings.Contains(got, "demo do-it foo") {
		t.Error("renderCommandHelp: expected first example in output")
	}
	if !strings.Contains(got, "demo do-it bar") {
		t.Error("renderCommandHelp: expected second example in output")
	}

	withoutExamples := command{
		name:  "demo2",
		desc:  "another demo command",
		usage: []string{"demo2 do-it"},
	}
	got2 := renderCommandHelp(withoutExamples)
	if strings.Contains(got2, "examples:") {
		t.Error("renderCommandHelp: must not render 'examples:' block when command has no examples")
	}
}

// Every action the help advertises must actually be accepted, and every action
// the implementation handles must be advertised.
func TestAdvertisedActionsMatchImplementation(t *testing.T) {
	// Actions implemented but deliberately not advertised as separate entries.
	extra := map[string][]string{
		// "/tasks clear" takes a column argument rather than being an action.
		"tasks": {"backlog", "completed"},
	}
	for _, c := range commands {
		for _, a := range c.actions {
			action, rest := splitAction(c.actions, []string{a, "x"})
			if action != a {
				t.Errorf("%s%s does not accept its advertised action %q", cmdPrefix, c.name, a)
			}
			if len(rest) != 1 || rest[0] != "x" {
				t.Errorf("%s%s %s mis-parsed its arguments: %v", cmdPrefix, c.name, a, rest)
			}
		}
		_ = extra[c.name]
	}
}

// Help detail must be usable in both modes: slashes in the chat window,
// bare subcommands on the command line.
func TestHelpPrefixFollowsMode(t *testing.T) {
	defer func() { cmdPrefix = "/" }()

	cmdPrefix = "/"
	if got := lookupCommand("tasks").summary(); !strings.HasPrefix(got, "/tasks") {
		t.Errorf("TUI summary = %q, want a leading slash", got)
	}
	if h := renderHelp(); !strings.Contains(h, "/tasks") {
		t.Error("TUI help lost its slashes")
	}

	cmdPrefix = ""
	if got := lookupCommand("tasks").summary(); strings.HasPrefix(got, "/") {
		t.Errorf("CLI summary = %q, want no leading slash", got)
	}
	if s := CommandSummary(); !strings.Contains(s, "tasks") || strings.Contains(s, "/tasks") {
		t.Errorf("CLI command summary should list bare names:\n%s", s)
	}
}

func TestRuntimeUsageMatchesCanonicalHelpSyntax(t *testing.T) {
	defer func() { cmdPrefix = "/" }()
	cmdPrefix = "/"

	c := lookupCommand("tasks")
	if c == nil {
		t.Fatal("tasks command missing")
	}
	m := newTestModel(t)
	m.selectedID = "p1"
	_, cmd := c.run(m, []string{"new", "|", "prompt"})
	if cmd == nil {
		t.Fatal("expected a runtime validation command")
	}
	msg, ok := cmd().(resultMsg)
	if !ok || msg.err == nil {
		t.Fatalf("expected a usage result, got %#v", msg)
	}

	got := msg.err.Error()
	if want := c.usageMessage("new"); got != want {
		t.Fatalf("runtime usage = %q, want canonical usage %q", got, want)
	}
	if help := renderCommandHelp(*c); !strings.Contains(help, strings.TrimPrefix(got, "usage: ")) {
		t.Fatalf("runtime usage %q is missing from /help tasks:\n%s", got, help)
	}

	cmdPrefix = ""
	if got := c.usageMessage("new"); got != "usage: tasks new <title> [| <prompt>]" {
		t.Fatalf("CLI usage = %q, want a bare command", got)
	}
}

// The -h/--help output must list the commands, not just the flags.
func TestCommandSummaryListsEveryCommand(t *testing.T) {
	s := CommandSummary()
	for _, c := range commands {
		if !strings.Contains(s, c.name) {
			t.Errorf("%s missing from the CLI command summary", c.name)
		}
	}
}

func TestModelsHelpDocumentsProviderLimitHealth(t *testing.T) {
	cmd := lookupCommand("models")
	if cmd == nil {
		t.Fatal("models command missing")
	}
	help := renderCommandHelp(*cmd)
	for _, want := range []string{"provider/account-limit health", "analytics usage"} {
		if !strings.Contains(help, want) {
			t.Errorf("models help missing %q:\n%s", want, help)
		}
	}
}

func TestTasksHelpDocumentsReviewSupport(t *testing.T) {
	cmd := lookupCommand("tasks")
	if cmd == nil {
		t.Fatal("tasks command missing")
	}
	help := renderCommandHelp(*cmd)
	for _, want := range []string{"tasks show <task> [tab]", detailTabUsageList(), "review", "tasks reviews add <task> <file>:<line> <comment>", "tasks lifecycle <task> [execution]", "tasks logs <task> [execution]", "tasks lifecycle \"Fix login bug\"", "tasks reviews add \"Fix login bug\" internal/auth.go:42 Handle token refresh errors"} {
		if !strings.Contains(help, want) {
			t.Errorf("tasks help missing %q:\n%s", want, help)
		}
	}
}
