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

func compatibleCapabilities() protocol.CapabilityFlags {
	return protocol.CapabilityFlags{
		AuthBootstrap: true,
		ProjectAttach: true,
		RunPrompt:     true,
	}
}

func boundPlanResponse() serverapi.ProjectBindingPlanResponse {
	return serverapi.ProjectBindingPlanResponse{
		Kind:    serverapi.ProjectBindingPlanKindBound,
		Binding: &serverapi.ProjectBinding{ProjectID: "project-1", WorkspaceID: "workspace-1"},
	}
}

func boundProjectView(plan func(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error)) *projectViewRemoteStub {
	return &projectViewRemoteStub{
		identity: protocol.ServerIdentity{Capabilities: compatibleCapabilities()},
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

func testRemotePolicy(dialProject func(context.Context, config.App) (ProjectViewRemote, error)) RemotePolicy {
	return RemotePolicy{
		Config:          config.App{WorkspaceRoot: "/workspace"},
		AttachTimeout:   time.Second,
		DialProjectView: dialProject,
		DialWorkspace:   testDialWorkspace,
		Supports:        func(protocol.CapabilityFlags) bool { return true },
	}
}

func testRemotePolicyWithDiscovery(dialProject func(context.Context, config.App) (ProjectViewRemote, error)) RemotePolicy {
	policy := testRemotePolicy(dialProject)
	policy.DiscoveryTimeout = time.Second
	return policy
}

func TestResolveWithoutStartersReturnsNoServerAvailable(t *testing.T) {
	// A pure client (no LaunchDaemon, no StartEmbedded) that cannot attach to a
	// configured remote must surface ErrNoServerAvailable rather than panicking
	// on a nil StartEmbedded. This backs the headless kent run pure-client path.
	resolution, err := Resolve[string](context.Background(), Request[string]{
		Mode:   ModeHeadless,
		Remote: testRemotePolicyWithDiscovery(unavailableProjectDial),
	})
	if !errors.Is(err, ErrNoServerAvailable) {
		t.Fatalf("err = %v, want ErrNoServerAvailable", err)
	}
	if resolution.Value != "" {
		t.Fatalf("resolution value = %q, want empty", resolution.Value)
	}
}

func boundProjectViewWithRoot(rootID string) *projectViewRemoteStub {
	return &projectViewRemoteStub{
		identity: protocol.ServerIdentity{Capabilities: compatibleCapabilities(), PersistenceRootID: rootID},
		plan: func(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
			return boundPlanResponse(), nil
		},
	}
}

func TestResolveReportsTypedPersistenceRootMismatch(t *testing.T) {
	for _, reportedRoot := range []string{"root-other", ""} {
		t.Run(reportedRoot, func(t *testing.T) {
			policy := testRemotePolicyWithDiscovery(func(context.Context, config.App) (ProjectViewRemote, error) {
				return boundProjectViewWithRoot(reportedRoot), nil
			})
			policy.RootID = "root-want"
			_, err := Resolve[string](context.Background(), Request[string]{Mode: ModeHeadless, Remote: policy})
			if !errors.Is(err, ErrReachableServerRootMismatch) || errors.Is(err, ErrNoServerAvailable) {
				t.Fatalf("err = %v, want root mismatch distinct from no server", err)
			}
			var mismatch *RootMismatchServerError
			if !errors.As(err, &mismatch) || strings.TrimSpace(mismatch.Reason) == "" {
				t.Fatalf("err = %v, want typed root mismatch with reason", err)
			}
		})
	}
}

func TestResolveAttachesConfiguredRemoteWithMatchingRootID(t *testing.T) {
	policy := testRemotePolicyWithDiscovery(func(context.Context, config.App) (ProjectViewRemote, error) {
		return boundProjectViewWithRoot("root-want"), nil
	})
	policy.RootID = "root-want"
	// The dialed workspace remote must also report the required root, since
	// RequireRoot now pins it for reconnect validation. Use a real server so the
	// handshake identity carries the matching persistence root id.
	dialWorkspace, closeServer := dialWorkspaceServerWithRoot(t, "root-want")
	defer closeServer()
	policy.DialWorkspace = dialWorkspace
	resolution, err := Resolve[string](context.Background(), Request[string]{
		Mode:   ModeHeadless,
		Remote: policy,
		WrapRemote: func(*client.Remote, config.App, func() error, OwnershipState) (Target[string], error) {
			return Target[string]{Value: "remote"}, nil
		},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolution.Source != SourceConfiguredRemote {
		t.Fatalf("source = %q, want configured remote", resolution.Source)
	}
	if resolution.Value != "remote" {
		t.Fatalf("value = %q, want remote", resolution.Value)
	}
}

// dialWorkspaceServerWithRoot starts a minimal RPC server whose handshake
// reports the given persistence root id, returning a DialWorkspace that attaches
// to it. It lets root-validation tests exercise the real client handshake path
// where ServerIdentity.PersistenceRootID is populated.
func dialWorkspaceServerWithRoot(t *testing.T, rootID string) (remoteattach.DialWorkspace, func()) {
	t.Helper()
	server := httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(func(ctx context.Context, conn rpcwire.Conn) {
		for event := range conn.Events() {
			if event.Err != nil {
				return
			}
			req := event.Frame.Request()
			switch req.Method {
			case protocol.MethodHandshake:
				_ = conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1", PersistenceRootID: rootID}})))
			case protocol.MethodAttachProject:
				_ = conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, protocol.AttachResponse{Kind: "project", ProjectID: "project-1"})))
			default:
				return
			}
		}
	}))
	wsURL := "ws" + server.URL[len("http"):]
	dial := func(ctx context.Context, _ config.App, projectID string, _ string) (*client.Remote, error) {
		return client.DialRemoteURLForProject(ctx, wsURL, projectID)
	}
	return dial, server.Close
}

func TestResolveReportsTypedIncompatibleServerReason(t *testing.T) {
	for _, tc := range []struct {
		name        string
		identity    protocol.ServerIdentity
		supports    remoteattach.Supports
		reasonParts []string
	}{
		{
			name:     "missing capabilities",
			identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version},
			supports: func(protocol.CapabilityFlags) bool { return false },
		},
		{
			name:        "protocol version mismatch",
			identity:    protocol.ServerIdentity{ProtocolVersion: "0.0.0-legacy", ServerID: "kent:7", PID: 7},
			supports:    remoteattach.SupportsRunPrompt,
			reasonParts: []string{"0.0.0-legacy", protocol.Version},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := testRemotePolicyWithDiscovery(func(context.Context, config.App) (ProjectViewRemote, error) {
				return &projectViewRemoteStub{identity: tc.identity}, nil
			})
			policy.Supports = tc.supports
			_, err := Resolve[string](context.Background(), Request[string]{Mode: ModeHeadless, Remote: policy})
			var incompatible *IncompatibleServerError
			if !errors.Is(err, ErrServerIncompatible) || errors.Is(err, ErrNoServerAvailable) ||
				!errors.As(err, &incompatible) || strings.TrimSpace(incompatible.Reason) == "" {
				t.Fatalf("err = %v, want typed incompatibility distinct from no server", err)
			}
			for _, part := range tc.reasonParts {
				if !strings.Contains(incompatible.Reason, part) {
					t.Fatalf("reason = %q, want %q", incompatible.Reason, part)
				}
			}
		})
	}
}

func TestResolveTargetResolutionPolicyTable(t *testing.T) {
	for _, tc := range []struct {
		name           string
		mode           Mode
		dialProject    func(context.Context, config.App) (ProjectViewRemote, error)
		redial         bool
		launchDaemon   func(context.Context, LaunchedRemoteDialer) (DaemonTarget[*client.Remote], bool, error)
		supports       remoteattach.Supports
		requireBound   bool
		wantSource     Source
		wantBinding    WorkspaceBindingState
		wantOwnership  OwnershipState
		wantCapability CapabilityCompatibility
		wantErr        error
		wantDialTarget string
	}{
		{
			name:        "interactive configured remote available",
			mode:        ModeInteractive,
			dialProject: boundProjectDial,
			launchDaemon: func(context.Context, LaunchedRemoteDialer) (DaemonTarget[*client.Remote], bool, error) {
				return DaemonTarget[*client.Remote]{}, false, errors.New("configured remote must win before daemon launch")
			},
			supports:       func(protocol.CapabilityFlags) bool { return true },
			wantSource:     SourceConfiguredRemote,
			wantBinding:    WorkspaceBindingInteractiveOptional,
			wantOwnership:  OwnershipExternalDaemon,
			wantCapability: CapabilityCompatibilityCompatible,
		},
		{
			name:           "interactive can require a registered binding",
			mode:           ModeInteractive,
			requireBound:   true,
			dialProject:    boundProjectDial,
			supports:       func(protocol.CapabilityFlags) bool { return true },
			wantSource:     SourceConfiguredRemote,
			wantBinding:    WorkspaceBindingInteractiveRequired,
			wantOwnership:  OwnershipExternalDaemon,
			wantCapability: CapabilityCompatibilityCompatible,
		},
		{
			name:   "headless configured remote unavailable launches daemon",
			mode:   ModeHeadless,
			redial: true,
			dialProject: func(context.Context, config.App) (ProjectViewRemote, error) {
				projectView := boundProjectView(func(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
					return boundPlanResponse(), nil
				})
				projectView.identity.PID = 42
				return projectView, nil
			},
			launchDaemon: func(ctx context.Context, dial LaunchedRemoteDialer) (DaemonTarget[*client.Remote], bool, error) {
				remote, ok, err := dial(ctx, func(identity protocol.ServerIdentity) bool { return identity.PID == 42 })
				return DaemonTarget[*client.Remote]{Value: remote}, ok, err
			},
			supports:       func(protocol.CapabilityFlags) bool { return true },
			wantSource:     SourceLaunchedDaemon,
			wantBinding:    WorkspaceBindingHeadlessRequired,
			wantOwnership:  OwnershipLaunchedDaemon,
			wantCapability: CapabilityCompatibilityCompatible,
		},
		{
			name:           "headless incompatible capabilities falls back embedded",
			mode:           ModeHeadless,
			dialProject:    boundProjectDial,
			supports:       func(protocol.CapabilityFlags) bool { return false },
			wantSource:     SourceEmbeddedFallback,
			wantBinding:    WorkspaceBindingHeadlessRequired,
			wantOwnership:  OwnershipEmbedded,
			wantCapability: CapabilityCompatibilityIncompatible,
		},
		{
			name:        "interactive daemon launch failure falls back embedded",
			mode:        ModeInteractive,
			dialProject: unavailableProjectDial,
			launchDaemon: func(context.Context, LaunchedRemoteDialer) (DaemonTarget[*client.Remote], bool, error) {
				return DaemonTarget[*client.Remote]{}, false, errors.New("daemon launch failed")
			},
			supports:       func(protocol.CapabilityFlags) bool { return true },
			wantSource:     SourceEmbeddedFallback,
			wantBinding:    WorkspaceBindingInteractiveOptional,
			wantOwnership:  OwnershipEmbedded,
			wantCapability: CapabilityCompatibilityUnchecked,
		},
		{
			name:        "headless unregistered workspace fails fast",
			mode:        ModeHeadless,
			dialProject: planProjectDial(serverapi.ProjectBindingPlanResponse{Kind: serverapi.ProjectBindingPlanKindLocalUnbound}),
			supports:    func(protocol.CapabilityFlags) bool { return true },
			wantErr:     serverapi.ErrWorkspaceNotRegistered,
		},
		{
			name: "headless remote workspace selection dials selected workspace",
			mode: ModeHeadless,
			dialProject: planProjectDial(serverapi.ProjectBindingPlanResponse{
				Kind:      serverapi.ProjectBindingPlanKindHeadlessRemoteSelected,
				Workspace: &serverapi.ProjectWorkspacePlanSelected{ProjectID: "remote-project", WorkspaceID: "remote-workspace"},
			}),
			supports:       func(protocol.CapabilityFlags) bool { return true },
			wantSource:     SourceConfiguredRemote,
			wantBinding:    WorkspaceBindingHeadlessRequired,
			wantOwnership:  OwnershipExternalDaemon,
			wantCapability: CapabilityCompatibilityCompatible,
			wantDialTarget: "remote-project/remote-workspace",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dialTarget := ""
			wrappedOwnership := OwnershipState("")
			dialProject := tc.dialProject
			if tc.redial {
				attempts, available := 0, dialProject
				dialProject = func(ctx context.Context, cfg config.App) (ProjectViewRemote, error) {
					attempts++
					if attempts == 1 {
						return unavailableProjectDial(ctx, cfg)
					}
					return available(ctx, cfg)
				}
			}
			resolution, err := Resolve[string](context.Background(), Request[string]{
				Mode: tc.mode,
				Remote: RemotePolicy{
					Config:           config.App{WorkspaceRoot: "/workspace"},
					AttachTimeout:    time.Second,
					DiscoveryTimeout: time.Second,
					DialProjectView:  dialProject,
					DialWorkspace: func(_ context.Context, _ config.App, projectID string, workspaceID string) (*client.Remote, error) {
						dialTarget = projectID + "/" + workspaceID
						return new(client.Remote), nil
					},
					Supports:     tc.supports,
					RequireBound: tc.mode == ModeHeadless || tc.requireBound,
				},
				LaunchDaemon: tc.launchDaemon,
				WrapRemote: func(_ *client.Remote, _ config.App, _ func() error, ownership OwnershipState) (Target[string], error) {
					wrappedOwnership = ownership
					return Target[string]{Value: "remote"}, nil
				},
				StartEmbedded: func(context.Context) (Target[string], error) {
					return Target[string]{Value: "embedded"}, nil
				},
			})
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Resolve error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if resolution.Source != tc.wantSource {
				t.Fatalf("source = %q, want %q", resolution.Source, tc.wantSource)
			}
			if resolution.WorkspaceBindingState != tc.wantBinding {
				t.Fatalf("binding = %q, want %q", resolution.WorkspaceBindingState, tc.wantBinding)
			}
			if resolution.Ownership != tc.wantOwnership {
				t.Fatalf("ownership = %q, want %q", resolution.Ownership, tc.wantOwnership)
			}
			if tc.wantOwnership != OwnershipEmbedded && wrappedOwnership != tc.wantOwnership {
				t.Fatalf("wrapped ownership = %q, want %q", wrappedOwnership, tc.wantOwnership)
			}
			if resolution.Capability != tc.wantCapability {
				t.Fatalf("capability = %q, want %q", resolution.Capability, tc.wantCapability)
			}
			if tc.wantDialTarget != "" && dialTarget != tc.wantDialTarget {
				t.Fatalf("workspace dial target = %q, want %q", dialTarget, tc.wantDialTarget)
			}
		})
	}
}

func TestResolveRecordsAuthReadinessFromValidation(t *testing.T) {
	resolution, err := Resolve[string](context.Background(), Request[string]{
		Mode:   ModeInteractive,
		Remote: testRemotePolicy(boundProjectDial),
		WrapRemote: func(_ *client.Remote, _ config.App, _ func() error, _ OwnershipState) (Target[string], error) {
			return Target[string]{Value: "remote"}, nil
		},
		StartEmbedded: func(context.Context) (Target[string], error) {
			return Target[string]{Value: "embedded"}, nil
		},
		Validate: func(context.Context, Resolution[string]) (AuthReadiness, error) {
			return AuthReadinessValidated, nil
		},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolution.Auth != AuthReadinessValidated {
		t.Fatalf("auth readiness = %q, want %q", resolution.Auth, AuthReadinessValidated)
	}
	if resolution.Capability != CapabilityCompatibilityCompatible {
		t.Fatalf("capability = %q, want %q", resolution.Capability, CapabilityCompatibilityCompatible)
	}
}

func TestResolveClosesOwnedTargetOnValidationFailure(t *testing.T) {
	wantErr := errors.New("auth bootstrap required")
	for _, source := range []Source{SourceConfiguredRemote, SourceLaunchedDaemon, SourceEmbeddedFallback} {
		t.Run(string(source), func(t *testing.T) {
			closed := 0
			ownedTarget := func(value string) Target[string] {
				return Target[string]{Value: value, Close: func() error {
					closed++
					return nil
				}}
			}
			req := Request[string]{
				Mode:   ModeInteractive,
				Remote: testRemotePolicyWithDiscovery(unavailableProjectDial),
				StartEmbedded: func(context.Context) (Target[string], error) {
					return ownedTarget("embedded"), nil
				},
			}
			if source != SourceEmbeddedFallback {
				req.WrapRemote = func(_ *client.Remote, _ config.App, _ func() error, _ OwnershipState) (Target[string], error) {
					return ownedTarget("remote"), nil
				}
			}
			switch source {
			case SourceConfiguredRemote:
				req.Remote = testRemotePolicy(boundProjectDial)
			case SourceLaunchedDaemon:
				req.LaunchDaemon = func(context.Context, LaunchedRemoteDialer) (DaemonTarget[*client.Remote], bool, error) {
					return DaemonTarget[*client.Remote]{Value: new(client.Remote)}, true, nil
				}
			}
			req.Validate = func(context.Context, Resolution[string]) (AuthReadiness, error) {
				return AuthReadinessUnchecked, wantErr
			}
			_, err := Resolve[string](context.Background(), req)
			if !errors.Is(err, wantErr) {
				t.Fatalf("Resolve error = %v, want %v", err, wantErr)
			}
			if closed != 1 {
				t.Fatalf("closed = %d, want 1", closed)
			}
		})
	}
}

func TestResolveDaemonWrapFailureClosesDaemonThenFallsBackEmbedded(t *testing.T) {
	wrapErr := errors.New("wrap failed")
	closed := 0
	resolution, err := Resolve[string](context.Background(), Request[string]{
		Mode:   ModeInteractive,
		Remote: testRemotePolicy(unavailableProjectDial),
		LaunchDaemon: func(context.Context, LaunchedRemoteDialer) (DaemonTarget[*client.Remote], bool, error) {
			return DaemonTarget[*client.Remote]{
				Value: new(client.Remote),
				Close: func() error {
					closed++
					return nil
				},
			}, true, nil
		},
		WrapRemote: func(_ *client.Remote, _ config.App, _ func() error, _ OwnershipState) (Target[string], error) {
			return Target[string]{}, wrapErr
		},
		StartEmbedded: func(context.Context) (Target[string], error) {
			return Target[string]{Value: "embedded"}, nil
		},
	})
	if err != nil {
		t.Fatalf("Resolve should fall back to embedded after daemon wrap failure: %v", err)
	}
	if resolution.Source != SourceEmbeddedFallback {
		t.Fatalf("source = %q, want %q", resolution.Source, SourceEmbeddedFallback)
	}
	if closed != 1 {
		t.Fatalf("daemon close count = %d, want 1", closed)
	}
}

func TestResolveJoinsLaunchAndEmbeddedErrors(t *testing.T) {
	launchErr := errors.New("daemon launch failed")
	embeddedErr := errors.New("embedded start failed")
	_, err := Resolve[string](context.Background(), Request[string]{
		Mode:   ModeInteractive,
		Remote: testRemotePolicy(unavailableProjectDial),
		LaunchDaemon: func(context.Context, LaunchedRemoteDialer) (DaemonTarget[*client.Remote], bool, error) {
			return DaemonTarget[*client.Remote]{}, false, launchErr
		},
		StartEmbedded: func(context.Context) (Target[string], error) {
			return Target[string]{}, embeddedErr
		},
	})
	if !errors.Is(err, launchErr) || !errors.Is(err, embeddedErr) {
		t.Fatalf("Resolve error = %v, want joined launch and embedded errors", err)
	}
}
