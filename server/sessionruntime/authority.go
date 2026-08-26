package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"core/server/auth"
	"core/server/runtime"
	"core/server/session"
	shelltool "core/server/tools/shell"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/invariant"
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
	Debug              bool
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
	invariantPolicy    invariant.Policy
	workflowTaskReads  atomic.Pointer[workflowTaskExecutionReadSnapshot]
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
		invariantPolicy:    invariant.OperationalPolicy(options.Debug),
	}
	if authority.options.background != nil {
		authority.options.background.SetEventHandler(authority.routeBackgroundEvent)
	}
	authority.workflowTaskReads.Store(&workflowTaskExecutionReadSnapshot{
		executions: map[workflow.TaskID]TaskExecutionSnapshot{},
	})
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
	go a.runLifecycleTask(ctx, task)
	return true
}

func (a *Authority) runLifecycleTask(ctx context.Context, task func(context.Context)) {
	defer a.lifecycleWG.Done()
	task(ctx)
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

func (a *Authority) ExecutionByCurrentNode(
	projectID string,
	workflowID runtimeids.WorkflowID,
	currentNode workflow.CurrentNodeReference,
) (ExecutionHandle, bool) {
	if a == nil || strings.TrimSpace(projectID) == "" || workflowID.IsZero() {
		return nil, false
	}
	key, err := currentNode.Key()
	if err != nil {
		return nil, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	execution := a.workflowExecutionByCurrentNodeLocked(WorkflowExecutionRef{
		ProjectID: projectID, WorkflowID: workflowID, CurrentNode: currentNode,
	}, key)
	if execution == nil {
		return nil, false
	}
	return executionHandle{execution: execution}, true
}

// HasLiveWorkflowTaskExecution checks the live execution authority for a
// safety-critical Task mutation precondition. Read projections must use the
// stale-tolerant snapshot APIs instead.
func (a *Authority) HasLiveWorkflowTaskExecution(taskID workflow.TaskID) (bool, error) {
	if a == nil {
		return false, errors.New("session runtime authority is required")
	}
	if strings.TrimSpace(string(taskID)) == "" {
		return false, errors.New("workflow task id is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.workflowTaskExecutionsLocked(taskID)) != 0, nil
}

func (a *Authority) workflowTaskExecutionsLocked(taskID workflow.TaskID) []*execution {
	executions := make([]*execution, 0)
	for _, execution := range a.byScope {
		ref, workflowScoped := execution.scope.Workflow()
		if workflowScoped && ref.CurrentNode.TaskID == taskID {
			executions = append(executions, execution)
		}
	}
	sort.Slice(executions, func(i, j int) bool {
		return executions[i].scope.ID().String() < executions[j].scope.ID().String()
	})
	return executions
}

func (a *Authority) workflowExecutionLocked(ref WorkflowExecutionRef, key workflow.CurrentNodeReferenceKey) *execution {
	return a.workflowExecutionByCurrentNodeLocked(ref, key)
}

func (a *Authority) workflowExecutionByCurrentNodeLocked(ref WorkflowExecutionRef, key workflow.CurrentNodeReferenceKey) *execution {
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

func (a *Authority) removeWorkflowExecutionLocked(ref WorkflowExecutionRef, key workflow.CurrentNodeReferenceKey, item *execution) bool {
	byProject := a.workflowExecutions[ref.ProjectID]
	if byProject == nil {
		return false
	}
	byWorkflow := byProject[ref.WorkflowID]
	if byWorkflow == nil {
		return false
	}
	byTask := byWorkflow[ref.CurrentNode.TaskID]
	if byTask == nil || byTask[key] != item {
		return false
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
	return true
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

func (a *Authority) CompleteAgentStep(
	ctx context.Context,
	scopeID runtimeids.ExecutionScopeID,
	runID runtimeids.RunID,
	stepID runtimeids.StepID,
	operation func() (workflowruntime.CompletionResult, error),
) (workflowruntime.CompletionResult, error) {
	handle, ok := a.ExecutionByScope(scopeID)
	if !ok {
		return workflowruntime.CompletionResult{}, ErrExecutionNoLongerLive
	}
	var result workflowruntime.CompletionResult
	err := a.WithExactExecutions([]ExecutionHandle{handle}, func() error {
		exact := handle.(executionHandle).execution
		if exact.scope.Kind() != ExecutionScopeAgent ||
			exact.phase != executionPhaseRunning ||
			exact.resource == nil {
			return ErrExecutionNoLongerLive
		}
		err := exact.resource.withEngine(
			ctx,
			exact.resource.ref,
			func(_ context.Context, engine *runtime.Engine) error {
				var completionErr error
				result, completionErr = engine.ApplyWorkflowAgentCompletion(scopeID, runID, stepID, operation)
				return completionErr
			},
		)
		return err
	})
	return result, err
}

func (a *Authority) CompleteFinalizingScript(
	scopeID runtimeids.ExecutionScopeID,
	operation func() (workflowruntime.CompletionResult, error),
) (workflowruntime.CompletionResult, error) {
	handle, ok := a.ExecutionByScope(scopeID)
	if !ok {
		return workflowruntime.CompletionResult{}, ErrExecutionNoLongerLive
	}
	var result workflowruntime.CompletionResult
	err := a.WithExactExecutions([]ExecutionHandle{handle}, func() error {
		exact := handle.(executionHandle).execution
		if exact.scope.Kind() != ExecutionScopeScript ||
			exact.phase != executionPhaseFinalizing {
			return ErrExecutionNoLongerLive
		}
		var err error
		result, err = operation()
		return err
	})
	return result, err
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
	scopeID := runtimeids.NewExecutionScopeID()
	executionGeneration := a.nextExecutionGenerationLocked()
	a.nextResource++
	resourceGeneration := a.nextResource
	if resourceGeneration == 0 {
		panic("session runtime resource generation overflow")
	}
	scope := newScriptExecutionScope(
		scopeID,
		executionGeneration,
		resourceGeneration,
		nil,
	)
	runCtx, cancel := context.WithCancel(context.Background())
	reserved := &execution{
		authority: a,
		scope:     scope,
		ctx:       runCtx,
		cancel:    cancel,
		done:      make(chan struct{}),
		prompts:   newExecutionPromptStore(a, scope, a.promptFeed),
		phase:     executionPhaseRunning,
	}
	return reserved, nil
}
