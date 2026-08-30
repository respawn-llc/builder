package runtimeattach

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"core/shared/llmerrors"
	"core/shared/serverapi"
)

func TestFormatSubmissionErrorSurfacesStall(t *testing.T) {
	stall := fmt.Errorf("model generation failed after retries: %w", llmerrors.ErrModelStreamStalled)
	want := llmerrors.UserFacingError(stall)
	if want == "" {
		t.Fatal("expected stall error to have a mapped user-facing message")
	}
	if got := FormatSubmissionError(stall); got != want {
		t.Fatalf("FormatSubmissionError(stall) = %q, want %q", got, want)
	}
}

func TestFormatSubmissionErrorSuppressesCancellation(t *testing.T) {
	if got := FormatSubmissionError(context.Canceled); got != "" {
		t.Fatalf("cancellation should not surface a submission error, got %q", got)
	}
	if got := FormatSubmissionError(errors.Join(ErrSubmissionInterrupted, errors.New("noise"))); got != "" {
		t.Fatalf("interrupt should not surface a submission error, got %q", got)
	}
}

func TestFormatSubmissionErrorRendersWorkflowResumeConflictGuidance(t *testing.T) {
	for _, state := range []serverapi.WorkflowTaskResumeConflictState{
		serverapi.WorkflowTaskResumeConflictPendingApproval,
		serverapi.WorkflowTaskResumeConflictFinished,
		serverapi.WorkflowTaskResumeConflictMovedCurrentNode,
		serverapi.WorkflowTaskResumeConflictCurrentNodeNotInterrupted,
		serverapi.WorkflowTaskResumeConflictNoResumableCurrentNode,
	} {
		err := errors.Join(serverapi.ErrRuntimeCommandNotAccepted, &serverapi.WorkflowTaskResumeConflictError{
			TaskID: "KNT-123", State: state,
		})
		got := FormatSubmissionError(err)
		if got == "" || got == err.Error() || !strings.Contains(got, "KNT-123") {
			t.Fatalf("FormatSubmissionError(%s) = %q, want state-specific guidance", state, got)
		}
	}
}
