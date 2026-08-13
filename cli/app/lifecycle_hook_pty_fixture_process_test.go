package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	checkpoint "core/internal/testharness/pty/analyzer"
	"core/internal/testharness/pty/appfixture"
	"core/server/metadata"
	"core/server/session"
	"core/shared/lifecyclecontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func runLifecycleHookPTYFixtureProcess(
	t *testing.T,
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
	if runtime != nil {
		defer func() { runErr = errors.Join(runErr, runtime.Close()) }()
	}
	options := Options{
		WorkspaceRoot:         processConfig.WorkspaceRoot,
		WorkspaceRootExplicit: true,
		SessionID:             sessionID,
		ConfigRoot:            processConfig.PersistenceRoot,
	}
	interactor := newInteractiveAuthInteractor()
	if processConfig.ServerMode == appfixture.LifecycleServerModeLocal {
		startConfiguredDaemonFixture(t, processConfig.WorkspaceRoot, serverStartupRequest(
			t, processConfig.WorkspaceRoot, processConfig.PersistenceRoot,
		), readyMemoryAuthHandler())
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

	intent := serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())
	openingKind := lifecyclecontract.OpeningKindNew
	if sessionID != "" {
		parsed, parseErr := runtimeids.ParseSessionID(sessionID)
		if parseErr != nil {
			return parseErr
		}
		intent = serverapi.OpenExistingSessionLaunchIntent(parsed)
		openingKind = lifecyclecontract.OpeningKindResumed
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
	cancelHookBarrier, hookBarrierDone := startLifecycleHookObservationBarrier(
		ctx,
		terminal,
		scenarioState,
		processConfig,
	)
	finalModel, uiErr := runUIProgram(composition, wrapped)
	cancelHookBarrier()
	var hookBarrierErr error
	if hookBarrierDone != nil {
		hookBarrierErr = <-hookBarrierDone
	}
	if uiErr != nil || hookBarrierErr != nil {
		return errors.Join(uiErr, hookBarrierErr)
	}
	if _, ok := finalModel.(*ptyCheckpointModel); !ok {
		return fmt.Errorf("lifecycle PTY fixture final model has unexpected type %T", finalModel)
	}
	return nil
}

func startLifecycleHookObservationBarrier(
	ctx context.Context,
	terminal *ptyCheckpointTerminalFile,
	scenario *ptyCheckpointScenarioState,
	processConfig appfixture.LifecycleProcessConfig,
) (context.CancelFunc, <-chan error) {
	if processConfig.HookObservationBarrier == nil {
		return func() {}, nil
	}
	barrierCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		err := appfixture.WaitForLifecycleHookCategories(
			barrierCtx,
			processConfig.HookRecordPath,
			processConfig.HookObservationBarrier.RequiredCategories,
		)
		if err == nil {
			err = scenario.waitFinalApplied(barrierCtx)
		}
		if err == nil {
			err = terminal.writer.Emit(checkpoint.KindLifecycleHooksObserved, nil)
		}
		done <- err
	}()
	return cancel, done
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
	state, runtime, err := newCheckpointPTYFixtureRuntime(*processConfig.LocalScriptPath, terminal)
	if err != nil {
		return nil, nil, "", err
	}
	baseURL := runtime.OpenAIBaseURL()
	if err := appfixture.PrepareConfigAndBindingWithOptions(
		ctx,
		processConfig.PersistenceRoot,
		processConfig.WorkspaceRoot,
		appfixture.ConfigOptions{LifecycleHookCommand: recorderCommand, OpenAIBaseURL: &baseURL},
	); err != nil {
		_ = runtime.Close()
		return nil, nil, "", err
	}
	if processConfig.TargetFinalAssistantCount != uint64(runtime.TargetFinalAssistantOrdinal()) {
		_ = runtime.Close()
		return nil, nil, "", fmt.Errorf(
			"lifecycle fixture target final count = %d, script target = %d",
			processConfig.TargetFinalAssistantCount,
			runtime.TargetFinalAssistantOrdinal(),
		)
	}
	sessionID, err := runtime.SeedSession(ctx, processConfig.PersistenceRoot, processConfig.WorkspaceRoot)
	if err != nil {
		_ = runtime.Close()
		return nil, nil, "", err
	}
	if err := setLifecycleFixtureSessionName(processConfig.PersistenceRoot, sessionID); err != nil {
		_ = runtime.Close()
		return nil, nil, "", err
	}
	return state, runtime, sessionID, nil
}

func newCheckpointPTYFixtureRuntime(
	scriptPath string,
	terminal *ptyCheckpointTerminalFile,
) (*ptyCheckpointScenarioState, *appfixture.Runtime, error) {
	var state *ptyCheckpointScenarioState
	runtime, err := appfixture.NewRuntime(scriptPath, func(
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
		return nil, nil, err
	}
	if state == nil {
		panic("lifecycle PTY fixture runtime did not configure scenario completion state")
	}
	return state, runtime, nil
}

func setLifecycleFixtureSessionName(persistenceRoot string, sessionID string) error {
	metadataStore, err := metadata.Open(persistenceRoot)
	if err != nil {
		return fmt.Errorf("open lifecycle fixture metadata: %w", err)
	}
	defer func() { _ = metadataStore.Close() }()
	store, err := session.OpenByID(
		persistenceRoot,
		sessionID,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		return fmt.Errorf("open lifecycle fixture session: %w", err)
	}
	if err := store.SetName("Lifecycle fixture"); err != nil {
		return fmt.Errorf("name lifecycle fixture session: %w", err)
	}
	return nil
}

func runLifecycleHookServerFixtureProcess(
	t *testing.T,
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
	runtime, err := appfixture.NewRuntime(processConfig.ScriptPath, nil)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, runtime.Close()) }()
	baseURL := runtime.OpenAIBaseURL()
	if err := appfixture.PrepareConfigAndBindingWithOptions(
		ctx,
		processConfig.PersistenceRoot,
		processConfig.WorkspaceRoot,
		appfixture.ConfigOptions{LifecycleHookCommand: recorderCommand, OpenAIBaseURL: &baseURL},
	); err != nil {
		return err
	}
	startConfiguredDaemonFixture(t, processConfig.WorkspaceRoot, serverStartupRequest(
		t, processConfig.WorkspaceRoot, processConfig.PersistenceRoot,
	), readyMemoryAuthHandler())
	if err := appfixture.WriteLifecycleServerProcessReady(
		processConfig.ReadyPath,
		appfixture.LifecycleServerProcessReady{PID: os.Getpid()},
	); err != nil {
		return fmt.Errorf("publish lifecycle server fixture readiness: %w", err)
	}
	select {}
}
