package llm

import (
	"encoding/json"
)

// MarshalOpenAIWirePayload builds the responses.ResponseNewParams that the
// OpenAI / openai-compatible HTTP transport would POST, using the production
// buildPayload path, and marshals a request-shape-equivalent diagnostic JSON.
// JSON escaping may differ from the openai-go SDK HTTP body. No HTTP is
// performed. This does not reproduce provider token accounting or
// compaction/context decisions.
//
// This is an operator-only diagnostic seam used by offline inspection tooling to
// capture a request payload for a session without executing a model turn.
//
//   - request: the provider-DTO request (project from a provider-agnostic Request
//     via RequestAsOpenAI, the same projection the live OpenAIClient uses).
//   - store: mirrors the transport's Store flag (Responses API persistence),
//     sourced from the session's provider settings.
//   - modelVerbosity: mirrors the transport's ModelVerbosity setting.
//   - mode: the auth mode (OAuth vs API key) — controls suppression of
//     temperature/max-tokens and the codex base URL. Pass the zero value for a
//     plain API-key session.
//   - capabilities: the resolved provider capabilities for the session's provider
//     (openai / openai-compatible / chatgpt-codex).
func MarshalOpenAIWirePayload(request OpenAIRequest, store bool, modelVerbosity string, mode OpenAIAuthMode, capabilities ProviderCapabilities) (json.RawMessage, error) {
	transport := &HTTPTransport{Store: store, ModelVerbosity: modelVerbosity}
	params, err := transport.buildPayload(request, mode, capabilities)
	if err != nil {
		return nil, err
	}
	return json.Marshal(params)
}
