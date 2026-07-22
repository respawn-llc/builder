package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestNewConsumesPendingModelRecoveryOnReopen(t *testing.T) {
	const stepID = "interrupted-step"

	store := mustCreateTestSession(t)
	mustAppendTestEvent(t, store, stepID, llm.Message{
		Role:    llm.RoleUser,
		Content: textutil.Value("interrupted input"),
	})
	if err := store.SetPendingModelRecovery(session.PendingModelRecovery{
		RecoveryID: "recovery-1",
		StepID:     stepID,
		Reason:     "provider_visible_output_persisted",
		CreatedAt:  time.Unix(0, 0).UTC(),
	}); err != nil {
		t.Fatalf("set pending recovery: %v", err)
	}

	reopened := mustOpenTestSession(t, store.Dir())
	_ = mustNewTestEngine(t, reopened, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})

	if reopened.Meta().PendingModelRecovery != nil {
		t.Fatal("reopened runtime retained pending model recovery")
	}

	window, err := mustMaterializeTestEventLog(t, reopened).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded recovery records: %v", err)
	}
	for _, record := range window.Records {
		message, ok := mustSessionEventPayload(record).(session.MessageRecord)
		if ok &&
			message.Role == session.MessageRoleDeveloper &&
			message.MessageType != nil &&
			*message.MessageType == session.MessageTypeInterruption {
			return
		}
	}
	t.Fatalf("bounded recovery records contain no durable interruption marker: %+v", window.Records)
}

func TestNewPublishesRecoveredDanglingToolStartOnReopen(t *testing.T) {
	const (
		stepID = "interrupted-tool-step"
		callID = "interrupted-tool-call"
	)

	store := mustCreateTestSession(t)
	mustAppendTestEvent(t, store, stepID, llm.Message{
		Role: llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{
			ID:    callID,
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{}`),
		}},
	})
	if err := store.SetPendingModelRecovery(session.PendingModelRecovery{
		RecoveryID:             "recovery-2",
		StepID:                 stepID,
		Reason:                 "provider_visible_output_persisted",
		CreatedAt:              time.Unix(0, 0).UTC(),
		OutstandingToolCallIDs: []string{callID},
	}); err != nil {
		t.Fatalf("set pending recovery: %v", err)
	}

	reopened := mustOpenTestSession(t, store.Dir())
	var events []Event
	_ = mustNewTestEngine(t, reopened, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			events = append(events, event)
		},
	})

	for _, event := range events {
		if event.Kind == EventToolCallStarted &&
			event.StepID == stepID &&
			event.ToolCall != nil &&
			event.ToolCall.ID == callID &&
			event.ToolCall.Name == string(toolspec.ToolExecCommand) {
			return
		}
	}
	t.Fatalf("reopen events contain no recovered tool start: %+v", events)
}

func TestReopenCarriesInterruptedAskQuestionToolAttemptIntoNextModelRequest(t *testing.T) {
	const (
		stepID = "interrupted-question-step"
		callID = "interrupted-question-call"
	)

	store := mustCreateTestSession(t)
	mustAppendTestEvent(t, store, stepID, llm.Message{
		Role:    llm.RoleUser,
		Content: textutil.Value("input"),
	})
	mustAppendTestEvent(t, store, stepID, llm.Message{
		Role: llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{
			ID:    callID,
			Name:  string(toolspec.ToolAskQuestion),
			Input: json.RawMessage(`{"question":"continue?"}`),
		}},
	})
	if err := store.SetPendingModelRecovery(session.PendingModelRecovery{
		RecoveryID: "recovery-3",
		StepID:     stepID,
		Reason:     "provider_visible_output_persisted",
		CreatedAt:  time.Unix(0, 0).UTC(),
	}); err != nil {
		t.Fatalf("set pending recovery: %v", err)
	}

	reopened := mustOpenTestSession(t, store.Dir())
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("resumed"),
			Phase:   textutil.Value(llm.MessagePhaseFinal),
		},
	}}}
	engine := mustNewTestEngine(t, reopened, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	if _, err := engine.SubmitUserMessage(context.Background(), "continue"); err != nil {
		t.Fatalf("submit after reopen: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("resumed model requests = %d, want one", len(client.calls))
	}

	foundCall := false
	foundOutput := false
	for _, item := range client.calls[0].Items {
		if item.CallID == nil || *item.CallID != callID {
			continue
		}
		if item.Type == llm.ResponseItemTypeFunctionCall &&
			item.Name != nil &&
			*item.Name == string(toolspec.ToolAskQuestion) {
			foundCall = true
		}
		if item.Type == llm.ResponseItemTypeFunctionCallOutput {
			foundOutput = true
		}
	}
	if !foundCall || foundOutput {
		t.Fatalf("resumed ask-question call preservation = call:%t output:%t", foundCall, foundOutput)
	}
}
