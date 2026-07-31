package main

import (
	"errors"

	"core/shared/runtimeids"
)

func parseWorkflowSelector(raw string) (runtimeids.WorkflowID, error) {
	value, err := runtimeids.ParseWorkflowID(raw)
	if err != nil {
		return runtimeids.WorkflowID{}, errors.New("invalid workflow ID")
	}
	return value, nil
}
