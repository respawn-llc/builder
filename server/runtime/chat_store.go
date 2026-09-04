package runtime

import (
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
)

type ChatEntry struct {
	StepID                *string
	Visibility            transcript.EntryVisibility
	RollbackTargetID      *string
	Role                  string
	Text                  string
	DurationMs            *int64
	CondensedText         string
	Phase                 llm.MessagePhase
	MessageType           llm.MessageType
	CompactionNumber      *int
	SourcePath            string
	WorktreeContext       *session.WorktreeContext
	CompactLabel          string
	ToolResultSummary     string
	ToolCallID            string
	QuestionAnswer        *tools.AskQuestionAnswer
	NoticeID              string
	BackgroundActivityID  string
	BackgroundProcessID   string
	BackgroundExitCode    *int
	ToolOutputRepair      *transcript.ToolOutputRepairNotice
	ProviderModelMismatch *transcript.ProviderModelMismatchNotice
	ToolCall              *transcript.ToolCallMeta
	CommittedProvenance   *TranscriptCommittedRowProvenance
	ReviewerFeedback      *ReviewerFeedbackChatEntry
	ReviewerError         *ReviewerErrorChatEntry
}

type ReviewerFeedbackChatEntry struct {
	ID          runtimeids.ReviewerFeedbackID
	Suggestions []string
}

type ReviewerErrorChatEntry struct {
	ID     runtimeids.ReviewerErrorID
	Detail string
}

type ChatSnapshot struct {
	Entries           []ChatEntry
	Streaming         string
	StreamingMetadata *AssistantStreamMetadata
	StreamingError    string
}

type AssistantStreamMetadata struct {
	StepID                  string
	BaseRevision            int64
	BaseCommittedEntryCount int
}

type TranscriptWindowSnapshot struct {
	Snapshot     ChatSnapshot
	TotalEntries int
	Offset       int
}

type storedToolCompletion struct {
	CallID         string                   `json:"call_id"`
	Name           string                   `json:"name"`
	IsError        bool                     `json:"is_error"`
	Output         json.RawMessage          `json:"output"`
	Summary        *string                  `json:"summary,omitempty"`
	CondensedText  *string                  `json:"condensed_text,omitempty"`
	Presentation   *transcript.ToolCallMeta `json:"presentation,omitempty"`
	ProviderItems  []llm.ResponseItem       `json:"provider_items,omitempty"`
	QuestionAnswer *tools.AskQuestionAnswer `json:"question_answer,omitempty"`
}

type chatStore struct {
	mu sync.RWMutex

	messageRecords []chatMessageRecord
	compact        *compactionCheckpoint
	local          []localChatEntry

	toolCompletions                   map[string]tools.Result
	toolCompletionProvenance          map[string]*TranscriptCommittedRowProvenance
	toolCompletionProviderItems       map[string][]llm.ResponseItem
	assistantToolCalls                map[string]struct{}
	assistantToolCallStepIDs          map[string]string
	materializedToolResults           map[string]struct{}
	synthesizedToolResults            map[string]struct{}
	assistantStreamIDsByEntry         map[int]uuid.UUID
	activeSegmentEntryStart           int
	streaming                         *assistantStreamingState
	streamingError                    string
	cwd                               string
	lastCommittedAssistantFinalAnswer *string
	transcriptEntryCount              int

	providerTokenEstimate      int
	providerTokenEstimateDirty bool
}

type chatMessageRecord struct {
	StepID        *string
	Message       llm.Message
	ProviderItems []llm.ResponseItem
	Provenance    *TranscriptCommittedRowProvenance
}

type localChatEntry struct {
	Entry             ChatEntry
	AfterMessageCount int
	AfterToolCallID   *string
	MarksBoundary     bool
	Projected         bool
	Provenance        *TranscriptCommittedRowProvenance
}

type assistantStreamingState struct {
	text               string
	metadata           *AssistantStreamMetadata
	transcriptStreamID *uuid.UUID
	phase              llm.MessagePhase
}

type assistantStreamingAppend struct {
	metadata           *AssistantStreamMetadata
	transcriptStreamID *uuid.UUID
	supersededMetadata *AssistantStreamMetadata
	supersededStreamID *uuid.UUID
}

type compactionCheckpoint struct {
	Items []llm.ResponseItem
}

func newChatStore() *chatStore {
	cwd, _ := os.Getwd()
	return newChatStoreWithCWD(cwd)
}

func newChatStoreWithCWD(cwd string) *chatStore {
	return &chatStore{
		toolCompletions:             make(map[string]tools.Result, 16),
		toolCompletionProvenance:    make(map[string]*TranscriptCommittedRowProvenance, 16),
		toolCompletionProviderItems: make(map[string][]llm.ResponseItem, 16),
		assistantToolCalls:          make(map[string]struct{}, 16),
		assistantToolCallStepIDs:    make(map[string]string, 16),
		materializedToolResults:     make(map[string]struct{}, 16),
		synthesizedToolResults:      make(map[string]struct{}, 16),
		cwd:                         strings.TrimSpace(cwd),
		providerTokenEstimateDirty:  true,
	}
}

func (s *chatStore) validateMessage(stepID *string, msg llm.Message) error {
	msg = normalizeMessageForTranscript(msg, s.cwd)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.validateMessageLocked(stepID, msg)
}

func (s *chatStore) appendMessage(stepID *string, msg llm.Message, provenances ...*TranscriptCommittedRowProvenance) error {
	var provenance *TranscriptCommittedRowProvenance
	if len(provenances) > 0 {
		provenance = provenances[0]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	msg = normalizeMessageForTranscript(msg, s.cwd)
	if err := s.validateMessageLocked(stepID, msg); err != nil {
		return err
	}
	s.messageRecords = append(s.messageRecords, chatMessageRecord{
		StepID:        textutil.Pointer(stepID),
		Message:       cloneChatStoreMessage(msg),
		ProviderItems: llm.ItemsFromMessages([]llm.Message{msg}),
		Provenance:    cloneTranscriptCommittedRowProvenance(provenance),
	})
	s.applyMessageStatsLocked(stepID, msg)
	s.placeAttachedLocalEntriesAfterMaterializedToolLocked(msg)
	s.providerTokenEstimateDirty = true
	return nil
}
func (s *chatStore) replaceHistory(stepID string, items []llm.ResponseItem) {
	s.replaceHistoryAtCommittedEntryStart(
		textutil.OptionalExactString(stepID),
		items,
		nil,
		transcriptEntriesFromHistoryReplacement(items, nil),
	)
}

func (s *chatStore) replaceHistoryAtCommittedEntryStart(
	stepID *string,
	items []llm.ResponseItem,
	committedEntryStart *int,
	projectedEntries []ChatEntry,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	activeSegmentEntryStart := s.transcriptEntryCount
	if committedEntryStart != nil && *committedEntryStart > s.transcriptEntryCount {
		s.transcriptEntryCount = *committedEntryStart
		activeSegmentEntryStart = *committedEntryStart
	} else if committedEntryStart != nil {
		activeSegmentEntryStart = *committedEntryStart
	}
	preparedItems := llm.PrepareOpenAIInputItems(items)
	s.recordReplacementToolCallStepIDsLocked(stepID, preparedItems)
	// Provider/model history switches to the compacted checkpoint while the
	// transcript receives its typed projection at the same committed boundary.
	projectedStart := len(s.local)
	if stepID != nil {
		for index := range projectedEntries {
			projectedEntries[index].StepID = cloneOptionalStepID(stepID)
		}
	}
	s.appendProjectedHistoryReplacementEntriesLocked(projectedEntries)
	s.local = append([]localChatEntry(nil), s.local[projectedStart:]...)
	s.compact = &compactionCheckpoint{
		Items: llm.CloneResponseItems(preparedItems),
	}
	s.activeSegmentEntryStart = activeSegmentEntryStart
	s.pruneAssistantStreamIDsBeforeLocked(activeSegmentEntryStart)
	s.messageRecords = nil
	s.pruneToolCompletionsToWorkingSetLocked()
	s.providerTokenEstimateDirty = true
}

func (s *chatStore) pruneAssistantStreamIDsBeforeLocked(activeSegmentEntryStart int) {
	for entryIndex := range s.assistantStreamIDsByEntry {
		if entryIndex < activeSegmentEntryStart {
			delete(s.assistantStreamIDsByEntry, entryIndex)
		}
	}
}

func (s *chatStore) pruneToolCompletionsToWorkingSetLocked() {
	if len(s.toolCompletions) == 0 &&
		len(s.toolCompletionProvenance) == 0 &&
		len(s.toolCompletionProviderItems) == 0 &&
		len(s.assistantToolCalls) == 0 &&
		len(s.assistantToolCallStepIDs) == 0 &&
		len(s.materializedToolResults) == 0 &&
		len(s.synthesizedToolResults) == 0 {
		return
	}
	referenced := make(map[string]struct{}, len(s.toolCompletions))
	for _, item := range s.providerItemsSourceLocked() {
		if !isToolCallItem(item.Type) {
			continue
		}
		if callID, present := textutil.FirstOptionalTrimmed(item.CallID, item.ID); present {
			referenced[callID] = struct{}{}
		}
	}
	pruneCallIDMapToReferenced(s.toolCompletions, referenced)
	pruneCallIDMapToReferenced(s.toolCompletionProvenance, referenced)
	pruneCallIDMapToReferenced(s.toolCompletionProviderItems, referenced)
	pruneCallIDMapToReferenced(s.assistantToolCalls, referenced)
	pruneCallIDMapToReferenced(s.assistantToolCallStepIDs, referenced)
	pruneCallIDMapToReferenced(s.materializedToolResults, referenced)
	pruneCallIDMapToReferenced(s.synthesizedToolResults, referenced)
}

func pruneCallIDMapToReferenced[V any](m map[string]V, referenced map[string]struct{}) {
	for callID := range m {
		if _, ok := referenced[callID]; !ok {
			delete(m, callID)
		}
	}
}

func (s *chatStore) estimatedProviderTokens() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.providerTokenEstimateDirty {
		return s.providerTokenEstimate
	}
	total := estimateItemsTokens(s.snapshotProviderItemsLocked())
	if total < 0 {
		total = 0
	}
	s.providerTokenEstimate = total
	s.providerTokenEstimateDirty = false
	return total
}

func (s *chatStore) snapshotItems() []llm.ResponseItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotProviderItemsLocked()
}

func (s *chatStore) toolCallSnapshot(callID string) (llm.ToolCall, bool) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return llm.ToolCall{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return toolCallSnapshotFromItems(s.providerItemsSourceLocked(), callID)
}

func toolCallSnapshotFromItems(items []llm.ResponseItem, callID string) (llm.ToolCall, bool) {
	for index := len(items) - 1; index >= 0; index-- {
		item := items[index]
		if !isToolCallItem(item.Type) {
			continue
		}
		itemCallID, present := textutil.FirstOptionalTrimmed(item.CallID, item.ID)
		if !present {
			continue
		}
		if itemCallID != callID {
			continue
		}
		name, _ := textutil.OptionalTrimmed(item.Name)
		call := llm.ToolCall{
			ID:           itemCallID,
			Name:         name,
			Presentation: append(json.RawMessage(nil), item.ToolPresentation...),
		}
		if item.Type == llm.ResponseItemTypeCustomToolCall {
			call.Custom = true
			call.CustomInput = textutil.Pointer(item.CustomInput)
			if item.CustomInput != nil {
				call.Input = normalizeRuntimeToolInput(*item.CustomInput)
			}
		} else {
			call.Input = append(json.RawMessage(nil), item.Arguments...)
		}
		return call, true
	}
	return llm.ToolCall{}, false
}

func (s *chatStore) restoreToolCompletionRecord(record session.ToolCompletionRecord, provenances ...*TranscriptCommittedRowProvenance) error {
	completion, err := storedToolCompletionFromSessionRecord(record)
	if err != nil {
		return fmt.Errorf("restore session tool completion record: %w", err)
	}
	s.recordToolCompletionWithProviderItems(tools.Result{
		CallID:         completion.CallID,
		Name:           toolspec.ID(completion.Name),
		IsError:        completion.IsError,
		Output:         completion.Output,
		Summary:        completion.Summary,
		CondensedText:  completion.CondensedText,
		Presentation:   completion.Presentation,
		QuestionAnswer: completion.QuestionAnswer,
	}, completion.ProviderItems, provenances...)
	return nil
}

func (s *chatStore) recordToolCompletionWithProviderItems(res tools.Result, providerItems []llm.ResponseItem, provenances ...*TranscriptCommittedRowProvenance) {
	callID := strings.TrimSpace(res.CallID)
	if callID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolCompletions[callID] = res
	if len(provenances) > 0 {
		s.toolCompletionProvenance[callID] = cloneTranscriptCommittedRowProvenance(provenances[0])
	}
	if len(providerItems) > 0 {
		s.toolCompletionProviderItems[callID] = llm.CloneResponseItems(providerItems)
	} else {
		delete(s.toolCompletionProviderItems, callID)
	}
	s.providerTokenEstimateDirty = true
	if _, ok := s.assistantToolCalls[callID]; ok {
		if _, materialized := s.materializedToolResults[callID]; !materialized {
			if _, synthesized := s.synthesizedToolResults[callID]; !synthesized {
				s.synthesizedToolResults[callID] = struct{}{}
				s.transcriptEntryCount++
			}
		}
	}
}

func (s *chatStore) appendStreamingDelta(stepID string, baseRevision int64, baseCommittedEntryCount int, delta string, phase llm.MessagePhase) assistantStreamingAppend {
	if delta == "" {
		return assistantStreamingAppend{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	nextMetadata := newAssistantStreamMetadata(stepID, baseRevision, baseCommittedEntryCount)
	var supersededMetadata *AssistantStreamMetadata
	var supersededStreamID *uuid.UUID
	if s.streaming != nil &&
		s.streaming.phase != "" &&
		phase != "" &&
		s.streaming.phase != phase {
		supersededMetadata = cloneAssistantStreamMetadata(s.streaming.metadata)
		supersededStreamID = cloneTranscriptStreamID(s.streaming.transcriptStreamID)
		s.streaming = nil
	}
	if s.streaming == nil || assistantStreamingSegmentChanged(s.streaming.metadata, nextMetadata) {
		streamID := uuid.New()
		s.streaming = &assistantStreamingState{metadata: nextMetadata, transcriptStreamID: &streamID}
	}
	s.streaming.text += delta
	if phase != "" {
		s.streaming.phase = phase
	}
	return assistantStreamingAppend{
		metadata:           cloneAssistantStreamMetadata(s.streaming.metadata),
		transcriptStreamID: cloneTranscriptStreamID(s.streaming.transcriptStreamID),
		supersededMetadata: supersededMetadata,
		supersededStreamID: supersededStreamID,
	}
}

func (s *chatStore) streamingSnapshot() (string, string, *AssistantStreamMetadata) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	streaming, metadata := s.streamingSnapshotLocked()
	return streaming, s.streamingError, metadata
}

func (s *chatStore) recordAssistantStreamFinalization(committedEntryStart int, streamID *uuid.UUID) {
	if s == nil || committedEntryStart < 0 || streamID == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.assistantStreamIDsByEntry == nil {
		s.assistantStreamIDsByEntry = make(map[int]uuid.UUID)
	}
	s.assistantStreamIDsByEntry[committedEntryStart] = *streamID
}

func (s *chatStore) discardStreaming() (*AssistantStreamMetadata, *uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	metadata := cloneAssistantStreamMetadata(s.streamingMetadataLocked())
	streamID := cloneTranscriptStreamID(s.streamingStreamIDLocked())
	s.streaming = nil
	return metadata, streamID
}

func (s *chatStore) setStreamingError(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streamingError = strings.TrimSpace(text)
}

func (s *chatStore) clearStreamingError() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streamingError = ""
}

func newAssistantStreamMetadata(stepID string, baseRevision int64, baseCommittedEntryCount int) *AssistantStreamMetadata {
	stepID = strings.TrimSpace(stepID)
	if stepID == "" {
		return nil
	}
	if baseRevision < 0 || baseCommittedEntryCount < 0 {
		return nil
	}
	return &AssistantStreamMetadata{
		StepID:                  stepID,
		BaseRevision:            baseRevision,
		BaseCommittedEntryCount: baseCommittedEntryCount,
	}
}

func assistantStreamingSegmentChanged(current *AssistantStreamMetadata, next *AssistantStreamMetadata) bool {
	if current == nil || next == nil {
		return current != next
	}
	return current.StepID != next.StepID ||
		current.BaseRevision != next.BaseRevision ||
		current.BaseCommittedEntryCount != next.BaseCommittedEntryCount
}

func (s *chatStore) streamingSnapshotLocked() (string, *AssistantStreamMetadata) {
	if s.streaming == nil {
		return "", nil
	}
	return s.streaming.text, cloneAssistantStreamMetadata(s.streaming.metadata)
}

func (s *chatStore) streamingMetadataLocked() *AssistantStreamMetadata {
	if s.streaming == nil {
		return nil
	}
	return s.streaming.metadata
}

func (s *chatStore) streamingStreamIDLocked() *uuid.UUID {
	if s.streaming == nil {
		return nil
	}
	return s.streaming.transcriptStreamID
}

func (s *chatStore) streamingPhaseLocked() llm.MessagePhase {
	if s.streaming == nil {
		return ""
	}
	return s.streaming.phase
}

func cloneAssistantStreamMetadata(metadata *AssistantStreamMetadata) *AssistantStreamMetadata {
	if metadata == nil {
		return nil
	}
	copyMetadata := *metadata
	return &copyMetadata
}

func cloneTranscriptStreamID(streamID *uuid.UUID) *uuid.UUID {
	if streamID == nil {
		return nil
	}
	copied := *streamID
	return &copied
}

func (s *chatStore) appendLocalEntryRecord(entry ChatEntry, afterToolCallID *string, provenances ...*TranscriptCommittedRowProvenance) {
	if strings.TrimSpace(entry.Text) == "" && entry.ToolOutputRepair == nil && entry.ProviderModelMismatch == nil && entry.ReviewerFeedback == nil && entry.ReviewerError == nil {
		return
	}
	entry.Visibility = normalizeRuntimeEntryVisibility(entry.Visibility)
	entry.CondensedText = strings.TrimSpace(entry.CondensedText)
	entry.NoticeID = strings.TrimSpace(entry.NoticeID)
	entry.ToolOutputRepair = textutil.Pointer(entry.ToolOutputRepair)
	entry.ProviderModelMismatch = textutil.Pointer(entry.ProviderModelMismatch)
	if entry.ReviewerFeedback != nil {
		feedback := *entry.ReviewerFeedback
		feedback.Suggestions = append([]string(nil), entry.ReviewerFeedback.Suggestions...)
		entry.ReviewerFeedback = &feedback
	}
	if entry.ReviewerError != nil {
		reviewerError := *entry.ReviewerError
		entry.ReviewerError = &reviewerError
	}
	if afterToolCallID != nil {
		callID := strings.TrimSpace(*afterToolCallID)
		if callID == "" {
			panic("append local transcript entry: after-tool call identity is empty")
		}
		afterToolCallID = &callID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.local = append(s.local, localChatEntry{
		Entry:             entry,
		AfterMessageCount: len(s.messageRecords),
		AfterToolCallID:   afterToolCallID,
		Provenance: func() *TranscriptCommittedRowProvenance {
			if len(provenances) == 0 {
				return cloneTranscriptCommittedRowProvenance(entry.CommittedProvenance)
			}
			return cloneTranscriptCommittedRowProvenance(provenances[0])
		}(),
		Projected: entry.ReviewerFeedback != nil || entry.ReviewerError != nil,
	})
	s.transcriptEntryCount++
}

func (s *chatStore) placeAttachedLocalEntriesAfterMaterializedToolLocked(msg llm.Message) {
	if msg.Role != llm.RoleTool {
		return
	}
	callID, present := textutil.OptionalTrimmed(msg.ToolCallID)
	if !present {
		return
	}
	changed := false
	for index := range s.local {
		attachedCallID := s.local[index].AfterToolCallID
		if attachedCallID == nil || strings.TrimSpace(*attachedCallID) != callID {
			continue
		}
		s.local[index].AfterMessageCount = len(s.messageRecords)
		changed = true
	}
	if changed {
		sort.SliceStable(s.local, func(left, right int) bool {
			return s.local[left].AfterMessageCount < s.local[right].AfterMessageCount
		})
	}
}

func (s *chatStore) appendProjectedHistoryReplacementEntriesLocked(entries []ChatEntry) {
	for idx, entry := range entries {
		s.appendProjectedEntryLocked(entry, idx == 0)
	}
}

func (s *chatStore) appendProjectedEntryLocked(entry ChatEntry, marksBoundary bool) {
	entry.Visibility = normalizeRuntimeEntryVisibility(entry.Visibility)
	entry.ToolCallID = strings.TrimSpace(entry.ToolCallID)
	s.local = append(s.local, localChatEntry{
		Entry:             entry,
		AfterMessageCount: 0,
		MarksBoundary:     marksBoundary,
		Projected:         true,
		Provenance:        cloneTranscriptCommittedRowProvenance(entry.CommittedProvenance),
	})
	s.transcriptEntryCount++
}

func (s *chatStore) committedEntryCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.transcriptEntryCount
}

func (s *chatStore) cachedLastCommittedAssistantFinalAnswer() *string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return textutil.Pointer(s.lastCommittedAssistantFinalAnswer)
}

func (s *chatStore) seedLastCommittedAssistantFinalAnswerIfAbsent(answer *string) {
	if answer == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastCommittedAssistantFinalAnswer == nil {
		s.lastCommittedAssistantFinalAnswer = textutil.Pointer(answer)
	}
}

func (s *chatStore) snapshotMessages() []llm.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return llm.MessagesFromItems(s.snapshotProviderItemsLocked())
}

func (s *chatStore) snapshotProviderItemsLocked() []llm.ResponseItem {
	items := s.providerItemsSourceLocked()
	materializedToolResults := collectMaterializedToolCalls(items)
	out := make([]llm.ResponseItem, 0, len(items)+len(s.toolCompletions))
	pendingOutputs := make([]llm.ResponseItem, 0, len(s.toolCompletions))
	inFunctionOutputRun := false
	flushPendingOutputs := func() {
		if len(pendingOutputs) == 0 {
			return
		}
		out = append(out, pendingOutputs...)
		pendingOutputs = pendingOutputs[:0]
	}
	for _, item := range items {
		if !isToolOutputItem(item.Type) {
			if inFunctionOutputRun {
				flushPendingOutputs()
				inFunctionOutputRun = false
			} else if !isToolCallItem(item.Type) {
				flushPendingOutputs()
			}
		}
		out = append(out, item)
		if !isToolCallItem(item.Type) {
			if isToolOutputItem(item.Type) {
				inFunctionOutputRun = true
			}
			continue
		}
		callID, present := textutil.FirstOptionalTrimmed(item.CallID, item.ID)
		if !present {
			continue
		}
		if _, ok := materializedToolResults[callID]; ok {
			continue
		}
		completion, ok := s.toolCompletions[callID]
		if !ok {
			continue
		}
		providerItems := s.toolCompletionProviderItems[callID]
		if len(providerItems) > 0 {
			pendingOutputs = append(pendingOutputs, llm.CloneResponseItems(providerItems)...)
			continue
		}
		itemName, _ := textutil.OptionalTrimmed(item.Name)
		completionName := strings.TrimSpace(string(completion.Name))
		if completionName == "" {
			completionName = itemName
		}
		pendingOutputs = append(pendingOutputs, llm.PrepareOpenAIInputItems([]llm.ResponseItem{{
			Type:   llm.ToolOutputItemType(item.Type == llm.ResponseItemTypeCustomToolCall),
			CallID: textutil.Value(callID),
			Name:   textutil.OptionalExactString(completionName),
			Output: append(json.RawMessage(nil), completion.Output...),
		}})...)
	}
	flushPendingOutputs()
	return out
}

// danglingToolCalls reports tool calls in the current provider-bound item
// sequence that have no accompanying output (neither a materialized tool message
// nor a recorded completion). These are exactly the calls a provider rejects
// with HTTP 400 because every tool call must be followed by its output.
func (s *chatStore) danglingToolCalls() []danglingToolCall {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := s.providerItemsSourceLocked()
	materialized := collectMaterializedToolCalls(items)
	seen := make(map[string]struct{})
	out := make([]danglingToolCall, 0)
	for _, item := range items {
		if !isToolCallItem(item.Type) {
			continue
		}
		callID, present := textutil.FirstOptionalTrimmed(item.CallID, item.ID)
		if !present {
			continue
		}
		if _, ok := seen[callID]; ok {
			continue
		}
		if _, ok := materialized[callID]; ok {
			continue
		}
		if _, ok := s.toolCompletions[callID]; ok {
			continue
		}
		name, _ := textutil.OptionalTrimmed(item.Name)
		stepID, hasStepID := s.assistantToolCallStepIDs[callID]
		var ownedStepID *string
		if hasStepID {
			ownedStepID = textutil.Value(stepID)
		}
		seen[callID] = struct{}{}
		out = append(out, danglingToolCall{callID: callID, name: name, stepID: ownedStepID})
	}
	return out
}

func isToolCallItem(itemType llm.ResponseItemType) bool {
	return itemType == llm.ResponseItemTypeFunctionCall || itemType == llm.ResponseItemTypeCustomToolCall
}

func isToolOutputItem(itemType llm.ResponseItemType) bool {
	return itemType == llm.ResponseItemTypeFunctionCallOutput || itemType == llm.ResponseItemTypeCustomToolOutput
}

func (s *chatStore) providerItemsSourceLocked() []llm.ResponseItem {
	active := s.activeProviderItemsLocked()
	if s.compact == nil {
		return active
	}
	base := llm.CloneResponseItems(s.compact.Items)
	out := make([]llm.ResponseItem, 0, len(base)+len(active))
	out = append(out, base...)
	out = append(out, active...)
	return out
}

func (s *chatStore) activeProviderItemsLocked() []llm.ResponseItem {
	itemCount := 0
	for _, record := range s.messageRecords {
		itemCount += len(record.ProviderItems)
	}
	items := make([]llm.ResponseItem, 0, itemCount)
	for _, record := range s.messageRecords {
		items = append(items, record.ProviderItems...)
	}
	return llm.CloneResponseItems(items)
}

func cloneChatStoreMessage(msg llm.Message) llm.Message {
	cloned := msg
	cloned.MessageType = textutil.Pointer(msg.MessageType)
	cloned.SourcePath = textutil.Pointer(msg.SourcePath)
	cloned.WorktreeContext = session.CloneWorktreeContext(msg.WorktreeContext)
	cloned.Content = textutil.Pointer(msg.Content)
	cloned.CompactContent = textutil.Pointer(msg.CompactContent)
	cloned.Name = textutil.Pointer(msg.Name)
	cloned.ToolCallID = textutil.Pointer(msg.ToolCallID)
	cloned.Phase = textutil.Pointer(msg.Phase)
	cloned.BackgroundActivityID = textutil.Pointer(msg.BackgroundActivityID)
	cloned.BackgroundExitCode = textutil.Pointer(msg.BackgroundExitCode)
	if len(msg.ToolCalls) > 0 {
		cloned.ToolCalls = append([]llm.ToolCall(nil), msg.ToolCalls...)
		for index := range cloned.ToolCalls {
			cloned.ToolCalls[index].Presentation = append(json.RawMessage(nil), msg.ToolCalls[index].Presentation...)
			cloned.ToolCalls[index].Input = append(json.RawMessage(nil), msg.ToolCalls[index].Input...)
			cloned.ToolCalls[index].CustomInput = textutil.Pointer(msg.ToolCalls[index].CustomInput)
		}
	}
	if len(msg.ReasoningItems) > 0 {
		cloned.ReasoningItems = append([]llm.ReasoningItem(nil), msg.ReasoningItems...)
	}
	return cloned
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (s *chatStore) validateMessageLocked(stepID *string, msg llm.Message) error {
	if msg.Role != llm.RoleAssistant || stepID == nil {
		return nil
	}
	exactStepID := strings.TrimSpace(*stepID)
	for _, call := range msg.ToolCalls {
		callID := strings.TrimSpace(call.ID)
		if callID == "" {
			continue
		}
		if existing, present := s.assistantToolCallStepIDs[callID]; present && existing != exactStepID {
			return fmt.Errorf(
				"assistant tool call %q changed step identity from %q to %q",
				callID,
				existing,
				exactStepID,
			)
		}
	}
	return nil
}

func (s *chatStore) applyMessageStatsLocked(stepID *string, msg llm.Message) {
	s.applyLastCommittedAssistantFinalAnswerLocked(msg)
	delta := len(VisibleChatEntriesFromMessage(msg))
	switch msg.Role {
	case llm.RoleAssistant:
		for _, call := range msg.ToolCalls {
			callID := strings.TrimSpace(call.ID)
			if callID == "" {
				continue
			}
			if stepID != nil {
				s.assistantToolCallStepIDs[callID] = strings.TrimSpace(*stepID)
			}
			s.assistantToolCalls[callID] = struct{}{}
			if _, materialized := s.materializedToolResults[callID]; materialized {
				continue
			}
			if _, synthesized := s.synthesizedToolResults[callID]; synthesized {
				continue
			}
			if _, completed := s.toolCompletions[callID]; completed {
				s.synthesizedToolResults[callID] = struct{}{}
				delta++
			}
		}
	case llm.RoleTool:
		if callID, present := textutil.OptionalTrimmed(msg.ToolCallID); present {
			s.materializedToolResults[callID] = struct{}{}
			if _, synthesized := s.synthesizedToolResults[callID]; synthesized {
				delete(s.synthesizedToolResults, callID)
				delta--
			}
		}
	}
	s.transcriptEntryCount += delta
	if s.transcriptEntryCount < 0 {
		s.transcriptEntryCount = 0
	}
}

func (s *chatStore) recordReplacementToolCallStepIDsLocked(stepID *string, items []llm.ResponseItem) {
	if stepID == nil {
		return
	}
	exactStepID := strings.TrimSpace(*stepID)
	for _, item := range items {
		if !isToolCallItem(item.Type) {
			continue
		}
		callID, present := textutil.FirstOptionalTrimmed(item.CallID, item.ID)
		if !present {
			continue
		}
		// Replacement items are born at the compaction boundary, which is also
		// the only ownership fact available when the active segment is restored.
		// Rebase live ownership to the same Step so restart cannot change it.
		s.assistantToolCallStepIDs[callID] = exactStepID
	}
}

func (s *chatStore) applyLastCommittedAssistantFinalAnswerLocked(msg llm.Message) {
	s.lastCommittedAssistantFinalAnswer = applyLastCommittedAssistantFinalAnswer(s.lastCommittedAssistantFinalAnswer, msg)
}

func applyLastCommittedAssistantFinalAnswer(current *string, msg llm.Message) *string {
	if messagePreservesLastCommittedAssistantFinalAnswer(msg) {
		return textutil.Pointer(current)
	}
	if isBlankFinalAnswer(msg) {
		return nil
	}
	if msg.Role == llm.RoleAssistant &&
		msg.Phase != nil &&
		*msg.Phase == llm.MessagePhaseFinal &&
		msg.Content != nil &&
		strings.TrimSpace(*msg.Content) != "" {
		return textutil.Pointer(msg.Content)
	}
	return nil
}

type transcriptDeliverySnapshot struct {
	Rows              []TranscriptCommittedRowFact
	Streaming         string
	StreamingMetadata *AssistantStreamMetadata
	StreamingStreamID *uuid.UUID
	StreamingPhase    llm.MessagePhase
}

type transcriptDeliveryFactScan struct {
	rows                     []TranscriptCommittedRowFact
	toolCompletions          map[string]tools.Result
	toolCompletionProvenance map[string]*TranscriptCommittedRowProvenance
	materializedToolCalls    map[string]struct{}
	streamIDsByEntry         map[int]uuid.UUID
	currentEntryIndex        int
}

func newTranscriptDeliveryFactScan(completions map[string]tools.Result, completionProvenance map[string]*TranscriptCommittedRowProvenance, materializedToolCalls map[string]struct{}, streamIDsByEntry map[int]uuid.UUID, currentEntryIndex int) *transcriptDeliveryFactScan {
	return &transcriptDeliveryFactScan{toolCompletions: completions, toolCompletionProvenance: completionProvenance, materializedToolCalls: materializedToolCalls, streamIDsByEntry: streamIDsByEntry, currentEntryIndex: currentEntryIndex}
}

func (s *transcriptDeliveryFactScan) ApplyMessage(stepID *string, msg llm.Message, provenance *TranscriptCommittedRowProvenance) {
	if s == nil {
		return
	}
	var streamID *uuid.UUID
	if id, ok := s.streamIDsByEntry[s.currentEntryIndex]; ok {
		streamID = &id
	}
	facts := transcriptCommittedRowFactsForStep(
		stepID,
		transcriptCommittedRowFactsFromMessage(
			msg,
			streamID,
			s.toolCompletions,
			s.materializedToolCalls,
			transcriptMessageProjectionContext{
				Provenance:           provenance,
				CompletionProvenance: s.toolCompletionProvenance,
			},
		),
	)
	s.rows = append(s.rows, facts...)
	s.currentEntryIndex += transcriptCommittedEntryCountFromMessage(msg, s.toolCompletions, s.materializedToolCalls)
}

func (s *transcriptDeliveryFactScan) ApplyLocalEntry(entry ChatEntry, projected bool) {
	if s == nil {
		return
	}
	if projected {
		if fact, ok := transcriptCommittedRowFactFromChatEntry(entry); ok {
			s.rows = append(s.rows, fact)
		}
	} else if strings.TrimSpace(entry.Role) == string(transcript.EntryRoleReasoning) {
		if fact, ok := transcriptCommittedRowFactFromChatEntry(entry); ok {
			s.rows = append(s.rows, fact)
		}
	} else {
		if fact, ok := transcriptNoticeRowFactFromChatEntry(entry); ok {
			s.rows = append(s.rows, fact)
		}
	}
	s.currentEntryIndex++
}

func (s *transcriptDeliveryFactScan) MarkCompactionBoundary() {
	if s == nil {
		return
	}
	s.rows = nil
}

func (s *transcriptDeliveryFactScan) Snapshot() []TranscriptCommittedRowFact {
	if s == nil || len(s.rows) == 0 {
		return nil
	}
	out := make([]TranscriptCommittedRowFact, len(s.rows))
	copy(out, s.rows)
	return locateTranscriptCommittedRowFacts(out)
}

func (s *chatStore) walkProjectionLocked(
	applyMessage func(chatMessageRecord),
	applyLocalEntry func(localChatEntry),
	markCompactionBoundary func(),
) {
	messageIndex := 0
	localIndex := 0
	appendLocalEntries := func(messageCount int) {
		for localIndex < len(s.local) && s.local[localIndex].AfterMessageCount <= messageCount {
			local := s.local[localIndex]
			if local.MarksBoundary && markCompactionBoundary != nil {
				markCompactionBoundary()
			}
			applyLocalEntry(local)
			localIndex++
		}
	}
	if s.compact != nil && markCompactionBoundary != nil {
		markCompactionBoundary()
	}
	appendLocalEntries(0)
	for _, record := range s.messageRecords {
		applyMessage(record)
		messageIndex++
		appendLocalEntries(messageIndex)
	}
	appendLocalEntries(messageIndex)
}

func (s *chatStore) deliverySnapshot() transcriptDeliverySnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	materializedToolResults := collectMaterializedToolCalls(s.activeProviderItemsLocked())
	streamIDsByEntry := make(map[int]uuid.UUID, len(s.assistantStreamIDsByEntry))
	for entryIndex, streamID := range s.assistantStreamIDsByEntry {
		streamIDsByEntry[entryIndex] = streamID
	}
	scan := newTranscriptDeliveryFactScan(s.toolCompletions, s.toolCompletionProvenance, materializedToolResults, streamIDsByEntry, s.activeSegmentEntryStart)
	s.walkProjectionLocked(
		func(record chatMessageRecord) {
			scan.ApplyMessage(record.StepID, record.Message, record.Provenance)
		},
		func(local localChatEntry) {
			entry := local.Entry
			entry.CommittedProvenance = cloneTranscriptCommittedRowProvenance(local.Provenance)
			scan.ApplyLocalEntry(entry, local.Projected)
		},
		scan.MarkCompactionBoundary,
	)
	streaming, metadata := s.streamingSnapshotLocked()
	return transcriptDeliverySnapshot{
		Rows:              scan.Snapshot(),
		Streaming:         streaming,
		StreamingMetadata: metadata,
		StreamingStreamID: cloneTranscriptStreamID(s.streamingStreamIDLocked()),
		StreamingPhase:    s.streamingPhaseLocked(),
	}
}
