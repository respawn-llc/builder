package metadata

import "core/server/session"

func mustEventLogRevision(eventLog session.MaterializedEventLog) int64 {
	revision, err := eventLog.Revision()
	if err != nil {
		panic(err)
	}
	return revision
}

func mustEventLogFreshness(
	eventLog session.MaterializedEventLog,
) session.ConversationFreshness {
	freshness, err := eventLog.ConversationFreshness()
	if err != nil {
		panic(err)
	}
	return freshness
}
