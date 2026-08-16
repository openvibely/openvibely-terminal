package tui

// Slash commands. Every command is "/resource [action] [args...]" and its
// output is rendered back into the chat transcript. With no action a resource
// lists itself; with an action it mutates and then re-lists.
//
//	/tasks                        list the board
//	/tasks run api refactor       run the task whose title matches
//	/alerts delete 1a2b           delete an alert
//	/skills add my-skill          create a skill
//
// Task/alert/skill/model/agent references accept an ID prefix or a
// case-insensitive substring of the name/title.

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// command is one entry in the registry.
type command struct {
	name    string
	aliases []string
	args    string   // argument hint, e.g. "<name>"
	actions []string // sub-actions for completion/help
	// usage lists the concrete syntax of each action, e.g.
	// "move <task> <backlog|active|completed>". Listing the action names alone
	// isn't enough to actually use a command, so /help <command> prints these.
	usage []string
	// examples holds concrete runnable invocations shown after the usage block
	// in /help <command> output, satisfying VISION.md "Help should include
	// examples, not only syntax."
	examples []string
	desc     string
	run   func(m Model, args []string) (Model, tea.Cmd)
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

// runCommand parses and executes a "/..." line.
func (m Model) runCommand(line string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "/"))
	if len(fields) == 0 {
		return m, nil
	}
	c := lookupCommand(fields[0])
	if c == nil {
		m.append(entry{role: "error", text: fmt.Sprintf("unknown command %q — /help lists everything", fields[0])})
		return m, nil
	}
	m.busy = true
	newModel, cmd := c.run(m, fields[1:])
	return newModel, cmd
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
	if m.selectedID == "" {
		m.busy = false
		m.append(entry{role: "error", text: "no project selected — use /project <name>"})
		return m, nil, false
	}
	return m, nil, true
}

// matchRef finds an item by reference, preferring the most specific match:
// an exact ID or name, then an ID/name prefix, then a name substring. Each
// tier is only consulted when the previous one found nothing, and a tier that
// matches several items reports them rather than guessing — so an exact name
// is never shadowed by a longer one that merely contains it.
func matchRef[T any](items []T, ref string, id func(T) string, name func(T) string) (T, error) {
	var zero T
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return zero, fmt.Errorf("missing id or name")
	}
	lower := strings.ToLower(ref)

	for _, it := range items {
		if strings.EqualFold(id(it), ref) || strings.EqualFold(name(it), ref) {
			return it, nil
		}
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
			var names []string
			for _, h := range hits {
				names = append(names, name(h))
			}
			return zero, fmt.Errorf("%q is ambiguous: %s — use the full name or ID",
				ref, strings.Join(names, ", "))
		}
	}
	return zero, fmt.Errorf("nothing matches %q", ref)
}
