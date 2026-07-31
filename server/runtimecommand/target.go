package runtimecommand

import (
	"errors"

	"core/server/sessionruntime"
	"core/shared/runtimeids"
)

type targetKind uint8

const (
	targetSession targetKind = iota + 1
	targetAgent
)

type Target struct {
	kind                targetKind
	resource            runtimeids.SessionResourceRef
	scopeID             runtimeids.ExecutionScopeID
	executionGeneration sessionruntime.ExecutionGeneration
}

func SessionTarget(resource runtimeids.SessionResourceRef) Target {
	return Target{kind: targetSession, resource: resource}
}

func AgentTarget(scope sessionruntime.ExecutionScope) (Target, error) {
	if scope.Kind() != sessionruntime.ExecutionScopeAgent {
		return Target{}, errors.New("agent runtime command target requires an Agent exact execution scope")
	}
	resource, ok := scope.Resource()
	if !ok {
		return Target{}, errors.New("agent runtime command target has no session resource")
	}
	return Target{
		kind:                targetAgent,
		resource:            resource,
		scopeID:             scope.ID(),
		executionGeneration: scope.ExecutionGeneration(),
	}, nil
}

func (t Target) Validate() error {
	switch t.kind {
	case targetSession:
		return t.resource.Validate()
	case targetAgent:
		if t.scopeID.IsZero() {
			return errors.New("agent runtime command target scope is required")
		}
		if t.executionGeneration == 0 {
			return errors.New("agent runtime command target execution generation is required")
		}
		return t.resource.Validate()
	default:
		return errors.New("runtime command target is required")
	}
}

func (t Target) Resource() (runtimeids.SessionResourceRef, bool) {
	if t.kind != targetSession && t.kind != targetAgent {
		return runtimeids.SessionResourceRef{}, false
	}
	return t.resource, true
}

func (t Target) same(other Target) bool {
	return t.kind == other.kind &&
		t.resource == other.resource &&
		t.scopeID == other.scopeID &&
		t.executionGeneration == other.executionGeneration
}
