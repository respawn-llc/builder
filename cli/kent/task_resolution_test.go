package main

import (
	"errors"
	"testing"

	"core/shared/serverapi"
)

func TestClassifyWorkflowTaskSelectorUsesFullCanonicalTaskID(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want workflowTaskSelectorKind
	}{
		{
			name: "canonical persistent task id",
			raw:  "task-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			want: workflowTaskSelectorPersistentID,
		},
		{
			name: "ordinary short id",
			raw:  "KENT-432",
			want: workflowTaskSelectorShortID,
		},
		{
			name: "prefix-like noncanonical id",
			raw:  "task-not-a-uuid",
			want: workflowTaskSelectorShortID,
		},
		{
			name: "prefix-like legacy id",
			raw:  "task-1",
			want: workflowTaskSelectorShortID,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classifyWorkflowTaskSelector(test.raw)
			if got.kind != test.want {
				t.Fatalf("selector kind = %v, want %v", got.kind, test.want)
			}
		})
	}
}

func TestWorkflowTaskNotFoundErrorPreservesMessageAndTypedIdentity(t *testing.T) {
	err := workflowTaskNotFoundError{message: `task "T-1" not found in project project`}
	if err.Error() != `task "T-1" not found in project project` || !errors.Is(err, serverapi.ErrWorkflowTaskNotFound) {
		t.Fatalf("error=%q, is_not_found=%v", err.Error(), errors.Is(err, serverapi.ErrWorkflowTaskNotFound))
	}
}
