package scriptedllm

import (
	"context"

	"core/server/llm"
)

type LegacyClient struct {
	inner *Client
}

func NewLegacyOnlyClient(caps llm.ProviderCapabilities, steps ...Step) *LegacyClient {
	return &LegacyClient{inner: NewClient(Script{Capabilities: &caps, Steps: steps})}
}

func (c *LegacyClient) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	return c.inner.Generate(ctx, req)
}

func (c *LegacyClient) ProviderCapabilities(ctx context.Context) (llm.ProviderCapabilities, error) {
	return c.inner.ProviderCapabilities(ctx)
}

func (c *LegacyClient) Requests() []llm.Request {
	return c.inner.Requests()
}

func (c *LegacyClient) RemainingSteps() int {
	return c.inner.RemainingSteps()
}

var _ llm.Client = (*LegacyClient)(nil)
var _ llm.ProviderCapabilitiesClient = (*LegacyClient)(nil)
