package apicontract

import (
	"reflect"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestTranscriptSubscriptionRouteContract(t *testing.T) {
	route, ok := RouteByMethod(protocol.MethodSessionSubscribeTranscript)
	if !ok {
		t.Fatal("transcript subscription route missing")
	}
	if route.Kind != KindSubscription || route.Auth != AuthServer || route.Scope != ScopeAttachedSession || route.Connection != ConnectionSubscription || route.Dependency != DependencySessionTranscript {
		t.Fatalf("transcript subscription route = %+v", route)
	}
	if route.RequestType != reflect.TypeOf(serverapi.TranscriptSubscribeRequest{}) {
		t.Fatalf("transcript subscription request type = %v, want TranscriptSubscribeRequest", route.RequestType)
	}
	if route.EventType != reflect.TypeOf(protocol.SessionTranscriptEventParams{}) {
		t.Fatalf("transcript subscription event type = %v, want SessionTranscriptEventParams", route.EventType)
	}
	if route.EventMethod != protocol.MethodSessionTranscriptEvent || route.CompleteMethod != protocol.MethodSessionTranscriptComplete {
		t.Fatalf("transcript subscription event methods = %q / %q", route.EventMethod, route.CompleteMethod)
	}
	if !route.ValidatesRequest {
		t.Fatal("transcript subscription route must validate its request")
	}
}

func TestTranscriptSubscriptionRequestHasNoCursor(t *testing.T) {
	typ := reflect.TypeOf(serverapi.TranscriptSubscribeRequest{})
	if typ.NumField() != 1 || typ.Field(0).Name != "SessionID" {
		t.Fatalf("TranscriptSubscribeRequest fields = %v, want only SessionID", typ)
	}
	activity, ok := RouteByMethod(protocol.MethodSessionSubscribeActivity)
	if !ok {
		t.Fatal("legacy session activity route missing")
	}
	if activity.RequestType != reflect.TypeOf(serverapi.SessionActivitySubscribeRequest{}) {
		t.Fatalf("session activity request type changed to %v", activity.RequestType)
	}
}

func TestLatestCommittedAssistantFinalAnswerRouteContract(t *testing.T) {
	route, ok := RouteByMethod(protocol.MethodSessionGetLatestCommittedAssistantFinalAnswer)
	if !ok {
		t.Fatal("latest committed assistant final answer route missing")
	}
	if route.Kind != KindUnary || route.Auth != AuthPreServerAuth || route.Scope != ScopeSessionActiveProject || route.Connection != ConnectionControl || route.Dependency != DependencySessionView {
		t.Fatalf("route = %+v", route)
	}
	if route.RequestType != reflect.TypeOf(serverapi.SessionLatestCommittedAssistantFinalAnswerRequest{}) || route.ResponseType != reflect.TypeOf(serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{}) {
		t.Fatalf("route request/response = %v / %v", route.RequestType, route.ResponseType)
	}
	if !route.ValidatesRequest {
		t.Fatal("latest committed assistant final answer route must validate its request")
	}
}
