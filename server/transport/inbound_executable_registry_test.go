package transport

import (
	"context"
	"reflect"
	"testing"

	"core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/serverapi"
)

type notificationRejectingGatewayDependencies struct {
	GatewayDependencies
	availabilityChecks int
}

func (d *notificationRejectingGatewayDependencies) RouteDependencyAvailable(apicontract.Dependency) error {
	d.availabilityChecks++
	return nil
}

func TestInboundExecutableRegistryExhaustivelyPartitionsRouteCatalog(t *testing.T) {
	if err := validateInboundExecutableRegistry(); err != nil {
		t.Fatal(err)
	}

	for _, route := range apicontract.Routes() {
		executable, registered := inboundExecutableRoutes[route.Method]
		switch route.Kind {
		case apicontract.KindUnary, apicontract.KindProgress, apicontract.KindSubscription:
			if !registered {
				t.Errorf("inbound route %q has no executable registration", route.Method)
				continue
			}
			if executable.route.Method != route.Method ||
				executable.route.Kind != route.Kind ||
				executable.requestType != route.RequestType {
				t.Errorf("route %q executable metadata does not match shared route metadata", route.Method)
			}
		case apicontract.KindNotification:
			if registered {
				t.Errorf("outbound notification %q has an inbound executable registration", route.Method)
			}
		default:
			t.Errorf("route %q has unsupported kind %q", route.Method, route.Kind)
		}
	}
}

func TestGatewayRejectsEveryOutboundNotificationBeforeDependencyExecution(t *testing.T) {
	deps := &notificationRejectingGatewayDependencies{}
	gateway := &Gateway{deps: deps}
	state := &connectionState{handshakeDone: true}

	notificationCount := 0
	for _, route := range apicontract.Routes() {
		if route.Kind != apicontract.KindNotification {
			continue
		}
		notificationCount++
		t.Run(route.Method, func(t *testing.T) {
			response := gateway.dispatch(context.Background(), state, protocol.Request{
				JSONRPC: protocol.JSONRPCVersion,
				ID:      "notification-request",
				Method:  route.Method,
				Params:  []byte(`{"malformed-notification-payload":true}`),
			})
			if response.Error == nil || response.Error.Code != protocol.ErrCodeMethodNotFound {
				t.Fatalf("response = %+v, want method-not-found", response)
			}
		})
	}

	if notificationCount == 0 {
		t.Fatal("shared route catalog contains no outbound notifications")
	}
	if deps.availabilityChecks != 0 {
		t.Fatalf("notification requests reached dependency availability %d times", deps.availabilityChecks)
	}
}

func TestInboundExecutableRegistryDeclaresValidationAndTypedAuthorization(t *testing.T) {
	for method, executable := range inboundExecutableRoutes {
		switch executable.validation {
		case apicontract.SemanticValidationRequired:
			if executable.validator == requestValidatorNone {
				t.Errorf("semantic route %q has no semantic validator", method)
			}
		case apicontract.NoSemanticValidation:
			if executable.validator != requestValidatorNone {
				t.Errorf("no-semantic route %q bypasses available validator %d", method, executable.validator)
			}
		default:
			t.Errorf("route %q has invalid validation policy %d", method, executable.validation)
		}
		if executable.authorizationType == nil {
			t.Errorf("route %q has no exact authorization-result type", method)
		}
	}

	worktree := inboundExecutableRoutes[protocol.MethodWorktreeWorkspaceList]
	if worktree.authorizationType != reflect.TypeOf(apicontract.AuthorizedProjectWorkspaceBinding{}) {
		t.Fatalf("worktree Workspace list authorization type = %v, want AuthorizedProjectWorkspaceBinding", worktree.authorizationType)
	}
	if worktree.authorizationType == reflect.TypeOf(noAuthorizationFacts{}) {
		t.Fatal("worktree Workspace list registered with zero authorization facts")
	}

	attachSession := inboundExecutableRoutes[protocol.MethodAttachSession]
	if attachSession.authorizationType != reflect.TypeOf(apicontract.AuthorizedSessionAttachment{}) {
		t.Fatalf("Attach Session authorization type = %v, want AuthorizedSessionAttachment", attachSession.authorizationType)
	}
	if attachSession.authorizationType == reflect.TypeOf(noAuthorizationFacts{}) {
		t.Fatal("Attach Session registered with zero authorization facts")
	}

	retarget := inboundExecutableRoutes[protocol.MethodSessionRetargetWorkspace]
	if retarget.authorizationType != reflect.TypeOf(apicontract.AttachedProjectConstraint{}) {
		t.Fatalf("Session Retarget authorization type = %v, want AttachedProjectConstraint", retarget.authorizationType)
	}
	if retarget.authorizationType == reflect.TypeOf(noAuthorizationFacts{}) {
		t.Fatal("Session Retarget registered with zero attached-Project constraint")
	}

	for _, method := range []string{
		protocol.MethodProcessGet,
		protocol.MethodProcessKill,
		protocol.MethodProcessInlineOutput,
		protocol.MethodProcessSubscribeOutput,
	} {
		process := inboundExecutableRoutes[method]
		if process.authorizationType != reflect.TypeOf(apicontract.AuthorizedProcessInActiveProject{}) {
			t.Errorf("%s authorization type = %v, want AuthorizedProcessInActiveProject", method, process.authorizationType)
		}
		if process.authorizationType == reflect.TypeOf(noAuthorizationFacts{}) {
			t.Errorf("%s registered with zero authorization facts", method)
		}
	}

	for _, route := range apicontract.Routes() {
		executable, registered := inboundExecutableRoutes[route.Method]
		if !registered {
			continue
		}
		switch route.Scope {
		case apicontract.ScopeSessionActiveProject:
			if executable.authorizationType != reflect.TypeOf(apicontract.AuthorizedSessionInActiveProject{}) {
				t.Errorf("%s authorization type = %v, want AuthorizedSessionInActiveProject", route.Method, executable.authorizationType)
			}
			if executable.authorizationType == reflect.TypeOf(noAuthorizationFacts{}) {
				t.Errorf("%s registered with zero authorization facts", route.Method)
			}
		case apicontract.ScopeSessionActiveProjectIfSet:
			if executable.authorizationType != reflect.TypeOf(apicontract.OptionalAuthorizedSessionInActiveProject{}) {
				t.Errorf("%s authorization type = %v, want OptionalAuthorizedSessionInActiveProject", route.Method, executable.authorizationType)
			}
			if executable.authorizationType == reflect.TypeOf(noAuthorizationFacts{}) {
				t.Errorf("%s registered with zero authorization facts", route.Method)
			}
		}
	}
}

func TestInboundExecutableRegistryUsesPreparedCustomDecoders(t *testing.T) {
	tests := []struct {
		method string
		want   requestDecoderKind
	}{
		{method: protocol.MethodOnboardingFinalize, want: requestDecoderOnboardingFinalize},
		{method: protocol.MethodSessionGetExecutionEnvironment, want: requestDecoderSessionExecutionEnvironment},
		{method: protocol.MethodRunPrompt, want: requestDecoderDefault},
		{method: protocol.MethodSessionPlan, want: requestDecoderDefault},
		{method: protocol.MethodAttachProject, want: requestDecoderDefault},
		{method: protocol.MethodSessionWorkspaceChatDraft, want: requestDecoderDefault},
	}
	for _, test := range tests {
		if got := inboundExecutableRoutes[test.method].decoder; got != test.want {
			t.Errorf("%s decoder = %v, want %v", test.method, got, test.want)
		}
	}

	if got := inboundExecutableRoutes[protocol.MethodWorktreeWorkspaceList].requestType; got != reflect.TypeOf(serverapi.WorktreeWorkspaceListRequest{}) {
		t.Fatalf("worktree Workspace list request type = %v", got)
	}
}

func TestOptionalActiveProjectSessionAuthorizationSkipsAllDependenciesWhenSessionIsAbsent(t *testing.T) {
	request := serverapi.SessionInitialInputRequest{}
	_, err := apicontract.WithValidated(
		request,
		apicontract.SemanticValidationRequired,
		func(validated apicontract.Validated[serverapi.SessionInitialInputRequest]) (struct{}, error) {
			authorization, err := authorizeOptionalSessionActiveProject(
				func(req serverapi.SessionInitialInputRequest) string { return req.SessionID },
			)(t.Context(), nil, nil, validated)
			if err != nil {
				return struct{}{}, err
			}
			if _, present := authorization.Authorization(); present {
				t.Fatal("absent Session request produced present authorization")
			}
			return struct{}{}, nil
		},
	)
	if err != nil {
		t.Fatalf("authorize optional absent Session: %v", err)
	}
}
