package apicontract

import (
	"testing"
)

func TestOutboundNotificationMetadataRemainsAvailableForEveryStreamEmission(t *testing.T) {
	references := make(map[string]struct {
		sourceMethod string
		payloadType  any
	})

	for _, route := range Routes() {
		if route.EventMethod != "" {
			references[route.EventMethod] = struct {
				sourceMethod string
				payloadType  any
			}{sourceMethod: route.Method, payloadType: route.EventType}
		}
		if route.CompleteMethod != "" {
			references[route.CompleteMethod] = struct {
				sourceMethod string
				payloadType  any
			}{sourceMethod: route.Method, payloadType: route.CompleteType}
		}
	}

	notificationCount := 0
	for _, route := range Routes() {
		if route.Kind != KindNotification {
			continue
		}
		notificationCount++
		reference, ok := references[route.Method]
		if !ok {
			t.Errorf("outbound notification %q is not referenced by a progress or subscription route", route.Method)
			continue
		}
		if route.RequestType != reference.payloadType {
			t.Errorf(
				"outbound notification %q payload type = %v, want %v from %q",
				route.Method,
				route.RequestType,
				reference.payloadType,
				reference.sourceMethod,
			)
		}
		lookedUp, ok := RouteByMethod(route.Method)
		if !ok || lookedUp.Kind != KindNotification || lookedUp.RequestType != route.RequestType {
			t.Errorf("outbound notification %q is unavailable through route lookup", route.Method)
		}
	}

	if notificationCount == 0 {
		t.Fatal("shared route catalog contains no outbound notifications")
	}
	if len(references) != notificationCount {
		t.Fatalf("stream emission methods = %d, outbound notification routes = %d", len(references), notificationCount)
	}
}
