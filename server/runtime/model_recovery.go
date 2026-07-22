package runtime

import (
	"strings"
	"time"

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
