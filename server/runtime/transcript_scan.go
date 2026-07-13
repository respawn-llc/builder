package runtime

import (
	"encoding/json"
	"strings"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/rollbacktarget"
	"core/shared/toolspec"
)

type transcriptPageSnapshot struct {
	Snapshot     ChatSnapshot
	TotalEntries int
	Offset       int
}

type inMemoryTranscriptScanRequest struct {
	Offset int
	Limit  int

	TrackRecentTail      bool
	TailLimit            int
	CompactionItemCutoff int
}

type inMemoryTranscriptScan struct {
	request      inMemoryTranscriptScanRequest
	totalEntries int
	pageEntries  []ChatEntry

	tailEntries             []ChatEntry
	tailStart               int
	compactionEntryStart    int
	hasCompactionCheckpoint bool

	toolCompletions       map[string]tools.Result
	materializedToolCalls map[string]struct{}
}

func newInMemoryTranscriptScan(req inMemoryTranscriptScanRequest, completions map[string]tools.Result, materializedToolCalls map[string]struct{}) *inMemoryTranscriptScan {
	if req.Offset < 0 {
		req.Offset = 0
	}
	if req.Limit < 0 {
		req.Limit = 0
	}
	if req.TailLimit < 0 {
		req.TailLimit = 0
	}
	return &inMemoryTranscriptScan{
		request:               req,
		compactionEntryStart:  -1,
		toolCompletions:       completions,
		materializedToolCalls: materializedToolCalls,
	}
}

func (s *inMemoryTranscriptScan) ApplyMessage(msg llm.Message, seq int64) {
	if s == nil {
		return
	}
	for _, entry := range s.visibleEntriesFromMessage(msg, seq) {
		s.appendEntry(entry)
	}
}

func (s *inMemoryTranscriptScan) PageSnapshot() transcriptPageSnapshot {
	if s == nil {
		return transcriptPageSnapshot{}
	}
	offset := s.request.Offset
	if offset > s.totalEntries {
		offset = s.totalEntries
	}
	return transcriptPageSnapshot{
		Snapshot:     ChatSnapshot{Entries: append([]ChatEntry(nil), s.pageEntries...)},
		TotalEntries: s.totalEntries,
		Offset:       offset,
	}
}

func (s *inMemoryTranscriptScan) RecentTailSnapshot() TranscriptWindowSnapshot {
	if s == nil {
		return TranscriptWindowSnapshot{}
	}
	return TranscriptWindowSnapshot{
		Snapshot:     ChatSnapshot{Entries: append([]ChatEntry(nil), s.tailEntries...)},
		TotalEntries: s.totalEntries,
		Offset:       s.tailStart,
	}
}

func (s *inMemoryTranscriptScan) MarkCompactionBoundary() {
	if s == nil {
		return
	}
	s.hasCompactionCheckpoint = true
	s.compactionEntryStart = s.totalEntries
	if !s.request.TrackRecentTail || s.request.TailLimit <= 0 {
		return
	}
	if s.compactionEntryStart > s.tailStart {
		drop := s.compactionEntryStart - s.tailStart
		if drop >= len(s.tailEntries) {
			s.tailEntries = nil
		} else {
			s.tailEntries = append([]ChatEntry(nil), s.tailEntries[drop:]...)
		}
	}
	s.tailStart = s.compactionEntryStart
}

func (s *inMemoryTranscriptScan) visibleEntriesFromMessage(msg llm.Message, seq int64) []ChatEntry {
	entries := make([]ChatEntry, 0, 1+len(msg.ToolCalls))
	switch msg.Role {
	case llm.RoleUser:
		if entry, ok := visibleUserTranscriptEntry(msg); ok {
			if strings.TrimSpace(entry.Role) == "user" && seq > 0 {
				targetID := rollbacktarget.EncodeUserMessageSeq(seq)
				entry.RollbackTargetID = &targetID
			}
			entries = append(entries, entry)
		}
	case llm.RoleAssistant:
		if strings.TrimSpace(msg.Content) != "" && !isNoopFinalAnswer(msg) {
			entries = append(entries, ChatEntry{
				Visibility: assistantTranscriptVisibility(msg.Phase),
				Role:       "assistant",
				Text:       msg.Content,
				Phase:      msg.Phase,
			})
		}
		for _, call := range msg.ToolCalls {
			entries = append(entries, formatPersistedToolCall(call))
			if synthesized, ok := s.synthesizedToolResult(call); ok {
				entries = append(entries, synthesized)
			}
		}
	case llm.RoleTool:
		callID := strings.TrimSpace(msg.ToolCallID)
		result := tools.Result{
			CallID: callID,
			Name:   toolspec.ID(strings.TrimSpace(msg.Name)),
			Output: json.RawMessage(msg.Content),
		}
		if completion, ok := s.toolCompletions[callID]; ok {
			if result.Name == "" {
				result.Name = completion.Name
			}
			if strings.TrimSpace(msg.Content) == "" && len(completion.Output) > 0 {
				result.Output = completion.Output
			}
			result.IsError = completion.IsError
			result.Summary = completion.Summary
			result.CondensedText = completion.CondensedText
			result.Presentation = completion.Presentation
		}
		if result.Name == "" {
			result.Name = toolspec.ID("tool")
		}
		entries = append(entries, toolResultChatEntry(result))
	case llm.RoleDeveloper:
		if entry, ok := visibleDeveloperChatEntry(msg); ok {
			entries = append(entries, entry)
		}
	}
	return entries
}

func (s *inMemoryTranscriptScan) synthesizedToolResult(call llm.ToolCall) (ChatEntry, bool) {
	if s == nil {
		return ChatEntry{}, false
	}
	callID := strings.TrimSpace(call.ID)
	if callID == "" {
		return ChatEntry{}, false
	}
	if _, ok := s.materializedToolCalls[callID]; ok {
		return ChatEntry{}, false
	}
	completion, ok := s.toolCompletions[callID]
	if !ok {
		return ChatEntry{}, false
	}
	return toolResultChatEntry(completion), true
}

func (s *inMemoryTranscriptScan) appendEntry(entry ChatEntry) {
	entry.Visibility = normalizeRuntimeEntryVisibility(entry.Visibility)
	entryIndex := s.totalEntries
	if entryIndex >= s.request.Offset && (s.request.Limit == 0 || entryIndex < s.request.Offset+s.request.Limit) {
		s.pageEntries = append(s.pageEntries, clonePersistedChatEntry(entry))
	}
	s.totalEntries++
	if s.request.TrackRecentTail && s.request.TailLimit > 0 {
		startLastN := s.totalEntries - s.request.TailLimit
		if startLastN < 0 {
			startLastN = 0
		}
		start := startLastN
		if s.hasCompactionCheckpoint && s.compactionEntryStart >= 0 {
			start = s.compactionEntryStart
		}
		if start > s.tailStart {
			drop := start - s.tailStart
			if drop >= len(s.tailEntries) {
				s.tailEntries = nil
			} else {
				s.tailEntries = append([]ChatEntry(nil), s.tailEntries[drop:]...)
			}
			s.tailStart = start
		}
		if s.tailEntries == nil {
			s.tailStart = start
		}
		s.tailEntries = append(s.tailEntries, clonePersistedChatEntry(entry))
	}
}

type responseItemMessageWalker struct {
	currentAssistant *llm.Message
	emit             func(llm.Message)
}

func newResponseItemMessageWalker(emit func(llm.Message)) *responseItemMessageWalker {
	return &responseItemMessageWalker{emit: emit}
}

func (w *responseItemMessageWalker) Apply(item llm.ResponseItem) {
	if w == nil {
		return
	}
	switch item.Type {
	case llm.ResponseItemTypeMessage:
		role := item.Role
		if role == "" {
			role = llm.RoleUser
		}
		msg := llm.Message{
			Role:            role,
			MessageType:     item.MessageType,
			SourcePath:      item.SourcePath,
			WorktreeContext: session.CloneWorktreeContext(item.WorktreeContext),
			Phase:           item.Phase,
			Content:         item.Content,
			CompactContent:  item.CompactContent,
			Name:            item.Name,
		}
		if role == llm.RoleAssistant {
			w.flushAssistant()
			w.currentAssistant = &msg
			return
		}
		w.flushAssistant()
		w.emit(msg)
	case llm.ResponseItemTypeFunctionCall, llm.ResponseItemTypeCustomToolCall:
		assistant := w.ensureAssistant()
		callID := strings.TrimSpace(item.CallID)
		if callID == "" {
			callID = strings.TrimSpace(item.ID)
		}
		call := llm.ToolCall{
			ID:           callID,
			Name:         item.Name,
			Presentation: append(json.RawMessage(nil), item.ToolPresentation...),
			Input:        normalizeRuntimeToolInput(string(item.Arguments)),
			Custom:       llm.ResponseItemTypeIsCustomToolCall(item.Type),
			CustomInput:  item.CustomInput,
		}
		if call.Custom && strings.TrimSpace(call.CustomInput) != "" {
			call.Input = normalizeRuntimeToolInput(call.CustomInput)
		}
		assistant.ToolCalls = append(assistant.ToolCalls, call)
	case llm.ResponseItemTypeFunctionCallOutput, llm.ResponseItemTypeCustomToolOutput:
		w.flushAssistant()
		callID := strings.TrimSpace(item.CallID)
		if callID == "" {
			callID = strings.TrimSpace(item.ID)
		}
		if callID == "" {
			return
		}
		w.emit(llm.Message{
			Role:        llm.RoleTool,
			MessageType: llm.ToolOutputMessageType(item.Type == llm.ResponseItemTypeCustomToolOutput),
			ToolCallID:  callID,
			Name:        item.Name,
			Content:     stringFromJSONRawRuntime(item.Output),
		})
	case llm.ResponseItemTypeReasoning:
		id := strings.TrimSpace(item.ID)
		encrypted := strings.TrimSpace(item.EncryptedContent)
		if id == "" || encrypted == "" {
			return
		}
		assistant := w.ensureAssistant()
		assistant.ReasoningItems = append(assistant.ReasoningItems, llm.ReasoningItem{ID: id, EncryptedContent: encrypted})
	}
}

func (w *responseItemMessageWalker) Flush() {
	if w == nil {
		return
	}
	w.flushAssistant()
}

func (w *responseItemMessageWalker) ensureAssistant() *llm.Message {
	if w.currentAssistant != nil {
		return w.currentAssistant
	}
	w.currentAssistant = &llm.Message{Role: llm.RoleAssistant}
	return w.currentAssistant
}

func (w *responseItemMessageWalker) flushAssistant() {
	if w.currentAssistant == nil {
		return
	}
	msg := *w.currentAssistant
	w.currentAssistant = nil
	if strings.TrimSpace(msg.Content) == "" && len(msg.ToolCalls) == 0 && len(msg.ReasoningItems) == 0 {
		return
	}
	w.emit(msg)
}

func stringFromJSONRawRuntime(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return trimmed
}

func normalizeRuntimeToolInput(arguments string) json.RawMessage {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return json.RawMessage(`{}`)
	}
	if json.Valid([]byte(arguments)) {
		return json.RawMessage(arguments)
	}
	quoted, _ := json.Marshal(arguments)
	return quoted
}

func collectMaterializedToolCalls(items []llm.ResponseItem) map[string]struct{} {
	out := make(map[string]struct{})
	walker := newResponseItemMessageWalker(func(msg llm.Message) {
		if msg.Role != llm.RoleTool {
			return
		}
		callID := strings.TrimSpace(msg.ToolCallID)
		if callID == "" {
			return
		}
		out[callID] = struct{}{}
	})
	for _, item := range items {
		walker.Apply(item)
	}
	walker.Flush()
	return out
}
