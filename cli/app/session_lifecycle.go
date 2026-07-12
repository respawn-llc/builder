package app

import (
	"context"
	"errors"
	"os"
	"strings"

	"core/cli/app/commands"
	"core/shared/client"
	"core/shared/config"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

type sessionLifecycleClientProvider interface {
	SessionLifecycleClient() client.SessionLifecycleClient
}

type sessionConfigProvider interface {
	Config() config.App
}

type sessionInitialInputServer interface {
	sessionLifecycleClientProvider
}

type sessionDraftPersistenceServer interface {
	sessionLifecycleClientProvider
}

type sessionTransitionServer interface {
	sessionLifecycleClientProvider
	Reauthenticate(ctx context.Context, interactor authInteractor, interactive bool) error
}

type sessionAuthReadinessServer interface {
	EnsureAuthReady(ctx context.Context, interactor authInteractor, interactive bool) error
}

type sessionWorkspaceChangeServer interface {
	sessionLifecycleClientProvider
	sessionConfigProvider
}

type interactiveProjectBindingServer interface {
	Config() config.App
	ProjectViewClient() client.ProjectViewClient
	BindProjectWorkspace(ctx context.Context, projectID string, workspaceID string) (interactiveSessionServer, error)
}

type interactiveSessionServer interface {
	appServerCore
	interactiveProjectBindingServer
	launchPlannerServer
	sessionWorkspaceChangeServer
	sessionInitialInputServer
	sessionDraftPersistenceServer
	sessionTransitionServer
	sessionAuthReadinessServer
}

type sessionLifecycleOptions struct {
	ForceNewSession bool
	Overrides       serverapi.RunPromptOverrides
}

func runSessionLifecycle(ctx context.Context, server interactiveSessionServer, interactor authInteractor, initialSessionID string) error {
	return runSessionLifecycleWithOptions(ctx, server, interactor, initialSessionID, sessionLifecycleOptions{})
}

func runSessionLifecycleWithOptions(ctx context.Context, server interactiveSessionServer, interactor authInteractor, initialSessionID string, opts sessionLifecycleOptions) error {
	originalServer := server
	boundServer, err := ensureInteractiveProjectBinding(ctx, server)
	if err != nil {
		return err
	}
	if shouldCloseReboundServer(originalServer, boundServer) {
		defer func() { _ = boundServer.Close() }()
	}
	server = boundServer
	planner := newSessionLaunchPlanner(server)
	currentSessionID := strings.TrimSpace(initialSessionID)
	nextSessionInitialPrompt := ""
	nextSessionInitialPromptHistoryRecorded := false
	nextSessionInitialInput := ""
	nextSessionParentID := ""
	forceNewSession := opts.ForceNewSession
	nextSessionOverrides := opts.Overrides
	showStartupUpdateNotice := true
	for {
		plan, err := planner.PlanSession(ctx, sessionLaunchRequest{
			Mode:              launchModeInteractive,
			SelectedSessionID: currentSessionID,
			ForceNewSession:   forceNewSession,
			ParentSessionID:   nextSessionParentID,
			Overrides:         nextSessionOverrides,
		})
		if err != nil {
			return err
		}
		forceNewSession = false
		nextSessionParentID = ""
		nextSessionOverrides = serverapi.RunPromptOverrides{}
		workspaceChangeAction, err := maybeHandlePickedSessionWorkspaceChange(ctx, server, plan)
		if err != nil {
			return err
		}
		switch workspaceChangeAction {
		case sessionWorkspaceChangePickAgain:
			currentSessionID = ""
			continue
		case sessionWorkspaceChangeReplanSelected:
			currentSessionID = plan.SessionID
			continue
		}
		runtimePlan, request, err := prepareSessionUIRun(
			ctx,
			server,
			planner,
			plan,
			nextSessionInitialPrompt,
			nextSessionInitialPromptHistoryRecorded,
			nextSessionInitialInput,
			showStartupUpdateNotice,
		)
		if err != nil {
			return err
		}
		finalModel, runErr := runUILoop(request)
		showStartupUpdateNotice = shouldRetryStartupUpdateNotice(finalModel, showStartupUpdateNotice)
		nextSessionInitialPrompt = ""
		nextSessionInitialPromptHistoryRecorded = false
		nextSessionInitialInput = ""
		if runErr != nil {
			if closeErr := runtimePlan.Close(); closeErr != nil {
				return errors.Join(runErr, closeErr)
			}
			return runErr
		}
		if err := persistSessionDraftToServer(ctx, server, plan.SessionID, finalModel); err != nil {
			if closeErr := runtimePlan.Close(); closeErr != nil {
				return errors.Join(err, closeErr)
			}
			return err
		}

		transition := extractUITransition(finalModel)
		if transition.Exit {
			if err := closeRuntimePlanAfterUIExit(runtimePlan, finalModel); err != nil {
				return err
			}
			return nil
		}
		resolved, err := resolveAndReleaseSessionAction(ctx, server, interactor, plan.SessionID, transition, runtimePlan)
		if err != nil {
			return err
		}
		if !resolved.ShouldContinue {
			return nil
		}
		currentSessionID = resolved.NextSessionID
		nextSessionInitialPrompt = resolved.InitialPrompt
		nextSessionInitialPromptHistoryRecorded = resolved.InitialPromptHistoryRecorded
		nextSessionInitialInput = resolved.InitialInput
		nextSessionParentID = resolved.ParentSessionID
		forceNewSession = resolved.ForceNewSession
	}
}

func resolveAndReleaseSessionAction(ctx context.Context, server sessionTransitionServer, interactor authInteractor, sessionID string, transition UITransition, runtimePlan *runtimeLaunchPlan) (resolvedSessionAction, error) {
	resolved, err := resolveSessionAction(ctx, server, interactor, sessionID, transition)
	if err != nil {
		return resolvedSessionAction{}, err
	}
	if err := runtimePlan.Close(); err != nil {
		return resolvedSessionAction{}, err
	}
	return resolved, nil
}

func prepareSessionUIRun(
	ctx context.Context,
	server interactiveSessionServer,
	planner *launchPlanner,
	plan sessionLaunchPlan,
	initialPrompt string,
	initialPromptHistoryRecorded bool,
	transitionInput string,
	startupUpdateNotice bool,
) (*runtimeLaunchPlan, uiLoopRequest, error) {
	runtimePlan, err := planner.PrepareRuntime(ctx, plan, os.Stderr, "app.start session_id="+plan.SessionID+" workspace="+plan.WorkspaceRoot+" model="+plan.ActiveSettings.Model)
	if err != nil {
		return nil, uiLoopRequest{}, err
	}
	cfg := server.Config()
	commandRegistry, err := commands.NewDefaultRegistryWithFilePrompts(cfg.WorkspaceRoot, cfg.PersistenceRoot)
	if err != nil {
		if closeErr := runtimePlan.Close(); closeErr != nil {
			return nil, uiLoopRequest{}, errors.Join(err, closeErr)
		}
		return nil, uiLoopRequest{}, err
	}
	initialState := sessionLaunchInitialStateFromServer(ctx, server, plan.SessionID, transitionInput)
	return runtimePlan, uiLoopRequest{
		wiring:                       runtimePlan.Wiring,
		active:                       plan.ActiveSettings,
		logger:                       runtimePlan.Logger,
		commandRegistry:              commandRegistry,
		initialPrompt:                initialPrompt,
		initialPromptHistoryRecorded: initialPromptHistoryRecorded,
		initialInput:                 initialState.Input,
		recoveryBuffers:              initialState.RecoveryBuffers,
		sessionName:                  plan.SessionName,
		modelContractLocked:          plan.ModelContractLocked,
		configuredModelName:          plan.ConfiguredModelName,
		statusConfig:                 plan.StatusConfig,
		startupUpdateNotice:          startupUpdateNotice,
	}, nil
}

func closeRuntimePlanAfterUIExit(runtimePlan *runtimeLaunchPlan, finalModel any) error {
	if ui, ok := finalModel.(*uiModel); ok && ui != nil && ui.forcedLocalExit {
		return runtimePlan.DetachOnlyClose()
	}
	return runtimePlan.Close()
}

func shouldRetryStartupUpdateNotice(model any, enabled bool) bool {
	if !enabled {
		return false
	}
	ui, ok := model.(*uiModel)
	return !ok || ui == nil || !ui.startupUpdateShown
}

func shouldCloseReboundServer(original appServerCore, rebound appServerCore) bool {
	if original == nil || rebound == nil || original == rebound {
		return false
	}
	originalEmbedded, originalOK := original.(*embeddedAppServer)
	reboundEmbedded, reboundOK := rebound.(*embeddedAppServer)
	if originalOK && reboundOK {
		return !originalEmbedded.SharesProcessWith(reboundEmbedded)
	}
	return true
}

func sessionLaunchInitialInputFromServer(ctx context.Context, server sessionInitialInputServer, sessionID string, transitionInput string) string {
	return sessionLaunchInitialStateFromServer(ctx, server, sessionID, transitionInput).Input
}

type sessionLaunchInitialState struct {
	Input           string
	RecoveryBuffers []serverapi.SessionDraftRecoveryBuffer
}

func sessionLaunchInitialStateFromServer(ctx context.Context, server sessionInitialInputServer, sessionID string, transitionInput string) sessionLaunchInitialState {
	if server == nil || server.SessionLifecycleClient() == nil {
		return sessionLaunchInitialState{Input: transitionInput}
	}
	resp, err := server.SessionLifecycleClient().GetInitialInput(ctx, serverapi.SessionInitialInputRequest{
		SessionID:       strings.TrimSpace(sessionID),
		TransitionInput: transitionInput,
	})
	if err != nil {
		return sessionLaunchInitialState{Input: transitionInput}
	}
	return sessionLaunchInitialState{Input: resp.Input, RecoveryBuffers: resp.RecoveryBuffers}
}

func persistSessionDraftToServer(ctx context.Context, server sessionDraftPersistenceServer, sessionID string, model any) error {
	if strings.TrimSpace(sessionID) == "" {
		return nil
	}
	ui, ok := model.(*uiModel)
	if !ok || ui == nil {
		return nil
	}
	if server == nil || server.SessionLifecycleClient() == nil {
		return nil
	}
	_, err := server.SessionLifecycleClient().PersistInputDraft(ctx, serverapi.SessionPersistInputDraftRequest{
		ClientRequestID: uuid.NewString(),
		SessionID:       strings.TrimSpace(sessionID),
		Input:           ui.input,
		RecoveryBuffers: ui.sessionDraftRecoveryBuffers(),
	})
	return err
}

type resolvedSessionAction struct {
	NextSessionID                string
	InitialPrompt                string
	InitialPromptHistoryRecorded bool
	InitialInput                 string
	ParentSessionID              string
	ForceNewSession              bool
	ShouldContinue               bool
}

func resolveSessionAction(ctx context.Context, server sessionTransitionServer, interactor authInteractor, sessionID string, transition UITransition) (resolvedSessionAction, error) {
	if transition.Exit {
		return resolvedSessionAction{}, nil
	}
	if server == nil || server.SessionLifecycleClient() == nil {
		return resolvedSessionAction{}, errors.New("session lifecycle client is required")
	}
	resolved, err := server.SessionLifecycleClient().ResolveTransition(ctx, serverapi.SessionResolveTransitionRequest{
		ClientRequestID: uuid.NewString(),
		SessionID:       strings.TrimSpace(sessionID),
		Transition: serverapi.SessionTransition{
			Action:                       transition.Action,
			InitialPrompt:                transition.InitialPrompt,
			InitialPromptHistoryRecorded: transition.InitialPromptHistoryRecorded,
			InitialInput:                 transition.InitialInput,
			TargetSessionID:              transition.TargetSessionID,
			ForkRollbackTargetID:         transition.ForkRollbackTargetID,
			ParentSessionID:              transition.ParentSessionID,
		},
	})
	if err != nil {
		return resolvedSessionAction{}, err
	}
	if resolved.RequiresReauth {
		if err := server.Reauthenticate(ctx, interactor, true); err != nil {
			return resolvedSessionAction{}, err
		}
	}
	return resolvedSessionAction{
		NextSessionID:                resolved.NextSessionID,
		InitialPrompt:                resolved.InitialPrompt,
		InitialPromptHistoryRecorded: resolved.InitialPromptHistoryRecorded,
		InitialInput:                 resolved.InitialInput,
		ParentSessionID:              resolved.ParentSessionID,
		ForceNewSession:              resolved.ForceNewSession,
		ShouldContinue:               resolved.ShouldContinue,
	}, nil
}
