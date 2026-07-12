package serverapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/shared/protocol"
)

var (
	ErrWorktreeSelectorNotFound    = errors.New("worktree selector not found")
	ErrWorktreeSelectorAmbiguous   = errors.New("worktree selector is ambiguous")
	ErrWorktreeSelectorUnavailable = errors.New("worktree selector is unavailable")
	ErrWorktreeOperationIDConflict = errors.New("worktree operation id conflicts with an existing payload")
	ErrWorktreeSetupRetained       = errors.New("worktree setup failed after worktree creation")
	ErrWorktreeDeletePrecondition  = errors.New("worktree deletion requires additional authorization")
)

type WorktreeSelectorErrorKind string

const (
	WorktreeSelectorErrorKindNotFound    WorktreeSelectorErrorKind = "not_found"
	WorktreeSelectorErrorKindAmbiguous   WorktreeSelectorErrorKind = "ambiguous"
	WorktreeSelectorErrorKindUnavailable WorktreeSelectorErrorKind = "unavailable"
)

type WorktreeSelectorCandidate struct {
	Variant          WorktreeTopologyVariant `json:"variant"`
	Selector         string                  `json:"selector"`
	BranchName       *string                 `json:"branch_name,omitempty"`
	DisplayName      *string                 `json:"display_name,omitempty"`
	FallbackIdentity string                  `json:"fallback_identity"`
}

type WorktreeSelectorError struct {
	Kind       WorktreeSelectorErrorKind   `json:"kind"`
	Input      string                      `json:"input"`
	Candidates []WorktreeSelectorCandidate `json:"candidates,omitempty"`
}

func (e *WorktreeSelectorError) Error() string {
	if e == nil {
		return "worktree selector error"
	}
	return "worktree selector error: " + string(e.Kind)
}

func (e *WorktreeSelectorError) Is(target error) bool {
	switch target {
	case ErrWorktreeSelectorNotFound:
		return e != nil && e.Kind == WorktreeSelectorErrorKindNotFound
	case ErrWorktreeSelectorAmbiguous:
		return e != nil && e.Kind == WorktreeSelectorErrorKindAmbiguous
	case ErrWorktreeSelectorUnavailable:
		return e != nil && e.Kind == WorktreeSelectorErrorKindUnavailable
	default:
		return false
	}
}

func (e *WorktreeSelectorError) RPCErrorCode() int {
	return protocol.ErrCodeWorktreeSelector
}

func (e *WorktreeSelectorError) RPCErrorData() json.RawMessage {
	if e == nil {
		return nil
	}
	return marshalRPCErrorData(struct {
		Type       string                      `json:"type"`
		Kind       WorktreeSelectorErrorKind   `json:"kind"`
		Input      string                      `json:"input"`
		Candidates []WorktreeSelectorCandidate `json:"candidates,omitempty"`
	}{
		Type:       "worktree_selector_error",
		Kind:       e.Kind,
		Input:      e.Input,
		Candidates: e.Candidates,
	})
}

func (e *WorktreeSelectorError) Validate() error {
	if e == nil {
		return errors.New("worktree selector error is required")
	}
	if strings.TrimSpace(e.Input) == "" {
		return errors.New("worktree selector error input is required")
	}
	switch e.Kind {
	case WorktreeSelectorErrorKindNotFound, WorktreeSelectorErrorKindUnavailable:
		if len(e.Candidates) != 0 {
			return errors.New("non-ambiguous selector error cannot contain candidates")
		}
	case WorktreeSelectorErrorKindAmbiguous:
		if len(e.Candidates) == 0 {
			return errors.New("ambiguous selector error requires candidates")
		}
		for _, candidate := range e.Candidates {
			if err := candidate.Validate(); err != nil {
				return err
			}
		}
	default:
		return errors.New("worktree selector error kind is invalid")
	}
	return nil
}

func (candidate WorktreeSelectorCandidate) Validate() error {
	switch candidate.Variant {
	case WorktreeTopologyVariantRegistered, WorktreeTopologyVariantExternal, WorktreeTopologyVariantMissing:
	default:
		return errors.New("worktree selector candidate variant is invalid")
	}
	if strings.TrimSpace(candidate.Selector) == "" || strings.TrimSpace(candidate.FallbackIdentity) == "" {
		return errors.New("worktree selector candidate requires selector and fallback_identity")
	}
	for _, fact := range []*string{candidate.BranchName, candidate.DisplayName} {
		if fact != nil && strings.TrimSpace(*fact) == "" {
			return errors.New("worktree selector candidate optional facts must not be empty")
		}
	}
	return nil
}

type WorktreeOperationIDConflictError struct {
	OperationID WorktreeOperationID      `json:"operation_id"`
	Existing    WorktreeOperationPayload `json:"existing"`
	Incoming    WorktreeOperationPayload `json:"incoming"`
}

func (e *WorktreeOperationIDConflictError) Error() string {
	return ErrWorktreeOperationIDConflict.Error()
}

func (e *WorktreeOperationIDConflictError) Is(target error) bool {
	return target == ErrWorktreeOperationIDConflict
}

func (e *WorktreeOperationIDConflictError) RPCErrorCode() int {
	return protocol.ErrCodeWorktreeOperationIDConflict
}

func (e *WorktreeOperationIDConflictError) RPCErrorData() json.RawMessage {
	if e == nil {
		return nil
	}
	return marshalRPCErrorData(struct {
		Type        string                   `json:"type"`
		OperationID WorktreeOperationID      `json:"operation_id"`
		Existing    WorktreeOperationPayload `json:"existing"`
		Incoming    WorktreeOperationPayload `json:"incoming"`
	}{
		Type:        "worktree_operation_id_conflict",
		OperationID: e.OperationID,
		Existing:    e.Existing,
		Incoming:    e.Incoming,
	})
}

func (e *WorktreeOperationIDConflictError) Validate() error {
	if e == nil {
		return errors.New("worktree operation id conflict is required")
	}
	if err := e.OperationID.Validate(); err != nil {
		return err
	}
	if err := e.Existing.Validate(); err != nil {
		return fmt.Errorf("existing payload: %w", err)
	}
	if err := e.Incoming.Validate(); err != nil {
		return fmt.Errorf("incoming payload: %w", err)
	}
	if e.Existing.Equal(e.Incoming) {
		return errors.New("worktree operation id conflict requires different payloads")
	}
	return nil
}

type WorktreeSetupRetainedError struct {
	Worktree   WorktreeTopologyEntry `json:"worktree"`
	Diagnostic string                `json:"diagnostic"`
}

func (e *WorktreeSetupRetainedError) Error() string {
	return ErrWorktreeSetupRetained.Error()
}

func (e *WorktreeSetupRetainedError) Is(target error) bool {
	return target == ErrWorktreeSetupRetained
}

func (e *WorktreeSetupRetainedError) RPCErrorCode() int {
	return protocol.ErrCodeWorktreeSetupRetained
}

func (e *WorktreeSetupRetainedError) RPCErrorData() json.RawMessage {
	if e == nil {
		return nil
	}
	return marshalRPCErrorData(struct {
		Type       string                `json:"type"`
		Worktree   WorktreeTopologyEntry `json:"worktree"`
		Diagnostic string                `json:"diagnostic"`
	}{
		Type:       "worktree_setup_retained",
		Worktree:   e.Worktree,
		Diagnostic: e.Diagnostic,
	})
}

func (e *WorktreeSetupRetainedError) Validate() error {
	if e == nil {
		return errors.New("worktree setup retained error is required")
	}
	if e.Worktree.Variant != WorktreeTopologyVariantRegistered {
		return errors.New("retained setup error requires a registered worktree")
	}
	if err := e.Worktree.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(e.Diagnostic) == "" {
		return errors.New("retained setup error diagnostic is required")
	}
	return nil
}

type WorktreeDirtyStateKind string

const (
	WorktreeDirtyStateClean   WorktreeDirtyStateKind = "clean"
	WorktreeDirtyStateDirty   WorktreeDirtyStateKind = "dirty"
	WorktreeDirtyStateUnknown WorktreeDirtyStateKind = "unknown"
)

type WorktreeDirtyState struct {
	Kind           WorktreeDirtyStateKind `json:"kind"`
	DirtyFileCount *int                   `json:"dirty_file_count,omitempty"`
	UnknownCause   *string                `json:"unknown_cause,omitempty"`
}

func (state WorktreeDirtyState) Validate() error {
	switch state.Kind {
	case WorktreeDirtyStateClean:
		if state.DirtyFileCount == nil || *state.DirtyFileCount != 0 || state.UnknownCause != nil {
			return errors.New("clean dirty state requires a present zero count only")
		}
	case WorktreeDirtyStateDirty:
		if state.DirtyFileCount == nil || *state.DirtyFileCount <= 0 || state.UnknownCause != nil {
			return errors.New("dirty state requires a positive count only")
		}
	case WorktreeDirtyStateUnknown:
		if state.DirtyFileCount != nil || state.UnknownCause == nil || strings.TrimSpace(*state.UnknownCause) == "" {
			return errors.New("unknown dirty state requires an unknown cause only")
		}
	default:
		return errors.New("worktree dirty state kind is invalid")
	}
	return nil
}

type WorktreeDeletePreconditionError struct {
	DirtyState WorktreeDirtyState `json:"dirty_state"`
}

func (e *WorktreeDeletePreconditionError) Error() string {
	return ErrWorktreeDeletePrecondition.Error()
}

func (e *WorktreeDeletePreconditionError) Is(target error) bool {
	return target == ErrWorktreeDeletePrecondition
}

func (e *WorktreeDeletePreconditionError) RPCErrorCode() int {
	return protocol.ErrCodeWorktreeDeletePrecondition
}

func (e *WorktreeDeletePreconditionError) RPCErrorData() json.RawMessage {
	if e == nil {
		return nil
	}
	return marshalRPCErrorData(struct {
		Type       string             `json:"type"`
		DirtyState WorktreeDirtyState `json:"dirty_state"`
	}{
		Type:       "worktree_delete_precondition",
		DirtyState: e.DirtyState,
	})
}

func (e *WorktreeDeletePreconditionError) Validate() error {
	if e == nil {
		return errors.New("worktree delete precondition error is required")
	}
	if err := e.DirtyState.Validate(); err != nil {
		return err
	}
	if e.DirtyState.Kind == WorktreeDirtyStateClean {
		return errors.New("clean worktree cannot fail delete precondition")
	}
	return nil
}

func DecodeWorktreeRPCError(data json.RawMessage, message string) error {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fallbackWorktreeRPCError(message)
	}
	switch envelope.Type {
	case "worktree_selector_error":
		var payload struct {
			Type       string                      `json:"type"`
			Kind       WorktreeSelectorErrorKind   `json:"kind"`
			Input      string                      `json:"input"`
			Candidates []WorktreeSelectorCandidate `json:"candidates,omitempty"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return fallbackWorktreeRPCError(message)
		}
		result := &WorktreeSelectorError{Kind: payload.Kind, Input: payload.Input, Candidates: payload.Candidates}
		if err := result.Validate(); err != nil {
			return fallbackWorktreeRPCError(message)
		}
		return result
	case "worktree_operation_id_conflict":
		var payload struct {
			Type        string                   `json:"type"`
			OperationID WorktreeOperationID      `json:"operation_id"`
			Existing    WorktreeOperationPayload `json:"existing"`
			Incoming    WorktreeOperationPayload `json:"incoming"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return fallbackWorktreeRPCError(message)
		}
		result := &WorktreeOperationIDConflictError{
			OperationID: payload.OperationID,
			Existing:    payload.Existing,
			Incoming:    payload.Incoming,
		}
		if err := result.Validate(); err != nil {
			return fallbackWorktreeRPCError(message)
		}
		return result
	case "worktree_setup_retained":
		var payload struct {
			Type       string                `json:"type"`
			Worktree   WorktreeTopologyEntry `json:"worktree"`
			Diagnostic string                `json:"diagnostic"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return fallbackWorktreeRPCError(message)
		}
		result := &WorktreeSetupRetainedError{Worktree: payload.Worktree, Diagnostic: payload.Diagnostic}
		if err := result.Validate(); err != nil {
			return fallbackWorktreeRPCError(message)
		}
		return result
	case "worktree_delete_precondition":
		var payload struct {
			Type       string             `json:"type"`
			DirtyState WorktreeDirtyState `json:"dirty_state"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return fallbackWorktreeRPCError(message)
		}
		result := &WorktreeDeletePreconditionError{DirtyState: payload.DirtyState}
		if err := result.Validate(); err != nil {
			return fallbackWorktreeRPCError(message)
		}
		return result
	default:
		return fallbackWorktreeRPCError(message)
	}
}

func fallbackWorktreeRPCError(message string) error {
	if trimmed := strings.TrimSpace(message); trimmed != "" {
		return errors.New(trimmed)
	}
	return errors.New("worktree RPC failed")
}
