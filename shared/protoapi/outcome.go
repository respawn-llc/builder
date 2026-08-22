package protoapi

import (
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func SuccessResult(method protoreflect.MethodDescriptor, success proto.Message) (proto.Message, error) {
	if method == nil {
		return nil, fmt.Errorf("method descriptor is required")
	}
	if success == nil || !success.ProtoReflect().IsValid() {
		return nil, fmt.Errorf("%s success is required", method.FullName())
	}
	convention, err := inspectResult(method.Output())
	if err != nil {
		return nil, err
	}
	if success.ProtoReflect().Descriptor().FullName() != convention.success.Message().FullName() {
		return nil, fmt.Errorf(
			"%s success type %s does not match %s",
			method.FullName(),
			success.ProtoReflect().Descriptor().FullName(),
			convention.success.Message().FullName(),
		)
	}
	result := dynamicpb.NewMessage(method.Output())
	result.Set(convention.success, protoreflect.ValueOfMessage(success.ProtoReflect()))
	return result, nil
}

func FailureResult(method protoreflect.MethodDescriptor, detail proto.Message) (proto.Message, error) {
	if method == nil {
		return nil, fmt.Errorf("method descriptor is required")
	}
	if detail == nil || !detail.ProtoReflect().IsValid() {
		return nil, fmt.Errorf("%s failure detail is required", method.FullName())
	}
	convention, err := inspectResult(method.Output())
	if err != nil {
		return nil, err
	}
	detailName := detail.ProtoReflect().Descriptor().FullName()
	if detailName == convention.failure.Message().FullName() {
		if err := Validate(detail); err != nil {
			return nil, err
		}
		result := dynamicpb.NewMessage(method.Output())
		result.Set(convention.failure, protoreflect.ValueOfMessage(detail.ProtoReflect()))
		return result, nil
	}
	var code string
	var detailField protoreflect.FieldDescriptor
	for candidateCode, candidate := range convention.error.detailByCode {
		if candidate.Message().FullName() != detailName {
			continue
		}
		if detailField != nil {
			return nil, fmt.Errorf("%s failure detail type %s is ambiguous", method.FullName(), detailName)
		}
		code, detailField = candidateCode, candidate
	}
	if detailField == nil {
		return nil, fmt.Errorf("%s does not declare failure detail type %s", method.FullName(), detailName)
	}
	failure := dynamicpb.NewMessage(convention.failure.Message())
	failure.Set(convention.error.code, protoreflect.ValueOfString(code))
	failure.Set(detailField, protoreflect.ValueOfMessage(detail.ProtoReflect()))
	result := dynamicpb.NewMessage(method.Output())
	result.Set(convention.failure, protoreflect.ValueOfMessage(failure))
	return result, nil
}
