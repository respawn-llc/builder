package serverapi

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"core/shared/protocol"
)

func TestWorktreeActionErrorsRenderTypedDiagnostics(t *testing.T) {
	setupDiagnostic := "setup command timed out after 17 seconds"
	if rendered := (&WorktreeSetupRetainedError{Diagnostic: setupDiagnostic}).Error(); !strings.Contains(rendered, setupDiagnostic) {
		t.Fatalf("retained setup error omitted diagnostic %q: %q", setupDiagnostic, rendered)
	}

	dirtyFileCount := 7
	dirty := &WorktreeDeletePreconditionError{DirtyState: WorktreeDirtyState{
		Kind:           WorktreeDirtyStateDirty,
		DirtyFileCount: &dirtyFileCount,
	}}
	if rendered := dirty.Error(); !strings.Contains(rendered, strconv.Itoa(dirtyFileCount)) {
		t.Fatalf("dirty delete precondition omitted file count %d: %q", dirtyFileCount, rendered)
	}

	unknownCause := "git status exited with code 128"
	unknown := &WorktreeDeletePreconditionError{DirtyState: WorktreeDirtyState{
		Kind:         WorktreeDirtyStateUnknown,
		UnknownCause: &unknownCause,
	}}
	if rendered := unknown.Error(); !strings.Contains(rendered, unknownCause) {
		t.Fatalf("unknown delete precondition omitted cause %q: %q", unknownCause, rendered)
	}
}

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
	pending := &WorktreeTransitionPendingError{
		SessionID:          "session",
		PendingOperationID: operationID,
	}
	immediate := NewWorktreeImmediateTransitionError(
		WorktreeImmediateTransitionOriginInactive,
		errors.New("originating step ended"),
	)
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

	for _, source := range []protocol.StructuredRPCError{selector, pending, immediate, retained, precondition} {
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

	decodedPending := DecodeWorktreeRPCError(pending.RPCErrorData(), pending.Error())
	var pendingError *WorktreeTransitionPendingError
	if !errors.As(decodedPending, &pendingError) {
		t.Fatalf("pending decode = %T, want WorktreeTransitionPendingError", decodedPending)
	}
	if !errors.Is(decodedPending, ErrWorktreeTransitionPending) {
		t.Fatalf("pending decode does not preserve pending state: %v", decodedPending)
	}
	if pendingError.SessionID != pending.SessionID || pendingError.PendingOperationID != operationID {
		t.Fatalf("pending facts changed: %+v", pendingError)
	}

	decodedImmediate := DecodeWorktreeRPCError(immediate.RPCErrorData(), immediate.Error())
	var immediateError *WorktreeImmediateTransitionError
	if !errors.As(decodedImmediate, &immediateError) {
		t.Fatalf("immediate decode = %T, want WorktreeImmediateTransitionError", decodedImmediate)
	}
	if immediateError.Kind != WorktreeImmediateTransitionOriginInactive {
		t.Fatalf("immediate kind = %q, want origin inactive", immediateError.Kind)
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
	if err := (&WorktreeTransitionPendingError{
		PendingOperationID: NewWorktreeOperationID(),
	}).Validate(); err == nil {
		t.Fatal("pending transition without session validated")
	}
}

func TestWorktreeCreateErrorRoundTripsWithOwnershipOnlyWireData(t *testing.T) {
	tests := []struct {
		name  string
		owner WorktreeCreateErrorOwner
	}{
		{name: "base ref", owner: WorktreeCreateErrorOwnerBaseRef},
		{name: "form", owner: WorktreeCreateErrorOwnerForm},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &WorktreeCreateError{
				Owner:      test.owner,
				Diagnostic: "git create diagnostic",
			}
			if err := source.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if source.RPCErrorCode() != protocol.ErrCodeWorktreeCreate {
				t.Fatalf("RPCErrorCode() = %d, want %d", source.RPCErrorCode(), protocol.ErrCodeWorktreeCreate)
			}

			var wire map[string]json.RawMessage
			if err := json.Unmarshal(source.RPCErrorData(), &wire); err != nil {
				t.Fatalf("unmarshal RPC data: %v", err)
			}
			if len(wire) != 2 {
				t.Fatalf("RPC data fields = %v, want exactly owner and diagnostic", wire)
			}
			if _, ok := wire["owner"]; !ok {
				t.Fatalf("RPC data omitted owner: %v", wire)
			}
			if _, ok := wire["diagnostic"]; !ok {
				t.Fatalf("RPC data omitted diagnostic: %v", wire)
			}
			if _, ok := wire["type"]; ok {
				t.Fatal("RPC data contains a duplicate type discriminator")
			}

			decoded := DecodeWorktreeCreateError(source.RPCErrorData(), source.Error())
			var typed *WorktreeCreateError
			if !errors.As(decoded, &typed) {
				t.Fatalf("decoded error = %T %v, want WorktreeCreateError", decoded, decoded)
			}
			if typed.Owner != source.Owner || typed.Diagnostic != source.Diagnostic {
				t.Fatalf("decoded error = %+v, want %+v", typed, source)
			}
		})
	}
}

func TestWorktreeCreateErrorRejectsMalformedWireDataAsContractError(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{name: "invalid owner", data: `{"owner":"other","diagnostic":"diagnostic"}`},
		{name: "blank diagnostic", data: `{"owner":"form","diagnostic":"  "}`},
		{name: "missing owner", data: `{"diagnostic":"diagnostic"}`},
		{name: "missing diagnostic", data: `{"owner":"form"}`},
		{name: "unknown field", data: `{"owner":"form","diagnostic":"diagnostic","type":"worktree_create_error"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decoded := DecodeWorktreeCreateError(json.RawMessage(test.data), "wire diagnostic")
			var contractErr *WorktreeCreateContractError
			if !errors.As(decoded, &contractErr) {
				t.Fatalf("decoded error = %T %v, want WorktreeCreateContractError", decoded, decoded)
			}
			if test.name == "missing owner" && contractErr.Owner != nil {
				t.Fatalf("missing owner decoded as %q, want absent owner", *contractErr.Owner)
			}
			var typed *WorktreeCreateError
			if errors.As(decoded, &typed) {
				t.Fatalf("malformed wire data decoded as typed create error: %+v", typed)
			}
		})
	}
}

func TestWorktreeCreateRequestValidationProjectsNeutralFailures(t *testing.T) {
	base := WorktreeCreateRequest{
		ClientRequestID:  "request-1",
		SetupOperationID: NewWorktreeSetupOperationID(),
		SessionID:        "session",
		BranchName:       "feature",
	}
	tests := []struct {
		name  string
		patch func(*WorktreeCreateRequest)
		owner WorktreeCreateErrorOwner
	}{
		{
			name: "new branch blank base ref",
			patch: func(request *WorktreeCreateRequest) {
				request.CreateBranch = true
			},
			owner: WorktreeCreateErrorOwnerBaseRef,
		},
		{
			name: "existing branch blank base ref",
			patch: func(request *WorktreeCreateRequest) {
				request.CreateBranch = false
			},
			owner: WorktreeCreateErrorOwnerForm,
		},
		{
			name: "new branch blank branch name",
			patch: func(request *WorktreeCreateRequest) {
				request.CreateBranch = true
				request.BaseRef = "HEAD"
				request.BranchName = ""
			},
			owner: WorktreeCreateErrorOwnerForm,
		},
		{
			name: "existing branch with branch name",
			patch: func(request *WorktreeCreateRequest) {
				request.CreateBranch = false
				request.BaseRef = "feature"
			},
			owner: WorktreeCreateErrorOwnerForm,
		},
		{
			name: "blank client request id",
			patch: func(request *WorktreeCreateRequest) {
				request.ClientRequestID = ""
				request.CreateBranch = true
				request.BaseRef = "HEAD"
			},
			owner: WorktreeCreateErrorOwnerForm,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base
			test.patch(&request)
			err := request.Validate()
			var typed *WorktreeCreateError
			if !errors.As(err, &typed) {
				t.Fatalf("Validate() = %T %v, want WorktreeCreateError", err, err)
			}
			if typed.Owner != test.owner {
				t.Fatalf("owner = %q, want %q", typed.Owner, test.owner)
			}
			if strings.TrimSpace(typed.Diagnostic) == "" {
				t.Fatal("validation error diagnostic is blank")
			}
		})
	}
}

func TestWorktreeSetupRetainedErrorKeepsExistingWireIdentity(t *testing.T) {
	source := &WorktreeSetupRetainedError{
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
		Diagnostic: "setup failed",
	}
	if source.RPCErrorCode() != protocol.ErrCodeWorktreeSetupRetained {
		t.Fatalf("setup-retained RPC code = %d, want %d", source.RPCErrorCode(), protocol.ErrCodeWorktreeSetupRetained)
	}
	decoded := DecodeWorktreeRPCError(source.RPCErrorData(), source.Error())
	var retained *WorktreeSetupRetainedError
	if !errors.As(decoded, &retained) {
		t.Fatalf("decoded error = %T %v, want WorktreeSetupRetainedError", decoded, decoded)
	}
	if !errors.Is(decoded, ErrWorktreeSetupRetained) {
		t.Fatalf("decoded error does not preserve setup-retained identity: %v", decoded)
	}
}

func integerPointer(value int) *int {
	return &value
}
