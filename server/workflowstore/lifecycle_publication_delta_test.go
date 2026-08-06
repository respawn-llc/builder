package workflowstore

import (
	"testing"

	"core/server/workflow"
)

func TestQueuedTaskLifecycleDeltaOwnsImmutableCurrentNodeCopy(t *testing.T) {
	reference, err := workflow.NewCurrentNodeReference(
		workflow.TaskID("task-delta-copy"),
		workflow.NodeID("node-agent"),
		nil,
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	source := []workflow.CurrentNodeReference{reference}
	delta, err := NewQueuedTaskLifecycleDelta(reference.TaskID, source)
	if err != nil {
		t.Fatalf("NewQueuedTaskLifecycleDelta: %v", err)
	}

	source[0].NodeID = workflow.NodeID("node-mutated-source")
	first := delta.QueuedCurrentNodes()
	if len(first) != 1 || !first[0].Equal(reference) {
		t.Fatalf("delta after source mutation = %+v, want %v", first, reference)
	}
	first[0].NodeID = workflow.NodeID("node-mutated-result")
	second := delta.QueuedCurrentNodes()
	if len(second) != 1 || !second[0].Equal(reference) {
		t.Fatalf("delta after result mutation = %+v, want %v", second, reference)
	}
}
