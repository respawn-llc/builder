package serverattach

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"core/cli/app/internal/remoteattach"
	"core/shared/client"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/serverapi"
)

type projectViewRemoteStub struct {
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

func allCapabilities() protocol.CapabilityFlags {
	return protocol.CapabilityFlags{
		AuthBootstrap:    true,
		ProjectAttach:    true,
		RunPrompt:        true,
		SessionPlan:      true,
		SessionLifecycle: true,
		SessionRuntime:   true,
		RuntimeControl:   true,
		PromptControl:    true,
		PromptActivity:   true,
		SessionActivity:  true,
		ProcessOutput:    true,
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
		identity: protocol.ServerIdentity{Capabilities: allCapabilities()},
		plan:     plan,
	}
}

func testDialWorkspace(context.Context, config.App, string, string) (*client.Remote, error) {
	return new(client.Remote), nil
}

func boundProjectDial(context.Context, config.App) (ProjectViewRemote, error) {
	return boundProjectView(func(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
		return boundPlanResponse(), nil
	}), nil
}

func unavailableProjectDial(context.Context, config.App) (ProjectViewRemote, error) {
	return nil, errors.New("configured remote unavailable")
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

func TestResolveUsesSharedRemoteDaemonEmbeddedPolicyByMode(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mode        Mode
		planMode    serverapi.ProjectBindingPlanMode
		planKind    serverapi.ProjectBindingPlanKind
		requireBind bool
		wantBinding WorkspaceBindingState
	}{
		{
			name:        "interactive uses optional registration policy",
			mode:        ModeInteractive,
			planMode:    serverapi.ProjectBindingPlanModeInteractive,
			planKind:    serverapi.ProjectBindingPlanKindBound,
			requireBind: false,
			wantBinding: WorkspaceBindingInteractiveOptional,
		},
		{
			name:        "interactive daemon launch can require registered binding",
			mode:        ModeInteractive,
			planMode:    serverapi.ProjectBindingPlanModeInteractive,
			planKind:    serverapi.ProjectBindingPlanKindBound,
			requireBind: true,
			wantBinding: WorkspaceBindingInteractiveRequired,
		},
		{
			name:        "headless requires server binding policy",
			mode:        ModeHeadless,
			planMode:    serverapi.ProjectBindingPlanModeHeadless,
			planKind:    serverapi.ProjectBindingPlanKindBound,
			requireBind: true,
			wantBinding: WorkspaceBindingHeadlessRequired,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var modes []serverapi.ProjectBindingPlanMode
			remotePolicy := testRemotePolicy(func(context.Context, config.App) (ProjectViewRemote, error) {
				return boundProjectView(func(_ context.Context, req serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
					modes = append(modes, req.Mode)
					if tc.planKind == serverapi.ProjectBindingPlanKindBound {
						return serverapi.ProjectBindingPlanResponse{
							Kind:    tc.planKind,
							Binding: &serverapi.ProjectBinding{ProjectID: "project-1", WorkspaceID: "workspace-1"},
						}, nil
					}
					return serverapi.ProjectBindingPlanResponse{Kind: tc.planKind}, nil
				}), nil
			})
			remotePolicy.RequireBound = tc.requireBind
			resolution, err := Resolve[string](context.Background(), Request[string]{
				Mode:   tc.mode,
				Remote: remotePolicy,
				WrapRemote: func(_ *client.Remote, _ config.App, _ func() error, _ OwnershipState) (Target[string], error) {
					return Target[string]{Value: "remote"}, nil
				},
				StartEmbedded: func(context.Context) (Target[string], error) {
					return Target[string]{Value: "embedded"}, nil
				},
			})
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if resolution.Value != "remote" {
				t.Fatalf("value = %q, want remote", resolution.Value)
			}
			if resolution.Mode != tc.mode {
				t.Fatalf("mode = %q, want %q", resolution.Mode, tc.mode)
			}
			if resolution.WorkspaceBindingState != tc.wantBinding {
				t.Fatalf("binding = %q, want %q", resolution.WorkspaceBindingState, tc.wantBinding)
			}
			if !reflect.DeepEqual(modes, []serverapi.ProjectBindingPlanMode{tc.planMode}) {
				t.Fatalf("plan modes = %v, want %v", modes, []serverapi.ProjectBindingPlanMode{tc.planMode})
			}
		})
	}
}

func TestResolveLaunchesDaemonWithSameRemoteAttachmentPolicy(t *testing.T) {
	acceptedPID := 0
	dialProjectView := func(context.Context, config.App) (ProjectViewRemote, error) {
		return &projectViewRemoteStub{
			identity: protocol.ServerIdentity{PID: 42, Capabilities: protocol.CapabilityFlags{RunPrompt: true, AuthBootstrap: true, ProjectAttach: true}},
			plan: func(_ context.Context, _ serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
				return serverapi.ProjectBindingPlanResponse{
					Kind:    serverapi.ProjectBindingPlanKindBound,
					Binding: &serverapi.ProjectBinding{ProjectID: "project-1", WorkspaceID: "workspace-1"},
				}, nil
			},
		}, nil
	}
	resolution, err := Resolve[string](context.Background(), Request[string]{
		Mode:   ModeHeadless,
		Remote: testRemotePolicyWithDiscovery(dialProjectView),
		LaunchDaemon: func(ctx context.Context, dial LaunchedRemoteDialer) (DaemonTarget[*client.Remote], bool, error) {
			remote, ok, err := dial(ctx, func(identity protocol.ServerIdentity) bool {
				acceptedPID = identity.PID
				return identity.PID == 42
			})
			return DaemonTarget[*client.Remote]{Value: remote}, ok, err
		},
		WrapRemote: func(_ *client.Remote, _ config.App, _ func() error, _ OwnershipState) (Target[string], error) {
			return Target[string]{Value: "daemon"}, nil
		},
		StartEmbedded: func(context.Context) (Target[string], error) {
			return Target[string]{Value: "embedded"}, nil
		},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolution.Source != SourceConfiguredRemote {
		t.Fatalf("source = %q, want configured remote before daemon", resolution.Source)
	}

	acceptedPID = 0
	dialAttempts := 0
	resolution, err = Resolve[string](context.Background(), Request[string]{
		Mode: ModeHeadless,
		Remote: testRemotePolicyWithDiscovery(func(context.Context, config.App) (ProjectViewRemote, error) {
			dialAttempts++
			if dialAttempts == 1 {
				return nil, errors.New("configured remote unavailable")
			}
			return dialProjectView(context.Background(), config.App{})
		}),
		LaunchDaemon: func(ctx context.Context, dial LaunchedRemoteDialer) (DaemonTarget[*client.Remote], bool, error) {
			remote, ok, err := dial(ctx, func(identity protocol.ServerIdentity) bool {
				acceptedPID = identity.PID
				return identity.PID == 42
			})
			return DaemonTarget[*client.Remote]{Value: remote}, ok, err
		},
		WrapRemote: func(_ *client.Remote, _ config.App, _ func() error, _ OwnershipState) (Target[string], error) {
			return Target[string]{Value: "daemon"}, nil
		},
		StartEmbedded: func(context.Context) (Target[string], error) {
			return Target[string]{Value: "embedded"}, nil
		},
	})
	if err != nil {
		t.Fatalf("Resolve with daemon path: %v", err)
	}
	if resolution.Source != SourceLaunchedDaemon {
		t.Fatalf("source = %q, want launched daemon", resolution.Source)
	}
	if acceptedPID != 42 {
		t.Fatalf("accepted pid = %d, want 42", acceptedPID)
	}
}

func TestResolveWithoutStartersReturnsNoServerAvailable(t *testing.T) {
	// A pure client (no LaunchDaemon, no StartEmbedded) that cannot attach to a
	// configured remote must surface ErrNoServerAvailable rather than panicking
	// on a nil StartEmbedded. This backs the headless kent run pure-client path.
	resolution, err := Resolve[string](context.Background(), Request[string]{
		Mode: ModeHeadless,
		Remote: testRemotePolicyWithDiscovery(func(context.Context, config.App) (ProjectViewRemote, error) {
			return nil, errors.New("configured remote unavailable")
		}),
		WrapRemote: func(_ *client.Remote, _ config.App, _ func() error, _ OwnershipState) (Target[string], error) {
			return Target[string]{Value: "remote"}, nil
		},
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
		identity: protocol.ServerIdentity{Capabilities: allCapabilities(), PersistenceRootID: rootID},
		plan: func(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
			return boundPlanResponse(), nil
		},
	}
}

func TestRootMatches(t *testing.T) {
	if !rootMatches("", protocol.ServerIdentity{PersistenceRootID: "anything"}) {
		t.Fatal("no required root must match any identity")
	}
	if !rootMatches("root-want", protocol.ServerIdentity{PersistenceRootID: "root-want"}) {
		t.Fatal("matching root must match")
	}
	if rootMatches("root-want", protocol.ServerIdentity{PersistenceRootID: "root-other"}) {
		t.Fatal("mismatched root must not match")
	}
	if rootMatches("root-want", protocol.ServerIdentity{}) {
		t.Fatal("server with no reported root must not match when a root is required")
	}
}

func TestResolveRejectsConfiguredRemoteWithMismatchedRootID(t *testing.T) {
	// A reachable server serving a different root, with no local starter, must
	// surface ErrReachableServerRootMismatch (not ErrNoServerAvailable) so the run
	// path tells the operator to stop/reconfigure the other-root server rather
	// than start another that would conflict on the same address.
	policy := testRemotePolicyWithDiscovery(func(context.Context, config.App) (ProjectViewRemote, error) {
		return boundProjectViewWithRoot("root-other"), nil
	})
	policy.RootID = "root-want"
	_, err := Resolve[string](context.Background(), Request[string]{
		Mode:   ModeHeadless,
		Remote: policy,
		WrapRemote: func(*client.Remote, config.App, func() error, OwnershipState) (Target[string], error) {
			return Target[string]{Value: "remote"}, nil
		},
	})
	if !errors.Is(err, ErrReachableServerRootMismatch) {
		t.Fatalf("err = %v, want ErrReachableServerRootMismatch for mismatched root", err)
	}
	if errors.Is(err, ErrNoServerAvailable) {
		t.Fatal("a reachable wrong-root server must not be reported as no server available")
	}
	var mismatch *RootMismatchServerError
	if !errors.As(err, &mismatch) {
		t.Fatalf("err = %v, want *RootMismatchServerError", err)
	}
	if strings.TrimSpace(mismatch.Reason) == "" {
		t.Fatal("root mismatch error must carry a reason naming the other server")
	}
}

func TestResolveReportsRootMismatchForServerReportingNoRoot(t *testing.T) {
	// An older server that reports no persistence root must be surfaced as a root
	// mismatch when a root is required, not as "no server available".
	policy := testRemotePolicyWithDiscovery(func(context.Context, config.App) (ProjectViewRemote, error) {
		return boundProjectViewWithRoot(""), nil
	})
	policy.RootID = "root-want"
	_, err := Resolve[string](context.Background(), Request[string]{
		Mode:   ModeHeadless,
		Remote: policy,
		WrapRemote: func(*client.Remote, config.App, func() error, OwnershipState) (Target[string], error) {
			return Target[string]{Value: "remote"}, nil
		},
	})
	if !errors.Is(err, ErrReachableServerRootMismatch) {
		t.Fatalf("err = %v, want ErrReachableServerRootMismatch for a server reporting no root", err)
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

func TestResolveWithoutStartersReportsIncompatibleServer(t *testing.T) {
	// A reachable server that fails the capability check, with no local starter,
	// must surface ErrServerIncompatible (not ErrNoServerAvailable) so the run
	// path can tell the operator to restart/upgrade the running server.
	policy := testRemotePolicyWithDiscovery(boundProjectDial)
	policy.Supports = func(protocol.CapabilityFlags) bool { return false }
	_, err := Resolve[string](context.Background(), Request[string]{
		Mode:   ModeHeadless,
		Remote: policy,
		WrapRemote: func(*client.Remote, config.App, func() error, OwnershipState) (Target[string], error) {
			return Target[string]{Value: "remote"}, nil
		},
	})
	if !errors.Is(err, ErrServerIncompatible) {
		t.Fatalf("err = %v, want ErrServerIncompatible", err)
	}
	if errors.Is(err, ErrNoServerAvailable) {
		t.Fatal("incompatible server must not be reported as no server available")
	}
	var incompatible *IncompatibleServerError
	if !errors.As(err, &incompatible) {
		t.Fatalf("err = %v, want *IncompatibleServerError", err)
	}
	if strings.TrimSpace(incompatible.Reason) == "" {
		t.Fatal("incompatible server error must carry a reason explaining why")
	}
}

func TestResolveIncompatibleServerReportsProtocolVersionReason(t *testing.T) {
	// A server reporting a different protocol version must surface that version
	// skew as the reason, so the operator learns it is an older/newer build.
	policy := testRemotePolicyWithDiscovery(func(context.Context, config.App) (ProjectViewRemote, error) {
		return &projectViewRemoteStub{
			identity: protocol.ServerIdentity{ProtocolVersion: "0.0.0-legacy", ServerID: "kent:7", PID: 7},
			plan: func(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
				return boundPlanResponse(), nil
			},
		}, nil
	})
	policy.Supports = remoteattach.SupportsRunPrompt
	_, err := Resolve[string](context.Background(), Request[string]{
		Mode:   ModeHeadless,
		Remote: policy,
		WrapRemote: func(*client.Remote, config.App, func() error, OwnershipState) (Target[string], error) {
			return Target[string]{Value: "remote"}, nil
		},
	})
	var incompatible *IncompatibleServerError
	if !errors.As(err, &incompatible) {
		t.Fatalf("err = %v, want *IncompatibleServerError", err)
	}
	if !strings.Contains(incompatible.Reason, "0.0.0-legacy") || !strings.Contains(incompatible.Reason, protocol.Version) {
		t.Fatalf("reason = %q, want it to name the reported and required protocol versions", incompatible.Reason)
	}
}

func TestResolvePassesRemoteOwnershipToWrapper(t *testing.T) {
	for _, tt := range []struct {
		name          string
		dialProject   func(context.Context, config.App) (ProjectViewRemote, error)
		launchDaemon  func(context.Context, LaunchedRemoteDialer) (DaemonTarget[*client.Remote], bool, error)
		wantOwnership OwnershipState
	}{
		{
			name: "configured remote is external",
			dialProject: func(context.Context, config.App) (ProjectViewRemote, error) {
				return boundProjectView(func(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
					return boundPlanResponse(), nil
				}), nil
			},
			wantOwnership: OwnershipExternalDaemon,
		},
		{
			name: "launched daemon is owned",
			dialProject: func(context.Context, config.App) (ProjectViewRemote, error) {
				return nil, errors.New("configured remote unavailable")
			},
			launchDaemon: func(context.Context, LaunchedRemoteDialer) (DaemonTarget[*client.Remote], bool, error) {
				return DaemonTarget[*client.Remote]{Value: new(client.Remote)}, true, nil
			},
			wantOwnership: OwnershipLaunchedDaemon,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gotOwnership := OwnershipState("")
			resolution, err := Resolve[string](context.Background(), Request[string]{
				Mode:         ModeInteractive,
				Remote:       testRemotePolicy(tt.dialProject),
				LaunchDaemon: tt.launchDaemon,
				WrapRemote: func(_ *client.Remote, _ config.App, _ func() error, ownership OwnershipState) (Target[string], error) {
					gotOwnership = ownership
					return Target[string]{Value: "remote"}, nil
				},
				StartEmbedded: func(context.Context) (Target[string], error) {
					return Target[string]{Value: "embedded"}, nil
				},
			})
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if resolution.Value != "remote" {
				t.Fatalf("value = %q, want remote", resolution.Value)
			}
			if gotOwnership != tt.wantOwnership {
				t.Fatalf("ownership = %q, want %q", gotOwnership, tt.wantOwnership)
			}
		})
	}
}

func TestResolveTargetResolutionPolicyTable(t *testing.T) {
	for _, tc := range []struct {
		name           string
		mode           Mode
		dialProject    func(*testing.T, *string) func(context.Context, config.App) (ProjectViewRemote, error)
		launchDaemon   func(context.Context, LaunchedRemoteDialer) (DaemonTarget[*client.Remote], bool, error)
		supports       remoteattach.Supports
		wantSource     Source
		wantCapability CapabilityCompatibility
		wantErr        error
		wantDialTarget string
	}{
		{
			name: "interactive configured remote available",
			mode: ModeInteractive,
			dialProject: func(t *testing.T, _ *string) func(context.Context, config.App) (ProjectViewRemote, error) {
				t.Helper()
				return func(context.Context, config.App) (ProjectViewRemote, error) {
					return boundProjectView(func(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
						return boundPlanResponse(), nil
					}), nil
				}
			},
			supports:       func(protocol.CapabilityFlags) bool { return true },
			wantSource:     SourceConfiguredRemote,
			wantCapability: CapabilityCompatibilityCompatible,
		},
		{
			name: "headless configured remote unavailable launches daemon",
			mode: ModeHeadless,
			dialProject: func(t *testing.T, _ *string) func(context.Context, config.App) (ProjectViewRemote, error) {
				t.Helper()
				attempts := 0
				return func(context.Context, config.App) (ProjectViewRemote, error) {
					attempts++
					if attempts == 1 {
						return nil, errors.New("configured remote unavailable")
					}
					return boundProjectView(func(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
						return boundPlanResponse(), nil
					}), nil
				}
			},
			launchDaemon: func(ctx context.Context, dial LaunchedRemoteDialer) (DaemonTarget[*client.Remote], bool, error) {
				remote, ok, err := dial(ctx, nil)
				return DaemonTarget[*client.Remote]{Value: remote}, ok, err
			},
			supports:       func(protocol.CapabilityFlags) bool { return true },
			wantSource:     SourceLaunchedDaemon,
			wantCapability: CapabilityCompatibilityCompatible,
		},
		{
			name: "headless incompatible capabilities falls back embedded",
			mode: ModeHeadless,
			dialProject: func(t *testing.T, _ *string) func(context.Context, config.App) (ProjectViewRemote, error) {
				t.Helper()
				return func(context.Context, config.App) (ProjectViewRemote, error) {
					return boundProjectView(func(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
						t.Fatal("unsupported remote should be skipped before workspace planning")
						return serverapi.ProjectBindingPlanResponse{}, nil
					}), nil
				}
			},
			supports:       func(protocol.CapabilityFlags) bool { return false },
			wantSource:     SourceEmbeddedFallback,
			wantCapability: CapabilityCompatibilityIncompatible,
		},
		{
			name: "interactive daemon launch failure falls back embedded",
			mode: ModeInteractive,
			dialProject: func(t *testing.T, _ *string) func(context.Context, config.App) (ProjectViewRemote, error) {
				t.Helper()
				return func(context.Context, config.App) (ProjectViewRemote, error) {
					return nil, errors.New("configured remote unavailable")
				}
			},
			launchDaemon: func(context.Context, LaunchedRemoteDialer) (DaemonTarget[*client.Remote], bool, error) {
				return DaemonTarget[*client.Remote]{}, false, errors.New("daemon launch failed")
			},
			supports:       func(protocol.CapabilityFlags) bool { return true },
			wantSource:     SourceEmbeddedFallback,
			wantCapability: CapabilityCompatibilityUnchecked,
		},
		{
			name: "headless unregistered workspace fails fast",
			mode: ModeHeadless,
			dialProject: func(t *testing.T, _ *string) func(context.Context, config.App) (ProjectViewRemote, error) {
				t.Helper()
				return func(context.Context, config.App) (ProjectViewRemote, error) {
					return boundProjectView(func(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
						return serverapi.ProjectBindingPlanResponse{Kind: serverapi.ProjectBindingPlanKindLocalUnbound}, nil
					}), nil
				}
			},
			supports: func(protocol.CapabilityFlags) bool { return true },
			wantErr:  serverapi.ErrWorkspaceNotRegistered,
		},
		{
			name: "headless remote workspace selection dials selected workspace",
			mode: ModeHeadless,
			dialProject: func(t *testing.T, _ *string) func(context.Context, config.App) (ProjectViewRemote, error) {
				t.Helper()
				return func(context.Context, config.App) (ProjectViewRemote, error) {
					return boundProjectView(func(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
						return serverapi.ProjectBindingPlanResponse{
							Kind:      serverapi.ProjectBindingPlanKindHeadlessRemoteSelected,
							Workspace: &serverapi.ProjectWorkspacePlanSelected{ProjectID: "remote-project", WorkspaceID: "remote-workspace"},
						}, nil
					}), nil
				}
			},
			supports:       func(protocol.CapabilityFlags) bool { return true },
			wantSource:     SourceConfiguredRemote,
			wantCapability: CapabilityCompatibilityCompatible,
			wantDialTarget: "remote-project/remote-workspace",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dialTarget := ""
			resolution, err := Resolve[string](context.Background(), Request[string]{
				Mode: tc.mode,
				Remote: RemotePolicy{
					Config:           config.App{WorkspaceRoot: "/workspace"},
					AttachTimeout:    time.Second,
					DiscoveryTimeout: time.Second,
					DialProjectView:  tc.dialProject(t, &dialTarget),
					DialWorkspace: func(_ context.Context, _ config.App, projectID string, workspaceID string) (*client.Remote, error) {
						dialTarget = projectID + "/" + workspaceID
						return new(client.Remote), nil
					},
					Supports:     tc.supports,
					RequireBound: tc.mode == ModeHeadless,
				},
				LaunchDaemon: tc.launchDaemon,
				WrapRemote: func(_ *client.Remote, _ config.App, _ func() error, _ OwnershipState) (Target[string], error) {
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
	for _, tc := range []struct {
		name string
		req  func(*int) Request[string]
	}{
		{
			name: "configured remote",
			req: func(closed *int) Request[string] {
				return Request[string]{
					Mode:   ModeInteractive,
					Remote: testRemotePolicy(boundProjectDial),
					WrapRemote: func(_ *client.Remote, _ config.App, _ func() error, _ OwnershipState) (Target[string], error) {
						return Target[string]{Value: "remote", Close: func() error {
							*closed = *closed + 1
							return nil
						}}, nil
					},
					StartEmbedded: func(context.Context) (Target[string], error) {
						return Target[string]{Value: "embedded"}, nil
					},
				}
			},
		},
		{
			name: "launched daemon",
			req: func(closed *int) Request[string] {
				return Request[string]{
					Mode:   ModeInteractive,
					Remote: testRemotePolicyWithDiscovery(unavailableProjectDial),
					LaunchDaemon: func(context.Context, LaunchedRemoteDialer) (DaemonTarget[*client.Remote], bool, error) {
						return DaemonTarget[*client.Remote]{Value: new(client.Remote)}, true, nil
					},
					WrapRemote: func(_ *client.Remote, _ config.App, _ func() error, _ OwnershipState) (Target[string], error) {
						return Target[string]{Value: "daemon", Close: func() error {
							*closed = *closed + 1
							return nil
						}}, nil
					},
					StartEmbedded: func(context.Context) (Target[string], error) {
						return Target[string]{Value: "embedded"}, nil
					},
				}
			},
		},
		{
			name: "embedded fallback",
			req: func(closed *int) Request[string] {
				return Request[string]{
					Mode: ModeInteractive,
					Remote: RemotePolicy{
						Config: config.App{WorkspaceRoot: "/workspace"},
						DialProjectView: func(context.Context, config.App) (ProjectViewRemote, error) {
							return nil, errors.New("configured remote unavailable")
						},
					},
					StartEmbedded: func(context.Context) (Target[string], error) {
						return Target[string]{Value: "embedded", Close: func() error {
							*closed = *closed + 1
							return nil
						}}, nil
					},
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			closed := 0
			req := tc.req(&closed)
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
		Mode: ModeInteractive,
		Remote: RemotePolicy{
			Config: config.App{WorkspaceRoot: "/workspace"},
			DialProjectView: func(context.Context, config.App) (ProjectViewRemote, error) {
				return nil, errors.New("configured remote unavailable")
			},
		},
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
		Mode: ModeInteractive,
		Remote: RemotePolicy{
			Config: config.App{WorkspaceRoot: "/workspace"},
			DialProjectView: func(context.Context, config.App) (ProjectViewRemote, error) {
				return nil, errors.New("configured remote unavailable")
			},
		},
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
