package protoapi

import (
	"errors"
	"fmt"

	authpb "core/shared/protoapi/gen/kent/api/auth"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	serverpb "core/shared/protoapi/gen/kent/api/server"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/serverapi"
	"core/shared/worktreecontract"

	"google.golang.org/protobuf/proto"
)

func WorktreeErrorToProto(
	err error,
	workspace *projectpb.WorkspaceNotRegisteredDetails,
) (proto.Message, bool, error) {
	switch {
	case errors.Is(err, serverapi.ErrServerAuthRequired):
		return &authpb.AuthRequiredDetails{}, true, nil
	case errors.Is(err, serverapi.ErrWorkspaceNotRegistered):
		if validationErr := Validate(workspace); validationErr != nil {
			return nil, true, validationErr
		}
		return workspace, true, nil
	}
	var notReady *serverapi.ServerNotReadyError
	if errors.As(err, &notReady) {
		details, conversionErr := ServerNotReadyToProto(notReady)
		return details, true, conversionErr
	}
	var selector *worktreecontract.SelectorError
	if errors.As(err, &selector) {
		details, conversionErr := worktreeSelectorErrorToProto(selector)
		return details, true, conversionErr
	}
	var pending *worktreecontract.TransitionPendingError
	if errors.As(err, &pending) {
		if validationErr := pending.Validate(); validationErr != nil {
			return nil, true, validationErr
		}
		details := &worktreepb.TransitionPendingDetails{
			SessionId:          pending.SessionID,
			PendingOperationId: pending.PendingOperationID.String(),
		}
		return details, true, Validate(details)
	}
	var immediate *worktreecontract.ImmediateTransitionError
	if errors.As(err, &immediate) {
		kind, conversionErr := worktreeImmediateTransitionKindToProto(immediate.Kind)
		if conversionErr != nil {
			return nil, true, conversionErr
		}
		details := &worktreepb.ImmediateTransitionDetails{Kind: kind, Diagnostic: immediate.Error()}
		return details, true, Validate(details)
	}
	var create *worktreecontract.CreateError
	if errors.As(err, &create) {
		if validationErr := create.Validate(); validationErr != nil {
			return nil, true, validationErr
		}
		owner, conversionErr := worktreeCreateErrorOwnerToProto(create.Owner)
		if conversionErr != nil {
			return nil, true, conversionErr
		}
		details := &worktreepb.CreateFailureDetails{Owner: owner, Diagnostic: create.Diagnostic}
		return details, true, Validate(details)
	}
	var retained *worktreecontract.SetupRetainedError
	if errors.As(err, &retained) {
		details, conversionErr := worktreeSetupRetainedToProto(retained)
		return details, true, conversionErr
	}
	var precondition *worktreecontract.DeletePreconditionError
	if errors.As(err, &precondition) {
		if validationErr := precondition.Validate(); validationErr != nil {
			return nil, true, validationErr
		}
		dirtyState, conversionErr := WorktreeDirtyStateToProto(precondition.DirtyState)
		if conversionErr != nil {
			return nil, true, conversionErr
		}
		details := &worktreepb.DeletePreconditionDetails{DirtyState: dirtyState}
		return details, true, Validate(details)
	}
	if errors.Is(err, worktreecontract.ErrWorktreeBlocked) {
		details := &worktreepb.BlockedDetails{Diagnostic: err.Error()}
		return details, true, Validate(details)
	}
	return nil, false, nil
}

func WorktreeErrorFromProto(detail proto.Message) (error, error) {
	switch value := detail.(type) {
	case *authpb.AuthRequiredDetails:
		if err := Validate(value); err != nil {
			return nil, err
		}
		return serverapi.ErrServerAuthRequired, nil
	case *serverpb.ServerNotReadyDetails:
		converted := ServerNotReadyFromProto(value)
		if converted == nil {
			return nil, errors.New("server-not-ready conversion returned no error")
		}
		return converted, nil
	case *projectpb.WorkspaceNotRegisteredDetails:
		converted := WorkspaceNotRegisteredFromProto(value)
		if converted == nil {
			return nil, errors.New("workspace-not-registered conversion returned no error")
		}
		return converted, nil
	case *sharedpb.InternalFailureDetails:
		converted := InternalFailureFromProto(value)
		if converted == nil {
			return nil, errors.New("internal-failure conversion returned no error")
		}
		return converted, nil
	case *worktreepb.SelectorErrorDetails:
		return worktreeSelectorErrorFromProto(value)
	case *worktreepb.TransitionPendingDetails:
		if err := Validate(value); err != nil {
			return nil, err
		}
		operationID, err := worktreecontract.ParseOperationID(value.PendingOperationId)
		if err != nil {
			return nil, err
		}
		converted := &worktreecontract.TransitionPendingError{
			SessionID: value.SessionId, PendingOperationID: operationID,
		}
		return converted, converted.Validate()
	case *worktreepb.ImmediateTransitionDetails:
		if err := Validate(value); err != nil {
			return nil, err
		}
		kind, err := worktreeImmediateTransitionKindFromProto(value.Kind)
		if err != nil {
			return nil, err
		}
		return &worktreecontract.ImmediateTransitionError{
			Kind: kind, Cause: errors.New(value.Diagnostic),
		}, nil
	case *worktreepb.CreateFailureDetails:
		if err := Validate(value); err != nil {
			return nil, err
		}
		owner, err := worktreeCreateErrorOwnerFromProto(value.Owner)
		if err != nil {
			return nil, err
		}
		return worktreecontract.NewCreateError(owner, value.Diagnostic, nil), nil
	case *worktreepb.SetupRetainedDetails:
		return worktreeSetupRetainedFromProto(value)
	case *worktreepb.DeletePreconditionDetails:
		if err := Validate(value); err != nil {
			return nil, err
		}
		dirtyState, err := WorktreeDirtyStateFromProto(value.DirtyState)
		if err != nil {
			return nil, err
		}
		converted := &worktreecontract.DeletePreconditionError{DirtyState: dirtyState}
		return converted, converted.Validate()
	case *worktreepb.BlockedDetails:
		if err := Validate(value); err != nil {
			return nil, err
		}
		return worktreeMappedDiagnosticError{
			diagnostic: value.Diagnostic,
			category:   worktreecontract.ErrWorktreeBlocked,
		}, nil
	default:
		return nil, fmt.Errorf("protobuf Worktree error detail %T is unsupported", detail)
	}
}

func worktreeSelectorErrorToProto(value *worktreecontract.SelectorError) (*worktreepb.SelectorErrorDetails, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	var kind worktreepb.SelectorErrorKind
	switch value.Kind {
	case worktreecontract.SelectorErrorKindNotFound:
		kind = worktreepb.SelectorErrorKind_WORKTREE_SELECTOR_ERROR_KIND_NOT_FOUND
	case worktreecontract.SelectorErrorKindAmbiguous:
		kind = worktreepb.SelectorErrorKind_WORKTREE_SELECTOR_ERROR_KIND_AMBIGUOUS
	case worktreecontract.SelectorErrorKindUnavailable:
		kind = worktreepb.SelectorErrorKind_WORKTREE_SELECTOR_ERROR_KIND_UNAVAILABLE
	default:
		return nil, fmt.Errorf("worktree selector error kind %q is unsupported", value.Kind)
	}
	candidates := make([]*worktreepb.SelectorCandidate, 0, len(value.Candidates))
	for _, candidate := range value.Candidates {
		variant, err := worktreeTopologyVariantToProto(candidate.Variant)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, &worktreepb.SelectorCandidate{
			Variant:          variant,
			Selector:         candidate.Selector,
			BranchName:       clonePointer(candidate.BranchName),
			DisplayName:      clonePointer(candidate.DisplayName),
			FallbackIdentity: candidate.FallbackIdentity,
		})
	}
	details := &worktreepb.SelectorErrorDetails{
		Kind: kind, Input: value.Input, Candidates: candidates,
	}
	return details, Validate(details)
}

func worktreeSelectorErrorFromProto(details *worktreepb.SelectorErrorDetails) (error, error) {
	if err := Validate(details); err != nil {
		return nil, err
	}
	var kind worktreecontract.SelectorErrorKind
	switch details.Kind {
	case worktreepb.SelectorErrorKind_WORKTREE_SELECTOR_ERROR_KIND_NOT_FOUND:
		kind = worktreecontract.SelectorErrorKindNotFound
	case worktreepb.SelectorErrorKind_WORKTREE_SELECTOR_ERROR_KIND_AMBIGUOUS:
		kind = worktreecontract.SelectorErrorKindAmbiguous
	case worktreepb.SelectorErrorKind_WORKTREE_SELECTOR_ERROR_KIND_UNAVAILABLE:
		kind = worktreecontract.SelectorErrorKindUnavailable
	default:
		return nil, fmt.Errorf("protobuf Worktree selector error kind %v is unsupported", details.Kind)
	}
	candidates := make([]worktreecontract.SelectorCandidate, 0, len(details.Candidates))
	for _, candidate := range details.Candidates {
		variant, err := worktreeTopologyVariantFromProto(candidate.Variant)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, worktreecontract.SelectorCandidate{
			Variant:          variant,
			Selector:         candidate.Selector,
			BranchName:       clonePointer(candidate.BranchName),
			DisplayName:      clonePointer(candidate.DisplayName),
			FallbackIdentity: candidate.FallbackIdentity,
		})
	}
	converted := &worktreecontract.SelectorError{Kind: kind, Input: details.Input, Candidates: candidates}
	return converted, converted.Validate()
}

func worktreeSetupRetainedToProto(value *worktreecontract.SetupRetainedError) (*worktreepb.SetupRetainedDetails, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	worktree, err := worktreeRegisteredFactsToProto(*value.Worktree.Registered)
	if err != nil {
		return nil, err
	}
	retainedPrevious, err := worktreeRetainedPreviousToProto(value.RetainedPreviousWorktree)
	if err != nil {
		return nil, err
	}
	details := &worktreepb.SetupRetainedDetails{
		Worktree:                 worktree,
		ScriptPath:               value.ScriptPath,
		Diagnostic:               value.Diagnostic,
		RetainedPreviousWorktree: retainedPrevious,
	}
	return details, Validate(details)
}

func worktreeSetupRetainedFromProto(details *worktreepb.SetupRetainedDetails) (error, error) {
	if err := Validate(details); err != nil {
		return nil, err
	}
	registered, err := worktreeRegisteredFactsFromProto(details.Worktree)
	if err != nil {
		return nil, err
	}
	retainedPrevious, err := worktreeRetainedPreviousFromProto(details.RetainedPreviousWorktree)
	if err != nil {
		return nil, err
	}
	converted, err := worktreecontract.NewSetupRetainedError(
		worktreecontract.TopologyEntry{
			Variant: worktreecontract.TopologyVariantRegistered, Registered: &registered,
		},
		details.ScriptPath,
		details.Diagnostic,
		nil,
	)
	if err != nil {
		return nil, err
	}
	converted.RetainedPreviousWorktree = retainedPrevious
	return converted, converted.Validate()
}

func worktreeRetainedPreviousToProto(value *worktreecontract.RetainedPreviousWorktree) (*worktreepb.RetainedPreviousWorktree, error) {
	if value == nil {
		return nil, nil
	}
	if err := value.Validate(); err != nil {
		return nil, err
	}
	worktree, err := worktreeRegisteredFactsToProto(*value.Worktree.Registered)
	if err != nil {
		return nil, err
	}
	message := &worktreepb.RetainedPreviousWorktree{Worktree: worktree}
	return message, Validate(message)
}

func worktreeRetainedPreviousFromProto(message *worktreepb.RetainedPreviousWorktree) (*worktreecontract.RetainedPreviousWorktree, error) {
	if message == nil {
		return nil, nil
	}
	if err := Validate(message); err != nil {
		return nil, err
	}
	registered, err := worktreeRegisteredFactsFromProto(message.Worktree)
	if err != nil {
		return nil, err
	}
	value := &worktreecontract.RetainedPreviousWorktree{
		Worktree: worktreecontract.TopologyEntry{
			Variant: worktreecontract.TopologyVariantRegistered, Registered: &registered,
		},
	}
	return value, value.Validate()
}

func worktreeTopologyVariantToProto(value worktreecontract.TopologyVariant) (worktreepb.TopologyVariant, error) {
	switch value {
	case worktreecontract.TopologyVariantRegistered:
		return worktreepb.TopologyVariant_WORKTREE_TOPOLOGY_VARIANT_REGISTERED, nil
	case worktreecontract.TopologyVariantExternal:
		return worktreepb.TopologyVariant_WORKTREE_TOPOLOGY_VARIANT_EXTERNAL, nil
	case worktreecontract.TopologyVariantMissing:
		return worktreepb.TopologyVariant_WORKTREE_TOPOLOGY_VARIANT_MISSING, nil
	default:
		return worktreepb.TopologyVariant_WORKTREE_TOPOLOGY_VARIANT_UNSPECIFIED, fmt.Errorf(
			"worktree topology variant %q is unsupported",
			value,
		)
	}
}

func worktreeTopologyVariantFromProto(value worktreepb.TopologyVariant) (worktreecontract.TopologyVariant, error) {
	switch value {
	case worktreepb.TopologyVariant_WORKTREE_TOPOLOGY_VARIANT_REGISTERED:
		return worktreecontract.TopologyVariantRegistered, nil
	case worktreepb.TopologyVariant_WORKTREE_TOPOLOGY_VARIANT_EXTERNAL:
		return worktreecontract.TopologyVariantExternal, nil
	case worktreepb.TopologyVariant_WORKTREE_TOPOLOGY_VARIANT_MISSING:
		return worktreecontract.TopologyVariantMissing, nil
	default:
		return "", fmt.Errorf("protobuf Worktree topology variant %v is unsupported", value)
	}
}

func worktreeImmediateTransitionKindToProto(value worktreecontract.ImmediateTransitionErrorKind) (worktreepb.ImmediateTransitionErrorKind, error) {
	switch value {
	case worktreecontract.ImmediateTransitionOriginInactive:
		return worktreepb.ImmediateTransitionErrorKind_WORKTREE_IMMEDIATE_TRANSITION_ORIGIN_INACTIVE, nil
	case worktreecontract.ImmediateTransitionApplyFailed:
		return worktreepb.ImmediateTransitionErrorKind_WORKTREE_IMMEDIATE_TRANSITION_APPLY_FAILED, nil
	default:
		return worktreepb.ImmediateTransitionErrorKind_WORKTREE_IMMEDIATE_TRANSITION_UNSPECIFIED, fmt.Errorf(
			"worktree immediate transition error kind %q is unsupported",
			value,
		)
	}
}

func worktreeImmediateTransitionKindFromProto(value worktreepb.ImmediateTransitionErrorKind) (worktreecontract.ImmediateTransitionErrorKind, error) {
	switch value {
	case worktreepb.ImmediateTransitionErrorKind_WORKTREE_IMMEDIATE_TRANSITION_ORIGIN_INACTIVE:
		return worktreecontract.ImmediateTransitionOriginInactive, nil
	case worktreepb.ImmediateTransitionErrorKind_WORKTREE_IMMEDIATE_TRANSITION_APPLY_FAILED:
		return worktreecontract.ImmediateTransitionApplyFailed, nil
	default:
		return "", fmt.Errorf("protobuf Worktree immediate transition error kind %v is unsupported", value)
	}
}

func worktreeCreateErrorOwnerToProto(value worktreecontract.CreateErrorOwner) (worktreepb.CreateErrorOwner, error) {
	switch value {
	case worktreecontract.CreateErrorOwnerBaseRef:
		return worktreepb.CreateErrorOwner_WORKTREE_CREATE_ERROR_OWNER_BASE_REF, nil
	case worktreecontract.CreateErrorOwnerForm:
		return worktreepb.CreateErrorOwner_WORKTREE_CREATE_ERROR_OWNER_FORM, nil
	default:
		return worktreepb.CreateErrorOwner_WORKTREE_CREATE_ERROR_OWNER_UNSPECIFIED, fmt.Errorf(
			"worktree create error owner %q is unsupported",
			value,
		)
	}
}

func worktreeCreateErrorOwnerFromProto(value worktreepb.CreateErrorOwner) (worktreecontract.CreateErrorOwner, error) {
	switch value {
	case worktreepb.CreateErrorOwner_WORKTREE_CREATE_ERROR_OWNER_BASE_REF:
		return worktreecontract.CreateErrorOwnerBaseRef, nil
	case worktreepb.CreateErrorOwner_WORKTREE_CREATE_ERROR_OWNER_FORM:
		return worktreecontract.CreateErrorOwnerForm, nil
	default:
		return "", fmt.Errorf("protobuf Worktree create error owner %v is unsupported", value)
	}
}

type worktreeMappedDiagnosticError struct {
	diagnostic string
	category   error
}

func (e worktreeMappedDiagnosticError) Error() string {
	return e.diagnostic
}

func (e worktreeMappedDiagnosticError) Is(target error) bool {
	return target == e.category
}
