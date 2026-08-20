package apicontract

import (
	"reflect"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestPromptAnswerBatchRouteContract(t *testing.T) {
	route, ok := RouteByMethod(protocol.MethodPromptAnswerBatch)
	if !ok {
		t.Fatal("prompt answer batch route missing")
	}
	if route.Kind != KindUnary ||
		route.Auth != AuthServer ||
		route.Scope != ScopeSessionActiveProject ||
		route.Connection != ConnectionControl {
		t.Fatalf("prompt answer batch route metadata = %+v", route)
	}
	if route.RequestType != reflect.TypeOf(serverapi.PromptAnswerBatchRequest{}) ||
		route.ResponseType != reflect.TypeOf(serverapi.PromptAnswerBatchResponse{}) ||
		!route.ValidatesRequest {
		t.Fatalf("prompt answer batch route types = %v/%v validates=%t", route.RequestType, route.ResponseType, route.ValidatesRequest)
	}
}

func TestPromptFollowUpRouteIsARegistrationAcknowledgedTerminalStream(t *testing.T) {
	route, ok := RouteByMethod(protocol.MethodPromptFollowUpWatch)
	if !ok {
		t.Fatal("prompt follow-up route missing")
	}
	if route.Kind != KindSubscription ||
		route.Auth != AuthServer ||
		route.Scope != ScopeSessionActiveProject ||
		route.Connection != ConnectionSubscription {
		t.Fatalf("prompt follow-up route metadata = %+v", route)
	}
	if route.RequestType != reflect.TypeOf(serverapi.PromptFollowUpWatchRequest{}) ||
		route.ResponseType != reflect.TypeOf(protocol.SubscribeResponse{}) ||
		route.EventType != reflect.TypeOf(protocol.PromptFollowUpEventParams{}) ||
		route.EventMethod != protocol.MethodPromptFollowUpEvent ||
		route.CompleteMethod != protocol.MethodPromptFollowUpComplete ||
		!route.ValidatesRequest {
		t.Fatalf("prompt follow-up route contract = %+v", route)
	}
}

func TestLegacyPromptAnswerRoutesStayRemoved(t *testing.T) {
	for _, method := range []string{"ask.answer", "approval.answer", "workflow.task.question.answer"} {
		if _, exists := RouteByMethod(method); exists {
			t.Fatalf("legacy prompt answer route %q is registered", method)
		}
	}
}
