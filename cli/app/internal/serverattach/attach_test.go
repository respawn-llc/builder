package serverattach

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"core/cli/app/internal/remoteattach"
	"core/shared/apicontract"
	"core/shared/client"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/serverapi"
)

type ProjectViewRemote = remoteattach.ProjectViewRemote

type projectViewRemoteStub struct {
	apicontract.ProjectViewService
	identity     protocol.ServerIdentity
	plan         func(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error)
	pinnedRootID string
	closed       bool
}

func (s *projectViewRemoteStub) Close() error {
	s.closed = true
	return nil
}

// RequireRoot mirrors client.Remote: it records the pinned id and rejects a
// mismatch against the stub's stamped identity (empty id disables validation).
func (s *projectViewRemoteStub) RequireRoot(rootID string) error {
	s.pinnedRootID = rootID
	if rootID != "" && s.identity.PersistenceRootID != rootID {
		return errors.New("project view root mismatch")
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

func boundPlanResponse() serverapi.ProjectBindingPlanResponse {
	return serverapi.ProjectBindingPlanResponse{
		Kind:    serverapi.ProjectBindingPlanKindBound,
		Binding: &serverapi.ProjectBinding{ProjectID: "project-1", WorkspaceID: "workspace-1"},
	}
}

func boundProjectView(plan func(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error)) *projectViewRemoteStub {
	return &projectViewRemoteStub{
		identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version},
		plan:     plan,
	}
}

func testDialWorkspace(context.Context, config.App, string, string) (*client.Remote, error) {
	return new(client.Remote), nil
}

func boundProjectDial(ctx context.Context, cfg config.App) (ProjectViewRemote, error) {
	return planProjectDial(boundPlanResponse())(ctx, cfg)
}

func unavailableProjectDial(context.Context, config.App) (ProjectViewRemote, error) {
	return nil, errors.New("configured remote unavailable")
}

func planProjectDial(response serverapi.ProjectBindingPlanResponse) func(context.Context, config.App) (ProjectViewRemote, error) {
	return func(context.Context, config.App) (ProjectViewRemote, error) {
		return boundProjectView(func(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
			return response, nil
		}), nil
	}
}

func testAttachRequest(dialProject remoteattach.DialProjectView) AttachRunPromptRequest {
	return AttachRunPromptRequest{
		Config:           config.App{WorkspaceRoot: "/workspace"},
		AttachTimeout:    time.Second,
		DiscoveryTimeout: time.Second,
		DialProjectView:  dialProject,
		DialWorkspace:    testDialWorkspace,
	}
}

func requireExplicitRoot(req *AttachRunPromptRequest) string {
	req.Config.PersistenceRoot = "/tmp/kent-attach-test-root"
	req.Config.Source = config.SourceReport{Sources: map[string]string{"persistence_root": "flag"}}
	return config.ExplicitPersistenceRootID(req.Config)
}

func TestAttachRunPromptWithoutReachableServer(t *testing.T) {
	_, _, err := AttachRunPrompt(context.Background(), testAttachRequest(unavailableProjectDial))
	if !errors.Is(err, ErrNoServerAvailable) {
		t.Fatalf("err = %v, want ErrNoServerAvailable", err)
	}
}

func boundProjectViewWithRoot(rootID string) *projectViewRemoteStub {
	return &projectViewRemoteStub{
		identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, PersistenceRootID: rootID},
		plan: func(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
			return boundPlanResponse(), nil
		},
	}
}

func TestAttachRunPromptReportsTypedPersistenceRootMismatch(t *testing.T) {
	for _, reportedRoot := range []string{"root-other", ""} {
		t.Run(reportedRoot, func(t *testing.T) {
			req := testAttachRequest(func(context.Context, config.App) (remoteattach.ProjectViewRemote, error) {
				projectViews := boundProjectViewWithRoot(reportedRoot)
				return projectViews, nil
			})
			requireExplicitRoot(&req)
			_, _, err := AttachRunPrompt(context.Background(), req)
			if !errors.Is(err, ErrReachableServerRootMismatch) ||
				errors.Is(err, ErrNoServerAvailable) {
				t.Fatalf("err = %v, want root mismatch distinct from no server", err)
			}
			var mismatch *RootMismatchServerError
			if !errors.As(err, &mismatch) || strings.TrimSpace(mismatch.Reason) == "" {
				t.Fatalf("err = %v, want typed root mismatch with reason", err)
			}
		})
	}
}

func TestAttachRunPromptReturnsExactRemoteAndCloseOperation(t *testing.T) {
	req := testAttachRequest(boundProjectDial)
	dialWorkspace, closeServer, _ := dialWorkspaceServerWithRoot(t, "", true)
	defer closeServer()
	var dialed *client.Remote
	req.DialWorkspace = func(ctx context.Context, cfg config.App, projectID string, workspaceID string) (*client.Remote, error) {
		remote, err := dialWorkspace(ctx, cfg, projectID, workspaceID)
		dialed = remote
		return remote, err
	}
	service, closeFn, err := AttachRunPrompt(context.Background(), req)
	if err != nil {
		t.Fatalf("AttachRunPrompt: %v", err)
	}
	if service != dialed || closeFn == nil {
		t.Fatal("attach did not return the exact validated remote and close operation")
	}
	if err := closeFn(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// dialWorkspaceServerWithRoot starts a minimal RPC server whose handshake
// reports the given persistence root id, returning a DialWorkspace that attaches
// to it. It lets root-validation tests exercise the real client handshake path
// where ServerIdentity.PersistenceRootID is populated.
func dialWorkspaceServerWithRoot(t *testing.T, rootID string, attachProject bool) (remoteattach.DialWorkspace, func(), <-chan struct{}) {
	t.Helper()
	disconnected := make(chan struct{}, 1)
	server := httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(func(ctx context.Context, conn rpcwire.Conn) {
		defer func() { disconnected <- struct{}{} }()
		for event := range conn.Events() {
			if event.Err != nil {
				return
			}
			req := event.Frame.Request()
			switch req.Method {
			case protocol.MethodHandshake:
				_ = conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1", PersistenceRootID: rootID}})))
			case protocol.MethodAttachProject:
				attachment, err := protocol.ProjectAttachResponse("project-1", "workspace-1", "/workspace")
				if err != nil {
					return
				}
				_ = conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, attachment)))
			default:
				return
			}
		}
	}))
	wsURL := "ws" + server.URL[len("http"):]
	dial := func(ctx context.Context, _ config.App, projectID string, _ string) (*client.Remote, error) {
		if !attachProject {
			return client.DialRemoteURL(ctx, wsURL)
		}
		return client.DialRemoteURLForProject(ctx, wsURL, projectID)
	}
	return dial, server.Close, disconnected
}

func TestAttachRunPromptPropagatesHeadlessWorkspaceFailures(t *testing.T) {
	for _, tc := range []struct {
		name        string
		dialProject func(context.Context, config.App) (ProjectViewRemote, error)
		wantErr     error
	}{
		{
			name:        "headless unregistered workspace fails fast",
			dialProject: planProjectDial(serverapi.ProjectBindingPlanResponse{Kind: serverapi.ProjectBindingPlanKindLocalUnbound}),
			wantErr:     serverapi.ErrWorkspaceNotRegistered,
		},
		{
			name:        "headless ambiguous remote workspace fails",
			dialProject: planProjectDial(serverapi.ProjectBindingPlanResponse{Kind: serverapi.ProjectBindingPlanKindHeadlessRemoteAmbiguous}),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := AttachRunPrompt(context.Background(), testAttachRequest(tc.dialProject))
			if err == nil {
				t.Fatal("AttachRunPrompt succeeded, want workspace selection failure")
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("AttachRunPrompt error = %v, want %v", err, tc.wantErr)
				}
			}
		})
	}
}
