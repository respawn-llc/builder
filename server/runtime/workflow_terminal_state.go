package runtime

import (
	"strings"
	"time"
)

type WorkflowCompletionSource string

const (
	WorkflowCompletionSourceTool             WorkflowCompletionSource = "tool"
	WorkflowCompletionSourceStructuredOutput WorkflowCompletionSource = "structured_output"
	WorkflowCompletionSourceUnstructured     WorkflowCompletionSource = "unstructured_output"
	WorkflowCompletionSourceObserved         WorkflowCompletionSource = "observed"
)

type WorkflowTerminalState struct {
	Completed   bool
	RunID       string
	Generation  int64
	Source      WorkflowCompletionSource
	CompletedAt time.Time
}

type WorkflowSessionState struct {
	RunID      string
	TaskID     string
	WorkflowID string
}

func (e *Engine) WorkflowSessionState() WorkflowSessionState {
	if e == nil {
		return WorkflowSessionState{}
	}
	if e.workflowRunActive() {
		return WorkflowSessionState{
			RunID:      strings.TrimSpace(string(e.cfg.WorkflowRun.Contract.RunID)),
			TaskID:     strings.TrimSpace(e.cfg.WorkflowRun.Instructions.TaskID),
			WorkflowID: strings.TrimSpace(e.cfg.WorkflowRun.Instructions.WorkflowID),
		}
	}
	if e.store == nil {
		return WorkflowSessionState{}
	}
	meta := e.store.Meta()
	workflowSession := meta.WorkflowSession
	if workflowSession == nil {
		return WorkflowSessionState{}
	}
	return WorkflowSessionState{
		RunID:      strings.TrimSpace(workflowSession.RunID),
		TaskID:     strings.TrimSpace(workflowSession.TaskID),
		WorkflowID: strings.TrimSpace(workflowSession.WorkflowID),
	}
}

func (e *Engine) WorkflowTerminalState() WorkflowTerminalState {
	if e == nil {
		return WorkflowTerminalState{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.workflowTerminal
}

// failQueuedUserWorkIfTerminal abandons queued user steering once the run has
// terminally completed, reporting whether it did so. It is the single place that
// ties workflow completion to queued-user-work failure, so scheduling and
// submission code can gate on terminal completion without inspecting workflow
// state directly.
func (e *Engine) failQueuedUserWorkIfTerminal() bool {
	if e == nil || !e.WorkflowTerminalState().Completed {
		return false
	}
	e.FailQueuedUserMessages(QueuedUserMessageFailureTerminalWorkflowCompletion)
	return true
}

func (e *Engine) setWorkflowTerminalState(source WorkflowCompletionSource) {
	if e == nil || !e.workflowRunActive() {
		return
	}
	transitioned := e.recordWorkflowTerminalState(source)
	if transitioned {
		e.cascadeCompleteActiveGoalOnWorkflowCompletion()
	}
}

func (e *Engine) recordWorkflowTerminalState(source WorkflowCompletionSource) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.workflowTerminal.Completed {
		return false
	}
	e.workflowTerminal = WorkflowTerminalState{
		Completed:   true,
		RunID:       strings.TrimSpace(string(e.cfg.WorkflowRun.Contract.RunID)),
		Generation:  e.cfg.WorkflowRun.Contract.ExpectedGeneration,
		Source:      source,
		CompletedAt: time.Now(),
	}
	return true
}
