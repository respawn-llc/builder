package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	checkpoint "core/internal/testharness/pty/analyzer"
	"core/internal/testharness/pty/appfixture"
	serverstartup "core/server/startup"
	"core/shared/config"
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
	scenarioState, runtime, sessionID, err := prepareLifecyclePTYFixtureRuntime(ctx, terminal, processConfig)
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
	serverConfig, clientSettings, err := config.LoadInteractive(
		processConfig.WorkspaceRoot,
		config.LoadOptions{ConfigRoot: processConfig.PersistenceRoot},
	)
	if err != nil {
		return fmt.Errorf("load lifecycle fixture interactive config: %w", err)
	}
	if processConfig.LocalScriptPath != nil &&
		(!serverConfig.Settings.ProviderCapabilities.SupportsResponsesCompact ||
			!serverConfig.Settings.ProviderCapabilities.SupportsRequestInputTokenCount) {
		return fmt.Errorf(
			"lifecycle fixture provider capabilities do not support scripted compaction: %+v",
			serverConfig.Settings.ProviderCapabilities,
		)
	}
	interactor := newInteractiveAuthInteractor()
	if processConfig.ServerMode == appfixture.LifecycleServerModeLocal {
		standingServer, err := startLifecycleHookFixtureServer(ctx, options, interactor, runtime)
		if err != nil {
			return err
		}
		defer func() { runErr = errors.Join(runErr, standingServer.Close()) }()
	}

	server, err := startSessionServer(ctx, options, interactor, true, serverConfig)
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

	intent, err := lifecyclePTYLaunchIntent(processConfig.OpeningKind, sessionID)
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
	if processConfig.LocalScriptPath != nil &&
		(!plan.ActiveSettings.ProviderCapabilities.SupportsResponsesCompact ||
			!plan.ActiveSettings.ProviderCapabilities.SupportsRequestInputTokenCount) {
		return fmt.Errorf(
			"lifecycle fixture session plan lost scripted compaction capabilities: %+v",
			plan.ActiveSettings.ProviderCapabilities,
		)
	}
	hookAttachmentPlan, err := deriveClientHookAttachmentPlan(clientSettings, intent, plan)
	if err != nil {
		return err
	}
	initialPrompt := ""
	if processConfig.InitialPrompt != nil {
		initialPrompt = *processConfig.InitialPrompt
	}
	runtimePlan, request, err := prepareSessionUIRun(
		ctx,
		server,
		planner,
		plan,
		initialPrompt,
		false,
		"",
		false,
		hookAttachmentPlan,
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
	switch processConfig.ServerMode {
	case appfixture.LifecycleServerModeRemote:
		if processConfig.TargetFinalAssistantCount == 0 {
			return nil, nil, "", errors.New("remote lifecycle PTY fixture target final count is required")
		}
		state := newPTYCheckpointScenarioState(
			appfixture.ScriptFinalAssistantOrdinal(processConfig.TargetFinalAssistantCount),
		)
		state.markScenarioComplete()
		sessionID := ""
		if processConfig.SessionID != nil {
			sessionID = *processConfig.SessionID
		}
		return state, nil, sessionID, nil
	case appfixture.LifecycleServerModeLocal:
	default:
		return nil, nil, "", fmt.Errorf("unsupported lifecycle PTY server mode %q", processConfig.ServerMode)
	}
	if processConfig.LocalScriptPath == nil {
		return nil, nil, "", errors.New("local lifecycle PTY fixture process requires a script path")
	}
	if err := os.MkdirAll(processConfig.WorkspaceRoot, 0o755); err != nil {
		return nil, nil, "", fmt.Errorf("create lifecycle fixture workspace: %w", err)
	}
	recorderCommand, err := lifecycleHookProductRecorderCommand(processConfig)
	if err != nil {
		return nil, nil, "", err
	}
	contextWindow := 40000
	compactionThreshold := 34000
	preSubmitLead := 2000
	compactionMode := config.CompactionMode("native")
	if err := appfixture.PrepareConfigAndBindingWithOptions(
		ctx,
		processConfig.PersistenceRoot,
		processConfig.WorkspaceRoot,
		lifecycleHookFixtureConfigOptions(
			recorderCommand,
			&contextWindow,
			&compactionThreshold,
			&preSubmitLead,
			&compactionMode,
		),
	); err != nil {
		return nil, nil, "", err
	}
	var state *ptyCheckpointScenarioState
	runtime, err := appfixture.NewRuntime(*processConfig.LocalScriptPath, func(
		targetFinalAssistantOrdinal appfixture.ScriptFinalAssistantOrdinal,
	) func(context.Context) error {
		checkpointTarget := targetFinalAssistantOrdinal
		if checkpointTarget == 0 {
			checkpointTarget = 1
		}
		state = newPTYCheckpointScenarioState(checkpointTarget)
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
	var sessionID string
	if processConfig.OpeningKind == appfixture.LifecycleOpeningKindResumed {
		sessionID, err = runtime.SeedSession(ctx, processConfig.PersistenceRoot, processConfig.WorkspaceRoot)
		if err != nil {
			return nil, nil, "", err
		}
		if processConfig.SessionID != nil && *processConfig.SessionID != sessionID {
			return nil, nil, "", fmt.Errorf(
				"seeded lifecycle fixture session id = %q, configured = %q",
				sessionID,
				*processConfig.SessionID,
			)
		}
	}
	return state, runtime, sessionID, nil
}

func startLifecycleHookFixtureServer(
	ctx context.Context,
	options Options,
	interactor authInteractor,
	runtime *appfixture.Runtime,
) (*serverstartup.EmbeddedServer, error) {
	if runtime == nil {
		return nil, errors.New("local lifecycle fixture server requires a runtime")
	}
	standingServer, err := serverstartup.StartWithOptions(ctx, serverstartup.Request{
		WorkspaceRoot:         options.WorkspaceRoot,
		WorkspaceRootExplicit: options.WorkspaceRootExplicit,
		SessionID:             options.SessionID,
		OpenAIBaseURL:         options.OpenAIBaseURL,
		OpenAIBaseURLExplicit: options.OpenAIBaseURLExplicit,
		LoadOptions:           config.LoadOptions{ConfigRoot: options.ConfigRoot},
	}, interactor, nil, runtime.StartupOptions())
	if err != nil {
		return nil, fmt.Errorf("start lifecycle fixture configured server: %w", err)
	}
	if err := standingServer.ServeBackground(); err != nil {
		_ = standingServer.Close()
		return nil, fmt.Errorf("serve lifecycle fixture configured server: %w", err)
	}
	return standingServer, nil
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
	recorderCommand, err := lifecycleHookProductRecorderCommand(appfixture.LifecycleProcessConfig{
		HookRecordPath: processConfig.HookRecordPath,
		HookBehavior:   processConfig.HookBehavior,
	})
	if err != nil {
		return err
	}
	if err := appfixture.PrepareConfigAndBindingWithOptions(
		ctx,
		processConfig.PersistenceRoot,
		processConfig.WorkspaceRoot,
		lifecycleHookFixtureConfigOptions(recorderCommand, nil, nil, nil, nil),
	); err != nil {
		return err
	}
	runtime, err := appfixture.NewRuntime(processConfig.ScriptPath, nil)
	if err != nil {
		return err
	}
	sessionID, err := runtime.SeedSession(ctx, processConfig.PersistenceRoot, processConfig.WorkspaceRoot)
	if err != nil {
		return err
	}
	serverConfig, _, err := config.LoadInteractive(
		processConfig.WorkspaceRoot,
		config.LoadOptions{ConfigRoot: processConfig.PersistenceRoot},
	)
	if err != nil {
		return err
	}
	options := Options{
		WorkspaceRoot:         processConfig.WorkspaceRoot,
		WorkspaceRootExplicit: true,
		ConfigRoot:            processConfig.PersistenceRoot,
		OpenAIBaseURL:         "http://127.0.0.1:1/v1",
		OpenAIBaseURLExplicit: true,
	}
	server, err := startLifecycleHookFixtureServer(
		ctx,
		options,
		newInteractiveAuthInteractor(),
		runtime,
	)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, server.Close()) }()
	ready, err := json.Marshal(struct {
		PID        int    `json:"pid"`
		SessionID  string `json:"session_id"`
		ServerPort int    `json:"server_port"`
	}{
		PID:        os.Getpid(),
		SessionID:  sessionID,
		ServerPort: serverConfig.Settings.ServerPort,
	})
	if err != nil {
		return fmt.Errorf("marshal lifecycle server fixture readiness: %w", err)
	}
	if err := os.WriteFile(processConfig.ReadyPath, ready, 0o600); err != nil {
		return fmt.Errorf("publish lifecycle server fixture readiness: %w", err)
	}
	select {}
}

func lifecycleHookFixtureConfigOptions(
	recorderCommand []string,
	contextWindow *int,
	compactionThreshold *int,
	preSubmitLead *int,
	compactionMode *config.CompactionMode,
) appfixture.ConfigOptions {
	return appfixture.ConfigOptions{
		LifecycleHookCommand:             recorderCommand,
		ModelContextWindow:               contextWindow,
		ContextCompactionThresholdTokens: compactionThreshold,
		PreSubmitCompactionLeadTokens:    preSubmitLead,
		CompactionMode:                   compactionMode,
		ProviderCapabilities: &config.ProviderCapabilitiesOverride{
			ProviderID:                     "openai",
			SupportsResponsesAPI:           true,
			SupportsResponsesCompact:       true,
			SupportsRequestInputTokenCount: true,
			IsOpenAIFirstParty:             true,
		},
	}
}

func lifecycleHookProductRecorderCommand(config appfixture.LifecycleProcessConfig) ([]string, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve lifecycle recorder executable: %w", err)
	}
	command := []string{
		executable,
		appfixture.LifecycleHookProductRecorderRunArg,
		"--",
		string(config.HookBehavior),
		config.HookRecordPath,
	}
	if config.HookBehavior == appfixture.LifecycleHookBehaviorHang {
		if config.HookReadyPath == nil {
			return nil, errors.New("hanging lifecycle recorder requires a ready path")
		}
		command = append(command, *config.HookReadyPath)
	}
	if config.HookBehavior == appfixture.LifecycleHookBehaviorNonzeroOnce {
		if config.HookStatePath == nil {
			return nil, errors.New("non-zero-once lifecycle recorder requires a state path")
		}
		command = append(command, *config.HookStatePath)
	}
	return command, nil
}

func lifecyclePTYLaunchIntent(
	openingKind appfixture.LifecycleOpeningKind,
	sessionID string,
) (serverapi.SessionLaunchIntent, error) {
	switch openingKind {
	case appfixture.LifecycleOpeningKindNew:
		return serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()), nil
	case appfixture.LifecycleOpeningKindResumed:
		parsed, err := runtimeids.ParseSessionID(sessionID)
		if err != nil {
			return serverapi.SessionLaunchIntent{}, err
		}
		return serverapi.OpenExistingSessionLaunchIntent(parsed), nil
	default:
		return serverapi.SessionLaunchIntent{}, fmt.Errorf("unsupported lifecycle PTY opening kind %q", openingKind)
	}
}
