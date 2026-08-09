package runtime

import (
	"fmt"
	"strings"

	"core/server/llm"
	"core/server/session"
)

// LatestCommittedAssistantFinalAnswerFromEventLog finds the newest durable
// assistant final answer in the active transcript segment. A compaction
// boundary is an absence result and is never decoded as carried history.
func LatestCommittedAssistantFinalAnswerFromEventLog(eventLog session.MaterializedEventLog) (*string, error) {
	var (
		answer   *string
		matchErr error
	)
	_, err := eventLog.ReadNewestSegmentBackward(func(record session.EventRecord) bool {
		payload, payloadErr := record.Payload()
		if payloadErr != nil {
			matchErr = payloadErr
			return true
		}
		switch payload := payload.(type) {
		case session.MessageRecord:
			message, restoreErr := llmMessageFromSessionRecord(payload)
			if restoreErr != nil {
				matchErr = fmt.Errorf("restore session message record seq %d: %w", record.Seq(), restoreErr)
				return true
			}
			if message.Role != llm.RoleAssistant ||
				message.Phase == nil ||
				*message.Phase != llm.MessagePhaseFinal ||
				message.Content == nil ||
				strings.TrimSpace(*message.Content) == "" ||
				isBlankFinalAnswer(message) {
				return false
			}
			text := *message.Content
			answer = &text
			matchErr = nil
			return true
		case session.HistoryReplacementRecord:
			_, restoreErr := historyReplacementPayloadFromSessionRecord(payload)
			if restoreErr != nil {
				matchErr = fmt.Errorf("restore session history replacement record seq %d: %w", record.Seq(), restoreErr)
				return true
			}
			answer = nil
			matchErr = nil
			return true
		default:
			return false
		}
	})
	if err != nil {
		return nil, fmt.Errorf("read newest transcript segment: %w", err)
	}
	if matchErr != nil {
		return nil, matchErr
	}
	return answer, nil
}
