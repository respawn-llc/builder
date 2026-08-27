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
	return a.invariantPolicy.OperationalError(ErrSessionRuntimeInvariant, operation, cause)
}
