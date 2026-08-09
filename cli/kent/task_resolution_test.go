package main

import (
	"errors"
	"testing"

	"core/shared/serverapi"
)

func TestClassifyWorkflowTaskSelectorPreservesPersistentTaskIDs(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		kind  workflowTaskSelectorKind
		value string
	}{
		{
			name:  "canonical persistent task id",
			raw:   "task-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			kind:  workflowTaskSelectorTaskID,
			value: "task-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		},
		{
			name:  "ordinary short id",
			raw:   "KENT-432",
			kind:  workflowTaskSelectorShortID,
			value: "KENT-432",
		},
		{
			name:  "prefix-like noncanonical id",
			raw:   "task-not-a-uuid",
			kind:  workflowTaskSelectorTaskID,
			value: "task-not-a-uuid",
		},
		{
			name:  "prefix-like legacy id",
			raw:   "task-1",
			kind:  workflowTaskSelectorTaskID,
			value: "task-1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := classifyWorkflowTaskSelector(test.raw)
			if err != nil {
				t.Fatalf("classify selector: %v", err)
			}
			if got.kind != test.kind || got.value != test.value {
				t.Fatalf("selector = %+v, want kind=%d value=%q", got, test.kind, test.value)
			}
		})
	}
}

func TestClassifyWorkflowTaskSelectorRejectsEmptyInput(t *testing.T) {
	if _, err := classifyWorkflowTaskSelector(" \t"); err == nil {
		t.Fatal("empty task selector unexpectedly accepted")
	}
}

func TestWorkflowTaskNotFoundErrorPreservesMessageAndTypedIdentity(t *testing.T) {
	err := workflowTaskNotFoundError{errors.New("not found")}
	if !errors.Is(err, serverapi.ErrWorkflowTaskNotFound) {
		t.Fatalf("error is not classified as workflow-task-not-found: %v", err)
	}
}
