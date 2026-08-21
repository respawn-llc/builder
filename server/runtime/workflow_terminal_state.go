package runtime

import (
	"fmt"
	"strings"
	"time"

	"core/server/llm"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
)

type WorkflowCompletionSource string

const (
	WorkflowCompletionSourceTool             WorkflowCompletionSource = "tool"
	WorkflowCompletionSourceStructuredOutput WorkflowCompletionSource = "structured_output"
	WorkflowCompletionSourceShellCommand     WorkflowCompletionSource = "shell_command"
	WorkflowCompletionSourceUnstructured     WorkflowCompletionSource = "unstructured_output"
)

type WorkflowTerminalState struct {
	Completed   bool
	Generation  int64
	Source      WorkflowCompletionSource
	Completion  workflowruntime.CompletionResult
	CompletedAt time.Time
}

type WorkflowTurnResult struct {
	Assistant  llm.Message
	Completion *workflowruntime.CompletionResult
}

func workflowCompletionSource(mode workflowruntime.CompletionMode) WorkflowCompletionSource {
	switch mode {
	case workflowruntime.CompletionModeTool:
		return WorkflowCompletionSourceTool
	case workflowruntime.CompletionModeStructuredOutput:
		return WorkflowCompletionSourceStructuredOutput
	case workflowruntime.CompletionModeShellCommand:
		return WorkflowCompletionSourceShellCommand
	case workflowruntime.CompletionModeUnstructuredOutput:
		return WorkflowCompletionSourceUnstructured
	default:
		return ""
	}
}

type WorkflowSessionState struct {
	TaskID     workflow.TaskID
	WorkflowID runtimeids.WorkflowID
}

func (e *Engine) WorkflowSessionState() (*WorkflowSessionState, error) {
	if e == nil {
		return nil, nil
	}
	if execution, active := e.currentNodeExecutionConfig(); active {
		instructions := execution.Instructions
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

func (e *Engine) setWorkflowTerminalState(source WorkflowCompletionSource, completion workflowruntime.CompletionResult) {
	if e == nil || !e.currentNodeExecutionActive() {
		return
	}
	transitioned := e.recordWorkflowTerminalState(source, completion)
	if transitioned {
		e.cascadeCompleteActiveGoalOnWorkflowCompletion()
	}
}

func (e *Engine) recordWorkflowTerminalState(source WorkflowCompletionSource, completion workflowruntime.CompletionResult) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.workflowTerminal.Completed {
		return false
	}
	e.workflowTerminal = WorkflowTerminalState{
		Completed:   true,
		Generation:  e.workflowTerminal.Generation + 1,
		Source:      source,
		Completion:  completion,
		CompletedAt: time.Now(),
	}
	return true
}
