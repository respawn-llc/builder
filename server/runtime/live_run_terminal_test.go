package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
)

func TestStepEndedFailureDoesNotReclassifyCompletedLiveRun(t *testing.T) {
	stepEndedErr := errors.New("step-ended sink failed")
	var batchEvents []Event
	sink := &callbackStepLifecycleSink{onTransition: func(transition StepLifecycleTransition) error {
		if transition == StepLifecycleTransitionEnded {
			return stepEndedErr
		}
		return nil
	}}
	eng := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{
		Model:         "gpt-5",
		StepLifecycle: sink,
		OnEvent: func(event Event) {
			if event.Kind == EventLiveRunBatchFinished {
				batchEvents = append(batchEvents, event)
			}
		},
	})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}

	err := lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(_ context.Context, stepID string) error {
		eng.recordLiveRunAssistantFinalAnswer(stepID, llm.Message{Role: llm.RoleAssistant, Content: "final answer"})
		return nil
	})

	if !errors.Is(err, stepEndedErr) {
		t.Fatalf("run error = %v, want StepEnded error", err)
	}
	assertOneCompletedFinalAnswerBatch(t, batchEvents)
}

func TestPendingRecoveryCleanupFailureDoesNotReclassifyCompletedLiveRun(t *testing.T) {
	clearErr := errors.New("pending recovery cleanup failed")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateNamedTestSession(t, "ws", t.TempDir(), session.WithPersistenceObserver(gate))
	var batchEvents []Event
	sink := &callbackStepLifecycleSink{onTransition: func(transition StepLifecycleTransition) error {
		if transition == StepLifecycleTransitionEnded {
			gate.FailNext(clearErr)
		}
		return nil
	}}
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model:         "gpt-5",
		StepLifecycle: sink,
		OnEvent: func(event Event) {
			if event.Kind == EventLiveRunBatchFinished {
				batchEvents = append(batchEvents, event)
			}
		},
	})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}

	err := lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(_ context.Context, stepID string) error {
		if err := eng.markProviderVisibleModelRecovery(stepID); err != nil {
			return err
		}
		eng.recordLiveRunAssistantFinalAnswer(stepID, llm.Message{Role: llm.RoleAssistant, Content: "final answer"})
		return nil
	})

	if !errors.Is(err, errPendingModelRecoveryClear) || !errors.Is(err, clearErr) {
		t.Fatalf("run error = %v, want pending-recovery cleanup failure", err)
	}
	assertOneCompletedFinalAnswerBatch(t, batchEvents)
}

func TestIdleSchedulingFailureDoesNotReclassifyCompletedLiveRun(t *testing.T) {
	store := mustCreateTestSession(t)
	if _, err := store.SetGoal("active goal", session.GoalActorUser); err != nil {
		t.Fatalf("set active goal: %v", err)
	}
	var batchEvents []Event
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			if event.Kind == EventLiveRunBatchFinished {
				batchEvents = append(batchEvents, event)
			}
		},
	})
	eng.deferGoalLoopStart()
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}

	err := lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(_ context.Context, stepID string) error {
		eng.recordLiveRunAssistantFinalAnswer(stepID, llm.Message{Role: llm.RoleAssistant, Content: "final answer"})
		return nil
	})

	if !errors.Is(err, ErrGoalRequiresAskQuestion) {
		t.Fatalf("run error = %v, want ErrGoalRequiresAskQuestion", err)
	}
	assertOneCompletedFinalAnswerBatch(t, batchEvents)
}

func assertOneCompletedFinalAnswerBatch(t *testing.T, batchEvents []Event) {
	t.Helper()
	if len(batchEvents) != 1 {
		t.Fatalf("batch-finished events = %+v, want exactly one", batchEvents)
	}
	result := batchEvents[0].LiveRunResult
	if result == nil || result.Status != RunStatusCompleted || result.ResultKind != LiveRunResultAssistantFinalAnswer || result.AssistantMessage.Content != "final answer" || result.Error != nil {
		t.Fatalf("batch-finished result = %+v, want final answer without operational error", result)
	}
}
