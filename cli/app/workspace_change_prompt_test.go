package app

import (
	"context"
	"errors"
	"testing"

	"core/shared/serverapi"

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

func TestWorkspaceRetargetLoadingModelRunsOperationAndShowsProgress(t *testing.T) {
	called := false
	model := &workspaceRetargetLoadingModel{
		width:  80,
		height: 24,
		theme:  "dark",
		run: func() error {
			called = true
			return nil
		},
	}
	message := model.Init()().(tea.BatchMsg)[0]()
	if !called {
		t.Fatal("loading model did not run Session retarget")
	}
	next, command := model.Update(message)
	if next.(*workspaceRetargetLoadingModel).err != nil || command == nil {
		t.Fatalf("completed loading model = %+v command=%v", next, command)
	}
}

func TestInteractiveSessionRetargetPreservesTypedFailureFacts(t *testing.T) {
	failure := serverapi.SessionRetargetFailure{
		Diagnostic: "target workspace became unavailable",
		UnchangedProject: serverapi.ProjectReference{
			ID:   "project-a",
			Name: "Project A",
		},
		UnchangedWorkingDirectory: "/workspace-a",
	}
	client := &recordingSessionLifecycleClient{
		retargetSessionWorkspace: func(
			_ context.Context,
			request serverapi.SessionRetargetWorkspaceRequest,
		) (serverapi.SessionRetargetWorkspaceResponse, error) {
			return serverapi.SessionRetargetWorkspaceResponse{
				Acknowledgement: serverapi.WorktreeScheduledAcknowledgement{
					OperationID: request.OperationID,
				},
				Outcome: &serverapi.SessionRetargetOutcome{
					OperationID: request.OperationID,
					Kind:        serverapi.SessionRetargetOutcomeFailed,
					Failure:     &failure,
				},
			}, nil
		},
	}
	err := retargetInteractiveSessionWorkspace(
		t.Context(),
		narrowSessionLifecycleServer{lifecycle: client},
		"session-a",
		"/workspace-b",
	)
	var typed *sessionRetargetFailureError
	if !errors.As(err, &typed) || typed.Failure != failure {
		t.Fatalf("retarget failure = %T %+v", err, err)
	}
}
