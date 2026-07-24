package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"core/server/auth"
	"core/server/session"
	shelltool "core/server/tools/shell"
	"core/server/workflow"
	"core/shared/runtimeids"
)

var ErrAuthorityClosed = errors.New("session runtime authority is closed")
var ErrExecutionNoLongerLive = errors.New("exact execution scope is no longer live")

type ExecutionFinalized interface {
	ExecutionFinalized(ExecutionScope)
}

type ExecutionFinalizedFunc func(ExecutionScope)

func (f ExecutionFinalizedFunc) ExecutionFinalized(scope ExecutionScope) {
	if f != nil {
		f(scope)
	}
}

type AuthorityOptions struct {
	ExecutionFinalized ExecutionFinalized
	PersistenceRoot    string
	AuthManager        *auth.Manager
	Background         *shelltool.Manager
	StoreOptions       []session.StoreOption
	EventFeed          AgentResourceEventFeed
	ResourceLifecycle  AgentResourceLifecycle
	StepLifecycle      AgentResourceStepLifecycle
	PromptFeed         ExecutionPromptFeed
}

type Authority struct {
	mu                 sync.Mutex
	closed             bool
	nextExecution      ExecutionGeneration
	nextResource       runtimeids.ResourceGeneration
	byScope            map[runtimeids.ExecutionScopeID]*execution
	byWorkflow         map[workflow.CurrentNodeReferenceKey]*execution
	resources          map[runtimeids.SessionID]*agentResource
	gates              map[runtimeids.SessionID]*sessionAdmissionGate
	executionFinalized ExecutionFinalized
	promptFeed         ExecutionPromptFeed
	options            authorityRuntimeOptions
}

func NewAuthority(options AuthorityOptions) *Authority {
	authority := &Authority{
		byScope:            make(map[runtimeids.ExecutionScopeID]*execution),
		byWorkflow:         make(map[workflow.CurrentNodeReferenceKey]*execution),
		resources:          make(map[runtimeids.SessionID]*agentResource),
		gates:              make(map[runtimeids.SessionID]*sessionAdmissionGate),
		executionFinalized: options.ExecutionFinalized,
		promptFeed:         options.PromptFeed,
		options:            newAuthorityRuntimeOptions(options),
	}
	if authority.options.background != nil {
		authority.options.background.SetEventHandler(authority.routeBackgroundEvent)
	}
	return authority
}

func (a *Authority) nextGenerationsLocked() (ExecutionGeneration, runtimeids.ResourceGeneration) {
	a.nextExecution++
	a.nextResource++
	if a.nextExecution == 0 || a.nextResource == 0 {
		panic("session runtime generation overflow")
	}
	return a.nextExecution, a.nextResource
}

func (a *Authority) nextExecutionGenerationLocked() ExecutionGeneration {
	a.nextExecution++
	if a.nextExecution == 0 {
		panic("session runtime execution generation overflow")
	}
	return a.nextExecution
}

func (a *Authority) NewWorkflowExecutionLease(ref WorkflowExecutionRef) (WorkflowExecutionLease, error) {
	if a == nil {
		return WorkflowExecutionLease{}, errors.New("session runtime authority is required")
	}
	if err := ref.Validate(); err != nil {
		return WorkflowExecutionLease{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return WorkflowExecutionLease{}, ErrAuthorityClosed
	}
	return WorkflowExecutionLease{
		authority:           a,
		workflow:            ref,
		scopeID:             runtimeids.NewExecutionScopeID(),
		executionGeneration: a.nextExecutionGenerationLocked(),
		start:               make(chan struct{}),
		canceled:            make(chan struct{}),
		startOnce:           &sync.Once{},
		cancelOnce:          &sync.Once{},
	}, nil
}

func (a *Authority) validateWorkflowExecutionLeaseLocked(lease *WorkflowExecutionLease) (WorkflowExecutionRef, error) {
	if lease == nil {
		return WorkflowExecutionRef{}, errors.New("workflow execution lease is required")
	}
	if lease.authority != a {
		return WorkflowExecutionRef{}, errors.New("workflow execution lease belongs to another authority")
	}
	if lease.scopeID.IsZero() || lease.executionGeneration == 0 || lease.start == nil || lease.canceled == nil || lease.startOnce == nil || lease.cancelOnce == nil {
		return WorkflowExecutionRef{}, errors.New("workflow execution lease is invalid")
	}
	if err := lease.workflow.Validate(); err != nil {
		return WorkflowExecutionRef{}, err
	}
	return lease.workflow, nil
}

func (a *Authority) ExecutionByWorkflow(ref WorkflowExecutionRef) (ExecutionHandle, bool) {
	if a == nil {
		return nil, false
	}
	key, err := workflowExecutionKeyFor(ref)
	if err != nil {
		return nil, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	execution := a.byWorkflow[key]
	if execution == nil {
		return nil, false
	}
	return executionHandle{execution: execution}, true
}

func (a *Authority) ExecutionByScope(id runtimeids.ExecutionScopeID) (ExecutionHandle, bool) {
	if a == nil || id.IsZero() {
		return nil, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	execution, ok := a.byScope[id]
	if !ok {
		return nil, false
	}
	return executionHandle{execution: execution}, true
}

// WithExactExecutions linearizes an operation against retirement of the exact
// execution handles. The operation runs while the authority lock keeps those
// scopes registered as live.
func (a *Authority) WithExactExecutions(handles []ExecutionHandle, operation func() error) error {
	if a == nil {
		return errors.New("session runtime authority is required")
	}
	if len(handles) == 0 {
		return ErrExecutionNoLongerLive
	}
	if operation == nil {
		return errors.New("exact execution operation is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, handle := range handles {
		exact, ok := handle.(executionHandle)
		if !ok || exact.execution == nil {
			return errors.New("execution handle does not belong to this authority")
		}
		execution := exact.execution
		if execution.authority != a || a.byScope[execution.scope.ID()] != execution {
			return ErrExecutionNoLongerLive
		}
		if workflowRef, ok := execution.scope.Workflow(); ok {
			workflowKey, err := workflowExecutionKeyFor(workflowRef)
			if err != nil || a.byWorkflow[workflowKey] != execution {
				return ErrExecutionNoLongerLive
			}
		}
	}
	return operation()
}

func (a *Authority) SessionExecution(sessionID runtimeids.SessionID) (ExecutionHandle, bool) {
	if a == nil || sessionID.IsZero() {
		return nil, false
	}
	a.mu.Lock()
	resource := a.resources[sessionID]
	a.mu.Unlock()
	if resource == nil {
		return nil, false
	}
	return resource.currentExecution()
}

func (a *Authority) StopWorkflowExecutions(ctx context.Context) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	executions := make(map[runtimeids.ExecutionScopeID]ExecutionHandle, len(a.byWorkflow))
	for _, running := range a.byWorkflow {
		executions[running.scope.ID()] = executionHandle{execution: running}
	}
	a.mu.Unlock()
	var stopErrs []error
	for _, running := range executions {
		if err := running.Stop(ctx); err != nil {
			stopErrs = append(stopErrs, fmt.Errorf("stop workflow execution scope %s: %w", running.Scope().ID(), err))
		}
	}
	return errors.Join(stopErrs...)
}

func (a *Authority) Close(ctx context.Context) error {
	if a == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.mu.Lock()
	a.closed = true
	executions := make([]ExecutionHandle, 0, len(a.byScope))
	for _, running := range a.byScope {
		executions = append(executions, executionHandle{execution: running})
	}
	resources := make([]*agentResource, 0, len(a.resources))
	for _, resource := range a.resources {
		resources = append(resources, resource)
	}
	a.mu.Unlock()

	var closeErrs []error
	for _, running := range executions {
		if err := running.Stop(ctx); err != nil {
			closeErrs = append(closeErrs, fmt.Errorf("stop execution scope %s: %w", running.Scope().ID(), err))
		}
	}
	for _, running := range executions {
		if err := running.Close(ctx); err != nil {
			closeErrs = append(closeErrs, fmt.Errorf("join execution scope %s: %w", running.Scope().ID(), err))
		}
	}
	for _, resource := range resources {
		if err := resource.closeResource(ctx); err != nil {
			closeErrs = append(closeErrs, fmt.Errorf(
				"close agent resource %s generation %d: %w",
				resource.ref.SessionID(),
				resource.ref.Generation(),
				err,
			))
		}
	}
	return errors.Join(closeErrs...)
}

func (a *Authority) reserveScriptExecutionLocked(req ScriptExecutionRequest) (*execution, error) {
	if a.closed {
		return nil, ErrAuthorityClosed
	}
	var workflowRef *WorkflowExecutionRef
	var scopeID runtimeids.ExecutionScopeID
	var executionGeneration ExecutionGeneration
	if req.Workflow != nil {
		ref, err := a.validateWorkflowExecutionLeaseLocked(req.Workflow)
		if err != nil {
			return nil, err
		}
		workflowRef = &ref
		scopeID = req.Workflow.scopeID
		executionGeneration = req.Workflow.executionGeneration
		workflowKey, err := workflowExecutionKeyFor(ref)
		if err != nil {
			return nil, err
		}
		if existing := a.byWorkflow[workflowKey]; existing != nil {
			return nil, fmt.Errorf(
				"workflow current node %v is already live",
				ref.CurrentNode,
			)
		}
	}
	if scopeID.IsZero() {
		scopeID = runtimeids.NewExecutionScopeID()
		executionGeneration = a.nextExecutionGenerationLocked()
	}
	a.nextResource++
	resourceGeneration := a.nextResource
	if resourceGeneration == 0 {
		panic("session runtime resource generation overflow")
	}
	scope := newScriptExecutionScope(
		scopeID,
		executionGeneration,
		resourceGeneration,
		workflowRef,
	)
	runCtx, cancel := context.WithCancel(context.Background())
	reserved := &execution{
		authority: a,
		scope:     scope,
		ctx:       runCtx,
		cancel:    cancel,
		done:      make(chan struct{}),
		prompts:   newExecutionPromptStore(scope, a.promptFeed),
	}
	if workflowRef != nil {
		reserved.script = &TaskScriptExecutionTarget{Path: req.Command.Path}
	}
	return reserved, nil
}
