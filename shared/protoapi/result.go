package protoapi

import (
	"fmt"

	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type OperationOutcome uint8

const (
	OperationSuccess OperationOutcome = iota + 1
	OperationKnownFailure
	OperationGenericFailure
)

type ClassifiedResult struct {
	Outcome OperationOutcome
	Success protoreflect.Message
	Failure *ClassifiedFailure
}

type ClassifiedFailure struct {
	Code   string
	Detail protoreflect.Message
}

func ClassifyResult(message proto.Message) (ClassifiedResult, error) {
	if message == nil {
		return ClassifiedResult{}, fmt.Errorf("operation result is required")
	}
	reflected := message.ProtoReflect()
	if !reflected.IsValid() {
		return ClassifiedResult{}, fmt.Errorf("operation result is required")
	}
	convention, err := inspectResult(reflected.Descriptor())
	if err != nil {
		return ClassifiedResult{}, err
	}
	if err := Validate(message); err != nil {
		return ClassifiedResult{}, fmt.Errorf("validate operation result: %w", err)
	}
	selected := reflected.WhichOneof(convention.outcome)
	switch selected {
	case convention.success:
		success := reflected.Get(convention.success).Message()
		if !success.IsValid() {
			return ClassifiedResult{}, fmt.Errorf("%s success is required", reflected.Descriptor().FullName())
		}
		return ClassifiedResult{Outcome: OperationSuccess, Success: success}, nil
	case convention.failure:
		failure := reflected.Get(convention.failure).Message()
		if !failure.IsValid() {
			return ClassifiedResult{}, fmt.Errorf("%s error is required", reflected.Descriptor().FullName())
		}
		return classifyFailure(failure, convention.error)
	case nil:
		return ClassifiedResult{}, fmt.Errorf("%s has no outcome", reflected.Descriptor().FullName())
	default:
		return ClassifiedResult{}, fmt.Errorf(
			"%s selected an undeclared outcome field %s",
			reflected.Descriptor().FullName(),
			selected.FullName(),
		)
	}
}

type resultConvention struct {
	outcome protoreflect.OneofDescriptor
	success protoreflect.FieldDescriptor
	failure protoreflect.FieldDescriptor
	error   errorConvention
}

type errorConvention struct {
	code         protoreflect.FieldDescriptor
	detail       protoreflect.OneofDescriptor
	detailByCode map[string]protoreflect.FieldDescriptor
}

func inspectResult(descriptor protoreflect.MessageDescriptor) (resultConvention, error) {
	if descriptor == nil {
		return resultConvention{}, fmt.Errorf("operation result descriptor is required")
	}
	var convention resultConvention
	fields := descriptor.Fields()
	for index := 0; index < fields.Len(); index++ {
		field := fields.Get(index)
		options, err := resultFieldOptions(field)
		if err != nil {
			return resultConvention{}, err
		}
		if options == nil {
			return resultConvention{}, fmt.Errorf("%s field %s has no result role", descriptor.FullName(), field.Name())
		}
		switch options.Role {
		case sharedpb.ResultFieldRole_RESULT_FIELD_ROLE_SUCCESS:
			if convention.success != nil {
				return resultConvention{}, fmt.Errorf("%s declares multiple success fields", descriptor.FullName())
			}
			convention.success = field
		case sharedpb.ResultFieldRole_RESULT_FIELD_ROLE_ERROR:
			if convention.failure != nil {
				return resultConvention{}, fmt.Errorf("%s declares multiple error fields", descriptor.FullName())
			}
			convention.failure = field
		default:
			return resultConvention{}, fmt.Errorf(
				"%s field %s has invalid top-level result role %s",
				descriptor.FullName(),
				field.Name(),
				options.Role,
			)
		}
		if options.ErrorCode != nil {
			return resultConvention{}, fmt.Errorf("%s top-level field %s must not declare an error code", descriptor.FullName(), field.Name())
		}
		if field.Kind() != protoreflect.MessageKind {
			return resultConvention{}, fmt.Errorf("%s field %s must be a message", descriptor.FullName(), field.Name())
		}
		outcome := field.ContainingOneof()
		if outcome == nil || outcome.IsSynthetic() {
			return resultConvention{}, fmt.Errorf("%s field %s must belong to the outcome oneof", descriptor.FullName(), field.Name())
		}
		if convention.outcome == nil {
			convention.outcome = outcome
		} else if convention.outcome != outcome {
			return resultConvention{}, fmt.Errorf("%s success and error fields must share one outcome oneof", descriptor.FullName())
		}
	}
	if convention.success == nil || convention.failure == nil || fields.Len() != 2 || convention.outcome.Fields().Len() != 2 {
		return resultConvention{}, fmt.Errorf("%s must declare exactly one success and one error field", descriptor.FullName())
	}
	errorConvention, err := inspectError(convention.failure.Message())
	if err != nil {
		return resultConvention{}, fmt.Errorf("%s error: %w", descriptor.FullName(), err)
	}
	convention.error = errorConvention
	return convention, nil
}

func inspectError(descriptor protoreflect.MessageDescriptor) (errorConvention, error) {
	convention := errorConvention{detailByCode: map[string]protoreflect.FieldDescriptor{}}
	fields := descriptor.Fields()
	for index := 0; index < fields.Len(); index++ {
		field := fields.Get(index)
		options, err := resultFieldOptions(field)
		if err != nil {
			return errorConvention{}, err
		}
		if options == nil {
			return errorConvention{}, fmt.Errorf("%s field %s has no error role", descriptor.FullName(), field.Name())
		}
		switch options.Role {
		case sharedpb.ResultFieldRole_RESULT_FIELD_ROLE_ERROR_CODE:
			if convention.code != nil ||
				field.Kind() != protoreflect.StringKind ||
				field.ContainingOneof() != nil ||
				options.ErrorCode != nil {
				return errorConvention{}, fmt.Errorf("%s field %s is not a valid error code field", descriptor.FullName(), field.Name())
			}
			convention.code = field
		case sharedpb.ResultFieldRole_RESULT_FIELD_ROLE_ERROR_DETAIL:
			detail := field.ContainingOneof()
			if field.Kind() != protoreflect.MessageKind || detail == nil || detail.IsSynthetic() {
				return errorConvention{}, fmt.Errorf("%s field %s is not a typed detail oneof field", descriptor.FullName(), field.Name())
			}
			code := options.GetErrorCode()
			if code == "" {
				return errorConvention{}, fmt.Errorf("%s detail field %s requires a non-empty error code", descriptor.FullName(), field.Name())
			}
			if _, duplicate := convention.detailByCode[code]; duplicate {
				return errorConvention{}, fmt.Errorf("%s declares duplicate detail code %q", descriptor.FullName(), code)
			}
			if convention.detail == nil {
				convention.detail = detail
			} else if convention.detail != detail {
				return errorConvention{}, fmt.Errorf("%s detail fields must share one detail oneof", descriptor.FullName())
			}
			convention.detailByCode[code] = field
		default:
			return errorConvention{}, fmt.Errorf("%s field %s has invalid error role %s", descriptor.FullName(), field.Name(), options.Role)
		}
	}
	if convention.code == nil {
		return errorConvention{}, fmt.Errorf("%s does not declare an error code field", descriptor.FullName())
	}
	if convention.detail == nil || convention.detail.Fields().Len() != len(convention.detailByCode) {
		return errorConvention{}, fmt.Errorf("%s must declare one typed detail oneof", descriptor.FullName())
	}
	return convention, nil
}

func classifyFailure(failure protoreflect.Message, convention errorConvention) (ClassifiedResult, error) {
	code := failure.Get(convention.code).String()
	if code == "" {
		return ClassifiedResult{}, fmt.Errorf("%s error code is required", failure.Descriptor().FullName())
	}
	selectedDetail := failure.WhichOneof(convention.detail)
	requiredDetail, known := convention.detailByCode[code]
	if !known {
		var detail protoreflect.Message
		if selectedDetail != nil {
			detail = failure.Get(selectedDetail).Message()
		}
		return ClassifiedResult{
			Outcome: OperationGenericFailure,
			Failure: &ClassifiedFailure{Code: code, Detail: detail},
		}, nil
	}
	if selectedDetail == nil {
		return ClassifiedResult{}, fmt.Errorf(
			"%s known error code %q requires detail %s",
			failure.Descriptor().FullName(),
			code,
			requiredDetail.Name(),
		)
	}
	if selectedDetail != requiredDetail {
		return ClassifiedResult{}, fmt.Errorf(
			"%s known error code %q requires detail %s, got %s",
			failure.Descriptor().FullName(),
			code,
			requiredDetail.Name(),
			selectedDetail.Name(),
		)
	}
	return ClassifiedResult{
		Outcome: OperationKnownFailure,
		Failure: &ClassifiedFailure{Code: code, Detail: failure.Get(selectedDetail).Message()},
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
