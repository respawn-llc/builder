package runtime

import (
	"fmt"
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

func transcriptEntriesFromHistoryReplacement(items []llm.ResponseItem, compactionNumber *int) []ChatEntry {
	entries := make([]ChatEntry, 0, len(items)+1)
	hasCompactionSummary := false
	walker := newResponseItemMessageWalker(func(msg llm.Message) {
		if entry, ok := preservedUserMessageEntry(msg); ok {
			entries = append(entries, entry)
			return
		}
		for _, entry := range VisibleChatEntriesFromMessage(msg) {
			if entry.MessageType == llm.MessageTypeCompactionSummary {
				hasCompactionSummary = true
			}
			entries = append(entries, clonePersistedChatEntry(entry))
		}
	})
	for _, item := range items {
		walker.Apply(item)
	}
	walker.Flush()

	// Empty legacy replacements are segment boundaries only. Non-empty
	// replacements represent compacted working sets and always receive a notice.
	if !hasCompactionSummary && len(items) > 0 {
		entries = append(
			[]ChatEntry{syntheticCompactionSummaryEntry(compactionNumber)},
			entries...,
		)
	}
	return entries
}

func preservedUserMessageEntry(msg llm.Message) (ChatEntry, bool) {
	if msg.Role != llm.RoleUser || msg.MessageType != nil || msg.Content == nil ||
		strings.TrimSpace(*msg.Content) == "" {
		return ChatEntry{}, false
	}
	// manual_compaction_carryover is the legacy wire name for any user message
	// preserved across a compaction boundary.
	messageType := llm.MessageTypeCompactionPreservedUserMessage
	preserved := msg
	preserved.Role = llm.RoleDeveloper
	preserved.MessageType = &messageType
	entry, ok := visibleDeveloperChatEntry(preserved)
	if !ok {
		return ChatEntry{}, false
	}
	return clonePersistedChatEntry(entry), true
}

func syntheticCompactionSummaryEntry(compactionNumber *int) ChatEntry {
	messageType := llm.MessageTypeCompactionSummary
	label := compactLabelForMessage(llm.Message{MessageType: &messageType})
	if compactionNumber != nil {
		label = compactionSummaryLabel(*compactionNumber)
	}
	return compactionSummaryChatEntry(llm.Message{
		Role:           llm.RoleUser,
		MessageType:    &messageType,
		Content:        &label,
		CompactContent: &label,
	})
}

func compactionSummaryLabel(compactionNumber int) string {
	return fmt.Sprintf(
		"Context compacted for the %s time.",
		ordinal(compactionNumber),
	)
}
