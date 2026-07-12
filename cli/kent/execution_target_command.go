package main

import (
	"errors"
	"strings"

	"core/shared/serverapi"
)

const executionTargetSelectorHelp = "none, head, default-branch, or ref:<revision>"

func parseTaskExecutionTargetSelector(raw string) (serverapi.WorkflowExecutionTargetSelection, error) {
	trimmed := strings.TrimSpace(raw)
	switch trimmed {
	case "none":
		return serverapi.WorkflowExecutionTargetSelection{Mode: serverapi.WorkflowExecutionTargetModeNone}, nil
	case "head":
		return serverapi.WorkflowExecutionTargetSelection{Mode: serverapi.WorkflowExecutionTargetModeHead}, nil
	case "default-branch":
		return serverapi.WorkflowExecutionTargetSelection{Mode: serverapi.WorkflowExecutionTargetModeDefaultBranch}, nil
	}
	if revision, ok := strings.CutPrefix(trimmed, "ref:"); ok {
		revision = strings.TrimSpace(revision)
		if revision == "" {
			return serverapi.WorkflowExecutionTargetSelection{}, errors.New("execution target ref:<revision> requires a non-blank revision")
		}
		return serverapi.WorkflowExecutionTargetSelection{
			Mode:      serverapi.WorkflowExecutionTargetModeCustomRef,
			CustomRef: &revision,
		}, nil
	}
	return serverapi.WorkflowExecutionTargetSelection{}, errors.New("execution target must be " + executionTargetSelectorHelp)
}

func parseWorkflowExecutionTargetPolicySelector(raw string) (serverapi.WorkflowExecutionTargetConfiguration, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "ask-on-first-execution" {
		return serverapi.WorkflowExecutionTargetConfiguration{Mode: serverapi.WorkflowExecutionTargetModeAskOnFirstExecution}, nil
	}
	selection, err := parseTaskExecutionTargetSelector(trimmed)
	if err != nil {
		return serverapi.WorkflowExecutionTargetConfiguration{}, errors.New("workflow execution target must be ask-on-first-execution, " + executionTargetSelectorHelp)
	}
	return serverapi.WorkflowExecutionTargetConfiguration{
		Mode:      selection.Mode,
		CustomRef: selection.CustomRef,
	}, nil
}

func workflowExecutionTargetPolicySelector(policy serverapi.WorkflowExecutionTargetConfiguration) string {
	switch policy.Mode {
	case serverapi.WorkflowExecutionTargetModeAskOnFirstExecution:
		return "ask-on-first-execution"
	case serverapi.WorkflowExecutionTargetModeNone:
		return "none"
	case serverapi.WorkflowExecutionTargetModeHead:
		return "head"
	case serverapi.WorkflowExecutionTargetModeDefaultBranch:
		return "default-branch"
	case serverapi.WorkflowExecutionTargetModeCustomRef:
		if policy.CustomRef != nil {
			return "ref:" + *policy.CustomRef
		}
		return "ref:"
	default:
		return string(policy.Mode)
	}
}
