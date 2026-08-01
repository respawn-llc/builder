package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/toolspec"
)

func TestGoalMutationDrainFailureAbortsBoundaryAndCompletesPendingManualCompaction(t *testing.T) {
	t.Parallel()

	cleanupErr := errors.New("goal mutation persistence failed")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model:        "gpt-5",
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	var entry *pendingManualCompaction

	providerErr := errors.New("provider failed")
	lifecycle := &defaultExclusiveStepLifecycle{engine: engine}
	engine.stepLifecycle = lifecycle
	err := lifecycle.Run(
		context.Background(),
		exclusiveStepOptions{ActiveKind: ActiveKindUserTurn},
		func(_ context.Context, stepID string) error {
			engine.agentStepBoundary(stepID).MarkDispatched()
			var enqueueErr error
			entry, enqueueErr = engine.compactionRuntimeState().manualBoundaryCoordinator().enqueueForGenerationOrdered(
				context.Background(),
				compactionInstructionsInput{},
				nil,
				nil,
			)
			if enqueueErr != nil {
				t.Fatalf("enqueue pending compaction: %v", enqueueErr)
			}
			if _, queued, queueErr := engine.QueueGoalSetForActiveStep("queued goal", session.GoalActorUser); queueErr != nil || !queued {
				t.Fatalf("queue goal mutation queued=%t err=%v", queued, queueErr)
			}
			gate.FailNext(cleanupErr)
			return providerErr
		},
	)
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("lifecycle error = %v, want goal cleanup failure", err)
	}

	select {
	case result := <-entry.done:
		if result.err == nil || !errors.Is(result.err, cleanupErr) {
			t.Fatalf("pending compaction result = %+v, want cleanup failure", result)
		}
	case <-t.Context().Done():
		t.Fatal("test context canceled")
	}
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(32)
	if err != nil {
		t.Fatalf("read cleanup failure records: %v", err)
	}
	for _, record := range window.Records {
		if _, ok := mustSessionEventPayload(record).(session.AgentStepBoundaryRecord); ok {
			t.Fatalf("cleanup failure committed agent step boundary in record %+v", record)
		}
	}
}

func TestTerminalFailureAbortsDanglingLiveToolBeforeBoundary(t *testing.T) {
	t.Parallel()

	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	engine.transcriptRuntimeState().SeedLiveTools([]TranscriptLiveToolStart{{
		StepID:     "step",
		ToolCallID: "dangling-tool",
		ToolName:   "exec_command",
	}})
	lifecycle := &defaultExclusiveStepLifecycle{engine: engine}
	err := lifecycle.Run(
		context.Background(),
		exclusiveStepOptions{ActiveKind: ActiveKindUserTurn},
		func(_ context.Context, stepID string) error {
			engine.agentStepBoundary(stepID).MarkDispatched()
			return errors.New("terminal provider failure")
		},
	)
	if err == nil {
		t.Fatal("terminal failure unexpectedly succeeded")
	}
	if _, ok := engine.transcriptRuntimeState().ToolCallSnapshot("dangling-tool"); ok {
		t.Fatal("terminal failure retained dangling live tool")
	}
}
