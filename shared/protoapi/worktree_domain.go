package protoapi

import (
	"fmt"

	"core/shared/clientui"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/worktreecontract"
)

func WorktreeStatusRequestToProto(request worktreecontract.StatusRequest) (*worktreepb.StatusRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	message := &worktreepb.StatusRequest{SessionId: request.SessionID}
	return message, Validate(message)
}

func WorktreeStatusRequestFromProto(message *worktreepb.StatusRequest) (worktreecontract.StatusRequest, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.StatusRequest{}, err
	}
	return worktreecontract.StatusRequest{SessionID: message.SessionId}, nil
}

func WorktreeListRequestToProto(request worktreecontract.ListRequest) (*worktreepb.ListRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	message := &worktreepb.ListRequest{SessionId: request.SessionID}
	return message, Validate(message)
}

func WorktreeListRequestFromProto(message *worktreepb.ListRequest) (worktreecontract.ListRequest, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.ListRequest{}, err
	}
	return worktreecontract.ListRequest{SessionID: message.SessionId}, nil
}

func WorktreeWorkspaceListRequestToProto(request worktreecontract.WorkspaceListRequest) (*worktreepb.WorkspaceListRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	message := &worktreepb.WorkspaceListRequest{
		ProjectId:   request.ProjectID,
		WorkspaceId: request.WorkspaceID,
	}
	return message, Validate(message)
}

func WorktreeWorkspaceListRequestFromProto(message *worktreepb.WorkspaceListRequest) (worktreecontract.WorkspaceListRequest, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.WorkspaceListRequest{}, err
	}
	return worktreecontract.WorkspaceListRequest{
		ProjectID:   message.ProjectId,
		WorkspaceID: message.WorkspaceId,
	}, nil
}

func WorktreeStatusSuccessToProto(response worktreecontract.StatusResponse) (*worktreepb.StatusSuccess, error) {
	if err := response.Validate(); err != nil {
		return nil, err
	}
	target, err := worktreeSessionExecutionTargetToProto(response.Target)
	if err != nil {
		return nil, err
	}
	problems := make([]*worktreepb.StatusProblem, 0, len(response.Problems))
	for _, problem := range response.Problems {
		converted, conversionErr := worktreeStatusProblemToProto(problem)
		if conversionErr != nil {
			return nil, conversionErr
		}
		problems = append(problems, converted)
	}
	message := &worktreepb.StatusSuccess{
		Target: target,
		Worktree: &worktreepb.StatusTarget{
			RecordedRoot:      response.Worktree.RecordedRoot,
			ObservedRoot:      clonePointer(response.Worktree.ObservedRoot),
			DisplayName:       clonePointer(response.Worktree.DisplayName),
			RecordedBranchRef: clonePointer(response.Worktree.RecordedBranchRef),
			ObservedBranchRef: clonePointer(response.Worktree.ObservedBranchRef),
		},
		Problems: problems,
	}
	return message, Validate(message)
}

func WorktreeStatusSuccessFromProto(message *worktreepb.StatusSuccess) (worktreecontract.StatusResponse, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.StatusResponse{}, err
	}
	target, err := worktreeSessionExecutionTargetFromProto(message.Target)
	if err != nil {
		return worktreecontract.StatusResponse{}, err
	}
	problems := make([]worktreecontract.StatusProblem, 0, len(message.Problems))
	for _, problem := range message.Problems {
		converted, conversionErr := worktreeStatusProblemFromProto(problem)
		if conversionErr != nil {
			return worktreecontract.StatusResponse{}, conversionErr
		}
		problems = append(problems, converted)
	}
	response := worktreecontract.StatusResponse{
		Target: target,
		Worktree: worktreecontract.StatusTarget{
			RecordedRoot:      message.Worktree.RecordedRoot,
			ObservedRoot:      clonePointer(message.Worktree.ObservedRoot),
			DisplayName:       clonePointer(message.Worktree.DisplayName),
			RecordedBranchRef: clonePointer(message.Worktree.RecordedBranchRef),
			ObservedBranchRef: clonePointer(message.Worktree.ObservedBranchRef),
		},
		Problems: problems,
	}
	return response, response.Validate()
}

func worktreeSessionExecutionTargetToProto(target worktreecontract.SessionExecutionTarget) (*worktreepb.SessionExecutionTarget, error) {
	workspaceAvailability, err := worktreeProjectAvailabilityToProto(target.WorkspaceAvailability)
	if err != nil {
		return nil, err
	}
	var worktree *worktreepb.SessionExecutionWorktreeTarget
	if target.Worktree != nil {
		availability, conversionErr := worktreeProjectAvailabilityToProto(
			worktreecontract.ProjectAvailability(target.Worktree.Availability),
		)
		if conversionErr != nil {
			return nil, conversionErr
		}
		worktree = &worktreepb.SessionExecutionWorktreeTarget{
			Id:           target.Worktree.ID,
			Name:         target.Worktree.Name,
			Root:         target.Worktree.Root,
			Availability: availability,
		}
	}
	message := &worktreepb.SessionExecutionTarget{
		WorkspaceId:           target.WorkspaceID,
		WorkspaceName:         target.WorkspaceName,
		WorkspaceRoot:         target.WorkspaceRoot,
		WorkspaceAvailability: workspaceAvailability,
		Worktree:              worktree,
		CwdRelpath:            target.CwdRelpath,
		EffectiveWorkdir:      target.EffectiveWorkdir,
	}
	return message, Validate(message)
}

func worktreeSessionExecutionTargetFromProto(message *worktreepb.SessionExecutionTarget) (worktreecontract.SessionExecutionTarget, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.SessionExecutionTarget{}, err
	}
	workspaceAvailability, err := worktreeProjectAvailabilityFromProto(message.WorkspaceAvailability)
	if err != nil {
		return worktreecontract.SessionExecutionTarget{}, err
	}
	var worktree *worktreecontract.SessionExecutionWorktreeTarget
	if message.Worktree != nil {
		availability, conversionErr := worktreeProjectAvailabilityFromProto(message.Worktree.Availability)
		if conversionErr != nil {
			return worktreecontract.SessionExecutionTarget{}, conversionErr
		}
		worktree = &worktreecontract.SessionExecutionWorktreeTarget{
			ID:           message.Worktree.Id,
			Name:         message.Worktree.Name,
			Root:         message.Worktree.Root,
			Availability: string(availability),
		}
	}
	return worktreecontract.SessionExecutionTarget{
		WorkspaceID:           message.WorkspaceId,
		WorkspaceName:         message.WorkspaceName,
		WorkspaceRoot:         message.WorkspaceRoot,
		WorkspaceAvailability: workspaceAvailability,
		Worktree:              worktree,
		CwdRelpath:            message.CwdRelpath,
		EffectiveWorkdir:      message.EffectiveWorkdir,
	}, nil
}

func worktreeProjectAvailabilityToProto(value worktreecontract.ProjectAvailability) (projectpb.ProjectAvailability, error) {
	return projectAvailabilityToProto(clientui.ProjectAvailability(value))
}

func worktreeProjectAvailabilityFromProto(value projectpb.ProjectAvailability) (worktreecontract.ProjectAvailability, error) {
	availability, err := projectAvailabilityFromProto(value)
	if err != nil {
		return "", err
	}
	return worktreecontract.ProjectAvailability(availability), nil
}

func worktreeStatusProblemToProto(problem worktreecontract.StatusProblem) (*worktreepb.StatusProblem, error) {
	var kind worktreepb.StatusProblemKind
	switch problem.Kind {
	case worktreecontract.StatusProblemRootMissing:
		kind = worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_ROOT_MISSING
	case worktreecontract.StatusProblemRootInaccessible:
		kind = worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_ROOT_INACCESSIBLE
	case worktreecontract.StatusProblemGitBindingMissing:
		kind = worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_GIT_BINDING_MISSING
	case worktreecontract.StatusProblemGitBindingMismatched:
		kind = worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_GIT_BINDING_MISMATCHED
	case worktreecontract.StatusProblemRecordedRefMissing:
		kind = worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_RECORDED_REF_MISSING
	default:
		return nil, fmt.Errorf("worktree status problem kind %q is unsupported", problem.Kind)
	}
	message := &worktreepb.StatusProblem{
		Kind: kind,
		Root: clonePointer(problem.Root),
		Ref:  clonePointer(problem.Ref),
	}
	return message, Validate(message)
}

func worktreeStatusProblemFromProto(message *worktreepb.StatusProblem) (worktreecontract.StatusProblem, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.StatusProblem{}, err
	}
	var kind worktreecontract.StatusProblemKind
	switch message.Kind {
	case worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_ROOT_MISSING:
		kind = worktreecontract.StatusProblemRootMissing
	case worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_ROOT_INACCESSIBLE:
		kind = worktreecontract.StatusProblemRootInaccessible
	case worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_GIT_BINDING_MISSING:
		kind = worktreecontract.StatusProblemGitBindingMissing
	case worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_GIT_BINDING_MISMATCHED:
		kind = worktreecontract.StatusProblemGitBindingMismatched
	case worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_RECORDED_REF_MISSING:
		kind = worktreecontract.StatusProblemRecordedRefMissing
	default:
		return worktreecontract.StatusProblem{}, fmt.Errorf("protobuf Worktree status problem kind %v is unsupported", message.Kind)
	}
	return worktreecontract.StatusProblem{
		Kind: kind,
		Root: clonePointer(message.Root),
		Ref:  clonePointer(message.Ref),
	}, nil
}
