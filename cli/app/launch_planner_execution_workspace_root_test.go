package app

import (
	"context"
	"testing"

	"core/shared/serverapi"
)

type executionWorkspaceRootReader struct {
	request       serverapi.SessionExecutionWorkspaceRootRequest
	workspaceRoot string
}

func (r *executionWorkspaceRootReader) GetSessionExecutionWorkspaceRoot(
	_ context.Context,
	request serverapi.SessionExecutionWorkspaceRootRequest,
) (serverapi.SessionExecutionWorkspaceRootResponse, error) {
	r.request = request
	return serverapi.SessionExecutionWorkspaceRootResponse{WorkspaceRoot: r.workspaceRoot}, nil
}

func TestLoadSelectedSessionWorkspaceRootUsesExecutionWorkspaceRootRead(t *testing.T) {
	reader := &executionWorkspaceRootReader{
		workspaceRoot: "/workspace",
	}

	root, err := loadSelectedSessionWorkspaceRoot(context.Background(), reader, "selected-session")
	if err != nil {
		t.Fatalf("loadSelectedSessionWorkspaceRoot: %v", err)
	}
	if root != "/workspace" {
		t.Fatalf("workspace root = %q, want %q", root, "/workspace")
	}
	if reader.request.SessionID.String() != "selected-session" {
		t.Fatalf("requested session = %q, want %q", reader.request.SessionID.String(), "selected-session")
	}
}

func TestLoadSelectedSessionWorkspaceRootRejectsMissingTarget(t *testing.T) {
	reader := &executionWorkspaceRootReader{}

	if _, err := loadSelectedSessionWorkspaceRoot(context.Background(), reader, "selected-session"); err == nil {
		t.Fatal("loadSelectedSessionWorkspaceRoot error = nil")
	}
}
