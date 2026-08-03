package clientui

import "time"

type PendingAsk struct {
	AskID                  string
	SessionID              string
	Question               string
	Suggestions            []string
	RecommendedOptionIndex *int
	CreatedAt              time.Time
}
