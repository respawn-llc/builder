package apicontract

import (
	"reflect"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestQuestionHistoryRouteContract(t *testing.T) {
	route, ok := RouteByMethod(protocol.MethodSessionQuestionHistorySubscribe)
	if !ok {
		t.Fatal("Question-history route is missing")
	}
	if route.Kind != KindSubscription ||
		route.Scope != ScopeAttachedSession ||
		route.RequestType != reflect.TypeOf(serverapi.QuestionHistorySubscribeRequest{}) ||
		route.EventType != reflect.TypeOf(protocol.SessionQuestionHistoryEventParams{}) ||
		route.EventMethod != protocol.MethodSessionQuestionHistoryEvent ||
		route.CompleteMethod != protocol.MethodSessionQuestionHistoryComplete {
		t.Fatalf("Question-history route = %#v", route)
	}
}
