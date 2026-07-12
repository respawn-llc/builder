package app

import (
	"context"
	"sync"
	"sync/atomic"

	"core/server/llm"
	"core/server/runtime"
	"core/shared/clientui"
	"core/shared/serverapi"
)

type countingSessionViewClient struct {
	view            clientui.RuntimeMainView
	page            clientui.TranscriptPage
	count           atomic.Int32
	mainViewCount   atomic.Int32
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
	return serverapi.SessionTranscriptPageResponse{Transcript: c.page}, nil
}

func (c *countingSessionViewClient) GetLatestCommittedAssistantFinalAnswer(_ context.Context, req serverapi.SessionLatestCommittedAssistantFinalAnswerRequest) (serverapi.SessionLatestCommittedAssistantFinalAnswerResponse, error) {
	c.lastFinalReq = req
	if c.finalAnswerErr != nil {
		return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{}, c.finalAnswerErr
	}
	return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{Answer: c.finalAnswer}, nil
}

type blockingSessionViewClient struct{}

func (blockingSessionViewClient) GetSessionMainView(ctx context.Context, _ serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
	<-ctx.Done()
	return serverapi.SessionMainViewResponse{}, ctx.Err()
}

func (blockingSessionViewClient) GetSessionTranscriptPage(ctx context.Context, _ serverapi.SessionTranscriptPageRequest) (serverapi.SessionTranscriptPageResponse, error) {
	<-ctx.Done()
	return serverapi.SessionTranscriptPageResponse{}, ctx.Err()
}

func (blockingSessionViewClient) GetLatestCommittedAssistantFinalAnswer(ctx context.Context, _ serverapi.SessionLatestCommittedAssistantFinalAnswerRequest) (serverapi.SessionLatestCommittedAssistantFinalAnswerResponse, error) {
	<-ctx.Done()
	return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{}, ctx.Err()
}

type controlledTranscriptPageResult struct {
	response serverapi.SessionTranscriptPageResponse
	err      error
}

type controlledTranscriptPageClient struct {
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

type flakySessionViewClient struct {
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

type mutableRuntimeResolver struct {
	mu     sync.Mutex
	engine *runtime.Engine
}

func (r *mutableRuntimeResolver) Set(engine *runtime.Engine) {
	r.mu.Lock()
	r.engine = engine
	r.mu.Unlock()
}

func (r *mutableRuntimeResolver) ResolveRuntime(context.Context, string) (*runtime.Engine, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.engine, nil
}

func (r *mutableRuntimeResolver) WithGuardedRuntime(_ context.Context, _ string, fn func(*runtime.Engine) error) (bool, error) {
	r.mu.Lock()
	engine := r.engine
	r.mu.Unlock()
	if engine == nil {
		return false, nil
	}
	return true, fn(engine)
}

func (r *mutableRuntimeResolver) BeginSessionRun(string) (func(), bool) {
	return func() {}, true
}

func (r *mutableRuntimeResolver) BeginCancellableSessionRun(string) (context.Context, func(), bool) {
	return context.Background(), func() {}, true
}

func (r *mutableRuntimeResolver) SessionRunsBlocked(string) bool { return false }

type runtimeClientFakeLLM struct {
	mu        sync.Mutex
	responses []llm.Response
}

func (f *runtimeClientFakeLLM) Generate(_ context.Context, _ llm.Request) (llm.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.responses) == 0 {
		return llm.Response{}, nil
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp, nil
}

func (f *runtimeClientFakeLLM) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{
		ProviderID:                    "openai",
		SupportsResponsesAPI:          true,
		SupportsResponsesCompact:      true,
		SupportsReasoningEncrypted:    true,
		SupportsServerSideContextEdit: true,
		IsOpenAIFirstParty:            true,
	}, nil
}
