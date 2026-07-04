package runtime

import (
	"core/server/llm"
	"core/server/tools"
	"core/shared/toolspec"
	"core/shared/transcript"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/google/uuid"
)

type ChatEntry struct {
	Visibility        transcript.EntryVisibility
	RollbackTargetID  string
	Role              string
	Text              string
	CondensedText     string
	Phase             llm.MessagePhase
	MessageType       llm.MessageType
	SourcePath        string
	CompactLabel      string
	ToolResultSummary string
	ToolCallID        string
	NoticeID          string
	ToolCall          *transcript.ToolCallMeta
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

	items   []llm.ResponseItem
	compact *compactionCheckpoint
	local   []localChatEntry

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
	messageCount                      int
	transcriptEntryCount              int

	providerTokenEstimate      int
	providerTokenEstimateDirty bool
}

type localChatEntry struct {
	Entry             ChatEntry
	AfterMessageCount int
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
	CutoffItemCount    int
	CutoffMessageCount int
	CutoffLocalCount   int
	Items              []llm.ResponseItem
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

func (s *chatStore) appendMessage(msg llm.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg = normalizeMessageForTranscript(msg, s.cwd)
	s.items = append(s.items, llm.ItemsFromMessages([]llm.Message{msg})...)
	s.applyMessageStatsLocked(msg)
	s.providerTokenEstimateDirty = true
}
func (s *chatStore) replaceHistory(items []llm.ResponseItem) {
	s.replaceHistoryAtCommittedEntryStart(items, nil)
}

func (s *chatStore) replaceHistoryAtCommittedEntryStart(items []llm.ResponseItem, committedEntryStart *int) {
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
	s.appendProjectedHistoryReplacementEntriesLocked(transcriptEntriesFromHistoryReplacement(preparedItems))
	s.local = append([]localChatEntry(nil), s.local[projectedStart:]...)
	s.compact = &compactionCheckpoint{
		CutoffItemCount:    0,
		CutoffMessageCount: 0,
		CutoffLocalCount:   0,
		Items:              llm.CloneResponseItems(preparedItems),
	}
	s.activeSegmentEntryStart = activeSegmentEntryStart
	s.pruneAssistantStreamIDsBeforeLocked(activeSegmentEntryStart)
	s.items = nil
	s.messageCount = 0
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

func (s *chatStore) transcriptStreamingSnapshot() (string, *uuid.UUID) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.streaming == nil {
		return "", nil
	}
	return s.streaming.text, cloneTranscriptStreamID(s.streaming.transcriptStreamID)
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

func (s *chatStore) appendLocalEntryRecord(entry ChatEntry) {
	if strings.TrimSpace(entry.Text) == "" {
		return
	}
	entry.Visibility = transcript.NormalizeEntryVisibility(entry.Visibility)
	entry.CondensedText = strings.TrimSpace(entry.CondensedText)
	entry.NoticeID = strings.TrimSpace(entry.NoticeID)
	s.mu.Lock()
	defer s.mu.Unlock()
	messageCount := s.messageCount
	s.local = append(s.local, localChatEntry{
		Entry:             entry,
		AfterMessageCount: messageCount,
	})
	s.transcriptEntryCount++
}

func (s *chatStore) appendProjectedHistoryReplacementEntriesLocked(entries []ChatEntry) {
	for idx, entry := range entries {
		s.appendProjectedEntryLocked(entry, idx == 0)
	}
}

func (s *chatStore) appendProjectedEntryLocked(entry ChatEntry, marksBoundary bool) {
	entry.Visibility = transcript.NormalizeEntryVisibility(entry.Visibility)
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
	if s.compact == nil {
		return llm.CloneResponseItems(s.items)
	}
	base := llm.CloneResponseItems(s.compact.Items)
	tailStart := s.compact.CutoffItemCount
	if tailStart < 0 {
		tailStart = 0
	}
	if tailStart >= len(s.items) {
		return base
	}
	tail := llm.CloneResponseItems(s.items[tailStart:])
	out := make([]llm.ResponseItem, 0, len(base)+len(tail))
	out = append(out, base...)
	out = append(out, tail...)
	return out
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
	s.messageCount++
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

func (s *chatStore) recentTailSnapshot(maxEntries int) TranscriptWindowSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items, localEntries := llm.CloneResponseItems(s.items), append([]localChatEntry(nil), s.local...)
	materializedToolResults := collectMaterializedToolCalls(items)
	scan := newInMemoryTranscriptScan(inMemoryTranscriptScanRequest{
		TrackRecentTail: true,
		TailLimit:       maxEntries,
	}, s.toolCompletions, materializedToolResults)
	localIndex := 0
	processedMessages := 0
	appendLocalEntries := func(messageCount int) {
		for localIndex < len(localEntries) {
			if localEntries[localIndex].AfterMessageCount > messageCount {
				break
			}
			if localEntries[localIndex].MarksBoundary {
				scan.MarkCompactionBoundary()
			}
			scan.appendEntry(localEntries[localIndex].Entry)
			localIndex++
		}
	}
	if s.compact != nil && s.compact.CutoffMessageCount == 0 {
		scan.MarkCompactionBoundary()
	}
	appendLocalEntries(0)
	walker := newResponseItemMessageWalker(func(msg llm.Message) {
		scan.ApplyMessage(msg, 0)
		processedMessages++
		if s.compact != nil && processedMessages == s.compact.CutoffMessageCount {
			scan.MarkCompactionBoundary()
		}
		appendLocalEntries(processedMessages)
	})
	for _, item := range items {
		walker.Apply(item)
	}
	walker.Flush()
	appendLocalEntries(processedMessages)
	window := scan.RecentTailSnapshot()
	window.Snapshot.Streaming, window.Snapshot.StreamingMetadata = s.streamingSnapshotLocked()
	window.Snapshot.StreamingError = s.streamingError
	return window
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

func (s *transcriptDeliveryFactScan) ApplyMessage(msg llm.Message) {
	if s == nil {
		return
	}
	var streamID *uuid.UUID
	if id, ok := s.streamIDsByEntry[s.currentEntryIndex]; ok {
		streamID = &id
	}
	facts := transcriptCommittedRowFactsFromMessage(msg, streamID, s.toolCompletions, s.materializedToolCalls)
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
		s.rows = append(s.rows, localEntryNoticeFact(entry))
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

func (s *chatStore) deliverySnapshot() transcriptDeliverySnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items, localEntries := llm.CloneResponseItems(s.items), append([]localChatEntry(nil), s.local...)
	materializedToolResults := collectMaterializedToolCalls(items)
	streamIDsByEntry := make(map[int]uuid.UUID, len(s.assistantStreamIDsByEntry))
	for entryIndex, streamID := range s.assistantStreamIDsByEntry {
		streamIDsByEntry[entryIndex] = streamID
	}
	scan := newTranscriptDeliveryFactScan(s.toolCompletions, materializedToolResults, streamIDsByEntry, s.activeSegmentEntryStart)
	localIndex := 0
	processedMessages := 0
	appendLocalEntries := func(messageCount int) {
		for localIndex < len(localEntries) {
			if localEntries[localIndex].AfterMessageCount > messageCount {
				break
			}
			if localEntries[localIndex].MarksBoundary {
				scan.MarkCompactionBoundary()
			}
			scan.ApplyLocalEntry(localEntries[localIndex].Entry, localEntries[localIndex].Projected)
			localIndex++
		}
	}
	if s.compact != nil && s.compact.CutoffMessageCount == 0 {
		scan.MarkCompactionBoundary()
	}
	appendLocalEntries(0)
	walker := newResponseItemMessageWalker(func(msg llm.Message) {
		scan.ApplyMessage(msg)
		processedMessages++
		if s.compact != nil && processedMessages == s.compact.CutoffMessageCount {
			scan.MarkCompactionBoundary()
		}
		appendLocalEntries(processedMessages)
	})
	for _, item := range items {
		walker.Apply(item)
	}
	walker.Flush()
	appendLocalEntries(processedMessages)
	streaming, metadata := s.streamingSnapshotLocked()
	return transcriptDeliverySnapshot{
		Rows:              scan.Snapshot(),
		Streaming:         streaming,
		StreamingMetadata: metadata,
		StreamingStreamID: cloneTranscriptStreamID(s.streamingStreamIDLocked()),
		StreamingPhase:    s.streamingPhaseLocked(),
	}
}

func (s *chatStore) transcriptPageSnapshot(offset, limit int) transcriptPageSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items, localEntries := llm.CloneResponseItems(s.items), append([]localChatEntry(nil), s.local...)
	materializedToolResults := collectMaterializedToolCalls(items)
	scan := newInMemoryTranscriptScan(inMemoryTranscriptScanRequest{Offset: offset, Limit: limit}, s.toolCompletions, materializedToolResults)
	localIndex := 0
	processedMessages := 0
	appendLocalEntries := func(messageCount int) {
		for localIndex < len(localEntries) {
			if localEntries[localIndex].AfterMessageCount > messageCount {
				break
			}
			scan.appendEntry(localEntries[localIndex].Entry)
			localIndex++
		}
	}
	appendLocalEntries(0)
	walker := newResponseItemMessageWalker(func(msg llm.Message) {
		scan.ApplyMessage(msg, 0)
		processedMessages++
		appendLocalEntries(processedMessages)
	})
	for _, item := range items {
		walker.Apply(item)
	}
	walker.Flush()
	appendLocalEntries(processedMessages)
	page := scan.PageSnapshot()
	page.Snapshot.Streaming, page.Snapshot.StreamingMetadata = s.streamingSnapshotLocked()
	page.Snapshot.StreamingError = s.streamingError
	return page
}

type materializedChatSnapshot struct {
	Snapshot             ChatSnapshot
	CompactionEntryStart int
}

func (s *chatStore) snapshotWithMetadata() materializedChatSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items, localEntries := llm.CloneResponseItems(s.items), append([]localChatEntry(nil), s.local...)
	materializedToolResults := collectMaterializedToolCalls(items)
	scan := newInMemoryTranscriptScan(inMemoryTranscriptScanRequest{Offset: 0, Limit: 0}, s.toolCompletions, materializedToolResults)
	localIndex := 0
	appendLocalEntries := func(processedMessages int) {
		for localIndex < len(localEntries) {
			if localEntries[localIndex].AfterMessageCount > processedMessages {
				break
			}
			scan.appendEntry(localEntries[localIndex].Entry)
			localIndex++
		}
	}
	appendLocalEntries(0)
	processedMessages := 0
	walker := newResponseItemMessageWalker(func(msg llm.Message) {
		scan.ApplyMessage(msg, 0)
		processedMessages++
		appendLocalEntries(processedMessages)
	})
	for _, item := range items {
		walker.Apply(item)
	}
	walker.Flush()
	appendLocalEntries(processedMessages)
	snapshot := scan.PageSnapshot().Snapshot
	snapshot.Streaming, snapshot.StreamingMetadata = s.streamingSnapshotLocked()
	snapshot.StreamingError = s.streamingError
	return materializedChatSnapshot{
		Snapshot:             snapshot,
		CompactionEntryStart: -1,
	}
}

func (s *chatStore) formatToolCall(call llm.ToolCall) ChatEntry {
	meta := decodeToolCallMeta(call)
	text := "tool call"
	if meta != nil {
		text = strings.TrimSpace(meta.Command)
	}
	if text == "" {
		text = "tool call"
	}
	return ChatEntry{
		Role:       "tool_call",
		Text:       text,
		ToolCallID: strings.TrimSpace(call.ID),
		ToolCall:   meta,
	}
}
