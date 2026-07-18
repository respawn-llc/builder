package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"sync"

	checkpoint "core/internal/testharness/pty/analyzer"
	"core/internal/testharness/pty/appfixture"
	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type ptyCheckpointTerminalFile struct {
	file   *os.File
	writer *checkpoint.Writer
}

func runConfiguredPTYClientProcess(ctx context.Context, processConfig appfixture.ConfiguredClientProcessConfig) (runErr error) {
	if err := processConfig.Validate(); err != nil {
		return err
	}
	endpoint, err := url.Parse(processConfig.ConfiguredServerEndpoint)
	if err != nil {
		return fmt.Errorf("parse configured client endpoint: %w", err)
	}
	host, port, err := net.SplitHostPort(endpoint.Host)
	if err != nil {
		return fmt.Errorf("split configured client endpoint: %w", err)
	}
	if err := os.Setenv(config.PersistenceRootEnvName, processConfig.PersistenceRoot); err != nil {
		return err
	}
	if err := os.Setenv("KENT_SERVER_HOST", host); err != nil {
		return err
	}
	if err := os.Setenv("KENT_SERVER_PORT", port); err != nil {
		return err
	}
	writer := checkpoint.NewWriter(os.Stdout)
	if err := writer.Emit(checkpoint.KindAppRunStarted, nil); err != nil {
		return err
	}
	previousSessionPicker := runSessionPickerFlow
	previousAuthPicker := runStartupPickerFlow
	runSessionPickerFlow = func(loader sessionPageLoader, theme string, header sessionPickerHeaderInfo) (sessionPickerResult, error) {
		if header.updateStatus == nil {
			return nil, errors.New("configured PTY picker requires server update-status client")
		}
		header.updateStatus = &configuredPTYUpdateStatusClient{
			inner: header.updateStatus,
			onStart: func() error {
				return writer.Emit(checkpoint.KindSessionPickerReady, nil)
			},
		}
		return previousSessionPicker(
			loader,
			theme,
			header,
		)
	}
	runStartupPickerFlow = func(model *startupPickerModel) (startupPickerResult, error) {
		if err := writer.Emit(checkpoint.KindAuthPickerReady, nil); err != nil {
			return startupPickerResult{}, err
		}
		return previousAuthPicker(model)
	}
	defer func() {
		runSessionPickerFlow = previousSessionPicker
		runStartupPickerFlow = previousAuthPicker
		if err := writer.Emit(checkpoint.KindAppRunExited, nil); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}()
	return Run(ctx, Options{
		WorkspaceRoot:         processConfig.WorkspaceRoot,
		WorkspaceRootExplicit: true,
		ConfigRoot:            processConfig.PersistenceRoot,
	})
}

type configuredPTYUpdateStatusClient struct {
	inner    apicontract.ServerStatusService
	onStart  func() error
	once     sync.Once
	readyErr error
}

func (client *configuredPTYUpdateStatusClient) GetServerReadiness(ctx context.Context, request serverapi.ServerReadinessRequest) (serverapi.ServerReadinessResponse, error) {
	return client.inner.GetServerReadiness(ctx, request)
}

func (client *configuredPTYUpdateStatusClient) GetUpdateStatus(ctx context.Context, request serverapi.UpdateStatusRequest) (serverapi.UpdateStatusResponse, error) {
	client.once.Do(func() {
		client.readyErr = client.onStart()
	})
	if client.readyErr != nil {
		return serverapi.UpdateStatusResponse{}, client.readyErr
	}
	return client.inner.GetUpdateStatus(ctx, request)
}

func newPTYCheckpointTerminalFile(file *os.File) *ptyCheckpointTerminalFile {
	if file == nil {
		panic("create PTY checkpoint terminal with nil file")
	}
	return &ptyCheckpointTerminalFile{
		file:   file,
		writer: checkpoint.NewWriter(file),
	}
}

func (file *ptyCheckpointTerminalFile) Write(payload []byte) (int, error) {
	return file.writer.Write(payload)
}

func (file *ptyCheckpointTerminalFile) Read(payload []byte) (int, error) {
	return file.file.Read(payload)
}

func (file *ptyCheckpointTerminalFile) Close() error {
	return file.file.Close()
}

func (file *ptyCheckpointTerminalFile) Fd() uintptr {
	return file.file.Fd()
}

func runPTYFixtureProcess(ctx context.Context, processConfig appfixture.ProcessConfig) (runErr error) {
	if err := processConfig.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(processConfig.WorkspaceRoot, 0o755); err != nil {
		return fmt.Errorf("create fixture workspace: %w", err)
	}
	if err := appfixture.PrepareConfigAndBinding(ctx, processConfig.PersistenceRoot, processConfig.WorkspaceRoot); err != nil {
		return err
	}

	terminal := newPTYCheckpointTerminalFile(os.Stdout)
	if err := terminal.writer.QueueBeforeNextWrite(checkpoint.KindScenarioStart, nil); err != nil {
		return fmt.Errorf("queue scenario-start checkpoint: %w", err)
	}
	var scenarioState *ptyCheckpointScenarioState
	runtime, err := appfixture.NewRuntime(processConfig.ScriptPath, func(
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
		panic("PTY fixture runtime did not configure scenario completion state")
	}
	defer func() {
		observationErr := appfixture.WriteObservation(
			processConfig.ObservationPath,
			runtime.Observation(runErr),
		)
		if observationErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("write fixture observation: %w", observationErr))
		}
	}()

	sessionID, err := runtime.SeedSession(ctx, processConfig.PersistenceRoot, processConfig.WorkspaceRoot)
	if err != nil {
		return err
	}
	options := Options{
		WorkspaceRoot:         processConfig.WorkspaceRoot,
		SessionID:             sessionID,
		ConfigRoot:            processConfig.PersistenceRoot,
		OpenAIBaseURL:         "http://127.0.0.1:1/v1",
		OpenAIBaseURLExplicit: true,
		startupOptions:        runtime.StartupOptions(),
	}
	interactor := newInteractiveAuthInteractor()
	standingServer, err := startEmbeddedServer(ctx, options, interactor, true)
	if err != nil {
		return fmt.Errorf("start fixture server: %w", err)
	}
	defer func() { _ = standingServer.Close() }()

	server, err := startSessionServer(ctx, options, interactor, true)
	if err != nil {
		return err
	}
	boundServer, err := ensureInteractiveProjectBinding(ctx, server)
	if err != nil {
		_ = server.Close()
		return err
	}
	if shouldCloseReboundServer(server, boundServer) {
		defer func() { _ = boundServer.Close() }()
	}
	server = boundServer

	planner := newSessionLaunchPlanner(server)
	parsedSessionID, err := runtimeids.ParseSessionID(sessionID)
	if err != nil {
		return err
	}
	launchRequest := sessionLaunchRequest{
		Mode:   launchModeInteractive,
		Intent: serverapi.OpenExistingSessionLaunchIntent(parsedSessionID),
	}
	plan, err := planner.PlanSession(ctx, launchRequest)
	if err != nil {
		return err
	}
	runtimePlan, request, err := prepareSessionUIRun(ctx, server, planner, plan, "", false, "", false)
	if err != nil {
		return err
	}
	defer runtimePlan.Close()
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
		return fmt.Errorf("PTY fixture final model has unexpected type %T", finalModel)
	}
	if _, ok := finalWrapped.appModel(); !ok {
		return fmt.Errorf("PTY fixture inner final model has unexpected type %T", finalWrapped.inner)
	}
	return nil
}
