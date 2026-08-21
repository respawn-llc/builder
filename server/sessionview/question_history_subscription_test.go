package sessionview

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"core/server/session"
	"core/shared/serverapi"
	"core/shared/transcript"
)

func TestQuestionHistorySubscriptionProjectsNewestAnsweredQuestions(t *testing.T) {
	store := newSessionViewStore(t, t.TempDir(), "ws", t.TempDir())
	appendSessionViewRecord(t, store, "step-1", questionCompletion(
		t,
		"older",
		[]string{"first", "second"},
		&session.QuestionAnswerRecord{SelectedOptionNumber: sessionViewIntPointer(2), Freeform: sessionViewStringPointer("comment")},
	))
	appendSessionViewHistoryReplacement(t, store, "step-2", session.HistoryReplacementRecord{
		Engine: "local",
		Mode:   session.CompactionModeHandoff,
		Items: []session.ProviderHistoryItem{{
			Type: session.ProviderHistoryItemTypeOther,
			Raw:  json.RawMessage(`{"type":"function_call_output","output":"copied provider history"}`),
		}},
	})
	appendSessionViewRecord(t, store, "step-3", questionCompletion(
		t,
		"newer",
		nil,
		&session.QuestionAnswerRecord{Freeform: sessionViewStringPointer("freeform")},
	))

	sub, err := NewService(newTestSessionResolver(store), nil, nil, nil).
		SubscribeQuestionHistory(t.Context(), serverapi.QuestionHistorySubscribeRequest{
			SessionID: store.Meta().SessionID, MaxHandoffs: 2,
		})
	if err != nil {
		t.Fatalf("subscribe Question history: %v", err)
	}
	defer sub.Close()
	started := nextQuestionHistoryEvent(t, sub)
	if started.Kind != serverapi.QuestionHistoryEventStarted ||
		started.LargeHistory == nil ||
		*started.LargeHistory {
		t.Fatalf("started event = %#v", started)
	}
	newer := nextQuestionHistoryEvent(t, sub)
	if newer.Question == nil ||
		newer.Question.Question != "newer" ||
		newer.Question.Answer != "freeform" ||
		newer.Question.SelectedOptionNumber != nil ||
		newer.Question.Commentary != nil ||
		newer.Question.At == nil {
		t.Fatalf("newer Question = %#v", newer)
	}
	older := nextQuestionHistoryEvent(t, sub)
	if older.Question == nil ||
		older.Question.Question != "older" ||
		older.Question.Answer != "second" ||
		older.Question.SelectedOptionNumber == nil ||
		*older.Question.SelectedOptionNumber != 2 ||
		older.Question.Commentary == nil ||
		*older.Question.Commentary != "comment" ||
		older.Question.At == nil {
		t.Fatalf("older Question = %#v", older)
	}
	completed := nextQuestionHistoryEvent(t, sub)
	if completed.Kind != serverapi.QuestionHistoryEventCompleted ||
		completed.HistoryOmitted == nil ||
		*completed.HistoryOmitted {
		t.Fatalf("completed event = %#v", completed)
	}
	if _, err := sub.Next(t.Context()); err != io.EOF {
		t.Fatalf("terminal Next error = %v, want EOF", err)
	}
}

func TestQuestionHistorySubscriptionOmissionEmptyAndCancellation(t *testing.T) {
	store := newSessionViewStore(t, t.TempDir(), "ws", t.TempDir())
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("persist Session: %v", err)
	}
	if _, err := store.MaterializeEventLog(); err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	sub, err := NewService(newTestSessionResolver(store), nil, nil, nil).
		SubscribeQuestionHistory(t.Context(), serverapi.QuestionHistorySubscribeRequest{
			SessionID: store.Meta().SessionID, MaxHandoffs: 1,
		})
	if err != nil {
		t.Fatalf("subscribe empty history: %v", err)
	}
	_ = nextQuestionHistoryEvent(t, sub)
	completed := nextQuestionHistoryEvent(t, sub)
	if completed.HistoryOmitted == nil || *completed.HistoryOmitted {
		t.Fatalf("empty completion = %#v", completed)
	}
	if err := sub.Close(); err != nil {
		t.Fatalf("close empty subscription: %v", err)
	}

	sub, err = NewService(newTestSessionResolver(store), nil, nil, nil).
		SubscribeQuestionHistory(t.Context(), serverapi.QuestionHistorySubscribeRequest{
			SessionID: store.Meta().SessionID, MaxHandoffs: 1,
		})
	if err != nil {
		t.Fatalf("subscribe for cancellation: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := sub.Next(ctx); err == nil {
		t.Fatal("canceled subscription Next succeeded")
	}
	_ = sub.Close()
}

func TestQuestionHistorySubscriptionUsesOnlyPersistedResolver(t *testing.T) {
	store := newSessionViewStore(t, t.TempDir(), "ws", t.TempDir())
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("persist Session: %v", err)
	}
	if _, err := store.MaterializeEventLog(); err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	resolver := persistedOnlySessionResolver{record: session.PersistedSessionRecord{
		SessionDir: store.Dir(),
		Meta:       sessionViewMetaPointer(store.Meta()),
	}}
	sub, err := NewService(resolver, nil, nil, nil).
		SubscribeQuestionHistory(t.Context(), serverapi.QuestionHistorySubscribeRequest{
			SessionID: store.Meta().SessionID, MaxHandoffs: 1,
		})
	if err != nil {
		t.Fatalf("subscribe with persisted-only resolver: %v", err)
	}
	defer sub.Close()
	_ = nextQuestionHistoryEvent(t, sub)
}

func TestQuestionHistorySubscriptionPreservesMultilinePresentedText(t *testing.T) {
	answers := []*session.QuestionAnswerRecord{
		{
			SelectedOptionNumber: sessionViewIntPointer(1),
			Freeform:             sessionViewStringPointer("comment\nline"),
		},
		{Freeform: sessionViewStringPointer("freeform\nanswer")},
	}
	for _, answer := range answers {
		record, err := session.NewEventRecord(
			1,
			nil,
			session.ToolCompletionRecord{
				CallID:     "call",
				Name:       "ask_question",
				OutputKind: session.ToolOutputKindFunction,
				Output:     json.RawMessage(`"flattened"`),
				Presentation: transcript.EncodeToolCallMeta(transcript.ToolCallMeta{
					ToolName:    "ask_question",
					Question:    "  question\nbody  ",
					Suggestions: []string{"  first\nline  "},
				}),
				QuestionAnswer: answer,
			},
		)
		if err != nil {
			t.Fatalf("create multiline Question: %v", err)
		}
		projected, err := projectQuestionHistoryRecord(record, session.EventLogVersionV2)
		if err == nil {
			t.Fatal("untimestamped v2 projection unexpectedly succeeded")
		}
		_ = projected
	}
}

func TestQuestionHistorySubscriptionChecksCancellationWhileSkippingRecords(t *testing.T) {
	store := newSessionViewStore(t, t.TempDir(), "ws", t.TempDir())
	appendSessionViewRecord(t, store, "step-1", session.ToolCompletionRecord{
		CallID:     "call-ignored",
		Name:       "ask_question",
		OutputKind: session.ToolOutputKindFunction,
		Output:     json.RawMessage(`"flattened"`),
		QuestionAnswer: &session.QuestionAnswerRecord{
			Freeform: sessionViewStringPointer("ignored"),
		},
	})
	sub, err := NewService(newTestSessionResolver(store), nil, nil, nil).
		SubscribeQuestionHistory(t.Context(), serverapi.QuestionHistorySubscribeRequest{
			SessionID: store.Meta().SessionID, MaxHandoffs: 1,
		})
	if err != nil {
		t.Fatalf("subscribe Question history: %v", err)
	}
	defer func() { _ = sub.Close() }()
	_ = nextQuestionHistoryEvent(t, sub)
	ctx := &cancelAfterSkippedRecordContext{Context: t.Context()}
	if _, err := sub.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Next after skipped record error = %v, want context canceled", err)
	}
	if ctx.errCalls != cancelAfterSkippedRecordErrCall {
		t.Fatalf(
			"context error checks = %d, want cancellation after skipped record at %d",
			ctx.errCalls,
			cancelAfterSkippedRecordErrCall,
		)
	}
}

func TestQuestionHistorySubscriptionPullsThroughLargeSingleWindow(t *testing.T) {
	store := newSessionViewStore(t, t.TempDir(), "ws", t.TempDir())
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("persist Session: %v", err)
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	const malformedCount = 2048
	payloads := make([]session.EventRecordPayload, 0, malformedCount+1)
	payloads = append(payloads, questionCompletion(
		t,
		"valid",
		nil,
		&session.QuestionAnswerRecord{Freeform: sessionViewStringPointer("answer")},
	))
	for index := 0; index < malformedCount; index++ {
		payloads = append(payloads, session.ToolCompletionRecord{
			CallID:       "call-malformed",
			Name:         "ask_question",
			OutputKind:   session.ToolOutputKindFunction,
			Output:       json.RawMessage(`"flattened"`),
			Presentation: transcript.EncodeToolCallMeta(transcript.ToolCallMeta{ToolName: "ask_question"}),
			QuestionAnswer: &session.QuestionAnswerRecord{
				Freeform: sessionViewStringPointer("ignored"),
			},
		})
	}
	if _, receipt, err := eventLog.AppendRecordsAtomic(nil, payloads); err != nil || !receipt.Committed {
		t.Fatalf("append large single-window fixture: receipt=%+v error=%v", receipt, err)
	}

	sub, err := NewService(newTestSessionResolver(store), nil, nil, nil).
		SubscribeQuestionHistory(t.Context(), serverapi.QuestionHistorySubscribeRequest{
			SessionID: store.Meta().SessionID, MaxHandoffs: 1,
		})
	if err != nil {
		t.Fatalf("subscribe Question history: %v", err)
	}
	defer sub.Close()
	_ = nextQuestionHistoryEvent(t, sub)
	question := nextQuestionHistoryEvent(t, sub)
	if question.Question == nil || question.Question.Question != "valid" {
		t.Fatalf("projected Question = %#v", question)
	}
	completed := nextQuestionHistoryEvent(t, sub)
	if completed.Kind != serverapi.QuestionHistoryEventCompleted {
		t.Fatalf("completed event = %#v", completed)
	}
}

type persistedOnlySessionResolver struct {
	record session.PersistedSessionRecord
}

func (r persistedOnlySessionResolver) ResolveSessionStore(context.Context, string) (*session.Store, error) {
	panic("Question history must not resolve a Session Store")
}

func (r persistedOnlySessionResolver) ResolvePersistedSession(context.Context, string) (session.PersistedSessionRecord, error) {
	return r.record, nil
}

func sessionViewMetaPointer(meta session.Meta) *session.Meta {
	return &meta
}

func questionCompletion(
	t *testing.T,
	question string,
	suggestions []string,
	answer *session.QuestionAnswerRecord,
) session.ToolCompletionRecord {
	t.Helper()
	return session.ToolCompletionRecord{
		CallID:     "call-" + question,
		Name:       "ask_question",
		OutputKind: session.ToolOutputKindFunction,
		Output:     json.RawMessage(`"flattened"`),
		Presentation: transcript.EncodeToolCallMeta(transcript.ToolCallMeta{
			ToolName:    "ask_question",
			Question:    question,
			Suggestions: suggestions,
		}),
		QuestionAnswer: answer,
	}
}

func nextQuestionHistoryEvent(
	t *testing.T,
	sub serverapi.QuestionHistorySubscription,
) serverapi.QuestionHistoryEvent {
	t.Helper()
	event, err := sub.Next(t.Context())
	if err != nil {
		t.Fatalf("Next Question-history event: %v", err)
	}
	return event
}

type cancelAfterSkippedRecordContext struct {
	context.Context
	errCalls int
}

// The first thirteen checks span subscription entry and reading/projecting the
// malformed candidate. The next check is the subscription boundary before it
// asks the cursor for another record.
const cancelAfterSkippedRecordErrCall = 14

func (c *cancelAfterSkippedRecordContext) Err() error {
	c.errCalls++
	if c.errCalls >= cancelAfterSkippedRecordErrCall {
		return context.Canceled
	}
	return nil
}
