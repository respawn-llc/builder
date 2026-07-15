package client

import (
	"context"

	servicecontract "core/shared/apicontract"
	"core/shared/serverapi"
)

type SessionViewClient interface {
	GetSessionMainView(ctx context.Context, req serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error)
	GetSessionTranscriptPage(ctx context.Context, req serverapi.SessionTranscriptPageRequest) (serverapi.SessionTranscriptPageResponse, error)
	GetLatestCommittedAssistantFinalAnswer(ctx context.Context, req serverapi.SessionLatestCommittedAssistantFinalAnswerRequest) (serverapi.SessionLatestCommittedAssistantFinalAnswerResponse, error)
	GetSessionExecutionEnvironment(ctx context.Context, req serverapi.SessionExecutionEnvironmentRequest) (serverapi.SessionExecutionEnvironmentResponse, error)
}

type loopbackSessionViewClient struct {
	loopbackClient[servicecontract.SessionViewService]
}

func NewLoopbackSessionViewClient(service servicecontract.SessionViewService) SessionViewClient {
	return &loopbackSessionViewClient{loopbackClient: newLoopbackClient(service)}
}

func (c *loopbackSessionViewClient) GetSessionMainView(ctx context.Context, req serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
	return callLoopbackClient(c, "session view service is required", ctx, req, servicecontract.SessionViewService.GetSessionMainView)
}

func (c *loopbackSessionViewClient) GetSessionTranscriptPage(ctx context.Context, req serverapi.SessionTranscriptPageRequest) (serverapi.SessionTranscriptPageResponse, error) {
	return callLoopbackClient(c, "session view service is required", ctx, req, servicecontract.SessionViewService.GetSessionTranscriptPage)
}

func (c *loopbackSessionViewClient) GetLatestCommittedAssistantFinalAnswer(ctx context.Context, req serverapi.SessionLatestCommittedAssistantFinalAnswerRequest) (serverapi.SessionLatestCommittedAssistantFinalAnswerResponse, error) {
	return callLoopbackClient(c, "session view service is required", ctx, req, servicecontract.SessionViewService.GetLatestCommittedAssistantFinalAnswer)
}

func (c *loopbackSessionViewClient) GetSessionExecutionEnvironment(ctx context.Context, req serverapi.SessionExecutionEnvironmentRequest) (serverapi.SessionExecutionEnvironmentResponse, error) {
	return callLoopbackClient(c, "session view service is required", ctx, req, servicecontract.SessionViewService.GetSessionExecutionEnvironment)
}
