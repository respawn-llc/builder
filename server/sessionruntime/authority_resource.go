package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"core/server/runlog"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/tools"
	shelltool "core/server/tools/shell"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type AgentResourceState uint8

type agentResourceOwnerlessDisposition uint8

var ErrSessionRunActive = errors.New("session has an active run")
var ErrSessionStartsBlocked = errors.New("session starts are blocked")
var ErrSessionWorkflowActivationActive = errors.New("session has a retained workflow activation")

const (
	AgentResourceBuilding AgentResourceState = iota + 1
	AgentResourceReady
	AgentResourceDraining
	AgentResourceClosed
)

const (
	agentResourceRemainAvailable agentResourceOwnerlessDisposition = iota + 1
	agentResourceRetireWhenIdle
)

type AgentResourceDescriptor struct {
	Ref   runtimeids.SessionResourceRef
	State AgentResourceState
}

type AgentResourceEventFeed func(runtimeids.SessionResourceRef, runtime.Event)

type AgentResourceRetainer func() (io.Closer, error)

type AgentResourceLifecycle interface {
	ResourceReady(context.Context, AgentResourceDescriptor, *runtime.Engine, AgentResourceRetainer) error
	ResourceDraining(context.Context, AgentResourceDescriptor) error
}

type AgentResourceStepLifecycle interface {
	StepBegan(context.Context, AgentResourceDescriptor, runtime.StepLifecycleSnapshot) error
	StepEnded(context.Context, AgentResourceDescriptor, runtime.StepLifecycleSnapshot) error
}

type AgentResourceSelection interface {
	agentResourceSelection()
}

type CurrentAgentResource struct{}

func (CurrentAgentResource) agentResourceSelection() {}

type OpenAgentResource struct{}

func (OpenAgentResource) agentResourceSelection() {}

type ReplaceAgentResource struct{}

func (ReplaceAgentResource) agentResourceSelection() {}

type RuntimeOpenRequest struct {
	SessionID runtimeids.SessionID
	OwnerID   string
	Runtime   *AgentRuntimePlan
}

type RuntimeReleasePolicy uint8

const (
	RuntimeReleaseDetach RuntimeReleasePolicy = iota + 1
	RuntimeReleaseCloseIfIdle
	RuntimeReleaseClose
)

type RuntimeReleaseResult struct {
	Released bool
	Active   bool
}

type RuntimeReleaseRequest struct {
	Resource  runtimeids.SessionResourceRef
	OwnerID   string
	DropOwner bool
	Policy    RuntimeReleasePolicy
}

type RuntimeAttachment struct {
	authority *Authority
	resource  runtimeids.SessionResourceRef
	ownerID   string
}

func (a RuntimeAttachment) Resource() runtimeids.SessionResourceRef {
	return a.resource
}

func (a RuntimeAttachment) Release(ctx context.Context, policy RuntimeReleasePolicy) (RuntimeReleaseResult, error) {
	if a.authority == nil {
		return RuntimeReleaseResult{}, errors.New("runtime attachment is uninitialized")
	}
	return a.authority.ReleaseRuntime(ctx, RuntimeReleaseRequest{
		Resource:  a.resource,
		OwnerID:   a.ownerID,
		DropOwner: true,
		Policy:    policy,
	})
}

type ResourceRetention struct {
	resource *agentResource
	once     sync.Once
	err      error
}

func (r *ResourceRetention) Close() error {
	if r == nil || r.resource == nil {
		return nil
	}
	r.once.Do(func() {
		r.err = r.resource.releasePin()
	})
	return r.err
}

type AgentRuntimeBridge struct {
	authority *Authority
	resource  runtimeids.SessionResourceRef
}

func (b AgentRuntimeBridge) WithEngine(ctx context.Context, callback func(context.Context, *runtime.Engine) error) error {
	if b.authority == nil {
		return errors.New("agent runtime bridge is uninitialized")
	}
	return b.authority.WithRuntime(ctx, b.resource, callback)
}

type AgentRunner func(context.Context, ExecutionScope, AgentRuntimeBridge) error

type ExecutionAskHandler func(context.Context, ExecutionScope, tools.AskQuestionRequest) (tools.AskQuestionResolution, error)

type AgentExecutionRequest struct {
	Descriptor session.SessionDescriptor
	Runtime    *AgentRuntimePlan
	Resource   AgentResourceSelection
	Ask        ExecutionAskHandler
	Runner     AgentRunner
}

type DetachedAgentExecutionRequest struct {
	Descriptor session.SessionDescriptor
	Runtime    *AgentRuntimePlan
	Workflow   WorkflowExecutionRef
	Resource   AgentResourceSelection
	Ask        ExecutionAskHandler
	Runner     AgentRunner
	Config     *workflowruntime.CurrentNodeExecutionConfig
	OnRetire   func()
}

type DetachedAgentExecution struct {
	authority     *Authority
	resource      *agentResource
	execution     *execution
	workflowKey   workflow.CurrentNodeReferenceKey
	config        *workflowruntime.CurrentNodeExecutionConfig
	correlation   *runtimeids.ExecutionCorrelation
	closeResource bool
	runner        AgentRunner
	ask           ExecutionAskHandler
	mu            sync.Mutex
	settled       bool
}

func (a *Authority) PrepareDetachedAgentExecution(
	ctx context.Context,
	request DetachedAgentExecutionRequest,
) (*DetachedAgentExecution, error) {
	if a == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if err := request.Descriptor.Validate(); err != nil {
		return nil, err
	}
	if err := request.Workflow.Validate(); err != nil {
		return nil, err
	}
	if request.Resource == nil || request.Runner == nil || request.Config == nil {
		return nil, errors.New("detached Agent execution request is incomplete")
	}
	gate := a.gateFor(request.Descriptor.SessionID())
	if err := gate.lock.LockContext(ctx); err != nil {
		return nil, err
	}
	if len(gate.blocks) != 0 {
		gate.lock.Unlock()
		return nil, sessionStartsBlockedError(request.Descriptor.SessionID())
	}
	resource, closeResource, err := a.selectDetachedResource(ctx, request.Descriptor, request.Runtime, request.Resource)
	gate.lock.Unlock()
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil, ErrAuthorityClosed
	}
	workflowKey, err := workflowExecutionKeyFor(request.Workflow)
	if err != nil {
		a.mu.Unlock()
		return nil, err
	}
	if a.workflowExecutionByCurrentNodeLocked(request.Workflow, workflowKey) != nil {
		a.mu.Unlock()
		return nil, fmt.Errorf("workflow current node %v is already live", request.Workflow.CurrentNode)
	}
	executionGeneration := a.nextExecutionGenerationLocked()
	a.mu.Unlock()
	resource.mu.Lock()
	if resource.current != nil {
		resource.mu.Unlock()
		return nil, errors.Join(ErrSessionRunActive, fmt.Errorf("session %s already has an agent execution", request.Descriptor.SessionID()))
	}
	if err := resource.pinLocked(); err != nil {
		resource.mu.Unlock()
		return nil, err
	}
	resource.mu.Unlock()
	scopeID := runtimeids.NewExecutionScopeID()
	config := *request.Config
	config.ScopeID = scopeID
	scope := newAgentExecutionScope(scopeID, executionGeneration, resource.ref, &request.Workflow)
	correlation, err := runtimeids.NewExecutionCorrelation(scope.ID(), resource.ref.Generation())
	if err != nil {
		_ = resource.releasePin()
		return nil, err
	}
	runCtx, cancel := context.WithCancel(resource.ctx)
	execution := &execution{
		authority: a, resource: resource, scope: scope, ctx: runCtx, cancel: cancel,
		done: make(chan struct{}), prompts: newExecutionPromptStore(a, scope, a.promptFeed),
		closeResource: closeResource, phase: executionPhaseQueued, onRetire: request.OnRetire,
	}
	return &DetachedAgentExecution{
		authority: a, resource: resource, execution: execution, workflowKey: workflowKey,
		config: &config, correlation: &correlation, closeResource: closeResource,
		runner: request.Runner, ask: request.Ask,
	}, nil
}

func (a *Authority) selectDetachedResource(
	ctx context.Context,
	descriptor session.SessionDescriptor,
	plan *AgentRuntimePlan,
	selection AgentResourceSelection,
) (*agentResource, bool, error) {
	if _, replacing := selection.(ReplaceAgentResource); !replacing {
		return a.selectResource(ctx, descriptor, plan, selection)
	}
	sessionID := descriptor.SessionID()
	a.mu.Lock()
	existing := a.resources[sessionID]
	a.mu.Unlock()
	if existing != nil {
		existing.mu.Lock()
		busy := existing.state == AgentResourceDraining ||
			existing.current != nil ||
			existing.callbacks != 0 ||
			existing.steps != 0
		existing.mu.Unlock()
		if busy {
			return nil, false, errors.Join(
				serverapi.ErrRuntimeUnavailable,
				fmt.Errorf("session %s runtime is draining or active", sessionID),
			)
		}
	}
	return a.selectResource(ctx, descriptor, plan, selection)
}

func (d *DetachedAgentExecution) Scope() (ExecutionScope, error) {
	if d == nil || d.execution == nil {
		return ExecutionScope{}, sessionRuntimeInvariant(
			d != nil && d.authority != nil && d.authority.options.debug,
			"read detached Agent execution Scope",
			errors.New("detached Agent execution is uninitialized"),
		)
	}
	return d.execution.scope, nil
}

func (d *DetachedAgentExecution) Publish(
	ctx context.Context,
	admit func() error,
	published func(ExecutionHandle),
) (ExecutionHandle, func(), error) {
	if d == nil || d.authority == nil || d.execution == nil || d.resource == nil {
		return nil, nil, errors.New("detached Agent execution is required")
	}
	if admit == nil {
		return nil, nil, errors.New("detached Agent admission is required")
	}
	d.mu.Lock()
	if d.settled {
		d.mu.Unlock()
		return nil, nil, ErrExecutionNoLongerLive
	}
	d.settled = true
	d.mu.Unlock()
	if err := context.Cause(ctx); err != nil {
		d.discard()
		return nil, nil, err
	}
	d.authority.mu.Lock()
	d.resource.mu.Lock()
	d.execution.exactMu.Lock()
	workflowRef, workflowErr := workflowRefForDetachedAgent(d.execution.scope)
	if workflowErr != nil {
		d.execution.exactMu.Unlock()
		d.resource.mu.Unlock()
		d.authority.mu.Unlock()
		invariantErr := d.authority.invariant("publish detached Agent execution", workflowErr)
		d.discard()
		return nil, nil, invariantErr
	}
	if d.authority.closed || d.resource.current != nil ||
		d.authority.workflowExecutionByCurrentNodeLocked(workflowRef, d.workflowKey) != nil {
		d.execution.exactMu.Unlock()
		d.resource.mu.Unlock()
		d.authority.mu.Unlock()
		d.discard()
		return nil, nil, ErrExecutionNoLongerLive
	}
	runtimePublication, err := d.resource.engine.PrepareCurrentNodeExecutionPublication(d.config)
	if err != nil {
		d.execution.exactMu.Unlock()
		d.resource.mu.Unlock()
		d.authority.mu.Unlock()
		d.discard()
		return nil, nil, err
	}
	if err := runtimePublication.Begin(); err != nil {
		d.execution.exactMu.Unlock()
		d.resource.mu.Unlock()
		d.authority.mu.Unlock()
		d.discard()
		return nil, nil, err
	}
	var correlationPublication *runtimewire.ExecutionCorrelationPublication
	if d.resource.localTools != nil {
		correlationPublication, err = d.resource.localTools.PrepareExecutionCorrelation(d.correlation)
		if err != nil {
			runtimePublication.Cancel()
			d.execution.exactMu.Unlock()
			d.resource.mu.Unlock()
			d.authority.mu.Unlock()
			d.discard()
			return nil, nil, err
		}
	}
	if err := admit(); err != nil {
		runtimePublication.Cancel()
		d.execution.exactMu.Unlock()
		d.resource.mu.Unlock()
		d.authority.mu.Unlock()
		d.discard()
		return nil, nil, err
	}
	binding := runtimePublication.Commit()
	d.execution.workflow = binding
	if correlationPublication != nil {
		correlationPublication.Commit()
	}
	if d.resource.askBroker != nil {
		scopeID := d.execution.scope.ID()
		askHandler := d.ask
		if askHandler == nil {
			askHandler = func(ctx context.Context, scope ExecutionScope, req tools.AskQuestionRequest) (tools.AskQuestionResolution, error) {
				return d.authority.AwaitPromptResolution(ctx, scope.ID(), req)
			}
		}
		d.resource.askBroker.SetAskHandler(func(ctx context.Context, req tools.AskQuestionRequest) (tools.AskQuestionResolution, error) {
			return askHandler(ctx, d.execution.scope, req)
		})
		d.resource.askScope = &scopeID
	}
	d.resource.current = d.execution
	d.resource.signalLocked()
	d.authority.byScope[d.execution.scope.ID()] = d.execution
	d.authority.addWorkflowExecutionLocked(workflowRef, d.workflowKey, d.execution)
	d.execution.phase = executionPhaseRunning
	d.execution.exactMu.Unlock()
	d.resource.mu.Unlock()
	d.authority.mu.Unlock()
	handle := executionHandle{execution: d.execution}
	if published != nil {
		published(handle)
	}
	return handle, func() {
		go func() {
			if err := context.Cause(d.execution.ctx); err != nil {
				d.execution.finish(ExecutionResult{}, err, nil)
				return
			}
			runErr := d.runner(d.execution.ctx, d.execution.scope, AgentRuntimeBridge{
				authority: d.authority,
				resource:  d.resource.ref,
			})
			d.execution.finish(ExecutionResult{}, runErr, nil)
		}()
	}, nil
}

func (d *DetachedAgentExecution) discard() {
	if d == nil || d.execution == nil || d.resource == nil {
		return
	}
	d.execution.cancel()
	_ = d.resource.releasePin()
}

func (d *DetachedAgentExecution) Cancel() error {
	if d == nil || d.execution == nil || d.resource == nil {
		return nil
	}
	d.mu.Lock()
	if d.settled {
		d.mu.Unlock()
		return nil
	}
	d.settled = true
	d.mu.Unlock()
	d.execution.cancel()
	return d.resource.releasePin()
}

func workflowRefForDetachedAgent(scope ExecutionScope) (WorkflowExecutionRef, error) {
	ref, ok := scope.Workflow()
	if !ok {
		return WorkflowExecutionRef{}, errors.New("detached Agent execution has no Workflow reference")
	}
	return ref, nil
}

type agentResource struct {
	authority *Authority
	ref       runtimeids.SessionResourceRef
	ctx       context.Context
	cancel    context.CancelFunc

	mu                   sync.Mutex
	changed              chan struct{}
	state                AgentResourceState
	owners               map[string]struct{}
	ownerlessDisposition agentResourceOwnerlessDisposition
	store                *session.Store
	engine               *runtime.Engine
	eventBridge          *runtimewire.EventBridge
	logger               *runlog.RunLogger
	localTools           *runtimewire.LocalToolRegistryBinding
	askBroker            *tools.AskQuestionBroker
	askScope             *runtimeids.ExecutionScopeID
	close                func() error
	backgroundLimit      int
	backgroundMode       shelltool.BackgroundOutputMode
	current              *execution
	pins                 int
	callbacks            int
	steps                int
	lifecycleReady       bool
	lifecycleDraining    bool
}

func (r *agentResource) descriptorLocked() AgentResourceDescriptor {
	return AgentResourceDescriptor{
		Ref:   r.ref,
		State: r.state,
	}
}

func (r *agentResource) descriptor() AgentResourceDescriptor {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.descriptorLocked()
}

func (r *agentResource) signalLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}

func (r *agentResource) currentExecution() (ExecutionHandle, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current == nil {
		return nil, false
	}
	return executionHandle{execution: r.current}, true
}

func (r *agentResource) pinLocked() error {
	if r.rejectsNewUseLocked() {
		return errors.Join(
			serverapi.ErrRuntimeUnavailable,
			fmt.Errorf("agent resource %s generation %d is unavailable", r.ref.SessionID(), r.ref.Generation()),
		)
	}
	r.pins++
	r.signalLocked()
	return nil
}

func (r *agentResource) releasePin() error {
	r.mu.Lock()
	if r.pins <= 0 {
		panic(fmt.Sprintf("agent resource %s generation %d pin underflow", r.ref.SessionID(), r.ref.Generation()))
	}
	r.pins--
	r.signalLocked()
	r.mu.Unlock()
	return r.authority.closeRetiringResource(context.Background(), r)
}

func (r *agentResource) requestRetirementIfOwnerless() {
	r.mu.Lock()
	if len(r.owners) == 0 {
		r.ownerlessDisposition = agentResourceRetireWhenIdle
		r.signalLocked()
	}
	r.mu.Unlock()
}

func (r *agentResource) withEngine(ctx context.Context, ref runtimeids.SessionResourceRef, callback func(context.Context, *runtime.Engine) error) (err error) {
	if callback == nil {
		return errors.New("agent resource callback is required")
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	if r.ref != ref || r.rejectsNewUseLocked() || r.engine == nil {
		r.mu.Unlock()
		return errors.Join(
			serverapi.ErrRuntimeUnavailable,
			fmt.Errorf("agent resource %s generation %d is unavailable", ref.SessionID(), ref.Generation()),
		)
	}
	engine := r.engine
	r.callbacks++
	r.signalLocked()
	r.mu.Unlock()
	defer func() {
		if releaseErr := r.releaseCallback(); releaseErr != nil {
			err = errors.Join(err, releaseErr)
		}
	}()
	return callback(ctx, engine)
}

// withStoreUnderAdmission runs while the caller owns the Session admission
// gate. Resource retirement must be attempted only after that gate is
// released, because retirement acquires the same gate.
func (r *agentResource) withStoreUnderAdmission(ctx context.Context, callback func(context.Context, *session.Store) error) error {
	r.mu.Lock()
	if r.rejectsNewUseLocked() || r.store == nil {
		r.mu.Unlock()
		return fmt.Errorf("agent resource %s generation %d is unavailable", r.ref.SessionID(), r.ref.Generation())
	}
	store := r.store
	r.callbacks++
	r.signalLocked()
	r.mu.Unlock()
	defer func() {
		r.releaseCallbackCount()
	}()
	return callback(ctx, store)
}

func (r *agentResource) withEngineUnderAdmission(
	ctx context.Context,
	callback func(context.Context, *runtime.Engine) error,
) error {
	if callback == nil {
		return errors.New("agent resource callback is required")
	}
	r.mu.Lock()
	if r.rejectsNewUseLocked() || r.engine == nil {
		ref := r.ref
		r.mu.Unlock()
		return errors.Join(
			serverapi.ErrRuntimeUnavailable,
			fmt.Errorf("agent resource %s generation %d is unavailable", ref.SessionID(), ref.Generation()),
		)
	}
	engine := r.engine
	r.callbacks++
	r.signalLocked()
	r.mu.Unlock()
	defer r.releaseCallbackCount()
	return callback(ctx, engine)
}

func (r *agentResource) releaseCallbackCount() {
	r.mu.Lock()
	if r.callbacks <= 0 {
		panic(fmt.Sprintf("agent resource %s generation %d callback underflow", r.ref.SessionID(), r.ref.Generation()))
	}
	r.callbacks--
	r.signalLocked()
	r.mu.Unlock()
}

func (r *agentResource) releaseCallback() error {
	r.releaseCallbackCount()
	return r.authority.closeRetiringResource(context.Background(), r)
}

func (r *agentResource) publishReady(ctx context.Context) error {
	if r.authority.options.resourceLifecycle == nil {
		return nil
	}
	r.mu.Lock()
	if r.state != AgentResourceReady || r.engine == nil {
		r.mu.Unlock()
		return fmt.Errorf("agent resource %s generation %d is not ready", r.ref.SessionID(), r.ref.Generation())
	}
	if r.lifecycleReady {
		r.mu.Unlock()
		return nil
	}
	r.lifecycleReady = true
	descriptor := r.descriptorLocked()
	engine := r.engine
	r.mu.Unlock()
	return r.authority.options.resourceLifecycle.ResourceReady(ctx, descriptor, engine, func() (io.Closer, error) {
		return r.authority.retainResource(r.ref)
	})
}

func (r *agentResource) closeResource(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	switch r.state {
	case AgentResourceClosed:
		r.mu.Unlock()
		return nil
	case AgentResourceBuilding:
		r.mu.Unlock()
		return fmt.Errorf("agent resource %s generation %d is still building", r.ref.SessionID(), r.ref.Generation())
	}
	r.state = AgentResourceDraining
	r.cancel()
	r.signalLocked()
	notifyDraining := r.lifecycleReady && !r.lifecycleDraining && r.authority.options.resourceLifecycle != nil
	if notifyDraining {
		r.lifecycleDraining = true
	}
	descriptor := r.descriptorLocked()
	engine := r.engine
	r.mu.Unlock()
	var interruptErr error
	if engine != nil {
		interruptErr = engine.Interrupt()
	}
	var lifecycleErr error
	if notifyDraining {
		lifecycleErr = r.authority.options.resourceLifecycle.ResourceDraining(ctx, descriptor)
	}
	r.mu.Lock()
	for r.pins != 0 || r.callbacks != 0 || r.steps != 0 || r.current != nil {
		changed := r.changed
		r.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
		r.mu.Lock()
	}
	closeEngine := r.close
	r.state = AgentResourceClosed
	r.signalLocked()
	r.mu.Unlock()
	if closeEngine == nil {
		return lifecycleErr
	}
	return errors.Join(lifecycleErr, interruptErr, closeEngine())
}

func (r *agentResource) StepBegan(ctx context.Context, snapshot runtime.StepLifecycleSnapshot) error {
	r.mu.Lock()
	if r.rejectsNewStepLocked() {
		r.mu.Unlock()
		return errors.Join(
			runtime.ErrEngineClosed,
			serverapi.ErrRuntimeUnavailable,
			fmt.Errorf("agent resource %s generation %d is retiring", r.ref.SessionID(), r.ref.Generation()),
		)
	}
	if r.steps != 0 {
		r.mu.Unlock()
		panic(fmt.Sprintf("agent resource %s generation %d admitted overlapping engine steps", r.ref.SessionID(), r.ref.Generation()))
	}
	r.steps++
	r.signalLocked()
	descriptor := r.descriptorLocked()
	r.mu.Unlock()
	if r.authority.options.stepLifecycle == nil {
		return nil
	}
	return r.authority.options.stepLifecycle.StepBegan(ctx, descriptor, snapshot)
}

func (r *agentResource) StepEnded(ctx context.Context, snapshot runtime.StepLifecycleSnapshot) error {
	r.mu.Lock()
	if r.steps == 0 {
		rejected := r.rejectsNewStepLocked()
		r.mu.Unlock()
		if rejected {
			return nil
		}
		panic(fmt.Sprintf("agent resource %s generation %d engine step underflow", r.ref.SessionID(), r.ref.Generation()))
	}
	descriptor := r.descriptorLocked()
	r.mu.Unlock()
	var publishErr error
	if r.authority.options.stepLifecycle != nil {
		publishErr = r.authority.options.stepLifecycle.StepEnded(ctx, descriptor, snapshot)
	}
	r.mu.Lock()
	if r.steps != 1 {
		r.mu.Unlock()
		panic(fmt.Sprintf("agent resource %s generation %d engine step count changed during completion", r.ref.SessionID(), r.ref.Generation()))
	}
	r.steps--
	r.signalLocked()
	r.mu.Unlock()
	return publishErr
}

func (r *agentResource) rejectsNewUseLocked() bool {
	return r.state != AgentResourceReady
}

func (r *agentResource) rejectsNewStepLocked() bool {
	return r.state != AgentResourceReady
}

func (a *Authority) OpenRuntime(ctx context.Context, request RuntimeOpenRequest) (RuntimeAttachment, error) {
	return a.openRuntime(ctx, request, nil)
}

func (a *Authority) openRuntime(
	ctx context.Context,
	request RuntimeOpenRequest,
	resolvePlan func(context.Context, *session.Store) (*AgentRuntimePlan, error),
) (RuntimeAttachment, error) {
	if a == nil {
		return RuntimeAttachment{}, errors.New("session runtime authority is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := context.Cause(ctx); err != nil {
		return RuntimeAttachment{}, err
	}
	if request.SessionID.IsZero() {
		return RuntimeAttachment{}, errors.New("session id is required")
	}
	ownerID := strings.TrimSpace(request.OwnerID)
	if ownerID == "" {
		return RuntimeAttachment{}, errors.New("runtime owner id is required")
	}
	gate := a.gateFor(request.SessionID)
	gate.lock.Lock()
	defer gate.lock.Unlock()
	if len(gate.blocks) != 0 {
		return RuntimeAttachment{}, sessionStartsBlockedError(request.SessionID)
	}
	descriptor, err := session.NewOpenSessionDescriptor(request.SessionID)
	if err != nil {
		return RuntimeAttachment{}, err
	}
	var admittedStore *runtimeStoreAdmission
	if resolvePlan != nil {
		admittedStore, err = a.materializeRuntimeStore(descriptor)
		if err != nil {
			return RuntimeAttachment{}, err
		}
		request.Runtime, err = resolvePlan(ctx, admittedStore.store)
		if err != nil {
			return RuntimeAttachment{}, err
		}
		if request.Runtime == nil {
			return RuntimeAttachment{}, errors.New("resolved runtime plan is required")
		}
	}
	resource, err := a.openResource(ctx, descriptor, request.Runtime, &ownerID, admittedStore)
	if err != nil {
		return RuntimeAttachment{}, err
	}
	return RuntimeAttachment{authority: a, resource: resource.ref, ownerID: ownerID}, nil
}

func (a *Authority) ReleaseRuntime(ctx context.Context, request RuntimeReleaseRequest) (RuntimeReleaseResult, error) {
	if a == nil {
		return RuntimeReleaseResult{}, errors.New("session runtime authority is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := context.Cause(ctx); err != nil {
		return RuntimeReleaseResult{}, err
	}
	if err := request.Resource.Validate(); err != nil {
		return RuntimeReleaseResult{}, err
	}
	ownerID := strings.TrimSpace(request.OwnerID)
	if ownerID == "" {
		return RuntimeReleaseResult{}, errors.New("runtime attachment owner id is required")
	}
	switch request.Policy {
	case RuntimeReleaseDetach, RuntimeReleaseCloseIfIdle, RuntimeReleaseClose:
	default:
		return RuntimeReleaseResult{}, errors.New("runtime release policy is required")
	}
	if request.Policy == RuntimeReleaseDetach && !request.DropOwner {
		return RuntimeReleaseResult{}, errors.New("runtime detach release requires owner drop")
	}
	sessionID := request.Resource.SessionID()
	gate := a.gateFor(sessionID)
	gate.lock.Lock()
	defer gate.lock.Unlock()

	a.mu.Lock()
	resource := a.resources[sessionID]
	a.mu.Unlock()
	if resource == nil || resource.ref != request.Resource {
		return RuntimeReleaseResult{Released: true}, nil
	}
	resource.mu.Lock()
	if resource.state == AgentResourceClosed || resource.state == AgentResourceDraining {
		resource.mu.Unlock()
		return RuntimeReleaseResult{Released: true}, nil
	}
	if _, owns := resource.owners[ownerID]; !owns {
		resource.mu.Unlock()
		return RuntimeReleaseResult{Released: true}, nil
	}
	if request.DropOwner {
		delete(resource.owners, ownerID)
		if len(resource.owners) == 0 && request.Policy == RuntimeReleaseDetach {
			resource.ownerlessDisposition = agentResourceRemainAvailable
		}
		resource.signalLocked()
	}
	if request.Policy == RuntimeReleaseDetach {
		resource.mu.Unlock()
		return RuntimeReleaseResult{Released: true}, nil
	}
	if request.Policy == RuntimeReleaseCloseIfIdle {
		if request.DropOwner && len(resource.owners) != 0 {
			resource.mu.Unlock()
			return RuntimeReleaseResult{}, nil
		}
		inFlight := resource.current != nil ||
			resource.pins != 0 ||
			resource.callbacks != 0 ||
			resource.steps != 0
		queued := resource.engine != nil && resource.engine.HasQueuedUserWork()
		scheduled := resource.engine != nil && resource.engine.HasScheduledQueuedUserWork()
		if inFlight || scheduled || (!request.DropOwner && queued) {
			if request.DropOwner {
				resource.ownerlessDisposition = agentResourceRetireWhenIdle
			}
			resource.mu.Unlock()
			return RuntimeReleaseResult{Active: true}, nil
		}
	}
	closed, closeErr := a.closeAdmittedResourceLocked(ctx, resource)
	return RuntimeReleaseResult{Released: closed}, closeErr
}

func (a *Authority) closeRetiringResource(ctx context.Context, resource *agentResource) error {
	if resource == nil {
		return nil
	}
	resource.mu.Lock()
	retiring := resource.state == AgentResourceReady &&
		resource.ownerlessDisposition == agentResourceRetireWhenIdle
	resource.mu.Unlock()
	if !retiring {
		return nil
	}
	sessionID := resource.ref.SessionID()
	gate := a.gateFor(sessionID)
	gate.lock.Lock()
	defer gate.lock.Unlock()

	resource.mu.Lock()
	if resource.ownerlessDisposition != agentResourceRetireWhenIdle ||
		resource.state != AgentResourceReady ||
		len(resource.owners) != 0 ||
		resource.current != nil ||
		resource.pins != 0 ||
		resource.callbacks != 0 ||
		resource.steps != 0 {
		resource.mu.Unlock()
		return nil
	}
	if resource.engine != nil && resource.engine.HasScheduledQueuedUserWork() {
		resource.mu.Unlock()
		return nil
	}
	_, closeErr := a.closeAdmittedResourceLocked(ctx, resource)
	return closeErr
}

func (a *Authority) retireRuntimeAbortResource(ctx context.Context, resource *agentResource) error {
	if resource == nil {
		return nil
	}
	sessionID := resource.ref.SessionID()
	gate := a.gateFor(sessionID)
	gate.lock.Lock()
	defer gate.lock.Unlock()

	resource.mu.Lock()
	a.mu.Lock()
	admitted := a.resources[sessionID] == resource
	a.mu.Unlock()
	if !admitted || resource.state == AgentResourceClosed {
		resource.mu.Unlock()
		return nil
	}
	if resource.state != AgentResourceReady ||
		resource.steps != 0 {
		descriptor := resource.descriptorLocked()
		resource.mu.Unlock()
		return fmt.Errorf(
			"retire runtime-aborted resource %s generation %d from state %d with live ownership",
			descriptor.Ref.SessionID(),
			descriptor.Ref.Generation(),
			descriptor.State,
		)
	}
	_, err := a.closeAdmittedResourceLocked(ctx, resource)
	return err
}

// closeAdmittedResourceLocked owns the exact transition from a ready resource
// admitted for closure to its removal from the live authority. The caller must
// hold the session admission gate and resource.mu.
func (a *Authority) closeAdmittedResourceLocked(ctx context.Context, resource *agentResource) (bool, error) {
	sessionID := resource.ref.SessionID()
	engine := resource.engine
	resource.state = AgentResourceDraining
	resource.signalLocked()
	resource.mu.Unlock()

	if engine != nil {
		engine.FailQueuedUserMessages(runtime.QueuedUserMessageFailureClosing)
	}
	closeErr := resource.closeResource(ctx)
	resource.mu.Lock()
	closed := resource.state == AgentResourceClosed
	resource.mu.Unlock()
	if closed {
		a.mu.Lock()
		if a.resources[sessionID] == resource {
			delete(a.resources, sessionID)
		}
		a.mu.Unlock()
	}
	return closed, closeErr
}

func (a *Authority) StartAgentExecution(ctx context.Context, request AgentExecutionRequest) (ExecutionHandle, error) {
	if a == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if err := request.Descriptor.Validate(); err != nil {
		return nil, err
	}
	sessionID := request.Descriptor.SessionID()
	if request.Resource == nil {
		return nil, errors.New("agent resource selection is required")
	}
	if request.Runner == nil {
		return nil, errors.New("agent runner is required")
	}
	gate := a.gateFor(sessionID)
	if err := gate.lock.LockContext(ctx); err != nil {
		return nil, err
	}
	defer gate.lock.Unlock()
	return a.startAgentExecutionUnderAdmission(ctx, gate, request)
}

func (a *Authority) startAgentExecutionUnderAdmission(
	ctx context.Context,
	gate *sessionAdmissionGate,
	request AgentExecutionRequest,
) (ExecutionHandle, error) {
	sessionID := request.Descriptor.SessionID()
	if len(gate.blocks) != 0 {
		return nil, sessionStartsBlockedError(sessionID)
	}
	resource, closeResource, err := a.selectResource(ctx, request.Descriptor, request.Runtime, request.Resource)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil, ErrAuthorityClosed
	}
	resource.mu.Lock()
	if resource.current != nil {
		resource.mu.Unlock()
		a.mu.Unlock()
		return nil, errors.Join(ErrSessionRunActive, fmt.Errorf("session %s already has an agent execution", sessionID))
	}
	if resource.engine.CurrentNodeExecutionConfigured() {
		resource.mu.Unlock()
		a.mu.Unlock()
		return nil, errors.Join(
			ErrSessionWorkflowActivationActive,
			fmt.Errorf("session %s cannot start an ordinary execution while its workflow activation remains active", sessionID),
		)
	}
	if err := resource.pinLocked(); err != nil {
		resource.mu.Unlock()
		a.mu.Unlock()
		return nil, err
	}
	scopeID := runtimeids.NewExecutionScopeID()
	executionGeneration := a.nextExecutionGenerationLocked()
	scope := newAgentExecutionScope(scopeID, executionGeneration, resource.ref, nil)
	correlation, err := runtimeids.NewExecutionCorrelation(scope.ID(), resource.ref.Generation())
	if err != nil {
		resource.pins--
		resource.signalLocked()
		resource.mu.Unlock()
		a.mu.Unlock()
		return nil, a.invariant(
			"create Agent execution correlation",
			fmt.Errorf("scope=%s resource=%v: %w", scope.ID(), resource.ref, err),
		)
	}
	if resource.localTools != nil {
		if err := resource.localTools.BindExecutionCorrelation(&correlation); err != nil {
			resource.pins--
			resource.signalLocked()
			resource.mu.Unlock()
			a.mu.Unlock()
			return nil, fmt.Errorf("bind agent execution correlation: %w", err)
		}
	}
	runCtx, cancel := context.WithCancel(resource.ctx)
	execution := &execution{
		authority:     a,
		resource:      resource,
		scope:         scope,
		ctx:           runCtx,
		cancel:        cancel,
		done:          make(chan struct{}),
		prompts:       newExecutionPromptStore(a, scope, a.promptFeed),
		closeResource: closeResource,
		phase:         executionPhaseRunning,
	}
	if resource.askBroker != nil {
		scopeID := scope.ID()
		askHandler := request.Ask
		if askHandler == nil {
			askHandler = func(ctx context.Context, scope ExecutionScope, req tools.AskQuestionRequest) (tools.AskQuestionResolution, error) {
				return a.AwaitPromptResolution(ctx, scope.ID(), req)
			}
		}
		resource.askBroker.SetAskHandler(func(ctx context.Context, req tools.AskQuestionRequest) (tools.AskQuestionResolution, error) {
			return askHandler(ctx, execution.scope, req)
		})
		resource.askScope = &scopeID
	}
	resource.current = execution
	resource.signalLocked()
	resource.mu.Unlock()
	a.byScope[scope.ID()] = execution
	a.mu.Unlock()
	go func() {
		runErr := request.Runner(execution.ctx, execution.scope, AgentRuntimeBridge{
			authority: a,
			resource:  resource.ref,
		})
		execution.finish(ExecutionResult{}, runErr, nil)
	}()
	return executionHandle{execution: execution}, nil
}

func (a *Authority) RunCurrentHumanTurn(
	ctx context.Context,
	descriptor session.SessionDescriptor,
	accept runtime.CommandAcceptance,
	run func(context.Context, *runtime.Engine, runtime.CommandAcceptance) error,
) error {
	if a == nil {
		return errors.New("session runtime authority is required")
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if err := descriptor.Validate(); err != nil {
		return err
	}
	if accept == nil {
		return errors.New("human turn acceptance is required")
	}
	if run == nil {
		return errors.New("human turn callback is required")
	}
	sessionID := descriptor.SessionID()
	gate := a.gateFor(sessionID)
	if err := gate.lock.LockContext(ctx); err != nil {
		return err
	}
	var releaseGateOnce sync.Once
	releaseGate := func() {
		releaseGateOnce.Do(gate.lock.Unlock)
	}
	defer releaseGate()
	if len(gate.blocks) != 0 {
		return sessionStartsBlockedError(sessionID)
	}

	a.mu.Lock()
	resource := a.resources[sessionID]
	a.mu.Unlock()
	if resource == nil {
		return runtimeUnavailableErr(sessionID.String())
	}

	accepted := make(chan struct{})
	var acceptedOnce sync.Once
	admit := runtime.CommandAcceptance(func(commit func() (bool, error)) (bool, error) {
		committed, err := accept(commit)
		if committed {
			acceptedOnce.Do(func() {
				releaseGate()
				close(accepted)
			})
		}
		return committed, err
	})

	resource.mu.Lock()
	current := resource.current
	resource.mu.Unlock()
	if current != nil {
		_, workflowExecution := current.scope.Workflow()
		interruptedHumanExecution := current.scope.Kind() == ExecutionScopeAgent &&
			!workflowExecution &&
			context.Cause(current.ctx) != nil
		if interruptedHumanExecution {
			releaseGate()
			if err := current.awaitDone(ctx); err != nil {
				return err
			}
			return a.RunCurrentHumanTurn(ctx, descriptor, accept, run)
		}
		runErr := resource.withEngineUnderAdmission(ctx, func(runCtx context.Context, engine *runtime.Engine) error {
			return run(runCtx, engine, admit)
		})
		releaseGate()
		return errors.Join(runErr, a.closeRetiringResource(context.Background(), resource))
	}

	operationContinues := make(chan bool, 1)
	handle, err := a.startAgentExecutionUnderAdmission(ctx, gate, AgentExecutionRequest{
		Descriptor: descriptor,
		Resource:   CurrentAgentResource{},
		Runner: func(executionCtx context.Context, _ ExecutionScope, bridge AgentRuntimeBridge) error {
			callbackRan := false
			runErr := bridge.WithEngine(executionCtx, func(_ context.Context, engine *runtime.Engine) error {
				callbackRan = true
				runCtx, stop := MergeContexts(executionCtx, ctx)
				err := run(runCtx, engine, admit)
				stop()
				if err != nil {
					operationContinues <- false
					return err
				}
				queuedWorkScheduled := engine.HasScheduledQueuedUserWork()
				goalLoopActive := engine.GoalLoopRunning()
				operationContinues <- queuedWorkScheduled || goalLoopActive
				if queuedWorkScheduled {
					if err := engine.WaitForScheduledQueuedUserWork(executionCtx); err != nil {
						return err
					}
				}
				if engine.GoalLoopRunning() {
					return engine.WaitForGoalLoop(executionCtx)
				}
				return nil
			})
			if !callbackRan {
				operationContinues <- false
			}
			return runErr
		},
	})
	if err != nil {
		return err
	}
	exactHandle, ok := handle.(executionHandle)
	if !ok || exactHandle.execution == nil {
		return a.invariant(
			"run current human Agent turn",
			fmt.Errorf("execution handle type=%T", handle),
		)
	}

	select {
	case <-accepted:
	case <-exactHandle.execution.done:
		releaseGate()
	case <-ctx.Done():
		releaseGate()
	}
	if <-operationContinues {
		return nil
	}
	_, err = handle.Wait(context.Background())
	acceptedFresh := false
	select {
	case <-accepted:
		acceptedFresh = true
	default:
	}
	if !acceptedFresh && context.Cause(ctx) == nil && errors.Is(err, context.Canceled) {
		return a.RunCurrentHumanTurn(ctx, descriptor, accept, run)
	}
	return err
}

func (a *Authority) RunCurrentAgentExecution(
	ctx context.Context,
	descriptor session.SessionDescriptor,
	run func(context.Context, *runtime.Engine) error,
) error {
	if a == nil {
		return errors.New("session runtime authority is required")
	}
	if run == nil {
		return errors.New("agent runtime callback is required")
	}
	operationContinues := make(chan bool, 1)
	handle, err := a.StartAgentExecution(ctx, AgentExecutionRequest{
		Descriptor: descriptor,
		Resource:   CurrentAgentResource{},
		Runner: func(executionCtx context.Context, _ ExecutionScope, bridge AgentRuntimeBridge) error {
			callbackRan := false
			runErr := bridge.WithEngine(executionCtx, func(_ context.Context, engine *runtime.Engine) error {
				callbackRan = true
				runCtx, stop := MergeContexts(executionCtx, ctx)
				err := run(runCtx, engine)
				stop()
				goalLoopActive := err == nil && engine.GoalLoopRunning()
				operationContinues <- goalLoopActive
				if err != nil || !goalLoopActive {
					return err
				}
				return engine.WaitForGoalLoop(executionCtx)
			})
			if !callbackRan {
				operationContinues <- false
			}
			return runErr
		},
	})
	if err != nil {
		return err
	}
	if <-operationContinues {
		return nil
	}
	_, err = handle.Wait(context.Background())
	return err
}

func (a *Authority) WithRuntime(ctx context.Context, ref runtimeids.SessionResourceRef, callback func(context.Context, *runtime.Engine) error) error {
	if a == nil {
		return errors.New("session runtime authority is required")
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	a.mu.Lock()
	resource := a.resources[ref.SessionID()]
	a.mu.Unlock()
	if resource == nil {
		return errors.Join(
			serverapi.ErrRuntimeUnavailable,
			fmt.Errorf("agent resource %s generation %d is unavailable", ref.SessionID(), ref.Generation()),
		)
	}
	return resource.withEngine(ctx, ref, callback)
}

func (a *Authority) WithCurrentRuntime(ctx context.Context, sessionID runtimeids.SessionID, callback func(context.Context, *runtime.Engine) error) error {
	if a == nil {
		return errors.New("session runtime authority is required")
	}
	if sessionID.IsZero() {
		return errors.New("session id is required")
	}
	a.mu.Lock()
	resource := a.resources[sessionID]
	a.mu.Unlock()
	if resource == nil {
		return errors.Join(
			serverapi.ErrRuntimeUnavailable,
			fmt.Errorf("session %s has no active runtime available", sessionID),
		)
	}
	return resource.withEngine(ctx, resource.ref, callback)
}

func (a *Authority) WithLiveExecutionRuntime(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	callback func(context.Context, *runtime.Engine) error,
) error {
	if a == nil {
		return errors.New("session runtime authority is required")
	}
	execution, ok := a.SessionExecution(sessionID)
	if !ok {
		err := a.WithCurrentRuntime(ctx, sessionID, func(context.Context, *runtime.Engine) error {
			return nil
		})
		if err != nil {
			return err
		}
		return serverapi.ErrRuntimeNoActiveRun
	}
	resource, ok := execution.Scope().Resource()
	if !ok {
		return errors.New("agent execution scope has no runtime resource")
	}
	return a.WithRuntime(ctx, resource, callback)
}

// WithRetainedWorkflowRuntime admits a callback only while the Session keeps
// Workflow activation without requiring an Exact Execution Scope to be live.
func (a *Authority) WithRetainedWorkflowRuntime(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	callback func(context.Context, *runtime.Engine) error,
) error {
	if callback == nil {
		return errors.New("retained workflow runtime callback is required")
	}
	return a.WithCurrentRuntime(ctx, sessionID, func(ctx context.Context, engine *runtime.Engine) error {
		if !engine.CurrentNodeExecutionConfigured() {
			return errors.Join(
				serverapi.ErrRuntimeNoActiveRun,
				fmt.Errorf("session %s has no retained workflow activation", sessionID),
			)
		}
		return callback(ctx, engine)
	})
}

func (a *Authority) withInterruptibleAgentTurn(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	withoutExecution func() error,
	callback func(context.Context, *runtime.Engine, *execution) error,
) error {
	if a == nil {
		return errors.New("session runtime authority is required")
	}
	if sessionID.IsZero() {
		return errors.New("session id is required")
	}
	if callback == nil {
		return errors.New("interruptible Agent Turn mutation is required")
	}
	runWithoutExecution := func(missing error) error {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		if withoutExecution != nil {
			return withoutExecution()
		}
		return missing
	}
	a.mu.Lock()
	resource := a.resources[sessionID]
	if resource == nil {
		defer a.mu.Unlock()
		return runWithoutExecution(errors.Join(serverapi.ErrRuntimeUnavailable, fmt.Errorf("session %s has no active runtime available", sessionID)))
	}
	resource.mu.Lock()
	execution := resource.current
	if execution == nil {
		defer a.mu.Unlock()
		defer resource.mu.Unlock()
		return runWithoutExecution(ErrExecutionNoLongerLive)
	}
	resource.mu.Unlock()
	a.mu.Unlock()
	execution.prompts.mu.RLock()
	defer execution.prompts.mu.RUnlock()
	resource.mu.Lock()
	defer resource.mu.Unlock()
	if resource.current != execution {
		return ErrExecutionNoLongerLive
	}
	if resource.rejectsNewUseLocked() || resource.engine == nil {
		return errors.Join(serverapi.ErrRuntimeUnavailable, fmt.Errorf("session %s has no active runtime available", sessionID))
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return callback(ctx, resource.engine, execution)
}

// WithInterruptibleAgentTurn prevents Question admission across one exact current-execution mutation.
func (a *Authority) WithInterruptibleAgentTurn(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	withoutExecution func() error,
	callback func(context.Context, *runtime.Engine) error,
) error {
	if callback == nil {
		return errors.New("interruptible Agent Turn mutation is required")
	}
	return a.withInterruptibleAgentTurn(
		ctx,
		sessionID,
		withoutExecution,
		func(ctx context.Context, engine *runtime.Engine, _ *execution) error {
			return callback(ctx, engine)
		},
	)
}

func (a *Authority) InterruptCurrentAgentTurn(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	withoutExecution func() error,
) (bool, error) {
	return a.interruptCurrentAgentExecution(
		ctx,
		sessionID,
		withoutExecution,
		func(engine *runtime.Engine) (bool, error) {
			return engine.TryInterruptActiveAgentTurn()
		},
	)
}

func (a *Authority) InterruptCurrentLiveRun(
	ctx context.Context,
	sessionID runtimeids.SessionID,
) (bool, error) {
	return a.interruptCurrentAgentExecution(
		ctx,
		sessionID,
		func() error { return nil },
		func(engine *runtime.Engine) (bool, error) {
			return engine.TryInterruptActiveRun()
		},
	)
}

func (a *Authority) interruptCurrentAgentExecution(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	withoutExecution func() error,
	interrupt func(*runtime.Engine) (bool, error),
) (bool, error) {
	if interrupt == nil {
		return false, errors.New("Agent execution interrupt operation is required")
	}
	var interrupted bool
	err := a.withInterruptibleAgentTurn(
		ctx,
		sessionID,
		withoutExecution,
		func(_ context.Context, engine *runtime.Engine, execution *execution) error {
			var err error
			interrupted, err = interrupt(engine)
			if err == nil && interrupted {
				execution.cancel()
			}
			return err
		},
	)
	return interrupted, err
}

func (a *Authority) retainResource(ref runtimeids.SessionResourceRef) (*ResourceRetention, error) {
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
		return nil, fmt.Errorf("agent resource %s generation %d is unavailable", ref.SessionID(), ref.Generation())
	}
	resource.mu.Lock()
	defer resource.mu.Unlock()
	if resource.ref != ref {
		return nil, fmt.Errorf("agent resource %s generation %d is stale", ref.SessionID(), ref.Generation())
	}
	if err := resource.pinLocked(); err != nil {
		return nil, err
	}
	return &ResourceRetention{resource: resource}, nil
}

func (a *Authority) selectResource(ctx context.Context, descriptor session.SessionDescriptor, plan *AgentRuntimePlan, selection AgentResourceSelection) (*agentResource, bool, error) {
	sessionID := descriptor.SessionID()
	switch selection.(type) {
	case CurrentAgentResource:
		a.mu.Lock()
		resource := a.resources[sessionID]
		a.mu.Unlock()
		if resource == nil {
			return nil, false, errors.Join(
				serverapi.ErrRuntimeUnavailable,
				fmt.Errorf("session %s has no registered runtime", sessionID),
			)
		}
		return resource, false, nil
	case OpenAgentResource:
		resource, err := a.openResource(ctx, descriptor, plan, nil, nil)
		return resource, true, err
	case ReplaceAgentResource:
		resource, err := a.replaceResource(ctx, descriptor, plan)
		return resource, true, err
	default:
		return nil, false, fmt.Errorf("unsupported agent resource selection %T", selection)
	}
}

func (a *Authority) openResource(
	ctx context.Context,
	descriptor session.SessionDescriptor,
	plan *AgentRuntimePlan,
	ownerID *string,
	admittedStore *runtimeStoreAdmission,
) (*agentResource, error) {
	sessionID := descriptor.SessionID()
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil, ErrAuthorityClosed
	}
	resource := a.resources[sessionID]
	a.mu.Unlock()
	created := false
	if resource == nil {
		var err error
		resource, err = a.buildAgentResource(ctx, descriptor, plan, admittedStore)
		if err != nil {
			return nil, err
		}
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			return nil, errors.Join(ErrAuthorityClosed, resource.closeResource(ctx))
		}
		if existing := a.resources[sessionID]; existing != nil {
			a.mu.Unlock()
			if err := resource.closeResource(ctx); err != nil {
				return nil, fmt.Errorf("close redundant session %s runtime: %w", sessionID, err)
			}
			resource = existing
		} else {
			a.resources[sessionID] = resource
			created = true
			a.mu.Unlock()
		}
	}
	if created {
		if err := resource.publishReady(ctx); err != nil {
			a.mu.Lock()
			if a.resources[sessionID] == resource {
				delete(a.resources, sessionID)
			}
			a.mu.Unlock()
			return nil, errors.Join(err, resource.closeResource(ctx))
		}
	}
	resource.mu.Lock()
	if resource.state != AgentResourceReady {
		resource.mu.Unlock()
		return nil, fmt.Errorf("session %s runtime is not ready", sessionID)
	}
	if ownerID != nil {
		resource.owners[*ownerID] = struct{}{}
		resource.ownerlessDisposition = agentResourceRemainAvailable
	}
	resource.signalLocked()
	resource.mu.Unlock()
	if a.options.background != nil {
		a.options.background.RetryTerminalEvents(sessionID.String())
	}
	return resource, nil
}

func (a *Authority) replaceResource(ctx context.Context, descriptor session.SessionDescriptor, plan *AgentRuntimePlan) (*agentResource, error) {
	sessionID := descriptor.SessionID()
	a.mu.Lock()
	existing := a.resources[sessionID]
	a.mu.Unlock()
	if existing != nil {
		if err := a.retireResourceForReplacement(ctx, existing); err != nil {
			return nil, err
		}
	}
	return a.openResource(ctx, descriptor, plan, nil, nil)
}

func (a *Authority) retireResourceForReplacement(ctx context.Context, resource *agentResource) error {
	for {
		resource.mu.Lock()
		switch resource.state {
		case AgentResourceClosed:
			resource.mu.Unlock()
			a.mu.Lock()
			if a.resources[resource.ref.SessionID()] == resource {
				delete(a.resources, resource.ref.SessionID())
			}
			a.mu.Unlock()
			return nil
		case AgentResourceDraining:
			changed := resource.changed
			resource.mu.Unlock()
			select {
			case <-changed:
				continue
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		case AgentResourceReady:
		default:
			state := resource.state
			resource.mu.Unlock()
			return fmt.Errorf(
				"session %s runtime generation %d cannot be replaced from state %d",
				resource.ref.SessionID(),
				resource.ref.Generation(),
				state,
			)
		}

		if resource.current != nil || resource.callbacks != 0 || resource.steps != 0 {
			changed := resource.changed
			resource.mu.Unlock()
			select {
			case <-changed:
				continue
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		}
		_, closeErr := a.closeAdmittedResourceLocked(ctx, resource)
		return closeErr
	}
}
