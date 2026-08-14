package runtime

import (
	"context"
	"sync/atomic"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestBlankFinalStaysHiddenAndSkipsReviewer(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	mainClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Phase:   textutil.Value(llm.MessagePhaseFinal),
			Content: textutil.Value(""),
		},
		Usage: llm.Usage{WindowTokens: 200_000},
	}}}
	reviewerClient := &fakeClient{}
	var (
		assistantFinalPublications atomic.Int32
		reviewerStarts             atomic.Int32
	)
	engine := mustNewTestEngine(
		t,
		store,
		mainClient,
		tools.NewRegistry(),
		Config{
			Model: "gpt-5",
			Reviewer: ReviewerConfig{
				Frequency: "all",
				Model:     "gpt-5",
				Client:    reviewerClient,
			},
			OnEvent: func(event Event) {
				switch event.Kind {
				case EventAssistantMessage:
					if event.Message.Role == llm.RoleAssistant &&
						event.Message.Phase != nil &&
						*event.Message.Phase == llm.MessagePhaseFinal {
						assistantFinalPublications.Add(1)
					}
				case EventReviewerStarted:
					reviewerStarts.Add(1)
				}
			},
		},
	)

	message, err := engine.SubmitUserMessage(context.Background(), "turn")
	if err != nil {
		t.Fatalf("submit user turn: %v", err)
	}
	if message.Content != nil {
		t.Fatalf("blank final returned assistant content")
	}
	if calls := len(mainClient.calls); calls != 1 {
		t.Fatalf("main provider dispatches = %d, want one", calls)
	}
	if calls := len(reviewerClient.calls); calls != 0 {
		t.Fatalf("reviewer provider dispatches = %d, want zero", calls)
	}
	if starts := reviewerStarts.Load(); starts != 0 {
		t.Fatalf("reviewer starts = %d, want zero", starts)
	}
	if publications := assistantFinalPublications.Load(); publications != 0 {
		t.Fatalf("assistant final publications = %d, want zero", publications)
	}

	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(8)
	if err != nil {
		t.Fatalf("read bounded blank-final records: %v", err)
	}
	finalRecords := 0
	noopFinalRecords := 0
	for _, record := range window.Records {
		messageRecord, ok := mustSessionEventPayload(record).(session.MessageRecord)
		if !ok {
			continue
		}
		persisted, restoreErr := llmMessageFromSessionRecord(messageRecord)
		if restoreErr != nil {
			t.Fatalf("restore persisted message: %v", restoreErr)
		}
		if persisted.Role != llm.RoleAssistant ||
			persisted.Phase == nil ||
			*persisted.Phase != llm.MessagePhaseFinal {
			continue
		}
		finalRecords++
		if isBlankFinalAnswer(persisted) {
			noopFinalRecords++
		}
	}
	if finalRecords != 1 || noopFinalRecords != 1 {
		t.Fatalf("persisted final records = %d blank finals = %d, want one blank final", finalRecords, noopFinalRecords)
	}
}

func TestBlankFinalWhitespaceStaysHiddenAndSkipsReviewer(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	mainClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Phase:   textutil.Value(llm.MessagePhaseFinal),
			Content: textutil.Value(" \n\t "),
		},
		Usage: llm.Usage{WindowTokens: 200_000},
	}}}
	reviewerClient := &fakeClient{}
	engine := mustNewTestEngine(
		t,
		store,
		mainClient,
		tools.NewRegistry(),
		Config{
			Model: "gpt-5",
			Reviewer: ReviewerConfig{
				Frequency: "all",
				Model:     "gpt-5",
				Client:    reviewerClient,
			},
		},
	)

	message, err := engine.SubmitUserMessage(context.Background(), "turn")
	if err != nil {
		t.Fatalf("submit user turn: %v", err)
	}
	if message.Content != nil {
		t.Fatalf("whitespace blank final returned assistant content")
	}
	if len(reviewerClient.calls) != 0 {
		t.Fatalf("reviewer provider dispatches = %d, want zero", len(reviewerClient.calls))
	}
}

func TestBlankFinalWithAcceptedToolCallsFailsBeforeExecution(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	var toolStarts atomic.Int32
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Phase:   textutil.Value(llm.MessagePhaseFinal),
			Content: textutil.Value(""),
		},
		ToolCalls: []llm.ToolCall{{
			ID:     "call-patch",
			Name:   string(toolspec.ToolPatch),
			Custom: true,
			CustomInput: textutil.Value(
				"*** Begin Patch\n*** Add File: should-not-exist.txt\n+not executed\n*** End Patch",
			),
		}},
		Usage: llm.Usage{WindowTokens: 200_000},
	}}}
	registry := tools.NewRegistry()
	if err := registry.ReplaceHandlers(tools.HandlerRegistration{
		ID:      toolspec.ToolPatch,
		Handler: fakeTool{name: toolspec.ToolPatch},
	}); err != nil {
		t.Fatalf("register patch tool: %v", err)
	}
	engine := mustNewTestEngine(
		t,
		store,
		client,
		registry,
		Config{
			Model:        "gpt-5",
			EnabledTools: []toolspec.ID{toolspec.ToolPatch},
			OnEvent: func(event Event) {
				if event.Kind == EventToolCallStarted {
					toolStarts.Add(1)
				}
			},
		},
	)

	if _, err := engine.SubmitUserMessage(context.Background(), "turn"); err == nil {
		t.Fatal("blank final with accepted tool call unexpectedly succeeded")
	}
	if starts := toolStarts.Load(); starts != 0 {
		t.Fatalf("tool starts = %d, want zero", starts)
	}
}

func TestFormerMarkerIsOrdinaryFinal(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{finalTextResponse("NO_OP")}}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})

	message, err := engine.SubmitUserMessage(context.Background(), "turn")
	if err != nil {
		t.Fatalf("submit user turn: %v", err)
	}
	if message.Content == nil || *message.Content != "NO_OP" {
		t.Fatalf("former marker result = %#v, want ordinary assistant text", message.Content)
	}
}
