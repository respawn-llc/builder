package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	checkpoint "core/internal/testharness/pty/analyzer"
	"core/internal/testharness/pty/appfixture"
	serverstartup "core/server/startup"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type ptyCheckpointTerminalFile struct {
	*os.File
	writer *checkpoint.Writer
}

func newPTYCheckpointTerminalFile(file *os.File) *ptyCheckpointTerminalFile {
	return &ptyCheckpointTerminalFile{
		File:   file,
		writer: checkpoint.NewWriter(file),
	}
}

func (file *ptyCheckpointTerminalFile) Write(payload []byte) (int, error) {
	return file.writer.Write(payload)
}

func runPTYFixtureProcess(t *testing.T, ctx context.Context, processConfig appfixture.ProcessConfig) (runErr error) {
	t.Helper()
	if err := processConfig.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(processConfig.WorkspaceRoot, 0o755); err != nil {
		return fmt.Errorf("create fixture workspace: %w", err)
	}
	terminal := newPTYCheckpointTerminalFile(os.Stdout)
	if err := terminal.writer.QueueBeforeNextWrite(checkpoint.KindScenarioStart, nil); err != nil {
		return fmt.Errorf("queue scenario-start checkpoint: %w", err)
	}
	scenarioState, runtime, err := newCheckpointPTYFixtureRuntime(processConfig.ScriptPath, terminal)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, runtime.Close()) }()
	baseURL := runtime.OpenAIBaseURL()
	if err := appfixture.PrepareConfigAndBindingWithOptions(
		ctx,
		processConfig.PersistenceRoot,
		processConfig.WorkspaceRoot,
		appfixture.ConfigOptions{OpenAIBaseURL: &baseURL},
	); err != nil {
		return err
	}
	defer func() {
		runErr = errors.Join(runErr, appfixture.WriteObservation(
			processConfig.ObservationPath,
			runtime.Observation(runErr),
		))
	}()

	sessionID, err := runtime.SeedSession(ctx, processConfig.PersistenceRoot, processConfig.WorkspaceRoot)
	if err != nil {
		return err
	}
	options := Options{
		WorkspaceRoot:         processConfig.WorkspaceRoot,
		WorkspaceRootExplicit: true,
		SessionID:             sessionID,
		ConfigRoot:            processConfig.PersistenceRoot,
	}
	interactor := newInteractiveAuthInteractor()
	startConfiguredDaemonFixture(t, processConfig.WorkspaceRoot, serverStartupRequest(
		t, processConfig.WorkspaceRoot, processConfig.PersistenceRoot,
	), readyMemoryAuthHandler())

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
	if _, ok := finalModel.(*ptyCheckpointModel); !ok {
		return fmt.Errorf("PTY fixture final model has unexpected type %T", finalModel)
	}
	return nil
}

func serverStartupRequest(t *testing.T, workspaceRoot, persistenceRoot string) serverstartup.Request {
	t.Setenv(config.PersistenceRootEnvName, persistenceRoot)
	return serverstartup.Request{
		WorkspaceRoot:         workspaceRoot,
		WorkspaceRootExplicit: true,
		AllowUnauthenticated:  true,
		LoadOptions:           config.LoadOptions{ConfigRoot: persistenceRoot},
	}
}
