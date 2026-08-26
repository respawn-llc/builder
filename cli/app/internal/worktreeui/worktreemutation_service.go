package worktreeui

import (
	"context"
	"errors"
	"strings"
	"time"

	"core/shared/apicontract"
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
	Runtime        RuntimeControl
	ResolveContext func() (context.Context, context.CancelFunc)
	NewOperationID func() worktreecontract.OperationID
}

func (s Service) List() (worktreecontract.ListResponse, error) {
	ctx, cancel, err := s.resolveMutationContext(false)
	if err != nil {
		return worktreecontract.ListResponse{}, err
	}
	defer cancel()
	return s.Client.ListWorktrees(ctx, worktreecontract.ListRequest{
		SessionID: s.SessionID,
	})
}

func (s Service) ResolveCreateTarget(target string) (worktreecontract.CreateTargetResolveResponse, error) {
	if s.Client == nil {
		return worktreecontract.CreateTargetResolveResponse{}, ErrClientUnavailable
	}
	ctx, cancel := s.resolveContext()
	defer cancel()
	return s.Client.ResolveWorktreeCreateTarget(ctx, worktreecontract.CreateTargetResolveRequest{
		SessionID: strings.TrimSpace(s.SessionID),
		Target:    target,
	})
}

func (s Service) ResolveSelector(selector string) (worktreecontract.SelectorResolveResponse, error) {
	if s.Client == nil {
		return worktreecontract.SelectorResolveResponse{}, ErrClientUnavailable
	}
	ctx, cancel := s.resolveContext()
	defer cancel()
	return s.Client.ResolveWorktreeSelector(ctx, worktreecontract.SelectorResolveRequest{
		SessionID: strings.TrimSpace(s.SessionID),
		Selector:  strings.TrimSpace(selector),
	})
}

func (s Service) Create(req worktreecontract.CreateRequest) (worktreecontract.CreateResponse, error) {
	if err := req.SetupOperationID.Validate(); err != nil {
		req.SetupOperationID = worktreecontract.NewSetupOperationID()
	}
	return runCreateMutation(s, func(ctx context.Context) (worktreecontract.CreateResponse, error) {
		req.SessionID = s.SessionID
		return s.Client.CreateWorktree(ctx, req)
	})
}

func (s Service) Enter(selector string) (worktreecontract.ScheduledAcknowledgement, error) {
	operationID := s.operationID()
	return runMutation(s, func(ctx context.Context) (worktreecontract.ScheduledAcknowledgement, error) {
		return s.Client.EnterWorktree(ctx, worktreecontract.EnterRequest{
			TransitionHeader: worktreecontract.TransitionHeader{
				OperationID: operationID,
				SessionID:   s.SessionID,
			},
			Selector: strings.TrimSpace(selector),
		})
	})
}

func (s Service) Delete(
	selector string,
	forceFolderRemoval bool,
	cleanupPolicy worktreecontract.BranchCleanupMode,
) (worktreecontract.DeleteResult, error) {
	operationID := s.operationID()
	return runMutation(s, func(ctx context.Context) (worktreecontract.DeleteResult, error) {
		return s.Client.DeleteWorktree(ctx, worktreecontract.DeleteRequest{
			TransitionHeader: worktreecontract.TransitionHeader{
				OperationID: operationID,
				SessionID:   s.SessionID,
			},
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
