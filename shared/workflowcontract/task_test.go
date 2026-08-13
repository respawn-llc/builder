package workflowcontract

import "testing"

func TestNewTaskTitleOwnsCanonicalRepresentation(t *testing.T) {
	title, err := NewTaskTitle("  Ship it  ")
	if err != nil {
		t.Fatalf("NewTaskTitle: %v", err)
	}
	if title.String() != "Ship it" {
		t.Fatalf("TaskTitle.String() = %q, want %q", title.String(), "Ship it")
	}
	for _, invalid := range []string{"", " \t\n "} {
		if _, err := NewTaskTitle(invalid); err == nil {
			t.Fatalf("NewTaskTitle(%q) succeeded", invalid)
		}
	}
}
