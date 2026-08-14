package workflowcontract

import (
	"errors"
	"strings"
	"testing"
)

func TestNewWorkflowNameOwnsCanonicalGrammar(t *testing.T) {
	name, err := NewWorkflowName("  Release workflow  ")
	if err != nil {
		t.Fatalf("NewWorkflowName: %v", err)
	}
	if got := name.String(); got != "Release workflow" {
		t.Fatalf("WorkflowName.String() = %q, want %q", got, "Release workflow")
	}

	if _, err := NewWorkflowName(" \t "); !errors.Is(err, ErrWorkflowNameRequired) {
		t.Fatalf("blank name error = %v, want ErrWorkflowNameRequired", err)
	}
	if _, err := NewWorkflowName(strings.Repeat("x", MaxDisplayNameChars+1)); !errors.Is(err, ErrWorkflowNameTooLong) {
		t.Fatalf("long name error = %v, want ErrWorkflowNameTooLong", err)
	}
}
