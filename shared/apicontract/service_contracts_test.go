package apicontract

import (
	"context"
	"reflect"
	"testing"

	"core/shared/protocol"
)

func TestServiceContractsCoverEveryNonProtocolRoute(t *testing.T) {
	interfaces := map[Dependency]reflect.Type{
		DependencyApprovalView:          typeOfService[ApprovalViewService](),
		DependencyAskView:               typeOfService[AskViewService](),
		DependencyAuthBootstrap:         typeOfService[AuthBootstrapService](),
		DependencyAuthStatus:            typeOfService[AuthStatusService](),
		DependencyProcessControl:        typeOfService[ProcessControlService](),
		DependencyProcessOutput:         typeOfService[ProcessOutputService](),
		DependencyProcessView:           typeOfService[ProcessViewService](),
		DependencyProjectView:           typeOfService[ProjectViewService](),
		DependencyPromptActivity:        typeOfService[PromptActivityService](),
		DependencyAttentionNotification: typeOfService[AttentionNotificationService](),
		DependencyPromptControl:         typeOfService[PromptControlService](),
		DependencyRunPrompt:             typeOfService[RunPromptService](),
		DependencyServerStatus:          typeOfService[ServerStatusService](),
		DependencyRuntimeControl:        typeOfService[RuntimeControlService](),
		DependencySessionActivity:       typeOfService[SessionActivityService](),
		DependencySessionLaunch:         typeOfService[SessionLaunchService](),
		DependencySessionLifecycle:      typeOfService[SessionLifecycleService](),
		DependencySessionRuntime:        typeOfService[SessionRuntimeService](),
		DependencySessionView:           typeOfService[SessionViewService](),
		DependencyWorktree:              typeOfService[WorktreeService](),
		DependencyWorkflow:              typeOfService[WorkflowService](),
	}
	for _, route := range Routes() {
		if route.Kind == KindNotification || route.Dependency == DependencyProtocol || route.Dependency == DependencyStreamNotification {
			continue
		}
		serviceType, ok := interfaces[route.Dependency]
		if !ok {
			t.Fatalf("route %q dependency %q missing service contract interface", route.Method, route.Dependency)
		}
		if !serviceTypeHasRouteMethod(serviceType, route) {
			t.Fatalf("route %q request/response types missing from service contract %s", route.Method, serviceType.Name())
		}
	}
}

func typeOfService[T any]() reflect.Type {
	return reflect.TypeOf((*T)(nil)).Elem()
}

func serviceTypeHasRouteMethod(serviceType reflect.Type, route Route) bool {
	contextType := reflect.TypeOf((*context.Context)(nil)).Elem()
	errorType := reflect.TypeOf((*error)(nil)).Elem()
	emptyResponseType := reflect.TypeOf(struct{}{})
	subscribeResponseType := reflect.TypeOf(protocol.SubscribeResponse{})
	for i := 0; i < serviceType.NumMethod(); i++ {
		method := serviceType.Method(i)
		if method.Type.NumIn() < 2 || method.Type.In(0) != contextType || method.Type.In(1) != route.RequestType {
			continue
		}
		if method.Type.NumOut() == 0 || method.Type.Out(method.Type.NumOut()-1) != errorType {
			continue
		}
		if route.Kind == KindSubscription {
			return true
		}
		if route.ResponseType == emptyResponseType {
			return method.Type.NumOut() == 1
		}
		if route.ResponseType == subscribeResponseType {
			return true
		}
		return method.Type.NumOut() >= 2 && method.Type.Out(0) == route.ResponseType
	}
	return false
}
