package runtimeattach

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	if got := FormatCompactionError(stall); got != want {
		t.Fatalf("non-provider compaction error = %q, want %q", got, want)
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

func TestFormatCompactionErrorHidesOnlyProviderDiagnostics(t *testing.T) {
	providerErr := &llmerrors.ProviderAPIError{ProviderID: "local", StatusCode: 400, Message: "secret detail", Raw: "raw payload"}
	got := FormatCompactionError(fmt.Errorf("compact: %w", providerErr))
	for _, raw := range []string{providerErr.Error(), providerErr.Message, providerErr.Raw} {
		if got == "" || strings.Contains(got, raw) {
			t.Fatalf("compaction error exposed provider diagnostics: %q", got)
		}
	}
}
