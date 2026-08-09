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
		want bool
	}{
		{
			name: "canonical persistent task id",
			raw:  "task-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			want: true,
		},
		{
			name: "ordinary short id",
			raw:  "KENT-432",
			want: false,
		},
		{
			name: "prefix-like noncanonical id",
			raw:  "task-not-a-uuid",
			want: false,
		},
		{
			name: "prefix-like legacy id",
			raw:  "task-1",
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isCanonicalWorkflowTaskID(test.raw); got != test.want {
				t.Fatalf("canonical selector = %v, want %v", got, test.want)
			}
		})
	}
}

func TestWorkflowTaskNotFoundErrorPreservesMessageAndTypedIdentity(t *testing.T) {
	err := workflowTaskNotFoundError{errors.New("not found")}
	if !errors.Is(err, serverapi.ErrWorkflowTaskNotFound) {
		t.Fatalf("error is not classified as workflow-task-not-found: %v", err)
	}
}
