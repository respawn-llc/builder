package app

import (
	"fmt"
	"strings"

	"core/cli/app/commands"
	"core/cli/tui"
	"core/cli/tui/transcriptrender"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type UIOption func(*uiModelConstruction)

type UIAction = serverapi.SessionTransitionAction

type UITransition struct {
	Action                       serverapi.SessionTransitionAction
	Exit                         bool
	InitialPrompt                string
	InitialPromptHistoryRecorded bool
	InitialInput                 *string
	TargetSessionID              string
	ForkRollbackTargetID         string
	PreviousSessionID            *runtimeids.SessionID
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
	return func(m *uiModelConstruction) {
		m.logger = logger
	}
}

func WithUIDebug(enabled bool) UIOption {
	return func(m *uiModelConstruction) {
		m.debugMode = enabled
	}
}

func WithUITerminalCursorState(state *uiTerminalCursorState) UIOption {
	return func(m *uiModelConstruction) {
		m.terminalCursor = state
	}
}

func WithUIRendererOutputGateState(state *uiRendererOutputGateState) UIOption {
	return func(m *uiModelConstruction) {
		m.rendererOutputGate = state
		m.syncRendererOutputGate()
	}
}

func WithUIModelName(model string) UIOption {
	return func(m *uiModelConstruction) {
		m.modelName = strings.TrimSpace(model)
	}
}

func WithUIConfiguredModelName(model string) UIOption {
	return func(m *uiModelConstruction) {
		m.configuredModelName = strings.TrimSpace(model)
	}
}

func WithUIThinkingLevel(thinkingLevel string) UIOption {
	return func(m *uiModelConstruction) {
		m.thinkingLevel = strings.TrimSpace(thinkingLevel)
	}
}

func WithUIConversationFreshness(freshness clientui.ConversationFreshness) UIOption {
	return func(m *uiModelConstruction) {
		m.conversationFreshness = freshness
	}
}

func WithUIModelContractLocked(locked bool) UIOption {
	return func(m *uiModelConstruction) {
		m.modelContractLocked = locked
	}
}

func WithUITheme(theme string) UIOption {
	return func(m *uiModelConstruction) {
		m.theme = strings.TrimSpace(theme)
		m.rebuildTranscriptView()
	}
}

func WithUIMarkdownLinkPresentation(
	linkPresentation transcriptrender.MarkdownLinkPresentation,
) UIOption {
	if !linkPresentation.Valid() {
		panic(fmt.Sprintf("configure UI with invalid Markdown link presentation %d", linkPresentation))
	}
	return func(m *uiModelConstruction) {
		m.markdownLinks = linkPresentation
		m.rebuildTranscriptView()
	}
}

func (m *uiModel) rebuildTranscriptView() {
	m.view = tui.NewModel(
		tui.WithTheme(m.theme),
		tui.WithMarkdownLinkPresentation(m.markdownLinks),
	)
}

func WithUICommandRegistry(registry *commands.Registry) UIOption {
	return func(m *uiModelConstruction) {
		if registry == nil {
			return
		}
		m.commandRegistry = registry
	}
}

func WithUIPromptCommandCatalog(catalog apicontract.PromptCommandCatalogService) UIOption {
	return func(m *uiModelConstruction) {
		m.promptCatalog = catalog
	}
}

func WithUIPromptCommandCatalogEntries(entries []commands.PromptCommandCatalogEntry) UIOption {
	return func(m *uiModelConstruction) {
		m.promptCatalogEntries = append([]commands.PromptCommandCatalogEntry(nil), entries...)
	}
}

func WithUIStartupSubmit(text string) UIOption {
	return func(m *uiModelConstruction) {
		m.startupSubmit = text
	}
}

func WithUIStartupSubmitPromptHistoryRecorded(recorded bool) UIOption {
	return func(m *uiModelConstruction) {
		m.startupSubmitPromptHistoryRecorded = recorded
	}
}

func WithUIInitialInput(text string) UIOption {
	return func(m *uiModelConstruction) {
		if text == "" || m.mainEditor.Text() != "" {
			return
		}
		m.replaceMainInputAtEnd(text)
	}
}

func WithUIInitialRecoveryBuffers(buffers []serverapi.SessionDraftRecoveryBuffer) UIOption {
	return func(m *uiModelConstruction) {
		m.restoreSessionDraftRecoveryBuffers(buffers)
	}
}

func WithUISessionName(name string) UIOption {
	return func(m *uiModelConstruction) {
		m.sessionName = strings.TrimSpace(name)
	}
}

func WithUISessionID(sessionID string) UIOption {
	return func(m *uiModelConstruction) {
		m.sessionID = strings.TrimSpace(sessionID)
	}
}

func WithUIProcessClient(client clientui.ProcessClient) UIOption {
	return func(m *uiModelConstruction) {
		m.processClient = client
		m.processClientExplicit = true
	}
}

func WithUIWorktreeClient(client apicontract.WorktreeService) UIOption {
	return func(m *uiModelConstruction) {
		m.worktreeClient = client
	}
}

func WithUITurnQueueHook(hook turnQueueHook) UIOption {
	return func(m *uiModelConstruction) {
		m.turnQueueHook = hook
	}
}

func WithUITerminalFocusState(state *terminalFocusState) UIOption {
	return func(m *uiModelConstruction) {
		if state != nil {
			m.terminalFocus = state
		}
	}
}

func WithUIPromptHistory(history []string) UIOption {
	return func(m *uiModelConstruction) {
		m.appendInitialPromptHistory(history)
	}
}

func WithUIClipboardPaster(paster uiClipboardPaster) UIOption {
	return func(m *uiModelConstruction) {
		m.clipboardPaster = paster
	}
}

func WithUIClipboardTextCopier(copier uiClipboardTextCopier) UIOption {
	return func(m *uiModelConstruction) {
		m.clipboardTextCopier = copier
	}
}
