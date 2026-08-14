package sessionruntime

import (
	"errors"

	"core/shared/invariant"
)

var ErrSessionRuntimeInvariant = errors.New("Session Runtime ownership invariant failed")

func sessionRuntimeInvariant(debug bool, operation string, cause error) error {
	return invariant.OperationalError(ErrSessionRuntimeInvariant, debug, operation, cause)
}

func (a *Authority) invariant(operation string, cause error) error {
	if a == nil {
		return sessionRuntimeInvariant(false, operation, cause)
	}
	a.invariantPolicy.Check(false, invariant.FailureDiagnostic(invariant.ScopeWorkflowExecution, operation, cause))
	return errors.Join(ErrSessionRuntimeInvariant, errors.New(operation+": "+cause.Error()))
}
