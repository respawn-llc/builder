package app

import (
	"context"
	"errors"
	"fmt"
	"os"

	checkpoint "core/internal/testharness/pty/analyzer"
	"core/internal/testharness/pty/appfixture"
)

type ptyCheckpointTerminalFile struct {
	file   *os.File
	writer *checkpoint.Writer
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
	server, err := startSessionServer(ctx, options, interactor, true)
	if err != nil {
		return err
	}
	defer func() { _ = server.Close() }()

	planner := newSessionLaunchPlanner(server)
	plan, err := planner.PlanSession(ctx, sessionLaunchRequest{
		Mode:              launchModeInteractive,
		SelectedSessionID: sessionID,
	})
	if err != nil {
		return err
	}
	runtimePlan, request, err := prepareSessionUIRun(ctx, server, planner, plan, "", false, "", true)
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
