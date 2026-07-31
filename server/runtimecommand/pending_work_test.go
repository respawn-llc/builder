package runtimecommand

import (
	"context"
	"testing"
)

type testPendingWorkAppender struct {
	values []int
}

func (a *testPendingWorkAppender) AppendPendingWork(_ Turn, value int) error {
	a.values = append(a.values, value)
	return nil
}

func TestPendingWorkAcceptanceAppendsAndReleasesTransientPermit(t *testing.T) {
	authority := NewAuthority(1)
	ref := testResourceRef(t)
	if err := authority.Admit(ref); err != nil {
		t.Fatalf("admit session: %v", err)
	}
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})

	appender := &testPendingWorkAppender{}
	future, err := EnqueuePendingWorkAcceptance(
		context.Background(),
		authority,
		OrderedAcceptance[int]{Target: SessionTarget(ref), Payload: 4},
		appender,
	)
	if err != nil {
		t.Fatalf("enqueue pending work acceptance: %v", err)
	}
	if _, err := future.Await(context.Background()); err != nil {
		t.Fatalf("pending work acceptance: %v", err)
	}
	if len(appender.values) != 1 || appender.values[0] != 4 {
		t.Fatalf("pending work values = %v, want [4]", appender.values)
	}
	next, err := Enqueue(context.Background(), authority, SessionTarget(ref), func(Turn) (string, error) {
		return "next", nil
	})
	if err != nil {
		t.Fatalf("enqueue next command: %v", err)
	}
	if got, err := next.Await(context.Background()); err != nil || got != "next" {
		t.Fatalf("next result = %q, %v", got, err)
	}
}
