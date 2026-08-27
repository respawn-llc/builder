package llm

import (
	"errors"
	"fmt"

	"core/shared/llmerrors"
)

var ErrModelStreamStalled = llmerrors.ErrModelStreamStalled

type APIStatusError = llmerrors.APIStatusError
type UnifiedErrorCode = llmerrors.UnifiedErrorCode
type ProviderAPIError = llmerrors.ProviderAPIError
type AuthError = llmerrors.AuthError
type ProviderSelectionError = llmerrors.ProviderSelectionError

type CompactionCheckpointReason string

const (
	CompactionCheckpointReasonZero                    CompactionCheckpointReason = "zero_checkpoints"
	CompactionCheckpointReasonMultiple                CompactionCheckpointReason = "multiple_checkpoints"
	CompactionCheckpointReasonMissingEncryptedContent CompactionCheckpointReason = "missing_encrypted_content"
)

type CompactionCheckpointContractError struct {
	Reason           CompactionCheckpointReason
	CompactionCount  int
	OutputCount      int
	OutputTypeCounts map[ResponseItemType]int
}

func (e *CompactionCheckpointContractError) Error() string {
	if e == nil {
		return "compaction checkpoint contract error"
	}
	return fmt.Sprintf(
		"Responses compaction V2 checkpoint contract violation (reason=%s compaction_count=%d output_count=%d output_type_counts=%v)",
		e.Reason,
		e.CompactionCount,
		e.OutputCount,
		e.OutputTypeCounts,
	)
}

const (
	UnifiedErrorCodeUnknown               = llmerrors.UnifiedErrorCodeUnknown
	UnifiedErrorCodeAuthentication        = llmerrors.UnifiedErrorCodeAuthentication
	UnifiedErrorCodeContextLengthOverflow = llmerrors.UnifiedErrorCodeContextLengthOverflow
	UnifiedErrorCodeProviderContract      = llmerrors.UnifiedErrorCodeProviderContract
	UnifiedErrorCodeProviderOverload      = llmerrors.UnifiedErrorCodeProviderOverload
)

func IsAuthenticationError(err error) bool {
	return llmerrors.IsAuthenticationError(err)
}

func IsNonRetriableModelError(err error) bool {
	return errors.Is(err, ErrInvalidRequest) || errors.Is(err, ErrUnsupportedToolChoicePolicy) || llmerrors.IsNonRetriableModelError(err)
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
