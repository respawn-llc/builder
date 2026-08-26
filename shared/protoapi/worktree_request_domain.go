package protoapi

import (
	"fmt"
	"strings"

	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/worktreecontract"
)

func WorktreeSelectorResolveRequestToProto(request worktreecontract.SelectorResolveRequest) (*worktreepb.SelectorResolveRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	message := &worktreepb.SelectorResolveRequest{SessionId: request.SessionID, Selector: request.Selector}
	return message, Validate(message)
}

func WorktreeSelectorResolveRequestFromProto(message *worktreepb.SelectorResolveRequest) (worktreecontract.SelectorResolveRequest, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.SelectorResolveRequest{}, err
	}
	return worktreecontract.SelectorResolveRequest{SessionID: message.SessionId, Selector: message.Selector}, nil
}

func WorktreeDeletePreviewRequestToProto(request worktreecontract.DeletePreviewRequest) (*worktreepb.DeletePreviewRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	message := &worktreepb.DeletePreviewRequest{SessionId: request.SessionID, Selector: request.Selector}
	return message, Validate(message)
}

func WorktreeDeletePreviewRequestFromProto(message *worktreepb.DeletePreviewRequest) (worktreecontract.DeletePreviewRequest, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.DeletePreviewRequest{}, err
	}
	return worktreecontract.DeletePreviewRequest{SessionID: message.SessionId, Selector: message.Selector}, nil
}

func WorktreeCreateTargetResolveRequestToProto(request worktreecontract.CreateTargetResolveRequest) (*worktreepb.CreateTargetResolveRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	message := &worktreepb.CreateTargetResolveRequest{SessionId: request.SessionID, Target: request.Target}
	return message, Validate(message)
}

func WorktreeCreateTargetResolveRequestFromProto(message *worktreepb.CreateTargetResolveRequest) (worktreecontract.CreateTargetResolveRequest, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.CreateTargetResolveRequest{}, err
	}
	return worktreecontract.CreateTargetResolveRequest{SessionID: message.SessionId, Target: message.Target}, nil
}

func WorktreeCreateRequestToProto(request worktreecontract.CreateRequest) (*worktreepb.CreateRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	message := &worktreepb.CreateRequest{
		SetupOperationId: request.SetupOperationID.String(),
		SessionId:        request.SessionID,
		BaseRef:          nonblankStringPointer(request.BaseRef),
		CreateBranch:     request.CreateBranch,
		BranchName:       nonblankStringPointer(request.BranchName),
		RootPath:         nonblankStringPointer(request.RootPath),
	}
	return message, Validate(message)
}

func WorktreeCreateRequestFromProto(message *worktreepb.CreateRequest) (worktreecontract.CreateRequest, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.CreateRequest{}, err
	}
	setupOperationID, err := worktreecontract.ParseSetupOperationID(message.SetupOperationId)
	if err != nil {
		return worktreecontract.CreateRequest{}, err
	}
	request := worktreecontract.CreateRequest{
		SetupOperationID: setupOperationID,
		SessionID:        message.SessionId,
		BaseRef:          dereference(message.BaseRef),
		CreateBranch:     message.CreateBranch,
		BranchName:       dereference(message.BranchName),
		RootPath:         dereference(message.RootPath),
	}
	return request, request.Validate()
}

func WorktreeEnterRequestToProto(request worktreecontract.EnterRequest) (*worktreepb.EnterRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	header, err := worktreeTransitionHeaderToProto(request.TransitionHeader)
	if err != nil {
		return nil, err
	}
	message := &worktreepb.EnterRequest{Transition: header, Selector: request.Selector}
	return message, Validate(message)
}

func WorktreeEnterRequestFromProto(message *worktreepb.EnterRequest) (worktreecontract.EnterRequest, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.EnterRequest{}, err
	}
	header, err := worktreeTransitionHeaderFromProto(message.Transition)
	if err != nil {
		return worktreecontract.EnterRequest{}, err
	}
	request := worktreecontract.EnterRequest{TransitionHeader: header, Selector: message.Selector}
	return request, request.Validate()
}

func WorktreeLeaveRequestToProto(request worktreecontract.LeaveRequest) (*worktreepb.LeaveRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	header, err := worktreeTransitionHeaderToProto(request.TransitionHeader)
	if err != nil {
		return nil, err
	}
	message := &worktreepb.LeaveRequest{Transition: header}
	return message, Validate(message)
}

func WorktreeLeaveRequestFromProto(message *worktreepb.LeaveRequest) (worktreecontract.LeaveRequest, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.LeaveRequest{}, err
	}
	header, err := worktreeTransitionHeaderFromProto(message.Transition)
	if err != nil {
		return worktreecontract.LeaveRequest{}, err
	}
	request := worktreecontract.LeaveRequest{TransitionHeader: header}
	return request, request.Validate()
}

func WorktreeDeleteRequestToProto(request worktreecontract.DeleteRequest) (*worktreepb.DeleteRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	header, err := worktreeTransitionHeaderToProto(request.TransitionHeader)
	if err != nil {
		return nil, err
	}
	policy, err := worktreeBranchCleanupModeToProto(request.BranchCleanupPolicy)
	if err != nil {
		return nil, err
	}
	message := &worktreepb.DeleteRequest{
		Transition:          header,
		Selector:            request.Selector,
		ForceFolderRemoval:  request.ForceFolderRemoval,
		BranchCleanupPolicy: policy,
	}
	return message, Validate(message)
}

func WorktreeDeleteRequestFromProto(message *worktreepb.DeleteRequest) (worktreecontract.DeleteRequest, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.DeleteRequest{}, err
	}
	header, err := worktreeTransitionHeaderFromProto(message.Transition)
	if err != nil {
		return worktreecontract.DeleteRequest{}, err
	}
	policy, err := worktreeBranchCleanupModeFromProto(message.BranchCleanupPolicy)
	if err != nil {
		return worktreecontract.DeleteRequest{}, err
	}
	request := worktreecontract.DeleteRequest{
		TransitionHeader:    header,
		Selector:            message.Selector,
		ForceFolderRemoval:  message.ForceFolderRemoval,
		BranchCleanupPolicy: policy,
	}
	return request, request.Validate()
}

func WorktreeSetupSubscribeRequestToProto(request worktreecontract.SetupSubscribeRequest) (*worktreepb.SetupSubscribeRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	message := &worktreepb.SetupSubscribeRequest{SetupOperationId: request.SetupOperationID.String()}
	return message, Validate(message)
}

func WorktreeSetupSubscribeRequestFromProto(message *worktreepb.SetupSubscribeRequest) (worktreecontract.SetupSubscribeRequest, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.SetupSubscribeRequest{}, err
	}
	setupOperationID, err := worktreecontract.ParseSetupOperationID(message.SetupOperationId)
	if err != nil {
		return worktreecontract.SetupSubscribeRequest{}, err
	}
	return worktreecontract.SetupSubscribeRequest{SetupOperationID: setupOperationID}, nil
}

func worktreeTransitionHeaderToProto(header worktreecontract.TransitionHeader) (*worktreepb.TransitionHeader, error) {
	if err := header.Validate(); err != nil {
		return nil, err
	}
	var origin *worktreepb.RuntimeStepOrigin
	if header.Origin != nil {
		origin = &worktreepb.RuntimeStepOrigin{RunId: header.Origin.RunID, StepId: header.Origin.StepID}
	}
	message := &worktreepb.TransitionHeader{
		OperationId: header.OperationID.String(),
		SessionId:   header.SessionID,
		Origin:      origin,
	}
	return message, Validate(message)
}

func worktreeTransitionHeaderFromProto(message *worktreepb.TransitionHeader) (worktreecontract.TransitionHeader, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.TransitionHeader{}, err
	}
	operationID, err := worktreecontract.ParseOperationID(message.OperationId)
	if err != nil {
		return worktreecontract.TransitionHeader{}, err
	}
	var origin *worktreecontract.RuntimeStepOrigin
	if message.Origin != nil {
		origin = &worktreecontract.RuntimeStepOrigin{RunID: message.Origin.RunId, StepID: message.Origin.StepId}
	}
	header := worktreecontract.TransitionHeader{
		OperationID: operationID,
		SessionID:   message.SessionId,
		Origin:      origin,
	}
	return header, header.Validate()
}

func worktreeBranchCleanupModeToProto(mode worktreecontract.BranchCleanupMode) (worktreepb.BranchCleanupMode, error) {
	switch mode {
	case worktreecontract.BranchCleanupModeRetain:
		return worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_RETAIN, nil
	case worktreecontract.BranchCleanupModeAutoIfKentCreated:
		return worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_AUTO_IF_KENT_CREATED, nil
	case worktreecontract.BranchCleanupModeDeleteSafe:
		return worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_DELETE_SAFE, nil
	case worktreecontract.BranchCleanupModeDeleteForce:
		return worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_DELETE_FORCE, nil
	default:
		return worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_UNSPECIFIED, fmt.Errorf(
			"worktree branch cleanup mode %q is unsupported",
			mode,
		)
	}
}

func worktreeBranchCleanupModeFromProto(mode worktreepb.BranchCleanupMode) (worktreecontract.BranchCleanupMode, error) {
	switch mode {
	case worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_RETAIN:
		return worktreecontract.BranchCleanupModeRetain, nil
	case worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_AUTO_IF_KENT_CREATED:
		return worktreecontract.BranchCleanupModeAutoIfKentCreated, nil
	case worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_DELETE_SAFE:
		return worktreecontract.BranchCleanupModeDeleteSafe, nil
	case worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_DELETE_FORCE:
		return worktreecontract.BranchCleanupModeDeleteForce, nil
	default:
		return "", fmt.Errorf("protobuf Worktree branch cleanup mode %v is unsupported", mode)
	}
}

func nonblankStringPointer(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}
