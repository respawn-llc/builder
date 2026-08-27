package workflowrunner

import (
	"context"
	"sync"

	"core/internal/testharness/scriptedllm"
	"core/server/llm"
)

var ErrScriptedRuntime = scriptedllm.ErrScriptExhausted

type ScriptedRuntimeStep = scriptedllm.Step
type ScriptedClient = scriptedllm.Client

type compactingScriptedClient struct {
	base        *ScriptedClient
	mu          sync.Mutex
	responses   []llm.CompactionResponse
	compactions []llm.CompactionRequest
}

func NewCompactingScriptedClient(
	caps llm.ProviderCapabilities,
	compactions []llm.CompactionResponse,
	steps ...ScriptedRuntimeStep,
) *compactingScriptedClient {
	return &compactingScriptedClient{
		base:      NewScriptedClient(caps, steps...),
		responses: append([]llm.CompactionResponse(nil), compactions...),
	}
}

func (c *compactingScriptedClient) Generate(ctx context.Context, request llm.Request, callbacks llm.StreamCallbacks) (llm.Response, error) {
	return c.base.Generate(ctx, request, callbacks)
}

func (c *compactingScriptedClient) ProviderCapabilities(ctx context.Context) (llm.ProviderCapabilities, error) {
	return c.base.ProviderCapabilities(ctx)
}

func (c *compactingScriptedClient) Requests() []llm.Request {
	return c.base.Requests()
}

func (c *compactingScriptedClient) Compact(ctx context.Context, request llm.CompactionRequest) (llm.CompactionResponse, error) {
	if err := ctx.Err(); err != nil {
		return llm.CompactionResponse{}, err
	}
	c.mu.Lock()
	c.compactions = append(c.compactions, request)
	if len(c.responses) == 0 {
		c.mu.Unlock()
		return llm.CompactionResponse{}, ErrScriptedRuntime
	}
	response := c.responses[0]
	c.responses = c.responses[1:]
	c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return llm.CompactionResponse{}, err
	}
	response.TrimmedItemsCount = new(int)
	*response.TrimmedItemsCount = len(request.InputItems)
	return response, nil
}

func (c *compactingScriptedClient) CompactionCalls() []llm.CompactionRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]llm.CompactionRequest(nil), c.compactions...)
}

func NewScriptedClient(caps llm.ProviderCapabilities, steps ...ScriptedRuntimeStep) *ScriptedClient {
	return scriptedllm.NewClient(scriptedllm.Script{Capabilities: &caps, Steps: steps})
}

func NewDefaultScriptedClient(steps ...ScriptedRuntimeStep) *ScriptedClient {
	caps := scriptedllm.DefaultProviderCapabilities()
	return scriptedllm.NewClient(scriptedllm.Script{Capabilities: &caps, Steps: steps})
}

func ScriptedFinalAnswer(content string) ScriptedRuntimeStep {
	return scriptedllm.FinalAnswer(content)
}

func ScriptedToolBatch(content string, calls ...llm.ToolCall) ScriptedRuntimeStep {
	return scriptedllm.ToolBatch(content, calls...)
}

func ScriptedRuntimeError(err error) ScriptedRuntimeStep {
	return scriptedllm.RuntimeError(err)
}

func ScriptedCancellation() ScriptedRuntimeStep {
	return scriptedllm.Cancellation()
}
