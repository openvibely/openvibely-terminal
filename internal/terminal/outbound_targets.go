package terminal

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

var outboundTargetOptionNames = map[string]string{
	"--platform":        "platform",
	"--kind":            "target_kind",
	"--target-kind":     "target_kind",
	"--type":            "target_kind",
	"--name":            "name",
	"--target":          "destination",
	"--target-id":       "destination",
	"--destination":     "destination",
	"--thread":          "thread_id",
	"--thread-id":       "thread_id",
	"--topic":           "thread_id",
	"--topic-id":        "thread_id",
	"--home":            "home",
	"--is-home":         "home",
	"--subject":         "default_subject",
	"--default-subject": "default_subject",
}

var outboundTargetPlatforms = []string{"slack", "telegram", "email", "discord", "x"}
var outboundTargetActions = []string{"list", "show", "add", "edit", "test", "remove", "policy"}

func outboundTargetOptionCompletionValues() []string {
	return []string{"--platform", "--kind", "--target-id", "--name", "--thread-id", "--home", "--default-subject"}
}

func isOutboundTargetsAction(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "targets", "outbound-targets", "outbound":
		return true
	default:
		return false
	}
}

func outboundTargetsUsage(action string) string {
	return commandUsage("channels", "targets "+action)
}

func validateOutboundTargetsArgs(args []string) error {
	if len(args) == 0 {
		return nil
	}
	action := strings.ToLower(strings.TrimSpace(args[0]))
	switch action {
	case "list":
		if len(args) == 1 {
			return nil
		}
	case "show", "test", "remove":
		if action == "test" && len(args) >= 2 && strings.EqualFold(args[1], "draft") {
			_, err := parseOutboundTargetAdd(args[2:])
			return err
		}
		if len(args) == 1 || len(args) == 2 {
			return nil
		}
	case "add":
		_, err := parseOutboundTargetAdd(args[1:])
		return err
	case "edit":
		if len(args) < 2 {
			return nil
		}
		_, _, err := parseOutboundTargetEdit(args[1:])
		return err
	case "policy":
		if len(args) == 1 {
			return nil
		}
		if len(args) == 2 {
			switch strings.ToLower(args[1]) {
			case "show", "on", "off", "true", "false", "enable", "disable":
				return nil
			}
		}
	}
	return errors.New(outboundTargetsUsage(""))
}

func parseOutboundTargetOptions(args []string) ([]string, map[string]string, map[string]bool, error) {
	positional := make([]string, 0)
	values := make(map[string]string)
	provided := make(map[string]bool)
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "" {
			return nil, nil, nil, errors.New("outbound target operands must not be empty")
		}
		if !strings.HasPrefix(arg, "-") || isOutboundTargetNegativeNumeric(arg) {
			positional = append(positional, args[i])
			continue
		}
		name, value, hasValue := arg, "", false
		if equals := strings.IndexByte(arg, '='); equals >= 0 {
			name, value, hasValue = arg[:equals], arg[equals+1:], true
		}
		canonical, ok := outboundTargetOptionNames[strings.ToLower(name)]
		if !ok {
			if strings.EqualFold(name, "--no-home") && !hasValue {
				canonical, value, hasValue = "home", "false", true
			} else {
				return nil, nil, nil, fmt.Errorf("unknown outbound target option %q", sanitizeAutomationDetailText(name))
			}
		}
		if provided[canonical] {
			return nil, nil, nil, fmt.Errorf("outbound target option %s was supplied more than once", name)
		}
		if canonical == "home" && !hasValue {
			if i+1 < len(args) && (strings.EqualFold(args[i+1], "true") || strings.EqualFold(args[i+1], "false")) {
				i++
				value, hasValue = args[i], true
			} else {
				value, hasValue = "true", true
			}
		} else if !hasValue {
			if i+1 >= len(args) || (strings.HasPrefix(args[i+1], "-") && !isOutboundTargetNegativeNumeric(args[i+1])) {
				return nil, nil, nil, fmt.Errorf("outbound target option %s requires a value", name)
			}
			i++
			value, hasValue = args[i], true
		}
		if canonical == "home" {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return nil, nil, nil, errors.New("outbound target home must be true or false")
			}
			value = strconv.FormatBool(parsed)
		}
		values[canonical] = value
		provided[canonical] = true
	}
	return positional, values, provided, nil
}

func isOutboundTargetNegativeNumeric(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 2 || value[0] != '-' {
		return false
	}
	for _, r := range value[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func parseOutboundTargetAdd(args []string) (client.OutboundTarget, error) {
	positional, values, _, err := parseOutboundTargetOptions(args)
	if err != nil {
		return client.OutboundTarget{}, err
	}
	if len(positional) > 2 {
		return client.OutboundTarget{}, errors.New("outbound target add accepts <platform> <destination> plus options")
	}
	platform := values["platform"]
	if len(positional) > 0 {
		if platform != "" {
			return client.OutboundTarget{}, errors.New("outbound target platform was supplied more than once")
		}
		platform = positional[0]
	}
	destination := values["destination"]
	if len(positional) > 1 {
		if destination != "" {
			return client.OutboundTarget{}, errors.New("outbound target destination was supplied more than once")
		}
		destination = positional[1]
	}
	platform = strings.ToLower(strings.TrimSpace(platform))
	if !isOutboundTargetPlatform(platform) {
		return client.OutboundTarget{}, errors.New("outbound target platform must be slack, telegram, email, discord, or x")
	}
	if strings.TrimSpace(destination) == "" {
		return client.OutboundTarget{}, errors.New("outbound target destination is required")
	}
	home, _ := strconv.ParseBool(values["home"])
	return client.OutboundTarget{
		Platform: platform, TargetKind: strings.ToLower(strings.TrimSpace(values["target_kind"])),
		Name: strings.TrimSpace(values["name"]), Destination: strings.TrimSpace(destination),
		TargetID: strings.TrimSpace(destination), ThreadID: strings.TrimSpace(values["thread_id"]),
		Home: home, DefaultSubject: strings.TrimSpace(values["default_subject"]),
	}, nil
}

func parseOutboundTargetEdit(args []string) (string, map[string]string, error) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return "", nil, errors.New("outbound target edit requires an ID or name")
	}
	positional, values, provided, err := parseOutboundTargetOptions(args[1:])
	if err != nil {
		return "", nil, err
	}
	if len(positional) > 0 {
		return "", nil, errors.New("outbound target edit accepts one ID or name plus options")
	}
	if len(provided) == 0 {
		return "", nil, errors.New("outbound target edit requires at least one option")
	}
	return strings.TrimSpace(args[0]), values, nil
}

func isOutboundTargetPlatform(platform string) bool {
	platform = strings.ToLower(strings.TrimSpace(platform))
	for _, supported := range outboundTargetPlatforms {
		if platform == supported {
			return true
		}
	}
	return false
}

func outboundTargetName(target client.OutboundTarget) string {
	name := strings.TrimSpace(target.Name)
	if name == "" {
		name = strings.TrimSpace(target.Destination)
	}
	if name == "" {
		name = "unnamed target"
	}
	return sanitizeAutomationDetailText(name)
}

func outboundTargetMatchesRef(target client.OutboundTarget) string {
	if strings.TrimSpace(target.Name) != "" {
		return target.Name
	}
	return target.Destination
}

func resolveOutboundTarget(targets []client.OutboundTarget, ref string) (client.OutboundTarget, error) {
	return matchRefWithDisplay(targets, ref,
		func(target client.OutboundTarget) string { return target.ID },
		outboundTargetMatchesRef, sanitizeAutomationDetailText)
}

func outboundTargetsEqual(left, right client.OutboundTarget) bool {
	return left.ID == right.ID && left.ProjectID == right.ProjectID &&
		left.Platform == right.Platform && left.TargetKind == right.TargetKind &&
		left.Name == right.Name && left.Destination == right.Destination &&
		left.TargetID == right.TargetID && left.ThreadID == right.ThreadID &&
		left.Home == right.Home && left.DefaultSubject == right.DefaultSubject
}

func renderOutboundTargets(targets []client.OutboundTarget) string {
	if len(targets) == 0 {
		return "No saved outbound targets."
	}
	var b strings.Builder
	b.WriteString("PLATFORM   KIND      NAME              DESTINATION          THREAD/TOPIC   HOME   DEFAULT SUBJECT\n")
	for _, target := range targets {
		name := firstNonEmpty(sanitizeAutomationDetailText(target.Name), "(unnamed)")
		home := "-"
		if target.Home {
			home = "Home"
		}
		fmt.Fprintf(&b, "%-10s %-9s %-17s %-20s %-14s %-6s %s\n",
			sanitizeAutomationDetailText(target.Platform),
			sanitizeAutomationDetailText(target.TargetKind), name,
			sanitizeAutomationDetailText(target.Destination),
			sanitizeAutomationDetailText(target.ThreadID), home,
			sanitizeAutomationDetailText(target.DefaultSubject))
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderOutboundTargetPolicy(allowed bool) string {
	if allowed {
		return "explicit unsaved targets: allowed"
	}
	return "explicit unsaved targets: blocked (saved targets only)"
}

type outboundTargetActionJSON struct {
	Action string                `json:"action"`
	Target client.OutboundTarget `json:"target"`
}

type outboundTargetTestJSON struct {
	Status string `json:"status"`
}

type outboundTargetPolicyJSON struct {
	ExplicitUnsavedTargetsAllowed bool `json:"explicit_unsaved_targets_allowed"`
}

func outboundTargetActionOutput(action string, target client.OutboundTarget) (string, error) {
	return marshalJSON(outboundTargetActionJSON{Action: action, Target: target})
}

func outboundTargetTestOutput(target client.OutboundTarget, sent bool) (string, error) {
	status := "failed"
	if sent {
		status = "sent"
	}
	if jsonMode {
		return marshalJSON(outboundTargetTestJSON{Status: status})
	}
	return fmt.Sprintf("outbound target test for %q: %s", outboundTargetName(target), status), nil
}

func outboundTargetMutationStatus(action string, target client.OutboundTarget) string {
	verb := ""
	switch action {
	case "add":
		verb = "added"
	case "edit":
		verb = "edited"
	case "remove":
		verb = "removed"
	default:
		return ""
	}
	return verb + " outbound target " + outboundTargetName(target)
}

func outboundTargetMutationOutput(ctx context.Context, c *client.Client, projectID, action, status string, target client.OutboundTarget) (string, error) {
	targets, err := c.ListOutboundTargets(ctx, projectID)
	if err != nil {
		if jsonMode {
			return outboundTargetActionOutput(action, target)
		}
		return status, nil
	}
	for _, candidate := range targets {
		if target.ID != "" && candidate.ID == target.ID {
			target = candidate
			break
		}
	}
	if target.ID == "" {
		var candidate client.OutboundTarget
		matches := 0
		for _, current := range targets {
			if strings.EqualFold(current.Platform, target.Platform) &&
				strings.EqualFold(current.TargetKind, target.TargetKind) &&
				strings.EqualFold(current.Destination, target.Destination) &&
				current.ThreadID == target.ThreadID {
				candidate = current
				matches++
			}
		}
		if matches == 1 {
			target = candidate
			if canonicalStatus := outboundTargetMutationStatus(action, target); canonicalStatus != "" {
				status = canonicalStatus
			}
		}
	}
	if jsonMode {
		return outboundTargetActionOutput(action, target)
	}
	return status + "\n\n" + renderOutboundTargets(targets), nil
}

func outboundTargetSelector(m Model, action string) (Model, tea.Cmd) {
	c, projectID := m.client, m.selectedID
	prefillSuffix := ""
	if action == "edit" {
		prefillSuffix = " "
	}
	return selectorOr(m, outboundTargetsUsage(action), selectorForWithSuffix(
		"Outbound targets", "channels targets "+action, "no saved outbound targets configured", prefillSuffix,
		func(ctx context.Context) ([]selectorItem, error) {
			targets, err := c.ListOutboundTargets(ctx, projectID)
			if err != nil {
				return nil, err
			}
			items := make([]selectorItem, 0, len(targets))
			for _, target := range targets {
				target := target
				item := selectorItem{
					ref: target.ID, label: outboundTargetName(target),
					detail: sanitizeAutomationDetailText(target.Platform + " " + target.TargetKind),
				}
				if action != "edit" {
					item.dispatch = func(m Model) (Model, tea.Cmd) {
						switch action {
						case "show":
							return m, m.run("Channels", cmdTimeout, func(context.Context) (string, error) {
								if jsonMode {
									return marshalJSON(target)
								}
								return renderOutboundTargets([]client.OutboundTarget{target}), nil
							})
						case "test":
							return m, m.run("Channels", cmdTimeout, func(ctx context.Context) (string, error) {
								sent, err := c.TestOutboundTarget(ctx, projectID, target.ID)
								if err != nil {
									return "", err
								}
								return outboundTargetTestOutput(target, sent)
							})
						case "remove":
							return confirmOutboundTargetRemoval(m, projectID, target)
						}
						return m, nil
					}
				}
				items = append(items, item)
			}
			return items, nil
		}))
}

func outboundTargetRemovalTarget(baseCtx context.Context, c *client.Client, projectID, ref string) tea.Cmd {
	return func() tea.Msg {
		if baseCtx == nil {
			baseCtx = context.Background()
		}
		ctx, cancel := context.WithTimeout(baseCtx, cmdTimeout)
		defer cancel()
		targets, err := c.ListOutboundTargets(ctx, projectID)
		var target client.OutboundTarget
		if err == nil {
			target, err = resolveOutboundTarget(targets, ref)
		}
		return outboundTargetRemovalTargetMsg{projectID: projectID, target: target, err: err}
	}
}

func confirmOutboundTargetRemoval(m Model, projectID string, target client.OutboundTarget) (Model, tea.Cmd) {
	projectID = strings.TrimSpace(projectID)
	capturedID := strings.TrimSpace(target.ID)
	label := outboundTargetName(target)
	cmd := m.run("Channels", cmdTimeout, func(ctx context.Context) (string, error) {
		current, err := m.client.GetOutboundTargets(ctx, projectID)
		if err != nil {
			return "", err
		}
		var currentTarget client.OutboundTarget
		found := false
		for _, candidate := range current.Targets {
			if candidate.ID == capturedID && candidate.ProjectID == projectID {
				currentTarget = candidate
				found = true
				break
			}
		}
		if !found {
			return "", errors.New("outbound target reference is stale or does not belong to selected project")
		}
		if !outboundTargetsEqual(target, currentTarget) {
			return "", errors.New("outbound target reference is stale or does not belong to selected project")
		}
		remaining := make([]client.OutboundTarget, 0, len(current.Targets)-1)
		for _, candidate := range current.Targets {
			if candidate.ID != capturedID {
				remaining = append(remaining, candidate)
			}
		}
		if err := m.client.SaveOutboundTargets(ctx, projectID, remaining, current.ExplicitUnsavedTargetsAllowed); err != nil {
			return "", err
		}
		return outboundTargetMutationOutput(ctx, m.client, projectID, "remove", "removed outbound target "+label, currentTarget)
	})
	return confirmOr(m,
		fmt.Sprintf("Remove outbound target %q? Type 'yes' to confirm or Esc to cancel.", label),
		fmt.Sprintf("use --force to confirm removal of outbound target %q", label), cmd)
}

func outboundTargetPolicyOutput(ctx context.Context, c *client.Client, projectID string, status string, allowed bool) (string, error) {
	// A final read makes successful policy writes visible. A failed refresh is
	// non-fatal and falls back to the state just submitted.
	current, err := c.GetOutboundTargetPolicy(ctx, projectID)
	if err != nil {
		if jsonMode {
			return marshalJSON(outboundTargetPolicyJSON{ExplicitUnsavedTargetsAllowed: allowed})
		}
		return status, nil
	}
	allowed = current
	if jsonMode {
		return marshalJSON(outboundTargetPolicyJSON{ExplicitUnsavedTargetsAllowed: allowed})
	}
	return status + "\n" + renderOutboundTargetPolicy(allowed), nil
}

func applyOutboundTargetEdit(target client.OutboundTarget, values map[string]string) client.OutboundTarget {
	if value, ok := values["platform"]; ok {
		target.Platform = strings.ToLower(strings.TrimSpace(value))
	}
	if value, ok := values["target_kind"]; ok {
		target.TargetKind = strings.ToLower(strings.TrimSpace(value))
	}
	if value, ok := values["name"]; ok {
		target.Name = strings.TrimSpace(value)
	}
	if value, ok := values["destination"]; ok {
		target.Destination = strings.TrimSpace(value)
		target.TargetID = target.Destination
	}
	if value, ok := values["thread_id"]; ok {
		target.ThreadID = strings.TrimSpace(value)
	}
	if value, ok := values["home"]; ok {
		target.Home, _ = strconv.ParseBool(value)
	}
	if value, ok := values["default_subject"]; ok {
		target.DefaultSubject = strings.TrimSpace(value)
	}
	return target
}

func runOutboundTargets(m Model, args []string) (Model, tea.Cmd) {
	c, projectID := m.client, m.selectedID
	action := ""
	if len(args) > 0 {
		action = strings.ToLower(strings.TrimSpace(args[0]))
	}
	if action == "" || action == "list" {
		return m, m.run("Channels", cmdTimeout, func(ctx context.Context) (string, error) {
			page, err := c.GetOutboundTargets(ctx, projectID)
			if err != nil {
				return "", err
			}
			if jsonMode {
				return marshalJSON(page.Targets)
			}
			return renderOutboundTargets(page.Targets), nil
		})
	}
	if action == "policy" {
		policyAction := "show"
		if len(args) == 2 {
			policyAction = strings.ToLower(args[1])
		}
		if policyAction == "show" {
			return m, m.run("Channels", cmdTimeout, func(ctx context.Context) (string, error) {
				allowed, err := c.GetOutboundTargetPolicy(ctx, projectID)
				if err != nil {
					return "", err
				}
				if jsonMode {
					return marshalJSON(outboundTargetPolicyJSON{ExplicitUnsavedTargetsAllowed: allowed})
				}
				return renderOutboundTargetPolicy(allowed), nil
			})
		}
		allowed := policyAction == "on" || policyAction == "true" || policyAction == "enable"
		return m, m.run("Channels", cmdTimeout, func(ctx context.Context) (string, error) {
			if err := c.SetOutboundTargetPolicy(ctx, projectID, allowed); err != nil {
				return "", err
			}
			return outboundTargetPolicyOutput(ctx, c, projectID, "updated outbound target policy", allowed)
		})
	}
	if action == "show" || action == "test" || action == "remove" || action == "edit" {
		if len(args) == 1 {
			return outboundTargetSelector(m, action)
		}
	}
	if action == "test" && len(args) >= 2 && strings.EqualFold(args[1], "draft") {
		target, err := parseOutboundTargetAdd(args[2:])
		if err != nil {
			return m, errCmd(err.Error())
		}
		return m, m.run("Channels", cmdTimeout, func(ctx context.Context) (string, error) {
			sent, err := c.TestOutboundTargetDraft(ctx, projectID, target)
			if err != nil {
				return "", err
			}
			return outboundTargetTestOutput(target, sent)
		})
	}
	if action == "add" {
		target, err := parseOutboundTargetAdd(args[1:])
		if err != nil {
			return m, errCmd(err.Error())
		}
		return m, m.run("Channels", cmdTimeout, func(ctx context.Context) (string, error) {
			page, err := c.GetOutboundTargets(ctx, projectID)
			if err != nil {
				return "", err
			}
			target.ProjectID = projectID
			targets := append(append([]client.OutboundTarget(nil), page.Targets...), target)
			if err := c.SaveOutboundTargets(ctx, projectID, targets, page.ExplicitUnsavedTargetsAllowed); err != nil {
				return "", err
			}
			return outboundTargetMutationOutput(ctx, c, projectID, "add", "added outbound target "+outboundTargetName(target), target)
		})
	}
	if action == "edit" {
		ref, values, err := parseOutboundTargetEdit(args[1:])
		if err != nil {
			return m, errCmd(err.Error())
		}
		return m, m.run("Channels", cmdTimeout, func(ctx context.Context) (string, error) {
			page, err := c.GetOutboundTargets(ctx, projectID)
			if err != nil {
				return "", err
			}
			target, err := resolveOutboundTarget(page.Targets, ref)
			if err != nil {
				return "", err
			}
			updated := applyOutboundTargetEdit(target, values)
			for i := range page.Targets {
				if page.Targets[i].ID == target.ID {
					page.Targets[i] = updated
					break
				}
			}
			if err := c.SaveOutboundTargets(ctx, projectID, page.Targets, page.ExplicitUnsavedTargetsAllowed); err != nil {
				return "", err
			}
			return outboundTargetMutationOutput(ctx, c, projectID, "edit", "edited outbound target "+outboundTargetName(updated), updated)
		})
	}
	if action == "show" || action == "test" {
		ref := strings.Join(args[1:], " ")
		return m, m.run("Channels", cmdTimeout, func(ctx context.Context) (string, error) {
			targets, err := c.ListOutboundTargets(ctx, projectID)
			if err != nil {
				return "", err
			}
			target, err := resolveOutboundTarget(targets, ref)
			if err != nil {
				return "", err
			}
			if action == "show" {
				if jsonMode {
					return marshalJSON(target)
				}
				return renderOutboundTargets([]client.OutboundTarget{target}), nil
			}
			sent, err := c.TestOutboundTarget(ctx, projectID, target.ID)
			if err != nil {
				return "", err
			}
			return outboundTargetTestOutput(target, sent)
		})
	}
	if action == "remove" {
		ref := strings.Join(args[1:], " ")
		return m, outboundTargetRemovalTarget(m.cliContext, c, projectID, ref)
	}
	return m, errCmd(outboundTargetsUsage(""))
}
