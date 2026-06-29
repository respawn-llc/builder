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
	if got := FormatSubmissionError(stall); got == "" {
		t.Fatal("expected stall error to format a non-empty submission message")
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
