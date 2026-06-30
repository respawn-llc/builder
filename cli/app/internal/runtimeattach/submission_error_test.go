package runtimeattach

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"core/shared/llmerrors"
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
