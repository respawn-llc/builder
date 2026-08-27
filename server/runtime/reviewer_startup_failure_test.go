package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/shared/transcript"
)

func TestReviewerStartupFailureSurfacesWithoutReenteringProtectedRuntimeFIFO(t *testing.T) {
	engine := mustNewExecTestEngine(t, mustCreateTestSession(t), &fakeClient{}, Config{Model: "gpt-5"})
	startupErr := errors.New("publish Reviewer activity")
	done := make(chan error, 1)
	go func() {
		done <- engine.stepLifecycle.Run(
			t.Context(),
			exclusiveStepOptions{ActiveKind: ActiveKindUserTurn},
			func(_ context.Context, stepID string) error {
				engine.surfaceRunErrorForStep(stepID, startupErr)
				return nil
			},
		)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("protected Agent Step: %v", err)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("Reviewer startup failure re-entered the protected Runtime FIFO")
	}
	if engine.HasPendingRuntimeOperations() {
		t.Fatal("Reviewer startup failure queued a Runtime FIFO operation")
	}
	entries := engine.ChatSnapshot().Entries
	if len(entries) != 1 ||
		entries[0].Role != string(transcript.EntryRoleDeveloperErrorFeedback) ||
		entries[0].Text != startupErr.Error() {
		t.Fatalf("Reviewer startup diagnostic entries = %+v, want one developer error", entries)
	}
}
