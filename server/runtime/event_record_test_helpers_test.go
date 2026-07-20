package runtime

import (
	"testing"

	"core/server/session"
)

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

func mustEngineTranscriptRevision(engine *Engine) int64 {
	revision, err := engine.TranscriptRevision()
	if err != nil {
		panic(err)
	}
	return revision
}

func mustEventLogRevision(eventLog session.MaterializedEventLog) int64 {
	revision, err := eventLog.Revision()
	if err != nil {
		panic(err)
	}
	return revision
}

func mustEngineConversationFreshness(engine *Engine) session.ConversationFreshness {
	freshness, err := engine.ConversationFreshness()
	if err != nil {
		panic(err)
	}
	return freshness
}

func mustEventLogConversationFreshness(
	eventLog session.MaterializedEventLog,
) session.ConversationFreshness {
	freshness, err := eventLog.ConversationFreshness()
	if err != nil {
		panic(err)
	}
	return freshness
}

func mustQueueUserMessage(t *testing.T, engine *Engine, text string) QueuedUserMessage {
	t.Helper()
	return engine.QueueUserMessage(text)
}

func mustQueueUserMessageWithClientRequestID(
	t *testing.T,
	engine *Engine,
	text string,
	clientRequestID string,
) QueuedUserMessage {
	t.Helper()
	return engine.QueueUserMessageWithClientRequestID(text, clientRequestID)
}

func mustDiscardQueuedUserMessage(
	t *testing.T,
	engine *Engine,
	queueItemID string,
) bool {
	t.Helper()
	return engine.DiscardQueuedUserMessage(queueItemID)
}
