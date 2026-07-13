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
	PresentationTheme() string
	ClientPromptRoots() (commands.ClientPromptRoots, error)
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
	handoff, err := initialSessionHandoff(initialSessionID, opts.ForceNewSession)
	if err != nil {
		return err
	}
	nextSessionOverrides := opts.Overrides
	showStartupUpdateNotice := true
	for {
		launchRequest, err := sessionLaunchRequestFromHandoff(handoff, nextSessionOverrides)
		if err != nil {
			return err
		}
		plan, err := planner.PlanSession(ctx, launchRequest)
		if err != nil {
			return err
		}
		nextSessionOverrides = serverapi.RunPromptOverrides{}
		workspaceChangeAction, err := maybeHandlePickedSessionWorkspaceChange(ctx, server, plan)
		if err != nil {
			return err
		}
		switch workspaceChangeAction {
		case sessionWorkspaceChangePickAgain:
			handoff.Destination = sessionPickerDestination{}
			continue
		case sessionWorkspaceChangeReplanSelected:
			destination, err := newSessionOpenDestination(plan.SessionID)
			if err != nil {
				return err
			}
			handoff.Destination = destination
			continue
		}
		runtimePlan, request, err := prepareSessionUIRun(
			ctx,
			server,
			planner,
			plan,
			handoff,
			showStartupUpdateNotice,
		)
		if err != nil {
			return err
		}
		finalModel, runErr := runUILoop(request)
		showStartupUpdateNotice = shouldRetryStartupUpdateNotice(finalModel, showStartupUpdateNotice)
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
		resolved, err := resolveAndReleaseSessionHandoff(ctx, server, interactor, plan.SessionID, transition, runtimePlan)
		if err != nil {
			return err
		}
		if resolved == nil {
			return nil
		}
		handoff = *resolved
	}
}

func resolveAndReleaseSessionHandoff(ctx context.Context, server sessionTransitionServer, interactor authInteractor, sessionID string, transition UITransition, runtimePlan *runtimeLaunchPlan) (*resolvedSessionHandoff, error) {
	resolved, err := resolveSessionAction(ctx, server, interactor, sessionID, transition)
	if err != nil {
		if closeErr := runtimePlan.Close(); closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
		return nil, err
	}
	if err := runtimePlan.Close(); err != nil {
		return nil, err
	}
	if resolved != nil && transition.Action == UIActionOpenSession {
		resolved.InitialInput.Precedence = sessionInitialInputPreferTransition
	}
	return resolved, nil
}

func prepareSessionUIRun(
	ctx context.Context,
	server interactiveSessionServer,
	planner *launchPlanner,
	plan sessionLaunchPlan,
	handoff resolvedSessionHandoff,
	startupUpdateNotice bool,
) (*runtimeLaunchPlan, uiLoopRequest, error) {
	runtimePlan, err := planner.PrepareRuntime(ctx, plan, os.Stderr, "app.start session_id="+plan.SessionID+" workspace="+plan.WorkspaceRoot+" model="+plan.ActiveSettings.Model)
	if err != nil {
		return nil, uiLoopRequest{}, err
	}
	promptRoots, err := server.ClientPromptRoots()
	if err != nil {
		return nil, uiLoopRequest{}, closeRuntimePlanAfterPreparationFailure(runtimePlan, err)
	}
	commandRegistry, err := commands.NewDefaultRegistryWithClientPromptRoots(promptRoots)
	if err != nil {
		return nil, uiLoopRequest{}, closeRuntimePlanAfterPreparationFailure(runtimePlan, err)
	}
	initialState, err := sessionLaunchInitialStateFromServer(ctx, server, plan.SessionID, handoff.InitialInput)
	if err != nil {
		return nil, uiLoopRequest{}, closeRuntimePlanAfterPreparationFailure(runtimePlan, err)
	}
	initialPrompt, initialPromptHistoryRecorded := handoff.initialPromptFields()
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

func closeRuntimePlanAfterPreparationFailure(runtimePlan *runtimeLaunchPlan, preparationErr error) error {
	if closeErr := runtimePlan.Close(); closeErr != nil {
		return errors.Join(preparationErr, closeErr)
	}
	return preparationErr
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

type sessionLaunchInitialState struct {
	Input           string
	RecoveryBuffers []serverapi.SessionDraftRecoveryBuffer
}

func sessionLaunchInitialStateFromServer(ctx context.Context, server sessionInitialInputServer, sessionID string, directive sessionInitialInputDirective) (sessionLaunchInitialState, error) {
	if server == nil || server.SessionLifecycleClient() == nil {
		return sessionLaunchInitialState{}, errors.New("session lifecycle client is required")
	}
	resp, err := server.SessionLifecycleClient().GetInitialInput(ctx, serverapi.SessionInitialInputRequest{
		SessionID:           strings.TrimSpace(sessionID),
		TransitionInput:     directive.TransitionInput,
		OverrideStoredDraft: directive.Precedence == sessionInitialInputPreferTransition,
	})
	if err != nil {
		return sessionLaunchInitialState{}, err
	}
	return sessionLaunchInitialState{Input: resp.Input, RecoveryBuffers: resp.RecoveryBuffers}, nil
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

type sessionInitialInputPrecedence uint8

const (
	sessionInitialInputPreferStoredDraft sessionInitialInputPrecedence = iota
	sessionInitialInputPreferTransition
)

type sessionInitialInputDirective struct {
	TransitionInput string
	Precedence      sessionInitialInputPrecedence
}

type sessionInitialPrompt struct {
	Text            string
	HistoryRecorded bool
}

type resolvedSessionHandoff struct {
	Destination   sessionLaunchDestination
	InitialPrompt *sessionInitialPrompt
	InitialInput  sessionInitialInputDirective
}

func initialSessionHandoff(initialSessionID string, forceNewSession bool) (resolvedSessionHandoff, error) {
	validatedSessionID := optionalValidatedSessionID(initialSessionID)
	if forceNewSession {
		if validatedSessionID != nil {
			return resolvedSessionHandoff{}, errors.New("initial session id cannot be combined with force-new session")
		}
		return resolvedSessionHandoff{
			Destination:  sessionCreateDestination{},
			InitialInput: sessionInitialInputDirective{Precedence: sessionInitialInputPreferStoredDraft},
		}, nil
	}
	if validatedSessionID == nil {
		return resolvedSessionHandoff{
			Destination:  sessionPickerDestination{},
			InitialInput: sessionInitialInputDirective{Precedence: sessionInitialInputPreferStoredDraft},
		}, nil
	}
	return resolvedSessionHandoff{
		Destination:  sessionOpenDestination{sessionID: *validatedSessionID},
		InitialInput: sessionInitialInputDirective{Precedence: sessionInitialInputPreferStoredDraft},
	}, nil
}

func sessionLaunchRequestFromHandoff(handoff resolvedSessionHandoff, overrides serverapi.RunPromptOverrides) (sessionLaunchRequest, error) {
	switch handoff.Destination.(type) {
	case sessionPickerDestination, sessionOpenDestination, sessionCreateDestination:
		return sessionLaunchRequest{
			Mode:        launchModeInteractive,
			Destination: handoff.Destination,
			Overrides:   overrides,
		}, nil
	default:
		return sessionLaunchRequest{}, errors.New("session handoff destination is required")
	}
}

func (h resolvedSessionHandoff) initialPromptFields() (string, bool) {
	if h.InitialPrompt == nil {
		return "", false
	}
	return h.InitialPrompt.Text, h.InitialPrompt.HistoryRecorded
}

func resolveSessionAction(ctx context.Context, server sessionTransitionServer, interactor authInteractor, sessionID string, transition UITransition) (*resolvedSessionHandoff, error) {
	if transition.Exit {
		return nil, nil
	}
	if server == nil || server.SessionLifecycleClient() == nil {
		return nil, errors.New("session lifecycle client is required")
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
		return nil, err
	}
	if resolved.RequiresReauth {
		if err := server.Reauthenticate(ctx, interactor, true); err != nil {
			return nil, err
		}
	}
	return resolvedSessionHandoffFromResponse(transition.Action, resolved)
}

func resolvedSessionHandoffFromResponse(action UIAction, resolved serverapi.SessionResolveTransitionResponse) (*resolvedSessionHandoff, error) {
	if !resolved.ShouldContinue {
		if transitionActionRequiresDestination(action) {
			return nil, errors.New("session transition did not resolve a destination")
		}
		if resolved.NextSessionID != "" ||
			resolved.InitialPrompt != "" ||
			resolved.InitialPromptHistoryRecorded ||
			resolved.InitialInput != "" ||
			resolved.ParentSessionID != "" ||
			resolved.ForceNewSession {
			return nil, errors.New("non-continuing session transition returned continuation state")
		}
		return nil, nil
	}

	destination, err := sessionLaunchDestinationFromResponse(resolved)
	if err != nil {
		return nil, err
	}
	if err := validateTransitionDestination(action, destination); err != nil {
		return nil, err
	}
	var prompt *sessionInitialPrompt
	switch {
	case resolved.InitialPrompt == "" && resolved.InitialPromptHistoryRecorded:
		return nil, errors.New("initial prompt history cannot be recorded without an initial prompt")
	case resolved.InitialPrompt != "":
		prompt = &sessionInitialPrompt{
			Text:            resolved.InitialPrompt,
			HistoryRecorded: resolved.InitialPromptHistoryRecorded,
		}
	}
	return &resolvedSessionHandoff{
		Destination:   destination,
		InitialPrompt: prompt,
		InitialInput: sessionInitialInputDirective{
			TransitionInput: resolved.InitialInput,
			Precedence:      sessionInitialInputPreferStoredDraft,
		},
	}, nil
}

func transitionActionRequiresDestination(action UIAction) bool {
	switch action {
	case UIActionNewSession,
		UIActionResume,
		UIActionLogout,
		UIActionForkRollback,
		UIActionOpenSession:
		return true
	default:
		return false
	}
}

func validateTransitionDestination(action UIAction, destination sessionLaunchDestination) error {
	switch action {
	case UIActionOpenSession, UIActionForkRollback:
		if _, ok := destination.(sessionOpenDestination); !ok {
			return errors.New("session transition requires an existing-session destination")
		}
	case UIActionNewSession:
		if _, ok := destination.(sessionCreateDestination); !ok {
			return errors.New("new-session transition requires a create-session destination")
		}
	case UIActionResume:
		if _, ok := destination.(sessionPickerDestination); !ok {
			return errors.New("resume transition requires a session-picker destination")
		}
	case UIActionLogout:
		switch destination.(type) {
		case sessionOpenDestination, sessionPickerDestination:
		default:
			return errors.New("logout transition requires an existing-session or session-picker destination")
		}
	default:
		return errors.New("continuing session transition has unsupported action")
	}
	return nil
}

func sessionLaunchDestinationFromResponse(resolved serverapi.SessionResolveTransitionResponse) (sessionLaunchDestination, error) {
	if resolved.ForceNewSession {
		if resolved.NextSessionID != "" {
			return nil, errors.New("force-new session transition cannot also select an existing session")
		}
		parent := optionalSessionParentReference(resolved.ParentSessionID)
		return sessionCreateDestination{Parent: parent}, nil
	}
	if resolved.NextSessionID != "" {
		if resolved.ParentSessionID != "" {
			return nil, errors.New("existing-session transition cannot also carry a parent session")
		}
		destination, err := newSessionOpenDestination(resolved.NextSessionID)
		if err != nil {
			return nil, err
		}
		return destination, nil
	}
	if resolved.ParentSessionID != "" {
		return nil, errors.New("picker transition cannot carry a parent session")
	}
	return sessionPickerDestination{}, nil
}
