package app

import (
	"errors"
	"testing"
	"time"

	"core/shared/clientui"
)

type recordingTaskCompletionSink struct {
	results []clientui.TranscriptLiveRunResult
}

func (s *recordingTaskCompletionSink) enqueueTaskCompletion(result clientui.TranscriptLiveRunResult) {
	s.results = append(s.results, result)
}

func TestTurnQueueHooksEmitEachAdmittedAssistantFinalOnceAfterIdleDrain(t *testing.T) {
	completions := &recordingTaskCompletionSink{}
	ringer := &countRinger{}
	hooks := newTurnQueueHooks(
		newBellHooks(ringer, nil, func() bool { return true }),
		completions,
	)
	model := newTurnQueueHookTestModel(hooks)
	model.activeSubmit = activeSubmitState{token: 1, text: "last queued turn"}

	first := testAssistantFinalLiveRunMessage(2, "first queued answer", true)
	second := testAssistantFinalLiveRunMessage(3, "second queued answer", false)
	for _, message := range []clientui.TranscriptMessage{
		ongoingHydrationMessage(1),
		first,
		second,
		second,
		ongoingHydrationMessage(1),
	} {
		model.handleOngoingTranscriptEvent(ongoingTranscriptEvent{
			Kind:    ongoingTranscriptEventMessage,
			Message: message,
		})
	}

	if got := len(completions.results); got != 0 {
		t.Fatalf("completion hooks before queue drain = %d, want 0", got)
	}

	_, _ = model.Update(submitDoneMsg{
		token:         1,
		submittedText: "last queued turn",
		message:       "second queued answer",
	})
	hooks.OnTurnQueueDrained()

	if got := len(completions.results); got != 2 {
		t.Fatalf("completion hooks after queue drain = %d, want 2", got)
	}
	if got := *completions.results[0].FinalAnswer; got != "first queued answer" {
		t.Fatalf("first completion answer = %q", got)
	}
	if !completions.results[0].WorkPerformed {
		t.Fatal("first completion omitted work-performed state")
	}
	if got := *completions.results[1].FinalAnswer; got != "second queued answer" {
		t.Fatalf("second completion answer = %q", got)
	}
	if completions.results[1].WorkPerformed {
		t.Fatal("second completion changed work-performed state")
	}
	if ringer.total() != 0 {
		t.Fatalf("focused zero-tool turns emitted %d terminal notifications", ringer.total())
	}
}

func TestAdmittedAssistantFinalRechecksAlreadyIdleTurnQueue(t *testing.T) {
	completions := &recordingTaskCompletionSink{}
	hooks := newTurnQueueHooks(newUnfocusedBellHooks(&countRinger{}), completions)
	model := newTurnQueueHookTestModel(hooks)

	model.handleOngoingTranscriptEvent(ongoingTranscriptEvent{
		Kind:    ongoingTranscriptEventMessage,
		Message: ongoingHydrationMessage(1),
	})
	model.activeSubmit = activeSubmitState{token: 1, text: "turn"}
	_, _ = model.Update(submitDoneMsg{
		token:         1,
		submittedText: "turn",
		message:       "answer",
	})
	model.handleOngoingTranscriptEvent(ongoingTranscriptEvent{
		Kind:    ongoingTranscriptEventMessage,
		Message: testAssistantFinalLiveRunMessage(2, "answer", true),
	})

	if got := len(completions.results); got != 1 {
		t.Fatalf("late admitted completion hooks = %d, want 1", got)
	}
}

func TestAdmittedAssistantFinalFlushesWhenQueueAbortsIdle(t *testing.T) {
	completions := &recordingTaskCompletionSink{}
	hooks := newTurnQueueHooks(newUnfocusedBellHooks(&countRinger{}), completions)
	model := newTurnQueueHookTestModel(hooks)
	model.activeSubmit = activeSubmitState{token: 1, text: "turn"}

	for _, message := range []clientui.TranscriptMessage{
		ongoingHydrationMessage(1),
		testAssistantFinalLiveRunMessage(2, "answer before submission error", true),
	} {
		model.handleOngoingTranscriptEvent(ongoingTranscriptEvent{
			Kind:    ongoingTranscriptEventMessage,
			Message: message,
		})
	}
	_, _ = model.Update(submitDoneMsg{
		token:         1,
		submittedText: "turn",
		err:           errors.New("submission failed after final observation"),
	})

	if got := len(completions.results); got != 1 {
		t.Fatalf("completion hooks after idle queue abort = %d, want 1", got)
	}
}

func TestAdmittedAssistantFinalWaitsForInjectedQueuedPromptDrain(t *testing.T) {
	completions := &recordingTaskCompletionSink{}
	hooks := newTurnQueueHooks(newUnfocusedBellHooks(&countRinger{}), completions)
	model := newTurnQueueHookTestModel(hooks)
	model.activeSubmit = activeSubmitState{token: 1, text: "current turn"}

	for _, message := range []clientui.TranscriptMessage{
		ongoingHydrationMessage(1),
		ongoingTranscriptMessage(2, clientui.TranscriptMessageQueuedMessageState),
		testAssistantFinalLiveRunMessage(3, "current answer", true),
	} {
		model.handleOngoingTranscriptEvent(ongoingTranscriptEvent{
			Kind:    ongoingTranscriptEventMessage,
			Message: message,
		})
	}
	_, _ = model.Update(submitDoneMsg{
		token:         1,
		submittedText: "current turn",
		message:       "current answer",
	})

	if got := len(completions.results); got != 0 {
		t.Fatalf("completion hooks before injected queue drain = %d, want 0", got)
	}

	model.handleOngoingTranscriptEvent(ongoingTranscriptEvent{
		Kind:    ongoingTranscriptEventMessage,
		Message: ongoingTranscriptMessage(4, clientui.TranscriptMessageUserMessageFlushed),
	})

	if got := len(completions.results); got != 1 {
		t.Fatalf("completion hooks after injected queue drain = %d, want 1", got)
	}
}

func newTurnQueueHookTestModel(hooks turnQueueHook) *uiModel {
	model := newProjectedStaticUIModel(WithUITurnQueueHook(hooks))
	model.ongoingTranscript = newOngoingTranscriptController(
		&ongoingSurfaceSpy{},
		ongoingTestFrameProvider,
		noopOngoingTranscriptRuntimeAdmission,
		model.applyAdmittedTranscriptMessageState,
	)
	return model
}

func TestTurnQueueHooksDoNotCompleteNonCompletedAssistantFinalResult(t *testing.T) {
	for _, status := range []clientui.LiveRunStatus{
		clientui.LiveRunStatusFailed,
		clientui.LiveRunStatusInterrupted,
	} {
		t.Run(string(status), func(t *testing.T) {
			completions := &recordingTaskCompletionSink{}
			hooks := newTurnQueueHooks(newUnfocusedBellHooks(&countRinger{}), completions)
			message := testAssistantFinalLiveRunMessage(2, "answer before non-completed outcome", true)
			result := message.Payload().(clientui.TranscriptLiveRunResult)
			result.Status = status
			if status == clientui.LiveRunStatusFailed {
				failure := "terminal failure"
				result.Failure = &failure
			}
			message = clientui.NewTranscriptMessage(2, clientui.NewTranscriptEvent(result))

			hooks.OnTranscriptMessage(message)
			hooks.OnTurnQueueDrained()

			if got := len(completions.results); got != 0 {
				t.Fatalf("%s assistant-final completion hooks = %d, want 0", status, got)
			}
		})
	}
}

func testAssistantFinalLiveRunMessage(
	sequence uint64,
	answer string,
	workPerformed bool,
) clientui.TranscriptMessage {
	startedAt := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(time.Duration(sequence) * time.Second)
	return clientui.NewTranscriptMessage(sequence, clientui.NewTranscriptEvent(clientui.TranscriptLiveRunResult{
		Status:        clientui.LiveRunStatusCompleted,
		ResultKind:    clientui.LiveRunResultAssistantFinalAnswer,
		WorkPerformed: workPerformed,
		FinalAnswer:   &answer,
		StartedAt:     startedAt,
		FinishedAt:    finishedAt,
	}))

}
