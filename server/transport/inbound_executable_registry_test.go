package transport

import (
	"reflect"
	"testing"

	"core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/serverapi"
)

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
}

func TestInboundExecutableRegistryUsesPreparedCustomDecoders(t *testing.T) {
	tests := []struct {
		method string
		want   requestDecoderKind
	}{
		{method: protocol.MethodOnboardingFinalize, want: requestDecoderOnboardingFinalize},
		{method: protocol.MethodSessionGetExecutionEnvironment, want: requestDecoderSessionExecutionEnvironment},
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
