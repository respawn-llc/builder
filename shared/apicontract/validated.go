package apicontract

import (
	"errors"
	"fmt"
	"strings"

	"core/shared/labelcontract"
	"core/shared/runtimeids"
	"core/shared/workflowcontract"
)

type ValidationPolicy uint8

const (
	SemanticValidationRequired ValidationPolicy = iota
	NoSemanticValidation
)

type ValidationMethod uint8

const (
	ValidationMethodNone ValidationMethod = iota
	ValidationMethodValidateRPC
	ValidationMethodValidate
)

type Validated[T any] struct {
	value T
}

func (v Validated[T]) Value() T {
	return v.value
}

func (v Validated[T]) SessionID(raw string) runtimeids.SessionID {
	id, err := runtimeids.ParseSessionID(strings.TrimSpace(raw))
	if err != nil {
		panic(fmt.Sprintf("validated request contains invalid Session ID %q: %v", raw, err))
	}
	return id
}

func (v Validated[T]) RuntimeClientRequestID(raw string) runtimeids.RuntimeClientRequestID {
	id, err := runtimeids.ParseRuntimeClientRequestID(raw)
	if err != nil {
		panic(fmt.Sprintf("validated request contains invalid Runtime Client Request ID %q: %v", raw, err))
	}
	return id
}

func (v Validated[T]) TaskID(raw string) runtimeids.TaskID {
	id, err := runtimeids.ParseTaskID(raw)
	if err != nil {
		panic(fmt.Sprintf("validated request contains invalid Task ID %q: %v", raw, err))
	}
	return id
}

func (v Validated[T]) LabelID(raw string) runtimeids.LabelID {
	id, err := runtimeids.ParseLabelID(raw)
	if err != nil {
		panic(fmt.Sprintf("validated request contains invalid Label ID %q: %v", raw, err))
	}
	return id
}

func (v Validated[T]) LabelIDs(raw []string) []runtimeids.LabelID {
	ids := make([]runtimeids.LabelID, 0, len(raw))
	for _, value := range raw {
		ids = append(ids, v.LabelID(value))
	}
	return ids
}

func (v Validated[T]) LabelName(raw string) labelcontract.Name {
	name, err := labelcontract.PrepareName(raw)
	if err != nil {
		panic(fmt.Sprintf("validated request contains invalid Label name %q: %v", raw, err))
	}
	return name
}

func (v Validated[T]) TaskTitle(raw string) workflowcontract.TaskTitle {
	title, err := workflowcontract.NewTaskTitle(raw)
	if err != nil {
		panic(fmt.Sprintf("validated request contains invalid Task title %q: %v", raw, err))
	}
	return title
}

func (v Validated[T]) OptionalTaskTitle(raw *string) *workflowcontract.TaskTitle {
	if raw == nil {
		return nil
	}
	title := v.TaskTitle(*raw)
	return &title
}

func (v Validated[T]) WorkflowName(raw string) string {
	name, err := workflowcontract.NewWorkflowName(raw)
	if err != nil {
		panic(fmt.Sprintf("validated request contains invalid Workflow name %q: %v", raw, err))
	}
	return name.String()
}

func (v Validated[T]) OptionalProjectKey(raw string) *runtimeids.ProjectKey {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	key, err := runtimeids.ParseProjectKey(raw)
	if err != nil {
		panic(fmt.Sprintf("validated request contains invalid Project Key %q: %v", raw, err))
	}
	return &key
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
	switch ValidationMethodFor(value) {
	case ValidationMethodValidateRPC:
		validator := any(value).(interface{ ValidateRPC() error })
		if err := validator.ValidateRPC(); err != nil {
			return zero, err
		}
	case ValidationMethodValidate:
		validator := any(value).(interface{ Validate() error })
		if err := validator.Validate(); err != nil {
			return zero, err
		}
	case ValidationMethodNone:
		if policy != NoSemanticValidation {
			return zero, fmt.Errorf("%T has no semantic validator", value)
		}
	}
	return consume(Validated[T]{value: value})
}

func ValidationMethodFor[T any](value T) ValidationMethod {
	if _, ok := any(value).(interface{ ValidateRPC() error }); ok {
		return ValidationMethodValidateRPC
	}
	if _, ok := any(value).(interface{ Validate() error }); ok {
		return ValidationMethodValidate
	}
	return ValidationMethodNone
}
