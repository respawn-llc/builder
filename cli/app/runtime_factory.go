package app

import (
	"core/shared/client"
	"core/shared/clientui"
)

type runtimeWiring struct {
	turnQueueHook         turnQueueHook
	askNotificationHook   askNotificationHook
	terminalFocus         *terminalFocusState
	runtimeEvents         <-chan clientui.Event
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
	sessionViews          client.SessionViewClient
	hasOtherSessions      bool
	hasOtherSessionsKnown bool
	promptHistory         []string
}
