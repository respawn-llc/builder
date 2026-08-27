package client

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"core/shared/apicontract"
	"core/shared/protoapi"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/protocol"
	"core/shared/serverapi"
	"core/shared/worktreecontract"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func worktreeMethod(service, method protoreflect.Name) protoreflect.MethodDescriptor {
	return bootstrapMethod(worktreepb.File_kent_api_worktree_worktree_proto, service, method)
}

func callWorktreeBinary[
	Request proto.Message,
	Success any,
	Failure comparableProtoMessage,
	Result generatedUnaryResult[Success, Failure],
](
	c *Remote,
	ctx context.Context,
	service, method protoreflect.Name,
	request Request,
	result Result,
	decodeFailure func(Failure) error,
	classifyValidation ...func(error) error,
) (Success, error) {
	return callGeneratedBinary(c, ctx, worktreeMethod(service, method), request, result, decodeFailure, classifyValidation...)
}

func (c *Remote) GetWorktreeStatus(ctx context.Context, request *worktreepb.StatusRequest) (*worktreepb.StatusSuccess, error) {
	return callWorktreeBinary(c, ctx, "StatusService", "Get", request, &worktreepb.StatusResult{}, worktreePlatformError[*worktreepb.StatusError])
}

func (c *Remote) ListWorktrees(ctx context.Context, request *worktreepb.ListRequest) (*worktreepb.ListSuccess, error) {
	success, err := callWorktreeBinary(c, ctx, "ListService", "List", request, &worktreepb.ListResult{}, worktreePlatformError[*worktreepb.ListError])
	if err != nil {
		return nil, err
	}
	if err := validateWorktreeListFallbacks(success.Worktrees); err != nil {
		return nil, invalidResponseError("worktree list", err)
	}
	return success, nil
}

func (c *Remote) ListWorkspaceWorktrees(ctx context.Context, request *worktreepb.WorkspaceListRequest) (*worktreepb.WorkspaceListSuccess, error) {
	success, err := callWorktreeBinary(c, ctx, "ListService", "ListWorkspace", request, &worktreepb.WorkspaceListResult{},
		func(failure *worktreepb.WorkspaceListError) error {
			if failure.GetWorkspaceNotRegistered() != nil {
				return serverapi.ErrWorkspaceNotRegistered
			}
			return worktreePlatformError(failure)
		})
	if err != nil {
		return nil, err
	}
	if success.WorkspaceId != request.WorkspaceId {
		return nil, invalidResponseError("workspace worktree list", fmt.Errorf(
			"response workspace %q does not match requested workspace %q",
			success.WorkspaceId,
			request.WorkspaceId,
		))
	}
	if err := validateWorktreeListFallbacks(success.Worktrees); err != nil {
		return nil, invalidResponseError("workspace worktree list", err)
	}
	return success, nil
}

func (c *Remote) ResolveWorktreeSelector(ctx context.Context, request *worktreepb.SelectorResolveRequest) (*worktreepb.SelectorResolveSuccess, error) {
	success, err := callWorktreeBinary(c, ctx, "SelectorService", "Resolve", request, &worktreepb.SelectorResolveResult{},
		func(failure *worktreepb.SelectorResolveError) error {
			if details := failure.GetSelectorError(); details != nil {
				return &worktreecontract.SelectorError{Details: details}
			}
			return worktreePlatformError(failure)
		})
	if err != nil {
		return nil, err
	}
	if err := validateWorktreeListFallback(success.Worktree); err != nil {
		return nil, invalidResponseError("worktree selector", err)
	}
	return success, nil
}

func (c *Remote) PreviewWorktreeDelete(ctx context.Context, request *worktreepb.DeletePreviewRequest) (*worktreepb.DeletePreviewSuccess, error) {
	return callWorktreeBinary(c, ctx, "DeletePreviewService", "Get", request, &worktreepb.DeletePreviewResult{},
		func(failure *worktreepb.DeletePreviewError) error {
			switch {
			case failure.GetSelectorError() != nil:
				return &worktreecontract.SelectorError{Details: failure.GetSelectorError()}
			case failure.GetWorktreeBlocked() != nil:
				return newWorktreeBlockedError(failure.GetWorktreeBlocked())
			default:
				return worktreePlatformError(failure)
			}
		})
}

func (c *Remote) ResolveWorktreeCreateTarget(ctx context.Context, request *worktreepb.CreateTargetResolveRequest) (*worktreepb.CreateTargetResolveSuccess, error) {
	return callWorktreeBinary(c, ctx, "CreateTargetService", "Resolve", request, &worktreepb.CreateTargetResolveResult{}, worktreePlatformError[*worktreepb.CreateTargetResolveError])
}

func (c *Remote) CreateWorktree(ctx context.Context, request *worktreepb.CreateRequest) (*worktreepb.CreateSuccess, error) {
	success, err := callWorktreeBinary(c, ctx, "CreateService", "Create", request, &worktreepb.CreateResult{},
		worktreeCreateError,
		protoapi.ClassifyWorktreeCreateValidation)
	if err != nil {
		return nil, err
	}
	if err := validateWorktreeListFallback(success.Worktree); err != nil {
		return nil, invalidResponseError("worktree create", err)
	}
	return success, nil
}

func (c *Remote) EnterWorktree(ctx context.Context, request *worktreepb.EnterRequest) (*worktreepb.ScheduledAcknowledgement, error) {
	success, err := callWorktreeBinary(c, ctx, "TransitionService", "Enter", request, &worktreepb.EnterResult{}, worktreeSelectorError[*worktreepb.EnterError])
	if err != nil {
		return nil, err
	}
	return validateWorktreeAcknowledgement("enter", request.OperationId, success)
}

func (c *Remote) LeaveWorktree(ctx context.Context, request *worktreepb.LeaveRequest) (*worktreepb.ScheduledAcknowledgement, error) {
	success, err := callWorktreeBinary(c, ctx, "TransitionService", "Leave", request, &worktreepb.LeaveResult{}, worktreePlatformError[*worktreepb.LeaveError])
	if err != nil {
		return nil, err
	}
	return validateWorktreeAcknowledgement("leave", request.OperationId, success)
}

func (c *Remote) DeleteWorktree(ctx context.Context, request *worktreepb.DeleteRequest) (*worktreepb.DeleteSuccess, error) {
	return callWorktreeBinary(c, ctx, "TransitionService", "Delete", request, &worktreepb.DeleteResult{},
		func(failure *worktreepb.DeleteError) error {
			switch {
			case failure.GetSelectorError() != nil:
				return &worktreecontract.SelectorError{Details: failure.GetSelectorError()}
			case failure.GetWorktreeBlocked() != nil:
				return newWorktreeBlockedError(failure.GetWorktreeBlocked())
			case failure.GetDeletePrecondition() != nil:
				return &worktreecontract.DeletePreconditionError{Details: failure.GetDeletePrecondition()}
			default:
				return worktreePlatformError(failure)
			}
		})
}

func (c *Remote) SubscribeWorktreeSetup(
	ctx context.Context,
	request *worktreepb.SetupSubscribeRequest,
) (apicontract.WorktreeSetupSubscription, error) {
	method := worktreeMethod("SetupService", "Subscribe")
	return subscribeGeneratedBinary(
		c,
		ctx,
		method,
		request,
		&worktreepb.SetupStartResult{},
		func() *worktreepb.SetupEvent { return &worktreepb.SetupEvent{} },
		func() *worktreepb.SetupCompletion { return &worktreepb.SetupCompletion{} },
		func(result *worktreepb.SetupStartResult) error {
			_, err := decodeGeneratedResult(method, result, worktreePlatformError[*worktreepb.SetupStartError])
			return err
		},
	)
}

func validateWorktreeAcknowledgement(
	operation string,
	requestedID string,
	success *worktreepb.ScheduledAcknowledgement,
) (*worktreepb.ScheduledAcknowledgement, error) {
	if success.OperationId != requestedID {
		return nil, invalidResponseError("worktree "+operation, fmt.Errorf(
			"acknowledgement operation %q does not match requested operation %q",
			success.OperationId,
			requestedID,
		))
	}
	return success, nil
}

type worktreePlatformFailure interface {
	GetCode() string
	GetInternalFailure() *sharedpb.InternalFailureDetails
}

type worktreeSelectorFailure interface {
	worktreePlatformFailure
	GetSelectorError() *worktreepb.SelectorErrorDetails
}

func worktreeSelectorError[Failure worktreeSelectorFailure](failure Failure) error {
	if details := failure.GetSelectorError(); details != nil {
		return &worktreecontract.SelectorError{Details: details}
	}
	return worktreePlatformError(failure)
}

func worktreePlatformError[Failure worktreePlatformFailure](failure Failure) error {
	switch failure.GetCode() {
	case "auth_required":
		return serverapi.ErrServerAuthRequired
	case "internal_failure":
		return protoapi.InternalFailureFromProto(failure.GetInternalFailure())
	default:
		return generatedOperationFailure(failure.GetCode())
	}
}

func worktreeCreateError(failure *worktreepb.CreateError) error {
	switch {
	case failure.GetCreateFailed() != nil:
		details := failure.GetCreateFailed()
		var owner worktreecontract.CreateErrorOwner
		switch details.Owner {
		case worktreepb.CreateErrorOwner_WORKTREE_CREATE_ERROR_OWNER_BASE_REF:
			owner = worktreecontract.CreateErrorOwnerBaseRef
		case worktreepb.CreateErrorOwner_WORKTREE_CREATE_ERROR_OWNER_FORM:
			owner = worktreecontract.CreateErrorOwnerForm
		default:
			return generatedOperationFailure(failure.Code)
		}
		return worktreecontract.NewCreateError(owner, details.Diagnostic, nil)
	case failure.GetSetupRetained() != nil:
		details := failure.GetSetupRetained()
		return &worktreecontract.SetupRetainedError{Details: details}
	default:
		return worktreePlatformError(failure)
	}
}

func newWorktreeBlockedError(details *worktreepb.BlockedDetails) error {
	if details == nil {
		return worktreecontract.ErrWorktreeBlocked
	}
	return protocol.NewSentinelError(worktreecontract.ErrWorktreeBlocked, details.Diagnostic)
}

func validateWorktreeListFallbacks(entries []*worktreepb.ListEntry) error {
	for _, entry := range entries {
		if err := validateWorktreeListFallback(entry); err != nil {
			return err
		}
	}
	return nil
}

func validateWorktreeListFallback(entry *worktreepb.ListEntry) error {
	external := entry.Topology.GetExternal()
	if external == nil || external.Git == nil || external.Git.BranchName != nil {
		return nil
	}
	expected := filepath.Base(external.Git.CanonicalRoot)
	if entry.Projection.FallbackIdentity == nil || *entry.Projection.FallbackIdentity != expected {
		return errors.New("external worktree fallback identity does not match its filesystem basename")
	}
	return nil
}
