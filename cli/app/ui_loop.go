package app

import (
	"errors"
	"fmt"
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
	terminalCapabilities := currentTerminalCapabilities()
	ongoingSurface := ongoing.NewSurfaceWithOptions(
		terminalOutput,
		ongoing.SurfaceOptions{
			TerminalResize: terminalCapabilities.ResizePolicy,
			MarkdownLinks:  terminalCapabilities.MarkdownLinks,
		},
	)
	options := mainUIProgramOptionsWithOutput(
		request.active,
		terminalCursor,
		rendererOutputGate,
		terminalOutput,
	)
	tuiLogger, _ := newRollingTUILogger(request.statusConfig.PersistenceRoot)
	uiLogger := newMultiUILogger(tuiLogger)
	runtimeClient := request.wiring.runtimeClient
	if runtimeClient == nil {
		if tuiLogger != nil {
			_ = tuiLogger.Close()
		}
		return nil, errors.New("runtime client is required")
	}
	if request.wiring.transcriptEvents == nil {
		if tuiLogger != nil {
			_ = tuiLogger.Close()
		}
		return nil, errors.New("transcript event stream is required")
	}
	// The first renderer write occurs only after Bubble Tea owns terminal mode.
	// Queue the native-cursor signal there, so it is a real input-ready boundary.
	if err := terminalOutput.AnnounceInputReady(); err != nil {
		return nil, fmt.Errorf("announce terminal input readiness: %w", err)
	}
	sessionID := ""
	if runtimeClient != nil {
		sessionID = runtimeClient.MainView().Session.SessionID
	}

	rawModel := NewProjectedUIModel(
		runtimeClient,
		WithUILogger(uiLogger),
		WithUIModelName(request.active.Model),
		WithUIConfiguredModelName(request.configuredModelName),
		WithUIThinkingLevel(request.active.ThinkingLevel),
		WithUIModelContractLocked(request.modelContractLocked),
		WithUITheme(request.active.Theme),
		WithUIMarkdownLinkPresentation(terminalCapabilities.MarkdownLinks),
		WithUIDebug(request.active.Debug),
		WithUICommandRegistry(request.commandRegistry),
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
	model.promptAnswers = request.wiring.promptAnswers
	model.promptAttention = request.wiring.promptAttention
	model.ongoingTranscript = newOngoingTranscriptController(
		ongoingSurface,
		model.ongoingFrameInput,
		model.applyTranscriptMessageState,
		withOngoingTranscriptDeveloperDiagnostics(model.debugMode, model.logf),
	)
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
