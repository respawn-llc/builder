package session

func collectEvents(store *Store) ([]Event, error) {
	events := make([]Event, 0)
	if err := store.WalkEvents(func(event Event) error {
		events = append(events, event)
		return nil
	}); err != nil {
		return nil, err
	}
	return events, nil
}
