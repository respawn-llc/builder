package worktreeui

import (
	"context"
	"errors"
	"strings"
	"time"

	"core/shared/client"
	"core/shared/serverapi"

	"github.com/google/uuid"
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
	Client             client.WorktreeClient
	SessionID          string
	Runtime            RuntimeControl
	ResolveContext     func() (context.Context, context.CancelFunc)
	NewClientRequestID func() string
}

func (s Service) List(includeDirtyCount bool) (serverapi.WorktreeListResponse, error) {
	ctx, cancel, err := s.resolveMutationContext(false)
	if err != nil {
		return serverapi.WorktreeListResponse{}, err
	}
	defer cancel()
	return s.Client.ListWorktrees(ctx, serverapi.WorktreeListRequest{
		SessionID:         s.SessionID,
		IncludeDirtyCount: includeDirtyCount,
	})
}

func (s Service) ResolveCreateTarget(target string) (serverapi.WorktreeCreateTargetResolveResponse, error) {
	if s.Client == nil {
		return serverapi.WorktreeCreateTargetResolveResponse{}, ErrClientUnavailable
	}
	ctx, cancel := s.resolveContext()
	defer cancel()
	return s.Client.ResolveWorktreeCreateTarget(ctx, serverapi.WorktreeCreateTargetResolveRequest{
		SessionID: strings.TrimSpace(s.SessionID),
		Target:    target,
	})
}

func (s Service) Create(req serverapi.WorktreeCreateRequest) (serverapi.WorktreeCreateResponse, error) {
	return runMutation(s, func(ctx context.Context) (serverapi.WorktreeCreateResponse, error) {
		req.ClientRequestID = s.clientRequestID()
		req.SessionID = s.SessionID
		return s.Client.CreateWorktree(ctx, req)
	})
}

func (s Service) Switch(worktreeID string) (serverapi.WorktreeSwitchResponse, error) {
	return runMutation(s, func(ctx context.Context) (serverapi.WorktreeSwitchResponse, error) {
		return s.Client.SwitchWorktree(ctx, serverapi.WorktreeSwitchRequest{
			ClientRequestID: s.clientRequestID(),
			SessionID:       s.SessionID,
			WorktreeID:      strings.TrimSpace(worktreeID),
		})
	})
}

func (s Service) Delete(worktreeID string, deleteBranch bool) (serverapi.WorktreeDeleteResponse, error) {
	return runMutation(s, func(ctx context.Context) (serverapi.WorktreeDeleteResponse, error) {
		return s.Client.DeleteWorktree(ctx, serverapi.WorktreeDeleteRequest{
			ClientRequestID: s.clientRequestID(),
			SessionID:       s.SessionID,
			WorktreeID:      strings.TrimSpace(worktreeID),
			DeleteBranch:    deleteBranch,
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

func (s Service) clientRequestID() string {
	if s.NewClientRequestID != nil {
		if id := strings.TrimSpace(s.NewClientRequestID()); id != "" {
			return id
		}
	}
	return uuid.NewString()
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
