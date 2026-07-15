package core

import (
	"context"

	"core/shared/client"
	"core/shared/serverapi"
)

type sessionExecutionEnvironmentClientBoundary interface {
	GetSessionExecutionEnvironment(context.Context, serverapi.SessionExecutionEnvironmentRequest) (serverapi.SessionExecutionEnvironmentResponse, error)
}

var _ sessionExecutionEnvironmentClientBoundary = (client.SessionViewClient)(nil)

type sessionExecutionWorkspaceRootClientBoundary interface {
	GetSessionExecutionWorkspaceRoot(context.Context, serverapi.SessionExecutionWorkspaceRootRequest) (serverapi.SessionExecutionWorkspaceRootResponse, error)
}

var _ sessionExecutionWorkspaceRootClientBoundary = (client.SessionViewClient)(nil)
