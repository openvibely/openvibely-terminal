package terminal

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

type deleteConfirmationCase struct {
	name       string
	wantPrompt string
	wantCLI    string
	open       func(t *testing.T, m Model) (Model, tea.Cmd)
}

func deleteConfirmationCases() []deleteConfirmationCase {
	return []deleteConfirmationCase{
		{
			name:       "task",
			wantPrompt: `Delete task "Ship it (t-1)"? Type 'yes' to confirm or Esc to cancel.`,
			wantCLI:    `use --force to confirm deletion of task "Ship it (t-1)"`,
			open: func(t *testing.T, m Model) (Model, tea.Cmd) {
				return confirmTaskDeletion(m, "p1", client.Task{ID: "t-1", Title: "Ship it"})
			},
		},
		{
			name:       "schedule",
			wantPrompt: `Delete schedule "s-1"? Type 'yes' to confirm or Esc to cancel.`,
			wantCLI:    `use --force to confirm deletion of schedule "s-1"`,
			open: func(t *testing.T, m Model) (Model, tea.Cmd) {
				return confirmScheduleDeletion(m, "p1", client.ScheduleEntry{ScheduleID: "s-1"})
			},
		},
		{
			name:       "model",
			wantPrompt: `Delete model "GPT-4o"? Type 'yes' to confirm or Esc to cancel.`,
			wantCLI:    `use --force to confirm deletion of model "GPT-4o"`,
			open: func(t *testing.T, m Model) (Model, tea.Cmd) {
				return typeLine(t, m, "/models delete GPT-4o")
			},
		},
		{
			name:       "alert",
			wantPrompt: `Delete alert "Deploy"? Type 'yes' to confirm or Esc to cancel.`,
			wantCLI:    `use --force to confirm deletion of alert "Deploy"`,
			open: func(t *testing.T, m Model) (Model, tea.Cmd) {
				next, cmd := m.Update(alertDeleteTargetMsg{projectID: m.selectedID, alert: client.Alert{ID: "a-1", Title: "Deploy"}})
				return next.(Model), cmd
			},
		},
		{
			name:       "bulk alert",
			wantPrompt: `Delete 2 selected alerts: "Deploy" (a-1), "Build" (a-2)? Type 'yes' to confirm or Esc to cancel.`,
			wantCLI:    `use --force to confirm deletion of 2 selected alerts`,
			open: func(t *testing.T, m Model) (Model, tea.Cmd) {
				next, cmd := m.Update(alertBulkTargetMsg{projectID: m.selectedID, action: "delete-bulk", alerts: []client.Alert{
					{ID: "a-1", Title: "Deploy"}, {ID: "a-2", Title: "Build"},
				}})
				return next.(Model), cmd
			},
		},
	}
}

func deleteConfirmationModel(t *testing.T) Model {
	t.Helper()
	m := newTestModel(t)
	m.selectedID = "p1"
	m.selectedName = "Demo"
	return m
}

func TestDeleteConfirmationPromptsShareInstruction(t *testing.T) {
	if confirmCancelInstruction != "Type 'yes' to confirm or Esc to cancel." {
		t.Fatalf("confirmCancelInstruction = %q", confirmCancelInstruction)
	}
	for _, tc := range deleteConfirmationCases() {
		t.Run(tc.name, func(t *testing.T) {
			m, cmd := tc.open(t, deleteConfirmationModel(t))
			if cmd != nil {
				t.Fatal("TUI delete returned a command before confirmation")
			}
			if m.pendingConfirmation == nil {
				t.Fatalf("no pending confirmation:\n%s", transcript(m))
			}
			if got := m.pendingConfirmation.message; got != tc.wantPrompt {
				t.Fatalf("prompt = %q, want %q", got, tc.wantPrompt)
			}
			if !strings.HasSuffix(m.pendingConfirmation.message, " "+confirmCancelInstruction) {
				t.Fatalf("prompt %q does not end with shared instruction", m.pendingConfirmation.message)
			}
		})
	}
}

func TestDeleteConfirmationYesRunsAndEscCancels(t *testing.T) {
	for _, tc := range deleteConfirmationCases() {
		t.Run(tc.name+"/yes", func(t *testing.T) {
			m, _ := tc.open(t, deleteConfirmationModel(t))
			m, cmd := typeLine(t, m, "yes")
			if m.pendingConfirmation != nil {
				t.Fatal("confirmation still pending after yes")
			}
			if cmd == nil || !m.busy {
				t.Fatalf("yes did not release the parked delete command (cmd nil=%v busy=%v)", cmd == nil, m.busy)
			}
		})
		t.Run(tc.name+"/esc", func(t *testing.T) {
			m, _ := tc.open(t, deleteConfirmationModel(t))
			next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			m = next.(Model)
			if m.pendingConfirmation != nil {
				t.Fatal("Esc did not clear the confirmation")
			}
			if cmd != nil {
				t.Fatal("Esc returned a command")
			}
			if !strings.Contains(transcript(m), "cancelled") {
				t.Fatalf("Esc did not report cancellation:\n%s", transcript(m))
			}
		})
	}
}

func TestDeleteConfirmationForceGate(t *testing.T) {
	oldCLI, oldForce := cliMode, forceMode
	defer func() { cliMode, forceMode = oldCLI, oldForce }()
	for _, tc := range deleteConfirmationCases() {
		t.Run(tc.name+"/without force", func(t *testing.T) {
			cliMode, forceMode = true, false
			m, cmd := tc.open(t, deleteConfirmationModel(t))
			if m.pendingConfirmation != nil {
				t.Fatal("CLI mode opened an interactive confirmation")
			}
			if cmd == nil {
				t.Fatal("CLI mode without --force returned no command")
			}
			res, ok := cmd().(resultMsg)
			if !ok || res.err == nil || res.err.Error() != tc.wantCLI {
				t.Fatalf("CLI gate = %#v, want error %q", res, tc.wantCLI)
			}
		})
		t.Run(tc.name+"/with force", func(t *testing.T) {
			cliMode, forceMode = true, true
			m, cmd := tc.open(t, deleteConfirmationModel(t))
			if m.pendingConfirmation != nil {
				t.Fatal("--force opened an interactive confirmation")
			}
			if cmd == nil {
				t.Fatal("--force returned no delete command")
			}
		})
	}
}
