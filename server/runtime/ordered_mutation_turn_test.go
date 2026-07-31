package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/tools"
)

type testOrderedMutationTurn struct {
	err error
}

func (t testOrderedMutationTurn) Apply(apply func() error) error {
	if t.err != nil {
		return t.err
	}
	return apply()
}

func (t testOrderedMutationTurn) RetainLease() (OrderedMutationLease, error) {
	return nil, errors.New("test turn cannot retain queue capacity")
}

func TestSynchronousControlMutationUsesLexicalTurn(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	orderedMutationErr := errors.New("nested ordered dispatch")
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OrderedMutation: func(func(OrderedMutationTurn) error) error {
			return orderedMutationErr
		},
	})

	if err := engine.AppendCommittedEntryWithOrderedTurn(testOrderedMutationTurn{}, "system", "ordered"); err != nil {
		t.Fatalf("append committed entry with lexical turn: %v", err)
	}
	changed, _, receipt, err := engine.SetQuestionsEnabledWithCommittedFeedbackAndOrderedTurn(
		testOrderedMutationTurn{},
		false,
		func(bool, bool) string { return "questions changed" },
	)
	if err != nil {
		t.Fatalf("questions control mutation with lexical turn: %v", err)
	}
	if !changed || !receipt.Committed {
		t.Fatalf("questions mutation result = changed %v, receipt %#v", changed, receipt)
	}
	if engine.QuestionsEnabled() {
		t.Fatal("questions remained enabled after ordered mutation")
	}
}

func TestSynchronousControlMutationRejectsExpiredLexicalTurn(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	expired := errors.New("ordered turn expired")

	err := engine.AppendCommittedEntryWithOrderedTurn(testOrderedMutationTurn{err: expired}, "system", "rejected")
	if !errors.Is(err, expired) {
		t.Fatalf("expired lexical turn error = %v, want %v", err, expired)
	}
	if rows := mustTranscriptHydrationSnapshot(t, engine).CommittedRows; len(rows) != 0 {
		t.Fatalf("expired lexical turn persisted rows: %+v", rows)
	}
}

func TestRetainedExecutionMutationDoesNotFallBackAfterContinuationRetirement(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})

	err := engine.ApplyRetainedExecutionMutation(context.Background(), func(turn OrderedMutationTurn) error {
		return turn.Apply(func() error {
			return engine.AppendCommittedEntry("system", "stale")
		})
	})
	if !errors.Is(err, ErrExecutionMutationUnavailable) {
		t.Fatalf("retired execution mutation error = %v, want ErrExecutionMutationUnavailable", err)
	}
	if rows := mustTranscriptHydrationSnapshot(t, engine).CommittedRows; len(rows) != 0 {
		t.Fatalf("retired execution mutation persisted rows: %+v", rows)
	}
}
