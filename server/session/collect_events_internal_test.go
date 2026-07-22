package session

// collectRecords accumulates the full typed event history for in-package tests.
func collectEvents(s *Store) ([]EventRecord, error) {
	log, err := s.MaterializeEventLog()
	if err != nil {
		return nil, err
	}
	records := make([]EventRecord, 0)
	if err := log.WalkRecords(func(record EventRecord) error {
		records = append(records, record)
		return nil
	}); err != nil {
		return nil, err
	}
	return records, nil
}
