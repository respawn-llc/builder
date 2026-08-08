package sessionruntime

import (
	"context"

	"core/server/llm"
)

func sessionRuntimeTestProviderCapabilities() llm.ProviderCapabilities {
	return llm.ProviderCapabilities{
		ProviderID:           "openai",
		SupportsResponsesAPI: true,
		IsOpenAIFirstParty:   true,
	}
}

func (*sessionRuntimeTestLLMClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return sessionRuntimeTestProviderCapabilities(), nil
}

func (*blockingLLMClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return sessionRuntimeTestProviderCapabilities(), nil
}

func (*ownerlessRetirementLLMClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return sessionRuntimeTestProviderCapabilities(), nil
}

func (lifecycleRequestCaptureClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return sessionRuntimeTestProviderCapabilities(), nil
}

func (*sequentialBackgroundClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return sessionRuntimeTestProviderCapabilities(), nil
}
