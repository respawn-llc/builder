package core

import (
	"context"

	"core/shared/apicontract"
	"core/shared/serverapi"
)

type sessionExecutionEnvironmentClientBoundary interface {
	GetSessionExecutionEnvironment(context.Context, serverapi.SessionExecutionEnvironmentRequest) (serverapi.SessionExecutionEnvironmentResponse, error)
}

var _ sessionExecutionEnvironmentClientBoundary = (apicontract.SessionViewService)(nil)
