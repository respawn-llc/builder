package runtime

import (
	"context"
	"sync/atomic"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/transcript"
)

func TestNoopFinalStaysHiddenAndSkipsReviewer(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	mainClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Phase:   textutil.Value(llm.MessagePhaseFinal),
			Content: textutil.Value(transcript.NoopFinalToken),
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
		t.Fatalf("noop final returned assistant content")
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
		t.Fatalf("read bounded noop-final records: %v", err)
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
		if isNoopFinalAnswer(persisted) {
			noopFinalRecords++
		}
	}
	if finalRecords != 1 || noopFinalRecords != 1 {
		t.Fatalf("persisted final records = %d noop finals = %d, want one noop final", finalRecords, noopFinalRecords)
	}
}
