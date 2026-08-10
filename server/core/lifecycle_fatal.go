package core

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"core/server/workflowexecution"
	"core/shared/apicontract"
)

type lifecycleFatalState struct {
	mu       sync.Mutex
	cause    error
	shutdown chan struct{}
	context  context.Context
	cancel   context.CancelCauseFunc
}

func newLifecycleFatalState(parent context.Context) *lifecycleFatalState {
	if parent == nil {
		parent = context.Background()
	}
	lifecycleContext, cancel := context.WithCancelCause(parent)
	return &lifecycleFatalState{
		shutdown: make(chan struct{}),
		context:  lifecycleContext,
		cancel:   cancel,
	}
}

func (s *lifecycleFatalState) Context() context.Context {
	if s == nil || s.context == nil {
		return context.Background()
	}
	return s.context
}

func (s *lifecycleFatalState) ReportFatal(diagnostic workflowexecution.LifecycleFatalDiagnostic) workflowexecution.LifecycleFatalReportResult {
	if s == nil {
		panic("Core lifecycle fatal state is required")
	}
	if diagnostic.PersistenceFailure == nil && diagnostic.CleanupFailure == nil {
		panic("workflow lifecycle fatal persistence or cleanup failure is required")
	}
	s.mu.Lock()
	accepted := s.cause == nil
	diagnosticErr := fmt.Errorf("%s shutdown_accepted=%t", diagnostic.Error(), accepted)
	s.cause = errors.Join(s.cause, diagnosticErr, diagnostic.PersistenceFailure, diagnostic.CleanupFailure)
	if accepted {
		s.cancel(s.cause)
		close(s.shutdown)
	}
	s.mu.Unlock()
	return workflowexecution.LifecycleFatalReportResult{ShutdownAccepted: accepted}
}

func (s *lifecycleFatalState) Available() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cause == nil {
		return nil
	}
	return workflowexecution.LifecycleUnavailableError{Cause: s.cause}
}

func (s *lifecycleFatalState) Cause() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cause
}

func (s *lifecycleFatalState) Shutdown() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.shutdown
}

func (s *Core) RouteDependencyAvailable(apicontract.Dependency) error {
	if s == nil {
		return errors.New("Core is required")
	}
	return s.fatalLifecycle.Available()
}

func (s *Core) LifecycleFatalShutdown() <-chan struct{} {
	if s == nil || s.fatalLifecycle == nil {
		return nil
	}
	return s.fatalLifecycle.Shutdown()
}

func (s *Core) LifecycleFatalCause() error {
	if s == nil || s.fatalLifecycle == nil {
		return nil
	}
	return s.fatalLifecycle.Cause()
}

var _ workflowexecution.LifecycleFatalReporter = (*lifecycleFatalState)(nil)
