package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	checkpoint "core/internal/testharness/pty/analyzer"
	"core/internal/testharness/pty/appfixture"
	"core/shared/lifecyclecontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func runLifecycleHookPTYFixtureProcess(
	ctx context.Context,
	processConfig appfixture.LifecycleProcessConfig,
) (runErr error) {
	if err := processConfig.Validate(); err != nil {
		return err
	}
	terminal := newPTYCheckpointTerminalFile(os.Stdout)
	if err := terminal.writer.QueueBeforeNextWrite(checkpoint.KindScenarioStart, nil); err != nil {
		return fmt.Errorf("queue lifecycle scenario-start checkpoint: %w", err)
	}
	scenarioState, runtime, sessionID, err := prepareLifecyclePTYFixtureRuntime(
		ctx,
		terminal,
		processConfig,
	)
	if err != nil {
		return err
	}
	options := Options{
		WorkspaceRoot:         processConfig.WorkspaceRoot,
		WorkspaceRootExplicit: true,
		SessionID:             sessionID,
		ConfigRoot:            processConfig.PersistenceRoot,
		OpenAIBaseURL:         "http://127.0.0.1:1/v1",
		OpenAIBaseURLExplicit: true,
	}
	if runtime != nil {
		options.startupOptions = runtime.StartupOptions()
	}
	interactor := newInteractiveAuthInteractor()
	if processConfig.ServerMode == appfixture.LifecycleServerModeLocal {
		standingServer, err := startEmbeddedServer(ctx, options, interactor, true)
		if err != nil {
			return fmt.Errorf("start local lifecycle fixture server: %w", err)
		}
		defer func() { runErr = errors.Join(runErr, standingServer.Close()) }()
	}

	server, err := startSessionServer(ctx, options, interactor, true)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, server.Close()) }()
	boundServer, err := ensureInteractiveProjectBinding(ctx, server)
	if err != nil {
		return err
	}
	if shouldCloseReboundServer(server, boundServer) {
		defer func() { runErr = errors.Join(runErr, boundServer.Close()) }()
	}
	server = boundServer

	intent, openingKind, err := lifecyclePTYLaunchIntent(processConfig.ServerMode, sessionID)
	if err != nil {
		return err
	}
	planner := newSessionLaunchPlanner(server)
	plan, err := planner.PlanSession(ctx, sessionLaunchRequest{
		Mode:   launchModeInteractive,
		Intent: intent,
	})
	if err != nil {
		return err
	}
	plan.ClientLifecycleCommand = clientSettingsForInteractiveServer(server).Hooks.LifecycleCommand()
	plan.ClientLifecycleOpeningKind = openingKind
	runtimePlan, request, err := prepareSessionUIRun(
		ctx,
		server,
		planner,
		plan,
		processConfig.InitialPrompt,
		false,
		"",
		false,
	)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, runtimePlan.Close()) }()
	composition, err := composeUIProgram(request, terminal)
	if err != nil {
		return err
	}
	wrapped := newPTYCheckpointModel(composition.model, terminal.writer, scenarioState)
	finalModel, err := runUIProgram(composition, wrapped)
	if err != nil {
		return err
	}
	finalWrapped, ok := finalModel.(*ptyCheckpointModel)
	if !ok {
		return fmt.Errorf("lifecycle PTY fixture final model has unexpected type %T", finalModel)
	}
	if _, ok := finalWrapped.appModel(); !ok {
		return fmt.Errorf("lifecycle PTY fixture inner final model has unexpected type %T", finalWrapped.inner)
	}
	return nil
}

func prepareLifecyclePTYFixtureRuntime(
	ctx context.Context,
	terminal *ptyCheckpointTerminalFile,
	processConfig appfixture.LifecycleProcessConfig,
) (*ptyCheckpointScenarioState, *appfixture.Runtime, string, error) {
	if processConfig.ServerMode == appfixture.LifecycleServerModeRemote {
		state := newPTYCheckpointScenarioState(
			appfixture.ScriptFinalAssistantOrdinal(processConfig.TargetFinalAssistantCount),
		)
		state.markScenarioComplete()
		return state, nil, "", nil
	}
	if err := os.MkdirAll(processConfig.WorkspaceRoot, 0o755); err != nil {
		return nil, nil, "", fmt.Errorf("create lifecycle fixture workspace: %w", err)
	}
	recorderCommand, err := lifecycleHookProductRecorderCommand(
		processConfig.HookRecordPath,
		processConfig.HookBehavior,
		processConfig.HookStatePath,
	)
	if err != nil {
		return nil, nil, "", err
	}
	if err := appfixture.PrepareConfigAndBindingWithOptions(
		ctx,
		processConfig.PersistenceRoot,
		processConfig.WorkspaceRoot,
		appfixture.ConfigOptions{LifecycleHookCommand: recorderCommand},
	); err != nil {
		return nil, nil, "", err
	}
	var state *ptyCheckpointScenarioState
	runtime, err := appfixture.NewRuntime(*processConfig.LocalScriptPath, func(
		targetFinalAssistantOrdinal appfixture.ScriptFinalAssistantOrdinal,
	) func(context.Context) error {
		state = newPTYCheckpointScenarioState(targetFinalAssistantOrdinal)
		return func(context.Context) error {
			if err := terminal.writer.Emit(checkpoint.KindScenarioComplete, nil); err != nil {
				return err
			}
			state.markScenarioComplete()
			return nil
		}
	})
	if err != nil {
		return nil, nil, "", err
	}
	if state == nil {
		panic("lifecycle PTY fixture runtime did not configure scenario completion state")
	}
	if processConfig.TargetFinalAssistantCount != uint64(runtime.TargetFinalAssistantOrdinal()) {
		return nil, nil, "", fmt.Errorf(
			"lifecycle fixture target final count = %d, script target = %d",
			processConfig.TargetFinalAssistantCount,
			runtime.TargetFinalAssistantOrdinal(),
		)
	}
	sessionID, err := runtime.SeedSession(
		ctx,
		processConfig.PersistenceRoot,
		processConfig.WorkspaceRoot,
	)
	if err != nil {
		return nil, nil, "", err
	}
	return state, runtime, sessionID, nil
}

func runLifecycleHookServerFixtureProcess(
	ctx context.Context,
	processConfig appfixture.LifecycleServerProcessConfig,
) (runErr error) {
	if err := processConfig.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(processConfig.WorkspaceRoot, 0o755); err != nil {
		return fmt.Errorf("create lifecycle server fixture workspace: %w", err)
	}
	recorderCommand, err := lifecycleHookProductRecorderCommand(
		processConfig.HookRecordPath,
		appfixture.LifecycleHookBehaviorSuccess,
		nil,
	)
	if err != nil {
		return err
	}
	if err := appfixture.PrepareConfigAndBindingWithOptions(
		ctx,
		processConfig.PersistenceRoot,
		processConfig.WorkspaceRoot,
		appfixture.ConfigOptions{LifecycleHookCommand: recorderCommand},
	); err != nil {
		return err
	}
	runtime, err := appfixture.NewRuntime(processConfig.ScriptPath, nil)
	if err != nil {
		return err
	}
	server, err := startEmbeddedServer(ctx, Options{
		WorkspaceRoot:         processConfig.WorkspaceRoot,
		WorkspaceRootExplicit: true,
		ConfigRoot:            processConfig.PersistenceRoot,
		OpenAIBaseURL:         "http://127.0.0.1:1/v1",
		OpenAIBaseURLExplicit: true,
		startupOptions:        runtime.StartupOptions(),
	}, newInteractiveAuthInteractor(), true)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, server.Close()) }()
	ready, err := json.Marshal(struct {
		PID int `json:"pid"`
	}{PID: os.Getpid()})
	if err != nil {
		return fmt.Errorf("marshal lifecycle server fixture readiness: %w", err)
	}
	if err := os.WriteFile(processConfig.ReadyPath, ready, 0o600); err != nil {
		return fmt.Errorf("publish lifecycle server fixture readiness: %w", err)
	}
	select {}
}

func lifecyclePTYLaunchIntent(
	mode appfixture.LifecycleServerMode,
	sessionID string,
) (serverapi.SessionLaunchIntent, lifecyclecontract.OpeningKind, error) {
	if mode == appfixture.LifecycleServerModeRemote {
		return serverapi.CreateNewSessionLaunchIntent(
			serverapi.IndependentSessionCreateOrigin(),
		), lifecyclecontract.OpeningKindNew, nil
	}
	parsed, err := runtimeids.ParseSessionID(sessionID)
	if err != nil {
		return serverapi.SessionLaunchIntent{}, "", err
	}
	return serverapi.OpenExistingSessionLaunchIntent(
		parsed,
	), lifecyclecontract.OpeningKindResumed, nil
}
