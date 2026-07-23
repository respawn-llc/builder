package embeddedattach

import (
	"context"
	"errors"
	"testing"

	"core/shared/apicontract"
	"core/shared/config"
)

type fakeSessionLaunchClient struct {
	apicontract.SessionLaunchService
}

type fakeRunPromptClient struct {
	apicontract.RunPromptService
}

type fakeServer struct {
	cfg           config.App
	rootLaunch    apicontract.SessionLaunchService
	rootRunPrompt apicontract.RunPromptService
	idLaunch      apicontract.SessionLaunchService
	idRunPrompt   apicontract.RunPromptService
	rootErr       error
	rootRunErr    error
	idErr         error
	idRunErr      error
	calls         []string
}

func (s *fakeServer) Config() config.App { return s.cfg }

func (s *fakeServer) SessionLaunchClientForProjectWorkspace(_ context.Context, projectID string, workspaceRoot string) (apicontract.SessionLaunchService, error) {
	s.calls = append(s.calls, "launch-root:"+projectID+":"+workspaceRoot)
	return s.rootLaunch, s.rootErr
}

func (s *fakeServer) RunPromptClientForProjectWorkspace(_ context.Context, projectID string, workspaceRoot string) (apicontract.RunPromptService, error) {
	s.calls = append(s.calls, "run-root:"+projectID+":"+workspaceRoot)
	return s.rootRunPrompt, s.rootRunErr
}

func (s *fakeServer) SessionLaunchClientForProjectWorkspaceID(_ context.Context, projectID string, workspaceID string) (apicontract.SessionLaunchService, error) {
	s.calls = append(s.calls, "launch-id:"+projectID+":"+workspaceID)
	return s.idLaunch, s.idErr
}

func (s *fakeServer) RunPromptClientForProjectWorkspaceID(_ context.Context, projectID string, workspaceID string) (apicontract.RunPromptService, error) {
	s.calls = append(s.calls, "run-id:"+projectID+":"+workspaceID)
	return s.idRunPrompt, s.idRunErr
}

func TestBindProjectWorkspaceUsesWorkspaceRootWhenWorkspaceIDEmpty(t *testing.T) {
	launch := fakeSessionLaunchClient{}
	runPrompt := fakeRunPromptClient{}
	server := &fakeServer{
		cfg:           config.App{WorkspaceRoot: "/workspace"},
		rootLaunch:    launch,
		rootRunPrompt: runPrompt,
	}
	bound, err := BindProjectWorkspace(context.Background(), WorkspaceBindingRequest{
		Server:      server,
		ProjectID:   " project-1 ",
		WorkspaceID: " ",
	})
	if err != nil {
		t.Fatalf("BindProjectWorkspace: %v", err)
	}
	if bound.ProjectID != "project-1" {
		t.Fatalf("project id = %q, want trimmed project id", bound.ProjectID)
	}
	if bound.SessionLaunch != launch {
		t.Fatal("expected workspace-root session launch client")
	}
	if bound.RunPrompt != runPrompt {
		t.Fatal("expected workspace-root run prompt client")
	}
	wantCalls := []string{"launch-root:project-1:/workspace", "run-root:project-1:/workspace"}
	if !equalStrings(server.calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", server.calls, wantCalls)
	}
}

func TestBindProjectWorkspaceUsesWorkspaceIDWhenPresent(t *testing.T) {
	launch := fakeSessionLaunchClient{}
	runPrompt := fakeRunPromptClient{}
	server := &fakeServer{idLaunch: launch, idRunPrompt: runPrompt}
	bound, err := BindProjectWorkspace(context.Background(), WorkspaceBindingRequest{
		Server:      server,
		ProjectID:   "project-1",
		WorkspaceID: " workspace-id ",
	})
	if err != nil {
		t.Fatalf("BindProjectWorkspace: %v", err)
	}
	if bound.SessionLaunch != launch {
		t.Fatal("expected workspace-id session launch client")
	}
	if bound.RunPrompt != runPrompt {
		t.Fatal("expected workspace-id run prompt client")
	}
	wantCalls := []string{"launch-id:project-1:workspace-id", "run-id:project-1:workspace-id"}
	if !equalStrings(server.calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", server.calls, wantCalls)
	}
}

func TestBindProjectWorkspaceRejectsMissingInputsBeforeClientSelection(t *testing.T) {
	server := &fakeServer{}
	_, err := BindProjectWorkspace(context.Background(), WorkspaceBindingRequest{Server: server, ProjectID: " "})
	if err == nil {
		t.Fatal("expected missing project id error")
	}
	if len(server.calls) != 0 {
		t.Fatalf("calls = %#v, want none", server.calls)
	}
	_, err = BindProjectWorkspace(context.Background(), WorkspaceBindingRequest{ProjectID: "project-1"})
	if err == nil {
		t.Fatal("expected missing embedded server error")
	}
}

func TestBindProjectWorkspaceStopsWhenLaunchClientFails(t *testing.T) {
	launchErr := errors.New("launch failed")
	server := &fakeServer{rootErr: launchErr}
	_, err := BindProjectWorkspace(context.Background(), WorkspaceBindingRequest{Server: server, ProjectID: "project-1"})
	if !errors.Is(err, launchErr) {
		t.Fatalf("error = %v, want %v", err, launchErr)
	}
	wantCalls := []string{"launch-root:project-1:"}
	if !equalStrings(server.calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", server.calls, wantCalls)
	}
}

func TestBindProjectWorkspaceReturnsRunPromptClientError(t *testing.T) {
	runErr := errors.New("run prompt failed")
	server := &fakeServer{rootRunErr: runErr}
	_, err := BindProjectWorkspace(context.Background(), WorkspaceBindingRequest{Server: server, ProjectID: "project-1"})
	if !errors.Is(err, runErr) {
		t.Fatalf("error = %v, want %v", err, runErr)
	}
}

func equalStrings(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
