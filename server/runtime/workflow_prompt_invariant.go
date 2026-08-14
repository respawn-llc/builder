package runtime

import (
	"core/server/llm"
	"core/shared/toolspec"
)

func (e *Engine) assertWorkflowInstructionsPresent(stepNo int) {
	if stepNo <= 1 {
		return
	}
	if !e.isWorkflowAgent() {
		return
	}
	for _, item := range e.transcriptRuntimeState().SnapshotItems() {
		if item.Type == llm.ResponseItemTypeMessage &&
			item.MessageType != nil &&
			*item.MessageType == llm.MessageTypeWorkflowMode {
			return
		}
	}
	panic("workflow mode skipped prompt")
}

func (e *Engine) isWorkflowAgent() bool {
	if e == nil {
		return false
	}
	if e.currentNodeExecutionActive() ||
		e.cfg.CurrentNodeExecution != nil ||
		e.cfg.WorkflowPrompt != nil ||
		e.workflowPromptContract != nil ||
		e.WorkflowTerminalState().Completed {
		return true
	}
	if locked, configured := e.lockedContractState().Snapshot(); configured &&
		locked.WorkflowCompletionMode != nil {
		return true
	}
	if e.store != nil {
		meta := e.store.Meta()
		if meta.ActiveWorkflowAssignment != nil ||
			(meta.Locked != nil && meta.Locked.WorkflowCompletionMode != nil) {
			return true
		}
	}
	e.workflowAssignmentMu.Lock()
	pendingAssignment := len(e.pendingWorkflowAssignments) != 0
	e.workflowAssignmentMu.Unlock()
	if pendingAssignment {
		return true
	}
	for _, toolID := range e.cfg.EnabledTools {
		if toolID == toolspec.ToolCompleteNode {
			return true
		}
	}
	for _, item := range e.transcriptRuntimeState().SnapshotItems() {
		if item.Type != llm.ResponseItemTypeMessage || item.MessageType == nil {
			continue
		}
		if *item.MessageType == llm.MessageTypeWorkflowMode ||
			*item.MessageType == llm.MessageTypeWorkflowModeExit {
			return true
		}
	}
	return false
}
