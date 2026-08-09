package main

import "testing"

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
