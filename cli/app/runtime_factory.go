package app

import (
	"core/shared/apicontract"
	"core/shared/clientui"
)

type runtimeWiring struct {
	turnQueueHook         turnQueueHook
	terminalFocus         *terminalFocusState
	runtimeEvents         <-chan clientui.Event
	transcriptEvents      <-chan ongoingTranscriptEvent
	requestTranscriptOpen func()
	askEvents             <-chan askEvent
	runtimeClient         clientui.RuntimeClient
	promptControl         apicontract.PromptControlService
	runtimeControls       apicontract.RuntimeControlService
	worktrees             apicontract.WorktreeService
	processControls       apicontract.ProcessControlService
	processOutput         apicontract.ProcessOutputService
	processViews          apicontract.ProcessViewService
	approvalViews         apicontract.ApprovalViewService
	askViews              apicontract.AskViewService
	sessionActivity       apicontract.SessionActivityService
	sessionTranscript     apicontract.SessionTranscriptService
	sessionViews          apicontract.SessionViewService
	promptHistory         []string
}
