package remoteattach

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/shared/client"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/serverapi"
)

type projectViewRemoteStub struct {
	identity     protocol.ServerIdentity
	plan         func(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error)
	requireRoot  func(string) error
	pinnedRootID string
	rootPinned   bool
	closed       bool
}

func (s *projectViewRemoteStub) Close() error {
	s.closed = true
	return nil
}

func (s *projectViewRemoteStub) RequireRoot(rootID string) error {
	s.pinnedRootID = rootID
	s.rootPinned = true
	if s.requireRoot != nil {
		return s.requireRoot(rootID)
	}
	return nil
}

func (s *projectViewRemoteStub) Identity() protocol.ServerIdentity {
	return s.identity
}

func (s *projectViewRemoteStub) PlanWorkspaceBinding(ctx context.Context, req serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
	if s.plan != nil {
		return s.plan(ctx, req)
	}
	return serverapi.ProjectBindingPlanResponse{}, errors.New("unexpected PlanWorkspaceBinding call")
}

func (*projectViewRemoteStub) ListProjects(context.Context, serverapi.ProjectListRequest) (serverapi.ProjectListResponse, error) {
	return serverapi.ProjectListResponse{}, errors.New("unexpected ListProjects call")
}

func (*projectViewRemoteStub) ListProjectHome(context.Context, serverapi.ProjectHomeListRequest) (serverapi.ProjectHomeListResponse, error) {
	return serverapi.ProjectHomeListResponse{}, errors.New("unexpected ListProjectHome call")
}

func (*projectViewRemoteStub) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, errors.New("unexpected ResolveProjectPath call")
}

func (*projectViewRemoteStub) CreateProject(context.Context, serverapi.ProjectCreateRequest) (serverapi.ProjectCreateResponse, error) {
	return serverapi.ProjectCreateResponse{}, errors.New("unexpected CreateProject call")
}

func (*projectViewRemoteStub) AttachWorkspaceToProject(context.Context, serverapi.ProjectAttachWorkspaceRequest) (serverapi.ProjectAttachWorkspaceResponse, error) {
	return serverapi.ProjectAttachWorkspaceResponse{}, errors.New("unexpected AttachWorkspaceToProject call")
}

func (*projectViewRemoteStub) ListProjectWorkspaces(context.Context, serverapi.ProjectWorkspaceListRequest) (serverapi.ProjectWorkspaceListResponse, error) {
	return serverapi.ProjectWorkspaceListResponse{}, errors.New("unexpected ListProjectWorkspaces call")
}

func (*projectViewRemoteStub) RebindWorkspace(context.Context, serverapi.ProjectRebindWorkspaceRequest) (serverapi.ProjectRebindWorkspaceResponse, error) {
	return serverapi.ProjectRebindWorkspaceResponse{}, errors.New("unexpected RebindWorkspace call")
}

func (*projectViewRemoteStub) GetProjectOverview(context.Context, serverapi.ProjectGetOverviewRequest) (serverapi.ProjectGetOverviewResponse, error) {
	return serverapi.ProjectGetOverviewResponse{}, errors.New("unexpected GetProjectOverview call")
}

func (*projectViewRemoteStub) ListSessionPage(context.Context, serverapi.SessionPageRequest) (serverapi.SessionPageResponse, error) {
	return serverapi.SessionPageResponse{}, errors.New("unexpected ListSessionPage call")
}

func TestDialHeadlessPinsProjectViewRootBeforeDiscovery(t *testing.T) {
	cfg := config.App{WorkspaceRoot: "/workspace"}
	pinErr := errors.New("root mismatch")
	projectViews := &projectViewRemoteStub{
		identity:    protocol.ServerIdentity{Capabilities: protocol.CapabilityFlags{RunPrompt: true, AuthBootstrap: true, ProjectAttach: true}},
		requireRoot: func(string) error { return pinErr },
		plan: func(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
			t.Fatal("discovery must not run when the project-view root pin fails")
			return serverapi.ProjectBindingPlanResponse{}, nil
		},
	}
	remote, ok, err := DialHeadless(context.Background(), HeadlessRequest{
		Config:           cfg,
		AttachTimeout:    20 * time.Millisecond,
		DiscoveryTimeout: 20 * time.Millisecond,
		RootID:           "root-want",
		DialProjectView:  func(context.Context, config.App) (ProjectViewRemote, error) { return projectViews, nil },
		DialWorkspace: func(context.Context, config.App, string, string) (*client.Remote, error) {
			t.Fatal("workspace dial must not run when the project-view root pin fails")
			return nil, nil
		},
		Supports: SupportsRunPrompt,
	})
	if !errors.Is(err, pinErr) {
		t.Fatalf("DialHeadless err = %v, want %v", err, pinErr)
	}
	if !ok {
		t.Fatal("DialHeadless ok = false, want true (server reachable)")
	}
	if remote != nil {
		t.Fatal("DialHeadless must not return a remote when the root pin fails")
	}
	if projectViews.pinnedRootID != "root-want" {
		t.Fatalf("project-view pinned root = %q, want %q", projectViews.pinnedRootID, "root-want")
	}
	if !projectViews.closed {
		t.Fatal("project-view remote must be closed when the root pin fails")
	}
}

func TestDialHeadlessUsesWorkspaceDiscoveryAndFreshWorkspaceDialTimeout(t *testing.T) {
	cfg := config.App{WorkspaceRoot: "/workspace"}
	attachTimeout := 20 * time.Millisecond
	projectViews := &projectViewRemoteStub{
		identity: protocol.ServerIdentity{Capabilities: protocol.CapabilityFlags{RunPrompt: true, AuthBootstrap: true, ProjectAttach: true}},
		plan: func(ctx context.Context, req serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
			if req.Path != cfg.WorkspaceRoot {
				t.Fatalf("path = %q, want %q", req.Path, cfg.WorkspaceRoot)
			}
			if req.Mode != serverapi.ProjectBindingPlanModeHeadless {
				t.Fatalf("mode = %q, want headless", req.Mode)
			}
			time.Sleep(attachTimeout + 10*time.Millisecond)
			if err := ctx.Err(); err != nil {
				return serverapi.ProjectBindingPlanResponse{}, err
			}
			return serverapi.ProjectBindingPlanResponse{
				Kind:      serverapi.ProjectBindingPlanKindHeadlessRemoteSelected,
				Workspace: &serverapi.ProjectWorkspacePlanSelected{ProjectID: "project-1", WorkspaceID: "workspace-1"},
			}, nil
		},
	}
	var dialRemaining time.Duration
	remote, ok, err := DialHeadless(context.Background(), HeadlessRequest{
		Config:           cfg,
		AttachTimeout:    attachTimeout,
		DiscoveryTimeout: 120 * time.Millisecond,
		DialProjectView: func(context.Context, config.App) (ProjectViewRemote, error) {
			return projectViews, nil
		},
		DialWorkspace: func(ctx context.Context, cfg config.App, projectID string, workspaceID string) (*client.Remote, error) {
			deadline, hasDeadline := ctx.Deadline()
			if !hasDeadline {
				t.Fatal("expected workspace dial deadline")
			}
			dialRemaining = time.Until(deadline)
			if cfg.WorkspaceRoot != "/workspace" {
				t.Fatalf("workspace root = %q, want /workspace", cfg.WorkspaceRoot)
			}
			if projectID != "project-1" || workspaceID != "workspace-1" {
				t.Fatalf("workspace dial target = %s/%s, want project-1/workspace-1", projectID, workspaceID)
			}
			return new(client.Remote), nil
		},
		Supports: SupportsRunPrompt,
	})
	if err != nil {
		t.Fatalf("DialHeadless: %v", err)
	}
	if !ok {
		t.Fatal("expected attach to succeed")
	}
	if remote == nil {
		t.Fatal("expected remote")
	}
	if !projectViews.closed {
		t.Fatal("expected project view remote to close after workspace selection")
	}
	if dialRemaining <= attachTimeout/2 {
		t.Fatalf("expected fresh attach timeout after workspace discovery, remaining=%v attach=%v", dialRemaining, attachTimeout)
	}
}

func TestDialHeadlessRejectsNilDialers(t *testing.T) {
	_, _, err := DialHeadless(context.Background(), HeadlessRequest{
		DialWorkspace: func(context.Context, config.App, string, string) (*client.Remote, error) {
			return nil, nil
		},
	})
	if !errors.Is(err, errProjectViewDialerRequired) {
		t.Fatalf("error = %v, want missing project view dialer", err)
	}

	_, _, err = DialHeadless(context.Background(), HeadlessRequest{
		DialProjectView: func(context.Context, config.App) (ProjectViewRemote, error) {
			return &projectViewRemoteStub{}, nil
		},
	})
	if !errors.Is(err, errWorkspaceDialerRequired) {
		t.Fatalf("error = %v, want missing workspace dialer", err)
	}
}

func TestDialHeadlessClosesAndReturnsPlanFailure(t *testing.T) {
	wantErr := errors.New("plan failed")
	projectViews := &projectViewRemoteStub{
		identity: protocol.ServerIdentity{Capabilities: protocol.CapabilityFlags{RunPrompt: true, AuthBootstrap: true, ProjectAttach: true}},
		plan: func(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
			return serverapi.ProjectBindingPlanResponse{}, wantErr
		},
	}
	remote, ok, err := DialHeadless(context.Background(), HeadlessRequest{
		Config:           config.App{WorkspaceRoot: "/workspace"},
		AttachTimeout:    time.Second,
		DiscoveryTimeout: time.Second,
		DialProjectView: func(context.Context, config.App) (ProjectViewRemote, error) {
			return projectViews, nil
		},
		DialWorkspace: func(context.Context, config.App, string, string) (*client.Remote, error) {
			t.Fatal("unexpected workspace dial")
			return nil, nil
		},
		Supports: SupportsRunPrompt,
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("DialHeadless error = %v, want %v", err, wantErr)
	}
	if !ok {
		t.Fatal("expected attempted attach to report ok=true on plan failure")
	}
	if remote != nil {
		t.Fatalf("expected no remote, got %v", remote)
	}
	if !projectViews.closed {
		t.Fatal("expected project view remote to close on plan failure")
	}
}

func TestDialHeadlessReturnsWorkspaceDialFailure(t *testing.T) {
	wantErr := errors.New("workspace dial failed")
	projectViews := &projectViewRemoteStub{
		identity: protocol.ServerIdentity{Capabilities: protocol.CapabilityFlags{RunPrompt: true, AuthBootstrap: true, ProjectAttach: true}},
		plan: func(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
			return serverapi.ProjectBindingPlanResponse{
				Kind:    serverapi.ProjectBindingPlanKindBound,
				Binding: &serverapi.ProjectBinding{ProjectID: "project-1", WorkspaceID: "workspace-1"},
			}, nil
		},
	}
	remote, ok, err := DialHeadless(context.Background(), HeadlessRequest{
		Config:           config.App{WorkspaceRoot: "/workspace"},
		AttachTimeout:    time.Second,
		DiscoveryTimeout: time.Second,
		DialProjectView: func(context.Context, config.App) (ProjectViewRemote, error) {
			return projectViews, nil
		},
		DialWorkspace: func(context.Context, config.App, string, string) (*client.Remote, error) {
			return nil, wantErr
		},
		Supports: SupportsRunPrompt,
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("DialHeadless error = %v, want %v", err, wantErr)
	}
	if !ok {
		t.Fatal("expected attempted attach to report ok=true on dial failure")
	}
	if remote != nil {
		t.Fatalf("expected no remote, got %v", remote)
	}
	if !projectViews.closed {
		t.Fatal("expected project view remote to close before workspace dial")
	}
}

func TestDialHeadlessClosesAndSkipsUnsupportedServer(t *testing.T) {
	projectViews := &projectViewRemoteStub{
		identity: protocol.ServerIdentity{Capabilities: protocol.CapabilityFlags{RunPrompt: true}},
	}
	remote, ok, err := DialHeadless(context.Background(), HeadlessRequest{
		Config:        config.App{WorkspaceRoot: "/workspace"},
		AttachTimeout: time.Second,
		DialProjectView: func(context.Context, config.App) (ProjectViewRemote, error) {
			return projectViews, nil
		},
		DialWorkspace: func(context.Context, config.App, string, string) (*client.Remote, error) {
			t.Fatal("unexpected workspace dial")
			return nil, nil
		},
		Supports: SupportsRunPrompt,
	})
	if err != nil {
		t.Fatalf("DialHeadless: %v", err)
	}
	if ok || remote != nil {
		t.Fatalf("expected unsupported server to be skipped, remote=%v ok=%t", remote, ok)
	}
	if !projectViews.closed {
		t.Fatal("expected unsupported project view remote to close")
	}
}

func TestDialInteractiveRejectsNilDialers(t *testing.T) {
	remote, ok := DialInteractive(context.Background(), InteractiveRequest{})
	if ok || remote != nil {
		t.Fatalf("expected nil dialers to skip, remote=%v ok=%t", remote, ok)
	}
}

func TestDialInteractiveBoundWorkspaceDialsWorkspaceAndClosesProjectView(t *testing.T) {
	cfg := config.App{WorkspaceRoot: "/workspace"}
	projectViews := &projectViewRemoteStub{
		identity: protocol.ServerIdentity{Capabilities: protocol.CapabilityFlags{ProjectAttach: true}},
		plan: func(ctx context.Context, req serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
			if err := ctx.Err(); err != nil {
				return serverapi.ProjectBindingPlanResponse{}, err
			}
			if req.Mode != serverapi.ProjectBindingPlanModeInteractive {
				t.Fatalf("mode = %q, want interactive", req.Mode)
			}
			return serverapi.ProjectBindingPlanResponse{
				Kind:    serverapi.ProjectBindingPlanKindBound,
				Binding: &serverapi.ProjectBinding{ProjectID: "project-1", WorkspaceID: "workspace-1"},
			}, nil
		},
	}
	remote, ok := DialInteractive(context.Background(), InteractiveRequest{
		Config:        cfg,
		AttachTimeout: time.Second,
		DialProjectView: func(context.Context, config.App) (ProjectViewRemote, error) {
			return projectViews, nil
		},
		DialWorkspace: func(ctx context.Context, cfg config.App, projectID string, workspaceID string) (*client.Remote, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if projectID != "project-1" || workspaceID != "workspace-1" {
				t.Fatalf("workspace dial target = %s/%s, want project-1/workspace-1", projectID, workspaceID)
			}
			return new(client.Remote), nil
		},
		Supports: func(protocol.CapabilityFlags) bool { return true },
	})
	if !ok {
		t.Fatal("expected interactive attach to succeed")
	}
	if remote == nil {
		t.Fatal("expected remote")
	}
	if !projectViews.closed {
		t.Fatal("expected project view remote to close before workspace dial")
	}
}

func TestDialInteractiveClosesNonRemoteUnboundFallback(t *testing.T) {
	projectViews := &projectViewRemoteStub{
		identity: protocol.ServerIdentity{Capabilities: protocol.CapabilityFlags{ProjectAttach: true}},
		plan: func(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
			return serverapi.ProjectBindingPlanResponse{Kind: serverapi.ProjectBindingPlanKindLocalUnbound}, nil
		},
	}
	remote, ok := DialInteractive(context.Background(), InteractiveRequest{
		Config:        config.App{WorkspaceRoot: "/workspace"},
		AttachTimeout: time.Second,
		DialProjectView: func(context.Context, config.App) (ProjectViewRemote, error) {
			return projectViews, nil
		},
		DialWorkspace: func(context.Context, config.App, string, string) (*client.Remote, error) {
			t.Fatal("unexpected workspace dial")
			return nil, nil
		},
		Supports:     func(protocol.CapabilityFlags) bool { return true },
		RequireBound: false,
	})
	if ok || remote != nil {
		t.Fatalf("expected non-remote unbound fallback to skip, remote=%v ok=%t", remote, ok)
	}
	if !projectViews.closed {
		t.Fatal("expected non-remote unbound fallback to close")
	}
}

func TestHeadlessWorkspaceRegistrationErrorWrapsSentinel(t *testing.T) {
	err := HeadlessWorkspaceRegistrationError(" /workspace ")
	if !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		t.Fatalf("error = %v, want ErrWorkspaceNotRegistered", err)
	}
}
