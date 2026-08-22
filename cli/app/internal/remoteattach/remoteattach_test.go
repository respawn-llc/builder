package remoteattach

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/shared/apicontract"
	"core/shared/client"
	"core/shared/config"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	"core/shared/protocol"
	"core/shared/serverapi"
)

type projectViewRemoteStub struct {
	apicontract.ProjectViewService
	identity     protocol.ServerIdentity
	plan         func(context.Context, *projectpb.PlanWorkspaceBindingRequest) (*projectpb.PlanWorkspaceBindingSuccess, error)
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

func (s *projectViewRemoteStub) PlanWorkspaceBinding(ctx context.Context, req *projectpb.PlanWorkspaceBindingRequest) (*projectpb.PlanWorkspaceBindingSuccess, error) {
	if s.plan != nil {
		return s.plan(ctx, req)
	}
	return &projectpb.PlanWorkspaceBindingSuccess{}, errors.New("unexpected PlanWorkspaceBinding call")
}

func TestDialHeadlessPinsProjectViewRootBeforeDiscovery(t *testing.T) {
	cfg := config.App{WorkspaceRoot: "/workspace"}
	pinErr := errors.New("root mismatch")
	projectViews := &projectViewRemoteStub{
		requireRoot: func(string) error { return pinErr },
		plan: func(context.Context, *projectpb.PlanWorkspaceBindingRequest) (*projectpb.PlanWorkspaceBindingSuccess, error) {
			t.Fatal("discovery must not run when the project-view root pin fails")
			return &projectpb.PlanWorkspaceBindingSuccess{}, nil
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
		plan: func(ctx context.Context, req *projectpb.PlanWorkspaceBindingRequest) (*projectpb.PlanWorkspaceBindingSuccess, error) {
			if req.Path != cfg.WorkspaceRoot {
				t.Fatalf("path = %q, want %q", req.Path, cfg.WorkspaceRoot)
			}
			if req.Mode != projectpb.WorkspaceBindingPlanMode_WORKSPACE_BINDING_PLAN_MODE_HEADLESS {
				t.Fatalf("mode = %q, want headless", req.Mode)
			}
			time.Sleep(attachTimeout + 10*time.Millisecond)
			if err := ctx.Err(); err != nil {
				return &projectpb.PlanWorkspaceBindingSuccess{}, err
			}
			return &projectpb.PlanWorkspaceBindingSuccess{
				Kind:      projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_HEADLESS_REMOTE_SELECTED,
				Workspace: &projectpb.SelectedProjectWorkspace{ProjectId: "project-1", WorkspaceId: "workspace-1"},
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
		plan: func(context.Context, *projectpb.PlanWorkspaceBindingRequest) (*projectpb.PlanWorkspaceBindingSuccess, error) {
			return &projectpb.PlanWorkspaceBindingSuccess{}, wantErr
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
		plan: func(context.Context, *projectpb.PlanWorkspaceBindingRequest) (*projectpb.PlanWorkspaceBindingSuccess, error) {
			return &projectpb.PlanWorkspaceBindingSuccess{
				Kind: projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_BOUND,
				Binding: &projectpb.ProjectBinding{
					ProjectId:       "project-1",
					WorkspaceId:     "workspace-1",
					WorkspaceStatus: projectpb.ProjectAvailability_PROJECT_AVAILABILITY_AVAILABLE,
				},
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

func TestDialInteractiveRejectsNilDialers(t *testing.T) {
	remote, ok := DialInteractive(context.Background(), InteractiveRequest{})
	if ok || remote != nil {
		t.Fatalf("expected nil dialers to skip, remote=%v ok=%t", remote, ok)
	}
}

func TestDialInteractiveBoundWorkspaceDialsWorkspaceAndClosesProjectView(t *testing.T) {
	cfg := config.App{WorkspaceRoot: "/workspace"}
	projectViews := &projectViewRemoteStub{
		plan: func(ctx context.Context, req *projectpb.PlanWorkspaceBindingRequest) (*projectpb.PlanWorkspaceBindingSuccess, error) {
			if err := ctx.Err(); err != nil {
				return &projectpb.PlanWorkspaceBindingSuccess{}, err
			}
			if req.Mode != projectpb.WorkspaceBindingPlanMode_WORKSPACE_BINDING_PLAN_MODE_INTERACTIVE {
				t.Fatalf("mode = %q, want interactive", req.Mode)
			}
			return &projectpb.PlanWorkspaceBindingSuccess{
				Kind: projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_BOUND,
				Binding: &projectpb.ProjectBinding{
					ProjectId:       "project-1",
					WorkspaceId:     "workspace-1",
					WorkspaceStatus: projectpb.ProjectAvailability_PROJECT_AVAILABILITY_AVAILABLE,
				},
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
		plan: func(context.Context, *projectpb.PlanWorkspaceBindingRequest) (*projectpb.PlanWorkspaceBindingSuccess, error) {
			return &projectpb.PlanWorkspaceBindingSuccess{Kind: projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_LOCAL_UNBOUND}, nil
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
