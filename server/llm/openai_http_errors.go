package llm

import (
	"fmt"
	"net/http"
	"strings"

	"core/shared/llmerrors"
)

type openAIRequestErrorMapper struct {
	providerID string
}

func newOpenAIRequestErrorMapper(providerID string) openAIRequestErrorMapper {
	return openAIRequestErrorMapper{providerID: providerID}
}

func (m openAIRequestErrorMapper) Map(err error, rawResp *http.Response, prefix string) error {
	reducer, reducerErr := providerErrorReducerForID(m.providerID)
	if reducerErr != nil {
		statusCode := 0
		if rawResp != nil {
			statusCode = rawResp.StatusCode
			if rawResp.Body != nil {
				rawResp.Body.Close()
				rawResp.Body = nil
			}
		}
		return fmt.Errorf("%s: %w", prefix, llmerrors.NewProviderContractError(m.providerID, statusCode, reducerErr))
	}
	reducedErr, ok := reducer.Reduce(err, rawResp)
	if ok && reducedErr != nil {
		enrichProviderAPIErrorFromResponseHeaders(reducedErr, rawResp)
		return fmt.Errorf("%s: %w", prefix, reducedErr)
	}
	if err == nil {
		return fmt.Errorf("%s: unknown error", prefix)
	}
	return fmt.Errorf("%s: %w", prefix, err)
}

func enrichProviderAPIErrorFromResponseHeaders(providerErr *ProviderAPIError, rawResp *http.Response) {
	if providerErr == nil || rawResp == nil {
		return
	}
	providerErr.ProviderRequestID = firstNonblankHeaderValue(rawResp.Header, "x-request-id")
	if providerErr.ProviderRequestID == nil {
		providerErr.ProviderRequestID = firstNonblankHeaderValue(rawResp.Header, "x-oai-request-id")
	}
	providerErr.AuthorizationDiagnostic = firstNonblankHeaderValue(rawResp.Header, "x-openai-authorization-error")
}

func firstNonblankHeaderValue(header http.Header, name string) *string {
	for _, value := range header.Values(name) {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return &trimmed
		}
	}
	return nil
}

func truncateError(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return "<empty error body>"
	}
	return trimmed
}
