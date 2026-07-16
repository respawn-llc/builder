package app

import (
	"os"
	"strings"

	"core/cli/app/commands"
	"core/cli/app/internal/status"
	"core/cli/tui"
	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"core/shared/theme"
)

func newUIModelDefaults(runtimeClient clientui.RuntimeClient) *uiModel {
	return &uiModel{
		eventDispatcher:                 newUIEventDispatcher(nil),
		uiRuntimeFeatureState:           newUIRuntimeFeatureState(runtimeClient),
		uiInputFeatureState:             newUIInputFeatureState(),
		uiPresentationFeatureState:      newUIPresentationFeatureState(),
		uiConversationFeatureState:      newUIConversationFeatureState(),
		uiSessionTransitionFeatureState: newUISessionTransitionFeatureState(),
		uiStatusFeatureState:            newUIStatusFeatureState(),
		uiRollbackFeatureState:          newUIRollbackFeatureState(),
	}
}

func newUIRuntimeFeatureState(runtimeClient clientui.RuntimeClient) uiRuntimeFeatureState {
	return uiRuntimeFeatureState{
		engine: runtimeClient,
		view:   tui.NewModel(),
	}
}

func newUIInputFeatureState() uiInputFeatureState {
	return uiInputFeatureState{
		activity:                 uiActivityIdle,
		inputCursor:              -1,
		mainInputDraftToken:      1,
		promptHistorySelection:   -1,
		promptHistoryDraftCursor: -1,
		commandRegistry:          commands.NewDefaultRegistry(),
		reviewerMode:             "off",
		autoCompactionEnabled:    true,
		questionsEnabled:         true,
		conversationFreshness:    clientui.ConversationFreshnessFresh,
	}
}

func newUIPresentationFeatureState() uiPresentationFeatureState {
	return uiPresentationFeatureState{
		theme:         theme.Auto,
		markdownLinks: transcriptrender.MarkdownLinkLabelOnly,
		terminalFocus: newTerminalFocusState(),
	}
}

func newUIConversationFeatureState() uiConversationFeatureState {
	return uiConversationFeatureState{
		interaction: uiInteractionState{Mode: uiInputModeMain},
		ask:         uiAskState{inputCursor: -1},
	}
}

func newUISessionTransitionFeatureState() uiSessionTransitionFeatureState {
	return uiSessionTransitionFeatureState{
		exitAction: UIActionNone,
	}
}

func newUIStatusFeatureState() uiStatusFeatureState {
	debug := envFlagEnabled("KENT_DEBUG")
	return uiStatusFeatureState{
		statusRepository:    status.NewMemoryRepository(),
		clipboardPaster:     newSystemClipboardPaster(),
		clipboardTextCopier: newSystemClipboardTextCopier(),
		debugKeys:           envFlagEnabled("KENT_DEBUG_KEYS"),
		debugMode:           debug,
	}
}

func newUIRollbackFeatureState() uiRollbackFeatureState {
	return uiRollbackFeatureState{
		rollback: uiRollbackState{phase: uiRollbackPhaseInactive},
	}
}

func envFlagEnabled(name string) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return false
	}
	switch strings.ToLower(value) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}
