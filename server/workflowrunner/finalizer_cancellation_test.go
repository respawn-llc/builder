package workflowrunner

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/runtimeids"
)

func TestAgentFinalizerCancellationUsesTypedDurableInterruption(t *testing.T) {
	controller := newCanceledFinalizerController()
	scopeID := runtimeids.NewExecutionScopeID()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- (&Starter{}).finalizeCurrentNodeAgent(ctx, controller, scopeID, nil)
	}()

	controller.awaitFinalizing(t)
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Agent finalizer error = %v, want cancellation", err)
	}
	controller.assertCanceledInterruption(t, scopeID)
	if controller.resultFinalizations != 0 {
		t.Fatalf("Agent result finalizations = %d, want 0", controller.resultFinalizations)
	}
}

func TestScriptFinalizerCancellationUsesTypedDurableInterruption(t *testing.T) {
	controller := newCanceledFinalizerController()
	scopeID := runtimeids.NewExecutionScopeID()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- (&Starter{}).finalizeCurrentNodeScript(
			ctx,
			workflowstore.CurrentNodeStartContext{},
			controller,
			scopeID,
			sessionruntime.ScriptResult{Stdout: []byte(`{"transition":"done"}`)},
			nil,
		)
	}()

	controller.awaitFinalizing(t)
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Script finalizer error = %v, want cancellation", err)
	}
	controller.assertCanceledInterruption(t, scopeID)
	if controller.completions != 0 {
		t.Fatalf("Script completions = %d, want 0", controller.completions)
	}
}

func TestAgentFinalizerContinuesPostTurnAfterCommittedDiagnostic(t *testing.T) {
	for _, test := range []struct {
		name              string
		cancelAfterCommit bool
	}{
		{name: "ordinary diagnostic"},
		{name: "canceled after commit", cancelAfterCommit: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			eventErr := errors.New("completion event delivery failed")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			diagnostic := error(eventErr)
			controller := &committedDiagnosticFinalizerController{diagnostic: diagnostic}
			if test.cancelAfterCommit {
				diagnostic = errors.Join(eventErr, context.Canceled)
				controller.diagnostic = diagnostic
				controller.afterResult = cancel
			}
			err := (&Starter{}).finalizeCurrentNodeAgentExecution(
				ctx,
				controller,
				runtimeids.NewExecutionScopeID(),
				nil,
				&currentNodeAgentPostTurn{
					sessionID: runtimeids.NewSessionID(),
					runtime:   workflowruntime.PostCompletionRuntime{CompactionMode: "none"},
				},
			)
			if !errors.Is(err, eventErr) {
				t.Fatalf("Agent finalizer error = %v, want committed diagnostic %v", err, diagnostic)
			}
			if controller.postTurnFinalizations != 1 {
				t.Fatalf("post-turn finalizations = %d, want 1", controller.postTurnFinalizations)
			}
			if controller.failurePublications != 0 {
				t.Fatalf("durable failure publications = %d, want 0", controller.failurePublications)
			}
		})
	}
}

type committedDiagnosticFinalizerController struct {
	canceledFinalizerController
	diagnostic            error
	afterResult           func()
	postTurnFinalizations int
	failurePublications   int
}

func (*committedDiagnosticFinalizerController) PublishCurrentNodeExactFinalizing(
	context.Context,
	runtimeids.ExecutionScopeID,
) error {
	return nil
}

func (c *committedDiagnosticFinalizerController) FinalizeCurrentNodeResult(
	context.Context,
	runtimeids.ExecutionScopeID,
	error,
) error {
	if c.afterResult != nil {
		c.afterResult()
	}
	return workflowruntime.NewCommittedCompletionDiagnostic(c.diagnostic)
}

func (c *committedDiagnosticFinalizerController) FinalizeCurrentNodePostTurn(
	ctx context.Context,
	_ runtimeids.ExecutionScopeID,
	_ runtimeids.SessionID,
	_ workflowruntime.PostCompletionRuntime,
) error {
	c.postTurnFinalizations++
	return context.Cause(ctx)
}

func (c *committedDiagnosticFinalizerController) FailCurrentNodeScope(
	context.Context,
	runtimeids.ExecutionScopeID,
	workflow.CurrentNodeInterruptionReason,
	error,
) error {
	c.failurePublications++
	return nil
}

type canceledFinalizerController struct {
	finalizingEntered chan struct{}
	finalizingOnce    sync.Once

	mu                  sync.Mutex
	completions         int
	resultFinalizations int
	failures            []canceledFinalizerFailure
}

type canceledFinalizerFailure struct {
	scopeID runtimeids.ExecutionScopeID
	reason  workflow.CurrentNodeInterruptionReason
	cause   error
	ctxErr  error
}

func newCanceledFinalizerController() *canceledFinalizerController {
	return &canceledFinalizerController{finalizingEntered: make(chan struct{})}
}

func (c *canceledFinalizerController) PublishCurrentNodeExactFinalizing(
	ctx context.Context,
	_ runtimeids.ExecutionScopeID,
) error {
	c.finalizingOnce.Do(func() { close(c.finalizingEntered) })
	<-ctx.Done()
	return context.Cause(ctx)
}

func (c *canceledFinalizerController) FailCurrentNodeScope(
	ctx context.Context,
	scopeID runtimeids.ExecutionScopeID,
	reason workflow.CurrentNodeInterruptionReason,
	cause error,
) error {
	c.mu.Lock()
	c.failures = append(c.failures, canceledFinalizerFailure{
		scopeID: scopeID,
		reason:  reason,
		cause:   cause,
		ctxErr:  context.Cause(ctx),
	})
	c.mu.Unlock()
	return nil
}

func (c *canceledFinalizerController) FinalizeCurrentNodeResult(
	context.Context,
	runtimeids.ExecutionScopeID,
	error,
) error {
	c.mu.Lock()
	c.resultFinalizations++
	c.mu.Unlock()
	return nil
}

func (c *canceledFinalizerController) CompleteCurrentNode(
	context.Context,
	workflowruntime.CompletionRequest,
) (workflowruntime.CompletionResult, error) {
	c.mu.Lock()
	c.completions++
	c.mu.Unlock()
	return workflowruntime.CompletionResult{}, nil
}

func (*canceledFinalizerController) RecordProtocolViolation(
	context.Context,
	workflowruntime.ViolationRequest,
) (workflowruntime.ViolationResult, error) {
	return workflowruntime.ViolationResult{}, nil
}

func (*canceledFinalizerController) ResetProtocolViolationBudget(
	context.Context,
	workflowruntime.ViolationResetRequest,
) error {
	return nil
}

func (*canceledFinalizerController) ObserveCurrentNodeCompletion(
	context.Context,
	workflowruntime.CompletionObservationRequest,
) (workflowruntime.CompletionObservationResult, error) {
	return workflowruntime.CompletionObservationResult{}, nil
}

func (c *canceledFinalizerController) awaitFinalizing(t *testing.T) {
	t.Helper()
	select {
	case <-c.finalizingEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("production finalizer did not enter finalizing publication")
	}
}

func (c *canceledFinalizerController) assertCanceledInterruption(
	t *testing.T,
	scopeID runtimeids.ExecutionScopeID,
) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.failures) != 1 {
		t.Fatalf("typed interruption calls = %d, want 1", len(c.failures))
	}
	failure := c.failures[0]
	if failure.scopeID != scopeID {
		t.Fatalf("interrupted scope = %s, want %s", failure.scopeID, scopeID)
	}
	if failure.reason != workflow.CurrentNodeInterruptionReasonRuntimeCanceled {
		t.Fatalf("interruption reason = %q, want %q", failure.reason, workflow.CurrentNodeInterruptionReasonRuntimeCanceled)
	}
	if failure.ctxErr != nil {
		t.Fatalf("durable interruption context error = %v, want nil", failure.ctxErr)
	}
	if !errors.Is(failure.cause, context.Canceled) {
		t.Fatalf("interruption cause = %v, want cancellation", failure.cause)
	}
}
