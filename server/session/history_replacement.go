package session

import (
	"encoding/json"
	"strings"
)

const LegacyReviewerRollbackHistoryReplacementEngine = "reviewer_rollback"

type historyReplacementEngine struct {
	Engine string `json:"engine"`
}

func IsLegacyReviewerRollbackHistoryReplacementEngine(engine string) bool {
	return strings.TrimSpace(engine) == LegacyReviewerRollbackHistoryReplacementEngine
}

func IsCompactionHistoryReplacementPayload(payload []byte) bool {
	var envelope historyReplacementEngine
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return false
	}
	return !IsLegacyReviewerRollbackHistoryReplacementEngine(envelope.Engine)
}

func isCompactionHistoryReplacementEvent(event Event) bool {
	return event.Kind == "history_replaced" && IsCompactionHistoryReplacementPayload(event.Payload)
}
