package llmerrors

import (
	"errors"
	"fmt"
	"strings"

	"core/shared/auth"
)

var ErrModelStreamStalled = errors.New("model stream stalled")

type APIStatusError struct {
	StatusCode int
	Body       string
}

func (e *APIStatusError) Error() string {
	if e == nil {
		return "openai status error"
	}
	return fmt.Sprintf("openai status %d: %s", e.StatusCode, e.Body)
}

type UnifiedErrorCode string

const (
	UnifiedErrorCodeUnknown               UnifiedErrorCode = "unknown"
	UnifiedErrorCodeAuthentication        UnifiedErrorCode = "authentication"
	UnifiedErrorCodeContextLengthOverflow UnifiedErrorCode = "context_length_overflow"
	UnifiedErrorCodeProviderContract      UnifiedErrorCode = "provider_contract_error"
)

const providerTypeResponseIncomplete = "response.incomplete"

type ProviderAPIError struct {
	ProviderID    string
	StatusCode    int
	Code          UnifiedErrorCode
	ProviderCode  string
	ProviderType  string
	ProviderParam string
	Message       string
	Raw           string
	Err           error
}

func NewProviderContractError(providerID string, statusCode int, cause error) *ProviderAPIError {
	message := "provider contract error"
	if cause != nil {
		message = cause.Error()
	}
	return &ProviderAPIError{
		ProviderID: providerID,
		StatusCode: statusCode,
		Code:       UnifiedErrorCodeProviderContract,
		Message:    message,
		Raw:        message,
		Err:        cause,
	}
}

func (e *ProviderAPIError) Error() string {
	if e == nil {
		return "provider api error"
	}
	return fmt.Sprintf("%s status %d [%s/%s]: %s", e.ProviderID, e.StatusCode, e.Code, e.ProviderCode, e.Message)
}

func (e *ProviderAPIError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type AuthError struct {
	Err error
}

type ProviderSelectionError struct {
	Model string
	Err   error
}

func (e *ProviderSelectionError) Error() string {
	if e == nil {
		return "provider selection error"
	}
	model := strings.TrimSpace(e.Model)
	if model == "" {
		return "could not determine which provider/auth path to use; set provider_override or openai_base_url"
	}
	return fmt.Sprintf("could not determine which provider/auth path to use for model %q; set provider_override or openai_base_url", model)
}

func (e *ProviderSelectionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *AuthError) Error() string {
	if e == nil || e.Err == nil {
		return "authentication error"
	}
	return e.Err.Error()
}

func (e *AuthError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func IsAuthenticationError(err error) bool {
	if err == nil {
		return false
	}
	var authErr *AuthError
	if errors.As(err, &authErr) {
		return true
	}
	var providerErr *ProviderAPIError
	if errors.As(err, &providerErr) {
		if providerErr.Code == UnifiedErrorCodeAuthentication {
			return true
		}
		switch providerErr.StatusCode {
		case 401, 403:
			return true
		}
	}
	var apiErr *APIStatusError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case 401, 403:
			return true
		}
	}
	return false
}

func IsNonRetriableModelError(err error) bool {
	if err == nil {
		return false
	}
	var providerSelectionErr *ProviderSelectionError
	if errors.As(err, &providerSelectionErr) {
		return true
	}
	if IsAuthenticationError(err) {
		return true
	}
	var providerErr *ProviderAPIError
	if errors.As(err, &providerErr) {
		if providerErr.ProviderType == providerTypeResponseIncomplete {
			return true
		}
		if providerErr.Code == UnifiedErrorCodeProviderContract {
			return true
		}
		switch providerErr.StatusCode {
		case 400, 401, 403, 404:
			return true
		}
	}
	var apiErr *APIStatusError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case 400, 401, 403, 404:
			return true
		}
	}
	return false
}

func IsContextLengthOverflowError(err error) bool {
	if err == nil {
		return false
	}
	var providerErr *ProviderAPIError
	if !errors.As(err, &providerErr) {
		return false
	}
	return providerErr.Code == UnifiedErrorCodeContextLengthOverflow
}

// HasHTTPStatus reports whether err (or anything it wraps) carries the given
// provider HTTP status code. Provider transports surface status either as a
// ProviderAPIError or a raw APIStatusError, so both shapes are inspected.
func HasHTTPStatus(err error, statusCode int) bool {
	if err == nil {
		return false
	}
	var providerErr *ProviderAPIError
	if errors.As(err, &providerErr) && providerErr.StatusCode == statusCode {
		return true
	}
	var apiErr *APIStatusError
	if errors.As(err, &apiErr) && apiErr.StatusCode == statusCode {
		return true
	}
	return false
}

func UserFacingError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrModelStreamStalled) {
		return "The model request stalled: no streaming activity within the timeout window (timeouts.model_request_seconds). The model may be overloaded; retry, or raise the timeout."
	}
	var providerSelectionErr *ProviderSelectionError
	if errors.As(err, &providerSelectionErr) {
		return providerSelectionErr.Error()
	}
	var authErr *AuthError
	if errors.As(err, &authErr) && errors.Is(authErr.Err, auth.ErrAuthNotConfigured) {
		return "Not authenticated, run /login to sign in with your provider"
	}
	if errors.Is(err, auth.ErrAuthNotConfigured) {
		return "Not authenticated, run /login to sign in with your provider"
	}
	var providerErr *ProviderAPIError
	if errors.As(err, &providerErr) {
		if providerErr.Code == UnifiedErrorCodeAuthentication || providerErr.StatusCode == 401 || providerErr.StatusCode == 403 {
			return authenticationFailedWarning(providerErr.ProviderID, providerErr.StatusCode)
		}
	}
	var statusErr *APIStatusError
	if errors.As(err, &statusErr) {
		if statusErr.StatusCode == 401 || statusErr.StatusCode == 403 {
			return authenticationFailedWarning("OpenAI-compatible API", statusErr.StatusCode)
		}
	}
	return ""
}

func authenticationFailedWarning(provider string, statusCode int) string {
	label := strings.TrimSpace(provider)
	if label == "" {
		label = "API"
	}
	if statusCode > 0 {
		return fmt.Sprintf("Authentication failed with %s (HTTP %d). Run /login or check OPENAI_API_KEY. If you're using a custom or local OpenAI-compatible server that needs no auth, set openai_base_url and continue without Kent auth.", label, statusCode)
	}
	return fmt.Sprintf("Authentication failed with %s. Run /login or check OPENAI_API_KEY. If you're using a custom or local OpenAI-compatible server that needs no auth, set openai_base_url and continue without Kent auth.", label)
}
