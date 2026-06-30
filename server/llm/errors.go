package llm

import "core/shared/llmerrors"

var ErrModelStreamStalled = llmerrors.ErrModelStreamStalled

type APIStatusError = llmerrors.APIStatusError
type UnifiedErrorCode = llmerrors.UnifiedErrorCode
type ProviderAPIError = llmerrors.ProviderAPIError
type AuthError = llmerrors.AuthError
type ProviderSelectionError = llmerrors.ProviderSelectionError

const (
	UnifiedErrorCodeUnknown               = llmerrors.UnifiedErrorCodeUnknown
	UnifiedErrorCodeAuthentication        = llmerrors.UnifiedErrorCodeAuthentication
	UnifiedErrorCodeContextLengthOverflow = llmerrors.UnifiedErrorCodeContextLengthOverflow
	UnifiedErrorCodeProviderContract      = llmerrors.UnifiedErrorCodeProviderContract
)

func IsAuthenticationError(err error) bool {
	return llmerrors.IsAuthenticationError(err)
}

func IsNonRetriableModelError(err error) bool {
	return llmerrors.IsNonRetriableModelError(err)
}

func IsContextLengthOverflowError(err error) bool {
	return llmerrors.IsContextLengthOverflowError(err)
}

func HasHTTPStatus(err error, statusCode int) bool {
	return llmerrors.HasHTTPStatus(err, statusCode)
}

func UserFacingError(err error) string {
	return llmerrors.UserFacingError(err)
}
