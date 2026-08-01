package runtime

import (
	"context"
	"errors"
	"testing"
)

func TestExecutionMutationBindingRestoresActiveOwner(t *testing.T) {
	engine := &Engine{}
	firstErr := errors.New("first mutation")
	secondErr := errors.New("second mutation")
	var firstCalls, secondCalls int
	first := engine.BindExecutionMutation(func(context.Context, func(OrderedMutationTurn) error) error {
		firstCalls++
		return firstErr
	})
	second := engine.BindExecutionMutation(func(context.Context, func(OrderedMutationTurn) error) error {
		secondCalls++
		return secondErr
	})

	engine.ClearExecutionMutationIf(second)

	if got := engine.executionMutationSnapshot()(context.Background(), nil); !errors.Is(got, firstErr) {
		t.Fatalf("restored execution mutation error = %v, want %v", got, firstErr)
	}
	if firstCalls != 1 || secondCalls != 0 {
		t.Fatalf("mutation calls after nested clear = first %d, second %d", firstCalls, secondCalls)
	}

	engine.ClearExecutionMutationIf(first)
	if got := engine.executionMutationSnapshot(); got != nil {
		t.Fatalf("execution mutation after owner clear = %v, want nil", got)
	}
}

func TestExecutionMutationBindingOutOfOrderClearSkipsRetiredOwner(t *testing.T) {
	engine := &Engine{}
	firstErr := errors.New("first mutation")
	secondErr := errors.New("second mutation")
	first := engine.BindExecutionMutation(func(context.Context, func(OrderedMutationTurn) error) error {
		return firstErr
	})
	second := engine.BindExecutionMutation(func(context.Context, func(OrderedMutationTurn) error) error {
		return secondErr
	})

	engine.ClearExecutionMutationIf(first)
	if got := engine.executionMutationSnapshot()(context.Background(), nil); !errors.Is(got, secondErr) {
		t.Fatalf("current execution mutation error = %v, want %v", got, secondErr)
	}

	engine.ClearExecutionMutationIf(second)
	if got := engine.executionMutationSnapshot(); got != nil {
		t.Fatalf("execution mutation after out-of-order clears = %v, want nil", got)
	}
}
