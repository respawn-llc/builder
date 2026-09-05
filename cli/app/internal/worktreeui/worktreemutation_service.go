package worktreeui

import (
	"context"
	"errors"
	"strings"
	"time"

	"core/shared/apicontract"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
	"core/shared/worktreecontract"
)

const defaultResolveTimeout = 3 * time.Second
const defaultMutationTimeout = 30 * time.Second

var (
	ErrClientUnavailable = errors.New("worktree client is unavailable")
)

type RuntimeControl struct {
	Context                  func() (context.Context, context.CancelFunc)
	MutationContext          func() (context.Context, context.CancelFunc)
	RecoverRuntimeConnection func(context.Context, error, bool) error
	AppendRecoveryWarning    bool
}

type Service struct {
	Client         apicontract.WorktreeService
	SessionID      string
	WorkspaceID    string
	WorkspaceRoot  string
	Runtime        RuntimeControl
	ResolveContext func() (context.Context, context.CancelFunc)
	NewOperationID func() worktreecontract.OperationID
}

func (s Service) List() (*worktreepb.ListSuccess, error) {
	ctx, cancel, err := s.resolveMutationContext(false)
	if err != nil {
		return nil, err
	}
	defer cancel()
	return s.Client.ListWorktrees(ctx, &worktreepb.ListRequest{
		SessionId: s.SessionID,
	})
}

func (s Service) ResolveCreateTarget(target string) (*worktreepb.CreateTargetResolveSuccess, error) {
	if s.Client == nil {
		return nil, ErrClientUnavailable
	}
	ctx, cancel := s.resolveContext()
	defer cancel()
	return s.Client.ResolveWorktreeCreateTarget(ctx, &worktreepb.CreateTargetResolveRequest{
		SessionId: strings.TrimSpace(s.SessionID),
		Target:    target,
	})
}

func (s Service) ResolveSelector(selector string) (*worktreepb.SelectorResolveSuccess, error) {
	if s.Client == nil {
		return nil, ErrClientUnavailable
	}
	ctx, cancel := s.resolveContext()
	defer cancel()
	return s.Client.ResolveWorktreeSelector(ctx, &worktreepb.SelectorResolveRequest{
		SessionId: strings.TrimSpace(s.SessionID),
		Selector:  strings.TrimSpace(selector),
	})
}

func (s Service) Create(req *worktreepb.CreateRequest) (*worktreepb.CreateSuccess, error) {
	if _, err := worktreecontract.ParseSetupOperationID(req.SetupOperationId); err != nil {
		req.SetupOperationId = worktreecontract.NewSetupOperationID().String()
	}
	return runCreateMutation(s, func(ctx context.Context) (*worktreepb.CreateSuccess, error) {
		req.SessionId = s.SessionID
		return s.Client.CreateWorktree(ctx, req)
	})
}

func (s Service) Enter(selector string) (*worktreepb.ScheduledAcknowledgement, error) {
	operationID := s.operationID()
	return runMutation(s, func(ctx context.Context) (*worktreepb.ScheduledAcknowledgement, error) {
		return s.Client.EnterWorktree(ctx, &worktreepb.EnterRequest{
			OperationId: operationID.String(),
			SessionId:   s.SessionID,
			Selector:    runtimeinput.NormalizePendingWorkArgument(selector),
			TargetWorkspace: &worktreepb.TransitionWorkspace{
				WorkspaceId:   strings.TrimSpace(s.WorkspaceID),
				WorkspaceRoot: strings.TrimSpace(s.WorkspaceRoot),
			},
		})
	})
}

func (s Service) Leave() (*worktreepb.ScheduledAcknowledgement, error) {
	operationID := s.operationID()
	return runMutation(s, func(ctx context.Context) (*worktreepb.ScheduledAcknowledgement, error) {
		return s.Client.LeaveWorktree(ctx, &worktreepb.LeaveRequest{
			OperationId: operationID.String(),
			SessionId:   s.SessionID,
		})
	})
}

func (s Service) Delete(
	selector string,
	forceFolderRemoval bool,
	cleanupPolicy worktreepb.BranchCleanupMode,
) (*worktreepb.DeleteSuccess, error) {
	return runMutation(s, func(ctx context.Context) (*worktreepb.DeleteSuccess, error) {
		return s.Client.DeleteWorktree(ctx, &worktreepb.DeleteRequest{
			SessionId:           s.SessionID,
			Selector:            strings.TrimSpace(selector),
			ForceFolderRemoval:  forceFolderRemoval,
			BranchCleanupPolicy: cleanupPolicy,
		})
	})
}

func runMutation[T any](s Service, call func(context.Context) (T, error)) (T, error) {
	var zero T
	ctx, cancel, err := s.resolveMutationContext(true)
	if err != nil {
		return zero, err
	}
	defer cancel()
	return retryControlCall(ctx, s.Runtime.RecoverRuntimeConnection, s.Runtime.AppendRecoveryWarning, func() (T, error) {
		return call(ctx)
	})
}

func runCreateMutation[T any](s Service, call func(context.Context) (T, error)) (T, error) {
	var zero T
	if s.Client == nil {
		return zero, ErrClientUnavailable
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	return retryControlCall(ctx, s.Runtime.RecoverRuntimeConnection, s.Runtime.AppendRecoveryWarning, func() (T, error) {
		return call(ctx)
	})
}

func (s Service) resolveMutationContext(mutation bool) (context.Context, context.CancelFunc, error) {
	if s.Client == nil {
		return nil, nil, ErrClientUnavailable
	}
	if mutation && s.Runtime.MutationContext != nil {
		if ctx, cancel := s.Runtime.MutationContext(); ctx != nil && cancel != nil {
			return ctx, cancel, nil
		}
	}
	if s.Runtime.Context != nil {
		if ctx, cancel := s.Runtime.Context(); ctx != nil && cancel != nil {
			return ctx, cancel, nil
		}
	}
	if s.ResolveContext != nil {
		if ctx, cancel := s.ResolveContext(); ctx != nil && cancel != nil {
			return ctx, cancel, nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultMutationTimeout)
	return ctx, cancel, nil
}

func (s Service) resolveContext() (context.Context, context.CancelFunc) {
	if s.Runtime.Context != nil {
		if ctx, cancel := s.Runtime.Context(); ctx != nil && cancel != nil {
			return ctx, cancel
		}
	}
	if s.ResolveContext != nil {
		if ctx, cancel := s.ResolveContext(); ctx != nil && cancel != nil {
			return ctx, cancel
		}
	}
	return context.WithTimeout(context.Background(), defaultResolveTimeout)
}

func DefaultMutationContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), defaultMutationTimeout)
}

func (s Service) operationID() worktreecontract.OperationID {
	if s.NewOperationID != nil {
		if id := s.NewOperationID(); id.Validate() == nil {
			return id
		}
	}
	return worktreecontract.NewOperationID()
}

func retryControlCall[T any](ctx context.Context, recoverRuntimeConnection func(context.Context, error, bool) error, appendRecoveryWarning bool, call func() (T, error)) (T, error) {
	value, err := call()
	if !isRecoverableControlError(err) {
		return value, err
	}
	if recoverRuntimeConnection == nil {
		return value, err
	}
	var zero T
	if recoverErr := recoverRuntimeConnection(ctx, err, appendRecoveryWarning); recoverErr != nil {
		return zero, recoverErr
	}
	return call()
}

func isRecoverableControlError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, serverapi.ErrRuntimeUnavailable)
}
