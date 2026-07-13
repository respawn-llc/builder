package runtime

import (
	goruntime "runtime"
	"strings"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/config"
	"core/shared/transcript"
	"core/shared/valuecopy"
)

type PersistedTranscriptScanRequest struct {
	Offset int
	Limit  int

	TrackRecentTail  bool
	TailLimit        int
	CacheWarningMode config.CacheWarningMode
}

type PersistedTranscriptScan struct {
	request PersistedTranscriptScanRequest
	scan    *streamingTranscriptScan
}

func NewPersistedTranscriptScan(req PersistedTranscriptScanRequest) *PersistedTranscriptScan {
	if req.Offset < 0 {
		req.Offset = 0
	}
	if req.Limit < 0 {
		req.Limit = 0
	}
	if req.TailLimit < 0 {
		req.TailLimit = 0
	}
	return &PersistedTranscriptScan{
		request: req,
		scan: newStreamingTranscriptScan(inMemoryTranscriptScanRequest{
			Offset:          req.Offset,
			Limit:           req.Limit,
			TrackRecentTail: req.TrackRecentTail,
			TailLimit:       req.TailLimit,
		}, req.CacheWarningMode),
	}
}

func (s *PersistedTranscriptScan) ApplyPersistedEvent(evt session.Event) error {
	if s == nil {
		return nil
	}
	return s.scan.ApplyPersistedEvent(evt)
}

func (s *PersistedTranscriptScan) TotalEntries() int {
	if s == nil {
		return 0
	}
	return s.scan.TotalEntries()
}

func (s *PersistedTranscriptScan) CollectedPageSnapshot() ChatSnapshot {
	if s == nil {
		return ChatSnapshot{}
	}
	page := s.scan.PageSnapshot()
	return ChatSnapshot{Entries: clonePersistedChatEntries(page.Snapshot.Entries)}
}

func (s *PersistedTranscriptScan) RecentTailSnapshot() TranscriptWindowSnapshot {
	if s == nil {
		return TranscriptWindowSnapshot{}
	}
	if !s.request.TrackRecentTail || s.request.TailLimit <= 0 {
		return TranscriptWindowSnapshot{}
	}
	tail := s.scan.RecentTailSnapshot()
	return TranscriptWindowSnapshot{
		Snapshot:     ChatSnapshot{Entries: clonePersistedChatEntries(tail.Snapshot.Entries)},
		TotalEntries: tail.TotalEntries,
		Offset:       tail.Offset,
	}
}

func (s *PersistedTranscriptScan) LastCommittedAssistantFinalAnswer() string {
	if s == nil {
		return ""
	}
	return s.scan.LastCommittedAssistantFinalAnswer()
}

func clonePersistedChatEntries(entries []ChatEntry) []ChatEntry {
	if len(entries) == 0 {
		return nil
	}
	cloned := make([]ChatEntry, 0, len(entries))
	for _, entry := range entries {
		cloned = append(cloned, clonePersistedChatEntry(entry))
	}
	return cloned
}

func clonePersistedChatEntry(entry ChatEntry) ChatEntry {
	copyEntry := entry
	copyEntry.RollbackTargetID = valuecopy.Pointer(entry.RollbackTargetID)
	copyEntry.BackgroundExitCode = valuecopy.Pointer(entry.BackgroundExitCode)
	copyEntry.ToolCall = clonePersistedToolCallMeta(entry.ToolCall)
	return copyEntry
}

func clonePersistedToolCallMeta(meta *transcript.ToolCallMeta) *transcript.ToolCallMeta {
	if meta == nil {
		return nil
	}
	copyMeta := *meta
	if len(meta.Suggestions) > 0 {
		copyMeta.Suggestions = append([]string(nil), meta.Suggestions...)
	}
	if meta.RenderHint != nil {
		renderHint := *meta.RenderHint
		copyMeta.RenderHint = &renderHint
	}
	return &copyMeta
}

func formatPersistedToolCall(call llm.ToolCall) ChatEntry {
	meta := persistedTranscriptToolCallMeta(call)
	text := "tool call"
	if meta != nil {
		text = strings.TrimSpace(meta.Command)
	}
	if text == "" {
		text = "tool call"
	}
	return ChatEntry{
		Visibility: transcript.EntryVisibilityOngoingCollapsed,
		Role:       "tool_call",
		Text:       text,
		ToolCallID: strings.TrimSpace(call.ID),
		ToolCall:   meta,
	}
}

func persistedTranscriptToolCallMeta(call llm.ToolCall) *transcript.ToolCallMeta {
	if meta, ok := transcript.DecodeToolCallMeta(call.Presentation); ok {
		return meta
	}
	input := call.Input
	if call.Custom && strings.TrimSpace(call.CustomInput) != "" {
		input = normalizeRuntimeToolInput(call.CustomInput)
	}
	built := tools.BuildCallTranscriptMeta(call.Name, tools.ToolCallContext{
		DefaultShellPath: currentTranscriptDefaultShellPath(),
		GOOS:             goruntime.GOOS,
	}, input)
	return &built
}
