package session

import (
	"errors"
	"fmt"
)

const activeWorkflowAssignmentMigrationMaxRecords = 4096

func advanceActiveWorkflowAssignmentFromRecords(meta *Meta, records []EventRecord) error {
	for _, record := range records {
		payload, err := record.Payload()
		if err != nil {
			return err
		}
		switch value := payload.(type) {
		case MessageRecord:
			if value.MessageType == nil {
				continue
			}
			switch *value.MessageType {
			case MessageTypeWorkflowMode:
				meta.ActiveWorkflowAssignment = cloneMessageRecord(&value)
				meta.ActiveWorkflowAssignmentState = &ActiveWorkflowAssignmentState{}
			case MessageTypeWorkflowModeExit:
				meta.ActiveWorkflowAssignment = nil
				meta.ActiveWorkflowAssignmentState = &ActiveWorkflowAssignmentState{}
			}
		case HistoryReplacementRecord:
			meta.ActiveWorkflowAssignment = nil
			meta.ActiveWorkflowAssignmentState = &ActiveWorkflowAssignmentState{}
			for _, item := range value.Items {
				if item.Type != ProviderHistoryItemTypeMessage ||
					item.Role == nil ||
					*item.Role != MessageRoleDeveloper ||
					item.MessageType == nil {
					continue
				}
				switch *item.MessageType {
				case MessageTypeWorkflowMode:
					message, err := normalizeMessageRecord(MessageRecord{
						Role:            *item.Role,
						MessageType:     item.MessageType,
						SourcePath:      item.SourcePath,
						WorktreeContext: item.WorktreeContext,
						Content:         item.Content,
						CompactContent:  item.CompactContent,
					})
					if err != nil {
						return err
					}
					meta.ActiveWorkflowAssignment = &message
				case MessageTypeWorkflowModeExit:
					meta.ActiveWorkflowAssignment = nil
				}
			}
		}
	}
	return nil
}

// ActiveWorkflowAssignmentProjection returns the authoritative current
// assignment. Sessions persisted before this projection existed are normalized
// once at this boundary; subsequent reads use the persisted projection.
func (s *Store) ActiveWorkflowAssignmentProjection() (*MessageRecord, error) {
	if s == nil {
		return nil, errors.New("session store is required")
	}
	meta := s.Meta()
	if meta.ActiveWorkflowAssignmentState == nil {
		if err := s.migrateActiveWorkflowAssignmentProjection(meta); err != nil {
			return nil, err
		}
		meta = s.Meta()
	}
	return cloneMessageRecord(meta.ActiveWorkflowAssignment), nil
}

func (s *Store) migrateActiveWorkflowAssignmentProjection(meta Meta) error {
	projected := Meta{
		ActiveWorkflowAssignment:      cloneMessageRecord(meta.ActiveWorkflowAssignment),
		ActiveWorkflowAssignmentState: cloneActiveWorkflowAssignmentState(meta.ActiveWorkflowAssignmentState),
	}
	if projected.ActiveWorkflowAssignmentState == nil && projected.ActiveWorkflowAssignment != nil {
		projected.ActiveWorkflowAssignmentState = &ActiveWorkflowAssignmentState{}
	}
	if projected.ActiveWorkflowAssignmentState == nil {
		eventLog, err := s.MaterializeEventLog()
		if err != nil {
			return err
		}
		window, err := eventLog.ReadRecentRecords(activeWorkflowAssignmentMigrationMaxRecords)
		if err != nil {
			return err
		}
		if err := advanceActiveWorkflowAssignmentFromRecords(&projected, window.Records); err != nil {
			return err
		}
		if projected.ActiveWorkflowAssignmentState == nil {
			if !window.ReachedStart {
				return fmt.Errorf(
					"cannot migrate workflow assignment from the most recent %d Session records",
					activeWorkflowAssignmentMigrationMaxRecords,
				)
			}
			projected.ActiveWorkflowAssignmentState = &ActiveWorkflowAssignmentState{}
		}
	}
	return s.mutateAndPersist(func() error {
		if s.meta.ActiveWorkflowAssignmentState != nil {
			return nil
		}
		s.meta.ActiveWorkflowAssignment = cloneMessageRecord(projected.ActiveWorkflowAssignment)
		s.meta.ActiveWorkflowAssignmentState = cloneActiveWorkflowAssignmentState(
			projected.ActiveWorkflowAssignmentState,
		)
		return nil
	})
}
