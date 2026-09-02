package tui

// Slash commands. Every command is "/resource [action] [args...]" and its
// output is rendered back into the chat transcript. With no action a resource
// lists itself; read-only show actions fetch detail, while mutation actions
// change the resource and may refresh its list.
//
//	/tasks                        list the board
//	/tasks run api refactor       run the task whose title matches
//	/alerts show 1a2b             inspect an alert's full context
//	/alerts delete 1a2b           delete an alert
//	/skills add my-skill          create a skill
//
// Task/alert/skill/model/agent references accept an ID prefix or a
// case-insensitive substring of the name/title.

import (
	"fmt"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-tui/internal/client"
)

// commandActionUsage is the canonical syntax for one action. The command name
// and action are kept separate from args so runtime usage messages and help
// lines cannot drift apart when an argument hint changes.
type commandActionUsage struct {
	action      string
	args        string
	description string
}

func (u commandActionUsage) syntax(commandName string) string {
	parts := []string{commandName, u.action}
	if u.args != "" {
		parts = append(parts, u.args)
	}
	return strings.Join(parts, " ")
}

func (u commandActionUsage) helpLine(commandName string) string {
	syntax := u.syntax(commandName)
	if u.description == "" {
		return syntax
	}
	const descriptionColumn = 43
	if len(syntax) >= descriptionColumn {
		return syntax + " " + u.description
	}
	return fmt.Sprintf("%-*s%s", descriptionColumn, syntax, u.description)
}

// command is one entry in the registry.
type command struct {
	name    string
	aliases []string
	args    string   // argument hint, e.g. "<name>"
	actions []string // sub-actions for completion/help
	// usage holds static usage/help lines. Action-specific syntax shared with
	// runtime validation lives in actionUsages below.
	usage []string
	// actionUsages is the canonical syntax for runtime validation messages and
	// the corresponding help lines for high-churn action arguments.
	actionUsages []commandActionUsage
	// examples holds concrete runnable invocations shown after the usage block
	// in /help <command> output, satisfying VISION.md "Help should include
	// examples, not only syntax."
	examples []string
	desc     string
	run      func(m Model, args []string) (Model, tea.Cmd)
}

func (c command) actionSyntax(action string) string {
	for _, usage := range c.actionUsages {
		if usage.action == action {
			return usage.syntax(c.name)
		}
	}
	return ""
}

// usageMessage formats a canonical action usage for the current output mode.
func (c command) usageMessage(action string) string {
	syntax := c.actionSyntax(action)
	if syntax == "" {
		syntax = strings.TrimSpace(c.name + " " + action)
	}
	return "usage: " + cmdPrefix + syntax
}

// commandUsage resolves registry metadata so callers in command handlers use
// the same action syntax that renderCommandHelp displays.
func commandUsage(commandName, action string) string {
	if c := lookupCommand(commandName); c != nil {
		return c.usageMessage(action)
	}
	return "usage: " + cmdPrefix + strings.TrimSpace(commandName+" "+action)
}

func (c command) matches(s string) bool {
	if c.name == s {
		return true
	}
	for _, a := range c.aliases {
		if a == s {
			return true
		}
	}
	return false
}

func (c command) hasPrefix(s string) bool {
	if strings.HasPrefix(c.name, s) {
		return true
	}
	for _, a := range c.aliases {
		if strings.HasPrefix(a, s) {
			return true
		}
	}
	return false
}

// label renders the command with its argument/action hint.
func (c command) label() string {
	out := cmdPrefix + c.name
	if len(c.actions) > 0 {
		out += " [" + strings.Join(c.actions, "|") + "]"
	}
	if c.args != "" {
		out += " " + c.args
	}
	return out
}

// summary is the short form used in the command table: the name and its
// argument hint, without the full action list (which is far too wide).
func (c command) summary() string {
	out := cmdPrefix + c.name
	if len(c.actions) > 0 {
		out += " [action]"
	}
	if c.args != "" {
		out += " " + c.args
	}
	return out
}

var commands []command

// cmdPrefix is how commands are written in help text: "/" in the chat window,
// empty in CLI mode where they're shell subcommands. Set by RunCLI so the
// examples help prints can be copied verbatim into whichever mode you're in.
var cmdPrefix = "/"

const projectCreateCommand = "projects create <name> <path>"

func projectCreationCommand() string {
	return cmdPrefix + projectCreateCommand
}

func noProjectsGuidance() string {
	return "no projects yet — use " + projectCreationCommand()
}

func lookupCommand(name string) *command {
	name = strings.ToLower(strings.TrimPrefix(name, "/"))
	for i := range commands {
		if commands[i].matches(name) {
			return &commands[i]
		}
	}
	return nil
}

// suggest returns commands whose name/alias starts with word.
func suggest(word string) []command {
	word = strings.ToLower(strings.TrimPrefix(word, "/"))
	var out []command
	for _, c := range commands {
		if word == "" || c.hasPrefix(word) {
			out = append(out, c)
		}
	}
	return out
}

// runCommand parses and executes an interactive "/..." line. Interactive
// input is one shell-like line, so quoted arguments are grouped and their
// matching delimiters are removed before command dispatch.
func (m Model) runCommand(line string) (tea.Model, tea.Cmd) {
	fields, err := tokenizeCommand(line)
	if err != nil {
		m.busy = false
		m.append(entry{role: "error", text: "parse error: " + err.Error()})
		return m, nil
	}
	return m.runCommandFields(fields)
}

// runCommandFields dispatches already-tokenized arguments. RunCLI uses this
// path because os.Args has already preserved shell argument boundaries; joining
// those arguments and tokenizing again would lose quoted task refs and paths.
func (m Model) runCommandFields(fields []string) (tea.Model, tea.Cmd) {
	if len(fields) == 0 {
		return m, nil
	}
	fields = append([]string(nil), fields...)
	fields[0] = strings.TrimPrefix(fields[0], "/")
	c := lookupCommand(fields[0])
	if c == nil {
		m.append(entry{role: "error", text: fmt.Sprintf("unknown command %q — /help lists everything", fields[0])})
		return m, nil
	}
	m.busy = true
	newModel, cmd := c.run(m, fields[1:])
	return newModel, withMessageGeneration(cmd, sessionGenerationOf(newModel), projectGenerationOf(newModel))
}

// tokenizeCommand groups whitespace inside matching single or double quotes,
// removes those delimiters, and preserves empty quoted arguments. A dangling
// quote is rejected before command lookup or any command side effect.
func tokenizeCommand(line string) ([]string, error) {
	line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "/"))
	var (
		fields       []string
		current      strings.Builder
		quoted       rune
		tokenStarted bool
	)
	flush := func() {
		if !tokenStarted {
			return
		}
		fields = append(fields, current.String())
		current.Reset()
		tokenStarted = false
	}

	for _, r := range line {
		if quoted != 0 {
			if r == quoted {
				quoted = 0
			} else {
				current.WriteRune(r)
			}
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quoted = r
			tokenStarted = true
		case unicode.IsSpace(r):
			flush()
		default:
			current.WriteRune(r)
			tokenStarted = true
		}
	}
	if quoted != 0 {
		name := "single"
		if quoted == '"' {
			name = "double"
		}
		return nil, fmt.Errorf("unmatched %s quote", name)
	}
	flush()
	return fields, nil
}

// refreshMenu recomputes the inline command menu from the current input.
func (m *Model) refreshMenu() {
	value := strings.TrimSpace(m.input.Value())
	if !strings.HasPrefix(value, "/") {
		m.menu = nil
		m.menuSel = 0
		return
	}
	// Once a full command plus a space is typed, the menu collapses to it so
	// its actions stay visible while arguments are typed.
	if strings.Contains(value, " ") {
		if c := lookupCommand(strings.Fields(value)[0]); c != nil {
			m.menu = []command{*c}
			m.menuSel = 0
			return
		}
		m.menu = nil
		return
	}
	m.menu = suggest(value)
	if m.menuSel >= len(m.menu) {
		m.menuSel = 0
	}
}

func completeSlashInput(value string, selected command) string {
	if !strings.HasPrefix(strings.TrimSpace(value), "/") {
		return value
	}

	fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(value), "/"))
	if len(fields) == 0 {
		return value
	}

	if len(fields) == 1 && !strings.Contains(strings.TrimSpace(value), " ") {
		out := "/" + selected.name
		if selected.args != "" || len(selected.actions) > 0 {
			out += " "
		}
		return out
	}

	c := lookupCommand(fields[0])
	if c == nil || len(c.actions) == 0 {
		return value
	}

	trailingSpace := strings.HasSuffix(value, " ")
	if len(fields) == 1 {
		return value
	}

	prefix := strings.ToLower(fields[1])
	matches := matchingActions(c.actions, prefix)
	if len(matches) != 1 {
		return value
	}

	fields[0] = c.name
	fields[1] = matches[0]
	out := "/" + strings.Join(fields, " ")
	if trailingSpace || len(fields) == 2 {
		out += " "
	}
	return out
}

func matchingActions(actions []string, prefix string) []string {
	var matches []string
	for _, action := range actions {
		if strings.HasPrefix(strings.ToLower(action), prefix) {
			matches = append(matches, action)
		}
	}
	return matches
}

// --- argument helpers ---

// splitAction pops the leading action word when it is one of the command's
// known actions; otherwise the whole slice is treated as arguments (so
// "/tasks some title" still works as a filter).
func splitAction(actions []string, args []string) (string, []string) {
	if len(args) == 0 {
		return "", nil
	}
	head := strings.ToLower(args[0])
	for _, a := range actions {
		if a == head {
			return head, args[1:]
		}
	}
	return "", args
}

// needProject returns an error command when no project is selected.
func (m Model) needProject() (Model, tea.Cmd, bool) {
	if m.authRequired {
		m.busy = false
		m.markAuthRequired()
		return m, nil, false
	}
	if m.selectedID == "" {
		m.busy = false
		m.append(entry{role: "error", text: "no project selected — use /project <name>"})
		return m, nil, false
	}
	return m, nil, true
}

// selectedProject returns the full backend project record for the active ID.
// Commands that need local project metadata must use this record rather than
// reconstructing a project from only the display name and ID.
func (m Model) selectedProject() client.Project {
	for _, project := range m.projects {
		if project.ID == m.selectedID {
			return project
		}
	}
	return client.Project{ID: m.selectedID, Name: m.selectedName}
}

// matchRef finds an item by reference, preferring the most specific match:
// a unique exact ID, then a unique exact name, then an ID/name prefix, then a
// name substring. Each tier is only consulted when the previous one found
// nothing, and a tier that matches several items reports them rather than
// guessing — so an exact name is never shadowed by a longer one that merely
// contains it.
func matchRef[T any](items []T, ref string, id func(T) string, name func(T) string) (T, error) {
	var zero T
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return zero, fmt.Errorf("missing id or name")
	}
	lower := strings.ToLower(ref)

	ambiguous := func(hits []T) error {
		var names []string
		for _, h := range hits {
			names = append(names, name(h))
		}
		return fmt.Errorf("%q is ambiguous: %s — use the full name or ID",
			ref, strings.Join(names, ", "))
	}

	// IDs are canonical references, so a unique exact ID takes precedence over
	// every name tier. Do not return from this loop: duplicate exact IDs must
	// not be resolved by listing order either.
	var exactIDs []T
	for _, it := range items {
		if strings.EqualFold(id(it), ref) {
			exactIDs = append(exactIDs, it)
		}
	}
	if len(exactIDs) == 1 {
		return exactIDs[0], nil
	}
	if len(exactIDs) > 1 {
		return zero, ambiguous(exactIDs)
	}

	// Exact names are a separate tier so duplicate case-insensitive names are
	// reported as ambiguous instead of selecting the first list item.
	var exactNames []T
	for _, it := range items {
		if strings.EqualFold(name(it), ref) {
			exactNames = append(exactNames, it)
		}
	}
	if len(exactNames) == 1 {
		return exactNames[0], nil
	}
	if len(exactNames) > 1 {
		return zero, ambiguous(exactNames)
	}

	for _, tier := range []func(T) bool{
		func(it T) bool {
			return strings.HasPrefix(strings.ToLower(id(it)), lower) ||
				strings.HasPrefix(strings.ToLower(name(it)), lower)
		},
		func(it T) bool { return strings.Contains(strings.ToLower(name(it)), lower) },
	} {
		var hits []T
		for _, it := range items {
			if tier(it) {
				hits = append(hits, it)
			}
		}
		if len(hits) == 1 {
			return hits[0], nil
		}
		if len(hits) > 1 {
			return zero, ambiguous(hits)
		}
	}
	return zero, fmt.Errorf("nothing matches %q", ref)
}
