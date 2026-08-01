package session

import (
	"errors"
	"fmt"
	"strings"
)

// AgentStepBoundaryRecord is a non-transcript fact that a provider iteration
// reached its durable Step Boundary for the originating Session.
type AgentStepBoundaryRecord struct {
	SessionID string `json:"session_id"`
}

func (AgentStepBoundaryRecord) eventKind() EventKind {
	return EventKindAgentStepBoundary
}

func (r AgentStepBoundaryRecord) validate() error {
	if strings.TrimSpace(r.SessionID) == "" {
		return errors.New("originating session id is required")
	}
	return nil
}

func normalizeAgentStepBoundaryRecord(record AgentStepBoundaryRecord) (AgentStepBoundaryRecord, error) {
	record.SessionID = strings.TrimSpace(record.SessionID)
	if err := record.validate(); err != nil {
		return AgentStepBoundaryRecord{}, fmt.Errorf("agent step boundary: %w", err)
	}
	return record, nil
}
