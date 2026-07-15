package app

import (
	"context"
	"errors"
	"os"
	"strings"

	"core/cli/app/commands"
	"core/shared/client"
	"core/shared/config"
	"core/shared/runtimeids"
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
	Intent    *serverapi.SessionLaunchIntent
	Overrides serverapi.RunPromptOverrides
}

func runSessionLifecycle(ctx context.Context, server interactiveSessionServer, interactor authInteractor, initialSessionID string) error {
	var intent *serverapi.SessionLaunchIntent
	if trimmed := strings.TrimSpace(initialSessionID); trimmed != "" {
		sessionID, err := runtimeids.ParseSessionID(trimmed)
		if err != nil {
			return err
		}
		open := serverapi.OpenExistingSessionLaunchIntent(sessionID)
		intent = &open
	}
	return runSessionLifecycleWithOptions(ctx, server, interactor, sessionLifecycleOptions{Intent: intent})
}

func runSessionLifecycleWithOptions(ctx context.Context, server interactiveSessionServer, interactor authInteractor, opts sessionLifecycleOptions) error {
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
	next := serverapi.SelectSessionLifecycleResult(serverapi.SessionAuthPreparationKeepCurrent)
	if opts.Intent != nil {
		next = serverapi.LaunchSessionLifecycleResult(
			*opts.Intent,
			serverapi.NewSessionLaunchPreparation(
				nil,
				serverapi.RestoreStoredDraftSessionInitialInputPolicy(),
				serverapi.SessionAuthPreparationKeepCurrent,
			),
		)
	}
	nextSessionOverrides := opts.Overrides
	showStartupUpdateNotice := true
	for {
		switch next.Kind() {
		case serverapi.SessionLifecycleResultStop:
			return nil
		case serverapi.SessionLifecycleResultSelectSession:
			picked, err := planner.selectSession(ctx)
			if err != nil {
				return err
			}
			switch picked := picked.(type) {
			case sessionPickerCancelResult:
				next = serverapi.StopSessionLifecycleResult()
			case sessionPickerCreateResult:
				next = serverapi.LaunchSessionLifecycleResult(
					serverapi.CreateNewSessionLaunchIntent(nil),
					serverapi.NewSessionLaunchPreparation(
						nil,
						serverapi.RestoreStoredDraftSessionInitialInputPolicy(),
						serverapi.SessionAuthPreparationKeepCurrent,
					),
				)
			case sessionPickerOpenResult:
				sessionID := picked.sessionID
				selectedWorkspaceRoot, err := loadSelectedSessionWorkspaceRoot(ctx, server.SessionViewClient(), sessionID.String())
				if err != nil {
					next = serverapi.SelectSessionLifecycleResult(serverapi.SessionAuthPreparationKeepCurrent)
					continue
				}
				workspaceChangeAction, err := maybeHandlePickedSessionWorkspaceChange(ctx, server, sessionID.String(), selectedWorkspaceRoot)
				if err != nil {
					return err
				}
				if workspaceChangeAction == sessionWorkspaceChangePickAgain {
					next = serverapi.SelectSessionLifecycleResult(serverapi.SessionAuthPreparationKeepCurrent)
					continue
				}
				next = serverapi.LaunchSessionLifecycleResult(
					serverapi.OpenExistingSessionLaunchIntent(sessionID),
					serverapi.NewSessionLaunchPreparation(
						nil,
						serverapi.RestoreStoredDraftSessionInitialInputPolicy(),
						serverapi.SessionAuthPreparationKeepCurrent,
					),
				)
			default:
				return errors.New("session picker returned an invalid result")
			}
			continue
		case serverapi.SessionLifecycleResultLaunch:
		default:
			return errors.New("session lifecycle returned no result")
		}

		intent, present := next.LaunchIntent()
		if !present {
			return errors.New("launch lifecycle result is missing an intent")
		}
		preparation, present := next.LaunchPreparation()
		if !present {
			return errors.New("launch lifecycle result is missing preparation")
		}
		launchRequest, err := sessionLaunchRequestFromIntent(intent, nextSessionOverrides)
		if err != nil {
			return err
		}
		plan, err := planner.PlanSession(ctx, launchRequest)
		if err != nil {
			return err
		}
		nextSessionOverrides = serverapi.RunPromptOverrides{}
		initialPrompt, initialPromptHistoryRecorded, transitionInput, overrideStoredDraft, err := sessionLaunchPreparationValues(preparation)
		if err != nil {
			return err
		}
		runtimePlan, request, err := prepareSessionUIRun(
			ctx,
			server,
			planner,
			plan,
			initialPrompt,
			initialPromptHistoryRecorded,
			transitionInput,
			overrideStoredDraft,
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
		resolved, err := resolveAndReleaseSessionAction(ctx, server, interactor, plan.SessionID, transition, runtimePlan)
		if err != nil {
			return err
		}
		next = resolved
	}
}

func resolveAndReleaseSessionAction(ctx context.Context, server sessionTransitionServer, interactor authInteractor, sessionID string, transition UITransition, runtimePlan *runtimeLaunchPlan) (serverapi.SessionLifecycleResult, error) {
	resolved, err := resolveSessionAction(ctx, server, interactor, sessionID, transition)
	if err != nil {
		if closeErr := runtimePlan.Close(); closeErr != nil {
			return serverapi.SessionLifecycleResult{}, errors.Join(err, closeErr)
		}
		return serverapi.SessionLifecycleResult{}, err
	}
	if err := runtimePlan.Close(); err != nil {
		return serverapi.SessionLifecycleResult{}, err
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
	overrideStoredDraft bool,
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
	initialState, err := sessionLaunchInitialStateFromServer(
		ctx,
		server,
		plan.SessionID,
		transitionInput,
		overrideStoredDraft,
	)
	if err != nil {
		return nil, uiLoopRequest{}, closeRuntimePlanAfterPreparationFailure(runtimePlan, err)
	}
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

func sessionLaunchInitialStateFromServer(
	ctx context.Context,
	server sessionInitialInputServer,
	sessionID string,
	transitionInput string,
	overrideStoredDraft bool,
) (sessionLaunchInitialState, error) {
	if server == nil || server.SessionLifecycleClient() == nil {
		return sessionLaunchInitialState{}, errors.New("session lifecycle client is required")
	}
	resp, err := server.SessionLifecycleClient().GetInitialInput(ctx, serverapi.SessionInitialInputRequest{
		SessionID:           strings.TrimSpace(sessionID),
		TransitionInput:     transitionInput,
		OverrideStoredDraft: overrideStoredDraft,
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

func sessionLaunchRequestFromIntent(intent serverapi.SessionLaunchIntent, overrides serverapi.RunPromptOverrides) (sessionLaunchRequest, error) {
	if err := intent.Validate(); err != nil {
		return sessionLaunchRequest{}, err
	}
	request := sessionLaunchRequest{
		Mode:      launchModeInteractive,
		Intent:    intent,
		Overrides: overrides,
	}
	switch intent.Kind() {
	case serverapi.SessionLaunchIntentCreateNew, serverapi.SessionLaunchIntentOpenExisting:
	default:
		return sessionLaunchRequest{}, errors.New("session launch intent kind is invalid")
	}
	return request, nil
}

func sessionLaunchPreparationValues(preparation serverapi.SessionLaunchPreparation) (string, bool, string, bool, error) {
	if err := preparation.Validate(); err != nil {
		return "", false, "", false, err
	}
	initialPrompt := ""
	initialPromptHistoryRecorded := false
	if prompt, present := preparation.InitialPrompt(); present {
		initialPrompt = prompt.Text
		initialPromptHistoryRecorded = prompt.HistoryRecorded
	}
	switch policy := preparation.InitialInputPolicy(); policy.Kind() {
	case serverapi.SessionInitialInputPolicyRestoreStoredDraft:
		return initialPrompt, initialPromptHistoryRecorded, "", false, nil
	case serverapi.SessionInitialInputPolicyOverrideStoredDraft:
		text, present := policy.OverrideText()
		if !present {
			return "", false, "", false, errors.New("override-stored-draft policy is missing text")
		}
		return initialPrompt, initialPromptHistoryRecorded, text, true, nil
	default:
		return "", false, "", false, errors.New("session initial input policy kind is invalid")
	}
}

func lifecycleResultAuthPreparation(result serverapi.SessionLifecycleResult) (serverapi.SessionAuthPreparation, bool, error) {
	if err := result.Validate(); err != nil {
		return "", false, err
	}
	switch result.Kind() {
	case serverapi.SessionLifecycleResultStop:
		return "", false, nil
	case serverapi.SessionLifecycleResultSelectSession:
		authPreparation, present := result.AuthPreparation()
		if !present {
			return "", false, errors.New("select-session lifecycle result is missing auth preparation")
		}
		return authPreparation, true, nil
	case serverapi.SessionLifecycleResultLaunch:
		preparation, present := result.LaunchPreparation()
		if !present {
			return "", false, errors.New("launch lifecycle result is missing preparation")
		}
		return preparation.AuthPreparation(), true, nil
	default:
		return "", false, errors.New("session lifecycle result kind is invalid")
	}
}

func resolveSessionAction(ctx context.Context, server sessionTransitionServer, interactor authInteractor, sessionID string, transition UITransition) (serverapi.SessionLifecycleResult, error) {
	if transition.Exit {
		return serverapi.StopSessionLifecycleResult(), nil
	}
	if server == nil || server.SessionLifecycleClient() == nil {
		return serverapi.SessionLifecycleResult{}, errors.New("session lifecycle client is required")
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
		return serverapi.SessionLifecycleResult{}, err
	}
	authPreparation, hasAuthPreparation, err := lifecycleResultAuthPreparation(resolved)
	if err != nil {
		return serverapi.SessionLifecycleResult{}, err
	}
	if hasAuthPreparation && authPreparation == serverapi.SessionAuthPreparationReauthenticate {
		if err := server.Reauthenticate(ctx, interactor, true); err != nil {
			return serverapi.SessionLifecycleResult{}, err
		}
	}
	return resolved, nil
}
