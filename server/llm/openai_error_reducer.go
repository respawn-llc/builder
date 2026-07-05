package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"
)

type openAICompatibleErrorReducer struct {
	providerID string
}

type opaqueProviderErrorReducer struct {
	providerID string
}

func newOpenAICompatibleErrorReducer(providerID string) ProviderErrorReducer {
	return openAICompatibleErrorReducer{providerID: strings.TrimSpace(providerID)}
}

func newOpaqueProviderErrorReducer(providerID string) ProviderErrorReducer {
	return opaqueProviderErrorReducer{providerID: strings.TrimSpace(providerID)}
}

func (r openAICompatibleErrorReducer) Reduce(err error, rawResp *http.Response) (*ProviderAPIError, bool) {
	if reduced, ok := r.reduceFromStreamError(err, openAIResponseStatusCode(rawResp)); ok {
		return reduced, true
	}
	if reduced, ok := r.reduceFromSDK(err); ok {
		return reduced, true
	}
	if reduced, ok := r.reduceFromResponse(rawResp); ok {
		return reduced, true
	}
	return nil, false
}

func (r opaqueProviderErrorReducer) Reduce(err error, rawResp *http.Response) (*ProviderAPIError, bool) {
	if reduced, ok := r.reduceFromResponse(rawResp); ok {
		return reduced, true
	}
	if err != nil {
		message := strings.TrimSpace(err.Error())
		return &ProviderAPIError{
			ProviderID: r.providerID,
			StatusCode: 0,
			Code:       UnifiedErrorCodeUnknown,
			Message:    message,
			Raw:        message,
			Err:        err,
		}, true
	}
	return nil, false
}

func (r openAICompatibleErrorReducer) reduceFromStreamError(err error, statusCode int) (*ProviderAPIError, bool) {
	if err == nil {
		return nil, false
	}
	var streamErr *ssestream.StreamError
	if !errors.As(err, &streamErr) {
		return nil, false
	}
	reduced, ok := mapOpenAIStreamErrorPayload(r.providerID, streamErr.Event.Data, err, statusCode)
	if !ok {
		return nil, false
	}
	return reduced, true
}

func mapOpenAIStreamErrorPayload(providerID string, data []byte, cause error, statusCode int) (*ProviderAPIError, bool) {
	payload, ok := decodeOpenAIStreamErrorPayload(data)
	if !ok {
		return nil, false
	}
	return mapOpenAIProviderErrorContract(
		providerID,
		statusCode,
		payload.Code,
		payload.Type,
		payload.Param,
		payload.Message,
		string(data),
		cause,
	), true
}

func (r openAICompatibleErrorReducer) reduceFromSDK(err error) (*ProviderAPIError, bool) {
	if err == nil {
		return nil, false
	}
	var sdkErr *openai.Error
	if !errors.As(err, &sdkErr) {
		return nil, false
	}
	return mapOpenAIProviderErrorContract(
		r.providerID,
		sdkErr.StatusCode,
		sdkErr.Code,
		sdkErr.Type,
		sdkErr.Param,
		sdkErr.Message,
		sdkErr.RawJSON(),
		err,
	), true
}

func (r openAICompatibleErrorReducer) reduceFromResponse(rawResp *http.Response) (*ProviderAPIError, bool) {
	if rawResp == nil || rawResp.StatusCode < 300 {
		return nil, false
	}
	if rawResp.Body == nil {
		return &ProviderAPIError{
			ProviderID: r.providerID,
			StatusCode: rawResp.StatusCode,
			Code:       UnifiedErrorCodeUnknown,
			Message:    http.StatusText(rawResp.StatusCode),
			Raw:        "<empty error body>",
		}, true
	}
	body, _ := io.ReadAll(rawResp.Body)
	rawResp.Body.Close()
	rawResp.Body = io.NopCloser(bytes.NewReader(body))
	raw := truncateError(body)

	var payload struct {
		Error openai.Error `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		return mapOpenAIProviderErrorContract(
			r.providerID,
			rawResp.StatusCode,
			payload.Error.Code,
			payload.Error.Type,
			payload.Error.Param,
			payload.Error.Message,
			raw,
			nil,
		), true
	}

	return &ProviderAPIError{
		ProviderID: r.providerID,
		StatusCode: rawResp.StatusCode,
		Code:       UnifiedErrorCodeUnknown,
		Message:    raw,
		Raw:        raw,
	}, true
}

func (r opaqueProviderErrorReducer) reduceFromResponse(rawResp *http.Response) (*ProviderAPIError, bool) {
	if rawResp == nil || rawResp.StatusCode < 300 {
		return nil, false
	}
	if rawResp.Body == nil {
		code := UnifiedErrorCodeUnknown
		if rawResp.StatusCode == 401 || rawResp.StatusCode == 403 {
			code = UnifiedErrorCodeAuthentication
		}
		return &ProviderAPIError{
			ProviderID: r.providerID,
			StatusCode: rawResp.StatusCode,
			Code:       code,
			Message:    http.StatusText(rawResp.StatusCode),
			Raw:        "<empty error body>",
		}, true
	}
	body, _ := io.ReadAll(rawResp.Body)
	rawResp.Body.Close()
	rawResp.Body = io.NopCloser(bytes.NewReader(body))
	raw := truncateError(body)
	code := UnifiedErrorCodeUnknown
	if rawResp.StatusCode == 401 || rawResp.StatusCode == 403 {
		code = UnifiedErrorCodeAuthentication
	}
	return &ProviderAPIError{
		ProviderID: r.providerID,
		StatusCode: rawResp.StatusCode,
		Code:       code,
		Message:    raw,
		Raw:        raw,
	}, true
}

type openAIStreamErrorPayload struct {
	Type    string
	Code    string
	Param   string
	Message string
}

func decodeOpenAIStreamErrorPayload(data []byte) (openAIStreamErrorPayload, bool) {
	if len(bytes.TrimSpace(data)) == 0 || !json.Valid(data) {
		return openAIStreamErrorPayload{}, false
	}
	var envelope struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Param   string `json:"param"`
		Message string `json:"message"`
		Error   struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Param   string `json:"param"`
			Message string `json:"message"`
		} `json:"error"`
		Response struct {
			IncompleteDetails struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
			Error struct {
				Type    string `json:"type"`
				Code    string `json:"code"`
				Param   string `json:"param"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return openAIStreamErrorPayload{}, false
	}
	eventType := strings.TrimSpace(envelope.Type)
	payload := openAIStreamErrorPayload{
		Type:    eventType,
		Code:    strings.TrimSpace(envelope.Code),
		Param:   strings.TrimSpace(envelope.Param),
		Message: strings.TrimSpace(envelope.Message),
	}
	if strings.TrimSpace(envelope.Error.Code) != "" || strings.TrimSpace(envelope.Error.Message) != "" {
		payload = openAIStreamErrorPayload{
			Type:    strings.TrimSpace(envelope.Error.Type),
			Code:    strings.TrimSpace(envelope.Error.Code),
			Param:   strings.TrimSpace(envelope.Error.Param),
			Message: strings.TrimSpace(envelope.Error.Message),
		}
	}
	if strings.TrimSpace(envelope.Response.Error.Code) != "" || strings.TrimSpace(envelope.Response.Error.Message) != "" {
		payload = openAIStreamErrorPayload{
			Type:    strings.TrimSpace(envelope.Response.Error.Type),
			Code:    strings.TrimSpace(envelope.Response.Error.Code),
			Param:   strings.TrimSpace(envelope.Response.Error.Param),
			Message: strings.TrimSpace(envelope.Response.Error.Message),
		}
	}
	if eventType == "response.incomplete" {
		reason := strings.TrimSpace(envelope.Response.IncompleteDetails.Reason)
		if reason != "" {
			payload = openAIStreamErrorPayload{
				Type:    eventType,
				Code:    reason,
				Param:   "response.incomplete_details.reason",
				Message: "response incomplete: " + reason,
			}
		}
	}
	if payload.Code == "" && payload.Message == "" {
		return openAIStreamErrorPayload{}, false
	}
	return payload, true
}

func mapOpenAIProviderErrorContract(
	providerID string,
	statusCode int,
	providerCode string,
	providerType string,
	providerParam string,
	message string,
	raw string,
	cause error,
) *ProviderAPIError {
	return &ProviderAPIError{
		ProviderID:    providerID,
		StatusCode:    statusCode,
		Code:          classifyOpenAIUnifiedErrorCode(statusCode, providerCode),
		ProviderCode:  providerCode,
		ProviderType:  providerType,
		ProviderParam: providerParam,
		Message:       message,
		Raw:           raw,
		Err:           cause,
	}
}

func classifyOpenAIUnifiedErrorCode(statusCode int, providerCode string) UnifiedErrorCode {
	if statusCode == 401 || statusCode == 403 {
		return UnifiedErrorCodeAuthentication
	}
	switch strings.ToLower(strings.TrimSpace(providerCode)) {
	case "context_length_exceeded",
		"context_window_exceeded",
		"max_context_length_exceeded",
		"token_limit_exceeded",
		"prompt_too_long",
		"input_too_long":
		return UnifiedErrorCodeContextLengthOverflow
	default:
		return UnifiedErrorCodeUnknown
	}
}
