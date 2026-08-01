package serverapi

import (
	"encoding/json"
	"errors"
	"testing"

	"core/shared/protocol"
)

func TestProjectWorkspaceTypedErrorsRoundTrip(t *testing.T) {
	cause := errors.New("dependency failed")
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "path identity",
			err:  WorkspacePathIdentityError{WorkspaceRoot: "/missing", Cause: cause},
		},
		{
			name: "detach conflict",
			err:  &WorkspaceDetachConflictError{ProjectID: "project-1", WorkspaceID: "workspace-1"},
		},
		{
			name: "mutation failure",
			err:  &WorkspaceMutationError{ProjectID: "project-1", WorkspaceID: "workspace-1", Cause: cause},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			structured, ok := test.err.(interface {
				RPCErrorCode() int
				RPCErrorData() json.RawMessage
			})
			if !ok {
				t.Fatalf("%T does not implement structured RPC error", test.err)
			}
			var decoded error
			switch structured.RPCErrorCode() {
			case protocol.ErrCodeWorkspacePathIdentity:
				decoded = DecodeWorkspacePathIdentityError(structured.RPCErrorData(), test.err.Error())
			case protocol.ErrCodeWorkspaceDetachConflict:
				decoded = DecodeWorkspaceDetachConflictError(structured.RPCErrorData(), test.err.Error())
			case protocol.ErrCodeWorkspaceMutationFailed:
				decoded = DecodeWorkspaceMutationError(structured.RPCErrorData(), test.err.Error())
			default:
				t.Fatalf("unexpected structured RPC code %d", structured.RPCErrorCode())
			}
			var pathIdentity WorkspacePathIdentityError
			var conflict *WorkspaceDetachConflictError
			var mutation *WorkspaceMutationError
			switch {
			case errors.As(test.err, &pathIdentity):
				if !errors.As(decoded, &pathIdentity) {
					t.Fatalf("decoded error = %T %v, want path identity", decoded, decoded)
				}
			case errors.As(test.err, &conflict):
				if !errors.As(decoded, &conflict) || conflict.ProjectID != "project-1" || conflict.WorkspaceID != "workspace-1" {
					t.Fatalf("decoded error = %T %+v, want conflict IDs", decoded, decoded)
				}
			case errors.As(test.err, &mutation):
				if !errors.As(decoded, &mutation) || mutation.ProjectID != "project-1" || mutation.WorkspaceID != "workspace-1" {
					t.Fatalf("decoded error = %T %+v, want mutation IDs", decoded, decoded)
				}
			}
		})
	}
}

func TestProjectWorkspaceRPCFallbackPreservesSentinel(t *testing.T) {
	err := DecodeWorkspaceMutationError(nil, "remote mutation failed")
	if !errors.Is(err, ErrWorkspaceMutationFailed) {
		t.Fatalf("fallback error = %v, want mutation sentinel", err)
	}
}

func TestProjectWorkspaceTypedErrorsTreatBlankRPCMessagesAsAbsentCauses(t *testing.T) {
	pathErr := DecodeWorkspacePathIdentityError(
		json.RawMessage(`{"type":"workspace_path_identity_error","workspace_root":"/missing"}`),
		"  ",
	)
	var pathIdentity WorkspacePathIdentityError
	if !errors.As(pathErr, &pathIdentity) || pathIdentity.Cause != nil {
		t.Fatalf("path identity error = %+v, want absent cause", pathErr)
	}
	mutationErr := DecodeWorkspaceMutationError(
		json.RawMessage(`{"type":"workspace_mutation_error","project_id":"project-1","workspace_id":"workspace-1"}`),
		"  ",
	)
	var mutation *WorkspaceMutationError
	if !errors.As(mutationErr, &mutation) || mutation.Cause != nil {
		t.Fatalf("mutation error = %+v, want absent cause", mutationErr)
	}
	if !errors.Is(mutationErr, ErrWorkspaceMutationFailed) {
		t.Fatalf("blank mutation error = %v, want mutation sentinel", mutationErr)
	}
}

func TestWorkspacePathIdentityDecoderPreservesRenderedRemoteMessage(t *testing.T) {
	message := `workspace path identity could not be recovered: "/missing": permission denied`
	err := DecodeWorkspacePathIdentityError(
		json.RawMessage(`{"type":"workspace_path_identity_error","workspace_root":"/missing"}`),
		message,
	)
	if err.Error() != message {
		t.Fatalf("decoded path identity error = %q, want %q", err.Error(), message)
	}
}
