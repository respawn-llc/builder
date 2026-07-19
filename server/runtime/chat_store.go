package runtime

import (
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
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
	StepID               string
	Visibility           transcript.EntryVisibility
	RollbackTargetID     *string
	Role                 string
	Text                 string
	CondensedText        string
	Phase                llm.MessagePhase
	MessageType          llm.MessageType
	SourcePath           string
	WorktreeContext      *session.WorktreeContext
	CompactLabel         string
	ToolResultSummary    string
	ToolCallID           string
	NoticeID             string
	BackgroundActivityID string
	BackgroundProcessID  string
	BackgroundExitCode   *int
	ToolCall             *transcript.ToolCallMeta
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
	CallID        string                   `json:"call_id"`
	Name          string                   `json:"name"`
	IsError       bool                     `json:"is_error"`
	Output        json.RawMessage          `json:"output"`
	Summary       string                   `json:"summary,omitempty"`
	CondensedText string                   `json:"condensed_text,omitempty"`
	Presentation  *transcript.ToolCallMeta `json:"presentation,omitempty"`
	ProviderItems []llm.ResponseItem       `json:"provider_items,omitempty"`
}

type chatStore struct {
	mu sync.RWMutex

	messageRecords []chatMessageRecord
	compact        *compactionCheckpoint
	local          []localChatEntry

	toolCompletions                   map[string]tools.Result
	toolCompletionProviderItems       map[string][]llm.ResponseItem
	assistantToolCalls                map[string]struct{}
	materializedToolResults           map[string]struct{}
	synthesizedToolResults            map[string]struct{}
	assistantStreamIDsByEntry         map[int]uuid.UUID
	activeSegmentEntryStart           int
	streaming                         *assistantStreamingState
	streamingError                    string
	cwd                               string
	lastCommittedAssistantFinalAnswer string
	transcriptEntryCount              int

	providerTokenEstimate      int
	providerTokenEstimateDirty bool
}

type chatMessageRecord struct {
	StepID        string
	Message       llm.Message
	ProviderItems []llm.ResponseItem
}

type localChatEntry struct {
	Entry             ChatEntry
	AfterMessageCount int
	AfterToolCallID   *string
	MarksBoundary     bool
	Projected         bool
}

type assistantStreamingState struct {
	text               string
	metadata           *AssistantStreamMetadata
	transcriptStreamID *uuid.UUID
	phase              llm.MessagePhase
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
		toolCompletionProviderItems: make(map[string][]llm.ResponseItem, 16),
		assistantToolCalls:          make(map[string]struct{}, 16),
		materializedToolResults:     make(map[string]struct{}, 16),
		synthesizedToolResults:      make(map[string]struct{}, 16),
		cwd:                         strings.TrimSpace(cwd),
		providerTokenEstimateDirty:  true,
	}
}

func (s *chatStore) appendMessage(stepID string, msg llm.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg = normalizeMessageForTranscript(msg, s.cwd)
	s.messageRecords = append(s.messageRecords, chatMessageRecord{
		StepID:        strings.TrimSpace(stepID),
		Message:       cloneLLMMessage(msg),
		ProviderItems: llm.ItemsFromMessages([]llm.Message{msg}),
	})
	s.applyMessageStatsLocked(msg)
	s.placeAttachedLocalEntriesAfterMaterializedToolLocked(msg)
	s.providerTokenEstimateDirty = true
}
func (s *chatStore) replaceHistory(stepID string, items []llm.ResponseItem) {
	s.replaceHistoryAtCommittedEntryStart(stepID, items, nil)
}

func (s *chatStore) replaceHistoryAtCommittedEntryStart(stepID string, items []llm.ResponseItem, committedEntryStart *int) {
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
	// Non-reviewer compaction keeps user-visible transcript history append-only by
	// materializing replacement items as synthetic local entries at the compaction
	// boundary while provider/model history switches to the compacted checkpoint.
	projectedStart := len(s.local)
	projectedEntries := transcriptEntriesFromHistoryReplacement(preparedItems)
	for index := range projectedEntries {
		projectedEntries[index].StepID = strings.TrimSpace(stepID)
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
		len(s.toolCompletionProviderItems) == 0 &&
		len(s.assistantToolCalls) == 0 &&
		len(s.materializedToolResults) == 0 &&
		len(s.synthesizedToolResults) == 0 {
		return
	}
	referenced := make(map[string]struct{}, len(s.toolCompletions))
	for _, item := range s.providerItemsSourceLocked() {
		if !isToolCallItem(item.Type) {
			continue
		}
		callID := strings.TrimSpace(item.CallID)
		if callID == "" {
			callID = strings.TrimSpace(item.ID)
		}
		if callID != "" {
			referenced[callID] = struct{}{}
		}
	}
	pruneCallIDMapToReferenced(s.toolCompletions, referenced)
	pruneCallIDMapToReferenced(s.toolCompletionProviderItems, referenced)
	pruneCallIDMapToReferenced(s.assistantToolCalls, referenced)
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
		itemCallID := strings.TrimSpace(item.CallID)
		if itemCallID == "" {
			itemCallID = strings.TrimSpace(item.ID)
		}
		if itemCallID != callID {
			continue
		}
		call := llm.ToolCall{
			ID:           itemCallID,
			Name:         strings.TrimSpace(item.Name),
			Presentation: append(json.RawMessage(nil), item.ToolPresentation...),
		}
		if item.Type == llm.ResponseItemTypeCustomToolCall {
			call.Custom = true
			call.CustomInput = item.CustomInput
			call.Input = normalizeRuntimeToolInput(item.CustomInput)
		} else {
			call.Input = append(json.RawMessage(nil), item.Arguments...)
		}
		return call, true
	}
	return llm.ToolCall{}, false
}

func (s *chatStore) restoreToolCompletionPayload(payload []byte) error {
	var completion storedToolCompletion
	if err := json.Unmarshal(payload, &completion); err != nil {
		return fmt.Errorf("decode tool_completed event: %w", err)
	}
	s.recordToolCompletionWithProviderItems(tools.Result{
		CallID:        completion.CallID,
		Name:          toolspec.ID(completion.Name),
		IsError:       completion.IsError,
		Output:        completion.Output,
		Summary:       completion.Summary,
		CondensedText: completion.CondensedText,
		Presentation:  completion.Presentation,
	}, completion.ProviderItems)
	return nil
}

func (s *chatStore) recordToolCompletionWithProviderItems(res tools.Result, providerItems []llm.ResponseItem) {
	callID := strings.TrimSpace(res.CallID)
	if callID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolCompletions[callID] = res
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

func (s *chatStore) appendStreamingDelta(stepID string, baseRevision int64, baseCommittedEntryCount int, delta string, phase llm.MessagePhase) (*AssistantStreamMetadata, *uuid.UUID) {
	if delta == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	nextMetadata := newAssistantStreamMetadata(stepID, baseRevision, baseCommittedEntryCount)
	if s.streaming == nil || assistantStreamingSegmentChanged(s.streaming.metadata, nextMetadata) {
		streamID := uuid.New()
		s.streaming = &assistantStreamingState{metadata: nextMetadata, transcriptStreamID: &streamID}
	}
	s.streaming.text += delta
	s.streaming.phase = phase
	return cloneAssistantStreamMetadata(s.streaming.metadata), cloneTranscriptStreamID(s.streaming.transcriptStreamID)
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

func (s *chatStore) appendLocalEntryRecord(entry ChatEntry, afterToolCallID *string) {
	if strings.TrimSpace(entry.Text) == "" {
		return
	}
	entry.Visibility = normalizeRuntimeEntryVisibility(entry.Visibility)
	entry.CondensedText = strings.TrimSpace(entry.CondensedText)
	entry.NoticeID = strings.TrimSpace(entry.NoticeID)
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
	})
	s.transcriptEntryCount++
}

func (s *chatStore) placeAttachedLocalEntriesAfterMaterializedToolLocked(msg llm.Message) {
	if msg.Role != llm.RoleTool {
		return
	}
	callID := strings.TrimSpace(msg.ToolCallID)
	if callID == "" {
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
	})
	s.transcriptEntryCount++
}

func (s *chatStore) committedEntryCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.transcriptEntryCount
}

func (s *chatStore) cachedLastCommittedAssistantFinalAnswer() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastCommittedAssistantFinalAnswer
}

func (s *chatStore) seedLastCommittedAssistantFinalAnswerIfEmpty(answer string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(s.lastCommittedAssistantFinalAnswer) == "" {
		s.lastCommittedAssistantFinalAnswer = answer
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
		callID := strings.TrimSpace(item.CallID)
		if callID == "" {
			callID = strings.TrimSpace(item.ID)
		}
		if callID == "" {
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
		pendingOutputs = append(pendingOutputs, llm.PrepareOpenAIInputItems([]llm.ResponseItem{{
			Type:   llm.ToolOutputItemType(item.Type == llm.ResponseItemTypeCustomToolCall),
			CallID: callID,
			Name:   firstNonEmpty(strings.TrimSpace(string(completion.Name)), strings.TrimSpace(item.Name)),
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
		callID := strings.TrimSpace(item.CallID)
		if callID == "" {
			callID = strings.TrimSpace(item.ID)
		}
		if callID == "" {
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
		seen[callID] = struct{}{}
		out = append(out, danglingToolCall{callID: callID, name: strings.TrimSpace(item.Name)})
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

func cloneLLMMessage(msg llm.Message) llm.Message {
	cloned := msg
	cloned.WorktreeContext = session.CloneWorktreeContext(msg.WorktreeContext)
	cloned.BackgroundExitCode = textutil.Pointer(msg.BackgroundExitCode)
	if len(msg.ToolCalls) > 0 {
		cloned.ToolCalls = append([]llm.ToolCall(nil), msg.ToolCalls...)
		for index := range cloned.ToolCalls {
			cloned.ToolCalls[index].Presentation = append(json.RawMessage(nil), msg.ToolCalls[index].Presentation...)
			cloned.ToolCalls[index].Input = append(json.RawMessage(nil), msg.ToolCalls[index].Input...)
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

func (s *chatStore) applyMessageStatsLocked(msg llm.Message) {
	s.applyLastCommittedAssistantFinalAnswerLocked(msg)
	delta := len(VisibleChatEntriesFromMessage(msg))
	switch msg.Role {
	case llm.RoleAssistant:
		for _, call := range msg.ToolCalls {
			callID := strings.TrimSpace(call.ID)
			if callID == "" {
				continue
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
		callID := strings.TrimSpace(msg.ToolCallID)
		if callID != "" {
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

func (s *chatStore) applyLastCommittedAssistantFinalAnswerLocked(msg llm.Message) {
	s.lastCommittedAssistantFinalAnswer = applyLastCommittedAssistantFinalAnswer(s.lastCommittedAssistantFinalAnswer, msg)
}

func applyLastCommittedAssistantFinalAnswer(current string, msg llm.Message) string {
	if messagePreservesLastCommittedAssistantFinalAnswer(msg) {
		return current
	}
	if isNoopFinalAnswer(msg) {
		return current
	}
	if msg.Role == llm.RoleAssistant && msg.Phase == llm.MessagePhaseFinal && strings.TrimSpace(msg.Content) != "" {
		return msg.Content
	}
	return ""
}

type transcriptDeliverySnapshot struct {
	Rows              []TranscriptCommittedRowFact
	Streaming         string
	StreamingMetadata *AssistantStreamMetadata
	StreamingStreamID *uuid.UUID
	StreamingPhase    llm.MessagePhase
}

type transcriptDeliveryFactScan struct {
	rows                  []TranscriptCommittedRowFact
	toolCompletions       map[string]tools.Result
	materializedToolCalls map[string]struct{}
	streamIDsByEntry      map[int]uuid.UUID
	currentEntryIndex     int
}

func newTranscriptDeliveryFactScan(completions map[string]tools.Result, materializedToolCalls map[string]struct{}, streamIDsByEntry map[int]uuid.UUID, currentEntryIndex int) *transcriptDeliveryFactScan {
	return &transcriptDeliveryFactScan{toolCompletions: completions, materializedToolCalls: materializedToolCalls, streamIDsByEntry: streamIDsByEntry, currentEntryIndex: currentEntryIndex}
}

func (s *transcriptDeliveryFactScan) ApplyMessage(stepID string, msg llm.Message) {
	if s == nil {
		return
	}
	var streamID *uuid.UUID
	if id, ok := s.streamIDsByEntry[s.currentEntryIndex]; ok {
		streamID = &id
	}
	facts := transcriptCommittedRowFactsForStep(
		stepID,
		transcriptCommittedRowFactsFromMessage(msg, streamID, s.toolCompletions, s.materializedToolCalls),
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
	return out
}

func (s *chatStore) walkProjectionLocked(
	applyMessage func(stepID string, msg llm.Message),
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
		applyMessage(record.StepID, record.Message)
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
	scan := newTranscriptDeliveryFactScan(s.toolCompletions, materializedToolResults, streamIDsByEntry, s.activeSegmentEntryStart)
	s.walkProjectionLocked(
		scan.ApplyMessage,
		func(local localChatEntry) {
			scan.ApplyLocalEntry(local.Entry, local.Projected)
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
