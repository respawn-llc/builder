package sessionruntime

import (
	"errors"
	"fmt"
	"strings"

	"core/server/workflow"
	"core/shared/runtimeids"
)

type ExecutionGeneration uint64

type WorkflowExecutionRef struct {
	ProjectID   string
	WorkflowID  runtimeids.WorkflowID
	OperationID runtimeids.CurrentNodeOperationID
	CurrentNode workflow.CurrentNodeReference
}

type WorkflowOperationRef struct {
	OperationID runtimeids.CurrentNodeOperationID
	CurrentNode workflow.CurrentNodeReference
}

func (r WorkflowOperationRef) Validate() error {
	if r.OperationID.IsZero() {
		return errors.New("current node operation id is required")
	}
	if err := r.CurrentNode.Validate(); err != nil {
		return fmt.Errorf("workflow current node: %w", err)
	}
	return nil
}

func (r WorkflowExecutionRef) Validate() error {
	if strings.TrimSpace(r.ProjectID) == "" {
		return errors.New("workflow project id is required")
	}
	if r.WorkflowID.IsZero() {
		return errors.New("workflow id is required")
	}
	if err := r.Operation().Validate(); err != nil {
		return err
	}
	return nil
}

func (r WorkflowExecutionRef) Operation() WorkflowOperationRef {
	return WorkflowOperationRef{
		OperationID: r.OperationID,
		CurrentNode: r.CurrentNode,
	}
}

type ExecutionScopeKind uint8

const (
	ExecutionScopeAgent ExecutionScopeKind = iota + 1
	ExecutionScopeScript
)

type executionScopeData struct {
	id                  runtimeids.ExecutionScopeID
	kind                ExecutionScopeKind
	executionGeneration ExecutionGeneration
	resourceGeneration  runtimeids.ResourceGeneration
	workflow            *WorkflowExecutionRef
}

type executionScopeVariant interface {
	scopeData() executionScopeData
}

type agentExecutionScope struct {
	executionScopeData
	resource runtimeids.SessionResourceRef
}

func (s agentExecutionScope) scopeData() executionScopeData {
	return s.executionScopeData
}

type scriptExecutionScope struct {
	executionScopeData
}

func (s scriptExecutionScope) scopeData() executionScopeData {
	return s.executionScopeData
}

type ExecutionScope struct {
	value executionScopeVariant
}

func newScriptExecutionScope(
	id runtimeids.ExecutionScopeID,
	executionGeneration ExecutionGeneration,
	resourceGeneration runtimeids.ResourceGeneration,
	workflowRef *WorkflowExecutionRef,
) ExecutionScope {
	return ExecutionScope{value: scriptExecutionScope{executionScopeData: executionScopeData{
		id:                  id,
		kind:                ExecutionScopeScript,
		executionGeneration: executionGeneration,
		resourceGeneration:  resourceGeneration,
		workflow:            cloneWorkflowExecutionRef(workflowRef),
	}}}
}

func newAgentExecutionScope(
	id runtimeids.ExecutionScopeID,
	executionGeneration ExecutionGeneration,
	resource runtimeids.SessionResourceRef,
	workflowRef *WorkflowExecutionRef,
) ExecutionScope {
	if err := resource.Validate(); err != nil {
		panic(fmt.Sprintf("new agent execution scope: %v", err))
	}
	return ExecutionScope{value: agentExecutionScope{
		executionScopeData: executionScopeData{
			id:                  id,
			kind:                ExecutionScopeAgent,
			executionGeneration: executionGeneration,
			resourceGeneration:  resource.Generation(),
			workflow:            cloneWorkflowExecutionRef(workflowRef),
		},
		resource: resource,
	}}
}

func (s ExecutionScope) data() executionScopeData {
	if s.value == nil {
		panic("execution scope is uninitialized")
	}
	return s.value.scopeData()
}

func (s ExecutionScope) ID() runtimeids.ExecutionScopeID {
	return s.data().id
}

func (s ExecutionScope) Kind() ExecutionScopeKind {
	return s.data().kind
}

func (s ExecutionScope) ExecutionGeneration() ExecutionGeneration {
	return s.data().executionGeneration
}

func (s ExecutionScope) ResourceGeneration() runtimeids.ResourceGeneration {
	return s.data().resourceGeneration
}

func (s ExecutionScope) Workflow() (WorkflowExecutionRef, bool) {
	workflowRef := s.data().workflow
	if workflowRef == nil {
		return WorkflowExecutionRef{}, false
	}
	return *workflowRef, true
}

func (s ExecutionScope) Resource() (runtimeids.SessionResourceRef, bool) {
	agent, ok := s.value.(agentExecutionScope)
	if !ok {
		return runtimeids.SessionResourceRef{}, false
	}
	return agent.resource, true
}

func cloneWorkflowExecutionRef(ref *WorkflowExecutionRef) *WorkflowExecutionRef {
	if ref == nil {
		return nil
	}
	cloned := *ref
	return &cloned
}

func workflowExecutionKeyFor(ref WorkflowExecutionRef) (workflow.CurrentNodeReferenceKey, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	return ref.CurrentNode.Key()
}
