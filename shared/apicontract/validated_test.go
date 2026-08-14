package apicontract

import (
	"errors"
	"testing"
)

func TestClassifyRequestValidationPreservesCause(t *testing.T) {
	cause := errors.New("invalid request")

	err := ClassifyRequestValidation(cause)

	if !errors.Is(err, cause) {
		t.Fatalf("validation error = %v, want cause %v", err, cause)
	}
	var classified interface{ RequestValidationCause() error }
	if !errors.As(err, &classified) || !errors.Is(classified.RequestValidationCause(), cause) {
		t.Fatalf("validation error = %v, want classified request validation cause", err)
	}
}
