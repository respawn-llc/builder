package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/server/llm"
	"core/server/session"
)

var errLatestCommittedAssistantFinalAnswerStoreRequired = errors.New("session store is required")

// LatestCommittedAssistantFinalAnswerFromStore finds the newest durable
// assistant final answer in the active transcript segment. A compaction
// boundary is an absence result and is never decoded as carried history.
func LatestCommittedAssistantFinalAnswerFromStore(store *session.Store) (*string, error) {
	if store == nil {
		return nil, errLatestCommittedAssistantFinalAnswerStoreRequired
	}

	var (
		answer   *string
		matchErr error
	)
	_, err := store.ReadNewestSegmentBackward(func(evt session.Event) bool {
		switch evt.Kind {
		case "message":
			var message llm.Message
			if err := json.Unmarshal(evt.Payload, &message); err != nil {
				matchErr = fmt.Errorf("decode message event seq %d: %w", evt.Seq, err)
				return true
			}
			if message.Role != llm.RoleAssistant ||
				message.Phase != llm.MessagePhaseFinal ||
				strings.TrimSpace(message.Content) == "" ||
				isNoopFinalAnswer(message) {
				return false
			}
			text := message.Content
			answer = &text
			return true
		case "history_replaced":
			_, ignoredLegacy, err := decodePersistedHistoryReplacementPayload(evt.Payload)
			if err != nil {
				matchErr = fmt.Errorf("%w seq %d: %w", errDecodeHistoryReplacedEvent, evt.Seq, err)
				return true
			}
			if !ignoredLegacy {
				answer = nil
				return true
			}
			return false
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
