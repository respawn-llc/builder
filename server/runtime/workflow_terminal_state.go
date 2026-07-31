package runtime

import (
	"fmt"
	"strings"
	"time"

	"core/server/workflow"
	"core/shared/runtimeids"
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
	Generation  int64
	Source      WorkflowCompletionSource
	CompletedAt time.Time
}

type WorkflowSessionState struct {
	TaskID     workflow.TaskID
	WorkflowID runtimeids.WorkflowID
}

func (e *Engine) WorkflowSessionState() (*WorkflowSessionState, error) {
	if e == nil {
		return nil, nil
	}
	if e.currentNodeExecutionActive() {
		instructions := e.cfg.CurrentNodeExecution.Instructions
		if strings.TrimSpace(string(instructions.CurrentNode.TaskID)) == "" {
			return nil, fmt.Errorf("active Workflow execution has no Task ID")
		}
		if instructions.WorkflowID.IsZero() {
			return nil, fmt.Errorf("active Workflow execution has no Workflow ID")
		}
		return &WorkflowSessionState{
			TaskID:     instructions.CurrentNode.TaskID,
			WorkflowID: instructions.WorkflowID,
		}, nil
	}
	return nil, nil
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
	if e == nil || !e.currentNodeExecutionActive() {
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
		Source:      source,
		CompletedAt: time.Now(),
	}
	return true
}
