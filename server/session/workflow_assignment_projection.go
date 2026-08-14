package session

import (
	"errors"
	"fmt"
)

const activeWorkflowAssignmentMigrationMaxRecords = 4096

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
