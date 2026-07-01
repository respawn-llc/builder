package app

import (
	"core/cli/app/internal/runtimestate"
	"core/shared/clientui"
)

func (a uiRuntimeAdapter) committedAdvanceSyncCommand(evt clientui.Event, sync runtimestate.RuntimeTranscriptSyncCommand) runtimeTranscriptSyncDecision {
	if sync.Reason != runtimestate.RuntimeTranscriptSyncCommittedAdvance {
		return a.syncConversationFromRuntimeTranscriptCommand(sync)
	}
	if a.model.nativeCommittedAdvanceRequiresContinuityBarrier(evt) {
		return a.model.startRuntimeTranscriptSyncRequest(runtimeTranscriptSyncRequestForPage(a.model.transcriptRequestForCurrentMode(), false, runtimeTranscriptSyncCauseCommittedGap, sync.RecoveryCause))
	}
	return a.syncConversationFromRuntimeTranscriptCommand(sync)
}

func (m *uiModel) nativeCommittedAdvanceRequiresContinuityBarrier(evt clientui.Event) bool {
	if m == nil || evt.Kind != clientui.EventConversationUpdated || !evt.CommittedTranscriptChanged || len(evt.TranscriptEntries) > 0 {
		return false
	}
	if evt.RecoveryCause != clientui.TranscriptRecoveryCauseNone {
		return false
	}
	if !(m.nativeSurfaceConfigured() || m.nativeImmutableTranscriptWritten || m.nativeAssistantStreamIncomplete) {
		return false
	}
	_, ownedCommittedCount := committedTranscriptOwnedTailIncludingDeferredTail(m)
	return evt.CommittedEntryCount > ownedCommittedCount
}
