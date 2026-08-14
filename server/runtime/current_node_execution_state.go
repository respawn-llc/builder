package runtime

import (
	"errors"
	"fmt"
	"sync"

	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
)

type CurrentNodeExecutionBinding struct {
	engine  *Engine
	scopeID runtimeids.ExecutionScopeID
	once    sync.Once
	err     error
}

type CurrentNodeExecutionPublication struct {
	engine *Engine
	state  *currentNodeExecutionState
	config *workflowruntime.CurrentNodeExecutionConfig
	locked bool
	done   bool
}

func (e *Engine) PrepareCurrentNodeExecutionPublication(
	config *workflowruntime.CurrentNodeExecutionConfig,
) (*CurrentNodeExecutionPublication, error) {
	if e == nil {
		return nil, errors.New("runtime engine is required")
	}
	if err := validateCurrentNodeExecutionConfig(config); err != nil {
		return nil, err
	}
	state := e.currentNodeExecution
	if state == nil {
		return nil, errors.New("current node execution state is unavailable")
	}
	cloned := cloneCurrentNodeExecutionConfig(config)
	return &CurrentNodeExecutionPublication{engine: e, state: state, config: cloned}, nil
}

func (p *CurrentNodeExecutionPublication) Begin() error {
	if p == nil || p.state == nil || p.done || p.locked {
		return errors.New("current node execution publication is invalid")
	}
	p.state.mu.Lock()
	if p.state.owner != nil {
		p.state.mu.Unlock()
		return fmt.Errorf("current node execution scope %s cannot publish while scope %s owns the state", p.config.ScopeID, p.state.owner.scopeID)
	}
	if p.state.config != nil && p.state.config.ScopeID == p.config.ScopeID &&
		!p.state.config.Instructions.CurrentNode.Equal(p.config.Instructions.CurrentNode) {
		p.state.mu.Unlock()
		return fmt.Errorf("current node execution scope %s cannot change Current Node", p.config.ScopeID)
	}
	p.locked = true
	return nil
}

func (p *CurrentNodeExecutionPublication) Commit() *CurrentNodeExecutionBinding {
	if p == nil || p.state == nil || p.done || !p.locked {
		panic("Current Node execution publication must be validated and locked before commit")
	}
	p.done = true
	p.state.config = p.config
	p.state.delivery = newWorkflowPromptDeliveryState(p.config)
	p.state.owner = &currentNodeExecutionOwner{scopeID: p.config.ScopeID}
	p.state.mu.Unlock()
	p.engine.mu.Lock()
	p.engine.workflowTerminal = WorkflowTerminalState{}
	p.engine.mu.Unlock()
	return &CurrentNodeExecutionBinding{engine: p.engine, scopeID: p.config.ScopeID}
}

func (p *CurrentNodeExecutionPublication) Cancel() {
	if p == nil || p.state == nil || p.done || !p.locked {
		return
	}
	p.done = true
	p.state.mu.Unlock()
}

type currentNodeExecutionSnapshot struct {
	config   *workflowruntime.CurrentNodeExecutionConfig
	delivery *workflowPromptDeliveryState
}

type currentNodeExecutionState struct {
	mu       sync.RWMutex
	config   *workflowruntime.CurrentNodeExecutionConfig
	delivery *workflowPromptDeliveryState
	owner    *currentNodeExecutionOwner
}

type currentNodeExecutionOwner struct {
	scopeID runtimeids.ExecutionScopeID
}

func newCurrentNodeExecutionState() *currentNodeExecutionState {
	return &currentNodeExecutionState{
		delivery: newWorkflowPromptDeliveryState(nil),
	}
}

func (b *CurrentNodeExecutionBinding) Close() error {
	if b == nil || b.engine == nil {
		return nil
	}
	b.once.Do(func() {
		state := b.engine.currentNodeExecution
		if state == nil {
			b.err = errors.New("current node execution state is unavailable")
			return
		}
		state.mu.Lock()
		switch {
		case state.config == nil:
			b.err = fmt.Errorf("current node execution scope %s is already unbound", b.scopeID)
		case state.owner == nil:
			b.err = fmt.Errorf("current node execution scope %s has no binding owner", b.scopeID)
		case state.owner.scopeID != b.scopeID:
			b.err = fmt.Errorf(
				"current node execution scope %s cannot unbind active scope %s",
				b.scopeID,
				state.owner.scopeID,
			)
		default:
			state.owner = nil
		}
		state.mu.Unlock()
		if b.err == nil {
			b.err = b.engine.finishCurrentNodeExecution()
		}
	})
	return b.err
}

func (e *Engine) finishCurrentNodeExecution() error {
	if e == nil {
		return errors.New("runtime engine is required")
	}
	state := e.currentNodeExecution
	if state == nil {
		return errors.New("current node execution state is unavailable")
	}
	completed := e.WorkflowTerminalState().Completed
	state.mu.Lock()
	if state.owner != nil {
		owner := state.owner.scopeID
		state.mu.Unlock()
		return fmt.Errorf("current node execution activation cannot finish while scope %s owns the state", owner)
	}
	if completed {
		if state.config == nil {
			state.mu.Unlock()
			return errors.New("completed current node execution has no active config")
		}
		state.config = nil
		state.delivery = newWorkflowPromptDeliveryState(nil)
	}
	state.mu.Unlock()
	e.mu.Lock()
	e.workflowTerminal = WorkflowTerminalState{}
	e.mu.Unlock()
	return nil
}

func (e *Engine) ResetLockedContractForWorkflowCompactionBoundary() error {
	if e == nil || e.store == nil {
		return errors.New("runtime engine is unavailable")
	}
	state := e.currentNodeExecution
	if state == nil {
		return errors.New("current node execution state is unavailable")
	}
	state.mu.RLock()
	owner := state.owner
	state.mu.RUnlock()
	if owner != nil {
		return fmt.Errorf(
			"locked contract cannot reset while current node execution scope %s is active",
			owner.scopeID,
		)
	}
	if err := e.store.ResetLockedContractForCompactionBoundary(); err != nil {
		return err
	}
	e.lockedContractState().Clear()
	return nil
}

func (e *Engine) currentNodeExecutionSnapshot() currentNodeExecutionSnapshot {
	if e == nil || e.currentNodeExecution == nil {
		return currentNodeExecutionSnapshot{}
	}
	e.currentNodeExecution.mu.RLock()
	defer e.currentNodeExecution.mu.RUnlock()
	return currentNodeExecutionSnapshot{
		config:   e.currentNodeExecution.config,
		delivery: e.currentNodeExecution.delivery,
	}
}

func (e *Engine) currentNodeExecutionConfig() (*workflowruntime.CurrentNodeExecutionConfig, bool) {
	snapshot := e.currentNodeExecutionSnapshot()
	return snapshot.config, snapshot.config != nil
}

func validateCurrentNodeExecutionConfig(config *workflowruntime.CurrentNodeExecutionConfig) error {
	if config == nil {
		return errors.New("current node execution config is required")
	}
	if config.ScopeID.IsZero() {
		return errors.New("current node execution scope id is required")
	}
	if err := config.Instructions.CurrentNode.Validate(); err != nil {
		return fmt.Errorf("current node execution reference: %w", err)
	}
	return nil
}

func cloneCurrentNodeExecutionConfig(
	config *workflowruntime.CurrentNodeExecutionConfig,
) *workflowruntime.CurrentNodeExecutionConfig {
	if config == nil {
		return nil
	}
	cloned := *config
	cloned.Contract.Transitions = append(
		[]workflowruntime.CompletionTransition(nil),
		config.Contract.Transitions...,
	)
	for index := range cloned.Contract.Transitions {
		cloned.Contract.Transitions[index].Parameters = append(
			[]workflow.Parameter(nil),
			config.Contract.Transitions[index].Parameters...,
		)
	}
	cloned.Instructions.Transitions = append(
		[]workflowruntime.TransitionInstruction(nil),
		config.Instructions.Transitions...,
	)
	return &cloned
}
