package apicontract

import (
	"errors"
	"fmt"
)

type ValidationPolicy uint8

const (
	SemanticValidationRequired ValidationPolicy = iota
	NoSemanticValidation
)

type Validated[T any] struct {
	value T
}

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

func (v Validated[T]) Value() T {
	return v.value
}

// ClassifyRequestValidation marks a concrete request validator failure for RPC mapping.
func ClassifyRequestValidation(cause error) error {
	if cause == nil {
		return nil
	}
	return requestValidationError{cause: cause}
}

func WithValidated[T any, R any](
	value T,
	policy ValidationPolicy,
	consume func(Validated[T]) (R, error),
) (R, error) {
	var zero R
	if consume == nil {
		return zero, errors.New("validated request consumer is required")
	}
	if err := validateRequest(value, policy); err != nil {
		return zero, err
	}
	return consume(Validated[T]{value: value})
}

func validateRequest[T any](value T, policy ValidationPolicy) error {
	if validator, ok := any(value).(interface{ ValidateRPC() error }); ok {
		if err := validator.ValidateRPC(); err != nil {
			return requestValidationError{cause: err}
		}
	} else if validator, ok := any(value).(interface{ Validate() error }); ok {
		if err := validator.Validate(); err != nil {
			return requestValidationError{cause: err}
		}
	} else if policy != NoSemanticValidation {
		return fmt.Errorf("%T has no semantic validator", value)
	}
	return nil
}
