package llm

import (
	"encoding/json"

	"github.com/openai/openai-go/v3/responses"
)

// MarshalOpenAIWirePayload builds the exact responses.ResponseNewParams that the
// OpenAI / openai-compatible HTTP transport would POST, using the same production
// buildPayload path, and marshals it to the byte-identical JSON body the openai-go
// SDK sends over the wire (it calls the same ResponseNewParams.MarshalJSON that
// requestconfig uses to build the HTTP body). No HTTP is performed.
//
// This is an operator-only diagnostic seam used by offline inspection tooling to
// capture the literal request payload for a session without executing a model turn.
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
	return marshalOpenAIResponseNewParams(params)
}

// marshalOpenAIResponseNewParams serializes a prepared ResponseNewParams using the
// SDK's own json.Marshaler implementation — the same encoder the openai-go
// requestconfig layer invokes to build the HTTP request body.
func marshalOpenAIResponseNewParams(params responses.ResponseNewParams) (json.RawMessage, error) {
	return json.Marshal(params)
}
