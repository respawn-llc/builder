package session

import "strings"

const LegacyReviewerRollbackHistoryReplacementEngine = "reviewer_rollback"

func IsLegacyReviewerRollbackHistoryReplacementEngine(engine string) bool {
	return strings.TrimSpace(engine) == LegacyReviewerRollbackHistoryReplacementEngine
}
