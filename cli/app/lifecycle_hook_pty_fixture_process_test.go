package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	checkpoint "core/internal/testharness/pty/analyzer"
	"core/internal/testharness/pty/appfixture"
	"core/server/metadata"
	"core/server/session"
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
	cancelHookBarrier, hookBarrierDone := startLifecycleHookObservationBarrier(
		ctx,
		terminal,
		scenarioState,
		processConfig,
	)
	finalModel, uiErr := runUIProgram(composition, wrapped)
	hookBarrierErr := stopLifecycleHookObservationBarrier(cancelHookBarrier, hookBarrierDone)
	if uiErr != nil || hookBarrierErr != nil {
		return errors.Join(uiErr, hookBarrierErr)
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

func stopLifecycleHookObservationBarrier(
	cancel context.CancelFunc,
	done <-chan error,
) error {
	cancel()
	if done == nil {
		return nil
	}
	return <-done
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
	if err := setLifecycleFixtureSessionName(processConfig.PersistenceRoot, sessionID); err != nil {
		return nil, nil, "", err
	}
	return state, runtime, sessionID, nil
}

const lifecycleFixtureSessionName = "Lifecycle fixture"

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
	if err := store.SetName(lifecycleFixtureSessionName); err != nil {
		return fmt.Errorf("name lifecycle fixture session: %w", err)
	}
	return nil
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
	if err := publishLifecycleServerFixtureReadiness(processConfig.ReadyPath, ready); err != nil {
		return fmt.Errorf("publish lifecycle server fixture readiness: %w", err)
	}
	select {}
}

func publishLifecycleServerFixtureReadiness(path string, contents []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".lifecycle-server-ready-*")
	if err != nil {
		return fmt.Errorf("create readiness staging file: %w", err)
	}
	stagedPath := file.Name()
	defer func() { _ = os.Remove(stagedPath) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("set readiness staging mode: %w", err)
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return fmt.Errorf("write readiness staging file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close readiness staging file: %w", err)
	}
	if err := os.Rename(stagedPath, path); err != nil {
		return fmt.Errorf("publish readiness staging file: %w", err)
	}
	return nil
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
