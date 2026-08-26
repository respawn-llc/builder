package transport

import (
	"reflect"
	"testing"

	"core/shared/protoapi"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestWorktreeGatewayBinaryBindingScopes(t *testing.T) {
	bindings := map[string]gatewayBinaryBinding{}
	if err := registerWorktreeGatewayBinaryBindings(bindings); err != nil {
		t.Fatalf("registerWorktreeGatewayBinaryBindings: %v", err)
	}

	tests := []struct {
		name    string
		service protoreflect.ServiceDescriptor
		method  protoreflect.Name
		request proto.Message
		want    routeScopeParams
	}{
		{
			name:    "session request",
			service: worktreepb.File_kent_api_worktree_worktree_proto.Services().ByName("StatusService"),
			method:  "Get",
			request: &worktreepb.StatusRequest{SessionId: "session-1"},
			want:    routeScopeParams{sessionID: "session-1"},
		},
		{
			name:    "workspace request",
			service: worktreepb.File_kent_api_worktree_worktree_proto.Services().ByName("ListService"),
			method:  "ListWorkspace",
			request: &worktreepb.WorkspaceListRequest{ProjectId: "project-1", WorkspaceId: "workspace-1"},
			want:    routeScopeParams{projectID: "project-1", workspaceID: "workspace-1"},
		},
		{
			name:    "transition request",
			service: worktreepb.File_kent_api_worktree_worktree_proto.Services().ByName("TransitionService"),
			method:  "Enter",
			request: &worktreepb.EnterRequest{
				Transition: &worktreepb.TransitionHeader{SessionId: "session-2"},
			},
			want: routeScopeParams{sessionID: "session-2"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			method := test.service.Methods().ByName(test.method)
			operation, err := protoapi.OperationFromDescriptor(method)
			if err != nil {
				t.Fatal(err)
			}
			binding, ok := bindings[operation.Name]
			if !ok {
				t.Fatalf("binding %q is missing", operation.Name)
			}
			got, err := binding.scope(test.request)
			if err != nil {
				t.Fatalf("scope: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("scope = %#v, want %#v", got, test.want)
			}
		})
	}
}
