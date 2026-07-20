package app

import (
	"context"
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
	if processConfig.ServerMode != appfixture.LifecycleServerModeLocal {
		return errors.New("remote lifecycle PTY fixture process is not implemented")
	}
	if processConfig.LocalScriptPath == nil {
		return errors.New("local lifecycle PTY fixture process requires a script path")
	}
	if err := os.MkdirAll(processConfig.WorkspaceRoot, 0o755); err != nil {
		return fmt.Errorf("create lifecycle fixture workspace: %w", err)
	}
	recorderCommand, err := lifecycleHookProductRecorderCommand(processConfig)
	if err != nil {
		return err
	}
	contextWindow := 40000
	compactionThreshold := 34000
	preSubmitLead := 2000
	compactionMode := config.CompactionMode("native")
	if err := appfixture.PrepareConfigAndBindingWithOptions(
		ctx,
		processConfig.PersistenceRoot,
		processConfig.WorkspaceRoot,
		appfixture.ConfigOptions{
			LifecycleHookCommand:             recorderCommand,
			ModelContextWindow:               &contextWindow,
			ContextCompactionThresholdTokens: &compactionThreshold,
			PreSubmitCompactionLeadTokens:    &preSubmitLead,
			CompactionMode:                   &compactionMode,
			ProviderCapabilities: &config.ProviderCapabilitiesOverride{
				ProviderID:                     "openai",
				SupportsResponsesAPI:           true,
				SupportsResponsesCompact:       true,
				SupportsRequestInputTokenCount: true,
				IsOpenAIFirstParty:             true,
			},
		},
	); err != nil {
		return err
	}

	terminal := newPTYCheckpointTerminalFile(os.Stdout)
	if err := terminal.writer.QueueBeforeNextWrite(checkpoint.KindScenarioStart, nil); err != nil {
		return fmt.Errorf("queue lifecycle scenario-start checkpoint: %w", err)
	}
	var scenarioState *ptyCheckpointScenarioState
	runtime, err := appfixture.NewRuntime(*processConfig.LocalScriptPath, func(
		targetFinalAssistantOrdinal appfixture.ScriptFinalAssistantOrdinal,
	) func(context.Context) error {
		scenarioState = newPTYCheckpointScenarioState(targetFinalAssistantOrdinal)
		return func(context.Context) error {
			if err := terminal.writer.Emit(checkpoint.KindScenarioComplete, nil); err != nil {
				return err
			}
			scenarioState.markScenarioComplete()
			return nil
		}
	})
	if err != nil {
		return err
	}
	if scenarioState == nil {
		panic("lifecycle PTY fixture runtime did not configure scenario completion state")
	}
	if processConfig.TargetFinalAssistantCount != uint64(runtime.TargetFinalAssistantOrdinal()) {
		return fmt.Errorf(
			"lifecycle fixture target final count = %d, script target = %d",
			processConfig.TargetFinalAssistantCount,
			runtime.TargetFinalAssistantOrdinal(),
		)
	}

	var sessionID string
	if processConfig.OpeningKind == appfixture.LifecycleOpeningKindResumed {
		sessionID, err = runtime.SeedSession(ctx, processConfig.PersistenceRoot, processConfig.WorkspaceRoot)
		if err != nil {
			return err
		}
		if processConfig.SessionID != nil && *processConfig.SessionID != sessionID {
			return fmt.Errorf("seeded lifecycle fixture session id = %q, configured = %q", sessionID, *processConfig.SessionID)
		}
	}
	options := Options{
		WorkspaceRoot:         processConfig.WorkspaceRoot,
		WorkspaceRootExplicit: true,
		SessionID:             sessionID,
		ConfigRoot:            processConfig.PersistenceRoot,
		OpenAIBaseURL:         "http://127.0.0.1:1/v1",
		OpenAIBaseURLExplicit: true,
		startupOptions:        runtime.StartupOptions(),
	}
	serverConfig, clientSettings, err := config.LoadInteractive(
		processConfig.WorkspaceRoot,
		config.LoadOptions{ConfigRoot: processConfig.PersistenceRoot},
	)
	if err != nil {
		return fmt.Errorf("load lifecycle fixture interactive config: %w", err)
	}
	if !serverConfig.Settings.ProviderCapabilities.SupportsResponsesCompact ||
		!serverConfig.Settings.ProviderCapabilities.SupportsRequestInputTokenCount {
		return fmt.Errorf(
			"lifecycle fixture provider capabilities do not support scripted compaction: %+v",
			serverConfig.Settings.ProviderCapabilities,
		)
	}
	interactor := newInteractiveAuthInteractor()
	standingServer, err := serverstartup.StartWithOptions(ctx, serverstartup.Request{
		WorkspaceRoot:         options.WorkspaceRoot,
		WorkspaceRootExplicit: options.WorkspaceRootExplicit,
		SessionID:             options.SessionID,
		OpenAIBaseURL:         options.OpenAIBaseURL,
		OpenAIBaseURLExplicit: options.OpenAIBaseURLExplicit,
		LoadOptions: config.LoadOptions{
			ConfigRoot: options.ConfigRoot,
		},
	}, interactor, nil, runtime.StartupOptions())
	if err != nil {
		return fmt.Errorf("start lifecycle fixture configured server: %w", err)
	}
	defer func() { runErr = errors.Join(runErr, standingServer.Close()) }()
	if err := standingServer.ServeBackground(); err != nil {
		return fmt.Errorf("serve lifecycle fixture configured server: %w", err)
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
	if !plan.ActiveSettings.ProviderCapabilities.SupportsResponsesCompact ||
		!plan.ActiveSettings.ProviderCapabilities.SupportsRequestInputTokenCount {
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

func lifecycleHookProductRecorderCommand(config appfixture.LifecycleProcessConfig) ([]string, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve lifecycle recorder executable: %w", err)
	}
	command := []string{
		executable,
		lifecycleHookProductRecorderRunArg,
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
