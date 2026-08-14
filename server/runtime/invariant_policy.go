package runtime

import (
	"errors"

	"core/shared/invariant"
)

var ErrRuntimeInvariant = errors.New("Runtime ownership invariant failed")

func (e *Engine) runtimeInvariant(operation string, cause error) error {
	if e == nil {
		return runtimeInvariant(false, operation, cause)
	}
	policy := e.invariantPolicy
	if policy.Mode() == "" {
		policy = invariant.OperationalPolicy(e.cfg.Debug)
	}
	return invariantError(policy, ErrRuntimeInvariant, operation, cause)
}

func runtimeInvariant(debug bool, operation string, cause error) error {
	return invariantError(invariant.OperationalPolicy(debug), ErrRuntimeInvariant, operation, cause)
}

func invariantError(policy invariant.Policy, sentinel error, operation string, cause error) error {
	policy.Check(false, invariant.FailureDiagnostic(invariant.ScopeWorkflowExecution, operation, cause))
	return errors.Join(sentinel, errors.New(operation+": "+cause.Error()))
}
