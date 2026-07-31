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

type currentNodeExecutionSnapshot struct {
	config   *workflowruntime.CurrentNodeExecutionConfig
	delivery *workflowPromptDeliveryState
}

type currentNodeExecutionState struct {
	mu       sync.RWMutex
	config   *workflowruntime.CurrentNodeExecutionConfig
	delivery *workflowPromptDeliveryState
	bound    bool
}

func newCurrentNodeExecutionState(config *workflowruntime.CurrentNodeExecutionConfig) *currentNodeExecutionState {
	state := &currentNodeExecutionState{
		delivery: newWorkflowPromptDeliveryState(nil),
	}
	if config != nil {
		state.config = cloneCurrentNodeExecutionConfig(config)
		state.delivery = newWorkflowPromptDeliveryState(state.config)
	}
	return state
}

func (e *Engine) BindCurrentNodeExecution(
	config *workflowruntime.CurrentNodeExecutionConfig,
) (*CurrentNodeExecutionBinding, error) {
	if e == nil {
		return nil, errors.New("runtime engine is required")
	}
	if config == nil {
		return nil, errors.New("current node execution config is required")
	}
	if err := validateCurrentNodeExecutionConfig(config); err != nil {
		return nil, err
	}
	state := e.currentNodeExecution
	if state == nil {
		return nil, errors.New("current node execution state is unavailable")
	}
	cloned := cloneCurrentNodeExecutionConfig(config)
	state.mu.Lock()
	if state.bound {
		current := state.config
		state.mu.Unlock()
		if current == nil {
			return nil, errors.New("current node execution state is bound without an active config")
		}
		return nil, fmt.Errorf(
			"current node execution scope %s for %v cannot bind while scope %s for %v already owns the state",
			cloned.ScopeID,
			cloned.Instructions.CurrentNode,
			current.ScopeID,
			current.Instructions.CurrentNode,
		)
	}
	if state.config != nil {
		current := state.config
		if current.ScopeID != cloned.ScopeID ||
			!current.Instructions.CurrentNode.Equal(cloned.Instructions.CurrentNode) {
			state.mu.Unlock()
			return nil, fmt.Errorf(
				"current node execution scope %s for %v cannot bind while scope %s for %v is active",
				cloned.ScopeID,
				cloned.Instructions.CurrentNode,
				current.ScopeID,
				current.Instructions.CurrentNode,
			)
		}
	} else {
		state.config = cloned
		state.delivery = newWorkflowPromptDeliveryState(cloned)
	}
	state.bound = true
	state.mu.Unlock()
	e.mu.Lock()
	e.workflowTerminal = WorkflowTerminalState{}
	e.mu.Unlock()
	return &CurrentNodeExecutionBinding{engine: e, scopeID: cloned.ScopeID}, nil
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
		case !state.bound:
			b.err = fmt.Errorf("current node execution scope %s has no binding owner", b.scopeID)
		case state.config.ScopeID != b.scopeID:
			b.err = fmt.Errorf(
				"current node execution scope %s cannot unbind active scope %s",
				b.scopeID,
				state.config.ScopeID,
			)
		default:
			state.config = nil
			state.delivery = newWorkflowPromptDeliveryState(nil)
			state.bound = false
		}
		state.mu.Unlock()
		if b.err == nil {
			b.engine.mu.Lock()
			b.engine.workflowTerminal = WorkflowTerminalState{}
			b.engine.mu.Unlock()
		}
	})
	return b.err
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
