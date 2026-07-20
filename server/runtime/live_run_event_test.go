package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"

	"core/server/llm"
	"core/server/tools"
)

func TestEnginePublishesCompletedLiveRunResult(t *testing.T) {
	store := mustCreateTestSession(t)
	var mu sync.Mutex
	var finished []LiveRunResult
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			if event.Kind != EventLiveRunFinished || event.LiveRunResult == nil {
				return
			}
			mu.Lock()
			finished = append(finished, *event.LiveRunResult)
			mu.Unlock()
		},
	})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	eng.stepLifecycle = lifecycle

	err := lifecycle.Run(t.Context(), exclusiveStepOptions{
		EmitRunState: true,
		ActiveKind:   ActiveKindUserTurn,
	}, func(_ context.Context, stepID string) error {
		eng.recordLiveRunAssistantFinalAnswer(stepID, llm.Message{Role: llm.RoleAssistant, Content: "done"})
		for _, callID := range []string{"call-1", "call-2"} {
			call := llm.ToolCall{ID: callID, Name: "test_tool"}
			if err := eng.steer(stepID, steerEventIntent(Event{
				Kind:     EventToolCallStarted,
				StepID:   stepID,
				ToolCall: &call,
			})); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(finished) != 1 {
		t.Fatalf("finished events = %d, want 1", len(finished))
	}
	result := finished[0]
	if result.Status != RunStatusCompleted ||
		result.ResultKind != LiveRunResultAssistantFinalAnswer ||
		result.AssistantMessage.Content != "done" ||
		!result.WorkPerformed {
		t.Fatalf("result = %+v", result)
	}
}

func TestEnginePublishesFailedLiveRunResult(t *testing.T) {
	store := mustCreateTestSession(t)
	failure := errors.New("provider failed")
	var finished *LiveRunResult
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			if event.Kind == EventLiveRunFinished && event.LiveRunResult != nil {
				copyResult := *event.LiveRunResult
				finished = &copyResult
			}
		},
	})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	eng.stepLifecycle = lifecycle

	err := lifecycle.Run(t.Context(), exclusiveStepOptions{
		EmitRunState: true,
		ActiveKind:   ActiveKindUserTurn,
	}, func(context.Context, string) error {
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("run error = %v, want %v", err, failure)
	}
	if finished == nil {
		t.Fatal("missing failed live-run event")
	}
	if finished.Status != RunStatusFailed ||
		finished.ResultKind != LiveRunResultNoFinalAnswer ||
		!errors.Is(finished.Error, failure) {
		t.Fatalf("result = %+v", *finished)
	}
}
