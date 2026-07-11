package app

import (
	"core/shared/client"
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
	promptControl         client.PromptControlClient
	runtimeControls       client.RuntimeControlClient
	worktrees             client.WorktreeClient
	processControls       client.ProcessControlClient
	processOutput         client.ProcessOutputClient
	processViews          client.ProcessViewClient
	approvalViews         client.ApprovalViewClient
	askViews              client.AskViewClient
	sessionActivity       client.SessionActivityClient
	sessionTranscript     client.SessionTranscriptClient
	sessionViews          client.SessionViewClient
	hasOtherSessions      bool
	hasOtherSessionsKnown bool
	promptHistory         []string
}
