package transport

import (
	"errors"
	"testing"

	"core/shared/apicontract"
	"core/shared/protocol"
)

func TestRegisteredInboundOperationsHaveExactlyOneHandler(t *testing.T) {
	routes := apicontract.Routes()
	handlers := currentGatewayHandlerKinds()
	if err := validateGatewayHandlerCoverage(routes, handlers); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayHandlerCoverageRejectsIncompleteOrAmbiguousInventory(t *testing.T) {
	baseRoutes := apicontract.Routes()
	baseHandlers := currentGatewayHandlerKinds()

	tests := []struct {
		name     string
		routes   func() []apicontract.Route
		handlers func() map[string][]apicontract.Kind
	}{
		{
			name:   "missing handler",
			routes: func() []apicontract.Route { return append([]apicontract.Route(nil), baseRoutes...) },
			handlers: func() map[string][]apicontract.Kind {
				handlers := cloneGatewayHandlerKinds(baseHandlers)
				delete(handlers, protocol.MethodHandshake)
				return handlers
			},
		},
		{
			name:   "orphan handler",
			routes: func() []apicontract.Route { return append([]apicontract.Route(nil), baseRoutes...) },
			handlers: func() map[string][]apicontract.Kind {
				handlers := cloneGatewayHandlerKinds(baseHandlers)
				handlers["test.orphan"] = []apicontract.Kind{apicontract.KindUnary}
				return handlers
			},
		},
		{
			name:   "wrong kind handler",
			routes: func() []apicontract.Route { return append([]apicontract.Route(nil), baseRoutes...) },
			handlers: func() map[string][]apicontract.Kind {
				handlers := cloneGatewayHandlerKinds(baseHandlers)
				handlers[protocol.MethodHandshake] = []apicontract.Kind{apicontract.KindSubscription}
				return handlers
			},
		},
		{
			name: "duplicate inbound registration",
			routes: func() []apicontract.Route {
				routes := append([]apicontract.Route(nil), baseRoutes...)
				handshake, ok := apicontract.RouteByMethod(protocol.MethodHandshake)
				if !ok {
					t.Fatal("handshake route is not registered")
				}
				return append(routes, handshake)
			},
			handlers: func() map[string][]apicontract.Kind {
				return cloneGatewayHandlerKinds(baseHandlers)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateGatewayHandlerCoverage(test.routes(), test.handlers()); err == nil {
				t.Fatal("invalid route and handler inventory was accepted")
			}
		})
	}
}

func currentGatewayHandlerKinds() map[string][]apicontract.Kind {
	handlers := make(map[string][]apicontract.Kind)
	for method := range gatewayUnaryHandlerEntries {
		handlers[method] = append(handlers[method], apicontract.KindUnary)
	}
	for method := range gatewayProgressHandlerEntries {
		handlers[method] = append(handlers[method], apicontract.KindProgress)
	}
	for method := range gatewaySubscriptionHandlerEntries {
		handlers[method] = append(handlers[method], apicontract.KindSubscription)
	}
	return handlers
}

func cloneGatewayHandlerKinds(source map[string][]apicontract.Kind) map[string][]apicontract.Kind {
	clone := make(map[string][]apicontract.Kind, len(source))
	for method, kinds := range source {
		clone[method] = append([]apicontract.Kind(nil), kinds...)
	}
	return clone
}

func validateGatewayHandlerCoverage(routes []apicontract.Route, handlers map[string][]apicontract.Kind) error {
	inbound := make(map[string]apicontract.Kind)
	for _, route := range routes {
		if route.Kind == apicontract.KindNotification {
			continue
		}
		if _, exists := inbound[route.Method]; exists {
			return errors.New("duplicate inbound operation registration")
		}
		inbound[route.Method] = route.Kind
	}
	for method, kind := range inbound {
		kinds := handlers[method]
		if len(kinds) != 1 || kinds[0] != kind {
			return errors.New("inbound operation does not have exactly one same-kind handler")
		}
	}
	for method, kinds := range handlers {
		kind, exists := inbound[method]
		if !exists || len(kinds) != 1 || kinds[0] != kind {
			return errors.New("handler does not have exactly one same-kind inbound operation")
		}
	}
	return nil
}
