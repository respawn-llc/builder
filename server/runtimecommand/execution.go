package runtimecommand

import (
	"context"
	"errors"
	"strings"
	"sync"

	"core/server/runtime"
	"core/server/session"
	"core/server/sessionruntime"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type SessionAgentOperationOwnerOrderingNotifier struct {
	state *sessionAgentOperationOwnerOrderingState
}

type SessionAgentOperationOwnerOrderingNotification struct {
	state *sessionAgentOperationOwnerOrderingState
}

type sessionAgentOperationOwnerOrderingState struct {
	once sync.Once
	done chan struct{}
}

func NewSessionAgentOperationOwnerOrdering() (
	SessionAgentOperationOwnerOrderingNotifier,
	SessionAgentOperationOwnerOrderingNotification,
) {
	state := &sessionAgentOperationOwnerOrderingState{done: make(chan struct{})}
	return SessionAgentOperationOwnerOrderingNotifier{state: state},
		SessionAgentOperationOwnerOrderingNotification{state: state}
}

func (n SessionAgentOperationOwnerOrderingNotifier) Complete() bool {
	if n.state == nil {
		return false
	}
	completed := false
	n.state.once.Do(func() {
		completed = true
		close(n.state.done)
	})
	return completed
}

func (n SessionAgentOperationOwnerOrderingNotification) Done() <-chan struct{} {
	if n.state == nil {
		return nil
	}
	return n.state.done
}

type ExecutionAdapter struct {
	authority *sessionruntime.Authority
	router    WorkflowSessionExecutionRouter
}

type WorkflowSessionExecutionRouter interface {
	RouteSessionAgentOperation(
		context.Context,
		runtimeids.SessionID,
		SessionAgentOperationDriver,
	) (bool, SessionAgentOperationOutcome, error)
}

type SessionAgentOperationStart func() (SessionAgentOperationOutcome, error)

type SessionAgentOperationAdmitter func(SessionAgentOperationStart) error

type WorkflowSessionAdmittedExecutionRouter interface {
	RouteAdmittedSessionAgentOperation(
		context.Context,
		runtimeids.SessionID,
		SessionAgentOperationDriver,
		SessionAgentOperationAdmitter,
	) (bool, error)
}

func NewExecutionAdapter(
	authority *sessionruntime.Authority,
	router WorkflowSessionExecutionRouter,
) *ExecutionAdapter {
	return &ExecutionAdapter{authority: authority, router: router}
}

func (a *ExecutionAdapter) RunAgentOperation(
	ctx context.Context,
	sessionID string,
	driver SessionAgentOperationDriver,
) (SessionAgentOperationOutcome, error) {
	if driver == nil {
		return nil, errors.New("session Agent operation driver is required")
	}
	id, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID))
	if err != nil {
		return nil, err
	}
	handled, outcome, routeErr := a.routeWorkflowAgentOperation(ctx, id, driver)
	if handled || routeErr != nil {
		return outcome, routeErr
	}
	return a.runOrdinaryAgentOperation(ctx, id, driver)
}

func (a *ExecutionAdapter) routeWorkflowAgentOperation(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	driver SessionAgentOperationDriver,
) (bool, SessionAgentOperationOutcome, error) {
	if a == nil || a.router == nil {
		return false, nil, nil
	}
	return a.router.RouteSessionAgentOperation(ctx, sessionID, driver)
}

func (a *ExecutionAdapter) runOrdinaryAgentOperation(
	ctx context.Context,
	id runtimeids.SessionID,
	driver SessionAgentOperationDriver,
) (SessionAgentOperationOutcome, error) {
	if a == nil || a.authority == nil {
		return nil, errors.New("session runtime authority is required")
	}
	descriptor, err := session.NewOpenSessionDescriptor(id)
	if err != nil {
		return nil, err
	}
	ordering, _ := NewSessionAgentOperationOwnerOrdering()
	var outcome SessionAgentOperationOutcome
	err = a.authority.RunCurrentAgentExecution(ctx, descriptor, func(runCtx context.Context, engine *runtime.Engine) error {
		var runErr error
		outcome, runErr = driver.StartOwner(runCtx, engine, ordering)
		return runErr
	})
	if errors.Is(err, sessionruntime.ErrSessionRunActive) {
		err = a.authority.WithLiveExecutionRuntime(ctx, id, func(runCtx context.Context, engine *runtime.Engine) error {
			var runErr error
			outcome, runErr = driver.JoinLive(runCtx, engine)
			return runErr
		})
	}
	if err != nil {
		if errors.Is(err, sessionruntime.ErrSessionStartsBlocked) {
			err = errors.Join(serverapi.ErrSessionWorktreeDeleting, err)
		}
		if errors.Is(err, sessionruntime.ErrSessionRunActive) {
			err = errors.Join(serverapi.ErrSessionRunStarting, err)
		}
	}
	return outcome, err
}

func (a *ExecutionAdapter) routeAdmittedWorkflowAgentOperation(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	driver SessionAgentOperationDriver,
	admit SessionAgentOperationAdmitter,
) (bool, error) {
	if a == nil || a.router == nil {
		return false, nil
	}
	router, ok := a.router.(WorkflowSessionAdmittedExecutionRouter)
	if !ok {
		return false, errors.New("workflow Session execution router does not support admitted work")
	}
	return router.RouteAdmittedSessionAgentOperation(ctx, sessionID, driver, admit)
}

func (a *ExecutionAdapter) JoinLiveAgentOperation(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	driver SessionAgentOperationDriver,
) (SessionAgentOperationOutcome, error) {
	if a == nil || a.authority == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if driver == nil {
		return nil, errors.New("session Agent operation driver is required")
	}
	if a.router != nil {
		handled, outcome, routeErr := a.router.RouteSessionAgentOperation(ctx, sessionID, driver)
		if handled || routeErr != nil {
			return outcome, routeErr
		}
	}
	var outcome SessionAgentOperationOutcome
	err := a.authority.WithLiveExecutionRuntime(ctx, sessionID, func(runCtx context.Context, engine *runtime.Engine) error {
		var runErr error
		outcome, runErr = driver.JoinLive(runCtx, engine)
		return runErr
	})
	return outcome, err
}
