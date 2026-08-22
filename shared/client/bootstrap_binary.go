package client

import (
	"errors"
	"fmt"

	"core/shared/protoapi"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/serverapi"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func bootstrapMethod(
	file protoreflect.FileDescriptor,
	serviceName protoreflect.Name,
	methodName protoreflect.Name,
) protoreflect.MethodDescriptor {
	service := file.Services().ByName(serviceName)
	if service == nil {
		panic(fmt.Sprintf("generated %s descriptor is required", serviceName))
	}
	method := service.Methods().ByName(methodName)
	if method == nil {
		panic(fmt.Sprintf("generated %s.%s descriptor is required", serviceName, methodName))
	}
	return method
}

func authGeneratedError(code string, internal *sharedpb.InternalFailureDetails) error {
	switch code {
	case "auth_required":
		return serverapi.ErrServerAuthRequired
	case "internal_failure":
		return protoapi.InternalFailureFromProto(internal)
	default:
		return generatedOperationFailure(code)
	}
}

func generatedOperationFailure(code string) error {
	if code == "" {
		return errors.New("server operation failed")
	}
	return fmt.Errorf("server operation failed with code %q", code)
}
