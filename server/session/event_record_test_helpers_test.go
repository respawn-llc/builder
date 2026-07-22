package session

func mustEventRecordKind(value any) EventKind {
	record := eventRecordForTest(value)
	kind, err := record.Kind()
	if err != nil {
		panic(err)
	}
	return kind
}

func mustEventRecordPayload(value any) EventRecordPayload {
	record := eventRecordForTest(value)
	payload, err := record.Payload()
	if err != nil {
		panic(err)
	}
	return payload
}

func eventRecordForTest(value any) EventRecord {
	switch record := value.(type) {
	case EventRecord:
		return record
	case *EventRecord:
		if record == nil {
			panic("nil EventRecord")
		}
		return *record
	default:
		panic("unsupported event record test value")
	}
}

func mustMaterializedRevision(capability MaterializedEventLog) int64 {
	revision, err := capability.Revision()
	if err != nil {
		panic(err)
	}
	return revision
}

func mustConversationFreshness(
	capability MaterializedEventLog,
) ConversationFreshness {
	freshness, err := capability.ConversationFreshness()
	if err != nil {
		panic(err)
	}
	return freshness
}
