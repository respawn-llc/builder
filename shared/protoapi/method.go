package protoapi

import (
	"fmt"

	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

type Operation struct {
	Name           string
	LegacyWireName *string
	Descriptor     protoreflect.MethodDescriptor
	Options        *sharedpb.KentMethodOptions
}

type SubscriptionOperations struct {
	Subscribe  Operation
	Event      Operation
	Completion Operation
}

func ResolveSubscriptionOperations(descriptor protoreflect.MethodDescriptor) (SubscriptionOperations, error) {
	subscribe, err := OperationFromDescriptor(descriptor)
	if err != nil {
		return SubscriptionOperations{}, err
	}
	if subscribe.Options.Kind != sharedpb.OperationKind_OPERATION_KIND_SUBSCRIPTION {
		return SubscriptionOperations{}, fmt.Errorf("%s is not a subscription", descriptor.FullName())
	}
	if subscribe.Options.Direction != sharedpb.Direction_DIRECTION_CLIENT_TO_SERVER {
		return SubscriptionOperations{}, fmt.Errorf("%s is not client-to-server", descriptor.FullName())
	}
	event, err := resolveAssociatedNotification(subscribe, "event", subscribe.Options.Event)
	if err != nil {
		return SubscriptionOperations{}, err
	}
	completion, err := resolveAssociatedNotification(
		subscribe,
		"completion",
		subscribe.Options.Completion,
	)
	if err != nil {
		return SubscriptionOperations{}, err
	}
	return SubscriptionOperations{Subscribe: subscribe, Event: event, Completion: completion}, nil
}

func resolveAssociatedNotification(
	subscribe Operation,
	role string,
	association *sharedpb.OperationAssociation,
) (Operation, error) {
	if association == nil {
		return Operation{}, fmt.Errorf("%s has no %s association", subscribe.Descriptor.FullName(), role)
	}
	fullName := protoreflect.FullName(association.Package + "." + association.Service + "." + association.Method)
	descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(fullName)
	if err != nil {
		return Operation{}, fmt.Errorf("%s %s association: %w", subscribe.Descriptor.FullName(), role, err)
	}
	method, ok := descriptor.(protoreflect.MethodDescriptor)
	if !ok {
		return Operation{}, fmt.Errorf("%s %s association %q is not a method", subscribe.Descriptor.FullName(), role, fullName)
	}
	operation, err := OperationFromDescriptor(method)
	if err != nil {
		return Operation{}, fmt.Errorf("%s %s association: %w", subscribe.Descriptor.FullName(), role, err)
	}
	if operation.Options.Kind != sharedpb.OperationKind_OPERATION_KIND_NOTIFICATION {
		return Operation{}, fmt.Errorf(
			"%s %s association %q is not a notification",
			subscribe.Descriptor.FullName(),
			role,
			operation.Name,
		)
	}
	if operation.Options.Direction != sharedpb.Direction_DIRECTION_SERVER_TO_CLIENT {
		return Operation{}, fmt.Errorf(
			"%s %s association %q is not server-to-client",
			subscribe.Descriptor.FullName(),
			role,
			operation.Name,
		)
	}
	return operation, nil
}

func OperationFromDescriptor(descriptor protoreflect.MethodDescriptor) (Operation, error) {
	if descriptor == nil {
		return Operation{}, fmt.Errorf("method descriptor is required")
	}
	options, err := methodOptions(descriptor)
	if err != nil {
		return Operation{}, err
	}
	if err := validateMethodOptions(options); err != nil {
		return Operation{}, fmt.Errorf("%s: %w", descriptor.FullName(), err)
	}
	name, err := ActiveOperationName(
		string(descriptor.ParentFile().Package()),
		string(descriptor.Parent().Name()),
		string(descriptor.Name()),
	)
	if err != nil {
		return Operation{}, fmt.Errorf("%s: %w", descriptor.FullName(), err)
	}
	var legacyWireName *string
	if options.LegacyWireName != nil {
		value := options.GetLegacyWireName()
		if value == "" {
			return Operation{}, fmt.Errorf("%s legacy wire name must not be empty", descriptor.FullName())
		}
		legacyWireName = &value
	}
	return Operation{
		Name:           name,
		LegacyWireName: legacyWireName,
		Descriptor:     descriptor,
		Options:        options,
	}, nil
}

func ActiveOperationName(packageName, service, method string) (string, error) {
	if err := validatePackageName(packageName); err != nil {
		return "", err
	}
	serviceName, err := pascalCaseToLowerSnake(service)
	if err != nil {
		return "", fmt.Errorf("service: %w", err)
	}
	methodName, err := pascalCaseToLowerSnake(method)
	if err != nil {
		return "", fmt.Errorf("method: %w", err)
	}
	return packageName + "." + serviceName + "." + methodName, nil
}

func methodOptions(descriptor protoreflect.MethodDescriptor) (*sharedpb.KentMethodOptions, error) {
	options := descriptor.Options()
	if !proto.HasExtension(options, sharedpb.E_KentMethod) {
		return nil, fmt.Errorf("%s method options are required", descriptor.FullName())
	}
	value, ok := proto.GetExtension(options, sharedpb.E_KentMethod).(*sharedpb.KentMethodOptions)
	if !ok {
		return nil, fmt.Errorf("%s method options have unexpected Go type", descriptor.FullName())
	}
	return value, nil
}

func validateMethodOptions(options *sharedpb.KentMethodOptions) error {
	if options == nil {
		return fmt.Errorf("method options are required")
	}
	switch options.AuthenticationStage {
	case sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_NONE,
		sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_PRE_SERVER,
		sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_SERVER:
	default:
		return fmt.Errorf("authentication stage %s is invalid", options.AuthenticationStage)
	}
	switch options.ScopePolicy {
	case sharedpb.ScopePolicy_SCOPE_POLICY_NONE,
		sharedpb.ScopePolicy_SCOPE_POLICY_ATTACH_PROJECT,
		sharedpb.ScopePolicy_SCOPE_POLICY_ATTACH_SESSION,
		sharedpb.ScopePolicy_SCOPE_POLICY_PROJECT_VIEW,
		sharedpb.ScopePolicy_SCOPE_POLICY_PROJECT_WORKSPACE,
		sharedpb.ScopePolicy_SCOPE_POLICY_PROJECT_WORKSPACE_BINDING,
		sharedpb.ScopePolicy_SCOPE_POLICY_SESSION_ACTIVE_PROJECT,
		sharedpb.ScopePolicy_SCOPE_POLICY_SESSION_ACTIVE_PROJECT_IF_SET,
		sharedpb.ScopePolicy_SCOPE_POLICY_SESSION_DRAFT_HANDOFF_PROJECT,
		sharedpb.ScopePolicy_SCOPE_POLICY_SESSION_ATTACHED_PROJECT,
		sharedpb.ScopePolicy_SCOPE_POLICY_ATTACHED_SESSION,
		sharedpb.ScopePolicy_SCOPE_POLICY_GOAL_SESSION,
		sharedpb.ScopePolicy_SCOPE_POLICY_RUNTIME_LIVE_SESSION_REQUIRED,
		sharedpb.ScopePolicy_SCOPE_POLICY_RUNTIME_LIVE_SESSION_OPTIONAL,
		sharedpb.ScopePolicy_SCOPE_POLICY_PROCESS_ACTIVE_PROJECT,
		sharedpb.ScopePolicy_SCOPE_POLICY_NOTIFICATION:
	default:
		return fmt.Errorf("scope policy %s is invalid", options.ScopePolicy)
	}
	switch options.Direction {
	case sharedpb.Direction_DIRECTION_CLIENT_TO_SERVER,
		sharedpb.Direction_DIRECTION_SERVER_TO_CLIENT:
	default:
		return fmt.Errorf("direction %s is invalid", options.Direction)
	}
	switch options.Kind {
	case sharedpb.OperationKind_OPERATION_KIND_UNARY:
		switch options.UnaryConnection {
		case sharedpb.UnaryConnection_UNARY_CONNECTION_MULTIPLEXED,
			sharedpb.UnaryConnection_UNARY_CONNECTION_DEDICATED:
		default:
			return fmt.Errorf("unary connection %s is invalid for unary operation", options.UnaryConnection)
		}
		if options.Event != nil || options.Completion != nil {
			return fmt.Errorf("unary operation must not declare event or completion association")
		}
	case sharedpb.OperationKind_OPERATION_KIND_SUBSCRIPTION:
		if options.UnaryConnection != sharedpb.UnaryConnection_UNARY_CONNECTION_UNSPECIFIED {
			return fmt.Errorf("non-unary operation must not declare unary connection")
		}
		if options.Event == nil || options.Completion == nil {
			return fmt.Errorf("subscription operation requires event and completion associations")
		}
	case sharedpb.OperationKind_OPERATION_KIND_PROGRESS:
		if options.UnaryConnection != sharedpb.UnaryConnection_UNARY_CONNECTION_UNSPECIFIED {
			return fmt.Errorf("non-unary operation must not declare unary connection")
		}
		if options.Event == nil || options.Completion != nil {
			return fmt.Errorf("progress operation requires only an event association")
		}
	case sharedpb.OperationKind_OPERATION_KIND_NOTIFICATION:
		if options.UnaryConnection != sharedpb.UnaryConnection_UNARY_CONNECTION_UNSPECIFIED {
			return fmt.Errorf("non-unary operation must not declare unary connection")
		}
		if options.Event != nil || options.Completion != nil {
			return fmt.Errorf("notification operation must not declare event or completion association")
		}
	default:
		return fmt.Errorf("operation kind %s is invalid", options.Kind)
	}
	return nil
}

func validatePackageName(packageName string) error {
	if packageName == "" {
		return fmt.Errorf("package is empty")
	}
	atSegmentStart := true
	for index := 0; index < len(packageName); index++ {
		character := packageName[index]
		if character == '.' {
			if atSegmentStart {
				return fmt.Errorf("package segment at byte %d is empty", index)
			}
			atSegmentStart = true
			continue
		}
		if atSegmentStart {
			if !isASCIILower(character) {
				return fmt.Errorf("package segment at byte %d must start with an ASCII lowercase letter", index)
			}
			atSegmentStart = false
			continue
		}
		if !isASCIILower(character) && !isASCIIDigit(character) && character != '_' {
			return fmt.Errorf("invalid package character at byte %d", index)
		}
	}
	if atSegmentStart {
		return fmt.Errorf("package has an empty trailing segment")
	}
	return nil
}

func pascalCaseToLowerSnake(identifier string) (string, error) {
	if identifier == "" {
		return "", fmt.Errorf("identifier is empty")
	}
	if !isASCIIUpper(identifier[0]) {
		return "", fmt.Errorf("identifier must start with an ASCII uppercase letter")
	}
	result := make([]byte, 0, len(identifier)+4)
	for index := 0; index < len(identifier); index++ {
		character := identifier[index]
		if !isASCIIUpper(character) && !isASCIILower(character) && !isASCIIDigit(character) {
			return "", fmt.Errorf("invalid identifier character at byte %d", index)
		}
		if isASCIIUpper(character) && index > 0 {
			previous := identifier[index-1]
			hasFollowingLower := index+1 < len(identifier) && isASCIILower(identifier[index+1])
			if isASCIILower(previous) || isASCIIDigit(previous) || isASCIIUpper(previous) && hasFollowingLower {
				result = append(result, '_')
			}
		}
		if isASCIIUpper(character) {
			character += 'a' - 'A'
		}
		result = append(result, character)
	}
	return string(result), nil
}

func isASCIIUpper(character byte) bool {
	return character >= 'A' && character <= 'Z'
}

func isASCIILower(character byte) bool {
	return character >= 'a' && character <= 'z'
}

func isASCIIDigit(character byte) bool {
	return character >= '0' && character <= '9'
}
