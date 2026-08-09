package sessionruntime

import (
	"context"
	"errors"
	"fmt"

	"core/server/runtime"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

var (
	ErrWorktreeBoundaryClaimed   = errors.New("worktree boundary is already claimed")
	ErrWorktreeBoundaryNotActive = errors.New("worktree boundary is not active")
)

type worktreeBoundaryPhase uint8

const (
	worktreeBoundaryPending worktreeBoundaryPhase = iota + 1
	worktreeBoundaryActiveIdle
	worktreeBoundaryActiveStepWait
	worktreeBoundaryReleased
	worktreeBoundaryUnavailable
)

type worktreeBoundaryRecord struct {
	operationID serverapi.WorktreeOperationID
	granted     chan struct{}
	settled     chan struct{}

	phase        worktreeBoundaryPhase
	err          error
	reducerGrant runtime.AgentStepReducerGrant
}

type reducerBoundaryPhase uint8

const (
	reducerBoundaryActive reducerBoundaryPhase = iota + 1
	reducerBoundaryReleased
	reducerBoundaryUnavailable
)

type reducerBoundaryRecord struct {
	phase reducerBoundaryPhase
}

type WorktreeBoundaryClaim struct {
	authority *Authority
	resource  runtimeids.SessionResourceRef
	record    *worktreeBoundaryRecord
}

type worktreeBoundaryWait struct {
	record *worktreeBoundaryRecord
}

type reducerBoundaryGrant struct {
	authority *Authority
	resource  runtimeids.SessionResourceRef
	record    *reducerBoundaryRecord
}

type idleReducerBoundaryGrant struct {
	authority *Authority
	resource  runtimeids.SessionResourceRef
	record    *reducerBoundaryRecord
}

func (a *Authority) ClaimWorktreeBoundary(
	resourceRef runtimeids.SessionResourceRef,
	operationID serverapi.WorktreeOperationID,
) (*WorktreeBoundaryClaim, error) {
	if a == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if err := resourceRef.Validate(); err != nil {
		return nil, err
	}
	if err := operationID.Validate(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	resource := a.resources[resourceRef.SessionID()]
	a.mu.Unlock()
	if resource == nil {
		return nil, runtimeUnavailableError(resourceRef)
	}
	resource.mu.Lock()
	defer resource.mu.Unlock()
	if resource.ref != resourceRef || resource.rejectsNewUseLocked() {
		return nil, runtimeUnavailableError(resourceRef)
	}
	if resource.worktreeBoundary != nil {
		return nil, errors.Join(
			ErrWorktreeBoundaryClaimed,
			fmt.Errorf(
				"resource %s generation %d already belongs to Worktree operation %s",
				resourceRef.SessionID(),
				resourceRef.Generation(),
				resource.worktreeBoundary.operationID,
			),
		)
	}
	record := &worktreeBoundaryRecord{
		operationID: operationID,
		granted:     make(chan struct{}),
		settled:     make(chan struct{}),
		phase:       worktreeBoundaryPending,
	}
	resource.worktreeBoundary = record
	if resource.current == nil && resource.reducerBoundary == nil {
		record.phase = worktreeBoundaryActiveIdle
		close(record.granted)
	}
	resource.signalLocked()
	return &WorktreeBoundaryClaim{
		authority: a,
		resource:  resourceRef,
		record:    record,
	}, nil
}

func (a *Authority) ClaimCurrentWorktreeBoundary(
	sessionID string,
	operationID serverapi.WorktreeOperationID,
) (*WorktreeBoundaryClaim, error) {
	id, err := runtimeids.ParseSessionID(sessionID)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	resource := a.resources[id]
	a.mu.Unlock()
	if resource == nil {
		return nil, nil
	}
	return a.ClaimWorktreeBoundary(resource.ref, operationID)
}

func (r *agentResource) AgentStepBegan(
	_ context.Context,
	origin serverapi.RuntimeStepOrigin,
) (runtimeids.ExecutionScopeID, error) {
	if err := origin.Validate(); err != nil {
		return runtimeids.ExecutionScopeID{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rejectsNewStepLocked() || r.current == nil {
		return runtimeids.ExecutionScopeID{}, runtimeUnavailableError(r.ref)
	}
	if r.reducerBoundary != nil {
		panic(fmt.Sprintf(
			"resource %s generation %d began Agent Step %s while reducer owns the Boundary",
			r.ref.SessionID(),
			r.ref.Generation(),
			origin.StepID,
		))
	}
	r.current.exactMu.Lock()
	defer r.current.exactMu.Unlock()
	err := setCompletionOriginLocked(r.current, origin)
	return r.current.scope.ID(), err
}

func (r *agentResource) AgentStepScopeLive(
	_ context.Context,
	scopeID runtimeids.ExecutionScopeID,
) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rejectsNewStepLocked() || r.current == nil || r.current.scope.ID() != scopeID {
		return false
	}
	r.current.exactMu.Lock()
	defer r.current.exactMu.Unlock()
	return r.current.phase == executionPhaseRunning && r.current.ctx.Err() == nil
}

func (r *agentResource) CurrentAgentExecutionScope(
	_ context.Context,
) (runtimeids.ExecutionScopeID, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current == nil || r.current.phase != executionPhaseRunning || r.current.ctx.Err() != nil {
		return runtimeids.ExecutionScopeID{}, false
	}
	return r.current.scope.ID(), true
}

func (r *agentResource) TryAcquireIdleBoundary(
	ctx context.Context,
) (runtime.IdleBoundaryReducerGrant, bool, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rejectsNewUseLocked() {
		return nil, false, runtimeUnavailableError(r.ref)
	}
	if r.current != nil || r.reducerBoundary != nil {
		return nil, false, nil
	}
	if worktree := r.worktreeBoundary; worktree != nil {
		switch worktree.phase {
		case worktreeBoundaryPending:
			worktree.phase = worktreeBoundaryActiveIdle
			close(worktree.granted)
			r.signalLocked()
		case worktreeBoundaryActiveIdle:
		case worktreeBoundaryActiveStepWait:
			panic(fmt.Sprintf(
				"resource %s generation %d retained an active Step Worktree boundary while idle",
				r.ref.SessionID(),
				r.ref.Generation(),
			))
		default:
			panic(fmt.Sprintf(
				"resource %s generation %d retained settled Worktree boundary phase %d",
				r.ref.SessionID(),
				r.ref.Generation(),
				worktree.phase,
			))
		}
		return nil, false, nil
	}
	record := r.newReducerBoundaryRecordLocked()
	return &idleReducerBoundaryGrant{
		authority: r.authority,
		resource:  r.ref,
		record:    record,
	}, true, nil
}

func (r *agentResource) AgentStepBoundary(
	_ context.Context,
	origin serverapi.RuntimeStepOrigin,
) (runtime.AgentStepBoundaryTransfer, error) {
	if err := origin.Validate(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rejectsNewStepLocked() || r.current == nil {
		return nil, runtimeUnavailableError(r.ref)
	}
	r.current.exactMu.Lock()
	defer r.current.exactMu.Unlock()
	if err := clearCompletionOriginLocked(r.current, origin); err != nil {
		return nil, err
	}
	if r.reducerBoundary != nil {
		panic(fmt.Sprintf(
			"resource %s generation %d reached Agent Step Boundary while reducer ownership remains active",
			r.ref.SessionID(),
			r.ref.Generation(),
		))
	}
	if worktree := r.worktreeBoundary; worktree != nil {
		switch worktree.phase {
		case worktreeBoundaryPending:
			worktree.phase = worktreeBoundaryActiveStepWait
			close(worktree.granted)
			return runtime.AgentStepWorktreeBoundary{
				Wait: worktreeBoundaryWait{record: worktree},
			}, nil
		case worktreeBoundaryActiveIdle, worktreeBoundaryActiveStepWait:
			return nil, errors.Join(
				ErrWorktreeBoundaryClaimed,
				fmt.Errorf("Worktree operation %s already owns the boundary", worktree.operationID),
			)
		default:
			panic(fmt.Sprintf(
				"resource %s generation %d retained settled Worktree boundary phase %d",
				r.ref.SessionID(),
				r.ref.Generation(),
				worktree.phase,
			))
		}
	}
	grant := r.newReducerBoundaryGrantLocked()
	return runtime.AgentStepReducerBoundary{Grant: grant}, nil
}

func setCompletionOriginLocked(
	execution *execution,
	origin serverapi.RuntimeStepOrigin,
) error {
	if execution.phase != executionPhaseRunning {
		return ErrExecutionNoLongerLive
	}
	if execution.completionOrigin != nil {
		panic(fmt.Sprintf(
			"execution %s already exposes completion origin %+v while beginning %+v",
			execution.scope.ID(),
			*execution.completionOrigin,
			origin,
		))
	}
	copyOrigin := origin
	execution.completionOrigin = &copyOrigin
	return nil
}

func clearCompletionOriginLocked(
	execution *execution,
	origin serverapi.RuntimeStepOrigin,
) error {
	if execution.phase != executionPhaseRunning ||
		execution.completionOrigin == nil ||
		*execution.completionOrigin != origin {
		return ErrExecutionNoLongerLive
	}
	execution.completionOrigin = nil
	return nil
}

func (a *Authority) ApplyWorkflowCompletion(
	scopeID runtimeids.ExecutionScopeID,
	origin serverapi.RuntimeStepOrigin,
	operation func() (bool, error),
) (bool, error) {
	if a == nil {
		return false, errors.New("session runtime authority is required")
	}
	if scopeID.IsZero() {
		return false, errors.New("execution scope id is required")
	}
	if err := origin.Validate(); err != nil {
		return false, err
	}
	if operation == nil {
		return false, errors.New("Workflow completion operation is required")
	}
	a.mu.Lock()
	execution := a.byScope[scopeID]
	a.mu.Unlock()
	if execution == nil {
		return false, ErrExecutionNoLongerLive
	}
	execution.exactMu.Lock()
	defer execution.exactMu.Unlock()
	a.mu.Lock()
	live := a.byScope[scopeID] == execution
	a.mu.Unlock()
	if !live ||
		execution.phase != executionPhaseRunning ||
		execution.ctx.Err() != nil ||
		execution.completionOrigin == nil ||
		*execution.completionOrigin != origin {
		return false, ErrExecutionNoLongerLive
	}
	committed, err := operation()
	if !committed {
		return false, err
	}
	execution.phase = executionPhaseFinalizing
	execution.completionOrigin = nil
	a.mu.Lock()
	execution.retireWorkflowLocked()
	a.mu.Unlock()
	execution.cancel()
	return true, err
}

func (r *agentResource) newReducerBoundaryGrantLocked() runtime.AgentStepReducerGrant {
	record := r.newReducerBoundaryRecordLocked()
	return &reducerBoundaryGrant{
		authority: r.authority,
		resource:  r.ref,
		record:    record,
	}
}

func (r *agentResource) newReducerBoundaryRecordLocked() *reducerBoundaryRecord {
	if r.reducerBoundary != nil {
		panic(fmt.Sprintf(
			"resource %s generation %d attempted to duplicate reducer Boundary ownership",
			r.ref.SessionID(),
			r.ref.Generation(),
		))
	}
	record := &reducerBoundaryRecord{phase: reducerBoundaryActive}
	r.reducerBoundary = record
	return record
}

func (g *reducerBoundaryGrant) RegisterNext(
	_ context.Context,
	origin serverapi.RuntimeStepOrigin,
) (runtimeids.ExecutionScopeID, error) {
	if g == nil || g.authority == nil || g.record == nil {
		return runtimeids.ExecutionScopeID{}, serverapi.ErrRuntimeUnavailable
	}
	if err := origin.Validate(); err != nil {
		return runtimeids.ExecutionScopeID{}, err
	}
	resource, err := g.authority.resourceForBoundary(g.resource)
	if err != nil {
		return runtimeids.ExecutionScopeID{}, err
	}
	resource.mu.Lock()
	defer resource.mu.Unlock()
	if resource.reducerBoundary != g.record ||
		g.record.phase != reducerBoundaryActive ||
		resource.current == nil {
		return runtimeids.ExecutionScopeID{}, runtimeUnavailableError(g.resource)
	}
	resource.current.exactMu.Lock()
	defer resource.current.exactMu.Unlock()
	if err := setCompletionOriginLocked(resource.current, origin); err != nil {
		return runtimeids.ExecutionScopeID{}, err
	}
	scopeID := resource.current.scope.ID()
	if _, err := resource.releaseReducerBoundaryLocked(g.record); err != nil {
		return runtimeids.ExecutionScopeID{}, err
	}
	return scopeID, nil
}

func (g *reducerBoundaryGrant) Release() error {
	if g == nil || g.authority == nil || g.record == nil {
		return serverapi.ErrRuntimeUnavailable
	}
	resource, err := g.authority.resourceForBoundary(g.resource)
	if err != nil {
		return err
	}
	resource.mu.Lock()
	defer resource.mu.Unlock()
	_, err = resource.releaseReducerBoundaryLocked(g.record)
	return err
}

func (g *idleReducerBoundaryGrant) Release() (bool, error) {
	if g == nil || g.authority == nil || g.record == nil {
		return false, serverapi.ErrRuntimeUnavailable
	}
	resource, err := g.authority.resourceForBoundary(g.resource)
	if err != nil {
		return false, err
	}
	resource.mu.Lock()
	if g.record.phase == reducerBoundaryReleased &&
		resource.reducerBoundary != g.record {
		resource.mu.Unlock()
		return false, nil
	}
	retry, releaseErr := resource.releaseReducerBoundaryLocked(g.record)
	retiring := resource.state == AgentResourceReady &&
		resource.ownerlessDisposition == agentResourceRetireWhenIdle &&
		len(resource.owners) == 0 &&
		!resource.hasInFlightUseLocked()
	resource.mu.Unlock()
	if releaseErr != nil {
		return retry, releaseErr
	}
	if !retiring {
		return retry, nil
	}
	if !g.authority.launchLifecycleTask(func(context.Context) {
		if closeErr := g.authority.closeRetiringResource(
			context.Background(),
			resource,
		); closeErr != nil && resource.logger != nil {
			resource.logger.Logf("runtime.retirement.error error=%q", closeErr.Error())
		}
	}) {
		return retry, ErrAuthorityClosed
	}
	return retry, nil
}

func (r *agentResource) releaseReducerBoundaryLocked(
	record *reducerBoundaryRecord,
) (bool, error) {
	if r.reducerBoundary != record ||
		record.phase != reducerBoundaryActive {
		return false, runtimeUnavailableError(r.ref)
	}
	record.phase = reducerBoundaryReleased
	r.reducerBoundary = nil
	retry := r.current == nil &&
		r.worktreeBoundary != nil &&
		r.worktreeBoundary.phase == worktreeBoundaryPending
	r.signalLocked()
	return retry, nil
}

func (w worktreeBoundaryWait) Await(
	ctx context.Context,
) (runtime.AgentStepReducerGrant, error) {
	if w.record == nil {
		return nil, serverapi.ErrRuntimeUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-w.record.settled:
		switch w.record.phase {
		case worktreeBoundaryReleased:
			if w.record.reducerGrant == nil {
				panic(fmt.Sprintf(
					"released Worktree operation %s has no reducer grant",
					w.record.operationID,
				))
			}
			return w.record.reducerGrant, nil
		case worktreeBoundaryUnavailable:
			if w.record.err == nil {
				return nil, serverapi.ErrRuntimeUnavailable
			}
			return nil, w.record.err
		default:
			panic(fmt.Sprintf(
				"Worktree operation %s settled from phase %d",
				w.record.operationID,
				w.record.phase,
			))
		}
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

func (c *WorktreeBoundaryClaim) AwaitGrant(ctx context.Context) error {
	if c == nil || c.record == nil {
		return serverapi.ErrRuntimeUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-c.record.granted:
		return nil
	case <-c.record.settled:
		if c.record.phase == worktreeBoundaryReleased {
			return nil
		}
		if c.record.err == nil {
			return serverapi.ErrRuntimeUnavailable
		}
		return c.record.err
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (c *WorktreeBoundaryClaim) Release() (runtime.AgentStepReducerGrant, error) {
	if c == nil || c.authority == nil || c.record == nil {
		return nil, serverapi.ErrRuntimeUnavailable
	}
	resource, err := c.authority.resourceForBoundary(c.resource)
	if err != nil {
		return nil, err
	}
	resource.mu.Lock()
	defer resource.mu.Unlock()
	if resource.worktreeBoundary != c.record ||
		(c.record.phase != worktreeBoundaryActiveIdle &&
			c.record.phase != worktreeBoundaryActiveStepWait) {
		return nil, errors.Join(
			ErrWorktreeBoundaryNotActive,
			runtimeUnavailableError(c.resource),
		)
	}
	activePhase := c.record.phase
	grant := resource.newReducerBoundaryGrantLocked()
	c.record.phase = worktreeBoundaryReleased
	if activePhase == worktreeBoundaryActiveStepWait {
		c.record.reducerGrant = grant
	}
	resource.worktreeBoundary = nil
	close(c.record.settled)
	resource.signalLocked()
	if c.record.reducerGrant != nil {
		return nil, nil
	}
	return grant, nil
}

func (c *WorktreeBoundaryClaim) Resource() runtimeids.SessionResourceRef {
	if c == nil || c.authority == nil || c.record == nil {
		panic("Worktree boundary claim is uninitialized")
	}
	return c.resource
}

func (c *WorktreeBoundaryClaim) Cancel(cause error) error {
	if c == nil || c.authority == nil || c.record == nil {
		return serverapi.ErrRuntimeUnavailable
	}
	if cause == nil {
		cause = serverapi.ErrRuntimeUnavailable
	}
	c.authority.mu.Lock()
	resource := c.authority.resources[c.resource.SessionID()]
	c.authority.mu.Unlock()
	if resource == nil {
		return runtimeUnavailableError(c.resource)
	}
	resource.mu.Lock()
	defer resource.mu.Unlock()
	if resource.ref != c.resource || resource.worktreeBoundary != c.record {
		return runtimeUnavailableError(c.resource)
	}
	switch c.record.phase {
	case worktreeBoundaryPending, worktreeBoundaryActiveIdle, worktreeBoundaryActiveStepWait:
		c.record.phase = worktreeBoundaryUnavailable
		c.record.err = cause
		resource.worktreeBoundary = nil
		close(c.record.settled)
		resource.signalLocked()
		return nil
	default:
		return runtimeUnavailableError(c.resource)
	}
}

func (a *Authority) resourceForBoundary(
	ref runtimeids.SessionResourceRef,
) (*agentResource, error) {
	if a == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	resource := a.resources[ref.SessionID()]
	a.mu.Unlock()
	if resource == nil {
		return nil, runtimeUnavailableError(ref)
	}
	resource.mu.Lock()
	valid := resource.ref == ref && !resource.rejectsNewUseLocked()
	resource.mu.Unlock()
	if !valid {
		return nil, runtimeUnavailableError(ref)
	}
	return resource, nil
}

func runtimeUnavailableError(resource runtimeids.SessionResourceRef) error {
	return errors.Join(
		serverapi.ErrRuntimeUnavailable,
		fmt.Errorf(
			"agent resource %s generation %d is unavailable",
			resource.SessionID(),
			resource.Generation(),
		),
	)
}

func (r *agentResource) settleWorktreeBoundaryLocked(err error) {
	if reducer := r.reducerBoundary; reducer != nil {
		if reducer.phase != reducerBoundaryActive {
			panic(fmt.Sprintf(
				"resource %s generation %d retained settled reducer Boundary phase %d",
				r.ref.SessionID(),
				r.ref.Generation(),
				reducer.phase,
			))
		}
		reducer.phase = reducerBoundaryUnavailable
		r.reducerBoundary = nil
	}
	record := r.worktreeBoundary
	if record == nil {
		return
	}
	switch record.phase {
	case worktreeBoundaryPending, worktreeBoundaryActiveIdle, worktreeBoundaryActiveStepWait:
		record.phase = worktreeBoundaryUnavailable
		record.err = err
		close(record.settled)
	default:
		panic(fmt.Sprintf(
			"resource %s generation %d retained settled Worktree boundary phase %d for operation %s",
			r.ref.SessionID(),
			r.ref.Generation(),
			record.phase,
			record.operationID,
		))
	}
	r.worktreeBoundary = nil
	r.signalLocked()
}
