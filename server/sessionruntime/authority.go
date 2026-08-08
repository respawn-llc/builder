package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
	lifecycleCtx       context.Context
	lifecycleCancel    context.CancelFunc
	lifecycleWG        sync.WaitGroup
	nextExecution      ExecutionGeneration
	nextResource       runtimeids.ResourceGeneration
	byScope            map[runtimeids.ExecutionScopeID]*execution
	workflowExecutions map[string]map[runtimeids.WorkflowID]map[workflow.TaskID]map[workflow.CurrentNodeReferenceKey]*execution
	resources          map[runtimeids.SessionID]*agentResource
	gates              map[runtimeids.SessionID]*sessionAdmissionGate
	executionFinalized ExecutionFinalized
	promptFeed         ExecutionPromptFeed
	options            authorityRuntimeOptions
}

func NewAuthority(options AuthorityOptions) *Authority {
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	authority := &Authority{
		byScope:            make(map[runtimeids.ExecutionScopeID]*execution),
		workflowExecutions: make(map[string]map[runtimeids.WorkflowID]map[workflow.TaskID]map[workflow.CurrentNodeReferenceKey]*execution),
		resources:          make(map[runtimeids.SessionID]*agentResource),
		gates:              make(map[runtimeids.SessionID]*sessionAdmissionGate),
		lifecycleCtx:       lifecycleCtx,
		lifecycleCancel:    lifecycleCancel,
		executionFinalized: options.ExecutionFinalized,
		promptFeed:         options.PromptFeed,
		options:            newAuthorityRuntimeOptions(options),
	}
	if authority.options.background != nil {
		authority.options.background.SetEventHandler(authority.routeBackgroundEvent)
	}
	return authority
}

func (a *Authority) launchLifecycleTask(task func(context.Context)) bool {
	if a == nil || task == nil {
		return false
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return false
	}
	a.lifecycleWG.Add(1)
	ctx := a.lifecycleCtx
	a.mu.Unlock()
	go func() {
		defer a.lifecycleWG.Done()
		task(ctx)
	}()
	return true
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
	execution := a.workflowExecutionLocked(ref, key)
	if execution == nil {
		return nil, false
	}
	return executionHandle{execution: execution}, true
}

func (a *Authority) workflowExecutionLocked(ref WorkflowExecutionRef, key workflow.CurrentNodeReferenceKey) *execution {
	byProject := a.workflowExecutions[ref.ProjectID]
	if byProject == nil {
		return nil
	}
	byWorkflow := byProject[ref.WorkflowID]
	if byWorkflow == nil {
		return nil
	}
	byTask := byWorkflow[ref.CurrentNode.TaskID]
	if byTask == nil {
		return nil
	}
	return byTask[key]
}

func (a *Authority) addWorkflowExecutionLocked(ref WorkflowExecutionRef, key workflow.CurrentNodeReferenceKey, item *execution) {
	byProject := a.workflowExecutions[ref.ProjectID]
	if byProject == nil {
		byProject = make(map[runtimeids.WorkflowID]map[workflow.TaskID]map[workflow.CurrentNodeReferenceKey]*execution)
		a.workflowExecutions[ref.ProjectID] = byProject
	}
	byWorkflow := byProject[ref.WorkflowID]
	if byWorkflow == nil {
		byWorkflow = make(map[workflow.TaskID]map[workflow.CurrentNodeReferenceKey]*execution)
		byProject[ref.WorkflowID] = byWorkflow
	}
	byTask := byWorkflow[ref.CurrentNode.TaskID]
	if byTask == nil {
		byTask = make(map[workflow.CurrentNodeReferenceKey]*execution)
		byWorkflow[ref.CurrentNode.TaskID] = byTask
	}
	byTask[key] = item
}

func (a *Authority) beginWorkflowExecution(item *execution) {
	a.mu.Lock()
	if a.byScope[item.scope.ID()] != item {
		a.mu.Unlock()
		return
	}
	if item.phase != executionPhaseQueued {
		a.mu.Unlock()
		panic(fmt.Sprintf("workflow execution scope %s began from phase %d", item.scope.ID(), item.phase))
	}
	if item.runningPublish == nil {
		item.phase = executionPhaseRunning
	} else {
		item.phase = executionPhasePublishing
	}
	ref, workflowScoped := item.scope.Workflow()
	if !workflowScoped {
		a.mu.Unlock()
		panic(fmt.Sprintf("publish workflow execution start %s: scope is not workflow scoped", item.scope.ID()))
	}
	start := TaskExecution{Ref: ref, ScopeID: item.scope.ID()}
	if resource, agent := item.scope.Resource(); agent {
		start.Agent = &TaskAgentExecutionTarget{SessionID: resource.SessionID()}
	} else if item.script != nil {
		start.Script = &TaskScriptExecutionTarget{Path: item.script.Path}
	}
	if err := start.Validate(); err != nil {
		a.mu.Unlock()
		panic(fmt.Sprintf("publish workflow execution start %s: %v", item.scope.ID(), err))
	}
	a.mu.Unlock()
	item.publishStart(start, nil)
}

func (a *Authority) removeWorkflowExecutionLocked(ref WorkflowExecutionRef, key workflow.CurrentNodeReferenceKey, item *execution) {
	byProject := a.workflowExecutions[ref.ProjectID]
	if byProject == nil {
		return
	}
	byWorkflow := byProject[ref.WorkflowID]
	if byWorkflow == nil {
		return
	}
	byTask := byWorkflow[ref.CurrentNode.TaskID]
	if byTask == nil || byTask[key] != item {
		return
	}
	delete(byTask, key)
	if len(byTask) == 0 {
		delete(byWorkflow, ref.CurrentNode.TaskID)
	}
	if len(byWorkflow) == 0 {
		delete(byProject, ref.WorkflowID)
	}
	if len(byProject) == 0 {
		delete(a.workflowExecutions, ref.ProjectID)
	}
}

func (a *Authority) forEachWorkflowExecutionLocked(fn func(*execution)) {
	for _, byWorkflow := range a.workflowExecutions {
		for _, byTask := range byWorkflow {
			for _, byCurrentNode := range byTask {
				for _, execution := range byCurrentNode {
					fn(execution)
				}
			}
		}
	}
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

func (a *Authority) exactExecutions(handles []ExecutionHandle) ([]*execution, error) {
	executions := make([]*execution, 0, len(handles))
	seen := make(map[*execution]struct{}, len(handles))
	for _, handle := range handles {
		exact, ok := handle.(executionHandle)
		if !ok || exact.execution == nil {
			return nil, errors.New("execution handle does not belong to this authority")
		}
		execution := exact.execution
		if execution.authority != a {
			return nil, errors.New("execution handle does not belong to this authority")
		}
		if _, duplicate := seen[execution]; duplicate {
			continue
		}
		seen[execution] = struct{}{}
		executions = append(executions, execution)
	}
	sort.Slice(executions, func(i, j int) bool {
		return executions[i].scope.ID().String() < executions[j].scope.ID().String()
	})
	return executions, nil
}

func lockExactExecutions(executions []*execution) {
	for _, execution := range executions {
		execution.exactMu.Lock()
	}
}

func unlockExactExecutions(executions []*execution) {
	for index := len(executions) - 1; index >= 0; index-- {
		executions[index].exactMu.Unlock()
	}
}

func (a *Authority) exactExecutionsLiveLocked(executions []*execution) bool {
	for _, execution := range executions {
		if a.byScope[execution.scope.ID()] != execution {
			return false
		}
	}
	return true
}

func (a *Authority) exactExecutionsLive(executions []*execution) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.exactExecutionsLiveLocked(executions)
}

// WithExactExecutions linearizes an operation against retirement of only the
// selected execution handles. It deliberately does not retain the Authority
// mutex while operation runs.
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
	executions, err := a.exactExecutions(handles)
	if err != nil {
		return err
	}
	lockExactExecutions(executions)
	defer unlockExactExecutions(executions)
	if !a.exactExecutionsLive(executions) {
		return ErrExecutionNoLongerLive
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
	executions := make(map[runtimeids.ExecutionScopeID]ExecutionHandle)
	a.forEachWorkflowExecutionLocked(func(running *execution) {
		executions[running.scope.ID()] = executionHandle{execution: running}
	})
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
	lifecycleCancel := a.lifecycleCancel
	executions := make([]ExecutionHandle, 0, len(a.byScope))
	for _, running := range a.byScope {
		executions = append(executions, executionHandle{execution: running})
	}
	resources := make([]*agentResource, 0, len(a.resources))
	for _, resource := range a.resources {
		resources = append(resources, resource)
	}
	a.mu.Unlock()
	if lifecycleCancel != nil {
		lifecycleCancel()
	}

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
	a.lifecycleWG.Wait()
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
		if existing := a.workflowExecutionLocked(ref, workflowKey); existing != nil {
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
		authority:      a,
		scope:          scope,
		ctx:            runCtx,
		cancel:         cancel,
		done:           make(chan struct{}),
		started:        make(chan struct{}),
		runningPublish: req.RunningPublication,
		publishDone:    make(chan struct{}),
		disposition:    make(chan struct{}),
		prompts:        newExecutionPromptStore(a, scope, a.promptFeed),
		phase:          executionPhaseRunning,
	}
	if workflowRef != nil {
		reserved.script = &TaskScriptExecutionTarget{Path: req.Command.Path}
		reserved.phase = executionPhaseQueued
	}
	return reserved, nil
}

func (a *Authority) ConfirmWorkflowDisposition(scopeID runtimeids.ExecutionScopeID) error {
	if a == nil {
		return errors.New("session runtime authority is required")
	}
	if scopeID.IsZero() {
		return errors.New("workflow Exact Scope id is required")
	}
	a.mu.Lock()
	execution := a.byScope[scopeID]
	a.mu.Unlock()
	if execution == nil {
		return ErrExecutionNoLongerLive
	}
	if _, workflowScoped := execution.scope.Workflow(); !workflowScoped {
		return errors.New("execution scope is not workflow scoped")
	}
	execution.confirmDisposition()
	return nil
}
