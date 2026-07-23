package app

import (
	"context"
	"sync"
	"sync/atomic"

	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/serverapi"
)

type countingSessionViewClient struct {
	apicontract.SessionViewService
	view            clientui.RuntimeMainView
	page            clientui.TranscriptPage
	count           atomic.Int32
	mainViewCount   atomic.Int32
	pageCount       atomic.Int32
	lastMainViewReq serverapi.SessionMainViewRequest
	lastPageReq     serverapi.SessionTranscriptPageRequest
	lastFinalReq    serverapi.SessionLatestCommittedAssistantFinalAnswerRequest
	finalAnswer     *string
	finalAnswerErr  error
}

func (c *countingSessionViewClient) GetSessionMainView(_ context.Context, req serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
	c.lastMainViewReq = req
	c.count.Add(1)
	c.mainViewCount.Add(1)
	return serverapi.SessionMainViewResponse{MainView: c.view}, nil
}

func (c *countingSessionViewClient) GetSessionTranscriptPage(_ context.Context, req serverapi.SessionTranscriptPageRequest) (serverapi.SessionTranscriptPageResponse, error) {
	c.lastPageReq = req
	c.pageCount.Add(1)
	return serverapi.SessionTranscriptPageResponse{Transcript: c.page}, nil
}

func (c *countingSessionViewClient) GetLatestCommittedAssistantFinalAnswer(_ context.Context, req serverapi.SessionLatestCommittedAssistantFinalAnswerRequest) (serverapi.SessionLatestCommittedAssistantFinalAnswerResponse, error) {
	c.lastFinalReq = req
	if c.finalAnswerErr != nil {
		return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{}, c.finalAnswerErr
	}
	return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{Answer: c.finalAnswer}, nil
}

func (c *countingSessionViewClient) GetSessionExecutionEnvironment(context.Context, serverapi.SessionExecutionEnvironmentRequest) (serverapi.SessionExecutionEnvironmentResponse, error) {
	return serverapi.SessionExecutionEnvironmentResponse{}, nil
}

type controlledTranscriptPageResult struct {
	response serverapi.SessionTranscriptPageResponse
	err      error
}

type controlledTranscriptPageClient struct {
	apicontract.SessionViewService
	started chan serverapi.SessionTranscriptPageRequest
	results chan controlledTranscriptPageResult
}

func newControlledTranscriptPageClient() *controlledTranscriptPageClient {
	return &controlledTranscriptPageClient{
		started: make(chan serverapi.SessionTranscriptPageRequest, 8),
		results: make(chan controlledTranscriptPageResult, 8),
	}
}

func (c *controlledTranscriptPageClient) GetSessionMainView(context.Context, serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
	return serverapi.SessionMainViewResponse{}, nil
}

func (c *controlledTranscriptPageClient) GetSessionTranscriptPage(ctx context.Context, req serverapi.SessionTranscriptPageRequest) (serverapi.SessionTranscriptPageResponse, error) {
	c.started <- req
	select {
	case result := <-c.results:
		return result.response, result.err
	case <-ctx.Done():
		return serverapi.SessionTranscriptPageResponse{}, ctx.Err()
	}
}

func (c *controlledTranscriptPageClient) GetLatestCommittedAssistantFinalAnswer(context.Context, serverapi.SessionLatestCommittedAssistantFinalAnswerRequest) (serverapi.SessionLatestCommittedAssistantFinalAnswerResponse, error) {
	return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{}, nil
}

func (c *controlledTranscriptPageClient) GetSessionExecutionEnvironment(ctx context.Context, _ serverapi.SessionExecutionEnvironmentRequest) (serverapi.SessionExecutionEnvironmentResponse, error) {
	<-ctx.Done()
	return serverapi.SessionExecutionEnvironmentResponse{}, ctx.Err()
}

type flakySessionViewClient struct {
	apicontract.SessionViewService
	mu        sync.Mutex
	responses []serverapi.SessionMainViewResponse
	errs      []error
	count     int
}

func (c *flakySessionViewClient) GetSessionMainView(context.Context, serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := c.count
	c.count++
	if idx < len(c.errs) && c.errs[idx] != nil {
		return serverapi.SessionMainViewResponse{}, c.errs[idx]
	}
	if idx < len(c.responses) {
		return c.responses[idx], nil
	}
	if len(c.responses) > 0 {
		return c.responses[len(c.responses)-1], nil
	}
	return serverapi.SessionMainViewResponse{}, nil
}

func (c *flakySessionViewClient) GetSessionTranscriptPage(context.Context, serverapi.SessionTranscriptPageRequest) (serverapi.SessionTranscriptPageResponse, error) {
	return serverapi.SessionTranscriptPageResponse{}, nil
}

func (c *flakySessionViewClient) GetLatestCommittedAssistantFinalAnswer(context.Context, serverapi.SessionLatestCommittedAssistantFinalAnswerRequest) (serverapi.SessionLatestCommittedAssistantFinalAnswerResponse, error) {
	return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{}, nil
}

func (c *flakySessionViewClient) GetSessionExecutionEnvironment(context.Context, serverapi.SessionExecutionEnvironmentRequest) (serverapi.SessionExecutionEnvironmentResponse, error) {
	return serverapi.SessionExecutionEnvironmentResponse{}, nil
}
