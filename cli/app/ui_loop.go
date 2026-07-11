package app

import (
	"errors"
	"io"
	"os"

	"core/cli/app/commands"
	"core/cli/tui/ongoing"
	"core/shared/config"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

type uiProgramComposition struct {
	model   *uiModel
	options []tea.ProgramOption
	logger  uiLogger
	close   func()
}

type uiLoopRequest struct {
	wiring                       *runtimeWiring
	active                       config.Settings
	logger                       *runLogger
	commandRegistry              *commands.Registry
	initialPrompt                string
	initialPromptHistoryRecorded bool
	initialInput                 string
	recoveryBuffers              []serverapi.SessionDraftRecoveryBuffer
	sessionName                  string
	modelContractLocked          bool
	configuredModelName          string
	statusConfig                 uiStatusConfig
	startupUpdateNotice          bool
}

func runUILoop(request uiLoopRequest) (tea.Model, error) {
	composition, err := composeUIProgram(request, os.Stdout)
	if err != nil {
		return nil, err
	}
	return runUIProgram(composition, composition.model)
}

func runUIProgram(composition *uiProgramComposition, initialModel tea.Model) (tea.Model, error) {
	if composition == nil {
		return nil, errors.New("UI program composition is required")
	}
	if initialModel == nil {
		return nil, errors.New("UI program model is required")
	}
	defer composition.close()
	finalModel, runErr := tea.NewProgram(initialModel, composition.options...).Run()
	if runErr != nil {
		if composition.logger != nil {
			composition.logger.Logf("app.exit err=%q", runErr.Error())
		}
		return nil, runErr
	}
	if composition.logger != nil {
		composition.logger.Logf("app.exit ok")
	}
	return finalModel, nil
}

func composeUIProgram(request uiLoopRequest, output io.Writer) (*uiProgramComposition, error) {
	terminalCursor := newUITerminalCursorState()
	rendererOutputGate := newUIRendererOutputGateState()
	// Preserve terminal-file identity (Fd/Read/Close) so Bubble Tea can detect
	// the real terminal and emit WindowSizeMsg.
	terminalOutput := newUITerminalOutputFile(output)
	ongoingSurface := ongoing.NewSurface(terminalOutput)
	options := mainUIProgramOptionsWithOutput(
		request.active,
		terminalCursor,
		rendererOutputGate,
		terminalOutput,
	)
	tuiLogger, err := newRollingTUILogger(request.statusConfig.PersistenceRoot)
	if err != nil && request.logger != nil {
		request.logger.Logf("tui_log.open err=%q", err.Error())
	}
	uiLogger := newMultiUILogger(request.logger, tuiLogger)
	runtimeClient := request.wiring.runtimeClient
	if runtimeClient == nil {
		if tuiLogger != nil {
			_ = tuiLogger.Close()
		}
		return nil, errors.New("runtime client is required")
	}
	runtimeEvents := request.wiring.runtimeEvents
	if runtimeEvents == nil {
		if tuiLogger != nil {
			_ = tuiLogger.Close()
		}
		return nil, errors.New("runtime event stream is required")
	}
	askEvents := request.wiring.askEvents
	if askEvents == nil {
		if tuiLogger != nil {
			_ = tuiLogger.Close()
		}
		return nil, errors.New("prompt event stream is required")
	}
	sessionID := ""
	if runtimeClient != nil {
		sessionID = runtimeClient.MainView().Session.SessionID
	}

	rawModel := NewProjectedUIModel(
		runtimeClient,
		runtimeEvents,
		askEvents,
		WithUILogger(uiLogger),
		WithUIModelName(request.active.Model),
		WithUIConfiguredModelName(request.configuredModelName),
		WithUIThinkingLevel(request.active.ThinkingLevel),
		WithUIModelContractLocked(request.modelContractLocked),
		WithUITheme(request.active.Theme),
		WithUIDebug(request.active.Debug),
		WithUICommandRegistry(request.commandRegistry),
		WithUIHasOtherSessions(request.wiring.hasOtherSessionsKnown, request.wiring.hasOtherSessions),
		WithUITurnQueueHook(request.wiring.turnQueueHook),
		WithUIProcessClient(newUIProcessClientWithReads(request.wiring.processViews, request.wiring.processControls)),
		WithUIWorktreeClient(request.wiring.worktrees),
		WithUIPromptHistory(request.wiring.promptHistory),
		WithUIStartupSubmit(request.initialPrompt),
		WithUIStartupSubmitPromptHistoryRecorded(request.initialPromptHistoryRecorded),
		WithUIInitialInput(request.initialInput),
		WithUIInitialRecoveryBuffers(request.recoveryBuffers),
		WithUISessionName(request.sessionName),
		WithUISessionID(sessionID),
		WithUIStatusConfig(request.statusConfig),
		WithUIStartupUpdateNotice(request.startupUpdateNotice),
		WithUITerminalCursorState(terminalCursor),
		WithUIRendererOutputGateState(rendererOutputGate),
		WithUIOngoingSurface(ongoingSurface),
		WithUIOngoingTranscriptEvents(request.wiring.transcriptEvents),
		WithUIOngoingTranscriptReopen(request.wiring.requestTranscriptOpen),
		WithUITerminalFocusState(request.wiring.terminalFocus),
	)
	model, ok := rawModel.(*uiModel)
	if !ok {
		if tuiLogger != nil {
			_ = tuiLogger.Close()
		}
		return nil, errors.New("projected UI model has unexpected type")
	}
	model.ongoingTranscript = newOngoingTranscriptController(ongoingSurface, model.ongoingFrameInput)
	return &uiProgramComposition{
		model:   model,
		options: options,
		logger:  uiLogger,
		close: func() {
			model.Close()
			if tuiLogger != nil {
				_ = tuiLogger.Close()
			}
		},
	}, nil
}

func mainUIProgramOptionsWithOutput(
	active config.Settings,
	terminalCursor *uiTerminalCursorState,
	rendererOutputGate *uiRendererOutputGateState,
	output io.Writer,
) []tea.ProgramOption {
	options := []tea.ProgramOption{
		tea.WithFilter(terminalCursorProgramFilter(terminalCursor)),
		tea.WithReportFocus(),
	}
	rendererOutput := output
	if terminalCursor != nil {
		rendererOutput = newUITerminalCursorWriter(rendererOutput, terminalCursor)
	}
	if rendererOutputGate != nil {
		rendererOutput = newUIRendererOutputGateWriter(rendererOutput, rendererOutputGate)
	}
	if rendererOutput != nil && rendererOutput != output {
		options = append(options, tea.WithOutput(rendererOutput))
	}
	return options
}

func extractUITransition(model tea.Model) UITransition {
	if model == nil {
		return UITransition{Action: UIActionNone}
	}
	typed, ok := model.(*uiModel)
	if !ok {
		return UITransition{Action: UIActionNone}
	}
	return typed.Transition()
}
