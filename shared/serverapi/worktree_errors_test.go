package serverapi

import (
	"errors"
	"testing"

	"core/shared/protocol"
)

func TestWorktreeStructuredErrorsRoundTripTypedFacts(t *testing.T) {
	selector := &WorktreeSelectorError{
		Kind:  WorktreeSelectorErrorKindAmbiguous,
		Input: "feature",
		Candidates: []WorktreeSelectorCandidate{{
			Variant:          WorktreeTopologyVariantRegistered,
			Selector:         "feature-a",
			BranchName:       stringPointer("feature"),
			DisplayName:      stringPointer("feature-a"),
			FallbackIdentity: "c4aaf0cf-4c50-4560-b6a2-6c294d0b1495",
		}},
	}
	operationID := NewWorktreeOperationID()
	conflict := &WorktreeOperationIDConflictError{
		OperationID: operationID,
		Existing: WorktreeOperationPayload{
			Version:             WorktreeOperationPayloadVersion1,
			SessionID:           "session",
			Kind:                WorktreeOperationKindDelete,
			Selector:            stringPointer("feature"),
			BranchCleanupPolicy: WorktreeBranchCleanupPolicyRetain,
		},
		Incoming: WorktreeOperationPayload{
			Version:             WorktreeOperationPayloadVersion1,
			SessionID:           "session",
			Kind:                WorktreeOperationKindDelete,
			Selector:            stringPointer("other"),
			BranchCleanupPolicy: WorktreeBranchCleanupPolicyRetain,
		},
	}
	retained := &WorktreeSetupRetainedError{
		Worktree: WorktreeTopologyEntry{
			Variant: WorktreeTopologyVariantRegistered,
			Registered: &WorktreeRegisteredFacts{
				Git: WorktreeGitFacts{CanonicalRoot: "/repo/feature", HeadObject: "abc123", PathAvailable: true},
				Kent: WorktreeKentFacts{
					WorktreeID:    "c4aaf0cf-4c50-4560-b6a2-6c294d0b1495",
					CanonicalRoot: "/repo/feature",
					DisplayName:   "feature",
				},
			},
		},
		Diagnostic: "setup exited unsuccessfully",
	}
	precondition := &WorktreeDeletePreconditionError{
		DirtyState: WorktreeDirtyState{
			Kind:         WorktreeDirtyStateUnknown,
			UnknownCause: stringPointer("status probe failed"),
		},
	}

	for _, source := range []protocol.StructuredRPCError{selector, conflict, retained, precondition} {
		if source.RPCErrorCode() >= 0 {
			t.Fatalf("%T protocol error code = %d, want implementation-defined error code", source, source.RPCErrorCode())
		}
	}

	decodedSelector := DecodeWorktreeRPCError(selector.RPCErrorData(), selector.Error())
	var selectorError *WorktreeSelectorError
	if !errors.As(decodedSelector, &selectorError) {
		t.Fatalf("selector decode = %T, want WorktreeSelectorError", decodedSelector)
	}
	if !errors.Is(decodedSelector, ErrWorktreeSelectorAmbiguous) {
		t.Fatalf("selector decode does not preserve ambiguity: %v", decodedSelector)
	}
	if selectorError.Input != selector.Input || len(selectorError.Candidates) != 1 || selectorError.Candidates[0].FallbackIdentity != selector.Candidates[0].FallbackIdentity {
		t.Fatalf("selector facts changed: %+v", selectorError)
	}

	decodedConflict := DecodeWorktreeRPCError(conflict.RPCErrorData(), conflict.Error())
	var conflictError *WorktreeOperationIDConflictError
	if !errors.As(decodedConflict, &conflictError) {
		t.Fatalf("conflict decode = %T, want WorktreeOperationIDConflictError", decodedConflict)
	}
	if !errors.Is(decodedConflict, ErrWorktreeOperationIDConflict) {
		t.Fatalf("conflict decode does not preserve conflict: %v", decodedConflict)
	}
	if conflictError.OperationID != operationID || *conflictError.Existing.Selector != "feature" || *conflictError.Incoming.Selector != "other" {
		t.Fatalf("conflict facts changed: %+v", conflictError)
	}

	decodedRetained := DecodeWorktreeRPCError(retained.RPCErrorData(), retained.Error())
	var retainedError *WorktreeSetupRetainedError
	if !errors.As(decodedRetained, &retainedError) {
		t.Fatalf("retained decode = %T, want WorktreeSetupRetainedError", decodedRetained)
	}
	if !errors.Is(decodedRetained, ErrWorktreeSetupRetained) {
		t.Fatalf("retained decode does not preserve retained setup: %v", decodedRetained)
	}
	if retainedError.Worktree.Registered == nil || retainedError.Worktree.Registered.Kent.WorktreeID != retained.Worktree.Registered.Kent.WorktreeID {
		t.Fatalf("retained worktree facts changed: %+v", retainedError)
	}

	decodedPrecondition := DecodeWorktreeRPCError(precondition.RPCErrorData(), precondition.Error())
	var preconditionError *WorktreeDeletePreconditionError
	if !errors.As(decodedPrecondition, &preconditionError) {
		t.Fatalf("precondition decode = %T, want WorktreeDeletePreconditionError", decodedPrecondition)
	}
	if !errors.Is(decodedPrecondition, ErrWorktreeDeletePrecondition) {
		t.Fatalf("precondition decode does not preserve delete precondition: %v", decodedPrecondition)
	}
	if preconditionError.DirtyState.Kind != WorktreeDirtyStateUnknown || preconditionError.DirtyState.UnknownCause == nil {
		t.Fatalf("dirty-state facts changed: %+v", preconditionError)
	}
}

func TestWorktreeStructuredErrorsRejectInvalidTypedData(t *testing.T) {
	if err := (&WorktreeSelectorError{
		Kind:  WorktreeSelectorErrorKindAmbiguous,
		Input: "feature",
	}).Validate(); err == nil {
		t.Fatal("ambiguous selector error without candidates validated")
	}
	if err := (WorktreeDirtyState{Kind: WorktreeDirtyStateClean}).Validate(); err == nil {
		t.Fatal("clean dirty-state without zero count validated")
	}
	if err := (WorktreeDirtyState{
		Kind:           WorktreeDirtyStateUnknown,
		UnknownCause:   stringPointer("status unavailable"),
		DirtyFileCount: integerPointer(1),
	}).Validate(); err == nil {
		t.Fatal("unknown dirty-state with a count validated")
	}
	payload := WorktreeOperationPayload{
		Version:             WorktreeOperationPayloadVersion1,
		SessionID:           "session",
		Kind:                WorktreeOperationKindLeave,
		BranchCleanupPolicy: WorktreeBranchCleanupPolicyRetain,
	}
	if err := (&WorktreeOperationIDConflictError{
		OperationID: NewWorktreeOperationID(),
		Existing:    payload,
		Incoming:    payload,
	}).Validate(); err == nil {
		t.Fatal("operation conflict with equal payloads validated")
	}
}

func integerPointer(value int) *int {
	return &value
}
