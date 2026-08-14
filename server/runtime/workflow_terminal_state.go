package runtime

import (
	"fmt"
	"strings"
	"time"

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
	CompletedAt time.Time
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

func (e *Engine) recordWorkflowTerminalState(source WorkflowCompletionSource) (bool, error) {
	switch source {
	case WorkflowCompletionSourceTool,
		WorkflowCompletionSourceStructuredOutput,
		WorkflowCompletionSourceShellCommand,
		WorkflowCompletionSourceUnstructured:
	default:
		return false, e.runtimeInvariant(
			"record Workflow terminal state",
			fmt.Errorf("unsupported Workflow completion source %q", source),
		)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.workflowTerminal.Completed {
		return false, nil
	}
	e.workflowTerminal = WorkflowTerminalState{
		Completed:   true,
		Source:      source,
		CompletedAt: time.Now(),
	}
	return true, nil
}
