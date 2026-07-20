package app

import (
	"testing"

	"core/shared/clientui"
	"core/shared/config"

	tea "github.com/charmbracelet/bubbletea"
)

func TestWorkspaceChangePromptDefaultsToNo(t *testing.T) {
	m := newWorkspaceChangePromptModel("/tmp/old", "/tmp/new", "dark")
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.cursor)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*workspaceChangePromptModel)
	if m.result.Rebind {
		t.Fatal("expected default enter action to return to picker")
	}
}

func TestWorkspaceChangePromptYesHotkeyRebinds(t *testing.T) {
	m := newWorkspaceChangePromptModel("/tmp/old", "/tmp/new", "dark")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = next.(*workspaceChangePromptModel)
	if !m.result.Rebind {
		t.Fatal("expected y hotkey to choose rebind")
	}
}

func TestInspectPickedSessionWorkspaceChangeUsesRemoteBindingRoot(t *testing.T) {
	change, err := inspectPickedSessionWorkspaceChange(
		&remoteAppServer{
			cfg: config.App{WorkspaceRoot: "/source-client-workspace"},
			retarget: &sessionWorkspaceRetargetContext{
				workspaceRoot: "/active-server-workspace",
				theme:         "dark",
			},
		},
		"session-1",
		clientui.SessionExecutionTarget{
			WorkspaceRoot:         "/target-server-workspace",
			WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
		},
	)
	if err != nil {
		t.Fatalf("inspect workspace change: %v", err)
	}
	if change == nil ||
		change.selectedRoot != "/target-server-workspace" ||
		change.currentRoot != "/active-server-workspace" {
		t.Fatalf("workspace change = %+v", change)
	}
}

func TestInspectPickedSessionWorkspaceChangeRejectsMissingBindingContext(t *testing.T) {
	_, err := inspectPickedSessionWorkspaceChange(
		narrowSessionLifecycleServer{},
		"session-1",
		clientui.SessionExecutionTarget{
			WorkspaceRoot:         "/target-server-workspace",
			WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
		},
	)
	if err == nil {
		t.Fatal("expected missing workspace retarget context error")
	}
}
