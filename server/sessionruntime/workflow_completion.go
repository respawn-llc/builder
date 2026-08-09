package sessionruntime

import (
	"context"
	"errors"

	"core/server/runtime"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func (a *Authority) AcceptWorkflowCompletion(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	origin serverapi.RuntimeStepOrigin,
	parsed workflowruntime.ParsedCompletion,
) (*runtime.WorkflowCompletionAcceptance, error) {
	if a == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if sessionID.IsZero() {
		return nil, errors.New("session id is required")
	}
	if err := origin.Validate(); err != nil {
		return nil, err
	}
	var acceptance *runtime.WorkflowCompletionAcceptance
	err := a.WithCurrentRuntime(
		ctx,
		sessionID,
		func(callbackCtx context.Context, engine *runtime.Engine) error {
			var acceptErr error
			acceptance, acceptErr = engine.AcceptWorkflowCompletion(
				callbackCtx,
				origin,
				parsed,
			)
			return acceptErr
		},
	)
	return acceptance, err
}
