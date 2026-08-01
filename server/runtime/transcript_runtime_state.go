package runtime

import (
	"fmt"
	"strings"
	"sync"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/config"
	"core/shared/rollbacktarget"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"

	"github.com/google/uuid"
)

type transcriptRuntimeState struct {
	mu                      sync.Mutex
	cwd                     string
	chat                    *chatStore
	liveTools               *transcriptLiveToolLedger
	latestRollbackCandidate *rollbacktarget.CandidateLocator
}

func newTranscriptRuntimeState(cwd string) *transcriptRuntimeState {
	return &transcriptRuntimeState{cwd: strings.TrimSpace(cwd), chat: newChatStore(), liveTools: newTranscriptLiveToolLedger()}
}

func (s *transcriptRuntimeState) SetWorkingDir(workdir string) bool {
	if s == nil {
		return false
	}
	trimmed := strings.TrimSpace(workdir)
	if trimmed == "" {
		return false
	}
	s.mu.Lock()
	s.cwd = trimmed
	s.mu.Unlock()
	return true
}

func (s *transcriptRuntimeState) WorkingDir() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(s.cwd)
}

func (s *transcriptRuntimeState) SetLatestRollbackCandidate(locator rollbacktarget.CandidateLocator) {
	if s == nil {
		return
	}
	if err := locator.Validate(); err != nil {
		panic(fmt.Sprintf("set latest rollback candidate: %v", err))
	}
	s.mu.Lock()
	s.latestRollbackCandidate = &locator
	s.mu.Unlock()
}

func (s *transcriptRuntimeState) LatestRollbackCandidate() *rollbacktarget.CandidateLocator {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return textutil.Pointer(s.latestRollbackCandidate)
}

func (s *transcriptRuntimeState) chatProjection() *chatStore {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.chat == nil {
		s.chat = newChatStore()
	}
	return s.chat
}

func (s *transcriptRuntimeState) liveToolLedger() *transcriptLiveToolLedger {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.liveTools == nil {
		s.liveTools = newTranscriptLiveToolLedger()
	}
	return s.liveTools
}

func (s *transcriptRuntimeState) RecordLiveToolStart(stepID string, call llm.ToolCall) error {
	if ledger := s.liveToolLedger(); ledger != nil {
		return ledger.RecordStart(transcriptLiveToolStartFromCall(stepID, call))
	}
	return nil
}

func (s *transcriptRuntimeState) CompleteLiveTool(callID string) {
	if ledger := s.liveToolLedger(); ledger != nil {
		ledger.Complete(callID)
	}
}

func (s *transcriptRuntimeState) SeedLiveTools(starts []TranscriptLiveToolStart) {
	if ledger := s.liveToolLedger(); ledger != nil {
		ledger.Seed(starts)
	}
}

func (s *transcriptRuntimeState) ToolCallSnapshot(callID string) (llm.ToolCall, bool) {
	if ledger := s.liveToolLedger(); ledger != nil {
		if start, ok := ledger.Lookup(callID); ok && start.Presentation != nil {
			return llm.ToolCall{
				ID:           start.ToolCallID,
				Name:         start.ToolName,
				Presentation: transcript.EncodeToolCallMeta(*start.Presentation),
			}, true
		}
	}
	if chat := s.chatProjection(); chat != nil {
		return chat.toolCallSnapshot(callID)
	}
	return llm.ToolCall{}, false
}

func (s *transcriptRuntimeState) AbortLiveTools() []TranscriptLiveToolStart {
	if ledger := s.liveToolLedger(); ledger != nil {
		return ledger.AbortAll()
	}
	return nil
}

func (s *transcriptRuntimeState) SnapshotMessages() []llm.Message {
	if chat := s.chatProjection(); chat != nil {
		return chat.snapshotMessages()
	}
	return nil
}

func (s *transcriptRuntimeState) SnapshotItems() []llm.ResponseItem {
	if chat := s.chatProjection(); chat != nil {
		return chat.snapshotItems()
	}
	return nil
}

func (s *transcriptRuntimeState) CommittedEntryCount() int {
	if chat := s.chatProjection(); chat != nil {
		return chat.committedEntryCount()
	}
	return 0
}

func (s *transcriptRuntimeState) StreamingSnapshot() (string, string, *AssistantStreamMetadata) {
	if chat := s.chatProjection(); chat != nil {
		return chat.streamingSnapshot()
	}
	return "", "", nil
}

func (s *transcriptRuntimeState) LastCommittedAssistantFinalAnswer() string {
	if chat := s.chatProjection(); chat != nil {
		return chat.cachedLastCommittedAssistantFinalAnswer()
	}
	return ""
}

func (s *transcriptRuntimeState) SeedLastCommittedAssistantFinalAnswerIfEmpty(answer string) {
	if strings.TrimSpace(answer) == "" {
		return
	}
	if chat := s.chatProjection(); chat != nil {
		chat.seedLastCommittedAssistantFinalAnswerIfEmpty(answer)
	}
}

func (s *transcriptRuntimeState) EstimatedProviderTokens() int {
	if chat := s.chatProjection(); chat != nil {
		return chat.estimatedProviderTokens()
	}
	return 0
}

func (s *transcriptRuntimeState) ToolCompletionSnapshot(callID string) (tools.Result, bool) {
	if chat := s.chatProjection(); chat != nil {
		chat.mu.Lock()
		defer chat.mu.Unlock()
		result, ok := chat.toolCompletions[strings.TrimSpace(callID)]
		return result, ok
	}
	return tools.Result{}, false
}

func (s *transcriptRuntimeState) ToolCompletionCount() int {
	if chat := s.chatProjection(); chat != nil {
		chat.mu.Lock()
		defer chat.mu.Unlock()
		return len(chat.toolCompletions)
	}
	return 0
}

func (s *transcriptRuntimeState) ValidateMessage(stepID string, msg llm.Message) error {
	return s.chatProjection().validateMessage(stepID, msg)
}

func (s *transcriptRuntimeState) AppendMessage(stepID string, msg llm.Message) error {
	return s.chatProjection().appendMessage(stepID, msg)
}

func (s *transcriptRuntimeState) AppendLocalEntryRecord(entry ChatEntry, afterToolCallID *string) {
	s.chatProjection().appendLocalEntryRecord(entry, afterToolCallID)
}

func (s *transcriptRuntimeState) AppendCommittedEntryWithVisibility(role, text string, visibility transcript.EntryVisibility) {
	s.chatProjection().appendLocalEntryRecord(ChatEntry{Visibility: visibility, Role: role, Text: text}, nil)
}

func (s *transcriptRuntimeState) AppendStreamingDelta(stepID string, baseRevision int64, baseCommittedEntryCount int, delta string, phase llm.MessagePhase) (*AssistantStreamMetadata, *uuid.UUID) {
	return s.chatProjection().appendStreamingDelta(stepID, baseRevision, baseCommittedEntryCount, delta, phase)
}

func (s *transcriptRuntimeState) RecordAssistantStreamFinalization(committedEntryStart int, streamID *uuid.UUID) {
	s.chatProjection().recordAssistantStreamFinalization(committedEntryStart, streamID)
}

func (s *transcriptRuntimeState) RecordStoredToolCompletion(completion storedToolCompletion) {
	s.chatProjection().recordToolCompletionWithProviderItems(tools.Result{
		CallID:        completion.CallID,
		Name:          toolspec.ID(completion.Name),
		IsError:       completion.IsError,
		Output:        completion.Output,
		Summary:       completion.Summary,
		CondensedText: completion.CondensedText,
		Presentation:  completion.Presentation,
	}, completion.ProviderItems)
}

func (s *transcriptRuntimeState) RestoreToolCompletionRecord(record session.ToolCompletionRecord) error {
	return s.chatProjection().restoreToolCompletionRecord(record)
}

func (s *transcriptRuntimeState) ReplaceHistoryAtCommittedEntryStart(
	stepID string,
	items []llm.ResponseItem,
	committedEntryStart *int,
	projectedEntries []ChatEntry,
) {
	s.chatProjection().replaceHistoryAtCommittedEntryStart(
		stepID,
		items,
		committedEntryStart,
		projectedEntries,
	)
}

func (s *transcriptRuntimeState) ClearStreamingAssistantState() (*AssistantStreamMetadata, *uuid.UUID) {
	chat := s.chatProjection()
	metadata, streamID := chat.discardStreaming()
	chat.clearStreamingError()
	return metadata, streamID
}

func (s *transcriptRuntimeState) SetStreamingError(text string) {
	s.chatProjection().setStreamingError(text)
}

func (s *transcriptRuntimeState) ClearStreamingError() {
	s.chatProjection().clearStreamingError()
}

func applyPersistedCacheWarningToTranscript(state *transcriptRuntimeState, record session.CacheWarningRecord, mode config.CacheWarningMode) {
	warning := cacheWarningFromSessionRecord(record)
	state.AppendCommittedEntryWithVisibility(cacheWarningTranscriptRole, transcript.CacheWarningText(warning), cacheWarningEntryVisibility(mode))
}
