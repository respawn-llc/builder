package protoapi

import (
	"fmt"
	"iter"
	"sort"

	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/protoapi/gen/registry"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Operation is the validated descriptor policy for one Server API method.
type Operation struct {
	ActiveName     string
	LegacyWireName *string
	Descriptor     protoreflect.MethodDescriptor
	Options        *sharedpb.KentMethodOptions
	Event          *OperationAssociation
	Completion     *OperationAssociation
}

// OperationAssociation identifies an event or completion method.
type OperationAssociation struct {
	ActiveName string
	Descriptor protoreflect.MethodDescriptor
}

// Operations returns the validated operation descriptor index sorted by active
// wire name.
func Operations() ([]Operation, error) {
	descriptors := make([]protoreflect.MethodDescriptor, 0)
	for file := range Files() {
		services := file.Services()
		for serviceIndex := 0; serviceIndex < services.Len(); serviceIndex++ {
			methods := services.Get(serviceIndex).Methods()
			for methodIndex := 0; methodIndex < methods.Len(); methodIndex++ {
				descriptors = append(descriptors, methods.Get(methodIndex))
			}
		}
	}

	operations := make([]Operation, 0, len(descriptors))
	byDeclaration := make(map[protoreflect.FullName]protoreflect.MethodDescriptor, len(descriptors))
	byActiveName := make(map[string]struct{}, len(descriptors))
	for _, descriptor := range descriptors {
		byDeclaration[descriptor.FullName()] = descriptor
		options, err := kentMethodOptions(descriptor)
		if err != nil {
			return nil, err
		}
		operation, err := OperationFromDescriptor(descriptor, options)
		if err != nil {
			return nil, err
		}
		if _, duplicate := byActiveName[operation.ActiveName]; duplicate {
			return nil, fmt.Errorf("duplicate active operation name %q", operation.ActiveName)
		}
		byActiveName[operation.ActiveName] = struct{}{}
		operations = append(operations, operation)
	}
	for index := range operations {
		options := operations[index].Options
		event, err := resolveAssociation(options.GetEvent(), byDeclaration)
		if err != nil {
			return nil, fmt.Errorf("%s event: %w", operations[index].Descriptor.FullName(), err)
		}
		completion, err := resolveAssociation(options.GetCompletion(), byDeclaration)
		if err != nil {
			return nil, fmt.Errorf("%s completion: %w", operations[index].Descriptor.FullName(), err)
		}
		operations[index].Event = event
		operations[index].Completion = completion
	}
	sort.Slice(operations, func(left, right int) bool {
		return operations[left].ActiveName < operations[right].ActiveName
	})
	return operations, nil
}

// OperationByName resolves only a normalized active operation name. Temporary
// legacy provenance is deliberately excluded from this index.
func OperationByName(activeName string) (Operation, bool, error) {
	operations, err := Operations()
	if err != nil {
		return Operation{}, false, err
	}
	index := sort.Search(len(operations), func(index int) bool {
		return operations[index].ActiveName >= activeName
	})
	if index == len(operations) || operations[index].ActiveName != activeName {
		return Operation{}, false, nil
	}
	return operations[index], true, nil
}

// OperationFromDescriptor validates typed method policy and derives its active
// name. Associations are resolved by Operations after every declaration is
// indexed.
func OperationFromDescriptor(
	descriptor protoreflect.MethodDescriptor,
	options *sharedpb.KentMethodOptions,
) (Operation, error) {
	if descriptor == nil {
		return Operation{}, fmt.Errorf("method descriptor is required")
	}
	if options == nil {
		return Operation{}, fmt.Errorf("%s method options are required", descriptor.FullName())
	}
	if err := ValidateKentMethodOptions(options); err != nil {
		return Operation{}, fmt.Errorf("%s: %w", descriptor.FullName(), err)
	}
	activeName, err := ActiveOperationName(
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
		ActiveName:     activeName,
		LegacyWireName: legacyWireName,
		Descriptor:     descriptor,
		Options:        options,
	}, nil
}

// ValidateKentMethodOptions rejects incomplete or contradictory descriptor
// policy.
func ValidateKentMethodOptions(options *sharedpb.KentMethodOptions) error {
	if options == nil {
		return fmt.Errorf("method options are required")
	}
	if options.Kind == sharedpb.OperationKind_OPERATION_KIND_UNSPECIFIED {
		return fmt.Errorf("operation kind is required")
	}
	switch options.AuthenticationStage {
	case sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_NONE,
		sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_PRE_SERVER,
		sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_SERVER:
	case sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_UNSPECIFIED:
		return fmt.Errorf("authentication stage is required")
	default:
		return fmt.Errorf("authentication stage %d is invalid", options.AuthenticationStage)
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
		sharedpb.ScopePolicy_SCOPE_POLICY_SESSION_ATTACHED_PROJECT,
		sharedpb.ScopePolicy_SCOPE_POLICY_ATTACHED_SESSION,
		sharedpb.ScopePolicy_SCOPE_POLICY_GOAL_SESSION,
		sharedpb.ScopePolicy_SCOPE_POLICY_RUNTIME_LIVE_SESSION_REQUIRED,
		sharedpb.ScopePolicy_SCOPE_POLICY_RUNTIME_LIVE_SESSION_OPTIONAL,
		sharedpb.ScopePolicy_SCOPE_POLICY_PROCESS_ACTIVE_PROJECT,
		sharedpb.ScopePolicy_SCOPE_POLICY_PROCESS_LIST_ACTIVE_PROJECT,
		sharedpb.ScopePolicy_SCOPE_POLICY_NOTIFICATION:
	case sharedpb.ScopePolicy_SCOPE_POLICY_UNSPECIFIED:
		return fmt.Errorf("scope policy is required")
	default:
		return fmt.Errorf("scope policy %d is invalid", options.ScopePolicy)
	}
	switch options.Direction {
	case sharedpb.Direction_DIRECTION_CLIENT_TO_SERVER,
		sharedpb.Direction_DIRECTION_SERVER_TO_CLIENT:
	case sharedpb.Direction_DIRECTION_UNSPECIFIED:
		return fmt.Errorf("direction is required")
	default:
		return fmt.Errorf("direction %d is invalid", options.Direction)
	}
	switch options.Kind {
	case sharedpb.OperationKind_OPERATION_KIND_UNARY:
		switch options.UnaryConnection {
		case sharedpb.UnaryConnection_UNARY_CONNECTION_MULTIPLEXED,
			sharedpb.UnaryConnection_UNARY_CONNECTION_DEDICATED:
		case sharedpb.UnaryConnection_UNARY_CONNECTION_UNSPECIFIED:
			return fmt.Errorf("unary connection is required for unary operation")
		default:
			return fmt.Errorf("unary connection %d is invalid", options.UnaryConnection)
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
		return fmt.Errorf("operation kind %d is invalid", options.Kind)
	}
	return nil
}

func kentMethodOptions(descriptor protoreflect.MethodDescriptor) (*sharedpb.KentMethodOptions, error) {
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

func resolveAssociation(
	declaration *sharedpb.OperationAssociation,
	descriptors map[protoreflect.FullName]protoreflect.MethodDescriptor,
) (*OperationAssociation, error) {
	if declaration == nil {
		return nil, nil
	}
	if err := ValidatePackageName(declaration.Package); err != nil {
		return nil, fmt.Errorf("package: %w", err)
	}
	if _, err := PascalCaseToLowerSnake(declaration.Service); err != nil {
		return nil, fmt.Errorf("service: %w", err)
	}
	if _, err := PascalCaseToLowerSnake(declaration.Method); err != nil {
		return nil, fmt.Errorf("method: %w", err)
	}
	fullName := protoreflect.FullName(declaration.Package + "." + declaration.Service + "." + declaration.Method)
	descriptor, exists := descriptors[fullName]
	if !exists {
		return nil, fmt.Errorf("method declaration %q does not exist", fullName)
	}
	activeName, err := ActiveOperationName(
		string(descriptor.ParentFile().Package()),
		string(descriptor.Parent().Name()),
		string(descriptor.Name()),
	)
	if err != nil {
		return nil, err
	}
	return &OperationAssociation{ActiveName: activeName, Descriptor: descriptor}, nil
}

// ValidatePackageName applies the Server API package-segment character policy.
func ValidatePackageName(packageName string) error {
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

// PascalCaseToLowerSnake converts a Protobuf identifier using Kent's locked
// initialism-aware character state machine.
func PascalCaseToLowerSnake(identifier string) (string, error) {
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
			if isASCIILower(previous) || isASCIIDigit(previous) || (isASCIIUpper(previous) && hasFollowingLower) {
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

// ActiveOperationName derives <package>.<service>.<method>. Service is never
// elided.
func ActiveOperationName(packageName string, service string, method string) (string, error) {
	if err := ValidatePackageName(packageName); err != nil {
		return "", err
	}
	serviceName, err := PascalCaseToLowerSnake(service)
	if err != nil {
		return "", fmt.Errorf("service: %w", err)
	}
	methodName, err := PascalCaseToLowerSnake(method)
	if err != nil {
		return "", fmt.Errorf("method: %w", err)
	}
	result := make([]byte, 0, len(packageName)+len(serviceName)+len(methodName)+2)
	result = append(result, packageName...)
	result = append(result, '.')
	result = append(result, serviceName...)
	result = append(result, '.')
	result = append(result, methodName...)
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

// Files enumerates the generated Server API file descriptors in schema order.
func Files() iter.Seq[protoreflect.FileDescriptor] {
	return registry.Files
}

// File returns the generated Server API file descriptor at path.
func File(path string) (protoreflect.FileDescriptor, bool) {
	return registry.File(path)
}

// DescriptorPaths returns the complete generated Server API descriptor set in
// schema-path order.
func DescriptorPaths() []string {
	paths := make([]string, 0)
	for path := range registry.Paths {
		paths = append(paths, path)
	}
	return paths
}
