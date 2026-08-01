package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"core/internal/testharness/scriptedllm"
	"core/server/llm"
	"core/server/session"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestCompletedResponseActiveStreamFinalizesOnce(t *testing.T) {
	t.Parallel()
	step := scriptedllm.FinalAnswer("completed")
	step.StreamDeltas = []llm.AssistantDelta{{
		Text:  "draft",
		Phase: llm.MessagePhaseFinal,
	}}
	var events []Event
	engine := mustNewExecTestEngine(
		t,
		mustCreateTestSession(t),
		scriptedllm.NewClient(scriptedllm.Script{Steps: []scriptedllm.Step{step}}),
		Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		},
	)

	if _, err := engine.SubmitUserMessage(context.Background(), "turn"); err != nil {
		t.Fatalf("submit user turn: %v", err)
	}

	var delta, assistant, reset *Event
	deltaIndex, assistantIndex, resetIndex := -1, -1, -1
	for index := range events {
		event := &events[index]
		switch event.Kind {
		case EventAssistantDelta:
			if delta != nil {
				t.Fatalf("multiple assistant deltas: %+v", events)
			}
			delta = event
			deltaIndex = index
		case EventAssistantMessage:
			if assistant != nil {
				t.Fatalf("multiple assistant messages: %+v", events)
			}
			assistant = event
			assistantIndex = index
		case EventAssistantDeltaReset:
			if reset != nil {
				t.Fatalf("multiple assistant stream terminals: %+v", events)
			}
			reset = event
			resetIndex = index
		}
	}

	if delta == nil || assistant == nil || reset == nil ||
		delta.AssistantTranscriptStreamID == nil ||
		assistant.AssistantTranscriptStreamID == nil ||
		reset.AssistantTranscriptStreamID == nil ||
		assistant.Message.Role != llm.RoleAssistant ||
		assistant.Message.Phase == nil ||
		*assistant.Message.Phase != llm.MessagePhaseFinal ||
		!assistant.CommittedEntryStartSet {
		t.Fatalf(
			"stream finalization facts = delta:%+v assistant:%+v reset:%+v",
			delta,
			assistant,
			reset,
		)
	}
	if deltaIndex >= assistantIndex || assistantIndex >= resetIndex {
		t.Fatalf(
			"stream finalization order = delta:%d assistant:%d reset:%d events:%+v",
			deltaIndex,
			assistantIndex,
			resetIndex,
			events,
		)
	}
	if *delta.AssistantTranscriptStreamID != *assistant.AssistantTranscriptStreamID ||
		*delta.AssistantTranscriptStreamID != *reset.AssistantTranscriptStreamID {
		t.Fatalf(
			"stream finalization UUIDs differ: delta:%s assistant:%s reset:%s",
			*delta.AssistantTranscriptStreamID,
			*assistant.AssistantTranscriptStreamID,
			*reset.AssistantTranscriptStreamID,
		)
	}
}

func TestCompletedResponseWithoutActiveStreamPublishesNoStreamTerminal(t *testing.T) {
	t.Parallel()
	var events []Event
	engine := mustNewExecTestEngine(
		t,
		mustCreateTestSession(t),
		scriptedllm.NewClient(scriptedllm.Script{Steps: []scriptedllm.Step{
			scriptedllm.FinalAnswer("completed"),
		}}),
		Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		},
	)

	if _, err := engine.SubmitUserMessage(context.Background(), "turn"); err != nil {
		t.Fatalf("submit user turn: %v", err)
	}

	var assistant *Event
	resetCount := 0
	for index := range events {
		event := &events[index]
		switch event.Kind {
		case EventAssistantMessage:
			if assistant != nil {
				t.Fatalf("multiple assistant messages: %+v", events)
			}
			assistant = event
		case EventAssistantDeltaReset:
			resetCount++
		}
	}
	if assistant == nil ||
		resetCount != 0 ||
		assistant.AssistantStreamMetadata != nil ||
		assistant.AssistantTranscriptStreamID != nil ||
		assistant.Message.Role != llm.RoleAssistant ||
		assistant.Message.Phase == nil ||
		*assistant.Message.Phase != llm.MessagePhaseFinal ||
		!assistant.CommittedEntryStartSet {
		t.Fatalf("non-streamed final assistant event = %+v", assistant)
	}
}

func TestCompletedResponseWorkflowPreflightAbortsBeforeContinuation(t *testing.T) {
	t.Parallel()
	scopeID := runtimeids.NewExecutionScopeID()
	controller := &workflowCompletionAccountingController{}
	rejected := scriptedllm.ToolBatch(
		"working",
		workflowCompleteNodeCall("duplicate-completion-a", `{"transition":"done","summary":"done"}`),
		workflowCompleteNodeCall("duplicate-completion-b", `{"transition":"done","summary":"done"}`),
	)
	rejected.StreamDeltas = []llm.AssistantDelta{{
		Text:  "draft",
		Phase: llm.MessagePhaseCommentary,
	}}
	accepted := scriptedllm.FinalAnswer(`{"transition":"done","summary":"done"}`)
	accepted.StreamDeltas = []llm.AssistantDelta{{
		Text:  "completed",
		Phase: llm.MessagePhaseFinal,
	}}
	var events []Event
	engine := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		scriptedllm.NewClient(scriptedllm.Script{Steps: []scriptedllm.Step{rejected, accepted}}),
		&workflowruntime.CurrentNodeExecutionConfig{
			ScopeID: scopeID,
			Contract: workflowruntime.CompletionContract{
				Transitions: []workflowruntime.CompletionTransition{{
					ID:         "done",
					Parameters: []workflow.Parameter{{Key: "summary"}},
				}},
			},
			CompletionMode:               workflowruntime.CompletionModeUnstructuredOutput,
			MaxInvalidCompletionAttempts: 2,
			Controller:                   controller,
		},
		Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		},
	)

	if _, err := engine.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("submit workflow turn: %v", err)
	}

	assertSupersededStreamPrecedesFinalContinuation(t, events, "workflow preflight")
	if got := controller.violations.Load(); got != 1 {
		t.Fatalf("workflow protocol violations = %d, want one", got)
	}
	if got := controller.completions.Load(); got != 1 {
		t.Fatalf("workflow completions = %d, want one", got)
	}
}

func TestCompletedResponseReasoningOnlyAbortsBeforeContinuation(t *testing.T) {
	t.Parallel()
	reasoning := scriptedllm.Step{
		Response: llm.Response{
			Assistant: llm.Message{
				Role: llm.RoleAssistant,
				ReasoningItems: []llm.ReasoningItem{{
					ID:               "reasoning-checkpoint",
					EncryptedContent: "encrypted",
				}},
			},
			ReasoningItems: []llm.ReasoningItem{{
				ID:               "reasoning-checkpoint",
				EncryptedContent: "encrypted",
			}},
			OutputItems: []llm.ResponseItem{{
				Type:             llm.ResponseItemTypeReasoning,
				ID:               textutil.Value("reasoning-checkpoint"),
				EncryptedContent: textutil.Value("encrypted"),
			}},
			Usage: llm.Usage{WindowTokens: 200_000},
		},
		StreamDeltas: []llm.AssistantDelta{{
			Text:  "draft",
			Phase: llm.MessagePhaseCommentary,
		}},
	}
	final := scriptedllm.FinalAnswer("completed")
	final.StreamDeltas = []llm.AssistantDelta{{
		Text:  "resolved",
		Phase: llm.MessagePhaseFinal,
	}}
	var events []Event
	engine := mustNewExecTestEngine(
		t,
		mustCreateTestSession(t),
		scriptedllm.NewClient(scriptedllm.Script{Steps: []scriptedllm.Step{reasoning, final}}),
		Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		},
	)

	if _, err := engine.SubmitUserMessage(context.Background(), "turn"); err != nil {
		t.Fatalf("submit user turn: %v", err)
	}

	assertSupersededStreamPrecedesFinalContinuation(t, events, "reasoning-only")
}

func assertSupersededStreamPrecedesFinalContinuation(t *testing.T, events []Event, label string) {
	t.Helper()
	var firstDelta, firstReset, secondDelta, finalAssistant *Event
	firstDeltaIndex, firstResetIndex, secondDeltaIndex, finalAssistantIndex := -1, -1, -1, -1
	for index := range events {
		event := &events[index]
		switch event.Kind {
		case EventAssistantDelta:
			if firstDelta == nil {
				firstDelta = event
				firstDeltaIndex = index
				continue
			}
			if secondDelta != nil {
				t.Fatalf("%s emitted unexpected assistant deltas: %+v", label, events)
			}
			secondDelta = event
			secondDeltaIndex = index
		case EventAssistantDeltaReset:
			if firstDelta != nil &&
				firstDelta.AssistantTranscriptStreamID != nil &&
				event.AssistantTranscriptStreamID != nil &&
				*event.AssistantTranscriptStreamID == *firstDelta.AssistantTranscriptStreamID {
				if firstReset != nil {
					t.Fatalf("%s emitted multiple terminals for the superseded stream: %+v", label, events)
				}
				firstReset = event
				firstResetIndex = index
			}
		case EventAssistantMessage:
			if finalAssistant != nil {
				t.Fatalf("%s published multiple assistant messages: %+v", label, events)
			}
			finalAssistant = event
			finalAssistantIndex = index
		}
	}

	if firstDelta == nil || firstReset == nil || secondDelta == nil || finalAssistant == nil ||
		firstDelta.AssistantTranscriptStreamID == nil ||
		firstReset.AssistantTranscriptStreamID == nil ||
		secondDelta.AssistantTranscriptStreamID == nil ||
		finalAssistant.AssistantTranscriptStreamID == nil ||
		finalAssistant.Message.Role != llm.RoleAssistant ||
		finalAssistant.Message.Phase == nil ||
		*finalAssistant.Message.Phase != llm.MessagePhaseFinal ||
		!finalAssistant.CommittedEntryStartSet {
		t.Fatalf(
			"%s stream facts = first_delta:%+v first_reset:%+v second_delta:%+v final:%+v",
			label,
			firstDelta,
			firstReset,
			secondDelta,
			finalAssistant,
		)
	}
	if *firstDelta.AssistantTranscriptStreamID != *firstReset.AssistantTranscriptStreamID ||
		*firstDelta.AssistantTranscriptStreamID == *secondDelta.AssistantTranscriptStreamID ||
		*secondDelta.AssistantTranscriptStreamID != *finalAssistant.AssistantTranscriptStreamID {
		t.Fatalf(
			"%s stream UUIDs = first:%s reset:%s second:%s final:%s",
			label,
			*firstDelta.AssistantTranscriptStreamID,
			*firstReset.AssistantTranscriptStreamID,
			*secondDelta.AssistantTranscriptStreamID,
			*finalAssistant.AssistantTranscriptStreamID,
		)
	}
	if firstDeltaIndex >= firstResetIndex ||
		firstResetIndex >= secondDeltaIndex ||
		secondDeltaIndex >= finalAssistantIndex {
		t.Fatalf(
			"%s stream order = first_delta:%d first_reset:%d second_delta:%d final:%d events:%+v",
			label,
			firstDeltaIndex,
			firstResetIndex,
			secondDeltaIndex,
			finalAssistantIndex,
			events,
		)
	}
}

func TestCompletedResponseFinalAnswerWithToolsFinalizesAfterToolPersistence(t *testing.T) {
	t.Parallel()
	const callID = "final-tool"
	step := scriptedllm.ToolBatch("completed", llm.ToolCall{
		ID:    callID,
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"cmd":"true"}`),
	})
	step.Response.Assistant.Phase = textutil.Value(llm.MessagePhaseFinal)
	step.StreamDeltas = []llm.AssistantDelta{{
		Text:  "draft",
		Phase: llm.MessagePhaseFinal,
	}}
	var events []Event
	store := mustCreateTestSession(t)
	engine := mustNewExecTestEngine(
		t,
		store,
		scriptedllm.NewClient(scriptedllm.Script{Steps: []scriptedllm.Step{step}}),
		Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		},
	)

	if _, err := engine.SubmitUserMessage(context.Background(), "turn"); err != nil {
		t.Fatalf("submit user turn: %v", err)
	}

	var delta, toolStart, toolCompletion, finalAssistant, terminal *Event
	deltaIndex, toolStartIndex, toolCompletionIndex, finalAssistantIndex, terminalIndex := -1, -1, -1, -1, -1
	for index := range events {
		event := &events[index]
		switch event.Kind {
		case EventAssistantDelta:
			if delta != nil {
				t.Fatalf("multiple assistant deltas: %+v", events)
			}
			delta = event
			deltaIndex = index
		case EventToolCallStarted:
			if !event.CommittedTranscriptChanged {
				continue
			}
			if event.ToolCall == nil || event.ToolCall.ID != callID {
				continue
			}
			if toolStart != nil {
				t.Fatalf("multiple starts for final-attached tool: %+v", events)
			}
			toolStart = event
			toolStartIndex = index
		case EventToolCallCompleted:
			if !event.CommittedTranscriptChanged {
				continue
			}
			if event.ToolResult == nil || event.ToolResult.CallID != callID {
				continue
			}
			if toolCompletion != nil {
				t.Fatalf("multiple completions for final-attached tool: %+v", events)
			}
			toolCompletion = event
			toolCompletionIndex = index
		case EventAssistantMessage:
			if event.Message.Role != llm.RoleAssistant ||
				event.Message.Phase == nil ||
				*event.Message.Phase != llm.MessagePhaseFinal {
				continue
			}
			if finalAssistant != nil {
				t.Fatalf("multiple final assistant events: %+v", events)
			}
			finalAssistant = event
			finalAssistantIndex = index
		case EventAssistantDeltaReset:
			if terminal != nil {
				t.Fatalf("multiple assistant stream terminals: %+v", events)
			}
			terminal = event
			terminalIndex = index
		}
	}

	if delta == nil || toolStart == nil || toolCompletion == nil || finalAssistant == nil || terminal == nil ||
		delta.AssistantTranscriptStreamID == nil ||
		finalAssistant.AssistantTranscriptStreamID == nil ||
		terminal.AssistantTranscriptStreamID == nil ||
		finalAssistant.Message.Role != llm.RoleAssistant ||
		finalAssistant.Message.Phase == nil ||
		*finalAssistant.Message.Phase != llm.MessagePhaseFinal ||
		!finalAssistant.CommittedEntryStartSet {
		t.Fatalf(
			"final-attached tool stream facts = delta:%+v start:%+v completion:%+v final:%+v terminal:%+v",
			delta,
			toolStart,
			toolCompletion,
			finalAssistant,
			terminal,
		)
	}
	if deltaIndex >= toolStartIndex ||
		toolStartIndex >= toolCompletionIndex ||
		toolCompletionIndex >= finalAssistantIndex ||
		finalAssistantIndex >= terminalIndex {
		t.Fatalf(
			"final-attached tool event order = delta:%d start:%d completion:%d final:%d terminal:%d events:%+v",
			deltaIndex,
			toolStartIndex,
			toolCompletionIndex,
			finalAssistantIndex,
			terminalIndex,
			events,
		)
	}
	if *delta.AssistantTranscriptStreamID != *finalAssistant.AssistantTranscriptStreamID ||
		*delta.AssistantTranscriptStreamID != *terminal.AssistantTranscriptStreamID {
		t.Fatalf(
			"final-attached tool stream UUIDs = delta:%s final:%s terminal:%s",
			*delta.AssistantTranscriptStreamID,
			*finalAssistant.AssistantTranscriptStreamID,
			*terminal.AssistantTranscriptStreamID,
		)
	}

	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded final-attached tool records: %v", err)
	}
	toolCompletionRecordIndex, finalAssistantRecordIndex := -1, -1
	for index, record := range window.Records {
		switch payload := mustSessionEventPayload(record).(type) {
		case session.ToolCompletionRecord:
			if payload.CallID != callID {
				continue
			}
			if toolCompletionRecordIndex >= 0 {
				t.Fatalf("multiple persisted completions for final-attached tool: %+v", window.Records)
			}
			if payload.Name != string(toolspec.ToolExecCommand) || payload.IsError {
				t.Fatalf("persisted final-attached tool completion = %+v", payload)
			}
			toolCompletionRecordIndex = index
		case session.MessageRecord:
			message, restoreErr := llmMessageFromSessionRecord(payload)
			if restoreErr != nil {
				t.Fatalf("restore persisted message: %v", restoreErr)
			}
			if message.Role != llm.RoleAssistant ||
				message.Phase == nil ||
				*message.Phase != llm.MessagePhaseFinal {
				continue
			}
			if finalAssistantRecordIndex >= 0 {
				t.Fatalf("multiple persisted final assistants: %+v", window.Records)
			}
			finalAssistantRecordIndex = index
		}
	}
	if toolCompletionRecordIndex < 0 ||
		finalAssistantRecordIndex < 0 ||
		toolCompletionRecordIndex >= finalAssistantRecordIndex {
		t.Fatalf(
			"final-attached tool persisted order = completion:%d final:%d records:%+v",
			toolCompletionRecordIndex,
			finalAssistantRecordIndex,
			window.Records,
		)
	}
}

func TestSubmitUserMessageFinalAnswerWithMixedToolCallsMaterializesAllToolsBeforeSingleFinal(t *testing.T) {
	t.Parallel()
	calls := []llm.ToolCall{
		{ID: "final-tool-one", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"true"}`)},
		{ID: "final-tool-two", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"true"}`)},
	}
	step := scriptedllm.ToolBatch("completed", calls...)
	step.Response.Assistant.Phase = textutil.Value(llm.MessagePhaseFinal)
	step.StreamDeltas = []llm.AssistantDelta{{Text: "draft", Phase: llm.MessagePhaseFinal}}
	var events []Event
	store := mustCreateTestSession(t)
	engine := mustNewExecTestEngine(t, store, scriptedllm.NewClient(scriptedllm.Script{Steps: []scriptedllm.Step{step}}), Config{
		Model:   "gpt-5",
		OnEvent: func(event Event) { events = append(events, event) },
	})
	if _, err := engine.SubmitUserMessage(context.Background(), "turn"); err != nil {
		t.Fatalf("submit turn: %v", err)
	}

	starts, completions := map[string]int{}, map[string]int{}
	finalIndex, terminalIndex := -1, -1
	for index, event := range events {
		switch event.Kind {
		case EventToolCallStarted:
			if event.ToolCall != nil {
				starts[event.ToolCall.ID] = index
			}
		case EventToolCallCompleted:
			if event.ToolResult != nil {
				completions[event.ToolResult.CallID] = index
			}
		case EventAssistantMessage:
			if event.Message.Role == llm.RoleAssistant && event.Message.Phase != nil && *event.Message.Phase == llm.MessagePhaseFinal {
				if finalIndex >= 0 {
					t.Fatalf("multiple final assistant events: %+v", events)
				}
				finalIndex = index
			}
		case EventAssistantDeltaReset:
			terminalIndex = index
		}
	}
	if finalIndex < 0 || terminalIndex <= finalIndex {
		t.Fatalf("final/terminal order = final:%d terminal:%d events:%+v", finalIndex, terminalIndex, events)
	}
	for _, call := range calls {
		start, started := starts[call.ID]
		completion, completed := completions[call.ID]
		if !started || !completed || start >= completion || completion >= finalIndex {
			t.Fatalf("tool materialization %s = start:%d completion:%d final:%d events:%+v", call.ID, start, completion, finalIndex, events)
		}
	}
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded records: %v", err)
	}
	recordCompletions, persistedFinalIndex := map[string]int{}, -1
	for index, record := range window.Records {
		switch payload := mustSessionEventPayload(record).(type) {
		case session.ToolCompletionRecord:
			recordCompletions[payload.CallID] = index
		case session.MessageRecord:
			message, restoreErr := llmMessageFromSessionRecord(payload)
			if restoreErr != nil {
				t.Fatalf("restore message: %v", restoreErr)
			}
			if message.Role == llm.RoleAssistant && message.Phase != nil && *message.Phase == llm.MessagePhaseFinal {
				persistedFinalIndex = index
			}
		}
	}
	for _, call := range calls {
		if completion, ok := recordCompletions[call.ID]; !ok || persistedFinalIndex < 0 || completion >= persistedFinalIndex {
			t.Fatalf("persisted tool/final order %s = completion:%d final:%d records:%+v", call.ID, completion, persistedFinalIndex, window.Records)
		}
	}
}

func TestNoopStreamedFinalResetsWithoutFinalPublication(t *testing.T) {
	t.Parallel()
	var events []Event
	engine := mustNewExecTestEngine(t, mustCreateTestSession(t), fakeNoopStreamClient{}, Config{
		Model:   "gpt-5",
		OnEvent: func(event Event) { events = append(events, event) },
	})
	if _, err := engine.SubmitUserMessage(context.Background(), "turn"); err != nil {
		t.Fatalf("submit noop stream: %v", err)
	}
	var delta, reset *Event
	for index := range events {
		event := &events[index]
		switch event.Kind {
		case EventAssistantDelta:
			delta = event
		case EventAssistantDeltaReset:
			reset = event
		case EventAssistantMessage, EventModelResponse:
			t.Fatalf("noop streamed final published %s: %+v", event.Kind, event)
		}
	}
	if delta == nil || reset == nil || delta.AssistantTranscriptStreamID == nil || reset.AssistantTranscriptStreamID == nil ||
		*delta.AssistantTranscriptStreamID != *reset.AssistantTranscriptStreamID {
		t.Fatalf("noop stream terminal facts = delta:%+v reset:%+v", delta, reset)
	}
}

func TestCompletedResponseExternalWorkflowCompletionDiscardsActiveStreamWithoutPersistence(t *testing.T) {
	t.Parallel()
	controller := &externallyCompletedWorkflowController{}
	step := scriptedllm.ToolBatch("", llm.ToolCall{
		ID:    "stale-call",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{}`),
	})
	step.StreamDeltas = []llm.AssistantDelta{{Text: "draft", Phase: llm.MessagePhaseCommentary}}
	step.AfterResponse = func(context.Context) error {
		controller.completed.Store(true)
		return nil
	}
	var events []Event
	scopeID := runtimeids.NewExecutionScopeID()
	engine := mustNewExecTestEngine(
		t,
		mustCreateTestSession(t),
		scriptedllm.NewClient(scriptedllm.Script{Steps: []scriptedllm.Step{step}}),
		Config{
			Model: "gpt-5",
			CurrentNodeExecution: &workflowruntime.CurrentNodeExecutionConfig{
				ScopeID:        scopeID,
				Contract:       workflowruntime.CompletionContract{},
				CompletionMode: workflowruntime.CompletionModeShellCommand,
				Controller:     controller,
			},
			OnEvent: func(event Event) { events = append(events, event) },
		},
	)

	if _, err := engine.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("submit workflow turn: %v", err)
	}

	var delta, reset *Event
	for index := range events {
		event := &events[index]
		switch event.Kind {
		case EventAssistantDelta:
			if delta != nil {
				t.Fatalf("multiple assistant deltas: %+v", events)
			}
			delta = event
		case EventAssistantDeltaReset:
			if reset != nil {
				t.Fatalf("multiple assistant delta resets: %+v", events)
			}
			reset = event
		case EventToolCallStarted:
			t.Fatalf("externally completed workflow executed a stale tool call: %+v", event)
		}
	}
	if delta == nil || reset == nil ||
		delta.AssistantTranscriptStreamID == nil ||
		reset.AssistantTranscriptStreamID == nil ||
		*delta.AssistantTranscriptStreamID != *reset.AssistantTranscriptStreamID ||
		reset.AssistantStreamAbortReason != string(AssistantStreamAbortSuperseded) {
		t.Fatalf("stale stream disposal = delta:%+v reset:%+v", delta, reset)
	}

	window, err := mustMaterializeTestEventLog(t, engine.store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded workflow records: %v", err)
	}
	for _, record := range window.Records {
		switch payload := mustSessionEventPayload(record).(type) {
		case session.ToolCompletionRecord:
			if payload.CallID == "stale-call" {
				t.Fatalf("stale workflow response persisted a tool completion: %+v", payload)
			}
		case session.MessageRecord:
			message, restoreErr := llmMessageFromSessionRecord(payload)
			if restoreErr != nil {
				t.Fatalf("restore persisted message: %v", restoreErr)
			}
			for _, call := range message.ToolCalls {
				if call.ID == "stale-call" {
					t.Fatalf("stale workflow response persisted a tool call: %+v", message)
				}
			}
		}
	}
}

func TestWorkflowDurableCompletionBeforeModelTurnStopsWithoutRequest(t *testing.T) {
	t.Parallel()
	controller := &externallyCompletedWorkflowController{}
	controller.completed.Store(true)
	scopeID := runtimeids.NewExecutionScopeID()
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant},
	}}}
	engine := mustNewExecTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		Config{
			Model: "gpt-5",
			CurrentNodeExecution: &workflowruntime.CurrentNodeExecutionConfig{
				ScopeID:        scopeID,
				Contract:       workflowruntime.CompletionContract{},
				CompletionMode: workflowruntime.CompletionModeShellCommand,
				Controller:     controller,
			},
		},
	)

	if _, err := engine.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("submit workflow turn: %v", err)
	}
	if calls := len(client.calls); calls != 0 {
		t.Fatalf("durably completed workflow dispatched %d model requests", calls)
	}
	terminal := engine.WorkflowTerminalState()
	if !terminal.Completed ||
		terminal.Source != WorkflowCompletionSourceObserved ||
		terminal.CompletedAt.IsZero() {
		t.Fatalf("workflow terminal state after durable completion = %+v", terminal)
	}
}

func TestWorkflowDelayedDurableCompletionObservedBeforeNextModelTurn(t *testing.T) {
	t.Parallel()
	const callID = "workflow-delayed-completion-call"
	controller := &externallyCompletedWorkflowController{completeAfterObservations: 4}
	scopeID := runtimeids.NewExecutionScopeID()
	toolCall := llm.ToolCall{
		ID:    callID,
		Name:  string(toolspec.ToolExecCommand),
		Input: mustJSON(map[string]any{"cmd": "pwd"}),
	}
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:      llm.RoleAssistant,
			Phase:     textutil.Value(llm.MessagePhaseCommentary),
			Content:   textutil.Value("working"),
			ToolCalls: []llm.ToolCall{toolCall},
		},
		ToolCalls: []llm.ToolCall{toolCall},
		Usage:     llm.Usage{InputTokens: 100, WindowTokens: 2_000},
	}}}
	store := mustCreateTestSession(t)
	engine := mustNewExecTestEngine(
		t,
		store,
		client,
		Config{
			Model:        "gpt-5",
			EnabledTools: []toolspec.ID{toolspec.ToolExecCommand},
			CurrentNodeExecution: &workflowruntime.CurrentNodeExecutionConfig{
				ScopeID:        scopeID,
				Contract:       workflowruntime.CompletionContract{},
				CompletionMode: workflowruntime.CompletionModeShellCommand,
				Controller:     controller,
			},
		},
	)

	if _, err := engine.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("submit workflow turn: %v", err)
	}
	if calls := len(client.calls); calls != 1 {
		t.Fatalf("delayed workflow completion dispatched %d model requests, want 1", calls)
	}
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded workflow records: %v", err)
	}
	foundCompletion := false
	for _, record := range window.Records {
		completion, ok := mustSessionEventPayload(record).(session.ToolCompletionRecord)
		if !ok || completion.CallID != callID {
			continue
		}
		if completion.IsError || completion.Name != string(toolspec.ToolExecCommand) {
			t.Fatalf("workflow tool completion is an error: %+v", completion)
		}
		foundCompletion = true
	}
	if !foundCompletion {
		t.Fatalf("bounded workflow records contain no tool completion for %q", callID)
	}
	terminal := engine.WorkflowTerminalState()
	if !terminal.Completed ||
		terminal.Source != WorkflowCompletionSourceObserved {
		t.Fatalf("workflow terminal state after delayed completion = %+v", terminal)
	}
	if observations := controller.observations.Load(); observations < controller.completeAfterObservations {
		t.Fatalf(
			"completion observations = %d, want at least %d",
			observations,
			controller.completeAfterObservations,
		)
	}
}

func TestWorkflowInvalidCompletionFailClosedWhenConfiguredCapInvalid(t *testing.T) {
	t.Parallel()
	scopeID := runtimeids.NewExecutionScopeID()
	controller := &interruptingWorkflowProtocolViolationController{}
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Phase:   textutil.Value(llm.MessagePhaseFinal),
			Content: textutil.Value("invalid workflow completion"),
		},
	}}}
	engine := mustNewExecTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		Config{
			Model: "gpt-5",
			CurrentNodeExecution: &workflowruntime.CurrentNodeExecutionConfig{
				ScopeID:                      scopeID,
				Contract:                     workflowruntime.CompletionContract{},
				CompletionMode:               workflowruntime.CompletionModeTool,
				MaxInvalidCompletionAttempts: 0,
				UseAutomaticToolChoice:       true,
				Controller:                   controller,
			},
		},
	)

	if _, err := engine.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("submit workflow turn: %v", err)
	}
	if len(client.calls) != 1 || client.calls[0].ToolChoiceMode != llm.ToolChoiceModeAutomatic {
		t.Fatalf("workflow requests = %+v, want one automatic-tool request", client.calls)
	}
	if len(controller.violations) != 1 {
		t.Fatalf("workflow protocol violations = %+v", controller.violations)
	}
	violation := controller.violations[0]
	if violation.Kind != workflowruntime.ViolationKindInvalidCompletion ||
		violation.MaxCount != 1 ||
		violation.SessionID == nil ||
		violation.SessionID.String() != engine.SessionID() {
		t.Fatalf("workflow protocol violation = %+v", violation)
	}
	if controller.result.Count != 1 || !controller.result.Interrupted {
		t.Fatalf("workflow protocol violation result = %+v", controller.result)
	}
}

func TestWorkflowCompletionControllerFailureUsesInvalidCompletionCapWithoutTerminalState(t *testing.T) {
	t.Parallel()
	scopeID := runtimeids.NewExecutionScopeID()
	controller := &failingWorkflowCompletionController{}
	completionResponse := llm.Response{Assistant: llm.Message{
		Role:    llm.RoleAssistant,
		Phase:   textutil.Value(llm.MessagePhaseFinal),
		Content: textutil.Value(`{"commentary":"complete","summary":"done"}`),
	}}
	engine := mustNewExecTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{responses: []llm.Response{
			completionResponse,
			completionResponse,
			completionResponse,
		}},
		Config{
			Model: "gpt-5",
			CurrentNodeExecution: &workflowruntime.CurrentNodeExecutionConfig{
				ScopeID: scopeID,
				Contract: workflowruntime.CompletionContract{
					Transitions: []workflowruntime.CompletionTransition{{
						ID:         "done",
						Parameters: []workflow.Parameter{{Key: "summary"}},
					}},
				},
				CompletionMode:               workflowruntime.CompletionModeUnstructuredOutput,
				MaxInvalidCompletionAttempts: 2,
				Controller:                   controller,
			},
		},
	)

	if _, err := engine.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("submit workflow turn: %v", err)
	}
	if len(controller.violationRequests) != 2 || len(controller.violationResults) != 2 {
		t.Fatalf(
			"workflow protocol violations = requests:%+v results:%+v",
			controller.violationRequests,
			controller.violationResults,
		)
	}
	for _, request := range controller.violationRequests {
		if request.Kind != workflowruntime.ViolationKindInvalidCompletion ||
			request.MaxCount != 2 {
			t.Fatalf("workflow protocol violation request = %+v", request)
		}
	}
	if first := controller.violationResults[0]; first.Count != 1 || first.Interrupted {
		t.Fatalf("first workflow violation result = %+v", first)
	}
	if second := controller.violationResults[1]; second.Count != 2 || !second.Interrupted {
		t.Fatalf("second workflow violation result = %+v", second)
	}
	if terminal := engine.WorkflowTerminalState(); terminal.Completed {
		t.Fatalf("workflow terminal state after controller failures = %+v", terminal)
	}
}

func TestWorkflowObservedDurableCompletionFailsQueuedSteeringDuringCloseDrain(t *testing.T) {
	t.Parallel()
	scopeID := runtimeids.NewExecutionScopeID()
	controller := &externallyCompletedWorkflowController{}
	controller.completed.Store(true)
	var statuses []QueuedUserMessageStatusEvent
	client := &fakeClient{}
	engine := mustNewExecTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		Config{
			Model: "gpt-5",
			CurrentNodeExecution: &workflowruntime.CurrentNodeExecutionConfig{
				ScopeID:        scopeID,
				Contract:       workflowruntime.CompletionContract{},
				CompletionMode: workflowruntime.CompletionModeShellCommand,
				Controller:     controller,
			},
			OnEvent: func(event Event) {
				if event.QueuedUserMessageStatus != nil {
					statuses = append(statuses, *event.QueuedUserMessageStatus)
				}
			},
		},
	)

	queued := engine.QueueUserMessageWithClientRequestID("queued user input", "request-id")
	completed, err := engine.observeWorkflowDurableCompletion(context.Background())
	if err != nil {
		t.Fatalf("observe durable workflow completion: %v", err)
	}
	if !completed {
		t.Fatal("durable workflow completion was not observed")
	}
	if err := engine.DrainQueuedUserMessagesBeforeClose(context.Background()); err != nil {
		t.Fatalf("drain queued user messages before close: %v", err)
	}
	if calls := len(client.calls); calls != 0 {
		t.Fatalf("terminal workflow completion dispatched %d model requests", calls)
	}
	if engine.HasQueuedUserWork() {
		t.Fatal("terminal workflow completion retained queued user work")
	}
	if len(statuses) != 2 {
		t.Fatalf("queued user statuses = %+v", statuses)
	}
	if accepted := statuses[0]; accepted.Status != QueuedUserMessageAccepted ||
		accepted.QueueItemID != queued.ID ||
		accepted.ClientRequestID != queued.ClientRequestID {
		t.Fatalf("accepted queue status = %+v", accepted)
	}
	if failed := statuses[1]; failed.Status != QueuedUserMessageFailed ||
		failed.QueueItemID != queued.ID ||
		failed.ClientRequestID != queued.ClientRequestID ||
		failed.FailureReason != QueuedUserMessageFailureTerminalWorkflowCompletion {
		t.Fatalf("terminal workflow queue failure = %+v", failed)
	}
}

func TestCompletedResponseFinalizationUsesActiveSegmentCoordinatesAfterCompaction(t *testing.T) {
	t.Parallel()
	first := scriptedllm.FinalAnswer("first")
	first.StreamDeltas = []llm.AssistantDelta{{Text: "first", Phase: llm.MessagePhaseFinal}}
	second := scriptedllm.FinalAnswer("second")
	second.StreamDeltas = []llm.AssistantDelta{{Text: "second", Phase: llm.MessagePhaseFinal}}

	var events []Event
	engine := mustNewExecTestEngine(
		t,
		mustCreateTestSession(t),
		scriptedllm.NewClient(scriptedllm.Script{Steps: []scriptedllm.Step{first, second}}),
		Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		},
	)

	if _, err := engine.SubmitUserMessage(context.Background(), "first input"); err != nil {
		t.Fatalf("submit pre-compaction message: %v", err)
	}
	if err := engine.steer(
		"compaction",
		steerHistoryReplacementIntent("local", compactionModeAuto, 1, "", "", nil),
	); err != nil {
		t.Fatalf("persist history replacement: %v", err)
	}
	activeSegmentStart := engine.CommittedTranscriptEntryCount()
	events = nil

	if _, err := engine.SubmitUserMessage(context.Background(), "second input"); err != nil {
		t.Fatalf("submit post-compaction message: %v", err)
	}

	var delta, finalized *Event
	for index := range events {
		event := &events[index]
		switch event.Kind {
		case EventAssistantDelta:
			if delta != nil {
				t.Fatalf("multiple post-compaction assistant deltas: %+v", events)
			}
			delta = event
		case EventAssistantMessage:
			if finalized != nil {
				t.Fatalf("multiple post-compaction finalized assistant messages: %+v", events)
			}
			finalized = event
		}
	}
	if delta == nil || finalized == nil ||
		delta.AssistantTranscriptStreamID == nil ||
		finalized.AssistantTranscriptStreamID == nil ||
		delta.AssistantStreamMetadata == nil ||
		!finalized.CommittedEntryStartSet {
		t.Fatalf("post-compaction stream finalization events = %+v", events)
	}
	if delta.AssistantStreamMetadata.BaseCommittedEntryCount != finalized.CommittedEntryStart ||
		finalized.CommittedEntryStart < activeSegmentStart ||
		*delta.AssistantTranscriptStreamID != *finalized.AssistantTranscriptStreamID {
		t.Fatalf(
			"post-compaction stream coordinates = delta:%+v finalized:%+v active_segment_start=%d",
			delta,
			finalized,
			activeSegmentStart,
		)
	}

	var hydratedAssistant *TranscriptCommittedRowFact
	if err := engine.WithTranscriptHydrationSnapshot(func(snapshot TranscriptHydrationSnapshot) error {
		for index := range snapshot.CommittedRows {
			row := &snapshot.CommittedRows[index]
			if row.Kind != TranscriptCommittedRowFactAssistant {
				continue
			}
			if hydratedAssistant != nil {
				t.Fatalf("multiple hydrated assistant rows after compaction: %+v", snapshot.CommittedRows)
			}
			hydratedAssistant = row
		}
		return nil
	}); err != nil {
		t.Fatalf("hydrate active segment: %v", err)
	}
	if hydratedAssistant == nil ||
		hydratedAssistant.Assistant == nil ||
		hydratedAssistant.Assistant.StreamID == nil ||
		*hydratedAssistant.Assistant.StreamID != *finalized.AssistantTranscriptStreamID {
		t.Fatalf(
			"hydrated post-compaction assistant = %+v, finalized stream=%+v",
			hydratedAssistant,
			finalized.AssistantTranscriptStreamID,
		)
	}
}

type externallyCompletedWorkflowController struct {
	completed                 atomic.Bool
	observations              atomic.Int32
	completeAfterObservations int32
}

type interruptingWorkflowProtocolViolationController struct {
	externallyCompletedWorkflowController
	violations []workflowruntime.ViolationRequest
	result     workflowruntime.ViolationResult
}

func (c *interruptingWorkflowProtocolViolationController) RecordProtocolViolation(
	_ context.Context,
	request workflowruntime.ViolationRequest,
) (workflowruntime.ViolationResult, error) {
	c.violations = append(c.violations, request)
	c.result = workflowruntime.ViolationResult{Count: 1, Interrupted: true}
	return c.result, nil
}

type failingWorkflowCompletionController struct {
	externallyCompletedWorkflowController
	violationRequests []workflowruntime.ViolationRequest
	violationResults  []workflowruntime.ViolationResult
}

func (c *failingWorkflowCompletionController) CompleteCurrentNode(
	context.Context,
	workflowruntime.CompletionRequest,
) (workflowruntime.CompletionResult, error) {
	return workflowruntime.CompletionResult{}, errors.New("workflow completion unavailable")
}

func (c *failingWorkflowCompletionController) RecordProtocolViolation(
	_ context.Context,
	request workflowruntime.ViolationRequest,
) (workflowruntime.ViolationResult, error) {
	c.violationRequests = append(c.violationRequests, request)
	result := workflowruntime.ViolationResult{
		Count:       int64(len(c.violationResults) + 1),
		Interrupted: len(c.violationResults)+1 >= request.MaxCount,
	}
	c.violationResults = append(c.violationResults, result)
	return result, nil
}

func (c *externallyCompletedWorkflowController) CompleteCurrentNode(
	context.Context,
	workflowruntime.CompletionRequest,
) (workflowruntime.CompletionResult, error) {
	return workflowruntime.CompletionResult{}, nil
}

func (c *externallyCompletedWorkflowController) RecordProtocolViolation(
	context.Context,
	workflowruntime.ViolationRequest,
) (workflowruntime.ViolationResult, error) {
	return workflowruntime.ViolationResult{}, nil
}

func (c *externallyCompletedWorkflowController) ResetProtocolViolationBudget(
	context.Context,
	workflowruntime.ViolationResetRequest,
) error {
	return nil
}

func (c *externallyCompletedWorkflowController) ObserveCurrentNodeCompletion(
	context.Context,
	workflowruntime.CompletionObservationRequest,
) (workflowruntime.CompletionObservationResult, error) {
	if count := c.observations.Add(1); c.completeAfterObservations > 0 && count >= c.completeAfterObservations {
		c.completed.Store(true)
	}
	return workflowruntime.CompletionObservationResult{Completed: c.completed.Load()}, nil
}
