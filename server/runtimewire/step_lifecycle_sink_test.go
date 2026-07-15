package runtimewire

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/runtimefeed"
	"core/shared/clientui"
	"core/shared/invariant"
)

const (
	stepLifecycleTestRunID  = "11111111-1111-4111-8111-111111111111"
	stepLifecycleTestStepID = "22222222-2222-4222-8222-222222222222"
)

type recordingRuntimeReadModelPublisher struct {
	snapshots     []runtimefeed.RuntimeReadModelUpdate
	panicNext     bool
	panicMessage  string
	panicConsumed bool
}

func (p *recordingRuntimeReadModelPublisher) PublishRuntimeReadModelUpdate(_ string, snapshot runtimefeed.RuntimeReadModelUpdate) {
	if p.panicNext && !p.panicConsumed {
		p.panicConsumed = true
		panic(p.panicMessage)
	}
	p.snapshots = append(p.snapshots, snapshot)
}

type registrySnapshotRuntimeReadModelPublisher struct {
	recordingRuntimeReadModelPublisher
	registry runtimeactivity.RegistrySnapshot
}

func (p *registrySnapshotRuntimeReadModelPublisher) RuntimeActivityRegistrySnapshot(string) runtimeactivity.RegistrySnapshot {
	return p.registry
}

func TestStepLifecycleSinkPublishesVersionedRunningThenIdleActivity(t *testing.T) {
	publisher := &recordingRuntimeReadModelPublisher{}
	sink := NewStepLifecycleSink("session-1", publisher)
	if sink == nil {
		t.Fatal("expected step lifecycle sink")
	}
	startedAt := time.Now().UTC()
	if err := sink.StepBegan(context.Background(), runtime.StepLifecycleSnapshot{
		SessionID:  "session-1",
		RunID:      stepLifecycleTestRunID,
		StepID:     stepLifecycleTestStepID,
		ActiveKind: runtime.ActiveKindGoalLoop,
		StartedAt:  startedAt,
	}); err != nil {
		t.Fatalf("StepBegan: %v", err)
	}
	if err := sink.StepEnded(context.Background(), runtime.StepLifecycleSnapshot{
		SessionID:  "session-1",
		RunID:      stepLifecycleTestRunID,
		StepID:     stepLifecycleTestStepID,
		ActiveKind: runtime.ActiveKindGoalLoop,
		StartedAt:  startedAt,
		FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("StepEnded: %v", err)
	}
	if len(publisher.snapshots) != 2 {
		t.Fatalf("snapshot count = %d, want 2", len(publisher.snapshots))
	}
	if publisher.snapshots[0].Activity.State != clientui.RuntimeActivityRunning ||
		publisher.snapshots[0].Activity.ActiveStep == nil ||
		publisher.snapshots[0].Activity.ActiveStep.ActiveKind != clientui.RuntimeActivityActiveKindGoalLoop {
		t.Fatalf("began snapshot = %+v, want running goal_loop", publisher.snapshots[0].Activity)
	}
	if publisher.snapshots[1].Activity.State != clientui.RuntimeActivityRegisteredIdle {
		t.Fatalf("ended snapshot = %+v, want registered idle", publisher.snapshots[1].Activity)
	}
	if !publisher.snapshots[1].Version.NewerThan(publisher.snapshots[0].Version) {
		t.Fatalf("ended version must be newer than began: began=%+v ended=%+v", publisher.snapshots[0].Version, publisher.snapshots[1].Version)
	}
}

func TestStepLifecycleSinkUsesPublisherRegistrySnapshotForTerminalActivity(t *testing.T) {
	publisher := &registrySnapshotRuntimeReadModelPublisher{registry: runtimeactivity.RegistrySnapshot{Registered: true, Draining: true}}
	sink := NewStepLifecycleSink("session-draining", publisher)

	if err := sink.StepEnded(context.Background(), runtime.StepLifecycleSnapshot{
		SessionID:  "session-draining",
		RunID:      stepLifecycleTestRunID,
		StepID:     stepLifecycleTestStepID,
		ActiveKind: runtime.ActiveKindUserTurn,
	}); err != nil {
		t.Fatalf("StepEnded: %v", err)
	}
	if len(publisher.snapshots) != 1 {
		t.Fatalf("snapshot count = %d, want 1", len(publisher.snapshots))
	}
	if publisher.snapshots[0].Activity.State != clientui.RuntimeActivityDraining || publisher.snapshots[0].Activity.QueueAccepting {
		t.Fatalf("terminal activity = %+v, want draining and not queue accepting", publisher.snapshots[0].Activity)
	}
}

func TestStepLifecycleSinkPublishesSafeRecoveryActivityOnTerminalPublicationInvariantFailure(t *testing.T) {
	publisher := &registrySnapshotRuntimeReadModelPublisher{
		recordingRuntimeReadModelPublisher: recordingRuntimeReadModelPublisher{panicNext: true, panicMessage: "broken publication"},
		registry:                           runtimeactivity.RegistrySnapshot{Registered: true, QueueAccepting: true},
	}
	var diagnostics []invariant.Diagnostic
	policy := invariant.NewPolicy(
		invariant.WithMode(invariant.ModeDiagnostic),
		invariant.WithSink(invariant.SinkFunc(func(d invariant.Diagnostic) {
			diagnostics = append(diagnostics, d)
		})),
	)
	sink := NewStepLifecycleSinkWithInvariantPolicy("session-recovery", publisher, policy)

	err := sink.StepEnded(context.Background(), runtime.StepLifecycleSnapshot{
		SessionID:  "session-recovery",
		RunID:      stepLifecycleTestRunID,
		StepID:     stepLifecycleTestStepID,
		ActiveKind: runtime.ActiveKindGoalLoop,
		FinishedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("StepEnded diagnostic recovery: %v", err)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %d, want 1", len(diagnostics))
	}
	if diagnostics[0].Scope != invariant.ScopeReadModelPublication ||
		diagnostics[0].Fields[invariant.FieldPublicationCause] != "step_ended" ||
		diagnostics[0].Fields[invariant.FieldProviderError] == "" {
		t.Fatalf("diagnostic = %+v, want read-model publication failure fields", diagnostics[0])
	}
	if len(publisher.snapshots) != 1 {
		t.Fatalf("published snapshots = %d, want recovery snapshot only", len(publisher.snapshots))
	}
	recovery := publisher.snapshots[0]
	if recovery.Activity.State != clientui.RuntimeActivityRegisteredIdle ||
		!recovery.Activity.DiagnosticRecovery ||
		!recovery.Activity.QueueAccepting {
		t.Fatalf("recovery activity = %+v, want diagnostic registered idle", recovery.Activity)
	}
	if err := recovery.Validate(); err != nil {
		t.Fatalf("recovery read model invalid: %v", err)
	}
}

func TestStepLifecycleSinkPanicsOnPublicationInvariantFailureInPanicMode(t *testing.T) {
	publisher := &recordingRuntimeReadModelPublisher{panicNext: true, panicMessage: "broken publication"}
	sink := NewStepLifecycleSinkWithInvariantPolicy("session-panic", publisher, invariant.NewPolicy(invariant.WithMode(invariant.ModePanic)))

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("StepEnded did not panic in panic invariant mode")
		}
		if len(publisher.snapshots) != 0 {
			t.Fatalf("published snapshots after panic = %+v, want none", publisher.snapshots)
		}
	}()
	err := sink.StepEnded(context.Background(), runtime.StepLifecycleSnapshot{
		SessionID:  "session-panic",
		RunID:      stepLifecycleTestRunID,
		StepID:     stepLifecycleTestStepID,
		ActiveKind: runtime.ActiveKindGoalLoop,
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected returned error before panic: %v", err)
	}
}
