package app

import (
	"core/cli/tui"
	"core/shared/clientui"
	"core/shared/transcript"

	tea "github.com/charmbracelet/bubbletea"
)

type uiAssistantStreamIdentity struct {
	StepID string
}

type uiDetailTranscriptWindow struct {
	loaded       bool
	entries      []tui.TranscriptEntry
	offset       int
	totalEntries int
	streaming    string
	streamingErr string
}

func (m *uiModel) primeDetailTranscriptFromCurrentTail() {
	if m == nil {
		return
	}
	m.detailTranscript = uiDetailTranscriptWindow{
		loaded:       true,
		entries:      append([]tui.TranscriptEntry(nil), m.transcriptEntries...),
		offset:       m.transcriptBaseOffset,
		totalEntries: m.transcriptTotalEntries,
		streaming:    m.view.OngoingStreamingText(),
	}
}

func (m *uiModel) maybeRequestDetailTranscriptWindow() tea.Cmd {
	return nil
}

func clientEntriesFromTranscriptEntries(entries []tui.TranscriptEntry) []clientui.ChatEntry {
	out := make([]clientui.ChatEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, clientui.ChatEntry{
			Visibility:        entry.Visibility,
			RollbackTargetID:  entry.RollbackTargetID,
			Role:              string(entry.Role),
			Text:              entry.Text,
			CondensedText:     entry.CondensedText,
			Phase:             string(entry.Phase),
			MessageType:       string(entry.MessageType),
			SourcePath:        entry.SourcePath,
			CompactLabel:      entry.CompactLabel,
			ToolResultSummary: entry.ToolResultSummary,
			ToolCallID:        entry.ToolCallID,
			NoticeID:          entry.NoticeID,
			ToolCall:          clientToolCallMetaFromTranscript(entry.ToolCall),
		})
	}
	return out
}

func transcriptToolCallMetaFromClient(meta *clientui.ToolCallMeta) *transcript.ToolCallMeta {
	if meta == nil {
		return nil
	}
	return &transcript.ToolCallMeta{
		ToolName:               meta.ToolName,
		Presentation:           transcript.ToolPresentationKind(meta.Presentation),
		RenderBehavior:         transcript.ToolCallRenderBehavior(meta.RenderBehavior),
		IsShell:                meta.IsShell,
		UserInitiated:          meta.UserInitiated,
		Command:                meta.Command,
		CompactText:            meta.CompactText,
		InlineMeta:             meta.InlineMeta,
		TimeoutLabel:           meta.TimeoutLabel,
		PatchSummary:           meta.PatchSummary,
		PatchDetail:            meta.PatchDetail,
		PatchRender:            meta.PatchRender,
		RenderHint:             transcriptToolRenderHintFromClient(meta.RenderHint),
		Question:               meta.Question,
		Suggestions:            append([]string(nil), meta.Suggestions...),
		RecommendedOptionIndex: meta.RecommendedOptionIndex,
		OmitSuccessfulResult:   meta.OmitSuccessfulResult,
		RawOutputRequested:     meta.RawOutputRequested,
		OutputTruncated:        meta.OutputTruncated,
	}
}

func clientToolCallMetaFromTranscript(meta *transcript.ToolCallMeta) *clientui.ToolCallMeta {
	if meta == nil {
		return nil
	}
	return &clientui.ToolCallMeta{
		ToolName:               meta.ToolName,
		Presentation:           clientui.ToolPresentationKind(meta.Presentation),
		RenderBehavior:         clientui.ToolCallRenderBehavior(meta.RenderBehavior),
		IsShell:                meta.IsShell,
		UserInitiated:          meta.UserInitiated,
		Command:                meta.Command,
		CompactText:            meta.CompactText,
		InlineMeta:             meta.InlineMeta,
		TimeoutLabel:           meta.TimeoutLabel,
		PatchSummary:           meta.PatchSummary,
		PatchDetail:            meta.PatchDetail,
		PatchRender:            meta.PatchRender,
		RenderHint:             clientToolRenderHintFromTranscript(meta.RenderHint),
		Question:               meta.Question,
		Suggestions:            append([]string(nil), meta.Suggestions...),
		RecommendedOptionIndex: meta.RecommendedOptionIndex,
		OmitSuccessfulResult:   meta.OmitSuccessfulResult,
		RawOutputRequested:     meta.RawOutputRequested,
		OutputTruncated:        meta.OutputTruncated,
	}
}

func transcriptToolRenderHintFromClient(hint *clientui.ToolRenderHint) *transcript.ToolRenderHint {
	if hint == nil {
		return nil
	}
	return &transcript.ToolRenderHint{
		Kind:         transcript.ToolRenderKind(hint.Kind),
		Path:         hint.Path,
		ResultOnly:   hint.ResultOnly,
		ShellDialect: transcript.ToolShellDialect(hint.ShellDialect),
	}
}

func clientToolRenderHintFromTranscript(hint *transcript.ToolRenderHint) *clientui.ToolRenderHint {
	if hint == nil {
		return nil
	}
	return &clientui.ToolRenderHint{
		Kind:         clientui.ToolRenderKind(hint.Kind),
		Path:         hint.Path,
		ResultOnly:   hint.ResultOnly,
		ShellDialect: clientui.ToolShellDialect(hint.ShellDialect),
	}
}

func cloneClientAssistantStreamMetadata(metadata *clientui.AssistantStreamMetadata) *clientui.AssistantStreamMetadata {
	if metadata == nil {
		return nil
	}
	copyMetadata := *metadata
	return &copyMetadata
}
