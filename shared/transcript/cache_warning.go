package transcript

import (
	"fmt"
	"strings"
)

type CacheWarningScope string
type CacheWarningReason string

const (
	CacheWarningScopeConversation CacheWarningScope = "conversation"
	CacheWarningScopeReviewer     CacheWarningScope = "reviewer"

	// CacheWarningReasonCompaction is retained for persisted legacy warnings only.
	// Active runtime cache-lineage logic should rotate cache keys instead of
	// emitting a synthetic compaction invalidation warning.
	CacheWarningReasonCompaction   CacheWarningReason = "compaction"
	CacheWarningReasonNonPostfix   CacheWarningReason = "non_postfix"
	CacheWarningReasonReuseDropped CacheWarningReason = "reuse_dropped"
)

type CacheWarning struct {
	Scope           CacheWarningScope  `json:"scope"`
	Reason          CacheWarningReason `json:"reason"`
	CacheKey        *string            `json:"cache_key,omitempty"`
	LostInputTokens *int               `json:"lost_input_tokens,omitempty"`
}

func CacheWarningText(w CacheWarning) string {
	text := fmt.Sprintf("Cache miss: %s", reasonText(w))
	if w.LostInputTokens == nil || *w.LostInputTokens <= 0 {
		return text
	}
	return fmt.Sprintf("%s, -%s tokens", text, formatTokenDeltaThousands(*w.LostInputTokens))
}

func reasonText(w CacheWarning) string {
	switch w.Reason {
	case CacheWarningReasonCompaction:
		return "compaction"
	case CacheWarningReasonNonPostfix:
		if w.Scope == CacheWarningScopeReviewer {
			return "supervisor request was not a postfix of the previous request for the same cache key"
		}
		return "request was not a postfix of the previous request for the same cache key"
	case CacheWarningReasonReuseDropped:
		if w.Scope == CacheWarningScopeReviewer {
			return "postfix-compatible supervisor cache reuse disappeared"
		}
		return "postfix-compatible cache reuse disappeared"
	default:
		trimmed := strings.TrimSpace(string(w.Reason))
		if trimmed == "" {
			return "unknown reason"
		}
		return trimmed
	}
}

func formatTokenDeltaThousands(tokens int) string {
	if tokens < 0 {
		tokens = 0
	}
	if tokens < 10_000 {
		thousands := float64(tokens) / 1000.0
		formatted := fmt.Sprintf("%.1f", thousands)
		formatted = strings.TrimSuffix(formatted, ".0")
		return formatted + "k"
	}
	rounded := (tokens + 500) / 1000
	return fmt.Sprintf("%dk", rounded)
}
