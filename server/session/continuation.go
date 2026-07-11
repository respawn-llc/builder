package session

import (
	"errors"
	"fmt"
	"strings"

	"core/shared/config"
)

var ErrInvalidContinuationAgentRole = errors.New("invalid continuation agent role")

// NormalizeContinuationContext validates persisted continuation metadata at
// every persistence boundary. An omitted or JSON-null role is the sole
// default-agent encoding.
func NormalizeContinuationContext(ctx ContinuationContext) (*ContinuationContext, error) {
	normalized := ContinuationContext{OpenAIBaseURL: strings.TrimSpace(ctx.OpenAIBaseURL)}
	if ctx.AgentRole != nil {
		raw := *ctx.AgentRole
		role := config.NormalizeSubagentSelector(raw)
		if strings.TrimSpace(raw) == "" || role == "" {
			return nil, fmt.Errorf("%w: %q", ErrInvalidContinuationAgentRole, raw)
		}
		normalized.AgentRole = &role
	}
	if normalized.OpenAIBaseURL == "" && normalized.AgentRole == nil {
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
