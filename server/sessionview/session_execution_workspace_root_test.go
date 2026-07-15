package sessionview

import (
	"context"
	"errors"
	"testing"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestGetSessionExecutionWorkspaceRootDoesNotResolveSessionStore(t *testing.T) {
	sessionID, err := runtimeids.ParseSessionID("target-session")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	want := clientui.SessionExecutionTarget{
		WorkspaceID:           "workspace-1",
		WorkspaceName:         "Workspace",
		WorkspaceRoot:         "/workspace",
		WorkspaceAvailability: "available",
		CwdRelpath:            "nested",
		EffectiveWorkdir:      "/workspace/nested",
	}
	service := NewService(
		failingSessionStoreResolver{err: errors.New("session store must not be resolved")},
		nil,
		staticExecutionTargetResolver{target: want},
	)

	response, err := service.GetSessionExecutionWorkspaceRoot(
		context.Background(),
		serverapi.SessionExecutionWorkspaceRootRequest{SessionID: sessionID},
	)
	if err != nil {
		t.Fatalf("GetSessionExecutionWorkspaceRoot: %v", err)
	}
	if response.WorkspaceRoot != want.WorkspaceRoot {
		t.Fatalf("workspace root = %q, want %q", response.WorkspaceRoot, want.WorkspaceRoot)
	}
}

func TestGetSessionExecutionWorkspaceRootRejectsUnavailableTargetMetadata(t *testing.T) {
	sessionID, err := runtimeids.ParseSessionID("target-session")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	for _, service := range []*Service{
		NewService(nil, nil, nil),
		NewService(nil, nil, staticExecutionTargetResolver{}),
	} {
		if _, err := service.GetSessionExecutionWorkspaceRoot(
			context.Background(),
			serverapi.SessionExecutionWorkspaceRootRequest{SessionID: sessionID},
		); err == nil {
			t.Fatal("GetSessionExecutionWorkspaceRoot error = nil")
		}
	}
}
