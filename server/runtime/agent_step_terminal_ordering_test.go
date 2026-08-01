package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
)

func TestExclusiveStepLifecycleWaitsForInlineCompactionBeforeTerminalPublication(t *testing.T) {
	t.Parallel()

	compactionStarted := make(chan struct{})
	releaseCompaction := make(chan struct{})
	client := &hookClient{
		response: llm.Response{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("compaction summary"),
			},
		},
		beforeReturn: func() error {
			close(compactionStarted)
			<-releaseCompaction
			return nil
		},
	}
	ended := make(chan StepLifecycleSnapshot, 1)
	sink := &callbackStepLifecycleSink{
		onTransition: func(transition StepLifecycleTransition) error {
			if transition == StepLifecycleTransitionEnded {
				ended <- StepLifecycleSnapshot{Transition: transition}
			}
			return nil
		},
	}
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model:          "gpt-5",
		CompactionMode: "local",
		StepLifecycle:  sink,
	})
	if err := engine.steer("seed", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}
	engine.compactionRuntimeState().SetManualCompactionEligible(true)
	var entry *pendingManualCompaction

	providerErr := errors.New("main provider failed")
	runDone := make(chan error, 1)
	go func() {
		runDone <- engine.stepLifecycle.Run(
			context.Background(),
			exclusiveStepOptions{ActiveKind: ActiveKindUserTurn},
			func(_ context.Context, stepID string) error {
				engine.agentStepBoundary(stepID).MarkDispatched()
				var enqueueErr error
				entry, enqueueErr = engine.compactionRuntimeState().manualBoundaryCoordinator().enqueueForGeneration(
					context.Background(),
					compactionInstructionsInput{},
					nil,
				)
				if enqueueErr != nil {
					t.Fatalf("enqueue pending compaction: %v", enqueueErr)
				}
				return providerErr
			},
		)
	}()

	select {
	case <-compactionStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("inline compaction did not start")
	}
	select {
	case <-ended:
		t.Fatal("terminal publication occurred while inline compaction was blocked")
	default:
	}
	if snapshot := engine.ActiveRun(); snapshot == nil || snapshot.ActiveKind != ActiveKindUserTurn {
		t.Fatalf("active run while inline compaction was blocked = %+v", snapshot)
	}

	close(releaseCompaction)
	select {
	case result := <-entry.done:
		if result.err != nil || !result.receipt.Committed {
			t.Fatalf("inline compaction result = %+v, want committed success", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("inline compaction did not complete")
	}
	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatal("terminal publication did not complete after inline compaction")
	}
	if err := <-runDone; !errors.Is(err, providerErr) {
		t.Fatalf("lifecycle error = %v, want provider failure", err)
	}
}
