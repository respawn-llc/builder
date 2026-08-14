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

func (v Validated[T]) Value() T {
	return v.value
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
	if validator, ok := any(value).(interface{ ValidateRPC() error }); ok {
		if err := validator.ValidateRPC(); err != nil {
			return zero, err
		}
	} else if validator, ok := any(value).(interface{ Validate() error }); ok {
		if err := validator.Validate(); err != nil {
			return zero, err
		}
	} else if policy != NoSemanticValidation {
		return zero, fmt.Errorf("%T has no semantic validator", value)
	}
	return consume(Validated[T]{value: value})
}
