package apicontract

import (
	"context"
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
		route.Connection != ConnectionControl ||
		route.Dependency != DependencyPromptControl {
		t.Fatalf("prompt answer batch route metadata = %+v", route)
	}
	if route.RequestType != reflect.TypeOf(serverapi.PromptAnswerBatchRequest{}) ||
		route.ResponseType != reflect.TypeOf(serverapi.PromptAnswerBatchResponse{}) ||
		!route.ValidatesRequest {
		t.Fatalf("prompt answer batch route types = %v/%v validates=%t", route.RequestType, route.ResponseType, route.ValidatesRequest)
	}
}

func TestPromptControlServiceExposesTypedBatchResponse(t *testing.T) {
	serviceType := reflect.TypeOf((*PromptControlService)(nil)).Elem()
	method, ok := serviceType.MethodByName("AnswerPromptBatch")
	if !ok {
		t.Fatal("PromptControlService missing AnswerPromptBatch")
	}
	want := reflect.TypeOf(func(context.Context, serverapi.PromptAnswerBatchRequest) (serverapi.PromptAnswerBatchResponse, error) {
		return serverapi.PromptAnswerBatchResponse{}, nil
	})
	if method.Type != want {
		t.Fatalf("AnswerPromptBatch type = %v, want %v", method.Type, want)
	}
}
