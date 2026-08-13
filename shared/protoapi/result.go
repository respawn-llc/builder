package protoapi

import (
	"fmt"

	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// OperationOutcomeKind identifies the selected branch of an operation-specific
// result without interpreting user-visible text.
type OperationOutcomeKind uint8

const (
	OperationOutcomeSuccess OperationOutcomeKind = iota + 1
	OperationOutcomeKnownFailure
	OperationOutcomeGenericFailure
)

// OperationResult is a validated operation-specific success or failure.
type OperationResult struct {
	Kind    OperationOutcomeKind
	Success protoreflect.Message
	Failure *OperationFailure
}

// OperationFailure is the stable machine-readable failure carried by an
// operation result.
type OperationFailure struct {
	Code   string
	Detail protoreflect.Message
}

// ClassifyOperationResult validates and classifies one generated
// operation-specific result message.
func ClassifyOperationResult(message proto.Message) (OperationResult, error) {
	if message == nil {
		return OperationResult{}, fmt.Errorf("operation result is required")
	}
	reflected := message.ProtoReflect()
	if !reflected.IsValid() {
		return OperationResult{}, fmt.Errorf("operation result is required")
	}
	convention, err := inspectOperationResult(reflected.Descriptor())
	if err != nil {
		return OperationResult{}, err
	}
	if err := ValidateGeneratedMessage(message); err != nil {
		return OperationResult{}, fmt.Errorf("validate operation result: %w", err)
	}
	selected := reflected.WhichOneof(convention.outcome)
	if selected == nil {
		return OperationResult{}, fmt.Errorf("%s has no outcome", reflected.Descriptor().FullName())
	}
	switch selected {
	case convention.success:
		success := reflected.Get(convention.success).Message()
		if !success.IsValid() {
			return OperationResult{}, fmt.Errorf("%s success is required", reflected.Descriptor().FullName())
		}
		return OperationResult{
			Kind:    OperationOutcomeSuccess,
			Success: success,
		}, nil
	case convention.failure:
		failure := reflected.Get(convention.failure).Message()
		if !failure.IsValid() {
			return OperationResult{}, fmt.Errorf("%s error is required", reflected.Descriptor().FullName())
		}
		return classifyOperationFailure(failure, convention.error)
	default:
		return OperationResult{}, fmt.Errorf("%s selected an undeclared outcome field %s", reflected.Descriptor().FullName(), selected.FullName())
	}
}

// IsOperationResultDescriptor reports whether a descriptor declares the complete
// operation-result convention. Malformed declarations return false.
func IsOperationResultDescriptor(descriptor protoreflect.MessageDescriptor) bool {
	_, err := inspectOperationResult(descriptor)
	return err == nil
}

type operationResultConvention struct {
	outcome protoreflect.OneofDescriptor
	success protoreflect.FieldDescriptor
	failure protoreflect.FieldDescriptor
	error   operationErrorConvention
}

type operationErrorConvention struct {
	code         protoreflect.FieldDescriptor
	detail       protoreflect.OneofDescriptor
	detailByCode map[string]protoreflect.FieldDescriptor
}

func inspectOperationResult(descriptor protoreflect.MessageDescriptor) (operationResultConvention, error) {
	if descriptor == nil {
		return operationResultConvention{}, fmt.Errorf("operation result descriptor is required")
	}
	var convention operationResultConvention
	fields := descriptor.Fields()
	for index := 0; index < fields.Len(); index++ {
		field := fields.Get(index)
		options, err := resultFieldOptions(field)
		if err != nil {
			return operationResultConvention{}, err
		}
		if options == nil {
			return operationResultConvention{}, fmt.Errorf("%s field %s has no result role", descriptor.FullName(), field.Name())
		}
		switch options.Role {
		case sharedpb.ResultFieldRole_RESULT_FIELD_ROLE_SUCCESS:
			if convention.success != nil {
				return operationResultConvention{}, fmt.Errorf("%s declares multiple success fields", descriptor.FullName())
			}
			convention.success = field
		case sharedpb.ResultFieldRole_RESULT_FIELD_ROLE_ERROR:
			if convention.failure != nil {
				return operationResultConvention{}, fmt.Errorf("%s declares multiple error fields", descriptor.FullName())
			}
			convention.failure = field
		default:
			return operationResultConvention{}, fmt.Errorf("%s field %s has invalid top-level result role %s", descriptor.FullName(), field.Name(), options.Role)
		}
		if options.ErrorCode != nil {
			return operationResultConvention{}, fmt.Errorf("%s top-level field %s must not declare an error code", descriptor.FullName(), field.Name())
		}
		if field.Kind() != protoreflect.MessageKind {
			return operationResultConvention{}, fmt.Errorf("%s field %s must be a message", descriptor.FullName(), field.Name())
		}
		if field.ContainingOneof() == nil || field.ContainingOneof().IsSynthetic() {
			return operationResultConvention{}, fmt.Errorf("%s field %s must belong to the outcome oneof", descriptor.FullName(), field.Name())
		}
		if convention.outcome == nil {
			convention.outcome = field.ContainingOneof()
		} else if convention.outcome != field.ContainingOneof() {
			return operationResultConvention{}, fmt.Errorf("%s success and error fields must share one outcome oneof", descriptor.FullName())
		}
	}
	if convention.success == nil || convention.failure == nil || fields.Len() != 2 {
		return operationResultConvention{}, fmt.Errorf("%s must declare exactly one success and one error field", descriptor.FullName())
	}
	if convention.outcome.Fields().Len() != 2 {
		return operationResultConvention{}, fmt.Errorf("%s outcome must contain exactly success and error", descriptor.FullName())
	}
	errorConvention, err := inspectOperationError(convention.failure.Message())
	if err != nil {
		return operationResultConvention{}, fmt.Errorf("%s error: %w", descriptor.FullName(), err)
	}
	convention.error = errorConvention
	return convention, nil
}

func inspectOperationError(descriptor protoreflect.MessageDescriptor) (operationErrorConvention, error) {
	convention := operationErrorConvention{
		detailByCode: make(map[string]protoreflect.FieldDescriptor),
	}
	fields := descriptor.Fields()
	for index := 0; index < fields.Len(); index++ {
		field := fields.Get(index)
		options, err := resultFieldOptions(field)
		if err != nil {
			return operationErrorConvention{}, err
		}
		if options == nil {
			return operationErrorConvention{}, fmt.Errorf("%s field %s has no error role", descriptor.FullName(), field.Name())
		}
		switch options.Role {
		case sharedpb.ResultFieldRole_RESULT_FIELD_ROLE_ERROR_CODE:
			if convention.code != nil {
				return operationErrorConvention{}, fmt.Errorf("%s declares multiple error code fields", descriptor.FullName())
			}
			if field.Kind() != protoreflect.StringKind || field.ContainingOneof() != nil || options.ErrorCode != nil {
				return operationErrorConvention{}, fmt.Errorf("%s field %s is not a valid error code field", descriptor.FullName(), field.Name())
			}
			convention.code = field
		case sharedpb.ResultFieldRole_RESULT_FIELD_ROLE_ERROR_DETAIL:
			if field.Kind() != protoreflect.MessageKind || field.ContainingOneof() == nil || field.ContainingOneof().IsSynthetic() {
				return operationErrorConvention{}, fmt.Errorf("%s field %s is not a typed detail oneof field", descriptor.FullName(), field.Name())
			}
			code := options.GetErrorCode()
			if code == "" {
				return operationErrorConvention{}, fmt.Errorf("%s detail field %s requires a non-empty error code", descriptor.FullName(), field.Name())
			}
			if _, duplicate := convention.detailByCode[code]; duplicate {
				return operationErrorConvention{}, fmt.Errorf("%s declares duplicate detail code %q", descriptor.FullName(), code)
			}
			if convention.detail == nil {
				convention.detail = field.ContainingOneof()
			} else if convention.detail != field.ContainingOneof() {
				return operationErrorConvention{}, fmt.Errorf("%s detail fields must share one detail oneof", descriptor.FullName())
			}
			convention.detailByCode[code] = field
		default:
			return operationErrorConvention{}, fmt.Errorf("%s field %s has invalid error role %s", descriptor.FullName(), field.Name(), options.Role)
		}
	}
	if convention.code == nil {
		return operationErrorConvention{}, fmt.Errorf("%s does not declare an error code field", descriptor.FullName())
	}
	if convention.detail == nil || convention.detail.Fields().Len() != len(convention.detailByCode) {
		return operationErrorConvention{}, fmt.Errorf("%s must declare one typed detail oneof", descriptor.FullName())
	}
	return convention, nil
}

func classifyOperationFailure(
	failure protoreflect.Message,
	convention operationErrorConvention,
) (OperationResult, error) {
	code := failure.Get(convention.code).String()
	if code == "" {
		return OperationResult{}, fmt.Errorf("%s error code is required", failure.Descriptor().FullName())
	}
	selectedDetail := failure.WhichOneof(convention.detail)
	requiredDetail, known := convention.detailByCode[code]
	if !known {
		var detail protoreflect.Message
		if selectedDetail != nil {
			detail = failure.Get(selectedDetail).Message()
		}
		return OperationResult{
			Kind: OperationOutcomeGenericFailure,
			Failure: &OperationFailure{
				Code:   code,
				Detail: detail,
			},
		}, nil
	}
	if selectedDetail == nil {
		return OperationResult{}, fmt.Errorf("%s known error code %q requires detail %s", failure.Descriptor().FullName(), code, requiredDetail.Name())
	}
	if selectedDetail != requiredDetail {
		return OperationResult{}, fmt.Errorf(
			"%s known error code %q requires detail %s, got %s",
			failure.Descriptor().FullName(),
			code,
			requiredDetail.Name(),
			selectedDetail.Name(),
		)
	}
	return OperationResult{
		Kind: OperationOutcomeKnownFailure,
		Failure: &OperationFailure{
			Code:   code,
			Detail: failure.Get(selectedDetail).Message(),
		},
	}, nil
}

func resultFieldOptions(field protoreflect.FieldDescriptor) (*sharedpb.KentResultFieldOptions, error) {
	options := field.Options()
	if !proto.HasExtension(options, sharedpb.E_KentResultField) {
		return nil, nil
	}
	value, ok := proto.GetExtension(options, sharedpb.E_KentResultField).(*sharedpb.KentResultFieldOptions)
	if !ok {
		return nil, fmt.Errorf("%s result field options have unexpected Go type", field.FullName())
	}
	return value, nil
}
