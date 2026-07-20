package app

import (
	"context"
	"testing"

	"core/shared/clientui"
	"core/shared/serverapi"
)

type executionTargetReader struct {
	request serverapi.SessionMainViewRequest
	target  clientui.SessionExecutionTarget
}

func (r *executionTargetReader) GetSessionMainView(
	_ context.Context,
	request serverapi.SessionMainViewRequest,
) (serverapi.SessionMainViewResponse, error) {
	r.request = request
	return serverapi.SessionMainViewResponse{
		MainView: clientui.RuntimeMainView{
			Session: clientui.RuntimeSessionView{ExecutionTarget: r.target},
		},
	}, nil
}

func TestLoadSelectedSessionExecutionTargetUsesMainViewAuthority(t *testing.T) {
	reader := &executionTargetReader{
		target: clientui.SessionExecutionTarget{
			WorkspaceRoot:         " /workspace ",
			WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
		},
	}

	target, err := loadSelectedSessionExecutionTarget(context.Background(), reader, "selected-session")
	if err != nil {
		t.Fatalf("loadSelectedSessionExecutionTarget: %v", err)
	}
	if target.WorkspaceRoot != "/workspace" ||
		target.WorkspaceAvailability != clientui.ProjectAvailabilityAvailable {
		t.Fatalf("execution target = %+v", target)
	}
	if reader.request.SessionID != "selected-session" {
		t.Fatalf("requested session = %q, want %q", reader.request.SessionID, "selected-session")
	}
}

func TestLoadSelectedSessionExecutionTargetPreservesUnavailableTarget(t *testing.T) {
	reader := &executionTargetReader{
		target: clientui.SessionExecutionTarget{
			WorkspaceRoot:         "/missing",
			WorkspaceAvailability: clientui.ProjectAvailabilityMissing,
		},
	}
	target, err := loadSelectedSessionExecutionTarget(context.Background(), reader, "selected-session")
	if err != nil {
		t.Fatalf("loadSelectedSessionExecutionTarget: %v", err)
	}
	if target.WorkspaceAvailability != clientui.ProjectAvailabilityMissing {
		t.Fatalf("execution target = %+v, want missing", target)
	}
}
