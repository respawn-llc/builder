package runtime

import (
	"encoding/json"
	"fmt"

	"core/server/llm"
	"core/server/tools"
	"core/shared/config"
	"core/shared/toolspec"
	"core/shared/transcript"

	"github.com/google/uuid"
)

type transcriptPersistenceCoordinator struct {
	state *transcriptRuntimeState
}

func newTranscriptPersistenceCoordinator(state *transcriptRuntimeState) transcriptPersistenceCoordinator {
	return transcriptPersistenceCoordinator{state: state}
}

func (p transcriptPersistenceCoordinator) AppendMessage(msg llm.Message) {
	if chat := p.chatProjection(); chat != nil {
		chat.appendMessage(msg)
	}
}

func (p transcriptPersistenceCoordinator) AppendLocalEntryRecord(entry ChatEntry) {
	if chat := p.chatProjection(); chat != nil {
		chat.appendLocalEntryRecord(entry)
	}
}

func (p transcriptPersistenceCoordinator) AppendCommittedEntryWithCondensedText(role, text, condensedText string) {
	if chat := p.chatProjection(); chat != nil {
		chat.appendLocalEntryRecord(ChatEntry{Visibility: transcript.EntryVisibilityAuto, Role: role, Text: text, CondensedText: condensedText})
	}
}

func (p transcriptPersistenceCoordinator) AppendCommittedEntryWithVisibility(role, text string, visibility transcript.EntryVisibility) {
	if chat := p.chatProjection(); chat != nil {
		chat.appendLocalEntryRecord(ChatEntry{Visibility: visibility, Role: role, Text: text})
	}
}

func (p transcriptPersistenceCoordinator) AppendStreamingDelta(stepID string, baseRevision int64, baseCommittedEntryCount int, delta string, phase llm.MessagePhase) (*AssistantStreamMetadata, *uuid.UUID) {
	if chat := p.chatProjection(); chat != nil {
		return chat.appendStreamingDelta(stepID, baseRevision, baseCommittedEntryCount, delta, phase)
	}
	return nil, nil
}

func (p transcriptPersistenceCoordinator) RecordAssistantStreamFinalization(committedEntryStart int, streamID *uuid.UUID) {
	if chat := p.chatProjection(); chat != nil {
		chat.recordAssistantStreamFinalization(committedEntryStart, streamID)
	}
}

func (p transcriptPersistenceCoordinator) RecordStoredToolCompletion(completion storedToolCompletion) {
	if chat := p.chatProjection(); chat != nil {
		chat.recordToolCompletionWithProviderItems(tools.Result{
			CallID:        completion.CallID,
			Name:          toolspec.ID(completion.Name),
			IsError:       completion.IsError,
			Output:        completion.Output,
			Summary:       completion.Summary,
			CondensedText: completion.CondensedText,
			Presentation:  completion.Presentation,
		}, completion.ProviderItems)
	}
}

func (p transcriptPersistenceCoordinator) RestoreToolCompletionPayload(payload []byte) error {
	if chat := p.chatProjection(); chat != nil {
		return chat.restoreToolCompletionPayload(payload)
	}
	return nil
}

func (p transcriptPersistenceCoordinator) ReplaceHistory(items []llm.ResponseItem) {
	p.ReplaceHistoryAtCommittedEntryStart(items, nil)
}

func (p transcriptPersistenceCoordinator) ReplaceHistoryAtCommittedEntryStart(items []llm.ResponseItem, committedEntryStart *int) {
	if chat := p.chatProjection(); chat != nil {
		chat.replaceHistoryAtCommittedEntryStart(items, committedEntryStart)
	}
}

func (p transcriptPersistenceCoordinator) ClearStreamingAssistantState() (*AssistantStreamMetadata, *uuid.UUID) {
	if chat := p.chatProjection(); chat != nil {
		metadata, streamID := chat.discardStreaming()
		chat.clearStreamingError()
		return metadata, streamID
	}
	return nil, nil
}

func (p transcriptPersistenceCoordinator) SetStreamingError(text string) {
	if chat := p.chatProjection(); chat != nil {
		chat.setStreamingError(text)
	}
}

func (p transcriptPersistenceCoordinator) ClearStreamingError() {
	if chat := p.chatProjection(); chat != nil {
		chat.clearStreamingError()
	}
}

func (p transcriptPersistenceCoordinator) chatProjection() *chatStore {
	if p.state == nil {
		return nil
	}
	return p.state.chatProjection()
}

func applyPersistedCacheWarningToTranscript(persistence transcriptPersistenceCoordinator, payload []byte, mode config.CacheWarningMode) error {
	var warning transcript.CacheWarning
	if err := json.Unmarshal(payload, &warning); err != nil {
		return fmt.Errorf("decode %s event: %w", sessionEventCacheWarning, err)
	}
	persistence.AppendCommittedEntryWithVisibility(cacheWarningTranscriptRole, transcript.CacheWarningText(warning), cacheWarningEntryVisibility(mode))
	return nil
}

func applyPersistedCacheWarningToChat(chat *chatStore, payload []byte, mode config.CacheWarningMode) error {
	var warning transcript.CacheWarning
	if err := json.Unmarshal(payload, &warning); err != nil {
		return fmt.Errorf("decode %s event: %w", sessionEventCacheWarning, err)
	}
	if chat != nil {
		chat.appendLocalEntryRecord(ChatEntry{Visibility: cacheWarningEntryVisibility(mode), Role: cacheWarningTranscriptRole, Text: transcript.CacheWarningText(warning)})
	}
	return nil
}
