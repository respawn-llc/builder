package apicontract

import (
	"reflect"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestAttentionNotificationRouteContracts(t *testing.T) {
	desktop, ok := RouteByMethod(protocol.MethodAttentionNotificationSubscribe)
	if !ok {
		t.Fatal("desktop attention notification route missing")
	}
	if desktop.Kind != KindSubscription || desktop.Auth != AuthServer || desktop.Scope != ScopeNone || desktop.Connection != ConnectionSubscription || desktop.Dependency != DependencyAttentionNotification {
		t.Fatalf("desktop attention notification route = %+v", desktop)
	}
	if desktop.RequestType != reflect.TypeOf(serverapi.AttentionNotificationSubscribeRequest{}) || desktop.EventType != reflect.TypeOf(protocol.AttentionNotificationEventParams{}) {
		t.Fatalf("desktop attention notification route types = %v / %v", desktop.RequestType, desktop.EventType)
	}
	if desktop.EventMethod != protocol.MethodAttentionNotificationEvent || desktop.CompleteMethod != protocol.MethodAttentionNotificationComplete {
		t.Fatalf("desktop attention notification event methods = %q / %q", desktop.EventMethod, desktop.CompleteMethod)
	}

	session, ok := RouteByMethod(protocol.MethodAttentionSessionNotificationSubscribe)
	if !ok {
		t.Fatal("session attention notification route missing")
	}
	if session.Kind != KindSubscription || session.Auth != AuthServer || session.Scope != ScopeAttachedSession || session.Dependency != DependencyAttentionNotification {
		t.Fatalf("session attention notification route = %+v", session)
	}
	if session.RequestType != reflect.TypeOf(serverapi.AttentionSessionNotificationSubscribeRequest{}) || !session.ValidatesRequest {
		t.Fatalf("session attention notification route request = %v validates=%t", session.RequestType, session.ValidatesRequest)
	}
}
