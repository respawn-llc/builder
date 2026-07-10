package scriptedllm

import (
	"context"
	"fmt"
	"sync"

	"core/server/llm"
	"core/shared/optional"
)

const defaultContextWindowTokens = 200000

type Client struct {
	mu              sync.Mutex
	steps           []Step
	compactions     []llm.CompactionResponse
	calls           []llm.Request
	compactionCalls []llm.CompactionRequest
	caps            llm.ProviderCapabilities
	inputTokens     int
	contextWindow   int
	allowConcurrent bool
	active          bool
	activeCh        chan struct{}
	activeClosed    bool
}

func NewClient(script Script) *Client {
	inputTokens := 0
	if script.InputTokenCount != nil {
		inputTokens = *script.InputTokenCount
	}
	contextWindow := defaultContextWindowTokens
	if script.ContextWindowTokens != nil {
		contextWindow = *script.ContextWindowTokens
	}
	return &Client{
		steps:           append([]Step(nil), script.Steps...),
		compactions:     append([]llm.CompactionResponse(nil), script.Compactions...),
		caps:            materializeCapabilities(script.Capabilities),
		inputTokens:     inputTokens,
		contextWindow:   contextWindow,
		allowConcurrent: script.AllowConcurrent,
		activeCh:        make(chan struct{}),
	}
}

func NewLegacyClient(caps llm.ProviderCapabilities, steps ...Step) *Client {
	return NewClient(Script{Capabilities: &caps, Steps: steps})
}

func (c *Client) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	step, finish, err := c.nextStep(req)
	if err != nil {
		return llm.Response{}, err
	}
	defer finish()
	return c.completeStep(ctx, req, step, nil)
}

func (c *Client) GenerateStreamWithEvents(ctx context.Context, req llm.Request, callbacks llm.StreamCallbacks) (llm.Response, error) {
	step, finish, err := c.nextStep(req)
	if err != nil {
		return llm.Response{}, err
	}
	defer finish()
	return c.completeStep(ctx, req, step, &callbacks)
}

func (c *Client) Compact(_ context.Context, req llm.CompactionRequest) (llm.CompactionResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.compactionCalls = append(c.compactionCalls, req)
	if len(c.compactions) == 0 {
		return llm.CompactionResponse{}, ErrScriptExhausted
	}
	response := c.compactions[0]
	c.compactions = c.compactions[1:]
	response.TrimmedItemsCount = optional.CloneInt(response.TrimmedItemsCount)
	return response, nil
}

func (c *Client) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.caps, nil
}

func materializeCapabilities(caps *llm.ProviderCapabilities) llm.ProviderCapabilities {
	if caps == nil {
		return DefaultProviderCapabilities()
	}
	return *caps
}

func DefaultProviderCapabilities() llm.ProviderCapabilities {
	return llm.ProviderCapabilities{ProviderID: "openai", SupportsResponsesAPI: true, IsOpenAIFirstParty: true}
}

func (c *Client) CountRequestInputTokens(context.Context, llm.Request) (int, error) {
	return c.inputTokens, nil
}

func (c *Client) SupportsRequestInputTokenCount(context.Context) (bool, error) {
	return true, nil
}

func (c *Client) ResolveModelContextWindow(context.Context, string) (int, error) {
	return c.contextWindow, nil
}

func (c *Client) Requests() []llm.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]llm.Request(nil), c.calls...)
}

func (c *Client) RemainingSteps() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.steps)
}

func (c *Client) WaitUntilActive(ctx context.Context) error {
	select {
	case <-c.activeCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) nextStep(req llm.Request) (Step, func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active && !c.allowConcurrent {
		return Step{}, nil, ErrConcurrentCall
	}
	if len(c.steps) == 0 {
		return Step{}, nil, ErrScriptExhausted
	}
	step := c.steps[0]
	c.steps = c.steps[1:]
	c.calls = append(c.calls, req)
	c.active = true
	if !c.activeClosed {
		close(c.activeCh)
		c.activeClosed = true
	}
	return step, c.finishCall, nil
}

func (c *Client) finishCall() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active = false
}

func (c *Client) completeStep(ctx context.Context, req llm.Request, step Step, callbacks *llm.StreamCallbacks) (llm.Response, error) {
	if step.Cancel {
		if err := ctx.Err(); err != nil {
			return llm.Response{}, err
		}
		return llm.Response{}, context.Canceled
	}
	if step.Err != nil {
		return llm.Response{}, step.Err
	}
	if err := validateExpectedToolResults(req, step.ExpectedToolResults); err != nil {
		return llm.Response{}, err
	}
	if step.BeforeResponse != nil {
		if err := step.BeforeResponse(ctx); err != nil {
			return llm.Response{}, err
		}
	}
	if callbacks != nil {
		if callbacks.OnStreamActivity != nil {
			callbacks.OnStreamActivity()
		}
		for _, delta := range step.StreamDeltas {
			if callbacks.OnAssistantDelta != nil {
				callbacks.OnAssistantDelta(delta)
			}
		}
		for _, delta := range step.ReasoningDeltas {
			if callbacks.OnReasoningSummaryDelta != nil {
				callbacks.OnReasoningSummaryDelta(delta)
			}
		}
	}
	if step.AfterResponse != nil {
		if err := step.AfterResponse(ctx); err != nil {
			return llm.Response{}, err
		}
	}
	return step.Response, nil
}

func validateExpectedToolResults(req llm.Request, expected []ExpectedToolResult) error {
	for _, want := range expected {
		found := false
		for _, item := range req.Items {
			if item.CallID == want.CallID && item.Name == want.Name && (item.Type == llm.ResponseItemTypeFunctionCallOutput || item.Type == llm.ResponseItemTypeCustomToolOutput) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: call_id=%s name=%s", ErrUnexpectedToolResult, want.CallID, want.Name)
		}
	}
	return nil
}

var _ llm.Client = (*Client)(nil)
var _ llm.StreamEventsClient = (*Client)(nil)
var _ llm.CompactionClient = (*Client)(nil)
var _ llm.ProviderCapabilitiesClient = (*Client)(nil)
var _ llm.RequestInputTokenCountClient = (*Client)(nil)
var _ llm.RequestInputTokenCountSupportClient = (*Client)(nil)
var _ llm.ModelContextWindowClient = (*Client)(nil)
