package authservice

import (
	"context"

	"core/server/auth"
)

type Readiness struct {
	Ready    bool
	Required bool
	Gate     auth.StartupGate
	State    auth.State
}

func EvaluateReadiness(ctx context.Context, manager *auth.Manager, required bool) (Readiness, error) {
	readiness := Readiness{Ready: !required, Required: required, State: auth.EmptyState()}
	if manager == nil {
		return readiness, nil
	}
	state, err := manager.Load(ctx)
	if err != nil {
		return Readiness{}, err
	}
	readiness.State = state
	readiness.Gate = auth.EvaluateStartupGate(state)
	readiness.Ready = !required || readiness.Gate.Ready
	return readiness, nil
}
