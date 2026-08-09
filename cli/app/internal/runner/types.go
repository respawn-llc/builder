package runner

import (
	"context"
	"io"

	"core/shared/serverapi"
)

type Request struct {
	SessionID                 string
	WorkspaceContextSessionID string
	AgentRole                 *string
}

type Dependencies struct {
	StartSessionServer  func(context.Context) (io.Closer, error)
	RunSessionLifecycle func(context.Context, io.Closer, *serverapi.SessionLaunchIntent, serverapi.RunPromptOverrides) error
}
