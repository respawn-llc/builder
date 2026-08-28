package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/llm"
	"core/shared/clientui"
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

func TestReviewerPreparationFailureLeavesActivityInactive(t *testing.T) {
	engine := mustNewExecTestEngine(t, mustCreateTestSession(t), &fakeClient{}, Config{Model: "gpt-5"})
	pipeline := reviewerPipelineWithPreparationError{err: errors.New("prepare Reviewer request")}
	stepID := runtimeTestStepID("reviewer-preparation-failure")

	if err := engine.startReviewer(t.Context(), stepID, &fakeClient{}, pipeline); err != nil {
		t.Fatalf("start Reviewer: %v", err)
	}
	waitEngineLifecycleTasks(t, engine)
	if got := engine.ReviewerActivity(); got != clientui.ReviewerActivityInactive {
		t.Fatalf("Reviewer activity after preparation failure = %q, want inactive", got)
	}
}

func TestPreparedReviewerReservationBlocksRetirementBeforeInvocation(t *testing.T) {
	engine := mustNewExecTestEngine(t, mustCreateTestSession(t), &fakeClient{}, Config{Model: "gpt-5"})
	stepID := runtimeTestStepID("reviewer-reservation")

	if !engine.reserveReviewerActivity(stepID) {
		t.Fatal("reserve Reviewer activity returned false")
	}
	if engine.ReviewerActivity() != clientui.ReviewerActivityInactive {
		t.Fatalf("reserved Reviewer activity = %q, want inactive", engine.ReviewerActivity())
	}
	if engine.BeginRetirement() {
		t.Fatal("BeginRetirement succeeded while prepared Reviewer work was reserved")
	}
	engine.releaseReviewerActivity(stepID)
}

type reviewerPipelineWithPreparationError struct {
	err error
}

func (reviewerPipelineWithPreparationError) ShouldRunTurn(string, llm.Client, bool) bool {
	return true
}

func (p reviewerPipelineWithPreparationError) Prepare(context.Context, string, llm.Client) (preparedReviewerRequest, error) {
	return preparedReviewerRequest{}, p.err
}

func (reviewerPipelineWithPreparationError) Run(context.Context, preparedReviewerRequest) reviewerProviderResult {
	panic("Reviewer provider must not run after preparation failure")
}
