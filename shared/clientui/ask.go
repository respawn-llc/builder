package clientui

import (
	"time"

	"core/shared/runtimeids"
)

type PendingAsk struct {
	PromptID               PromptID
	SessionID              runtimeids.SessionID
	StepID                 runtimeids.StepID
	Question               string
	Suggestions            []string
	RecommendedOptionIndex *int
	CreatedAt              time.Time
}
