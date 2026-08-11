package sessionruntime

import (
	"context"
	"testing"

	"core/server/runtime"
	"core/shared/runtimeids"
)

func TestAgentResourceStepLifecycleAllowsBoundaryCompactionNesting(t *testing.T) {
	resource := newAgentResourceStepLifecycleTestResource(t)
	outer := runtime.StepLifecycleSnapshot{
		StepID:     "outer-agent-step",
		ActiveKind: runtime.ActiveKindUserTurn,
		Transition: runtime.StepLifecycleTransitionBegan,
	}
	nested := runtime.StepLifecycleSnapshot{
		StepID:     "nested-compaction",
		ActiveKind: runtime.ActiveKindCompaction,
		Transition: runtime.StepLifecycleTransitionBegan,
	}

	if err := resource.StepBegan(context.Background(), outer); err != nil {
		t.Fatalf("begin outer Agent Step: %v", err)
	}
	if err := resource.StepBegan(context.Background(), nested); err != nil {
		t.Fatalf("begin boundary compaction: %v", err)
	}
	if err := resource.StepEnded(context.Background(), nested); err != nil {
		t.Fatalf("end boundary compaction: %v", err)
	}
	resource.state = AgentResourceDraining
	nested.StepID = "rejected-nested-compaction"
	if err := resource.StepBegan(context.Background(), nested); err == nil {
		t.Fatal("draining resource accepted nested compaction")
	}
	if err := resource.StepEnded(context.Background(), nested); err != nil {
		t.Fatalf("clean up rejected boundary compaction: %v", err)
	}
	if len(resource.steps) != 1 || resource.steps[0].stepID != outer.StepID {
		t.Fatalf("rejected nested cleanup changed outer step stack: %+v", resource.steps)
	}
	if err := resource.StepEnded(context.Background(), outer); err != nil {
		t.Fatalf("end outer Agent Step: %v", err)
	}
	if len(resource.steps) != 0 {
		t.Fatalf("remaining resource step stack = %+v, want empty", resource.steps)
	}
}

func TestAgentResourceStepLifecycleRejectsUnrelatedOverlap(t *testing.T) {
	resource := newAgentResourceStepLifecycleTestResource(t)
	if err := resource.StepBegan(context.Background(), runtime.StepLifecycleSnapshot{
		StepID:     "outer-agent-step",
		ActiveKind: runtime.ActiveKindUserTurn,
		Transition: runtime.StepLifecycleTransitionBegan,
	}); err != nil {
		t.Fatalf("begin outer Agent Step: %v", err)
	}

	defer func() {
		if recover() == nil {
			t.Fatal("unrelated overlapping step did not panic")
		}
	}()
	_ = resource.StepBegan(context.Background(), runtime.StepLifecycleSnapshot{
		StepID:     "overlapping-maintenance",
		ActiveKind: runtime.ActiveKindRuntimeMaintenance,
		Transition: runtime.StepLifecycleTransitionBegan,
	})
}

func newAgentResourceStepLifecycleTestResource(t *testing.T) *agentResource {
	t.Helper()
	ref, err := runtimeids.NewSessionResourceRef(runtimeids.NewSessionID(), 1)
	if err != nil {
		t.Fatalf("new session resource ref: %v", err)
	}
	return &agentResource{authority: &Authority{}, ref: ref, state: AgentResourceReady, changed: make(chan struct{})}
}
