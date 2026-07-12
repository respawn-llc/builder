package runtime

import (
	"errors"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/shared/transcript"
)

func appendFinalAnswerTestEvent(t *testing.T, store *session.Store, kind string, payload any) {
	t.Helper()
	if _, _, err := store.AppendEvent("step", kind, payload); err != nil {
		t.Fatalf("append %s: %v", kind, err)
	}
}

func TestLatestCommittedAssistantFinalAnswerReturnsNewestFinalByteForByte(t *testing.T) {
	store := mustCreateTestSession(t)
	appendFinalAnswerTestEvent(t, store, "message", llm.Message{
		Role:    llm.RoleAssistant,
		Phase:   llm.MessagePhaseFinal,
		Content: "  exact final answer\n",
	})

	answer, err := LatestCommittedAssistantFinalAnswerFromStore(store)
	if err != nil {
		t.Fatalf("lookup final answer: %v", err)
	}
	if answer == nil {
		t.Fatal("expected final answer")
	}
	if got, want := *answer, "  exact final answer\n"; got != want {
		t.Fatalf("answer = %q, want %q", got, want)
	}
}

func TestLatestCommittedAssistantFinalAnswerSkipsLaterNonCandidatesAndNoopFinals(t *testing.T) {
	store := mustCreateTestSession(t)
	appendFinalAnswerTestEvent(t, store, "message", llm.Message{
		Role:    llm.RoleAssistant,
		Phase:   llm.MessagePhaseFinal,
		Content: "committed answer",
	})
	appendFinalAnswerTestEvent(t, store, "message", llm.Message{Role: llm.RoleUser, Content: "next task"})
	appendFinalAnswerTestEvent(t, store, "message", llm.Message{
		Role:    llm.RoleAssistant,
		Phase:   llm.MessagePhaseCommentary,
		Content: "streaming-style persisted commentary",
	})
	appendFinalAnswerTestEvent(t, store, "message", llm.Message{
		Role:    llm.RoleAssistant,
		Phase:   llm.MessagePhaseFinal,
		Content: transcript.NoopFinalToken,
	})

	answer, err := LatestCommittedAssistantFinalAnswerFromStore(store)
	if err != nil {
		t.Fatalf("lookup final answer: %v", err)
	}
	if answer == nil || *answer != "committed answer" {
		t.Fatalf("answer = %v, want committed answer", answer)
	}
}

func TestLatestCommittedAssistantFinalAnswerCompactionBoundaryReturnsAbsence(t *testing.T) {
	store := mustCreateTestSession(t)
	appendFinalAnswerTestEvent(t, store, "message", llm.Message{
		Role:    llm.RoleAssistant,
		Phase:   llm.MessagePhaseFinal,
		Content: "pre-compaction answer",
	})
	appendFinalAnswerTestEvent(t, store, "history_replaced", historyReplacementPayload{
		Engine:                            "compaction",
		LastCommittedAssistantFinalAnswer: "carried pre-compaction answer",
	})

	answer, err := LatestCommittedAssistantFinalAnswerFromStore(store)
	if err != nil {
		t.Fatalf("lookup final answer: %v", err)
	}
	if answer != nil {
		t.Fatalf("answer = %q, want absence at compaction boundary", *answer)
	}
}

func TestLatestCommittedAssistantFinalAnswerReturnsPostCompactionAnswer(t *testing.T) {
	store := mustCreateTestSession(t)
	appendFinalAnswerTestEvent(t, store, "message", llm.Message{
		Role:    llm.RoleAssistant,
		Phase:   llm.MessagePhaseFinal,
		Content: "pre-compaction answer",
	})
	appendFinalAnswerTestEvent(t, store, "history_replaced", historyReplacementPayload{Engine: "compaction"})
	appendFinalAnswerTestEvent(t, store, "message", llm.Message{
		Role:    llm.RoleAssistant,
		Phase:   llm.MessagePhaseFinal,
		Content: "post-compaction answer",
	})

	answer, err := LatestCommittedAssistantFinalAnswerFromStore(store)
	if err != nil {
		t.Fatalf("lookup final answer: %v", err)
	}
	if answer == nil || *answer != "post-compaction answer" {
		t.Fatalf("answer = %v, want post-compaction answer", answer)
	}
}

func TestLatestCommittedAssistantFinalAnswerCrossesLegacyReviewerRollback(t *testing.T) {
	store := mustCreateTestSession(t)
	appendFinalAnswerTestEvent(t, store, "message", llm.Message{
		Role:    llm.RoleAssistant,
		Phase:   llm.MessagePhaseFinal,
		Content: "answer before rollback",
	})
	appendFinalAnswerTestEvent(t, store, "history_replaced", historyReplacementPayload{
		Engine: legacyHistoryReplacementEngineReviewerRollback,
		Mode:   "manual",
	})

	answer, err := LatestCommittedAssistantFinalAnswerFromStore(store)
	if err != nil {
		t.Fatalf("lookup final answer: %v", err)
	}
	if answer == nil || *answer != "answer before rollback" {
		t.Fatalf("answer = %v, want answer before rollback", answer)
	}
}

func TestLatestCommittedAssistantFinalAnswerReturnsAbsenceWithoutFinal(t *testing.T) {
	store := mustCreateTestSession(t)
	appendFinalAnswerTestEvent(t, store, "message", llm.Message{Role: llm.RoleUser, Content: "task"})

	answer, err := LatestCommittedAssistantFinalAnswerFromStore(store)
	if err != nil {
		t.Fatalf("lookup final answer: %v", err)
	}
	if answer != nil {
		t.Fatalf("answer = %q, want absence", *answer)
	}
}

func TestLatestCommittedAssistantFinalAnswerFailsOnMalformedRelevantEvents(t *testing.T) {
	tests := []struct {
		name    string
		kind    string
		payload string
		wantErr error
	}{
		{name: "message", kind: "message", payload: `"not a message"`, wantErr: nil},
		{name: "history replacement", kind: "history_replaced", payload: `{"engine":"compaction","items":"not-an-array"}`, wantErr: errDecodeHistoryReplacedEvent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			appendFinalAnswerTestEvent(t, store, "message", llm.Message{
				Role:    llm.RoleAssistant,
				Phase:   llm.MessagePhaseFinal,
				Content: "older answer",
			})
			if _, err := store.AppendReplayEvents([]session.ReplayEvent{{
				StepID:  "step",
				Kind:    tt.kind,
				Payload: []byte(tt.payload),
			}}); err != nil {
				t.Fatalf("append malformed event: %v", err)
			}

			answer, err := LatestCommittedAssistantFinalAnswerFromStore(store)
			if err == nil {
				t.Fatal("expected malformed event error")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if answer != nil {
				t.Fatalf("answer = %q, must not cross malformed event", *answer)
			}
		})
	}
}
