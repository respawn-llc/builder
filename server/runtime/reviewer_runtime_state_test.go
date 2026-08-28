package runtime

import (
	"testing"

	"core/shared/clientui"
)

func TestReviewerRuntimeStateReservesBeforeInvocationAndTransitionsToAddressingFeedback(t *testing.T) {
	state := newReviewerRuntimeState(nil)
	stepID := runtimeTestStepID("reviewer-phase")

	if !state.Reserve(stepID) {
		t.Fatal("Reserve returned false")
	}
	if !state.Active() {
		t.Fatal("reserved Reviewer activity is not active")
	}
	if got := state.Activity(); got != clientui.ReviewerActivityInactive {
		t.Fatalf("reserved Reviewer activity = %q, want inactive", got)
	}
	if state.Reserve(runtimeTestStepID("reviewer-second")) {
		t.Fatal("Reserve accepted a second Reviewer request")
	}
	if !state.Start(stepID) {
		t.Fatal("Start returned false")
	}
	if got := state.Activity(); got != clientui.ReviewerActivityInvoking {
		t.Fatalf("started Reviewer activity = %q, want invoking", got)
	}
	if !state.SetAddressingFeedback(stepID) {
		t.Fatal("SetAddressingFeedback returned false")
	}
	if got := state.Activity(); got != clientui.ReviewerActivityAddressingFeedback {
		t.Fatalf("Reviewer activity after provider result = %q, want addressing_feedback", got)
	}
	if !state.Clear(stepID) {
		t.Fatal("Clear returned false")
	}
	if got := state.Activity(); got != clientui.ReviewerActivityInactive {
		t.Fatalf("completed Reviewer activity = %q, want inactive", got)
	}
}
