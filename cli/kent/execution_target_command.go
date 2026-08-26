package main

import (
	"errors"
	"strings"

	"core/shared/serverapi"
	"core/shared/workflowcontract"
)

const executionTargetSelectorHelp = "none, head, default-branch, or ref:<revision>"

func parseTaskExecutionTargetSelector(raw string) (workflowcontract.ExecutionTargetSelection, error) {
	trimmed := strings.TrimSpace(raw)
	switch trimmed {
	case "none":
		return workflowcontract.ExecutionTargetSelection{Mode: workflowcontract.ExecutionTargetModeNone}, nil
	case "head":
		return workflowcontract.ExecutionTargetSelection{Mode: workflowcontract.ExecutionTargetModeHead}, nil
	case "default-branch":
		return workflowcontract.ExecutionTargetSelection{Mode: workflowcontract.ExecutionTargetModeDefaultBranch}, nil
	}
	if revision, ok := strings.CutPrefix(trimmed, "ref:"); ok {
		revision = strings.TrimSpace(revision)
		if revision == "" {
			return workflowcontract.ExecutionTargetSelection{}, errors.New("execution target ref:<revision> requires a non-blank revision")
		}
		return workflowcontract.ExecutionTargetSelection{
			Mode:      workflowcontract.ExecutionTargetModeCustomRef,
			CustomRef: &revision,
		}, nil
	}
	return workflowcontract.ExecutionTargetSelection{}, errors.New("execution target must be " + executionTargetSelectorHelp)
}

func parseOptionalTaskExecutionTarget(raw string, provided bool) (*workflowcontract.ExecutionTargetSelection, error) {
	if !provided {
		return nil, nil
	}
	selection, err := parseTaskExecutionTargetSelector(raw)
	if err != nil {
		return nil, err
	}
	return &selection, nil
}

func parseWorkflowExecutionTargetPolicySelector(raw string) (serverapi.WorkflowExecutionTargetConfiguration, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "ask-on-first-execution" {
		return serverapi.WorkflowExecutionTargetConfiguration{Mode: workflowcontract.ExecutionTargetModeAskOnFirstExecution}, nil
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
	case workflowcontract.ExecutionTargetModeAskOnFirstExecution:
		return "ask-on-first-execution"
	case workflowcontract.ExecutionTargetModeNone:
		return "none"
	case workflowcontract.ExecutionTargetModeHead:
		return "head"
	case workflowcontract.ExecutionTargetModeDefaultBranch:
		return "default-branch"
	case workflowcontract.ExecutionTargetModeCustomRef:
		if policy.CustomRef != nil {
			return "ref:" + *policy.CustomRef
		}
		return "ref:"
	default:
		return string(policy.Mode)
	}
}
