package session

import "errors"

var ErrActiveWorkflowAssignmentProjectionUnavailable = errors.New(
	"active workflow assignment projection is unavailable",
)

// ActiveWorkflowAssignmentProjection returns the authoritative current
// assignment. A nil assignment is valid only when the persisted state marker
// proves that absence.
func (s *Store) ActiveWorkflowAssignmentProjection() (*MessageRecord, error) {
	if s == nil {
		return nil, errors.New("session store is required")
	}
	meta := s.Meta()
	if meta.ActiveWorkflowAssignmentState == nil && meta.ActiveWorkflowAssignment == nil {
		return nil, ErrActiveWorkflowAssignmentProjectionUnavailable
	}
	return cloneMessageRecord(meta.ActiveWorkflowAssignment), nil
}
