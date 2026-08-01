package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"core/server/runlog"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/tools"
	shelltool "core/server/tools/shell"
	"core/server/workflow"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type AgentResourceState uint8

type agentResourceOwnerlessDisposition uint8

var ErrSessionRunActive = errors.New("session has an active run")
var ErrSessionStartsBlocked = errors.New("session starts are blocked")

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

type AgentResourceEventFeed func(AgentResourceDescriptor, runtime.Event)

type AgentResourceRetainer func() (io.Closer, error)

type AgentResourceLifecycle interface {
	ResourceReady(context.Context, AgentResourceDescriptor, *runtime.Engine, AgentResourceRetainer) error
	ResourceDraining(context.Context, AgentResourceDescriptor) error
}

type AgentResourceStepLifecycle interface {
	StepBegan(context.Context, AgentResourceDescriptor, ExecutionScope, runtime.StepLifecycleSnapshot) error
	StepEnded(context.Context, AgentResourceDescriptor, ExecutionScope, runtime.StepLifecycleSnapshot) error
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

type ExecutionAskHandler func(context.Context, ExecutionScope, tools.AskQuestionRequest) (tools.AskQuestionResponse, error)

type AgentExecutionRequest struct {
	Descriptor              session.SessionDescriptor
	Runtime                 *AgentRuntimePlan
	Workflow                *WorkflowExecutionLease
	Resource                AgentResourceSelection
	Ask                     ExecutionAskHandler
	CommandLease            ResourceExecutionLease
	DeferCommandLeaseCommit bool
	Runner                  AgentRunner
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
	commandAdmitted      bool
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
		err = errors.Join(err, r.releaseCallback())
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
	if err := r.admitCommandResource(ctx); err != nil {
		return err
	}
	if r.authority.options.resourceLifecycle == nil {
		return nil
	}
	return r.authority.options.resourceLifecycle.ResourceReady(ctx, descriptor, engine, func() (io.Closer, error) {
		return r.authority.retainResource(r.ref)
	})
}

func (r *agentResource) admitCommandResource(ctx context.Context) error {
	lifecycle := r.authority.options.commandLifecycle
	if lifecycle == nil {
		return nil
	}
	if err := lifecycle.AdmitResource(ctx, r.ref); err != nil {
		return err
	}
	r.mu.Lock()
	r.commandAdmitted = true
	r.signalLocked()
	r.mu.Unlock()
	return nil
}

func (r *agentResource) AcquireLifecycleTask(ctx context.Context) (runtime.LifecycleTaskLease, error) {
	if r == nil || r.authority == nil || r.authority.options.commandExecution == nil {
		return nil, nil
	}
	lease, err := r.authority.options.commandExecution.AcquireExecution(ctx, r.ref)
	if err != nil {
		return nil, err
	}
	return lease, nil
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
	commandAdmitted := r.commandAdmitted
	r.mu.Unlock()
	cleanupCtx := ctx
	cleanupCancel := func() {}
	if ctx.Err() != nil {
		cleanupCtx, cleanupCancel = context.WithTimeout(context.Background(), 5*time.Second)
	}
	defer cleanupCancel()
	var interruptErr error
	if engine != nil {
		interruptErr = engine.Interrupt()
	}
	var lifecycleErr error
	if notifyDraining {
		lifecycleErr = r.authority.options.resourceLifecycle.ResourceDraining(cleanupCtx, descriptor)
	}
	var waitErr error
	r.mu.Lock()
	for r.pins != 0 || r.callbacks != 0 || r.steps != 0 || r.current != nil {
		changed := r.changed
		r.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			waitErr = context.Cause(ctx)
			cleanupCtx, cleanupCancel = context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			select {
			case <-changed:
			case <-cleanupCtx.Done():
				waitErr = errors.Join(waitErr, context.Cause(cleanupCtx))
			}
		}
		r.mu.Lock()
		if waitErr != nil {
			break
		}
	}
	closeEngine := r.close
	r.state = AgentResourceClosed
	r.signalLocked()
	r.mu.Unlock()
	var commandErr error
	if commandAdmitted && r.authority.options.commandLifecycle != nil {
		commandErr = r.authority.options.commandLifecycle.CloseResource(cleanupCtx, descriptor.Ref)
	}
	if closeEngine == nil {
		return errors.Join(lifecycleErr, interruptErr, commandErr, waitErr)
	}
	return errors.Join(lifecycleErr, interruptErr, commandErr, waitErr, closeEngine())
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
	current := r.current
	descriptor := r.descriptorLocked()
	r.mu.Unlock()
	if current == nil || r.authority.options.stepLifecycle == nil {
		return nil
	}
	return r.authority.options.stepLifecycle.StepBegan(ctx, descriptor, current.scope, snapshot)
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
	current := r.current
	descriptor := r.descriptorLocked()
	r.mu.Unlock()
	var publishErr error
	if current != nil && r.authority.options.stepLifecycle != nil {
		publishErr = r.authority.options.stepLifecycle.StepEnded(ctx, descriptor, current.scope, snapshot)
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
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if len(gate.blocks) != 0 {
		return RuntimeAttachment{}, sessionStartsBlockedError(request.SessionID)
	}
	descriptor, err := session.NewOpenSessionDescriptor(request.SessionID)
	if err != nil {
		return RuntimeAttachment{}, err
	}
	resource, err := a.openResource(ctx, descriptor, request.Runtime, &ownerID)
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
	gate.mu.Lock()
	defer gate.mu.Unlock()

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
	gate.mu.Lock()
	defer gate.mu.Unlock()

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

func (a *Authority) StartAgentExecution(ctx context.Context, request AgentExecutionRequest) (handle ExecutionHandle, err error) {
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
	gate.mu.Lock()
	defer gate.mu.Unlock()
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
	var workflowKey workflow.CurrentNodeReferenceKey
	var workflowRef *WorkflowExecutionRef
	var scopeID runtimeids.ExecutionScopeID
	var executionGeneration ExecutionGeneration
	if request.Workflow != nil {
		ref, leaseErr := a.validateWorkflowExecutionLeaseLocked(request.Workflow)
		if leaseErr != nil {
			a.mu.Unlock()
			return nil, leaseErr
		}
		workflowRef = &ref
		scopeID = request.Workflow.scopeID
		executionGeneration = request.Workflow.executionGeneration
		workflowKey, err = workflowExecutionKeyFor(ref)
		if err != nil {
			a.mu.Unlock()
			return nil, err
		}
		if a.workflowExecutionLocked(ref, workflowKey) != nil {
			a.mu.Unlock()
			return nil, fmt.Errorf("workflow current node %v is already live", ref.CurrentNode)
		}
	}
	resource.mu.Lock()
	if resource.current != nil {
		resource.mu.Unlock()
		a.mu.Unlock()
		return nil, errors.Join(ErrSessionRunActive, fmt.Errorf("session %s already has an agent execution", sessionID))
	}
	commandLease := request.CommandLease
	if commandLease == nil && a.options.commandExecution != nil {
		commandLease, err = a.options.commandExecution.AcquireExecution(ctx, resource.ref)
		if err != nil {
			resource.mu.Unlock()
			a.mu.Unlock()
			return nil, err
		}
	}
	commandLeaseTransferred := false
	cleanupCommandLease := request.CommandLease == nil && commandLease != nil
	defer func() {
		if !cleanupCommandLease || commandLeaseTransferred {
			return
		}
		cleanupErr := commandLease.Abort(err)
		cleanupErr = errors.Join(cleanupErr, commandLease.Release())
		err = errors.Join(err, cleanupErr)
	}()
	var workflowBinding *runtime.CurrentNodeExecutionBinding
	closeWorkflowBinding := func() error {
		if workflowBinding == nil {
			return nil
		}
		closeErr := workflowBinding.Close()
		workflowBinding = nil
		return closeErr
	}
	if workflowRef != nil &&
		request.Runtime != nil &&
		request.Runtime.options.CurrentNodeExecution != nil {
		workflowConfig := request.Runtime.options.CurrentNodeExecution
		if workflowConfig.ScopeID != scopeID {
			resource.mu.Unlock()
			a.mu.Unlock()
			return nil, fmt.Errorf(
				"workflow execution scope %s does not match runtime config scope %s",
				scopeID,
				workflowConfig.ScopeID,
			)
		}
		if !workflowConfig.Instructions.CurrentNode.Equal(workflowRef.CurrentNode) {
			resource.mu.Unlock()
			a.mu.Unlock()
			return nil, errors.New("workflow runtime config does not match execution current node")
		}
		workflowBinding, err = resource.engine.BindCurrentNodeExecution(workflowConfig)
		if err != nil {
			resource.mu.Unlock()
			a.mu.Unlock()
			return nil, fmt.Errorf("bind workflow current node execution: %w", err)
		}
	}
	if err := resource.pinLocked(); err != nil {
		bindingErr := closeWorkflowBinding()
		resource.mu.Unlock()
		a.mu.Unlock()
		return nil, errors.Join(err, bindingErr)
	}
	if scopeID.IsZero() {
		scopeID = runtimeids.NewExecutionScopeID()
		executionGeneration = a.nextExecutionGenerationLocked()
	}
	scope := newAgentExecutionScope(scopeID, executionGeneration, resource.ref, workflowRef)
	correlation, err := runtimeids.NewExecutionCorrelation(scope.ID(), resource.ref.Generation())
	if err != nil {
		bindingErr := closeWorkflowBinding()
		resource.pins--
		resource.signalLocked()
		resource.mu.Unlock()
		a.mu.Unlock()
		panic(fmt.Sprintf("new agent execution correlation: %v", errors.Join(err, bindingErr)))
	}
	if resource.localTools != nil {
		if err := resource.localTools.BindExecutionCorrelation(&correlation); err != nil {
			bindingErr := closeWorkflowBinding()
			resource.pins--
			resource.signalLocked()
			resource.mu.Unlock()
			a.mu.Unlock()
			return nil, errors.Join(fmt.Errorf("bind agent execution correlation: %w", err), bindingErr)
		}
	}
	runCtx, cancel := context.WithCancel(resource.ctx)
	execution := &execution{
		authority:     a,
		resource:      resource,
		scope:         scope,
		workflow:      workflowBinding,
		ctx:           runCtx,
		cancel:        cancel,
		done:          make(chan struct{}),
		prompts:       newExecutionPromptStore(scope, a.promptFeed),
		closeResource: closeResource,
		phase:         executionPhaseRunning,
		commandLease:  commandLease,
	}
	if workflowRef != nil {
		execution.phase = executionPhaseQueued
	}
	if resource.askBroker != nil {
		scopeID := scope.ID()
		askHandler := request.Ask
		if askHandler == nil {
			askHandler = func(ctx context.Context, scope ExecutionScope, req tools.AskQuestionRequest) (tools.AskQuestionResponse, error) {
				return a.AwaitPromptResponse(ctx, scope.ID(), req)
			}
		}
		resource.askBroker.SetAskHandler(func(ctx context.Context, req tools.AskQuestionRequest) (tools.AskQuestionResponse, error) {
			return askHandler(ctx, execution.scope, req)
		})
		resource.askScope = &scopeID
	}
	resource.current = execution
	resource.signalLocked()
	resource.mu.Unlock()
	a.byScope[scope.ID()] = execution
	if workflowRef != nil {
		a.addWorkflowExecutionLocked(*workflowRef, workflowKey, execution)
	}
	a.mu.Unlock()
	commandLeaseTransferred = true

	go func() {
		if commandLease != nil {
			if waitErr := commandLease.Wait(execution.ctx); waitErr != nil {
				_ = commandLease.Abort(waitErr)
				execution.finish(ExecutionResult{}, waitErr, nil)
				return
			}
		}
		if request.Workflow != nil {
			if waitErr := request.Workflow.wait(execution.ctx); waitErr != nil {
				execution.finish(ExecutionResult{}, waitErr, nil)
				return
			}
			a.beginWorkflowExecution(execution)
		}
		if mutation, ok := commandLease.(ResourceExecutionMutation); ok {
			binding := resource.engine.BindExecutionMutation(runtime.ExecutionMutation(mutation.OrderedMutation))
			defer resource.engine.ClearExecutionMutationIf(binding)
		}
		runErr := request.Runner(execution.ctx, execution.scope, AgentRuntimeBridge{
			authority: a,
			resource:  resource.ref,
		})
		execution.finish(ExecutionResult{}, runErr, nil)
	}()
	if commandLease != nil && !request.DeferCommandLeaseCommit {
		if commitErr := commandLease.Commit(); commitErr != nil {
			_ = commandLease.Abort(commitErr)
		}
	}
	return executionHandle{execution: execution}, nil
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

type SubmittedTurnOutcome struct {
	Err       error
	Continues bool
}

func RunSubmittedAgentExecution(
	executionCtx context.Context,
	bridge AgentRuntimeBridge,
	run func(context.Context, *runtime.Engine) error,
	report func(SubmittedTurnOutcome),
) error {
	if run == nil || report == nil {
		return errors.New("submitted agent execution callbacks are required")
	}
	callbackRan := false
	runErr := bridge.WithEngine(executionCtx, func(_ context.Context, engine *runtime.Engine) error {
		callbackRan = true
		releaseAutoDrain := engine.RetainQueuedUserAutoDrain()
		defer releaseAutoDrain()
		releaseGoalContinuation := engine.RetainGoalLoopContinuation()
		defer releaseGoalContinuation()
		if err := run(executionCtx, engine); err != nil {
			report(SubmittedTurnOutcome{Err: err})
			return err
		}
		continues := engine.GoalLoopRunning() ||
			engine.GoalLoopContinuationPending() ||
			engine.HasPendingUserMessages()
		report(SubmittedTurnOutcome{Continues: continues})
		if !continues {
			return nil
		}
		for engine.HasPendingUserMessages() {
			if _, err := engine.SubmitQueuedUserMessages(executionCtx); err != nil {
				return err
			}
		}
		if engine.GoalLoopRunning() {
			return engine.WaitForGoalLoop(executionCtx)
		}
		return nil
	})
	if !callbackRan {
		report(SubmittedTurnOutcome{Err: runErr})
	}
	return runErr
}

func (a *Authority) WithOrderedExecution(ctx context.Context, scopeID runtimeids.ExecutionScopeID, callback func() error) error {
	if a == nil {
		return errors.New("session runtime authority is required")
	}
	if scopeID.IsZero() {
		return errors.New("execution scope id is required")
	}
	if callback == nil {
		return errors.New("ordered execution callback is required")
	}
	handle, ok := a.ExecutionByScope(scopeID)
	if !ok {
		return ErrExecutionNoLongerLive
	}
	resource, ok := handle.Scope().Resource()
	if !ok {
		return callback()
	}
	if a.options.agentOrderedMutation != nil {
		return a.options.agentOrderedMutation(ctx, handle.Scope(), func(turn runtime.OrderedMutationTurn) error {
			return turn.Apply(callback)
		})
	}
	return a.WithRuntime(ctx, resource, func(_ context.Context, engine *runtime.Engine) error {
		return engine.ApplyExecutionMutation(ctx, func(turn runtime.OrderedMutationTurn) error {
			return turn.Apply(callback)
		})
	})
}

func (a *Authority) WithExecutionMutation(
	ctx context.Context,
	scopeID runtimeids.ExecutionScopeID,
	callback func(runtime.OrderedMutationTurn) error,
) error {
	if a == nil {
		return errors.New("session runtime authority is required")
	}
	if scopeID.IsZero() {
		return errors.New("execution scope id is required")
	}
	if callback == nil {
		return errors.New("execution mutation callback is required")
	}
	handle, ok := a.ExecutionByScope(scopeID)
	if !ok || handle.Scope().Kind() != ExecutionScopeAgent {
		return ErrExecutionNoLongerLive
	}
	resource, ok := handle.Scope().Resource()
	if !ok {
		return ErrExecutionNoLongerLive
	}
	return a.WithRuntime(ctx, resource, func(callbackCtx context.Context, engine *runtime.Engine) error {
		if a.options.agentOrderedMutation != nil {
			return a.options.agentOrderedMutation(callbackCtx, handle.Scope(), callback)
		}
		return engine.ApplyExecutionMutation(callbackCtx, callback)
	})
}

func (a *Authority) CurrentResourceRef(ctx context.Context, sessionID runtimeids.SessionID) (runtimeids.SessionResourceRef, error) {
	if a == nil {
		return runtimeids.SessionResourceRef{}, errors.New("session runtime authority is required")
	}
	if err := context.Cause(ctx); err != nil {
		return runtimeids.SessionResourceRef{}, err
	}
	if sessionID.IsZero() {
		return runtimeids.SessionResourceRef{}, errors.New("session id is required")
	}
	a.mu.Lock()
	resource := a.resources[sessionID]
	a.mu.Unlock()
	if resource == nil {
		return runtimeids.SessionResourceRef{}, errors.Join(
			serverapi.ErrRuntimeUnavailable,
			fmt.Errorf("session %s has no active runtime available", sessionID),
		)
	}
	resource.mu.Lock()
	defer resource.mu.Unlock()
	if resource.rejectsNewUseLocked() {
		return runtimeids.SessionResourceRef{}, errors.Join(
			serverapi.ErrRuntimeUnavailable,
			fmt.Errorf("session %s runtime generation %d is unavailable", sessionID, resource.ref.Generation()),
		)
	}
	return resource.ref, nil
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
		resource, err := a.openResource(ctx, descriptor, plan, nil)
		return resource, true, err
	case ReplaceAgentResource:
		resource, err := a.replaceResource(ctx, descriptor, plan)
		return resource, true, err
	default:
		return nil, false, fmt.Errorf("unsupported agent resource selection %T", selection)
	}
}

func (a *Authority) openResource(ctx context.Context, descriptor session.SessionDescriptor, plan *AgentRuntimePlan, ownerID *string) (*agentResource, error) {
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
		resource, err = a.buildAgentResource(ctx, descriptor, plan)
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
	return a.openResource(ctx, descriptor, plan, nil)
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
		engine := resource.engine
		if engine != nil && engine.HasQueuedUserWork() {
			resource.mu.Unlock()
			if err := engine.DrainQueuedUserMessagesBeforeClose(ctx); err != nil {
				return fmt.Errorf(
					"drain session %s runtime generation %d before replacement: %w",
					resource.ref.SessionID(),
					resource.ref.Generation(),
					err,
				)
			}
			continue
		}
		_, closeErr := a.closeAdmittedResourceLocked(ctx, resource)
		return closeErr
	}
}
