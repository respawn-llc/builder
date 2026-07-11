package runtime

import (
	"encoding/json"
	"strings"
	"time"

	"core/server/llm"
	"core/server/session"

	"github.com/google/uuid"
)

func (e *Engine) markProviderVisibleModelRecovery(stepID string) error {
	if e == nil || e.store == nil || strings.TrimSpace(stepID) == "" {
		return nil
	}
	if current := e.store.Meta().PendingModelRecovery; current != nil && strings.TrimSpace(current.StepID) == strings.TrimSpace(stepID) {
		return nil
	}
	return e.store.SetPendingModelRecovery(session.PendingModelRecovery{
		RecoveryID: uuid.NewString(),
		StepID:     strings.TrimSpace(stepID),
		Reason:     "provider_visible_output_persisted",
		CreatedAt:  time.Now().UTC(),
	})
}

func cloneSessionPendingModelRecovery(recovery *session.PendingModelRecovery) session.PendingModelRecovery {
	if recovery == nil {
		return session.PendingModelRecovery{}
	}
	cloned := *recovery
	cloned.OutstandingToolCallIDs = append([]string(nil), recovery.OutstandingToolCallIDs...)
	return cloned
}

func (e *Engine) inferLegacyPendingModelRecovery(candidate session.PendingModelRecovery) (session.PendingModelRecovery, bool, error) {
	if e == nil || e.store == nil {
		return candidate, false, nil
	}
	events, err := e.store.ReadEventsBackwardUntil(isCompactionSegmentBoundary)
	if err != nil {
		return session.PendingModelRecovery{}, false, err
	}
	for i := len(events) - 1; i >= 0; i-- {
		stepID := strings.TrimSpace(events[i].StepID)
		if stepID == "" || !legacyRecoveryInferenceEvent(events[i]) {
			continue
		}
		inferred := cloneSessionPendingModelRecovery(&candidate)
		inferred.StepID = stepID
		if strings.TrimSpace(inferred.RecoveryID) == "" || strings.TrimSpace(inferred.RecoveryID) == "legacy-in-flight" {
			inferred.RecoveryID = uuid.NewString()
		}
		if strings.TrimSpace(inferred.Reason) == "" {
			inferred.Reason = "legacy_in_flight_step"
		}
		if inferred.CreatedAt.IsZero() {
			inferred.CreatedAt = time.Now().UTC()
		}
		return inferred, true, nil
	}
	return candidate, false, nil
}

func legacyRecoveryInferenceEvent(evt session.Event) bool {
	switch evt.Kind {
	case "message", "tool_completed", "local_entry":
	default:
		return false
	}
	if evt.Kind != "message" {
		return true
	}
	var msg llm.Message
	if err := json.Unmarshal(evt.Payload, &msg); err != nil {
		return true
	}
	return msg.Role == llm.RoleAssistant || msg.Role == llm.RoleTool || msg.Role == llm.RoleDeveloper || msg.Role == llm.RoleUser
}
