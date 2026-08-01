package runtimecommand

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	runtimepkg "core/server/runtime"
	"core/server/sessionruntime"
	"core/shared/runtimeids"
)

var (
	ErrAuthorityClosed      = errors.New("runtime command authority is closed")
	ErrResourceUnavailable  = errors.New("runtime command resource is unavailable")
	ErrTurnExpired          = errors.New("runtime command turn is expired")
	ErrCrossResourceTurn    = errors.New("runtime command turn targets another resource")
	ErrCommandHandlerNeeded = errors.New("runtime command handler is required")
)

type Authority struct {
	mu             sync.Mutex
	closed         bool
	resources      map[runtimeids.SessionID]*resourceQueue
	capacity       int
	scopeAuthority *sessionruntime.Authority
}

func (a *Authority) WithExecutionScopeAuthority(authority *sessionruntime.Authority) *Authority {
	if a != nil {
		a.mu.Lock()
		a.scopeAuthority = authority
		for _, resource := range a.resources {
			resource.setScopeAuthority(authority)
		}
		a.mu.Unlock()
	}
	return a
}

func NewAuthority(stageCapacity int) *Authority {
	if stageCapacity <= 0 {
		panic("runtime command stage capacity must be positive")
	}
	return &Authority{
		resources: make(map[runtimeids.SessionID]*resourceQueue),
		capacity:  stageCapacity,
	}
}

func NewProcessAuthority() *Authority {
	return NewAuthority(32)
}

func (a *Authority) AdmitResource(ctx context.Context, ref runtimeids.SessionResourceRef) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return a.Admit(ref)
}

func (a *Authority) AcquireExecution(
	ctx context.Context,
	ref runtimeids.SessionResourceRef,
) (sessionruntime.ResourceExecutionLease, error) {
	resource, err := a.resourceFor(SessionTarget(ref))
	if err != nil {
		return nil, err
	}
	resource.lifecycleMu.Lock()
	defer resource.lifecycleMu.Unlock()
	resource.mu.Lock()
	closed := resource.closed
	resource.mu.Unlock()
	if closed {
		return nil, ErrResourceUnavailable
	}
	if err := resource.acquirePermit(ctx); err != nil {
		return nil, err
	}
	return newExecutionLease(resource), nil
}

func (a *Authority) BeginCompletionAttempt(
	ctx context.Context,
	ref runtimeids.SessionResourceRef,
) (CompletionAttempt, error) {
	resource, err := a.resourceFor(SessionTarget(ref))
	if err != nil {
		return CompletionAttempt{}, err
	}
	if err := context.Cause(ctx); err != nil {
		return CompletionAttempt{}, err
	}
	return resource.completionFence.Begin()
}

func (a *Authority) AcceptInput(
	ctx context.Context,
	ref runtimeids.SessionResourceRef,
) error {
	acceptance, err := a.BeginInput(ctx, ref)
	if err != nil {
		return err
	}
	return acceptance.Commit()
}

func (a *Authority) BeginInput(
	ctx context.Context,
	ref runtimeids.SessionResourceRef,
) (InputAcceptance, error) {
	resource, err := a.resourceFor(SessionTarget(ref))
	if err != nil {
		return InputAcceptance{}, err
	}
	if err := context.Cause(ctx); err != nil {
		return InputAcceptance{}, err
	}
	return resource.completionFence.BeginInput()
}

func (a *Authority) Dispatch(
	ctx context.Context,
	ref runtimeids.SessionResourceRef,
	apply func(runtimepkg.OrderedMutationTurn) error,
) error {
	if apply == nil {
		return ErrCommandHandlerNeeded
	}
	future, err := Enqueue(ctx, a, SessionTarget(ref), func(turn Turn) (struct{}, error) {
		return struct{}{}, apply(turn)
	})
	if err != nil {
		return err
	}
	_, err = future.Await(ctx)
	return err
}

func (a *Authority) DispatchAgent(
	ctx context.Context,
	scope sessionruntime.ExecutionScope,
	apply func(runtimepkg.OrderedMutationTurn) error,
) error {
	target, err := AgentTarget(scope)
	if err != nil {
		return err
	}
	if apply == nil {
		return ErrCommandHandlerNeeded
	}
	future, err := Enqueue(ctx, a, target, func(turn Turn) (struct{}, error) {
		scopeAuthority := a.executionScopeAuthority()
		if scopeAuthority == nil {
			return struct{}{}, sessionruntime.ErrExecutionNoLongerLive
		}
		current, ok := scopeAuthority.CurrentExecutionScope(target.scopeID)
		if !ok {
			return struct{}{}, sessionruntime.ErrExecutionNoLongerLive
		}
		currentTarget, targetErr := AgentTarget(current)
		if targetErr != nil {
			return struct{}{}, targetErr
		}
		if err := turn.CheckTarget(currentTarget); err != nil {
			return struct{}{}, err
		}
		if !target.same(currentTarget) {
			return struct{}{}, sessionruntime.ErrExecutionNoLongerLive
		}
		return struct{}{}, apply(turn)
	})
	if err != nil {
		return err
	}
	_, err = future.Await(ctx)
	return err
}

func (a *Authority) Admit(ref runtimeids.SessionResourceRef) error {
	if a == nil {
		return ErrAuthorityClosed
	}
	if err := ref.Validate(); err != nil {
		return fmt.Errorf("admit runtime command resource: %w", err)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return ErrAuthorityClosed
	}
	sessionID := ref.SessionID()
	if existing := a.resources[sessionID]; existing != nil {
		if existing.ref == ref {
			return nil
		}
		return fmt.Errorf("%w: session resource generation %d is already admitted", ErrResourceUnavailable, existing.ref.Generation())
	}
	resource := newResourceQueue(ref, a.capacity)
	resource.setScopeAuthority(a.scopeAuthority)
	a.resources[sessionID] = resource
	return nil
}

func (a *Authority) CloseResource(ctx context.Context, ref runtimeids.SessionResourceRef) error {
	if a == nil {
		return ErrAuthorityClosed
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	a.mu.Lock()
	resource := a.resources[ref.SessionID()]
	if resource == nil || resource.ref != ref {
		a.mu.Unlock()
		return ErrResourceUnavailable
	}
	delete(a.resources, ref.SessionID())
	a.mu.Unlock()
	return resource.close(ctx)
}

func (a *Authority) Close(ctx context.Context) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	resources := make([]*resourceQueue, 0, len(a.resources))
	for _, resource := range a.resources {
		resources = append(resources, resource)
	}
	a.resources = make(map[runtimeids.SessionID]*resourceQueue)
	a.mu.Unlock()

	var closeErr error
	for _, resource := range resources {
		closeErr = errors.Join(closeErr, resource.close(ctx))
	}
	return closeErr
}

func (a *Authority) resourceFor(target Target) (*resourceQueue, error) {
	if a == nil {
		return nil, ErrAuthorityClosed
	}
	if err := target.Validate(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, ErrAuthorityClosed
	}
	resource := a.resources[target.resource.SessionID()]
	if resource == nil || resource.ref != target.resource {
		return nil, ErrResourceUnavailable
	}
	return resource, nil
}

type resourceQueue struct {
	ref             runtimeids.SessionResourceRef
	permits         chan struct{}
	stages          chan *stage
	completionFence *CompletionFence
	closedCh        chan struct{}
	permitCh        chan struct{}
	closeOnce       sync.Once
	done            chan struct{}
	worker          sync.WaitGroup
	scopeAuthority  *sessionruntime.Authority

	mu          sync.Mutex
	lifecycleMu sync.Mutex
	closed      bool
	seq         uint64
}

func (a *Authority) executionScopeAuthority() *sessionruntime.Authority {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.scopeAuthority
}

func (r *resourceQueue) setScopeAuthority(authority *sessionruntime.Authority) {
	r.mu.Lock()
	r.scopeAuthority = authority
	r.mu.Unlock()
}

func (r *resourceQueue) scopeExecutionAuthority() *sessionruntime.Authority {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.scopeAuthority
}

func (r *resourceQueue) withLifecycleReservation(fn func() error) error {
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	return fn()
}

func newResourceQueue(ref runtimeids.SessionResourceRef, capacity int) *resourceQueue {
	resource := &resourceQueue{
		ref:             ref,
		permits:         make(chan struct{}, capacity),
		stages:          make(chan *stage, capacity),
		completionFence: NewCompletionFence(SessionTarget(ref)),
		closedCh:        make(chan struct{}),
		permitCh:        make(chan struct{}),
		done:            make(chan struct{}),
	}
	resource.worker.Add(1)
	go resource.run()
	return resource
}

func (r *resourceQueue) run() {
	defer r.worker.Done()
	for stage := range r.stages {
		retained := stage.execute(Turn{
			queue:    r,
			sequence: stage.sequence,
			target:   stage.target,
			state:    stage.state,
		})
		if !retained {
			r.releasePermit()
		}
	}
	close(r.done)
}

func (r *resourceQueue) close(ctx context.Context) error {
	r.closeOnce.Do(func() {
		r.lifecycleMu.Lock()
		defer r.lifecycleMu.Unlock()
		r.mu.Lock()
		r.closed = true
		close(r.closedCh)
		close(r.stages)
		r.mu.Unlock()
	})
	select {
	case <-r.done:
	default:
	}
	for {
		select {
		case <-r.done:
			r.mu.Lock()
			drained := len(r.permits) == 0
			changed := r.permitCh
			r.mu.Unlock()
			if drained {
				return nil
			}
			select {
			case <-changed:
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
}

func (r *resourceQueue) acquirePermit(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case r.permits <- struct{}{}:
		return nil
	case <-r.closedCh:
		return ErrResourceUnavailable
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (r *resourceQueue) releasePermit() {
	<-r.permits
	r.mu.Lock()
	close(r.permitCh)
	r.permitCh = make(chan struct{})
	r.mu.Unlock()
}

type stage struct {
	sequence uint64
	target   Target
	state    *turnState
	execute  func(Turn) bool
}

type turnState struct {
	valid  atomic.Bool
	retain *Continuation
}

// Turn is the lexical capability for one synchronous Runtime Command
// application. It becomes invalid before the worker starts the next stage.
type Turn struct {
	queue    *resourceQueue
	sequence uint64
	target   Target
	state    *turnState
}

func (t Turn) CheckTarget(target Target) error {
	if t.queue == nil || t.state == nil || !t.state.valid.Load() {
		return ErrTurnExpired
	}
	if err := target.Validate(); err != nil {
		return err
	}
	if !t.target.same(target) {
		return ErrCrossResourceTurn
	}
	return nil
}

func (t Turn) Retain() (*Continuation, error) {
	if t.queue == nil || t.state == nil || !t.state.valid.Load() {
		return nil, ErrTurnExpired
	}
	if t.state.retain != nil {
		return nil, errors.New("runtime command turn already has a continuation")
	}
	t.state.retain = newContinuation(t.queue, t.target)
	return t.state.retain, nil
}

func (t Turn) Sequence() (uint64, error) {
	if t.queue == nil || t.state == nil || !t.state.valid.Load() {
		return 0, ErrTurnExpired
	}
	return t.sequence, nil
}

func (t Turn) Apply(apply func() error) error {
	if t.queue == nil || t.state == nil || !t.state.valid.Load() {
		return ErrTurnExpired
	}
	if apply == nil {
		return ErrCommandHandlerNeeded
	}
	return apply()
}

func (t Turn) RetainLease() (runtimepkg.OrderedMutationLease, error) {
	continuation, err := t.Retain()
	if err != nil {
		return nil, err
	}
	return continuation, nil
}

func (t Turn) RetainExecutionLease() (sessionruntime.ResourceExecutionLease, error) {
	if t.queue == nil || t.state == nil || !t.state.valid.Load() {
		return nil, ErrTurnExpired
	}
	continuation, err := t.Retain()
	if err != nil {
		return nil, err
	}
	return newExecutionLeaseFromContinuation(t.queue, continuation), nil
}

func Enqueue[T any](
	ctx context.Context,
	authority *Authority,
	target Target,
	apply func(Turn) (T, error),
) (Future[T], error) {
	return enqueue(ctx, authority, target, apply)
}

func enqueue[T any](
	ctx context.Context,
	authority *Authority,
	target Target,
	apply func(Turn) (T, error),
) (Future[T], error) {
	if apply == nil {
		return Future[T]{}, ErrCommandHandlerNeeded
	}
	resource, err := authority.resourceFor(target)
	if err != nil {
		return Future[T]{}, err
	}
	select {
	case resource.permits <- struct{}{}:
	case <-resource.closedCh:
		return Future[T]{}, ErrResourceUnavailable
	case <-ctx.Done():
		return Future[T]{}, context.Cause(ctx)
	}

	done := make(chan struct{})
	result := &futureResult[T]{}
	resource.mu.Lock()
	if resource.closed {
		resource.mu.Unlock()
		resource.releasePermit()
		return Future[T]{}, ErrResourceUnavailable
	}
	resource.seq++
	sequence := resource.seq
	state := &turnState{}
	state.valid.Store(true)
	resource.stages <- &stage{
		sequence: sequence,
		target:   target,
		state:    state,
		execute: func(turn Turn) bool {
			value, applyErr := apply(turn)
			state.valid.Store(false)
			result.value = value
			result.err = applyErr
			close(done)
			return state.retain != nil
		},
	}
	resource.mu.Unlock()
	return Future[T]{done: done, result: result}, nil
}
