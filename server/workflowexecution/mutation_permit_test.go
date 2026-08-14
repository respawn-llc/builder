package workflowexecution

import (
	"context"
	"testing"
	"time"
)

func TestMutationPermitSerializesIndependentWorkflowMutations(t *testing.T) {
	permit := NewMutationPermit()
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- permit.Run(context.Background(), func(context.Context) error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered

	secondEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- permit.Run(context.Background(), func(context.Context) error {
			close(secondEntered)
			return nil
		})
	}()

	select {
	case <-secondEntered:
		t.Fatal("second workflow mutation entered while first mutation held the permit")
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first mutation: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second mutation: %v", err)
	}
}

func TestMutationPermitReusesPermitFromMutationContext(t *testing.T) {
	permit := NewMutationPermit()
	err := permit.Run(context.Background(), func(ctx context.Context) error {
		return permit.Run(ctx, func(context.Context) error {
			return nil
		})
	})
	if err != nil {
		t.Fatalf("nested workflow mutation: %v", err)
	}
}
