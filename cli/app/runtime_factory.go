package app

import (
	"core/shared/apicontract"
	"core/shared/clientui"
)

type runtimeWiring struct {
	turnQueueHook          *bellHooks
	terminalFocus          *terminalFocusState
	transcriptEvents       <-chan ongoingTranscriptEvent
	requestTranscriptOpen  func()
	attentionEvents        <-chan attentionStreamOutcome
	requestAttentionReopen func()
	promptAnswers          *transcriptPromptAnswerer
	promptAttention        *bellHooks
	runtimeClient          clientui.RuntimeClient
	worktrees              apicontract.WorktreeService
	processControls        apicontract.ProcessControlService
	processOutput          apicontract.ProcessOutputService
	processViews           apicontract.ProcessViewService
	promptHistory          []string
}
