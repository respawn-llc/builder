package app

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"core/shared/llmerrors"
)

func TestFormatRuntimeSubmissionErrorSurfacesMappedFailureAndSuppressesInterruption(t *testing.T) {
	t.Parallel()

	stall := fmt.Errorf("model generation failed after retries: %w", llmerrors.ErrModelStreamStalled)
	if got, want := formatRuntimeSubmissionError(stall), llmerrors.UserFacingError(stall); got != want {
		t.Fatalf("formatRuntimeSubmissionError(stall) = %q, want %q", got, want)
	}
	for _, err := range []error{context.Canceled, errors.Join(errRuntimeSubmissionInterrupted, errors.New("noise"))} {
		if got := formatRuntimeSubmissionError(err); got != "" {
			t.Fatalf("formatRuntimeSubmissionError(%v) = %q, want empty", err, got)
		}
	}
}
