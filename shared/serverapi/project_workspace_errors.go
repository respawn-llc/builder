package serverapi

import (
	"encoding/json"
	"errors"
	"strings"

	"core/shared/protocol"
)

var ErrWorkspaceDetachConflict = errors.New("workspace detach preparation conflicted")
var ErrWorkspaceMutationFailed = errors.New("workspace mutation failed")

func (e WorkspacePathIdentityError) RPCErrorCode() int {
	return protocol.ErrCodeWorkspacePathIdentity
}

func (e WorkspacePathIdentityError) RPCErrorData() json.RawMessage {
	return marshalRPCErrorData(struct {
		Type string `json:"type"`
	}{
		Type: "workspace_path_identity_error",
	})
}

type WorkspaceDetachConflictError struct {
	ProjectID   string
	WorkspaceID string
}

func (e *WorkspaceDetachConflictError) Error() string {
	if e == nil {
		return ErrWorkspaceDetachConflict.Error()
	}
	return "workspace detach preparation was invalidated"
}

func (e *WorkspaceDetachConflictError) Is(target error) bool {
	return target == ErrWorkspaceDetachConflict
}

func (e *WorkspaceDetachConflictError) RPCErrorCode() int {
	return protocol.ErrCodeWorkspaceDetachConflict
}

func (e *WorkspaceDetachConflictError) RPCErrorData() json.RawMessage {
	if e == nil || !validWorkspaceMutationIdentity(e.ProjectID, e.WorkspaceID) {
		return nil
	}
	return marshalWorkspaceMutationIdentity("workspace_detach_conflict_error", e.ProjectID, e.WorkspaceID)
}

type WorkspaceMutationError struct {
	ProjectID   string
	WorkspaceID string
	Cause       error
}

func (e *WorkspaceMutationError) Error() string {
	if e == nil {
		return ErrWorkspaceMutationFailed.Error()
	}
	if e.Cause == nil {
		return ErrWorkspaceMutationFailed.Error()
	}
	return e.Cause.Error()
}

func (e *WorkspaceMutationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *WorkspaceMutationError) Is(target error) bool {
	return target == ErrWorkspaceMutationFailed
}

func (e *WorkspaceMutationError) RPCErrorCode() int {
	return protocol.ErrCodeWorkspaceMutationFailed
}

func (e *WorkspaceMutationError) RPCErrorData() json.RawMessage {
	if e == nil || !validWorkspaceMutationIdentity(e.ProjectID, e.WorkspaceID) {
		return nil
	}
	return marshalWorkspaceMutationIdentity("workspace_mutation_error", e.ProjectID, e.WorkspaceID)
}

func validWorkspaceMutationIdentity(projectID string, workspaceID string) bool {
	return strings.TrimSpace(projectID) != "" && strings.TrimSpace(workspaceID) != ""
}

type workspaceMutationIdentityEnvelope struct {
	Type        string `json:"type"`
	ProjectID   string `json:"project_id"`
	WorkspaceID string `json:"workspace_id"`
}

func marshalWorkspaceMutationIdentity(kind string, projectID string, workspaceID string) json.RawMessage {
	return marshalRPCErrorData(workspaceMutationIdentityEnvelope{
		Type:        kind,
		ProjectID:   strings.TrimSpace(projectID),
		WorkspaceID: strings.TrimSpace(workspaceID),
	})
}

func decodeWorkspaceMutationIdentity(data json.RawMessage, expectedType string) (workspaceMutationIdentityEnvelope, bool) {
	var envelope workspaceMutationIdentityEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil ||
		envelope.Type != expectedType ||
		!validWorkspaceMutationIdentity(envelope.ProjectID, envelope.WorkspaceID) {
		return workspaceMutationIdentityEnvelope{}, false
	}
	envelope.ProjectID = strings.TrimSpace(envelope.ProjectID)
	envelope.WorkspaceID = strings.TrimSpace(envelope.WorkspaceID)
	return envelope, true
}

func DecodeWorkspacePathIdentityError(data json.RawMessage, message string) error {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Type != "workspace_path_identity_error" {
		return fallbackProjectWorkspaceRPCError(message, ErrWorkspacePathIdentity)
	}
	return WorkspacePathIdentityError{
		remoteMessage: trimmedRPCMessage(message),
	}
}

func DecodeWorkspaceDetachConflictError(data json.RawMessage, message string) error {
	envelope, ok := decodeWorkspaceMutationIdentity(data, "workspace_detach_conflict_error")
	if !ok {
		return fallbackProjectWorkspaceRPCError(message, ErrWorkspaceDetachConflict)
	}
	return &WorkspaceDetachConflictError{ProjectID: envelope.ProjectID, WorkspaceID: envelope.WorkspaceID}
}

func DecodeWorkspaceMutationError(data json.RawMessage, message string) error {
	envelope, ok := decodeWorkspaceMutationIdentity(data, "workspace_mutation_error")
	if !ok {
		return fallbackProjectWorkspaceRPCError(message, ErrWorkspaceMutationFailed)
	}
	return &WorkspaceMutationError{
		ProjectID:   envelope.ProjectID,
		WorkspaceID: envelope.WorkspaceID,
		Cause:       causeFromRPCMessage(message),
	}
}

func causeFromRPCMessage(message string) error {
	trimmed := trimmedRPCMessage(message)
	if trimmed == nil {
		return nil
	}
	return errors.New(*trimmed)
}

func trimmedRPCMessage(message string) *string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func fallbackProjectWorkspaceRPCError(message string, sentinel error) error {
	return protocol.NewSentinelError(sentinel, message)
}
