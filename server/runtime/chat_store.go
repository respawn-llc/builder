package runtime

import (
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/toolspec"
	"core/shared/transcript"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	DeveloperDiagnostic  *transcript.DeveloperDiagnostic
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
	CallID        string                          `json:"call_id"`
	Name          string                          `json:"name"`
	IsError       bool                            `json:"is_error"`
	Output        json.RawMessage                 `json:"output"`
	Summary       string                          `json:"summary,omitempty"`
	CondensedText string                          `json:"condensed_text,omitempty"`
	Presentation  *transcript.ToolCallMeta        `json:"presentation,omitempty"`
	ProviderItems []llm.ResponseItem              `json:"provider_items,omitempty"`
	Diagnostic    *transcript.DeveloperDiagnostic `json:"diagnostic,omitempty"`
}

func toolCompletionDiagnosticChatEntry(diagnostic transcript.DeveloperDiagnostic) ChatEntry {
	return ChatEntry{
		Visibility:          transcript.EntryVisibilityOngoing,
		Role:                string(transcript.EntryRoleDeveloperErrorFeedback),
		DeveloperDiagnostic: transcript.CloneDeveloperDiagnostic(&diagnostic),
	}
}

func validateStoredToolCompletionDiagnostic(completion storedToolCompletion) error {
	if completion.Diagnostic == nil {
		return nil
	}
	if completion.IsError {
		return errors.New("failed tool completion cannot carry a developer diagnostic")
	}
	if err := completion.Diagnostic.Validate(); err != nil {
		return fmt.Errorf("tool completion diagnostic: %w", err)
	}
	context := completion.Diagnostic.DeletionFactMismatch
	if context == nil {
		return errors.New("tool completion diagnostic requires a deletion fact mismatch context")
	}
	if strings.TrimSpace(context.CallID) != strings.TrimSpace(completion.CallID) {
		return fmt.Errorf(
			"tool completion diagnostic call id %q does not match completion call id %q",
			context.CallID,
			completion.CallID,
		)
	}
	return nil
}

type chatStore struct {
	mu sync.RWMutex

	items          []llm.ResponseItem
	messageStepIDs []string
	compact        *compactionCheckpoint
	local          []localChatEntry

	toolCompletions                    map[string]tools.Result
	toolCompletionProviderItems        map[string][]llm.ResponseItem
	toolCompletionDiagnostics          map[string]*transcript.DeveloperDiagnostic
	assistantToolCalls                 map[string]struct{}
	materializedToolResults            map[string]struct{}
	synthesizedToolResults             map[string]struct{}
	projectedToolCompletionDiagnostics map[string]struct{}
	assistantStreamIDsByEntry          map[int]uuid.UUID
	activeSegmentEntryStart            int
	streaming                          *assistantStreamingState
	streamingError                     string
	cwd                                string
	lastCommittedAssistantFinalAnswer  string
	messageCount                       int
	transcriptEntryCount               int

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
		toolCompletions:                    make(map[string]tools.Result, 16),
		toolCompletionProviderItems:        make(map[string][]llm.ResponseItem, 16),
		toolCompletionDiagnostics:          make(map[string]*transcript.DeveloperDiagnostic, 16),
		assistantToolCalls:                 make(map[string]struct{}, 16),
		materializedToolResults:            make(map[string]struct{}, 16),
		synthesizedToolResults:             make(map[string]struct{}, 16),
		projectedToolCompletionDiagnostics: make(map[string]struct{}, 16),
		cwd:                                strings.TrimSpace(cwd),
		providerTokenEstimateDirty:         true,
	}
}

func (s *chatStore) appendMessage(stepID string, msg llm.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg = normalizeMessageForTranscript(msg, s.cwd)
	s.items = append(s.items, llm.ItemsFromMessages([]llm.Message{msg})...)
	s.messageStepIDs = append(s.messageStepIDs, strings.TrimSpace(stepID))
	s.applyMessageStatsLocked(msg)
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
		CutoffItemCount:    0,
		CutoffMessageCount: 0,
		CutoffLocalCount:   0,
		Items:              llm.CloneResponseItems(preparedItems),
	}
	s.activeSegmentEntryStart = activeSegmentEntryStart
	s.pruneAssistantStreamIDsBeforeLocked(activeSegmentEntryStart)
	s.items = nil
	s.messageStepIDs = nil
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
		len(s.toolCompletionDiagnostics) == 0 &&
		len(s.assistantToolCalls) == 0 &&
		len(s.materializedToolResults) == 0 &&
		len(s.synthesizedToolResults) == 0 &&
		len(s.projectedToolCompletionDiagnostics) == 0 {
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
	pruneCallIDMapToReferenced(s.toolCompletionDiagnostics, referenced)
	pruneCallIDMapToReferenced(s.assistantToolCalls, referenced)
	pruneCallIDMapToReferenced(s.materializedToolResults, referenced)
	pruneCallIDMapToReferenced(s.synthesizedToolResults, referenced)
	pruneCallIDMapToReferenced(s.projectedToolCompletionDiagnostics, referenced)
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
	if call, ok := toolCallSnapshotFromItems(s.items, callID); ok {
		return call, true
	}
	if s.compact != nil {
		return toolCallSnapshotFromItems(s.compact.Items, callID)
	}
	return llm.ToolCall{}, false
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
	if err := validateStoredToolCompletionDiagnostic(completion); err != nil {
		return fmt.Errorf("decode tool_completed event: %w", err)
	}
	s.recordStoredToolCompletion(completion)
	return nil
}

func (s *chatStore) recordStoredToolCompletion(completion storedToolCompletion) {
	s.recordToolCompletionWithDiagnostic(tools.Result{
		CallID:        completion.CallID,
		Name:          toolspec.ID(completion.Name),
		IsError:       completion.IsError,
		Output:        completion.Output,
		Summary:       completion.Summary,
		CondensedText: completion.CondensedText,
		Presentation:  completion.Presentation,
	}, completion.ProviderItems, completion.Diagnostic)
}

func (s *chatStore) recordToolCompletionWithProviderItems(res tools.Result, providerItems []llm.ResponseItem) {
	s.recordToolCompletionWithDiagnostic(res, providerItems, nil)
}

func (s *chatStore) recordToolCompletionWithDiagnostic(res tools.Result, providerItems []llm.ResponseItem, diagnostic *transcript.DeveloperDiagnostic) {
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
	if diagnostic == nil {
		delete(s.toolCompletionDiagnostics, callID)
	} else {
		s.toolCompletionDiagnostics[callID] = transcript.CloneDeveloperDiagnostic(diagnostic)
	}
	s.providerTokenEstimateDirty = true
	s.transcriptEntryCount += s.projectToolCompletionLocked(callID)
}

func (s *chatStore) projectToolCompletionLocked(callID string) int {
	if _, ok := s.assistantToolCalls[callID]; !ok {
		return 0
	}
	if _, ok := s.toolCompletions[callID]; !ok {
		return 0
	}
	added := 0
	if _, materialized := s.materializedToolResults[callID]; !materialized {
		if _, synthesized := s.synthesizedToolResults[callID]; !synthesized {
			s.synthesizedToolResults[callID] = struct{}{}
			added++
		}
	}
	if s.toolCompletionDiagnostics[callID] != nil {
		if _, projected := s.projectedToolCompletionDiagnostics[callID]; !projected {
			s.projectedToolCompletionDiagnostics[callID] = struct{}{}
			added++
		}
	}
	return added
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

func (s *chatStore) appendLocalEntryRecord(entry ChatEntry) {
	if strings.TrimSpace(entry.Text) == "" {
		return
	}
	entry.Visibility = normalizeRuntimeEntryVisibility(entry.Visibility)
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
			delta += s.projectToolCompletionLocked(callID)
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
	rows                      []TranscriptCommittedRowFact
	toolCompletions           map[string]tools.Result
	materializedToolCalls     map[string]struct{}
	toolCompletionDiagnostics map[string]*transcript.DeveloperDiagnostic
	streamIDsByEntry          map[int]uuid.UUID
	currentEntryIndex         int
}

func newTranscriptDeliveryFactScan(
	completions map[string]tools.Result,
	materializedToolCalls map[string]struct{},
	diagnostics map[string]*transcript.DeveloperDiagnostic,
	streamIDsByEntry map[int]uuid.UUID,
	currentEntryIndex int,
) *transcriptDeliveryFactScan {
	return &transcriptDeliveryFactScan{
		toolCompletions:           completions,
		materializedToolCalls:     materializedToolCalls,
		toolCompletionDiagnostics: diagnostics,
		streamIDsByEntry:          streamIDsByEntry,
		currentEntryIndex:         currentEntryIndex,
	}
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
		transcriptCommittedRowFactsWithToolCompletionDiagnostics(
			msg,
			streamID,
			s.toolCompletions,
			s.materializedToolCalls,
			s.toolCompletionDiagnostics,
		),
	)
	s.rows = append(s.rows, facts...)
	s.currentEntryIndex += transcriptCommittedEntryCountWithToolCompletionDiagnostics(
		msg,
		s.toolCompletions,
		s.materializedToolCalls,
		s.toolCompletionDiagnostics,
	)
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
	if s.compact != nil && s.compact.CutoffMessageCount == 0 && markCompactionBoundary != nil {
		markCompactionBoundary()
	}
	appendLocalEntries(0)
	walker := newResponseItemMessageWalker(func(msg llm.Message) {
		if messageIndex >= len(s.messageStepIDs) {
			panic(fmt.Sprintf("transcript message/step identity alignment exhausted: message_index=%d step_id_count=%d", messageIndex, len(s.messageStepIDs)))
		}
		applyMessage(strings.TrimSpace(s.messageStepIDs[messageIndex]), msg)
		messageIndex++
		if s.compact != nil && messageIndex == s.compact.CutoffMessageCount && markCompactionBoundary != nil {
			markCompactionBoundary()
		}
		appendLocalEntries(messageIndex)
	})
	for _, item := range s.items {
		walker.Apply(item)
	}
	walker.Flush()
	if messageIndex != len(s.messageStepIDs) {
		panic(fmt.Sprintf("transcript message/step identity alignment has unused identities: consumed=%d step_id_count=%d", messageIndex, len(s.messageStepIDs)))
	}
	appendLocalEntries(messageIndex)
}

func (s *chatStore) deliverySnapshot() transcriptDeliverySnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	materializedToolResults := collectMaterializedToolCalls(s.items)
	streamIDsByEntry := make(map[int]uuid.UUID, len(s.assistantStreamIDsByEntry))
	for entryIndex, streamID := range s.assistantStreamIDsByEntry {
		streamIDsByEntry[entryIndex] = streamID
	}
	scan := newTranscriptDeliveryFactScan(
		s.toolCompletions,
		materializedToolResults,
		s.toolCompletionDiagnostics,
		streamIDsByEntry,
		s.activeSegmentEntryStart,
	)
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
