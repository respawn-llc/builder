package app

import (
	"core/shared/apicontract"
	"core/shared/clientui"
)

type runtimeWiring struct {
	nativeTurnNotifications *nativeTurnNotificationObserver
	terminalFocus           *terminalFocusState
	eventDispatcher         *uiEventDispatcher
	lifecycleCoordinator    *clientLifecycleCoordinator
	requestTranscriptOpen   func()
	promptAnswers           *transcriptPromptAnswerer
	promptAttention         *nativeTurnNotificationObserver
	runtimeClient           clientui.RuntimeClient
	worktrees               apicontract.WorktreeService
	processControls         apicontract.ProcessControlService
	processOutput           apicontract.ProcessOutputService
	processViews            apicontract.ProcessViewService
	promptHistory           []string
}
