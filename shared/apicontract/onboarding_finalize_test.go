package apicontract_test

import (
	"testing"

	"core/shared/apicontract"
	"core/shared/protocol"
)

func TestOnboardingFinalizeRouteMetadata(t *testing.T) {
	route, ok := apicontract.RouteByMethod(protocol.MethodOnboardingFinalize)
	if !ok {
		t.Fatal("onboarding finalize route is not registered")
	}
	if route.Kind != apicontract.KindUnary ||
		route.Auth != apicontract.AuthPreServerAuth ||
		route.Scope != apicontract.ScopeNone ||
		route.Dependency != apicontract.DependencyOnboardingFinalize ||
		route.Connection != apicontract.ConnectionUnscoped {
		t.Fatalf("unexpected onboarding finalize route metadata: %+v", route)
	}
}
