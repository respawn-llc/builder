package runtime

import (
	"context"
	"sync"

	"core/server/llm"
	"core/shared/textutil"
)

const chatStoreTestStepID = "11111111-1111-4111-8111-111111111111"

type blockingThenQueuedClient struct {
	started  chan struct{}
	releaseC chan struct{}
	mu       sync.Mutex
	calls    []llm.Request
}

func newBlockingThenQueuedClient() *blockingThenQueuedClient {
	return &blockingThenQueuedClient{
		started:  make(chan struct{}),
		releaseC: make(chan struct{}),
	}
}

func (client *blockingThenQueuedClient) Generate(ctx context.Context, request llm.Request, _ llm.StreamCallbacks) (llm.Response, error) {
	client.mu.Lock()
	client.calls = append(client.calls, request)
	callCount := len(client.calls)
	if callCount == 1 {
		close(client.started)
	}
	client.mu.Unlock()
	if callCount == 1 {
		<-client.releaseC
		return llm.Response{}, ctx.Err()
	}
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("queued work handled"), Phase: textutil.Value(llm.MessagePhaseFinal)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}, nil
}

func (*blockingThenQueuedClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{
		ProviderID:           "openai",
		SupportsResponsesAPI: true,
		IsOpenAIFirstParty:   true,
	}, nil
}
