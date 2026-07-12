package app

import (
	"os"
	"strings"

	"core/cli/app/commands"
	"core/cli/app/internal/status"
	"core/cli/tui"
	"core/shared/clientui"
	"core/shared/theme"
)

func newUIModelDefaults(runtimeClient clientui.RuntimeClient, runtimeEvents <-chan clientui.Event, askEvents <-chan askEvent) *uiModel {
	return &uiModel{
		uiRuntimeFeatureState:           newUIRuntimeFeatureState(runtimeClient, runtimeEvents, askEvents),
		uiInputFeatureState:             newUIInputFeatureState(),
		uiPresentationFeatureState:      newUIPresentationFeatureState(),
		uiConversationFeatureState:      newUIConversationFeatureState(),
		uiSessionTransitionFeatureState: newUISessionTransitionFeatureState(),
		uiStatusFeatureState:            newUIStatusFeatureState(),
		uiRollbackFeatureState:          newUIRollbackFeatureState(),
	}
}

func newUIRuntimeFeatureState(runtimeClient clientui.RuntimeClient, runtimeEvents <-chan clientui.Event, askEvents <-chan askEvent) uiRuntimeFeatureState {
	return uiRuntimeFeatureState{
		engine:        runtimeClient,
		view:          tui.NewModel(),
		runtimeEvents: runtimeEvents,
		askEvents:     askEvents,
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
		statusRepository:      status.NewMemoryRepository(),
		clipboardPaster:       newSystemClipboardPaster(),
		clipboardTextCopier:   newSystemClipboardTextCopier(),
		debugKeys:             envFlagEnabled("KENT_DEBUG_KEYS"),
		debugMode:             debug,
		transcriptDiagnostics: envFlagEnabled("KENT_TRANSCRIPT_DIAGNOSTICS"),
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
