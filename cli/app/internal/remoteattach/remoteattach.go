package remoteattach

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"core/shared/client"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/serverapi"
)

// errProjectViewDialerRequired and errWorkspaceDialerRequired report that a
// headless attach request omitted a required dialer dependency.
var (
	errProjectViewDialerRequired = errors.New("project view dialer is required")
	errWorkspaceDialerRequired   = errors.New("workspace dialer is required")
)

type ProjectViewRemote interface {
	client.ProjectViewClient
	Close() error
	Identity() protocol.ServerIdentity
	// RequireRoot pins the persistence-root id that every (re)connect handshake
	// on this connection must report, so a control connection that drops and
	// reconnects over the fallback TCP endpoint cannot silently serve a
	// different root. An empty id disables validation.
	RequireRoot(rootID string) error
}

type DialProjectView func(context.Context, config.App) (ProjectViewRemote, error)
type DialWorkspace func(context.Context, config.App, string, string) (*client.Remote, error)
type Supports func(protocol.CapabilityFlags) bool
type Accept func(protocol.ServerIdentity) bool

type HeadlessRequest struct {
	Config           config.App
	AttachTimeout    time.Duration
	DiscoveryTimeout time.Duration
	DialProjectView  DialProjectView
	DialWorkspace    DialWorkspace
	Accept           Accept
	Supports         Supports
	// RootID, when non-empty, is pinned on the attached remote so every
	// (re)connect handshake must report a matching persistence root id.
	RootID string
}

type InteractiveRequest struct {
	Config          config.App
	AttachTimeout   time.Duration
	DialProjectView DialProjectView
	DialWorkspace   DialWorkspace
	Accept          Accept
	Supports        Supports
	RequireBound    bool
	// RootID, when non-empty, is pinned on the attached remote so every
	// (re)connect handshake must report a matching persistence root id.
	RootID string
}

func DialHeadless(ctx context.Context, req HeadlessRequest) (*client.Remote, bool, error) {
	if req.DialProjectView == nil {
		return nil, false, errProjectViewDialerRequired
	}
	if req.DialWorkspace == nil {
		return nil, false, errWorkspaceDialerRequired
	}
	attachCtx, cancel := context.WithTimeout(ctx, req.AttachTimeout)
	defer cancel()
	projectViews, err := req.DialProjectView(attachCtx, req.Config)
	if err != nil {
		return nil, false, nil
	}
	if req.Accept != nil && !req.Accept(projectViews.Identity()) {
		_ = projectViews.Close()
		return nil, false, nil
	}
	if req.Supports != nil && !req.Supports(projectViews.Identity().Capabilities) {
		_ = projectViews.Close()
		return nil, false, nil
	}
	// Pin the expected root before the first discovery RPC so a control
	// connection that drops between identity acceptance and discovery cannot
	// reconnect over the fallback TCP endpoint and resolve a binding plan from a
	// different root before validation runs.
	if err := projectViews.RequireRoot(req.RootID); err != nil {
		_ = projectViews.Close()
		return nil, true, err
	}
	discoveryCtx, discoveryCancel := context.WithTimeout(ctx, req.DiscoveryTimeout)
	plan, err := projectViews.PlanWorkspaceBinding(discoveryCtx, serverapi.ProjectBindingPlanRequest{Path: req.Config.WorkspaceRoot, Mode: serverapi.ProjectBindingPlanModeHeadless})
	discoveryCancel()
	if err != nil {
		_ = projectViews.Close()
		return nil, true, err
	}
	switch plan.Kind {
	case serverapi.ProjectBindingPlanKindBound:
		if plan.Binding == nil {
			_ = projectViews.Close()
			return nil, true, errors.New("resolved project binding is required")
		}
		_ = projectViews.Close()
		remote, err := dialWorkspaceWithTimeout(ctx, req.Config, req.AttachTimeout, req.DialWorkspace, plan.Binding.ProjectID, plan.Binding.WorkspaceID)
		if err != nil {
			return nil, true, err
		}
		if err := remote.RequireRoot(req.RootID); err != nil {
			_ = remote.Close()
			return nil, true, err
		}
		return remote, true, nil
	case serverapi.ProjectBindingPlanKindLocalUnbound:
		_ = projectViews.Close()
		return nil, true, HeadlessWorkspaceRegistrationError(req.Config.WorkspaceRoot)
	case serverapi.ProjectBindingPlanKindHeadlessRemoteAmbiguous:
		_ = projectViews.Close()
		return nil, true, errors.New("remote server could not resolve the current workspace and no single server workspace could be chosen automatically. Run `kent project list`, `kent project create --path <server-path> --name <project-name>`, or `kent attach --project <project-id> <server-path>` against the configured server, or start interactive Kent to choose an existing server project/workspace")
	case serverapi.ProjectBindingPlanKindHeadlessRemoteSelected:
		if plan.Workspace == nil {
			_ = projectViews.Close()
			return nil, true, errors.New("resolved remote workspace is required")
		}
		_ = projectViews.Close()
		remote, err := dialWorkspaceWithTimeout(ctx, req.Config, req.AttachTimeout, req.DialWorkspace, plan.Workspace.ProjectID, plan.Workspace.WorkspaceID)
		if err != nil {
			return nil, true, err
		}
		if err := remote.RequireRoot(req.RootID); err != nil {
			_ = remote.Close()
			return nil, true, err
		}
		return remote, true, nil
	default:
		_ = projectViews.Close()
		return nil, true, fmt.Errorf("unsupported headless project binding plan %q", plan.Kind)
	}
}

func DialInteractive(ctx context.Context, req InteractiveRequest) (*client.Remote, bool) {
	if req.DialProjectView == nil || req.DialWorkspace == nil {
		return nil, false
	}
	attachCtx, cancel := context.WithTimeout(ctx, req.AttachTimeout)
	defer cancel()
	projectViews, err := req.DialProjectView(attachCtx, req.Config)
	if err != nil {
		return nil, false
	}
	if req.Accept != nil && !req.Accept(projectViews.Identity()) {
		_ = projectViews.Close()
		return nil, false
	}
	if req.Supports != nil && !req.Supports(projectViews.Identity().Capabilities) {
		_ = projectViews.Close()
		return nil, false
	}
	// Pin the expected root before the first discovery RPC so a dropped control
	// connection cannot reconnect over the fallback TCP endpoint and resolve a
	// binding from a different root before validation runs.
	if err := projectViews.RequireRoot(req.RootID); err != nil {
		_ = projectViews.Close()
		return nil, false
	}
	binding, resolveErr := resolveInteractiveBinding(attachCtx, projectViews, req.Config.WorkspaceRoot)
	if resolveErr != nil {
		_ = projectViews.Close()
		return nil, false
	}
	if binding == nil {
		if req.RequireBound {
			_ = projectViews.Close()
			return nil, false
		}
		remote, ok := projectViews.(*client.Remote)
		if !ok {
			_ = projectViews.Close()
			return nil, false
		}
		// projectViews was already pinned via RequireRoot above, so the
		// unbound remote we return keeps validating its root on every reconnect.
		return remote, true
	}
	_ = projectViews.Close()
	remote, err := dialWorkspaceWithTimeout(ctx, req.Config, req.AttachTimeout, req.DialWorkspace, binding.ProjectID, binding.WorkspaceID)
	if err != nil {
		return nil, false
	}
	if err := remote.RequireRoot(req.RootID); err != nil {
		_ = remote.Close()
		return nil, false
	}
	return remote, true
}

func SupportsRunPrompt(flags protocol.CapabilityFlags) bool {
	return flags.RunPrompt && flags.AuthBootstrap && flags.ProjectAttach
}

func SupportsRuntimeLiveControl(flags protocol.CapabilityFlags) bool {
	return flags.RuntimeLiveControl && flags.RuntimeControl && flags.AuthBootstrap
}

func SupportsInteractiveSession(flags protocol.CapabilityFlags) bool {
	return flags.AuthBootstrap &&
		flags.ProjectAttach &&
		flags.SessionPlan &&
		flags.SessionLifecycle &&
		flags.SessionRuntime &&
		flags.RuntimeControl &&
		flags.PromptControl &&
		flags.PromptActivity &&
		flags.SessionActivity &&
		flags.ProcessOutput
}

func HeadlessWorkspaceRegistrationError(workspaceRoot string) error {
	trimmedRoot := strings.TrimSpace(workspaceRoot)
	if trimmedRoot == "" {
		trimmedRoot = "current workspace"
	}
	return fmt.Errorf("%w: %s is not attached to a project. Run `kent project` in a workspace that already belongs to the target project, then run `kent attach <path>` from there or `kent attach --project <project-id> <path>`", serverapi.ErrWorkspaceNotRegistered, trimmedRoot)
}

func resolveInteractiveBinding(ctx context.Context, projectViews client.ProjectViewClient, workspaceRoot string) (*serverapi.ProjectBinding, error) {
	resp, err := projectViews.PlanWorkspaceBinding(ctx, serverapi.ProjectBindingPlanRequest{Path: workspaceRoot, Mode: serverapi.ProjectBindingPlanModeInteractive})
	if err != nil {
		return nil, err
	}
	if resp.Kind != serverapi.ProjectBindingPlanKindBound {
		return nil, nil
	}
	return resp.Binding, nil
}

func dialWorkspaceWithTimeout(ctx context.Context, cfg config.App, timeout time.Duration, dial DialWorkspace, projectID string, workspaceID string) (*client.Remote, error) {
	attachCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return dial(attachCtx, cfg, projectID, workspaceID)
}
