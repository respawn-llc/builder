package protoapi

import (
	"fmt"

	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/worktreecontract"
)

func WorktreeListSuccessToProto(response worktreecontract.ListResponse) (*worktreepb.ListSuccess, error) {
	if err := response.Validate(); err != nil {
		return nil, err
	}
	target, err := worktreeSessionExecutionTargetToProto(response.Target)
	if err != nil {
		return nil, err
	}
	worktrees, err := worktreeListEntriesToProto(response.Worktrees)
	if err != nil {
		return nil, err
	}
	message := &worktreepb.ListSuccess{Target: target, Worktrees: worktrees}
	return message, Validate(message)
}

func WorktreeListSuccessFromProto(message *worktreepb.ListSuccess) (worktreecontract.ListResponse, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.ListResponse{}, err
	}
	target, err := worktreeSessionExecutionTargetFromProto(message.Target)
	if err != nil {
		return worktreecontract.ListResponse{}, err
	}
	worktrees, err := worktreeListEntriesFromProto(message.Worktrees)
	if err != nil {
		return worktreecontract.ListResponse{}, err
	}
	response := worktreecontract.ListResponse{Target: target, Worktrees: worktrees}
	return response, response.Validate()
}

func WorktreeWorkspaceListSuccessToProto(response worktreecontract.WorkspaceListResponse) (*worktreepb.WorkspaceListSuccess, error) {
	if err := response.Validate(); err != nil {
		return nil, err
	}
	worktrees, err := worktreeListEntriesToProto(response.Worktrees)
	if err != nil {
		return nil, err
	}
	message := &worktreepb.WorkspaceListSuccess{WorkspaceId: response.WorkspaceID, Worktrees: worktrees}
	return message, Validate(message)
}

func WorktreeWorkspaceListSuccessFromProto(message *worktreepb.WorkspaceListSuccess) (worktreecontract.WorkspaceListResponse, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.WorkspaceListResponse{}, err
	}
	worktrees, err := worktreeListEntriesFromProto(message.Worktrees)
	if err != nil {
		return worktreecontract.WorkspaceListResponse{}, err
	}
	response := worktreecontract.WorkspaceListResponse{
		WorkspaceID: message.WorkspaceId,
		Worktrees:   worktrees,
	}
	return response, response.Validate()
}

func WorktreeSelectorResolveSuccessToProto(response worktreecontract.SelectorResolveResponse) (*worktreepb.SelectorResolveSuccess, error) {
	if err := response.Validate(); err != nil {
		return nil, err
	}
	worktree, err := worktreeListEntryToProto(response.Worktree)
	if err != nil {
		return nil, err
	}
	message := &worktreepb.SelectorResolveSuccess{Worktree: worktree}
	return message, Validate(message)
}

func WorktreeSelectorResolveSuccessFromProto(message *worktreepb.SelectorResolveSuccess) (worktreecontract.SelectorResolveResponse, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.SelectorResolveResponse{}, err
	}
	worktree, err := worktreeListEntryFromProto(message.Worktree)
	if err != nil {
		return worktreecontract.SelectorResolveResponse{}, err
	}
	response := worktreecontract.SelectorResolveResponse{Worktree: worktree}
	return response, response.Validate()
}

func WorktreeDeletePreviewSuccessToProto(response worktreecontract.DeletePreviewResponse) (*worktreepb.DeletePreviewSuccess, error) {
	if err := response.Validate(); err != nil {
		return nil, err
	}
	worktree, err := WorktreeTopologyEntryToProto(response.Worktree)
	if err != nil {
		return nil, err
	}
	cleanliness, err := WorktreeDirtyStateToProto(response.Cleanliness)
	if err != nil {
		return nil, err
	}
	message := &worktreepb.DeletePreviewSuccess{
		Worktree:         worktree,
		DeletionSelector: response.DeletionSelector,
		Cleanliness:      cleanliness,
	}
	return message, Validate(message)
}

func WorktreeDeletePreviewSuccessFromProto(message *worktreepb.DeletePreviewSuccess) (worktreecontract.DeletePreviewResponse, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.DeletePreviewResponse{}, err
	}
	worktree, err := WorktreeTopologyEntryFromProto(message.Worktree)
	if err != nil {
		return worktreecontract.DeletePreviewResponse{}, err
	}
	cleanliness, err := WorktreeDirtyStateFromProto(message.Cleanliness)
	if err != nil {
		return worktreecontract.DeletePreviewResponse{}, err
	}
	response := worktreecontract.DeletePreviewResponse{
		Worktree:         worktree,
		DeletionSelector: message.DeletionSelector,
		Cleanliness:      cleanliness,
	}
	return response, response.Validate()
}

func WorktreeCreateTargetResolveSuccessToProto(response worktreecontract.CreateTargetResolveResponse) (*worktreepb.CreateTargetResolveSuccess, error) {
	resolution, err := worktreeCreateTargetResolutionToProto(response.Resolution)
	if err != nil {
		return nil, err
	}
	message := &worktreepb.CreateTargetResolveSuccess{Resolution: resolution}
	return message, Validate(message)
}

func WorktreeCreateTargetResolveSuccessFromProto(message *worktreepb.CreateTargetResolveSuccess) (worktreecontract.CreateTargetResolveResponse, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.CreateTargetResolveResponse{}, err
	}
	resolution, err := worktreeCreateTargetResolutionFromProto(message.Resolution)
	if err != nil {
		return worktreecontract.CreateTargetResolveResponse{}, err
	}
	return worktreecontract.CreateTargetResolveResponse{Resolution: resolution}, nil
}

func WorktreeCreateSuccessToProto(response worktreecontract.CreateResponse) (*worktreepb.CreateSuccess, error) {
	if err := response.Validate(); err != nil {
		return nil, err
	}
	target, err := worktreeSessionExecutionTargetToProto(response.Target)
	if err != nil {
		return nil, err
	}
	worktree, err := worktreeListEntryToProto(response.Worktree)
	if err != nil {
		return nil, err
	}
	message := &worktreepb.CreateSuccess{Target: target, Worktree: worktree}
	return message, Validate(message)
}

func WorktreeCreateSuccessFromProto(message *worktreepb.CreateSuccess) (worktreecontract.CreateResponse, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.CreateResponse{}, err
	}
	target, err := worktreeSessionExecutionTargetFromProto(message.Target)
	if err != nil {
		return worktreecontract.CreateResponse{}, err
	}
	worktree, err := worktreeListEntryFromProto(message.Worktree)
	if err != nil {
		return worktreecontract.CreateResponse{}, err
	}
	response := worktreecontract.CreateResponse{Target: target, Worktree: worktree}
	return response, response.Validate()
}

func WorktreeScheduledAcknowledgementToProto(ack worktreecontract.ScheduledAcknowledgement) (*worktreepb.ScheduledAcknowledgement, error) {
	if err := ack.Validate(); err != nil {
		return nil, err
	}
	message := &worktreepb.ScheduledAcknowledgement{OperationId: ack.OperationID.String()}
	return message, Validate(message)
}

func WorktreeScheduledAcknowledgementFromProto(message *worktreepb.ScheduledAcknowledgement) (worktreecontract.ScheduledAcknowledgement, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.ScheduledAcknowledgement{}, err
	}
	operationID, err := worktreecontract.ParseOperationID(message.OperationId)
	if err != nil {
		return worktreecontract.ScheduledAcknowledgement{}, err
	}
	return worktreecontract.ScheduledAcknowledgement{OperationID: operationID}, nil
}

func WorktreeDeleteSuccessToProto(result worktreecontract.DeleteResult) (*worktreepb.DeleteSuccess, error) {
	if err := result.Validate(); err != nil {
		return nil, err
	}
	message := &worktreepb.DeleteSuccess{}
	switch result.Kind {
	case worktreecontract.DeleteResultKindCompleted:
		cleanup, err := worktreeBranchCleanupOutcomeToProto(result.Completed.Cleanup)
		if err != nil {
			return nil, err
		}
		message.Result = &worktreepb.DeleteSuccess_Completed{
			Completed: &worktreepb.DeleteCompleted{
				Cleanup: cleanup, LeftoverRoot: clonePointer(result.Completed.LeftoverRoot),
			},
		}
	case worktreecontract.DeleteResultKindScheduled:
		scheduled, err := WorktreeScheduledAcknowledgementToProto(*result.Scheduled)
		if err != nil {
			return nil, err
		}
		message.Result = &worktreepb.DeleteSuccess_Scheduled{Scheduled: scheduled}
	default:
		return nil, fmt.Errorf("worktree delete result kind %q is unsupported", result.Kind)
	}
	return message, Validate(message)
}

func WorktreeDeleteSuccessFromProto(message *worktreepb.DeleteSuccess) (worktreecontract.DeleteResult, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.DeleteResult{}, err
	}
	var result worktreecontract.DeleteResult
	switch value := message.Result.(type) {
	case *worktreepb.DeleteSuccess_Completed:
		cleanup, err := worktreeBranchCleanupOutcomeFromProto(value.Completed.Cleanup)
		if err != nil {
			return worktreecontract.DeleteResult{}, err
		}
		result.Kind = worktreecontract.DeleteResultKindCompleted
		result.Completed = &worktreecontract.DeleteCompletedResult{
			Cleanup: cleanup, LeftoverRoot: clonePointer(value.Completed.LeftoverRoot),
		}
	case *worktreepb.DeleteSuccess_Scheduled:
		scheduled, err := WorktreeScheduledAcknowledgementFromProto(value.Scheduled)
		if err != nil {
			return worktreecontract.DeleteResult{}, err
		}
		result.Kind = worktreecontract.DeleteResultKindScheduled
		result.Scheduled = &scheduled
	default:
		return worktreecontract.DeleteResult{}, fmt.Errorf("protobuf Worktree delete result %T is unsupported", value)
	}
	return result, result.Validate()
}

func WorktreeDirtyStateToProto(state worktreecontract.DirtyState) (*worktreepb.DirtyState, error) {
	if err := state.Validate(); err != nil {
		return nil, err
	}
	var kind worktreepb.DirtyStateKind
	switch state.Kind {
	case worktreecontract.DirtyStateClean:
		kind = worktreepb.DirtyStateKind_DIRTY_STATE_CLEAN
	case worktreecontract.DirtyStateDirty:
		kind = worktreepb.DirtyStateKind_DIRTY_STATE_DIRTY
	case worktreecontract.DirtyStateUnknown:
		kind = worktreepb.DirtyStateKind_DIRTY_STATE_UNKNOWN
	default:
		return nil, fmt.Errorf("worktree dirty state kind %q is unsupported", state.Kind)
	}
	var dirtyFileCount *int32
	if state.DirtyFileCount != nil {
		value, err := projectInt32(*state.DirtyFileCount, "worktree dirty file count")
		if err != nil {
			return nil, err
		}
		dirtyFileCount = &value
	}
	message := &worktreepb.DirtyState{
		Kind:           kind,
		DirtyFileCount: dirtyFileCount,
		UnknownCause:   clonePointer(state.UnknownCause),
	}
	return message, Validate(message)
}

func WorktreeDirtyStateFromProto(message *worktreepb.DirtyState) (worktreecontract.DirtyState, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.DirtyState{}, err
	}
	var kind worktreecontract.DirtyStateKind
	switch message.Kind {
	case worktreepb.DirtyStateKind_DIRTY_STATE_CLEAN:
		kind = worktreecontract.DirtyStateClean
	case worktreepb.DirtyStateKind_DIRTY_STATE_DIRTY:
		kind = worktreecontract.DirtyStateDirty
	case worktreepb.DirtyStateKind_DIRTY_STATE_UNKNOWN:
		kind = worktreecontract.DirtyStateUnknown
	default:
		return worktreecontract.DirtyState{}, fmt.Errorf("protobuf Worktree dirty state kind %v is unsupported", message.Kind)
	}
	var dirtyFileCount *int
	if message.DirtyFileCount != nil {
		value := int(*message.DirtyFileCount)
		dirtyFileCount = &value
	}
	state := worktreecontract.DirtyState{
		Kind:           kind,
		DirtyFileCount: dirtyFileCount,
		UnknownCause:   clonePointer(message.UnknownCause),
	}
	return state, state.Validate()
}

func worktreeCreateTargetResolutionToProto(resolution worktreecontract.CreateTargetResolution) (*worktreepb.CreateTargetResolution, error) {
	var kind worktreepb.CreateTargetResolutionKind
	switch resolution.Kind {
	case worktreecontract.CreateTargetResolutionKindNewBranch:
		kind = worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_NEW_BRANCH
	case worktreecontract.CreateTargetResolutionKindExistingBranch:
		kind = worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_EXISTING_BRANCH
	case worktreecontract.CreateTargetResolutionKindDetachedRef:
		kind = worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_DETACHED_REF
	default:
		return nil, fmt.Errorf("worktree create target resolution kind %q is unsupported", resolution.Kind)
	}
	message := &worktreepb.CreateTargetResolution{
		Input: resolution.Input, Kind: kind, ResolvedRef: nonblankStringPointer(resolution.ResolvedRef),
	}
	return message, Validate(message)
}

func worktreeCreateTargetResolutionFromProto(message *worktreepb.CreateTargetResolution) (worktreecontract.CreateTargetResolution, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.CreateTargetResolution{}, err
	}
	var kind worktreecontract.CreateTargetResolutionKind
	switch message.Kind {
	case worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_NEW_BRANCH:
		kind = worktreecontract.CreateTargetResolutionKindNewBranch
	case worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_EXISTING_BRANCH:
		kind = worktreecontract.CreateTargetResolutionKindExistingBranch
	case worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_DETACHED_REF:
		kind = worktreecontract.CreateTargetResolutionKindDetachedRef
	default:
		return worktreecontract.CreateTargetResolution{}, fmt.Errorf(
			"protobuf Worktree create target resolution kind %v is unsupported",
			message.Kind,
		)
	}
	return worktreecontract.CreateTargetResolution{
		Input: message.Input, Kind: kind, ResolvedRef: dereference(message.ResolvedRef),
	}, nil
}

func worktreeBranchCleanupOutcomeToProto(outcome worktreecontract.BranchCleanupOutcome) (*worktreepb.BranchCleanupOutcome, error) {
	if err := outcome.Validate(); err != nil {
		return nil, err
	}
	var kind worktreepb.BranchCleanupOutcomeKind
	switch outcome.Kind {
	case worktreecontract.BranchCleanupOutcomeNotRequested:
		kind = worktreepb.BranchCleanupOutcomeKind_WORKTREE_BRANCH_CLEANUP_OUTCOME_NOT_REQUESTED
	case worktreecontract.BranchCleanupOutcomeNotApplicable:
		kind = worktreepb.BranchCleanupOutcomeKind_WORKTREE_BRANCH_CLEANUP_OUTCOME_NOT_APPLICABLE
	case worktreecontract.BranchCleanupOutcomeDeleted:
		kind = worktreepb.BranchCleanupOutcomeKind_WORKTREE_BRANCH_CLEANUP_OUTCOME_DELETED
	case worktreecontract.BranchCleanupOutcomeRetained:
		kind = worktreepb.BranchCleanupOutcomeKind_WORKTREE_BRANCH_CLEANUP_OUTCOME_RETAINED
	default:
		return nil, fmt.Errorf("worktree branch cleanup outcome kind %q is unsupported", outcome.Kind)
	}
	message := &worktreepb.BranchCleanupOutcome{
		Kind: kind, BranchName: clonePointer(outcome.BranchName), Diagnostic: clonePointer(outcome.Diagnostic),
	}
	return message, Validate(message)
}

func worktreeBranchCleanupOutcomeFromProto(message *worktreepb.BranchCleanupOutcome) (worktreecontract.BranchCleanupOutcome, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.BranchCleanupOutcome{}, err
	}
	var kind worktreecontract.BranchCleanupOutcomeKind
	switch message.Kind {
	case worktreepb.BranchCleanupOutcomeKind_WORKTREE_BRANCH_CLEANUP_OUTCOME_NOT_REQUESTED:
		kind = worktreecontract.BranchCleanupOutcomeNotRequested
	case worktreepb.BranchCleanupOutcomeKind_WORKTREE_BRANCH_CLEANUP_OUTCOME_NOT_APPLICABLE:
		kind = worktreecontract.BranchCleanupOutcomeNotApplicable
	case worktreepb.BranchCleanupOutcomeKind_WORKTREE_BRANCH_CLEANUP_OUTCOME_DELETED:
		kind = worktreecontract.BranchCleanupOutcomeDeleted
	case worktreepb.BranchCleanupOutcomeKind_WORKTREE_BRANCH_CLEANUP_OUTCOME_RETAINED:
		kind = worktreecontract.BranchCleanupOutcomeRetained
	default:
		return worktreecontract.BranchCleanupOutcome{}, fmt.Errorf(
			"protobuf Worktree branch cleanup outcome kind %v is unsupported",
			message.Kind,
		)
	}
	outcome := worktreecontract.BranchCleanupOutcome{
		Kind: kind, BranchName: clonePointer(message.BranchName), Diagnostic: clonePointer(message.Diagnostic),
	}
	return outcome, outcome.Validate()
}

func worktreeListEntriesToProto(entries []worktreecontract.ListEntry) ([]*worktreepb.ListEntry, error) {
	result := make([]*worktreepb.ListEntry, 0, len(entries))
	for _, entry := range entries {
		converted, err := worktreeListEntryToProto(entry)
		if err != nil {
			return nil, err
		}
		result = append(result, converted)
	}
	return result, nil
}

func worktreeListEntriesFromProto(entries []*worktreepb.ListEntry) ([]worktreecontract.ListEntry, error) {
	result := make([]worktreecontract.ListEntry, 0, len(entries))
	for _, entry := range entries {
		converted, err := worktreeListEntryFromProto(entry)
		if err != nil {
			return nil, err
		}
		result = append(result, converted)
	}
	return result, nil
}

func worktreeListEntryToProto(entry worktreecontract.ListEntry) (*worktreepb.ListEntry, error) {
	if err := entry.Validate(); err != nil {
		return nil, err
	}
	topology, err := WorktreeTopologyEntryToProto(entry.Topology)
	if err != nil {
		return nil, err
	}
	projection, err := worktreeListProjectionToProto(entry.Projection)
	if err != nil {
		return nil, err
	}
	message := &worktreepb.ListEntry{Topology: topology, Projection: projection}
	return message, Validate(message)
}

func worktreeListEntryFromProto(message *worktreepb.ListEntry) (worktreecontract.ListEntry, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.ListEntry{}, err
	}
	topology, err := WorktreeTopologyEntryFromProto(message.Topology)
	if err != nil {
		return worktreecontract.ListEntry{}, err
	}
	projection, err := worktreeListProjectionFromProto(message.Projection)
	if err != nil {
		return worktreecontract.ListEntry{}, err
	}
	entry := worktreecontract.ListEntry{Topology: topology, Projection: projection}
	return entry, entry.Validate()
}

func WorktreeTopologyEntryToProto(entry worktreecontract.TopologyEntry) (*worktreepb.TopologyEntry, error) {
	if err := entry.Validate(); err != nil {
		return nil, err
	}
	message := &worktreepb.TopologyEntry{}
	switch entry.Variant {
	case worktreecontract.TopologyVariantRegistered:
		registered, err := worktreeRegisteredFactsToProto(*entry.Registered)
		if err != nil {
			return nil, err
		}
		message.Topology = &worktreepb.TopologyEntry_Registered{Registered: registered}
	case worktreecontract.TopologyVariantExternal:
		git, err := worktreeGitFactsToProto(entry.External.Git)
		if err != nil {
			return nil, err
		}
		message.Topology = &worktreepb.TopologyEntry_External{
			External: &worktreepb.ExternalFacts{Git: git},
		}
	case worktreecontract.TopologyVariantMissing:
		kent, err := worktreeKentFactsToProto(entry.Missing.Kent)
		if err != nil {
			return nil, err
		}
		message.Topology = &worktreepb.TopologyEntry_Missing{
			Missing: &worktreepb.MissingFacts{Kent: kent},
		}
	default:
		return nil, fmt.Errorf("worktree topology variant %q is unsupported", entry.Variant)
	}
	return message, Validate(message)
}

func WorktreeTopologyEntryFromProto(message *worktreepb.TopologyEntry) (worktreecontract.TopologyEntry, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.TopologyEntry{}, err
	}
	var entry worktreecontract.TopologyEntry
	switch topology := message.Topology.(type) {
	case *worktreepb.TopologyEntry_Registered:
		registered, err := worktreeRegisteredFactsFromProto(topology.Registered)
		if err != nil {
			return worktreecontract.TopologyEntry{}, err
		}
		entry.Variant = worktreecontract.TopologyVariantRegistered
		entry.Registered = &registered
	case *worktreepb.TopologyEntry_External:
		git, err := worktreeGitFactsFromProto(topology.External.Git)
		if err != nil {
			return worktreecontract.TopologyEntry{}, err
		}
		entry.Variant = worktreecontract.TopologyVariantExternal
		entry.External = &worktreecontract.ExternalFacts{Git: git}
	case *worktreepb.TopologyEntry_Missing:
		kent, err := worktreeKentFactsFromProto(topology.Missing.Kent)
		if err != nil {
			return worktreecontract.TopologyEntry{}, err
		}
		entry.Variant = worktreecontract.TopologyVariantMissing
		entry.Missing = &worktreecontract.MissingFacts{Kent: kent}
	default:
		return worktreecontract.TopologyEntry{}, fmt.Errorf("protobuf Worktree topology %T is unsupported", topology)
	}
	return entry, entry.Validate()
}

func worktreeRegisteredFactsToProto(facts worktreecontract.RegisteredFacts) (*worktreepb.RegisteredFacts, error) {
	if err := facts.Validate(); err != nil {
		return nil, err
	}
	git, err := worktreeGitFactsToProto(facts.Git)
	if err != nil {
		return nil, err
	}
	kent, err := worktreeKentFactsToProto(facts.Kent)
	if err != nil {
		return nil, err
	}
	message := &worktreepb.RegisteredFacts{Git: git, Kent: kent}
	return message, Validate(message)
}

func worktreeRegisteredFactsFromProto(message *worktreepb.RegisteredFacts) (worktreecontract.RegisteredFacts, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.RegisteredFacts{}, err
	}
	git, err := worktreeGitFactsFromProto(message.Git)
	if err != nil {
		return worktreecontract.RegisteredFacts{}, err
	}
	kent, err := worktreeKentFactsFromProto(message.Kent)
	if err != nil {
		return worktreecontract.RegisteredFacts{}, err
	}
	facts := worktreecontract.RegisteredFacts{Git: git, Kent: kent}
	return facts, facts.Validate()
}

func worktreeGitFactsToProto(facts worktreecontract.GitFacts) (*worktreepb.GitFacts, error) {
	if err := facts.Validate(); err != nil {
		return nil, err
	}
	message := &worktreepb.GitFacts{
		CanonicalRoot:  facts.CanonicalRoot,
		HeadObject:     facts.HeadObject,
		BranchRef:      clonePointer(facts.BranchRef),
		BranchName:     clonePointer(facts.BranchName),
		Detached:       facts.Detached,
		Bare:           facts.Bare,
		LockedReason:   clonePointer(facts.LockedReason),
		PrunableReason: clonePointer(facts.PrunableReason),
		IsMain:         facts.IsMain,
		PathAvailable:  facts.PathAvailable,
	}
	return message, Validate(message)
}

func worktreeGitFactsFromProto(message *worktreepb.GitFacts) (worktreecontract.GitFacts, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.GitFacts{}, err
	}
	facts := worktreecontract.GitFacts{
		CanonicalRoot:  message.CanonicalRoot,
		HeadObject:     message.HeadObject,
		BranchRef:      clonePointer(message.BranchRef),
		BranchName:     clonePointer(message.BranchName),
		Detached:       message.Detached,
		Bare:           message.Bare,
		LockedReason:   clonePointer(message.LockedReason),
		PrunableReason: clonePointer(message.PrunableReason),
		IsMain:         message.IsMain,
		PathAvailable:  message.PathAvailable,
	}
	return facts, facts.Validate()
}

func worktreeKentFactsToProto(facts worktreecontract.KentFacts) (*worktreepb.KentFacts, error) {
	if err := facts.Validate(); err != nil {
		return nil, err
	}
	message := &worktreepb.KentFacts{
		WorktreeId:      facts.WorktreeID,
		CanonicalRoot:   facts.CanonicalRoot,
		DisplayName:     facts.DisplayName,
		Managed:         facts.Managed,
		CreatedBranch:   facts.CreatedBranch,
		OriginSessionId: clonePointer(facts.OriginSessionID),
	}
	return message, Validate(message)
}

func worktreeKentFactsFromProto(message *worktreepb.KentFacts) (worktreecontract.KentFacts, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.KentFacts{}, err
	}
	facts := worktreecontract.KentFacts{
		WorktreeID:      message.WorktreeId,
		CanonicalRoot:   message.CanonicalRoot,
		DisplayName:     message.DisplayName,
		Managed:         message.Managed,
		CreatedBranch:   message.CreatedBranch,
		OriginSessionID: clonePointer(message.OriginSessionId),
	}
	return facts, facts.Validate()
}

func worktreeListProjectionToProto(projection worktreecontract.ListProjection) (*worktreepb.ListProjection, error) {
	if err := projection.Validate(); err != nil {
		return nil, err
	}
	var switchOperation *worktreepb.SwitchOperation
	if projection.Switch != nil {
		var kind worktreepb.SwitchOperationKind
		switch projection.Switch.Kind {
		case worktreecontract.SwitchOperationEnter:
			kind = worktreepb.SwitchOperationKind_WORKTREE_SWITCH_OPERATION_ENTER
		case worktreecontract.SwitchOperationLeaveMain:
			kind = worktreepb.SwitchOperationKind_WORKTREE_SWITCH_OPERATION_LEAVE_MAIN
		default:
			return nil, fmt.Errorf("worktree switch operation kind %q is unsupported", projection.Switch.Kind)
		}
		switchOperation = &worktreepb.SwitchOperation{
			Kind:     kind,
			Selector: clonePointer(projection.Switch.Selector),
		}
	}
	var deletePreview *worktreepb.DeletePreviewOperation
	if projection.DeletePreview != nil {
		deletePreview = &worktreepb.DeletePreviewOperation{Selector: projection.DeletePreview.Selector}
	}
	message := &worktreepb.ListProjection{
		Selector:         projection.Selector,
		IsCurrent:        projection.IsCurrent,
		Switch:           switchOperation,
		DeletePreview:    deletePreview,
		FallbackIdentity: clonePointer(projection.FallbackIdentity),
	}
	return message, Validate(message)
}

func worktreeListProjectionFromProto(message *worktreepb.ListProjection) (worktreecontract.ListProjection, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.ListProjection{}, err
	}
	var switchOperation *worktreecontract.SwitchOperation
	if message.Switch != nil {
		var kind worktreecontract.SwitchOperationKind
		switch message.Switch.Kind {
		case worktreepb.SwitchOperationKind_WORKTREE_SWITCH_OPERATION_ENTER:
			kind = worktreecontract.SwitchOperationEnter
		case worktreepb.SwitchOperationKind_WORKTREE_SWITCH_OPERATION_LEAVE_MAIN:
			kind = worktreecontract.SwitchOperationLeaveMain
		default:
			return worktreecontract.ListProjection{}, fmt.Errorf(
				"protobuf Worktree switch operation kind %v is unsupported",
				message.Switch.Kind,
			)
		}
		switchOperation = &worktreecontract.SwitchOperation{
			Kind:     kind,
			Selector: clonePointer(message.Switch.Selector),
		}
	}
	var deletePreview *worktreecontract.DeletePreviewOperation
	if message.DeletePreview != nil {
		deletePreview = &worktreecontract.DeletePreviewOperation{Selector: message.DeletePreview.Selector}
	}
	projection := worktreecontract.ListProjection{
		Selector:         message.Selector,
		IsCurrent:        message.IsCurrent,
		Switch:           switchOperation,
		DeletePreview:    deletePreview,
		FallbackIdentity: clonePointer(message.FallbackIdentity),
	}
	return projection, projection.Validate()
}
