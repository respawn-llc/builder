package session

import (
	"errors"
	"fmt"
)

const activeWorkflowAssignmentMigrationWindow = 256

var ErrActiveWorkflowAssignmentProjectionUnavailable = errors.New(
	"active workflow assignment projection is unavailable",
)

// EnsureActiveWorkflowAssignmentProjection performs a bounded, one-time
// initialization for Sessions created before the projection was persisted.
func (s *Store) EnsureActiveWorkflowAssignmentProjection() error {
	if s == nil {
		return errors.New("session store is required")
	}
	if s.Meta().ActiveWorkflowAssignmentKnown {
		return nil
	}
	eventLog, err := s.MaterializeEventLog()
	if err != nil {
		return err
	}
	window, err := eventLog.ReadRecentRecords(activeWorkflowAssignmentMigrationWindow)
	if err != nil {
		return err
	}
	var (
		projected *MessageRecord
		resolved  bool
	)
	for index := len(window.Records) - 1; index >= 0; index-- {
		record := window.Records[index]
		payload, payloadErr := record.Payload()
		if payloadErr != nil {
			return payloadErr
		}
		switch value := payload.(type) {
		case MessageRecord:
			if value.MessageType == nil {
				continue
			}
			switch *value.MessageType {
			case MessageTypeWorkflowMode:
				projected = cloneMessageRecord(&value)
				resolved = true
			case MessageTypeWorkflowModeExit:
				resolved = true
			}
		case HistoryReplacementRecord:
			meta := Meta{}
			if projectionErr := advanceActiveWorkflowAssignmentFromRecords(&meta, []EventRecord{record}); projectionErr != nil {
				return projectionErr
			}
			projected = cloneMessageRecord(meta.ActiveWorkflowAssignment)
			resolved = true
		}
		if resolved {
			break
		}
	}
	if !resolved && !window.ReachedStart {
		return fmt.Errorf(
			"%w within the most recent %d records",
			ErrActiveWorkflowAssignmentProjectionUnavailable,
			activeWorkflowAssignmentMigrationWindow,
		)
	}
	return s.mutateAndPersist(func() error {
		if s.meta.ActiveWorkflowAssignmentKnown {
			return nil
		}
		s.meta.ActiveWorkflowAssignment = cloneMessageRecord(projected)
		s.meta.ActiveWorkflowAssignmentKnown = true
		return nil
	})
}
