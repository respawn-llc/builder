package app

import "core/server/session"

func mustSessionEventKind(record session.EventRecord) session.EventKind {
	kind, err := record.Kind()
	if err != nil {
		panic(err)
	}
	return kind
}

func mustSessionEventPayload(record session.EventRecord) session.EventRecordPayload {
	payload, err := record.Payload()
	if err != nil {
		panic(err)
	}
	return payload
}
