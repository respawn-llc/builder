package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"core/internal/testharness/scriptedllm"
	"core/server/llm"
	"core/server/tools"
	"core/shared/config"
	"core/shared/toolspec"
)

func TestCompletedResponseActiveStreamFinalizesOnce(t *testing.T) {
	step := scriptedllm.FinalAnswer("done")
	step.StreamDeltas = []llm.AssistantDelta{{Text: "done", Phase: llm.MessagePhaseFinal}}
	events := make([]Event, 0, 8)
	eng := mustNewTestEngine(t, mustCreateTestSession(t), scriptedllm.NewClient(scriptedllm.Script{Steps: []scriptedllm.Step{step}}), tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			events = append(events, evt)
		},
	})

	if _, err := eng.SubmitUserMessage(context.Background(), "finish"); err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}

	deltas := runtimeEventsOfKind(events, EventAssistantDelta)
	assistants := runtimeEventsOfKind(events, EventAssistantMessage)
	resets := runtimeEventsOfKind(events, EventAssistantDeltaReset)
	if len(deltas) != 1 || len(assistants) != 1 || len(resets) != 1 {
		t.Fatalf("stream lifecycle counts = deltas:%d assistants:%d resets:%d events=%+v", len(deltas), len(assistants), len(resets), events)
	}
	if deltas[0].AssistantTranscriptStreamID == nil || assistants[0].AssistantTranscriptStreamID == nil {
		t.Fatalf("stream identities missing: delta=%+v assistant=%+v", deltas[0], assistants[0])
	}
	if *assistants[0].AssistantTranscriptStreamID != *deltas[0].AssistantTranscriptStreamID {
		t.Fatalf("assistant stream id = %s, want delta stream id %s", *assistants[0].AssistantTranscriptStreamID, *deltas[0].AssistantTranscriptStreamID)
	}
	if strings.TrimSpace(resets[0].AssistantStreamAbortReason) != "" {
		t.Fatalf("finalization reset abort reason = %q, want empty", resets[0].AssistantStreamAbortReason)
	}
	if streaming := strings.TrimSpace(eng.ChatSnapshot().Streaming); streaming != "" {
		t.Fatalf("streaming snapshot after finalization = %q, want empty", streaming)
	}
}

func TestCompletedResponseWithoutActiveStreamPublishesNoStreamTerminal(t *testing.T) {
	events := make([]Event, 0, 6)
	eng := mustNewTestEngine(t, mustCreateTestSession(t), scriptedllm.NewClient(scriptedllm.Script{Steps: []scriptedllm.Step{
		scriptedllm.FinalAnswer("done"),
	}}), tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			events = append(events, evt)
		},
	})

	if _, err := eng.SubmitUserMessage(context.Background(), "finish"); err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}

	assistants := runtimeEventsOfKind(events, EventAssistantMessage)
	if len(assistants) != 1 {
		t.Fatalf("assistant event count = %d, want 1; events=%+v", len(assistants), events)
	}
	if assistants[0].AssistantTranscriptStreamID != nil {
		t.Fatalf("assistant without active stream received stream id %s", *assistants[0].AssistantTranscriptStreamID)
	}
	if resets := runtimeEventsOfKind(events, EventAssistantDeltaReset); len(resets) != 0 {
		t.Fatalf("assistant without active stream emitted %d reset terminals: %+v", len(resets), resets)
	}
}

func TestCompletedResponseExternalWorkflowCompletionDiscardsActiveStreamWithoutPersistence(t *testing.T) {
	controller := &fakeWorkflowController{}
	step := scriptedllm.ToolBatch("stale assistant", llm.ToolCall{
		ID:    "call-stale",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"cmd":"true"}`),
	})
	step.StreamDeltas = []llm.AssistantDelta{{Text: "stale draft", Phase: llm.MessagePhaseCommentary}}
	step.AfterResponse = func(context.Context) error {
		controller.completedExternally.Store(true)
		return nil
	}
	events := make([]Event, 0, 8)
	eng := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		scriptedllm.NewClient(scriptedllm.Script{Steps: []scriptedllm.Step{step}}),
		testWorkflowConfig(controller, config.WorkflowCompletionModeShellCommand),
		Config{
			OnEvent: func(evt Event) {
				events = append(events, evt)
			},
		},
	)

	if _, err := eng.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("SubmitWorkflowTurn: %v", err)
	}

	deltas := runtimeEventsOfKind(events, EventAssistantDelta)
	resets := runtimeEventsOfKind(events, EventAssistantDeltaReset)
	if len(deltas) != 1 || len(resets) != 1 {
		t.Fatalf("discard lifecycle counts = deltas:%d resets:%d events=%+v", len(deltas), len(resets), events)
	}
	if deltas[0].AssistantTranscriptStreamID == nil || resets[0].AssistantTranscriptStreamID == nil {
		t.Fatalf("discard stream identities missing: delta=%+v reset=%+v", deltas[0], resets[0])
	}
	if *resets[0].AssistantTranscriptStreamID != *deltas[0].AssistantTranscriptStreamID {
		t.Fatalf("discard stream id = %s, want delta stream id %s", *resets[0].AssistantTranscriptStreamID, *deltas[0].AssistantTranscriptStreamID)
	}
	if resets[0].AssistantStreamAbortReason != string(AssistantStreamAbortSuperseded) {
		t.Fatalf("discard reason = %q, want %q", resets[0].AssistantStreamAbortReason, AssistantStreamAbortSuperseded)
	}
	if starts := runtimeEventsOfKind(events, EventToolCallStarted); len(starts) != 0 {
		t.Fatalf("stale workflow response executed tools: %+v", starts)
	}
	for _, message := range eng.transcriptRuntimeState().SnapshotMessages() {
		if message.Role == llm.RoleAssistant {
			t.Fatalf("stale assistant response was persisted: %+v", eng.transcriptRuntimeState().SnapshotMessages())
		}
		if message.Role == llm.RoleTool && message.ToolCallID == "call-stale" {
			t.Fatalf("stale tool result was persisted: %+v", eng.transcriptRuntimeState().SnapshotMessages())
		}
	}
}

func TestCompletedResponseWorkflowPreflightAbortsBeforeContinuation(t *testing.T) {
	controller := &fakeWorkflowController{}
	rejected := scriptedllm.ToolBatch("",
		completeNodeCall("call-complete-a", json.RawMessage(`{"commentary":"a","summary":"a"}`)),
		completeNodeCall("call-complete-b", json.RawMessage(`{"commentary":"b","summary":"b"}`)),
	)
	rejected.StreamDeltas = []llm.AssistantDelta{{Text: "draft", Phase: llm.MessagePhaseCommentary}}
	accepted := scriptedllm.FinalAnswer(`{"commentary":"complete","summary":"done"}`)
	accepted.StreamDeltas = []llm.AssistantDelta{{Text: "done", Phase: llm.MessagePhaseFinal}}
	events := make([]Event, 0, 16)
	eng := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		scriptedllm.NewClient(scriptedllm.Script{Steps: []scriptedllm.Step{rejected, accepted}}),
		testWorkflowConfig(controller, config.WorkflowCompletionModeUnstructured),
		Config{
			OnEvent: func(evt Event) {
				events = append(events, evt)
			},
		},
	)

	if _, err := eng.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("SubmitWorkflowTurn: %v", err)
	}

	assertFirstCompletedResponseAbortedBeforeFinalContinuation(t, events)
	if got := controller.violations.Load(); got != 1 {
		t.Fatalf("workflow violations = %d, want 1", got)
	}
	if got := controller.completed.Load(); got != 1 {
		t.Fatalf("workflow completions = %d, want 1", got)
	}
}

func TestCompletedResponseReasoningOnlyAbortsBeforeContinuation(t *testing.T) {
	reasoning := scriptedllm.Step{
		Response: llm.Response{
			Assistant: llm.Message{
				Role: llm.RoleAssistant,
				ReasoningItems: []llm.ReasoningItem{{
					ID:               "reasoning-1",
					EncryptedContent: "encrypted",
				}},
			},
			ReasoningItems: []llm.ReasoningItem{{
				ID:               "reasoning-1",
				EncryptedContent: "encrypted",
			}},
			OutputItems: []llm.ResponseItem{{
				Type:             llm.ResponseItemTypeReasoning,
				ID:               "reasoning-1",
				EncryptedContent: "encrypted",
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		StreamDeltas: []llm.AssistantDelta{{Text: "draft", Phase: llm.MessagePhaseCommentary}},
	}
	final := scriptedllm.FinalAnswer("done")
	final.StreamDeltas = []llm.AssistantDelta{{Text: "done", Phase: llm.MessagePhaseFinal}}
	events := make([]Event, 0, 12)
	eng := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		scriptedllm.NewClient(scriptedllm.Script{Steps: []scriptedllm.Step{reasoning, final}}),
		tools.NewRegistry(),
		Config{
			Model: "gpt-5",
			OnEvent: func(evt Event) {
				events = append(events, evt)
			},
		},
	)

	if _, err := eng.SubmitUserMessage(context.Background(), "reason first"); err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}

	assertFirstCompletedResponseAbortedBeforeFinalContinuation(t, events)
}

func TestCompletedResponseFinalAnswerWithToolsFinalizesAfterToolPersistence(t *testing.T) {
	step := scriptedllm.ToolBatch("done", llm.ToolCall{
		ID:    "call-tool",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"cmd":"true"}`),
	})
	step.Response.Assistant.Phase = llm.MessagePhaseFinal
	step.StreamDeltas = []llm.AssistantDelta{{Text: "done", Phase: llm.MessagePhaseFinal}}
	events := make([]Event, 0, 12)
	eng := mustNewExecTestEngine(
		t,
		mustCreateTestSession(t),
		scriptedllm.NewClient(scriptedllm.Script{Steps: []scriptedllm.Step{step}}),
		Config{
			Model: "gpt-5",
			OnEvent: func(evt Event) {
				events = append(events, evt)
			},
		},
	)

	if _, err := eng.SubmitUserMessage(context.Background(), "run then finish"); err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}

	deltas := runtimeEventsOfKind(events, EventAssistantDelta)
	resets := runtimeEventsOfKind(events, EventAssistantDeltaReset)
	if len(deltas) != 1 || len(resets) != 1 {
		t.Fatalf("final-answer tool lifecycle counts = deltas:%d resets:%d events=%+v", len(deltas), len(resets), events)
	}
	final := firstRuntimeEventObservation(events, func(event Event) bool {
		return event.Kind == EventAssistantMessage &&
			event.Message.Phase == llm.MessagePhaseFinal &&
			event.AssistantTranscriptStreamID != nil
	})
	toolStart := firstRuntimeEventObservation(events, func(event Event) bool {
		return event.Kind == EventToolCallStarted && event.ToolCall != nil && event.ToolCall.ID == "call-tool"
	})
	toolCompletion := firstRuntimeEventObservation(events, func(event Event) bool {
		return event.Kind == EventToolCallCompleted && event.ToolResult != nil && event.ToolResult.CallID == "call-tool"
	})
	if toolStart == nil || toolCompletion == nil || final == nil ||
		toolCompletion.position <= toolStart.position ||
		final.position <= toolCompletion.position {
		t.Fatalf("final-answer tool order = start:%+v completion:%+v final:%+v events=%+v", toolStart, toolCompletion, final, events)
	}
	if deltas[0].AssistantTranscriptStreamID == nil || final.event.AssistantTranscriptStreamID == nil {
		t.Fatalf("finalization stream identities missing: delta=%+v final=%+v", deltas[0], final.event)
	}
	if *final.event.AssistantTranscriptStreamID != *deltas[0].AssistantTranscriptStreamID {
		t.Fatalf("final assistant stream id = %s, want delta stream id %s", *final.event.AssistantTranscriptStreamID, *deltas[0].AssistantTranscriptStreamID)
	}
	if resets[0].AssistantStreamAbortReason != "" {
		t.Fatalf("finalization reset abort reason = %q, want empty", resets[0].AssistantStreamAbortReason)
	}
}

func runtimeEventsOfKind(events []Event, kind EventKind) []Event {
	out := make([]Event, 0, 1)
	for _, event := range events {
		if event.Kind == kind {
			out = append(out, event)
		}
	}
	return out
}

func assertFirstCompletedResponseAbortedBeforeFinalContinuation(t *testing.T, events []Event) {
	t.Helper()
	deltas := runtimeEventsOfKind(events, EventAssistantDelta)
	resets := runtimeEventsOfKind(events, EventAssistantDeltaReset)
	if len(deltas) != 2 || len(resets) != 2 {
		t.Fatalf("continuation lifecycle counts = deltas:%d resets:%d events=%+v", len(deltas), len(resets), events)
	}
	if deltas[0].AssistantTranscriptStreamID == nil || resets[0].AssistantTranscriptStreamID == nil {
		t.Fatalf("continuation abort stream identities missing: delta=%+v reset=%+v", deltas[0], resets[0])
	}
	if *resets[0].AssistantTranscriptStreamID != *deltas[0].AssistantTranscriptStreamID ||
		resets[0].AssistantStreamAbortReason != string(AssistantStreamAbortSuperseded) {
		t.Fatalf("continuation abort = %+v, want superseded terminal for first delta %+v", resets[0], deltas[0])
	}
	firstAbort := firstRuntimeEventObservation(events, func(event Event) bool {
		return event.Kind == EventAssistantDeltaReset && event.AssistantStreamAbortReason == string(AssistantStreamAbortSuperseded)
	})
	resumedDelta := firstRuntimeEventObservation(events, func(event Event) bool {
		return event.Kind == EventAssistantDelta && event.AssistantDeltaPhase == llm.MessagePhaseFinal
	})
	if firstAbort == nil || resumedDelta == nil || resumedDelta.position <= firstAbort.position {
		t.Fatalf("continuation order = abort:%+v resumed-delta:%+v events=%+v", firstAbort, resumedDelta, events)
	}
}

type runtimeEventObservation struct {
	position int
	event    Event
}

func firstRuntimeEventObservation(events []Event, predicate func(Event) bool) *runtimeEventObservation {
	for index, event := range events {
		if predicate(event) {
			return &runtimeEventObservation{position: index, event: event}
		}
	}
	return nil
}
