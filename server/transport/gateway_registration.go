package transport

import (
	"fmt"
	"sort"
	"strings"

	"core/shared/apicontract"
	"core/shared/protoapi"

	sharedpb "core/shared/protoapi/gen/kent/api/shared"
)

type gatewayRegistration struct {
	operations map[string]protoapi.Operation
	legacy     map[string]apicontract.Route
	binary     map[string]gatewayBinaryBinding
}

func productionGatewayRegistration() (gatewayRegistration, error) {
	operations, err := protoapi.Operations()
	if err != nil {
		return gatewayRegistration{}, err
	}
	registration := gatewayRegistration{
		operations: make(map[string]protoapi.Operation, len(operations)),
		legacy:     make(map[string]apicontract.Route),
	}
	legacyRoutes := make(map[string]apicontract.Route)
	for _, route := range apicontract.Routes() {
		if _, duplicate := legacyRoutes[route.Method]; duplicate {
			return gatewayRegistration{}, fmt.Errorf("duplicate legacy route %q", route.Method)
		}
		legacyRoutes[route.Method] = route
	}
	for _, operation := range operations {
		if _, duplicate := registration.operations[operation.Name]; duplicate {
			return gatewayRegistration{}, fmt.Errorf("duplicate descriptor operation %q", operation.Name)
		}
		registration.operations[operation.Name] = operation
		if operation.LegacyWireName == nil {
			continue
		}
		if route, exists := legacyRoutes[*operation.LegacyWireName]; exists {
			registration.legacy[operation.Name] = route
		}
	}
	registration.binary, err = productionGatewayBinaryBindings()
	if err != nil {
		return gatewayRegistration{}, err
	}
	return registration, nil
}

func (r gatewayRegistration) Validate() error {
	usedLegacyRoutes := make(map[string]string, len(r.legacy))
	for name, operation := range r.operations {
		binding, migrated := r.binary[name]
		route, legacy := r.legacy[name]
		if migrated == legacy {
			if migrated {
				return fmt.Errorf("operation %q has both binary and legacy authorities", name)
			}
			return fmt.Errorf("operation %q has no active authority", name)
		}
		if migrated {
			if operation.LegacyWireName != nil {
				return fmt.Errorf("migrated operation %q retains legacy provenance %q", name, *operation.LegacyWireName)
			}
			if err := validateBinaryBinding(operation, binding); err != nil {
				return err
			}
			continue
		}
		if operation.LegacyWireName == nil {
			return fmt.Errorf("legacy operation %q has no legacy provenance", name)
		}
		if route.Method != *operation.LegacyWireName {
			return fmt.Errorf(
				"legacy operation %q resolves route %q, want %q",
				name,
				route.Method,
				*operation.LegacyWireName,
			)
		}
		if previous, duplicate := usedLegacyRoutes[route.Method]; duplicate {
			return fmt.Errorf("legacy route %q resolves both %q and %q", route.Method, previous, name)
		}
		usedLegacyRoutes[route.Method] = name
		if err := validateLegacyRegistration(operation, route); err != nil {
			return err
		}
	}
	for name := range r.binary {
		if _, exists := r.operations[name]; !exists {
			return fmt.Errorf("binary binding %q has no descriptor operation", name)
		}
	}
	var unresolvedLegacyRoutes []string
	for _, route := range apicontract.Routes() {
		if _, used := usedLegacyRoutes[route.Method]; !used {
			unresolvedLegacyRoutes = append(unresolvedLegacyRoutes, route.Method)
		}
	}
	if len(unresolvedLegacyRoutes) > 0 {
		sort.Strings(unresolvedLegacyRoutes)
		return fmt.Errorf("legacy routes have no descriptor provenance: %s", strings.Join(unresolvedLegacyRoutes, ", "))
	}
	return nil
}

func (r gatewayRegistration) AllowedPreAuthMethods() []string {
	methods := make([]string, 0)
	for name, operation := range r.operations {
		if operation.Options.AuthenticationStage != sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_PRE_SERVER {
			continue
		}
		if _, migrated := r.binary[name]; migrated {
			methods = append(methods, name)
			continue
		}
		if route, legacy := r.legacy[name]; legacy {
			methods = append(methods, route.Method)
		}
	}
	sort.Strings(methods)
	return methods
}

func (r gatewayRegistration) LegacyOperation(method string) (protoapi.Operation, apicontract.Route, bool) {
	for name, route := range r.legacy {
		if route.Method == method {
			return r.operations[name], route, true
		}
	}
	return protoapi.Operation{}, apicontract.Route{}, false
}

func (r gatewayRegistration) BinaryBinding(operation string) (gatewayBinaryBinding, bool) {
	binding, exists := r.binary[operation]
	return binding, exists
}

func validateBinaryBinding(operation protoapi.Operation, binding gatewayBinaryBinding) error {
	if binding.operation.Name != operation.Name ||
		binding.operation.Descriptor.FullName() != operation.Descriptor.FullName() {
		return fmt.Errorf("binary binding %q has a mismatched method descriptor", operation.Name)
	}
	if binding.invoke == nil {
		return fmt.Errorf("binary binding %q has no handler", operation.Name)
	}
	if binding.request == nil {
		return fmt.Errorf("binary binding %q has no request constructor", operation.Name)
	}
	switch binding.policy {
	case gatewayBinaryPreCoreOrdinary,
		gatewayBinaryPreCoreExclusive,
		gatewayBinaryCoreActiveOrdinary,
		gatewayBinaryCoreActiveExclusive:
	default:
		return fmt.Errorf("binary binding %q has no execution policy", operation.Name)
	}
	request := binding.request()
	if request == nil || request.ProtoReflect().Descriptor().FullName() != operation.Descriptor.Input().FullName() {
		return fmt.Errorf("binary binding %q has a mismatched request type", operation.Name)
	}
	if binding.failure == nil {
		return fmt.Errorf("binary binding %q has no failure mapper", operation.Name)
	}
	if operation.Options.Kind != sharedpb.OperationKind_OPERATION_KIND_UNARY {
		return fmt.Errorf("binary binding %q has unsupported operation kind %s", operation.Name, operation.Options.Kind)
	}
	return nil
}

func validateLegacyRegistration(operation protoapi.Operation, route apicontract.Route) error {
	if routeKind(operation.Options.Kind) != route.Kind {
		return fmt.Errorf(
			"legacy route %q kind %q does not match descriptor kind %q",
			route.Method,
			route.Kind,
			operation.Options.Kind,
		)
	}
	if routeAuthPolicy(operation.Options.AuthenticationStage) != route.Auth {
		return fmt.Errorf(
			"legacy route %q auth %q does not match descriptor auth %q",
			route.Method,
			route.Auth,
			operation.Options.AuthenticationStage,
		)
	}
	if routeScopePolicy(operation.Options.ScopePolicy) != route.Scope {
		return fmt.Errorf(
			"legacy route %q scope %q does not match descriptor scope %q",
			route.Method,
			route.Scope,
			operation.Options.ScopePolicy,
		)
	}
	switch route.Kind {
	case apicontract.KindUnary:
		if _, exists := gatewayUnaryHandlers[route.Method]; !exists {
			return fmt.Errorf("legacy unary route %q has no unary handler", route.Method)
		}
	case apicontract.KindProgress:
		if _, exists := gatewayProgressHandlers[route.Method]; !exists {
			return fmt.Errorf("legacy progress route %q has no progress handler", route.Method)
		}
	case apicontract.KindSubscription:
		if _, exists := gatewaySubscriptionHandlers[route.Method]; !exists {
			return fmt.Errorf("legacy subscription route %q has no subscription handler", route.Method)
		}
	case apicontract.KindNotification:
		if operation.Options.Direction != sharedpb.Direction_DIRECTION_SERVER_TO_CLIENT {
			return fmt.Errorf("legacy notification route %q is not server-to-client", route.Method)
		}
	default:
		return fmt.Errorf("legacy route %q has unsupported kind %q", route.Method, route.Kind)
	}
	return nil
}

func routeKind(kind sharedpb.OperationKind) apicontract.Kind {
	switch kind {
	case sharedpb.OperationKind_OPERATION_KIND_UNARY:
		return apicontract.KindUnary
	case sharedpb.OperationKind_OPERATION_KIND_SUBSCRIPTION:
		return apicontract.KindSubscription
	case sharedpb.OperationKind_OPERATION_KIND_PROGRESS:
		return apicontract.KindProgress
	case sharedpb.OperationKind_OPERATION_KIND_NOTIFICATION:
		return apicontract.KindNotification
	default:
		return ""
	}
}

func routeAuthPolicy(stage sharedpb.AuthenticationStage) apicontract.AuthPolicy {
	switch stage {
	case sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_NONE:
		return apicontract.AuthNone
	case sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_PRE_SERVER:
		return apicontract.AuthPreServerAuth
	case sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_SERVER:
		return apicontract.AuthServer
	default:
		return ""
	}
}

func routeScopePolicy(scope sharedpb.ScopePolicy) apicontract.ScopePolicy {
	switch scope {
	case sharedpb.ScopePolicy_SCOPE_POLICY_NONE:
		return apicontract.ScopeNone
	case sharedpb.ScopePolicy_SCOPE_POLICY_ATTACH_PROJECT:
		return apicontract.ScopeAttachProject
	case sharedpb.ScopePolicy_SCOPE_POLICY_ATTACH_SESSION:
		return apicontract.ScopeAttachSession
	case sharedpb.ScopePolicy_SCOPE_POLICY_PROJECT_VIEW:
		return apicontract.ScopeProjectView
	case sharedpb.ScopePolicy_SCOPE_POLICY_PROJECT_WORKSPACE:
		return apicontract.ScopeProjectWorkspace
	case sharedpb.ScopePolicy_SCOPE_POLICY_PROJECT_WORKSPACE_BINDING:
		return apicontract.ScopeProjectWorkspaceBinding
	case sharedpb.ScopePolicy_SCOPE_POLICY_SESSION_ACTIVE_PROJECT:
		return apicontract.ScopeSessionActiveProject
	case sharedpb.ScopePolicy_SCOPE_POLICY_SESSION_ACTIVE_PROJECT_IF_SET:
		return apicontract.ScopeSessionActiveProjectIfSet
	case sharedpb.ScopePolicy_SCOPE_POLICY_SESSION_ATTACHED_PROJECT:
		return apicontract.ScopeSessionAttachedProject
	case sharedpb.ScopePolicy_SCOPE_POLICY_ATTACHED_SESSION:
		return apicontract.ScopeAttachedSession
	case sharedpb.ScopePolicy_SCOPE_POLICY_GOAL_SESSION:
		return apicontract.ScopeGoalSession
	case sharedpb.ScopePolicy_SCOPE_POLICY_RUNTIME_LIVE_SESSION_REQUIRED:
		return apicontract.ScopeRuntimeLiveSessionRequired
	case sharedpb.ScopePolicy_SCOPE_POLICY_RUNTIME_LIVE_SESSION_OPTIONAL:
		return apicontract.ScopeRuntimeLiveSessionOptional
	case sharedpb.ScopePolicy_SCOPE_POLICY_PROCESS_ACTIVE_PROJECT:
		return apicontract.ScopeProcessActiveProject
	case sharedpb.ScopePolicy_SCOPE_POLICY_PROCESS_LIST_ACTIVE_PROJECT:
		return apicontract.ScopeProcessListActiveProject
	case sharedpb.ScopePolicy_SCOPE_POLICY_NOTIFICATION:
		return apicontract.ScopeNotification
	default:
		return ""
	}
}
