package sessioncontract

import (
	"errors"
	"fmt"
	"strings"
)

type WorkflowCompletionMode string

const (
	WorkflowCompletionModeStructuredOutput   WorkflowCompletionMode = "structured_output"
	WorkflowCompletionModeTool               WorkflowCompletionMode = "tool"
	WorkflowCompletionModeShellCommand       WorkflowCompletionMode = "shell_command"
	WorkflowCompletionModeUnstructuredOutput WorkflowCompletionMode = "unstructured_output"
)

func ParseWorkflowCompletionMode(raw string) (WorkflowCompletionMode, error) {
	mode := WorkflowCompletionMode(strings.TrimSpace(raw))
	switch mode {
	case WorkflowCompletionModeStructuredOutput,
		WorkflowCompletionModeTool,
		WorkflowCompletionModeShellCommand,
		WorkflowCompletionModeUnstructuredOutput:
		return mode, nil
	case "":
		return "", errors.New("workflow effective completion mode is required")
	default:
		return "", fmt.Errorf("invalid workflow effective completion mode %q", raw)
	}
}
