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
