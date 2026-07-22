package runtime

import "core/server/session"

func (e *Engine) DangerouslyWalkEntireHugeEventLog(req PersistedTranscriptScanRequest) *PersistedTranscriptScan {
	scan := NewPersistedTranscriptScan(req)
	if e == nil || e.store == nil {
		return scan
	}
	if err := e.eventLog.WalkRecords(func(evt session.EventRecord) error {
		return scan.ApplyPersistedEvent(evt)
	}); err != nil {
		return NewPersistedTranscriptScan(req)
	}
	return scan
}

func (e *Engine) ChatSnapshot() ChatSnapshot {
	if e == nil {
		return ChatSnapshot{}
	}
	snapshot := e.DangerouslyWalkEntireHugeEventLog(PersistedTranscriptScanRequest{CacheWarningMode: e.cfg.CacheWarningMode}).CollectedPageSnapshot()
	e.overlayLiveStreaming(&snapshot)
	return snapshot
}
