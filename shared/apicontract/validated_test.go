package apicontract

import (
	"errors"
	"testing"
)

type countedRequestValidator struct {
	calls *int
	err   error
}

func (v countedRequestValidator) Validate() error {
	*v.calls++
	return v.err
}

func TestValidateRequestRunsSemanticValidationOnceAndClassifiesFailure(t *testing.T) {
	calls := 0
	cause := errors.New("invalid request")

	err := ValidateRequest(
		countedRequestValidator{calls: &calls, err: cause},
		SemanticValidationRequired,
	)

	if calls != 1 {
		t.Fatalf("validation calls = %d, want 1", calls)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("validation error = %v, want cause %v", err, cause)
	}
	var classified interface{ RequestValidationCause() error }
	if !errors.As(err, &classified) || !errors.Is(classified.RequestValidationCause(), cause) {
		t.Fatalf("validation error = %v, want classified request validation cause", err)
	}
}
