package sessionruntime

import (
	"context"
	"testing"

	"core/server/runtime"
	"core/shared/runtimeids"
)

func TestAgentResourceStepLifecycleAllowsBoundaryCompactionNesting(t *testing.T) {
	sessionID, err := runtimeids.ParseSessionID("018fdd67-89ab-4cde-8123-456789abcdef")
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	ref, err := runtimeids.NewSessionResourceRef(sessionID, 1)
	if err != nil {
		t.Fatalf("new session resource ref: %v", err)
	}
	resource := &agentResource{
		authority: &Authority{},
		ref:       ref,
		state:     AgentResourceReady,
		changed:   make(chan struct{}),
	}
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
	if err := resource.StepEnded(context.Background(), outer); err != nil {
		t.Fatalf("end outer Agent Step: %v", err)
	}
	if len(resource.steps) != 0 {
		t.Fatalf("remaining resource step stack = %+v, want empty", resource.steps)
	}
}

func TestAgentResourceStepLifecycleRejectsUnrelatedOverlap(t *testing.T) {
	sessionID, err := runtimeids.ParseSessionID("018fdd67-89ab-4cde-8123-456789abcdef")
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	ref, err := runtimeids.NewSessionResourceRef(sessionID, 1)
	if err != nil {
		t.Fatalf("new session resource ref: %v", err)
	}
	resource := &agentResource{
		authority: &Authority{},
		ref:       ref,
		state:     AgentResourceReady,
		changed:   make(chan struct{}),
	}
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
