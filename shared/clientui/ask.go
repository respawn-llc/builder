package clientui

import (
	"time"

	"core/shared/runtimeids"
)

type PendingAsk struct {
	ToolCallID             ToolCallID
	SessionID              runtimeids.SessionID
	StepID                 runtimeids.StepID
	Question               string
	Suggestions            []string
	RecommendedOptionIndex *int
	CreatedAt              time.Time
}
