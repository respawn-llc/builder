package workflowscript

import (
	"encoding/json"
	"reflect"
	"testing"

	"core/server/workflow"
)

func TestCurrentNodeIdentityUsesTaskNodeAndOptionalBranch(t *testing.T) {
	branch := workflow.TransitionBranchKey("release-notes")
	reference, err := workflow.NewCurrentNodeReference("task-1", "node-1", &branch)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	identity, err := IdentityForCurrentNode(reference)
	if err != nil {
		t.Fatalf("IdentityForCurrentNode: %v", err)
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded CurrentNodeIdentity
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, identity) {
		t.Fatalf("decoded identity = %#v, want %#v", decoded, identity)
	}
}
