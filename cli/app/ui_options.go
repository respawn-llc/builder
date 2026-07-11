package app

import (
	"strings"

	"core/cli/app/commands"
	"core/cli/tui"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/serverapi"
)

type UIOption func(*uiModel)

type UIAction = serverapi.SessionTransitionAction

type UITransition struct {
	Action                       serverapi.SessionTransitionAction
	Exit                         bool
	InitialPrompt                string
	InitialPromptHistoryRecorded bool
	InitialInput                 string
	TargetSessionID              string
	ForkRollbackTargetID         string
	ParentSessionID              string
}

const (
	UIActionNone         UIAction = serverapi.SessionTransitionActionNone
	UIActionExit         UIAction = "exit"
	UIActionNewSession   UIAction = serverapi.SessionTransitionActionNewSession
	UIActionResume       UIAction = serverapi.SessionTransitionActionResume
	UIActionLogout       UIAction = serverapi.SessionTransitionActionLogout
	UIActionForkRollback UIAction = serverapi.SessionTransitionActionForkRollback
	UIActionOpenSession  UIAction = serverapi.SessionTransitionActionOpenSession
)

func WithUILogger(logger uiLogger) UIOption {
	return func(m *uiModel) {
		m.logger = logger
		if logger != nil {
			if configurable, ok := m.engine.(interface{ SetTranscriptDiagnosticLogger(func(string)) }); ok {
				configurable.SetTranscriptDiagnosticLogger(func(line string) {
					logger.Logf("%s", strings.TrimSpace(line))
				})
			}
		}
	}
}

func WithUITranscriptDiagnostics(enabled bool) UIOption {
	return func(m *uiModel) {
		m.transcriptDiagnostics = enabled
		m.updateTranscriptDiagnosticsMode()
	}
}

func WithUIDebug(enabled bool) UIOption {
	return func(m *uiModel) {
		m.debugMode = enabled
		m.updateTranscriptDiagnosticsMode()
	}
}

func WithUITerminalCursorState(state *uiTerminalCursorState) UIOption {
	return func(m *uiModel) {
		m.terminalCursor = state
	}
}

func WithUIRendererOutputGateState(state *uiRendererOutputGateState) UIOption {
	return func(m *uiModel) {
		m.rendererOutputGate = state
		m.syncRendererOutputGate()
	}
}

func WithUIModelName(model string) UIOption {
	return func(m *uiModel) {
		m.modelName = strings.TrimSpace(model)
	}
}

func WithUIConfiguredModelName(model string) UIOption {
	return func(m *uiModel) {
		m.configuredModelName = strings.TrimSpace(model)
	}
}

func WithUIThinkingLevel(thinkingLevel string) UIOption {
	return func(m *uiModel) {
		m.thinkingLevel = strings.TrimSpace(thinkingLevel)
	}
}

func WithUIFastModeAvailable(available bool) UIOption {
	return func(m *uiModel) {
		m.fastModeAvailable = available
	}
}

func WithUIFastModeEnabled(enabled bool) UIOption {
	return func(m *uiModel) {
		m.fastModeEnabled = enabled
	}
}

func WithUIConversationFreshness(freshness clientui.ConversationFreshness) UIOption {
	return func(m *uiModel) {
		m.conversationFreshness = freshness
	}
}

func WithUIModelContractLocked(locked bool) UIOption {
	return func(m *uiModel) {
		m.modelContractLocked = locked
	}
}

func WithUITheme(theme string) UIOption {
	return func(m *uiModel) {
		m.theme = strings.TrimSpace(theme)
		m.view = tui.NewModel(tui.WithTheme(theme))
	}
}

func WithUICommandRegistry(registry *commands.Registry) UIOption {
	return func(m *uiModel) {
		if registry == nil {
			return
		}
		m.commandRegistry = registry
	}
}

func WithUIStartupSubmit(text string) UIOption {
	return func(m *uiModel) {
		m.startupSubmit = text
	}
}

func WithUIStartupSubmitPromptHistoryRecorded(recorded bool) UIOption {
	return func(m *uiModel) {
		m.startupSubmitPromptHistoryRecorded = recorded
	}
}

func WithUIInitialInput(text string) UIOption {
	return func(m *uiModel) {
		if text == "" || m.input != "" {
			return
		}
		m.replaceMainInput(text, -1)
	}
}

func WithUIInitialRecoveryBuffers(buffers []serverapi.SessionDraftRecoveryBuffer) UIOption {
	return func(m *uiModel) {
		m.restoreSessionDraftRecoveryBuffers(buffers)
	}
}

func WithUISessionName(name string) UIOption {
	return func(m *uiModel) {
		m.sessionName = strings.TrimSpace(name)
	}
}

func WithUISessionID(sessionID string) UIOption {
	return func(m *uiModel) {
		m.sessionID = strings.TrimSpace(sessionID)
	}
}

func WithUIProcessClient(client clientui.ProcessClient) UIOption {
	return func(m *uiModel) {
		m.processClient = client
		m.processClientExplicit = true
	}
}

func WithUIWorktreeClient(client client.WorktreeClient) UIOption {
	return func(m *uiModel) {
		m.worktreeClient = client
	}
}

func WithUITurnQueueHook(hook turnQueueHook) UIOption {
	return func(m *uiModel) {
		m.turnQueueHook = hook
	}
}

func WithUITerminalFocusState(state *terminalFocusState) UIOption {
	return func(m *uiModel) {
		if state != nil {
			m.terminalFocus = state
		}
	}
}

func WithUIPromptHistory(history []string) UIOption {
	return func(m *uiModel) {
		m.loadPromptHistory(history)
	}
}

func WithUIStartupUpdateNotice(enabled bool) UIOption {
	return func(m *uiModel) {
		m.startupUpdateNotice = enabled
	}
}

func WithUIClipboardPaster(paster uiClipboardPaster) UIOption {
	return func(m *uiModel) {
		m.clipboardPaster = paster
	}
}

func WithUIClipboardTextCopier(copier uiClipboardTextCopier) UIOption {
	return func(m *uiModel) {
		m.clipboardTextCopier = copier
	}
}
