package runtime

import (
	"strings"

	"core/server/llm"
	"core/server/session"
)

func normalizeHistoryReplacementEngine(engine string) string {
	engine = strings.TrimSpace(engine)
	if session.IsLegacyReviewerRollbackHistoryReplacementEngine(engine) {
		return ""
	}
	return engine
}

func isCompactionEventRecordBoundary(record session.EventRecord) (bool, error) {
	payload, err := record.Payload()
	if err != nil {
		return false, err
	}
	_, ok := payload.(session.HistoryReplacementRecord)
	return ok, nil
}

func compactionBoundaryMatcher(matchErr *error) func(session.EventRecord) bool {
	return func(record session.EventRecord) bool {
		matches, err := isCompactionEventRecordBoundary(record)
		if err != nil {
			*matchErr = err
			return true
		}
		return matches
	}
}

func transcriptEntriesFromHistoryReplacement(items []llm.ResponseItem) []ChatEntry {
	if len(items) == 0 {
		return nil
	}
	entries := visibleChatEntriesFromResponseItems(items)
	if len(entries) == 0 {
		return nil
	}
	out := make([]ChatEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, clonePersistedChatEntry(entry))
	}
	return out
}
