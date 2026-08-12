package session

import (
	"errors"
	"fmt"
	"strings"

	"core/shared/config"
	"core/shared/textutil"
)

var ErrInvalidContinuationAgentRole = errors.New("invalid continuation agent role")
var ErrInvalidContinuationOpenAIBaseURL = errors.New("invalid continuation OpenAI base URL")

// ContinuationAgentRole returns the persisted named role, if one was selected.
func ContinuationAgentRole(meta Meta) *string {
	if meta.Continuation == nil {
		return nil
	}
	return textutil.Pointer(meta.Continuation.AgentRole)
}

// NormalizeContinuationContext validates persisted continuation metadata at
// every persistence boundary. Omitted or JSON-null values encode absence;
// present blank values are invalid.
func NormalizeContinuationContext(ctx ContinuationContext) (*ContinuationContext, error) {
	normalized := ContinuationContext{}
	if ctx.OpenAIBaseURL != nil {
		baseURL := strings.TrimSpace(*ctx.OpenAIBaseURL)
		if baseURL == "" {
			return nil, fmt.Errorf("%w: %q", ErrInvalidContinuationOpenAIBaseURL, *ctx.OpenAIBaseURL)
		}
		normalized.OpenAIBaseURL = &baseURL
	}
	if ctx.AgentRole != nil {
		raw := *ctx.AgentRole
		role := config.NormalizeSubagentSelector(raw)
		if strings.TrimSpace(raw) == "" || role == "" {
			return nil, fmt.Errorf("%w: %q", ErrInvalidContinuationAgentRole, raw)
		}
		normalized.AgentRole = &role
	}
	if normalized.OpenAIBaseURL == nil && normalized.AgentRole == nil {
		return nil, nil
	}
	return &normalized, nil
}

func normalizeMetaContinuation(meta *Meta) error {
	if meta == nil || meta.Continuation == nil {
		return nil
	}
	normalized, err := NormalizeContinuationContext(*meta.Continuation)
	if err != nil {
		return err
	}
	meta.Continuation = normalized
	return nil
}
