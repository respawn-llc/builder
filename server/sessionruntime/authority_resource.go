package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/server/runlog"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/tools"
	"core/shared/runtimeids"
)

type AgentResourceState uint8

const (
	AgentResourceBuilding AgentResourceState = iota + 1
	AgentResourceReady
	AgentResourceDraining
	AgentResourceClosed
)

type AgentResourceDescriptor struct {
	Ref        runtimeids.SessionResourceRef
	State      AgentResourceState
	OwnerCount int
}

type AgentResourceEventFeed func(AgentResourceDescriptor, runtime.Event)

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

type RuntimeAttachment struct {
	authority *Authority
	resource  runtimeids.SessionResourceRef
	ownerID   string
}

func (a RuntimeAttachment) Resource() runtimeids.SessionResourceRef {
	return a.resource
}

func (a RuntimeAttachment) Snapshot() (AgentResourceDescriptor, bool) {
	if a.authority == nil || a.resource.Validate() != nil {
		return AgentResourceDescriptor{}, false
	}
	a.authority.mu.Lock()
	resource := a.authority.resources[a.resource.SessionID()]
	a.authority.mu.Unlock()
	if resource == nil {
		return AgentResourceDescriptor{}, false
	}
	resource.mu.Lock()
	defer resource.mu.Unlock()
	if resource.ref != a.resource || resource.state == AgentResourceClosed {
		return AgentResourceDescriptor{}, false
	}
	return resource.descriptorLocked(), true
}

func (a RuntimeAttachment) Release(ctx context.Context, policy RuntimeReleasePolicy) (RuntimeReleaseResult, error) {
	if a.authority == nil {
		return RuntimeReleaseResult{}, errors.New("runtime attachment is uninitialized")
	}
	return a.authority.releaseRuntimeAttachment(ctx, a, policy)
}

type ResourceRetention struct {
	resource *agentResource
	once     sync.Once
}

func (r *ResourceRetention) Close() error {
	if r == nil || r.resource == nil {
		return nil
	}
	r.once.Do(r.resource.releasePin)
	return nil
}

type AgentRuntimeBridge struct {
	authority *Authority
	resource  runtimeids.SessionResourceRef
}

func (b AgentRuntimeBridge) WithEngine(ctx context.Context, callback func(context.Context, *runtime.Engine) error) error {
	if b.authority == nil {
		return errors.New("agent runtime bridge is uninitialized")
	}
	return b.authority.withAgentResource(ctx, b.resource, callback)
}

type AgentRunner func(context.Context, ExecutionScope, AgentRuntimeBridge) error

type ExecutionAskHandler func(context.Context, ExecutionScope, tools.AskQuestionRequest) (tools.AskQuestionResponse, error)

type AgentExecutionRequest struct {
	Descriptor session.SessionDescriptor
	Runtime    *AgentRuntimePlan
	Workflow   *WorkflowExecutionRef
	Resource   AgentResourceSelection
	Ask        ExecutionAskHandler
	Runner     AgentRunner
}

type agentResource struct {
	authority *Authority
	ref       runtimeids.SessionResourceRef
	ctx       context.Context
	cancel    context.CancelFunc

	mu          sync.Mutex
	changed     chan struct{}
	state       AgentResourceState
	owners      map[string]struct{}
	store       *session.Store
	engine      *runtime.Engine
	eventBridge *runtimewire.EventBridge
	logger      *runlog.RunLogger
	localTools  *runtimewire.LocalToolRegistryBinding
	askBroker   *tools.AskQuestionBroker
	askScope    *runtimeids.ExecutionScopeID
	close       func() error
	current     *execution
	pins        int
	callbacks   int
}

func (r *agentResource) descriptorLocked() AgentResourceDescriptor {
	return AgentResourceDescriptor{
		Ref:        r.ref,
		State:      r.state,
		OwnerCount: len(r.owners),
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
	if r.state != AgentResourceReady {
		return fmt.Errorf("agent resource %s generation %d is not ready", r.ref.SessionID(), r.ref.Generation())
	}
	r.pins++
	r.signalLocked()
	return nil
}

func (r *agentResource) releasePin() {
	r.mu.Lock()
	if r.pins <= 0 {
		panic(fmt.Sprintf("agent resource %s generation %d pin underflow", r.ref.SessionID(), r.ref.Generation()))
	}
	r.pins--
	r.signalLocked()
	r.mu.Unlock()
}

func (r *agentResource) withEngine(ctx context.Context, ref runtimeids.SessionResourceRef, callback func(context.Context, *runtime.Engine) error) error {
	if callback == nil {
		return errors.New("agent resource callback is required")
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	if r.ref != ref || r.state != AgentResourceReady || r.engine == nil {
		r.mu.Unlock()
		return fmt.Errorf("agent resource %s generation %d is unavailable", ref.SessionID(), ref.Generation())
	}
	engine := r.engine
	r.callbacks++
	r.signalLocked()
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.callbacks--
		r.signalLocked()
		r.mu.Unlock()
	}()
	return callback(ctx, engine)
}

func (r *agentResource) withStore(ctx context.Context, callback func(context.Context, *session.Store) error) error {
	r.mu.Lock()
	if r.state != AgentResourceReady || r.store == nil {
		r.mu.Unlock()
		return fmt.Errorf("agent resource %s generation %d is unavailable", r.ref.SessionID(), r.ref.Generation())
	}
	store := r.store
	r.callbacks++
	r.signalLocked()
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.callbacks--
		r.signalLocked()
		r.mu.Unlock()
	}()
	return callback(ctx, store)
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
	for r.pins != 0 || r.callbacks != 0 || r.current != nil {
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
		return nil
	}
	return closeEngine()
}

func (r *agentResource) StepBegan(ctx context.Context, snapshot runtime.StepLifecycleSnapshot) error {
	return r.stepLifecycle(ctx, snapshot, true)
}

func (r *agentResource) StepEnded(ctx context.Context, snapshot runtime.StepLifecycleSnapshot) error {
	return r.stepLifecycle(ctx, snapshot, false)
}

func (r *agentResource) stepLifecycle(ctx context.Context, snapshot runtime.StepLifecycleSnapshot, began bool) error {
	r.mu.Lock()
	current := r.current
	descriptor := r.descriptorLocked()
	r.mu.Unlock()
	if current == nil || r.authority.options.stepLifecycle == nil {
		return nil
	}
	if began {
		return r.authority.options.stepLifecycle.StepBegan(ctx, descriptor, current.scope, snapshot)
	}
	return r.authority.options.stepLifecycle.StepEnded(ctx, descriptor, current.scope, snapshot)
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
	descriptor, err := session.NewOpenSessionDescriptor(request.SessionID)
	if err != nil {
		return RuntimeAttachment{}, err
	}
	resource, err := a.openResource(ctx, descriptor, nil, &ownerID)
	if err != nil {
		return RuntimeAttachment{}, err
	}
	return RuntimeAttachment{authority: a, resource: resource.ref, ownerID: ownerID}, nil
}

func (a *Authority) releaseRuntimeAttachment(ctx context.Context, attachment RuntimeAttachment, policy RuntimeReleasePolicy) (RuntimeReleaseResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := context.Cause(ctx); err != nil {
		return RuntimeReleaseResult{}, err
	}
	if err := attachment.resource.Validate(); err != nil {
		return RuntimeReleaseResult{}, err
	}
	if strings.TrimSpace(attachment.ownerID) == "" {
		return RuntimeReleaseResult{}, errors.New("runtime attachment owner id is required")
	}
	switch policy {
	case RuntimeReleaseDetach, RuntimeReleaseCloseIfIdle, RuntimeReleaseClose:
	default:
		return RuntimeReleaseResult{}, errors.New("runtime release policy is required")
	}
	sessionID := attachment.resource.SessionID()
	gate := a.gateFor(sessionID)
	gate.mu.Lock()
	defer gate.mu.Unlock()

	a.mu.Lock()
	resource := a.resources[sessionID]
	a.mu.Unlock()
	if resource == nil || resource.ref != attachment.resource {
		return RuntimeReleaseResult{Released: true}, nil
	}
	resource.mu.Lock()
	if resource.state == AgentResourceClosed || resource.state == AgentResourceDraining {
		resource.mu.Unlock()
		return RuntimeReleaseResult{Released: true}, nil
	}
	if _, owns := resource.owners[attachment.ownerID]; !owns {
		resource.mu.Unlock()
		return RuntimeReleaseResult{Released: true}, nil
	}
	delete(resource.owners, attachment.ownerID)
	resource.signalLocked()
	if policy == RuntimeReleaseDetach {
		resource.mu.Unlock()
		return RuntimeReleaseResult{Released: true}, nil
	}
	if policy == RuntimeReleaseCloseIfIdle {
		active := len(resource.owners) != 0 ||
			resource.current != nil ||
			resource.pins != 0 ||
			resource.callbacks != 0 ||
			(resource.engine != nil && resource.engine.HasQueuedUserWork())
		if active {
			resource.mu.Unlock()
			return RuntimeReleaseResult{Active: true}, nil
		}
	}
	resource.state = AgentResourceDraining
	resource.signalLocked()
	resource.mu.Unlock()

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
	return RuntimeReleaseResult{Released: closed}, closeErr
}

func (a *Authority) closeExecutionResource(ctx context.Context, resource *agentResource) error {
	if resource == nil {
		return nil
	}
	sessionID := resource.ref.SessionID()
	gate := a.gateFor(sessionID)
	gate.mu.Lock()
	defer gate.mu.Unlock()

	resource.mu.Lock()
	if resource.state != AgentResourceReady ||
		len(resource.owners) != 0 ||
		resource.current != nil ||
		resource.pins != 0 ||
		resource.callbacks != 0 {
		resource.mu.Unlock()
		return nil
	}
	if resource.engine != nil {
		resource.engine.FailQueuedUserMessages(runtime.QueuedUserMessageFailureClosing)
	}
	resource.state = AgentResourceDraining
	resource.signalLocked()
	resource.mu.Unlock()

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
	return closeErr
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
	if request.Workflow != nil {
		if err := request.Workflow.Validate(); err != nil {
			return nil, err
		}
	}
	gate := a.gateFor(sessionID)
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if len(gate.blocks) != 0 {
		return nil, errors.New("session starts are blocked")
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
	if request.Workflow != nil && a.byWorkflow[*request.Workflow] != nil {
		a.mu.Unlock()
		return nil, fmt.Errorf("workflow execution %q generation %d is already live", request.Workflow.RunID, request.Workflow.Generation)
	}
	resource.mu.Lock()
	if resource.current != nil {
		resource.mu.Unlock()
		a.mu.Unlock()
		return nil, fmt.Errorf("session %s already has an agent execution", sessionID)
	}
	if err := resource.pinLocked(); err != nil {
		resource.mu.Unlock()
		a.mu.Unlock()
		return nil, err
	}
	executionGeneration := a.nextExecutionGenerationLocked()
	scope := newAgentExecutionScope(runtimeids.NewExecutionScopeID(), executionGeneration, resource.ref, request.Workflow)
	correlation, err := runtimeids.NewExecutionCorrelation(scope.ID(), resource.ref.Generation())
	if err != nil {
		resource.pins--
		resource.signalLocked()
		resource.mu.Unlock()
		a.mu.Unlock()
		panic(fmt.Sprintf("new agent execution correlation: %v", err))
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
		prompts:       newExecutionPromptStore(scope, a.promptFeed),
		closeResource: closeResource,
	}
	if resource.askBroker != nil {
		scopeID := scope.ID()
		askHandler := request.Ask
		if askHandler == nil {
			askHandler = func(ctx context.Context, scope ExecutionScope, req tools.AskQuestionRequest) (tools.AskQuestionResponse, error) {
				return a.AwaitPromptResponse(ctx, scope.ID(), req)
			}
		}
		resource.askBroker.SetAskHandler(func(req tools.AskQuestionRequest) (tools.AskQuestionResponse, error) {
			return askHandler(execution.ctx, execution.scope, req)
		})
		resource.askScope = &scopeID
	}
	resource.current = execution
	resource.signalLocked()
	resource.mu.Unlock()
	a.byScope[scope.ID()] = execution
	if request.Workflow != nil {
		a.byWorkflow[*request.Workflow] = execution
	}
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

func (a *Authority) withAgentResource(ctx context.Context, ref runtimeids.SessionResourceRef, callback func(context.Context, *runtime.Engine) error) error {
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
		return fmt.Errorf("agent resource %s generation %d is unavailable", ref.SessionID(), ref.Generation())
	}
	return resource.withEngine(ctx, ref, callback)
}

func (a *Authority) RetainResource(ref runtimeids.SessionResourceRef) (*ResourceRetention, error) {
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
			return nil, false, fmt.Errorf("session %s has no registered runtime", sessionID)
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
			a.mu.Unlock()
		}
	}
	resource.mu.Lock()
	if resource.state != AgentResourceReady {
		resource.mu.Unlock()
		return nil, fmt.Errorf("session %s runtime is not ready", sessionID)
	}
	if ownerID != nil {
		resource.owners[*ownerID] = struct{}{}
	}
	resource.signalLocked()
	resource.mu.Unlock()
	return resource, nil
}

func (a *Authority) replaceResource(ctx context.Context, descriptor session.SessionDescriptor, plan *AgentRuntimePlan) (*agentResource, error) {
	sessionID := descriptor.SessionID()
	a.mu.Lock()
	existing := a.resources[sessionID]
	a.mu.Unlock()
	if existing != nil {
		existing.mu.Lock()
		reject := existing.state != AgentResourceReady || existing.current != nil || existing.pins != 0 || existing.callbacks != 0 || (existing.engine != nil && existing.engine.HasQueuedUserWork())
		if !reject {
			existing.state = AgentResourceDraining
			existing.signalLocked()
		}
		existing.mu.Unlock()
		if reject {
			return nil, fmt.Errorf("session %s runtime cannot be replaced while active", sessionID)
		}
		if err := existing.closeResource(ctx); err != nil {
			return nil, err
		}
		a.mu.Lock()
		if a.resources[sessionID] == existing {
			delete(a.resources, sessionID)
		}
		a.mu.Unlock()
	}
	return a.openResource(ctx, descriptor, plan, nil)
}
