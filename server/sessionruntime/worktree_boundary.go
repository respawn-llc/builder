package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"

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
	worktreeBoundaryActive
	worktreeBoundaryReleased
	worktreeBoundaryUnavailable
)

type worktreeBoundaryRecord struct {
	operationID serverapi.WorktreeOperationID
	granted     chan struct{}
	settled     chan struct{}

	mu    sync.Mutex
	phase worktreeBoundaryPhase
	err   error
}

type WorktreeBoundaryClaim struct {
	authority *Authority
	resource  runtimeids.SessionResourceRef
	record    *worktreeBoundaryRecord
}

type WorktreeBoundaryGrant struct {
	OperationID serverapi.WorktreeOperationID
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
	resource.signalLocked()
	return &WorktreeBoundaryClaim{
		authority: a,
		resource:  resourceRef,
		record:    record,
	}, nil
}

func (a *Authority) GrantWorktreeBoundary(
	resourceRef runtimeids.SessionResourceRef,
) (*WorktreeBoundaryGrant, error) {
	if a == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if err := resourceRef.Validate(); err != nil {
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
	record := resource.worktreeBoundary
	if record == nil {
		return nil, nil
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	switch record.phase {
	case worktreeBoundaryPending:
		record.phase = worktreeBoundaryActive
		close(record.granted)
		return &WorktreeBoundaryGrant{OperationID: record.operationID}, nil
	case worktreeBoundaryActive:
		return nil, errors.Join(
			ErrWorktreeBoundaryClaimed,
			fmt.Errorf("Worktree operation %s already owns the boundary", record.operationID),
		)
	default:
		panic(fmt.Sprintf(
			"resource %s generation %d retained settled Worktree boundary phase %d for operation %s",
			resourceRef.SessionID(),
			resourceRef.Generation(),
			record.phase,
			record.operationID,
		))
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
		c.record.mu.Lock()
		err := c.record.err
		c.record.mu.Unlock()
		if err == nil {
			return serverapi.ErrRuntimeUnavailable
		}
		return err
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (c *WorktreeBoundaryClaim) Release() error {
	if c == nil || c.authority == nil || c.record == nil {
		return serverapi.ErrRuntimeUnavailable
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
	c.record.mu.Lock()
	defer c.record.mu.Unlock()
	if c.record.phase != worktreeBoundaryActive {
		return errors.Join(
			ErrWorktreeBoundaryNotActive,
			fmt.Errorf("Worktree operation %s phase is %d", c.record.operationID, c.record.phase),
		)
	}
	c.record.phase = worktreeBoundaryReleased
	resource.worktreeBoundary = nil
	close(c.record.settled)
	resource.signalLocked()
	return nil
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
	record := r.worktreeBoundary
	if record == nil {
		return
	}
	record.mu.Lock()
	switch record.phase {
	case worktreeBoundaryPending, worktreeBoundaryActive:
		record.phase = worktreeBoundaryUnavailable
		record.err = err
		close(record.settled)
	default:
		record.mu.Unlock()
		panic(fmt.Sprintf(
			"resource %s generation %d retained settled Worktree boundary phase %d for operation %s",
			r.ref.SessionID(),
			r.ref.Generation(),
			record.phase,
			record.operationID,
		))
	}
	record.mu.Unlock()
	r.worktreeBoundary = nil
	r.signalLocked()
}
