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
	return policy.OperationalError(ErrRuntimeInvariant, operation, cause)
}

func runtimeInvariant(debug bool, operation string, cause error) error {
	return invariant.OperationalError(ErrRuntimeInvariant, debug, operation, cause)
}
