package serverapi

import "errors"

type requestValidationError struct {
	cause error
}

func (e requestValidationError) Error() string {
	return e.cause.Error()
}

func (e requestValidationError) Unwrap() error {
	return e.cause
}

func (e requestValidationError) RequestValidationCause() error {
	return e.cause
}

func classifyRequestValidation(cause error) error {
	if cause == nil {
		return nil
	}
	var classified interface{ RequestValidationCause() error }
	if errors.As(cause, &classified) {
		return cause
	}
	return requestValidationError{cause: cause}
}
