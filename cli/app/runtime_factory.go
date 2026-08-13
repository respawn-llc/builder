package app

import (
	"core/shared/apicontract"
	"core/shared/clientui"
)

type runtimeWiring struct {
	turnQueueHook         turnQueueHook
	terminalFocus         *terminalFocusState
	eventDispatcher       *uiEventDispatcher
	requestTranscriptOpen func()
	promptAnswers         *transcriptPromptAnswerer
	promptAttention       promptAttentionSink
	runtimeClient         clientui.RuntimeClient
	worktrees             apicontract.WorktreeService
	processControls       apicontract.ProcessControlService
	processOutput         apicontract.ProcessOutputService
	processViews          apicontract.ProcessViewService
	promptHistory         []string
	lifecycleHookIssues   <-chan lifecycleHookIssue
	lifecycleHookDone     <-chan struct{}
}

func (w *runtimeWiring) bindTerminalOutput(output *uiTerminalOutput) {
	if w == nil || output == nil {
		return
	}
	if hooks, ok := w.promptAttention.(*bellHooks); ok {
		hooks.setOutput(output)
	}
}
