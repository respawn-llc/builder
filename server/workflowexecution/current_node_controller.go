package workflowexecution

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/server/sessionruntime"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

const (
	ReasonCurrentNodeStartupRecovery workflow.CurrentNodeInterruptionReason = "workflow_startup_recovery"
	reasonProtocolViolationCap       workflow.CurrentNodeInterruptionReason = "workflow_protocol_violation_cap"
)

// CurrentNodeRunner starts a lease that has already been admitted under the
// controller mutation permit. The runner owns slow launch preparation; the
// controller owns its gate and live-scope registration.
type CurrentNodeRunner interface {
	StartCurrentNode(context.Context, workflow.CurrentNodeReference, sessionruntime.WorkflowExecutionLease, workflowruntime.Controller) error
}

type CurrentNodeControllerConfig struct {
	AutomaticConcurrency int
}

// CurrentNodeAutomaticIntent is volatile automatic work. It has a Current Node
// natural reference rather than a replacement execution identity.
type CurrentNodeAutomaticIntent struct {
	CurrentNode workflow.CurrentNodeReference
}

type CurrentNodeAdmissionGateSnapshot struct {
	CurrentNode workflow.CurrentNodeReference
	ScopeID     runtimeids.ExecutionScopeID
	Automatic   bool
}

type CurrentNodeLiveScopeSnapshot struct {
	CurrentNode workflow.CurrentNodeReference
	ScopeID     runtimeids.ExecutionScopeID
	Automatic   bool
}

type CurrentNodeHeldIntentSnapshot struct {
	CurrentNode workflow.CurrentNodeReference
	SourceScope runtimeids.ExecutionScopeID
}

// CurrentNodeExecutionSnapshot is immutable live controller state. Durable
// Current Node scheduling rows are intentionally not inferred from this view.
type CurrentNodeExecutionSnapshot struct {
	AutomaticIntents []CurrentNodeAutomaticIntent
	HeldIntents      []CurrentNodeHeldIntentSnapshot
	Gates            []CurrentNodeAdmissionGateSnapshot
	LiveScopes       []CurrentNodeLiveScopeSnapshot
}

type currentNodeAdmissionGate struct {
	reference workflow.CurrentNodeReference
	lease     sessionruntime.WorkflowExecutionLease
	automatic bool
}

type currentNodeLiveScope struct {
	reference workflow.CurrentNodeReference
	lease     sessionruntime.WorkflowExecutionLease
	automatic bool
}

type CurrentNodeController struct {
	store interface {
		AdmitCurrentNode(context.Context, workflow.CurrentNodeReference) error
		ResumeCurrentNode(context.Context, workflow.CurrentNodeReference) error
		InterruptAdmittedCurrentNode(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error
		InterruptCurrentNode(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error
		RecoverAdmittedCurrentNodes(context.Context, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) (int64, error)
		ResolveIdleExecutableCurrentNode(context.Context, workflowstore.IdleCurrentNodeSelector) (workflow.CurrentNode, error)
		CompleteCurrentNode(context.Context, workflowstore.CurrentNodeCompletionRequest) (workflowstore.CurrentNodeCompletionResult, error)
		ValidateCurrentNodeSessionBinding(context.Context, runtimeids.SessionID, workflow.CurrentNodeReference) error
	}
	runner    CurrentNodeRunner
	authority *sessionruntime.Authority
	permit    *MutationPermit

	automaticConcurrency int
	workerContext        context.Context
	workerCancel         context.CancelFunc
	workerWake           chan struct{}
	workerWG             sync.WaitGroup
	admissionWG          sync.WaitGroup

	mu                sync.Mutex
	closed            bool
	gates             map[workflow.CurrentNodeReferenceKey]currentNodeAdmissionGate
	live              map[runtimeids.ExecutionScopeID]currentNodeLiveScope
	liveByNode        map[workflow.CurrentNodeReferenceKey]runtimeids.ExecutionScopeID
	stopping          map[runtimeids.ExecutionScopeID]struct{}
	completed         map[runtimeids.ExecutionScopeID]struct{}
	violations        map[runtimeids.ExecutionScopeID]int64
	heldIntents       map[runtimeids.ExecutionScopeID][]CurrentNodeAutomaticIntent
	automaticQueue    []CurrentNodeAutomaticIntent
	queued            map[workflow.CurrentNodeReferenceKey]struct{}
	lastAutomaticTask *workflow.TaskID
}

func NewCurrentNodeController(
	store interface {
		AdmitCurrentNode(context.Context, workflow.CurrentNodeReference) error
		ResumeCurrentNode(context.Context, workflow.CurrentNodeReference) error
		InterruptAdmittedCurrentNode(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error
		InterruptCurrentNode(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error
		RecoverAdmittedCurrentNodes(context.Context, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) (int64, error)
		ResolveIdleExecutableCurrentNode(context.Context, workflowstore.IdleCurrentNodeSelector) (workflow.CurrentNode, error)
		CompleteCurrentNode(context.Context, workflowstore.CurrentNodeCompletionRequest) (workflowstore.CurrentNodeCompletionResult, error)
		ValidateCurrentNodeSessionBinding(context.Context, runtimeids.SessionID, workflow.CurrentNodeReference) error
	},
	runner CurrentNodeRunner,
	authority *sessionruntime.Authority,
	permit *MutationPermit,
	cfg CurrentNodeControllerConfig,
) (*CurrentNodeController, error) {
	if store == nil {
		return nil, errors.New("current node workflow store is required")
	}
	if runner == nil {
		return nil, errors.New("current node workflow runner is required")
	}
	if authority == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if permit == nil {
		return nil, errors.New("workflow mutation permit is required")
	}
	if cfg.AutomaticConcurrency <= 0 {
		return nil, errors.New("automatic workflow concurrency must be positive")
	}
	workerContext, workerCancel := context.WithCancel(context.Background())
	controller := &CurrentNodeController{
		store:                store,
		runner:               runner,
		authority:            authority,
		permit:               permit,
		automaticConcurrency: cfg.AutomaticConcurrency,
		workerContext:        workerContext,
		workerCancel:         workerCancel,
		workerWake:           make(chan struct{}, 1),
		gates:                make(map[workflow.CurrentNodeReferenceKey]currentNodeAdmissionGate),
		live:                 make(map[runtimeids.ExecutionScopeID]currentNodeLiveScope),
		liveByNode:           make(map[workflow.CurrentNodeReferenceKey]runtimeids.ExecutionScopeID),
		stopping:             make(map[runtimeids.ExecutionScopeID]struct{}),
		completed:            make(map[runtimeids.ExecutionScopeID]struct{}),
		violations:           make(map[runtimeids.ExecutionScopeID]int64),
		heldIntents:          make(map[runtimeids.ExecutionScopeID][]CurrentNodeAutomaticIntent),
		queued:               make(map[workflow.CurrentNodeReferenceKey]struct{}),
	}
	controller.workerWG.Add(1)
	go controller.runAutomaticAdmissions()
	return controller, nil
}

func (c *CurrentNodeController) Recover(ctx context.Context) (int64, error) {
	if c == nil {
		return 0, errors.New("current node workflow controller is required")
	}
	return RunMutation(ctx, c.permit, func(ctx context.Context) (int64, error) {
		return c.store.RecoverAdmittedCurrentNodes(ctx, ReasonCurrentNodeStartupRecovery, workflow.CurrentNodeInterruptionDetail{})
	})
}

func (c *CurrentNodeController) Start(ctx context.Context, reference workflow.CurrentNodeReference) error {
	return c.admit(ctx, reference, false, false)
}

func (c *CurrentNodeController) Resume(ctx context.Context, reference workflow.CurrentNodeReference) error {
	return c.admit(ctx, reference, true, false)
}

// EnsureTaskQuiescent rejects Task-wide state replacement while the
// controller owns live, admitted, or automatic work for the Task. Callers
// hold the shared mutation permit while invoking it and applying the durable
// replacement.
func (c *CurrentNodeController) EnsureTaskQuiescent(taskID workflow.TaskID) error {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	if taskID == "" {
		return errors.New("workflow task id is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, gate := range c.gates {
		if gate.reference.TaskID == taskID {
			return ErrTaskExecutionNotQuiescent
		}
	}
	for _, live := range c.live {
		if live.reference.TaskID == taskID {
			return ErrTaskExecutionNotQuiescent
		}
	}
	for _, intent := range c.automaticQueue {
		if intent.CurrentNode.TaskID == taskID {
			return ErrTaskExecutionNotQuiescent
		}
	}
	for _, intents := range c.heldIntents {
		for _, intent := range intents {
			if intent.CurrentNode.TaskID == taskID {
				return ErrTaskExecutionNotQuiescent
			}
		}
	}
	return nil
}

func (c *CurrentNodeController) admit(ctx context.Context, reference workflow.CurrentNodeReference, resume bool, automatic bool) (err error) {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	if err := reference.Validate(); err != nil {
		return err
	}
	key, err := reference.Key()
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("current node workflow controller is closed")
	}
	c.admissionWG.Add(1)
	c.mu.Unlock()
	defer c.admissionWG.Done()

	var lease sessionruntime.WorkflowExecutionLease
	if err := c.permit.Run(ctx, func(ctx context.Context) error {
		next, err := c.authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{CurrentNode: reference})
		if err != nil {
			return err
		}
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			next.Cancel()
			return errors.New("current node workflow controller is closed")
		}
		if _, exists := c.gates[key]; exists {
			c.mu.Unlock()
			next.Cancel()
			return fmt.Errorf("current node %v is already being admitted", reference)
		}
		if _, exists := c.liveByNode[key]; exists {
			c.mu.Unlock()
			next.Cancel()
			return fmt.Errorf("current node %v already has a live execution scope", reference)
		}
		// The gate precedes the durable restart marker, so any conflicting
		// lifecycle mutation sees admission before slow runner preparation.
		c.gates[key] = currentNodeAdmissionGate{reference: reference, lease: next, automatic: automatic}
		c.mu.Unlock()

		if resume {
			if err := c.store.ResumeCurrentNode(ctx, reference); err != nil {
				c.removeGate(key, next.ScopeID())
				next.Cancel()
				return err
			}
		}
		if err := c.store.AdmitCurrentNode(ctx, reference); err != nil {
			c.removeGate(key, next.ScopeID())
			next.Cancel()
			return err
		}
		lease = next
		return nil
	}); err != nil {
		return err
	}
	if err := c.runner.StartCurrentNode(ctx, reference, lease, c); err != nil {
		return c.compensateAdmission(context.WithoutCancel(ctx), reference, key, lease, err)
	}
	if err := c.permit.Run(ctx, func(context.Context) error {
		handle, ok := c.authority.ExecutionByScope(lease.ScopeID())
		if !ok {
			return errors.New("current node runner returned without its exact live scope")
		}
		scopeRef, ok := handle.Scope().Workflow()
		if !ok || !scopeRef.CurrentNode.Equal(reference) {
			return errors.New("current node runner started a mismatched workflow scope")
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		gate, exists := c.gates[key]
		if !exists || gate.lease.ScopeID() != lease.ScopeID() {
			return errors.New("current node admission gate was replaced before live scope registration")
		}
		if c.closed {
			return errors.New("current node workflow controller closed during admission")
		}
		delete(c.gates, key)
		c.live[lease.ScopeID()] = currentNodeLiveScope{reference: reference, lease: lease, automatic: gate.automatic}
		c.liveByNode[key] = lease.ScopeID()
		lease.Release()
		return nil
	}); err != nil {
		return c.compensateAdmission(context.WithoutCancel(ctx), reference, key, lease, err)
	}
	return nil
}

// Interrupt stops the exact live workflow scope selected by the caller. A
// Task-wide interrupt also drains controller-owned automatic work for that
// Task so a successor cannot start after the interrupt returns.
func (c *CurrentNodeController) Interrupt(ctx context.Context, selector InterruptSelector) error {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	if err := selector.Validate(); err != nil {
		return err
	}
	var (
		handles        []sessionruntime.ExecutionHandle
		liveReferences []workflow.CurrentNodeReference
		drained        []workflow.CurrentNodeReference
		drainedGates   []currentNodeAdmissionGate
	)
	if err := c.permit.Run(ctx, func(ctx context.Context) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		for scopeID, live := range c.live {
			if live.reference.TaskID != selector.TaskID {
				continue
			}
			handle, exists := c.authority.ExecutionByScope(scopeID)
			if !exists {
				continue
			}
			if selector.SessionID != nil {
				resource, agent := handle.Scope().Resource()
				if !agent || resource.SessionID() != *selector.SessionID {
					continue
				}
			}
			handles = append(handles, handle)
			liveReferences = append(liveReferences, live.reference)
			c.stopping[scopeID] = struct{}{}
		}
		if len(handles) == 0 {
			return ErrNoInterruptibleExecution
		}
		if selector.SessionID != nil {
			return nil
		}
		for key, gate := range c.gates {
			if gate.reference.TaskID != selector.TaskID {
				continue
			}
			delete(c.gates, key)
			drainedGates = append(drainedGates, gate)
			drained = append(drained, gate.reference)
		}
		queue := c.automaticQueue[:0]
		for _, intent := range c.automaticQueue {
			if intent.CurrentNode.TaskID == selector.TaskID {
				key, err := intent.CurrentNode.Key()
				if err != nil {
					return err
				}
				delete(c.queued, key)
				drained = append(drained, intent.CurrentNode)
				continue
			}
			queue = append(queue, intent)
		}
		c.automaticQueue = queue
		for sourceScope, intents := range c.heldIntents {
			kept := intents[:0]
			for _, intent := range intents {
				if intent.CurrentNode.TaskID == selector.TaskID {
					drained = append(drained, intent.CurrentNode)
					continue
				}
				kept = append(kept, intent)
			}
			if len(kept) == 0 {
				delete(c.heldIntents, sourceScope)
			} else {
				c.heldIntents[sourceScope] = kept
			}
		}
		return interruptCurrentNodeReferences(ctx, c.store.InterruptCurrentNode, drained, workflow.CurrentNodeInterruptionReason("user_interrupted"))
	}); err != nil {
		return err
	}
	for _, gate := range drainedGates {
		gate.lease.Cancel()
	}
	for _, handle := range handles {
		handle.RequestStop()
	}
	var waitErrs []error
	for _, handle := range handles {
		if _, err := handle.Wait(ctx); err != nil && !errors.Is(err, context.Canceled) {
			waitErrs = append(waitErrs, err)
		}
	}
	if len(waitErrs) != 0 {
		return errors.Join(waitErrs...)
	}
	return c.permit.Run(ctx, func(ctx context.Context) error {
		if err := interruptCurrentNodeReferences(ctx, c.store.InterruptCurrentNode, liveReferences, workflow.CurrentNodeInterruptionReason("user_interrupted")); err != nil {
			return err
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, live := range c.live {
			if live.reference.TaskID == selector.TaskID && selector.SessionID == nil {
				return errors.New("workflow task interrupt left a live execution scope")
			}
		}
		return nil
	})
}

func interruptCurrentNodeReferences(
	ctx context.Context,
	interrupt func(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error,
	references []workflow.CurrentNodeReference,
	reason workflow.CurrentNodeInterruptionReason,
) error {
	seen := make(map[workflow.CurrentNodeReferenceKey]struct{}, len(references))
	for _, reference := range references {
		key, err := reference.Key()
		if err != nil {
			return err
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		err = interrupt(ctx, reference, reason, workflow.CurrentNodeInterruptionDetail{Code: string(reason)})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}

func (c *CurrentNodeController) removeGate(key workflow.CurrentNodeReferenceKey, scopeID runtimeids.ExecutionScopeID) {
	c.mu.Lock()
	if gate, exists := c.gates[key]; exists && gate.lease.ScopeID() == scopeID {
		delete(c.gates, key)
	}
	c.mu.Unlock()
}

func (c *CurrentNodeController) compensateAdmission(ctx context.Context, reference workflow.CurrentNodeReference, key workflow.CurrentNodeReferenceKey, lease sessionruntime.WorkflowExecutionLease, cause error) error {
	c.removeGate(key, lease.ScopeID())
	c.mu.Lock()
	delete(c.live, lease.ScopeID())
	if current, exists := c.liveByNode[key]; exists && current == lease.ScopeID() {
		delete(c.liveByNode, key)
	}
	c.mu.Unlock()
	lease.Cancel()
	interruptErr := c.permit.Run(ctx, func(ctx context.Context) error {
		return c.store.InterruptAdmittedCurrentNode(ctx, reference, "workflow_runtime_start_failed", workflow.CurrentNodeInterruptionDetail{
			Code:   "workflow_runtime_start_failed",
			Fields: map[string]string{"error": cause.Error()},
		})
	})
	return errors.Join(cause, interruptErr)
}

func (c *CurrentNodeController) CompleteCurrentNode(ctx context.Context, req workflowruntime.CompletionRequest) (workflowruntime.CompletionResult, error) {
	_, err := c.completeLiveCurrentNode(ctx, req)
	if err != nil {
		return workflowruntime.CompletionResult{}, err
	}
	return workflowruntime.CompletionResult{TransitionID: workflow.TransitionID(req.TransitionID), State: "applied"}, nil
}

// CompleteSessionCurrentNode completes the one exact live agent scope for a
// Session. It is the agent-facing completion entrypoint; a Session ID is
// sufficient because Exact Scope identity is resolved only from Authority live
// state, never from durable workflow rows.
func (c *CurrentNodeController) CompleteSessionCurrentNode(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	transitionID string,
	outputValues map[string]string,
	commentary string,
) (workflowstore.CurrentNodeCompletionResult, error) {
	if c == nil {
		return workflowstore.CurrentNodeCompletionResult{}, errors.New("current node workflow controller is required")
	}
	if sessionID.IsZero() {
		return workflowstore.CurrentNodeCompletionResult{}, errors.New("session id is required")
	}
	c.mu.Lock()
	var scopeID runtimeids.ExecutionScopeID
	for candidateScopeID, live := range c.live {
		handle, exists := c.authority.ExecutionByScope(candidateScopeID)
		if !exists {
			continue
		}
		resource, agent := handle.Scope().Resource()
		if !agent || resource.SessionID() != sessionID {
			continue
		}
		if !scopeID.IsZero() {
			c.mu.Unlock()
			return workflowstore.CurrentNodeCompletionResult{}, workflowstore.ErrCurrentNodeCompletionSelectorAmbiguous
		}
		scopeID = candidateScopeID
		_ = live
	}
	c.mu.Unlock()
	if scopeID.IsZero() {
		return workflowstore.CurrentNodeCompletionResult{}, sessionruntime.ErrExecutionNoLongerLive
	}
	return c.completeLiveCurrentNode(ctx, workflowruntime.CompletionRequest{
		ScopeID:      scopeID,
		TransitionID: transitionID,
		OutputValues: outputValues,
		Commentary:   commentary,
	})
}

// AnswerWorkflowQuestion delivers an answer only after resolving one exact
// live Authority prompt and proving its Session still owns the same Current
// Node in durable Task state. Prompt state itself remains volatile.
func (c *CurrentNodeController) AnswerWorkflowQuestion(
	ctx context.Context,
	taskID workflow.TaskID,
	askID string,
	response askquestion.AskQuestionResponse,
	submitErr error,
) error {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	if strings.TrimSpace(string(taskID)) == "" {
		return errors.New("workflow task id is required")
	}
	askID = strings.TrimSpace(askID)
	if askID == "" {
		return errors.New("workflow ask id is required")
	}
	if strings.TrimSpace(response.RequestID) != askID {
		return errors.New("workflow question response does not match ask id")
	}
	return c.permit.Run(ctx, func(ctx context.Context) error {
		resolution, err := c.authority.ResolvePendingWorkflowPrompt(taskID, askID)
		if err != nil {
			return err
		}
		c.mu.Lock()
		live, isLive := c.live[resolution.ScopeID]
		c.mu.Unlock()
		if !isLive || !live.reference.Equal(resolution.CurrentNode) {
			return serverapi.ErrPromptNotFound
		}
		if err := c.store.ValidateCurrentNodeSessionBinding(ctx, resolution.SessionID, resolution.CurrentNode); err != nil {
			if errors.Is(err, workflowstore.ErrSessionNotCurrentWorkflowNode) {
				return serverapi.ErrPromptNotFound
			}
			return err
		}
		return c.authority.SubmitPromptResponseForScope(resolution.ScopeID, response, submitErr)
	})
}

func (c *CurrentNodeController) completeLiveCurrentNode(ctx context.Context, req workflowruntime.CompletionRequest) (workflowstore.CurrentNodeCompletionResult, error) {
	lease, err := c.liveLease(req.ScopeID)
	if err != nil {
		return workflowstore.CurrentNodeCompletionResult{}, err
	}
	handle, ok := c.authority.ExecutionByScope(req.ScopeID)
	if !ok {
		return workflowstore.CurrentNodeCompletionResult{}, sessionruntime.ErrExecutionNoLongerLive
	}
	var completed workflowstore.CurrentNodeCompletionResult
	err = c.authority.WithExactExecutions([]sessionruntime.ExecutionHandle{handle}, func() error {
		return c.permit.Run(ctx, func(ctx context.Context) error {
			var completionErr error
			completed, completionErr = c.store.CompleteCurrentNode(ctx, workflowstore.CurrentNodeCompletionRequest{
				Source:       lease.Workflow().CurrentNode,
				TransitionID: req.TransitionID,
				OutputValues: req.OutputValues,
				Commentary:   req.Commentary,
			})
			if completionErr != nil {
				return completionErr
			}
			intents, intentErr := currentNodeAutomaticIntents(completed.AutomaticIntents)
			if intentErr != nil {
				return intentErr
			}
			c.mu.Lock()
			c.completed[req.ScopeID] = struct{}{}
			c.heldIntents[req.ScopeID] = intents
			c.mu.Unlock()
			return nil
		})
	})
	if err != nil {
		return workflowstore.CurrentNodeCompletionResult{}, err
	}
	return completed, nil
}

// CompleteIdleCurrentNode applies a forced completion only after the
// controller has established that it owns no active, admitted, queued, or
// retirement-held work for the Task. Unlike a live completion there is no
// predecessor scope, so successor Automatic Intents are enqueued immediately
// after the aggregate mutation commits.
func (c *CurrentNodeController) CompleteIdleCurrentNode(
	ctx context.Context,
	selector workflowstore.IdleCurrentNodeSelector,
	transitionID string,
	outputValues map[string]string,
	commentary string,
) (workflowstore.CurrentNodeCompletionResult, error) {
	if c == nil {
		return workflowstore.CurrentNodeCompletionResult{}, errors.New("current node workflow controller is required")
	}
	completed, err := RunMutation(ctx, c.permit, func(ctx context.Context) (workflowstore.CurrentNodeCompletionResult, error) {
		source, err := c.store.ResolveIdleExecutableCurrentNode(ctx, selector)
		if err != nil {
			return workflowstore.CurrentNodeCompletionResult{}, err
		}
		if err := c.EnsureTaskQuiescent(source.Reference.TaskID); err != nil {
			return workflowstore.CurrentNodeCompletionResult{}, err
		}
		return c.store.CompleteCurrentNode(ctx, workflowstore.CurrentNodeCompletionRequest{
			Source:       source.Reference,
			TransitionID: transitionID,
			OutputValues: outputValues,
			Commentary:   commentary,
		})
	})
	if err != nil {
		return workflowstore.CurrentNodeCompletionResult{}, err
	}
	intents, err := currentNodeAutomaticIntents(completed.AutomaticIntents)
	if err != nil {
		return workflowstore.CurrentNodeCompletionResult{}, err
	}
	c.enqueueAutomaticIntents(intents)
	return completed, nil
}

func (c *CurrentNodeController) RecordProtocolViolation(ctx context.Context, req workflowruntime.ViolationRequest) (workflowruntime.ViolationResult, error) {
	if req.ScopeID.IsZero() {
		return workflowruntime.ViolationResult{}, errors.New("workflow exact execution scope id is required")
	}
	if req.MaxCount <= 0 {
		return workflowruntime.ViolationResult{}, errors.New("workflow protocol violation cap must be positive")
	}
	if _, err := c.liveLease(req.ScopeID); err != nil {
		return workflowruntime.ViolationResult{}, err
	}
	c.mu.Lock()
	c.violations[req.ScopeID]++
	count := c.violations[req.ScopeID]
	c.mu.Unlock()
	interrupted := count >= int64(req.MaxCount)
	if interrupted {
		cause := errors.New("workflow protocol violation budget exhausted")
		if req.Detail != "" {
			cause = errors.New(req.Detail)
		}
		if err := c.FailCurrentNodeScope(ctx, req.ScopeID, reasonProtocolViolationCap, cause); err != nil {
			return workflowruntime.ViolationResult{}, err
		}
	}
	return workflowruntime.ViolationResult{Count: count, Interrupted: interrupted}, nil
}

func (c *CurrentNodeController) ResetProtocolViolationBudget(_ context.Context, req workflowruntime.ViolationResetRequest) error {
	if _, err := c.liveLease(req.ScopeID); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.violations, req.ScopeID)
	c.mu.Unlock()
	return nil
}

func (c *CurrentNodeController) ObserveCurrentNodeCompletion(_ context.Context, req workflowruntime.CompletionObservationRequest) (workflowruntime.CompletionObservationResult, error) {
	if req.ScopeID.IsZero() {
		return workflowruntime.CompletionObservationResult{}, errors.New("workflow exact execution scope id is required")
	}
	c.mu.Lock()
	_, completed := c.completed[req.ScopeID]
	c.mu.Unlock()
	return workflowruntime.CompletionObservationResult{Completed: completed}, nil
}

func (c *CurrentNodeController) FailCurrentNodeScope(
	ctx context.Context,
	scopeID runtimeids.ExecutionScopeID,
	reason workflow.CurrentNodeInterruptionReason,
	cause error,
) error {
	lease, err := c.liveLease(scopeID)
	if err != nil {
		return err
	}
	detail := workflow.CurrentNodeInterruptionDetail{Code: string(reason)}
	if cause != nil {
		detail.Fields = map[string]string{"error": cause.Error()}
	}
	if err := c.permit.Run(ctx, func(ctx context.Context) error {
		return c.store.InterruptAdmittedCurrentNode(ctx, lease.Workflow().CurrentNode, reason, detail)
	}); err != nil {
		return err
	}
	if handle, live := c.authority.ExecutionByScope(scopeID); live {
		handle.RequestStop()
	}
	return nil
}

func (c *CurrentNodeController) ExecutionFinalized(scope sessionruntime.ExecutionScope) {
	ref, ok := scope.Workflow()
	if !ok || c == nil {
		return
	}
	key, err := ref.CurrentNode.Key()
	if err != nil {
		panic(fmt.Sprintf("workflow execution finalized invalid current node: %v", err))
	}
	c.mu.Lock()
	live, isLive := c.live[scope.ID()]
	delete(c.live, scope.ID())
	if current, exists := c.liveByNode[key]; exists && current == scope.ID() {
		delete(c.liveByNode, key)
	}
	delete(c.violations, scope.ID())
	delete(c.stopping, scope.ID())
	_, completed := c.completed[scope.ID()]
	delete(c.completed, scope.ID())
	intents := append([]CurrentNodeAutomaticIntent(nil), c.heldIntents[scope.ID()]...)
	delete(c.heldIntents, scope.ID())
	if gate, gated := c.gates[key]; gated && gate.lease.ScopeID() == scope.ID() {
		delete(c.gates, key)
	}
	closed := c.closed
	c.mu.Unlock()
	if !isLive || !live.lease.Workflow().CurrentNode.Equal(ref.CurrentNode) || !completed || closed {
		return
	}
	// A successor becomes eligible only after the source scope has retired.
	// Queueing happens after retirement rather than by directly starting it in
	// the Authority callback, which keeps capacity, affinity, and cancellation
	// under one controller-owned protocol.
	c.enqueueAutomaticIntents(intents)
}

func (c *CurrentNodeController) Snapshot() CurrentNodeExecutionSnapshot {
	if c == nil {
		return CurrentNodeExecutionSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot := CurrentNodeExecutionSnapshot{
		AutomaticIntents: append([]CurrentNodeAutomaticIntent(nil), c.automaticQueue...),
		Gates:            make([]CurrentNodeAdmissionGateSnapshot, 0, len(c.gates)),
		LiveScopes:       make([]CurrentNodeLiveScopeSnapshot, 0, len(c.live)),
	}
	for _, gate := range c.gates {
		snapshot.Gates = append(snapshot.Gates, CurrentNodeAdmissionGateSnapshot{
			CurrentNode: gate.reference,
			ScopeID:     gate.lease.ScopeID(),
			Automatic:   gate.automatic,
		})
	}
	for scopeID, live := range c.live {
		snapshot.LiveScopes = append(snapshot.LiveScopes, CurrentNodeLiveScopeSnapshot{
			CurrentNode: live.reference,
			ScopeID:     scopeID,
			Automatic:   live.automatic,
		})
	}
	for sourceScope, intents := range c.heldIntents {
		for _, intent := range intents {
			snapshot.HeldIntents = append(snapshot.HeldIntents, CurrentNodeHeldIntentSnapshot{
				CurrentNode: intent.CurrentNode,
				SourceScope: sourceScope,
			})
		}
	}
	return snapshot
}

func (c *CurrentNodeController) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.automaticQueue = nil
	c.queued = make(map[workflow.CurrentNodeReferenceKey]struct{})
	c.heldIntents = make(map[runtimeids.ExecutionScopeID][]CurrentNodeAutomaticIntent)
	gates := make([]currentNodeAdmissionGate, 0, len(c.gates))
	for _, gate := range c.gates {
		gates = append(gates, gate)
	}
	liveScopes := make([]runtimeids.ExecutionScopeID, 0, len(c.live))
	for scopeID := range c.live {
		liveScopes = append(liveScopes, scopeID)
	}
	c.mu.Unlock()

	c.workerCancel()
	for _, gate := range gates {
		gate.lease.Cancel()
	}
	var stopErrs []error
	for _, scopeID := range liveScopes {
		handle, exists := c.authority.ExecutionByScope(scopeID)
		if !exists {
			continue
		}
		if err := handle.Stop(context.Background()); err != nil {
			stopErrs = append(stopErrs, fmt.Errorf("stop workflow execution scope %s: %w", scopeID, err))
		}
	}
	c.workerWG.Wait()
	c.admissionWG.Wait()
	return errors.Join(stopErrs...)
}

func (c *CurrentNodeController) enqueueAutomaticIntents(intents []CurrentNodeAutomaticIntent) {
	if len(intents) == 0 || c == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	for _, intent := range intents {
		key, err := intent.CurrentNode.Key()
		if err != nil {
			panic(fmt.Sprintf("enqueue automatic current node intent: %v", err))
		}
		if _, queued := c.queued[key]; queued {
			continue
		}
		if _, gated := c.gates[key]; gated {
			continue
		}
		if _, live := c.liveByNode[key]; live {
			continue
		}
		c.automaticQueue = append(c.automaticQueue, intent)
		c.queued[key] = struct{}{}
	}
	c.mu.Unlock()
	c.wakeAutomaticWorker()
}

func (c *CurrentNodeController) wakeAutomaticWorker() {
	select {
	case c.workerWake <- struct{}{}:
	default:
	}
}

func (c *CurrentNodeController) runAutomaticAdmissions() {
	defer c.workerWG.Done()
	for {
		select {
		case <-c.workerContext.Done():
			return
		case <-c.workerWake:
		}
		for {
			intent, ok := c.takeAutomaticIntent()
			if !ok {
				break
			}
			c.workerWG.Add(1)
			go func(intent CurrentNodeAutomaticIntent) {
				defer c.workerWG.Done()
				// Admission failures compensate durable admitted work to
				// interruption. A stale intent has no durable work left to
				// alter, so it is simply discarded instead of retried.
				_ = c.admit(c.workerContext, intent.CurrentNode, false, true)
			}(intent)
		}
	}
}

func (c *CurrentNodeController) takeAutomaticIntent() (CurrentNodeAutomaticIntent, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.automaticActiveLocked() >= c.automaticConcurrency || len(c.automaticQueue) == 0 {
		return CurrentNodeAutomaticIntent{}, false
	}
	index := 0
	if c.lastAutomaticTask != nil {
		for candidateIndex, candidate := range c.automaticQueue {
			if candidate.CurrentNode.TaskID == *c.lastAutomaticTask {
				index = candidateIndex
				break
			}
		}
	}
	intent := c.automaticQueue[index]
	c.automaticQueue = append(c.automaticQueue[:index], c.automaticQueue[index+1:]...)
	key, err := intent.CurrentNode.Key()
	if err != nil {
		panic(fmt.Sprintf("take automatic current node intent: %v", err))
	}
	delete(c.queued, key)
	taskID := intent.CurrentNode.TaskID
	c.lastAutomaticTask = &taskID
	return intent, true
}

func (c *CurrentNodeController) automaticActiveLocked() int {
	active := 0
	for _, gate := range c.gates {
		if gate.automatic {
			active++
		}
	}
	for _, live := range c.live {
		if live.automatic {
			active++
		}
	}
	return active
}

func (c *CurrentNodeController) liveLease(scopeID runtimeids.ExecutionScopeID) (sessionruntime.WorkflowExecutionLease, error) {
	if c == nil || scopeID.IsZero() {
		return sessionruntime.WorkflowExecutionLease{}, errors.New("workflow exact execution scope id is required")
	}
	c.mu.Lock()
	if _, stopping := c.stopping[scopeID]; stopping {
		c.mu.Unlock()
		return sessionruntime.WorkflowExecutionLease{}, sessionruntime.ErrExecutionNoLongerLive
	}
	live, exists := c.live[scopeID]
	c.mu.Unlock()
	if !exists {
		return sessionruntime.WorkflowExecutionLease{}, sessionruntime.ErrExecutionNoLongerLive
	}
	return live.lease, nil
}

func currentNodeAutomaticIntents(references []workflow.CurrentNodeReference) ([]CurrentNodeAutomaticIntent, error) {
	intents := make([]CurrentNodeAutomaticIntent, 0, len(references))
	seen := make(map[workflow.CurrentNodeReferenceKey]struct{}, len(references))
	for index, reference := range references {
		key, err := reference.Key()
		if err != nil {
			return nil, fmt.Errorf("automatic successor current node at index %d: %w", index, err)
		}
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("automatic successor current node at index %d is duplicated", index)
		}
		seen[key] = struct{}{}
		intents = append(intents, CurrentNodeAutomaticIntent{CurrentNode: reference})
	}
	return intents, nil
}

var _ workflowruntime.Controller = (*CurrentNodeController)(nil)
var _ sessionruntime.ExecutionFinalized = (*CurrentNodeController)(nil)
