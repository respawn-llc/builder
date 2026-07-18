package main

import (
	"core/shared/runtimeids"

	"github.com/google/uuid"
)

const persistedWorkflowIDPrefix = "workflow-"

type workflowSelector struct {
	value uuid.UUID
}

func parseWorkflowSelector(raw string) (workflowSelector, error) {
	value, err := runtimeids.ParseCanonicalUUIDv4(raw, "workflow selector")
	if err != nil {
		return workflowSelector{}, err
	}
	return workflowSelector{value: value}, nil
}

func workflowSelectorFromPersistedID(raw string) (workflowSelector, error) {
	value, err := runtimeids.ParseCanonicalPrefixedUUIDv4(raw, persistedWorkflowIDPrefix, "workflow id")
	if err != nil {
		return workflowSelector{}, err
	}
	return workflowSelector{value: value}, nil
}

func (s workflowSelector) String() string {
	return s.value.String()
}

func (s workflowSelector) PersistedID() string {
	return persistedWorkflowIDPrefix + s.String()
}
