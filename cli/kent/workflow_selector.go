package main

import (
	"errors"

	"core/shared/runtimeids"
)

type workflowSelector struct {
	value runtimeids.WorkflowID
}

func parseWorkflowSelector(raw string) (workflowSelector, error) {
	value, err := runtimeids.ParseWorkflowID(raw)
	if err != nil {
		return workflowSelector{}, errors.New("invalid workflow ID")
	}
	return workflowSelector{value: value}, nil
}

func (s workflowSelector) String() string {
	return s.value.String()
}
