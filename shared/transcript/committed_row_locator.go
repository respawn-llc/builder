package transcript

import "fmt"

// CommittedRowLocator identifies one committed transcript row within a Session.
// EventSequence is the durable event-record sequence and RowOrdinal is the
// one-based projection ordinal within that event.
type CommittedRowLocator struct {
	EventSequence int64 `json:"event_sequence"`
	RowOrdinal    int64 `json:"row_ordinal"`
}

func (l CommittedRowLocator) Validate() error {
	if l.EventSequence <= 0 {
		return fmt.Errorf("committed row locator event sequence must be positive, got %d", l.EventSequence)
	}
	if l.RowOrdinal <= 0 {
		return fmt.Errorf("committed row locator row ordinal must be positive, got %d", l.RowOrdinal)
	}
	return nil
}
