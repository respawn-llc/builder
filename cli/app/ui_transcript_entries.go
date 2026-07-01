package app

import (
	"strings"

	"core/cli/tui"
	"core/shared/clientui"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
)

func authoritativePageDuplicatesCommittedAssistantOngoing(entries []tui.TranscriptEntry, pageOngoing string, liveOngoing string) bool {
	trimmedPageOngoing := strings.TrimSpace(pageOngoing)
	trimmedLiveOngoing := strings.TrimSpace(liveOngoing)
	if trimmedPageOngoing != "" || trimmedLiveOngoing == "" {
		return false
	}
	for idx := len(entries) - 1; idx >= 0; idx-- {
		entry := entries[idx]
		if strings.TrimSpace(entry.Text) == "" && strings.TrimSpace(entry.CondensedText) == "" {
			continue
		}
		if entry.Role != tui.TranscriptRoleAssistant {
			return false
		}
		return strings.TrimSpace(entry.Text) == trimmedLiveOngoing
	}
	return false
}

func transcriptEntriesFromPage(page clientui.TranscriptPage) []tui.TranscriptEntry {
	entries := make([]tui.TranscriptEntry, 0, len(page.Entries))
	for _, entry := range page.Entries {
		entries = append(entries, transcriptEntryFromProjectedChatEntry(entry, false, false))
	}
	return entries
}

func committedTranscriptEntriesForApp(entries []tui.TranscriptEntry) []tui.TranscriptEntry {
	if len(entries) == 0 {
		return nil
	}
	normalized := make([]tui.TranscriptEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Transient && !entry.Committed {
			continue
		}
		copyEntry := entry
		copyEntry.Transient = false
		normalized = append(normalized, copyEntry)
	}
	return tui.CommittedOngoingEntries(normalized)
}

func transcriptEntryFromProjectedChatEntry(entry clientui.ChatEntry, transient bool, committed bool) tui.TranscriptEntry {
	role := tui.TranscriptRoleFromWire(entry.Role)
	return transcriptEntryFromProjectedChatEntryWithRole(entry, transient, committed, role)
}

func transcriptEntryFromProjectedEventEntry(_ clientui.Event, entry clientui.ChatEntry, transient bool, committed bool) tui.TranscriptEntry {
	role := tui.TranscriptRoleFromWire(entry.Role)
	return transcriptEntryFromProjectedChatEntryWithRole(entry, transient, committed, role)
}

func transcriptEntryFromProjectedChatEntryWithRole(entry clientui.ChatEntry, transient bool, committed bool, role tui.TranscriptRole) tui.TranscriptEntry {
	return tui.TranscriptEntry{
		Visibility:        entry.Visibility,
		RollbackTargetID:  entry.RollbackTargetID,
		Transient:         transient,
		Committed:         committed,
		Role:              role,
		Text:              entry.Text,
		CondensedText:     entry.CondensedText,
		Phase:             clientui.MessagePhase(entry.Phase),
		MessageType:       clientui.MessageType(entry.MessageType),
		SourcePath:        strings.TrimSpace(entry.SourcePath),
		CompactLabel:      strings.TrimSpace(entry.CompactLabel),
		ToolResultSummary: strings.TrimSpace(entry.ToolResultSummary),
		ToolCallID:        entry.ToolCallID,
		NoticeID:          strings.TrimSpace(entry.NoticeID),
		ToolCall:          transcriptToolCallMeta(entry.ToolCall),
	}
}

func appendTranscriptMsgFromEntry(entry tui.TranscriptEntry) tui.AppendTranscriptMsg {
	return tui.AppendTranscriptMsg{
		Visibility:        entry.Visibility,
		Transient:         entry.Transient,
		Committed:         entry.Committed,
		Role:              entry.Role,
		Text:              entry.Text,
		CondensedText:     entry.CondensedText,
		Phase:             entry.Phase,
		MessageType:       entry.MessageType,
		SourcePath:        strings.TrimSpace(entry.SourcePath),
		CompactLabel:      strings.TrimSpace(entry.CompactLabel),
		ToolResultSummary: strings.TrimSpace(entry.ToolResultSummary),
		ToolCallID:        strings.TrimSpace(entry.ToolCallID),
		NoticeID:          strings.TrimSpace(entry.NoticeID),
		ToolCall:          entry.ToolCall,
	}
}

func allTranscriptEntriesTransient(entries []tui.TranscriptEntry) bool {
	if len(entries) == 0 {
		return false
	}
	for _, entry := range entries {
		if !entry.Transient {
			return false
		}
	}
	return true
}

func cloneChatEntries(entries []clientui.ChatEntry) []clientui.ChatEntry {
	if len(entries) == 0 {
		return nil
	}
	cloned := make([]clientui.ChatEntry, 0, len(entries))
	for _, entry := range entries {
		copyEntry := entry
		copyEntry.ToolCallID = strings.TrimSpace(copyEntry.ToolCallID)
		copyEntry.ToolCall = transcriptToolCallMetaClient(transcriptToolCallMeta(entry.ToolCall))
		cloned = append(cloned, copyEntry)
	}
	return cloned
}

func cloneTUITranscriptEntries(entries []tui.TranscriptEntry) []tui.TranscriptEntry {
	if len(entries) == 0 {
		return nil
	}
	cloned := make([]tui.TranscriptEntry, 0, len(entries))
	for _, entry := range entries {
		copyEntry := entry
		copyEntry.ToolCall = cloneTranscriptToolCallMeta(entry.ToolCall)
		cloned = append(cloned, copyEntry)
	}
	return cloned
}

func cloneTranscriptToolCallMeta(meta *transcript.ToolCallMeta) *transcript.ToolCallMeta {
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
	if meta.PatchRender != nil {
		copyMeta.PatchRender = cloneRenderedPatch(meta.PatchRender)
	}
	return &copyMeta
}

func eventTranscriptEntriesReconcileWithCommittedTail(evt clientui.Event) bool {
	if !evt.CommittedTranscriptChanged || len(evt.TranscriptEntries) == 0 {
		return false
	}
	if evt.RecoveryCause != clientui.TranscriptRecoveryCauseNone {
		return false
	}
	_, _, ok := projectedTranscriptEventRange(evt, len(evt.TranscriptEntries))
	return ok
}

func transcriptPayloadFromTUIEntry(entry tui.TranscriptEntry) transcript.EntryPayload {
	return transcript.EntryPayload{
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
		ToolCall:          entry.ToolCall,
	}
}

func transcriptPayloadFromClientEntry(entry clientui.ChatEntry) transcript.EntryPayload {
	return transcript.EntryPayload{
		Visibility:        entry.Visibility,
		RollbackTargetID:  entry.RollbackTargetID,
		Role:              entry.Role,
		Text:              entry.Text,
		CondensedText:     entry.CondensedText,
		Phase:             entry.Phase,
		MessageType:       entry.MessageType,
		SourcePath:        entry.SourcePath,
		CompactLabel:      entry.CompactLabel,
		ToolResultSummary: entry.ToolResultSummary,
		ToolCallID:        entry.ToolCallID,
		NoticeID:          entry.NoticeID,
		ToolCall:          transcriptToolCallMeta(entry.ToolCall),
	}
}

func projectedTranscriptEventRange(evt clientui.Event, entryCount int) (int, int, bool) {
	if entryCount <= 0 {
		return 0, 0, false
	}
	if evt.CommittedEntryStartSet {
		if evt.CommittedEntryStart < 0 {
			return 0, 0, false
		}
		return evt.CommittedEntryStart, evt.CommittedEntryStart + entryCount, true
	}
	if evt.CommittedEntryCount <= 0 {
		return 0, 0, false
	}
	start := evt.CommittedEntryCount - entryCount
	if start < 0 {
		return 0, 0, false
	}
	return start, evt.CommittedEntryCount, true
}

func transcriptToolCallMeta(meta *clientui.ToolCallMeta) *transcript.ToolCallMeta {
	if meta == nil {
		return nil
	}
	out := &transcript.ToolCallMeta{
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
		Question:               meta.Question,
		RecommendedOptionIndex: meta.RecommendedOptionIndex,
		OmitSuccessfulResult:   meta.OmitSuccessfulResult,
		RawOutputRequested:     meta.RawOutputRequested,
		OutputTruncated:        meta.OutputTruncated,
	}
	if len(meta.Suggestions) > 0 {
		out.Suggestions = append([]string(nil), meta.Suggestions...)
	}
	if meta.RenderHint != nil {
		out.RenderHint = &transcript.ToolRenderHint{
			Kind:         transcript.ToolRenderKind(meta.RenderHint.Kind),
			Path:         meta.RenderHint.Path,
			ResultOnly:   meta.RenderHint.ResultOnly,
			ShellDialect: transcript.ToolShellDialect(meta.RenderHint.ShellDialect),
		}
	}
	if meta.PatchRender != nil {
		out.PatchRender = cloneRenderedPatch(meta.PatchRender)
	}
	return out
}

func transcriptToolCallMetaClient(meta *transcript.ToolCallMeta) *clientui.ToolCallMeta {
	if meta == nil {
		return nil
	}
	out := &clientui.ToolCallMeta{
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
		Question:               meta.Question,
		RecommendedOptionIndex: meta.RecommendedOptionIndex,
		OmitSuccessfulResult:   meta.OmitSuccessfulResult,
		RawOutputRequested:     meta.RawOutputRequested,
		OutputTruncated:        meta.OutputTruncated,
	}
	if len(meta.Suggestions) > 0 {
		out.Suggestions = append([]string(nil), meta.Suggestions...)
	}
	if meta.RenderHint != nil {
		out.RenderHint = &clientui.ToolRenderHint{
			Kind:         clientui.ToolRenderKind(meta.RenderHint.Kind),
			Path:         meta.RenderHint.Path,
			ResultOnly:   meta.RenderHint.ResultOnly,
			ShellDialect: clientui.ToolShellDialect(meta.RenderHint.ShellDialect),
		}
	}
	if meta.PatchRender != nil {
		out.PatchRender = cloneRenderedPatch(meta.PatchRender)
	}
	return out
}

func cloneRenderedPatch(in *patchformat.RenderedPatch) *patchformat.RenderedPatch {
	if in == nil {
		return nil
	}
	out := &patchformat.RenderedPatch{}
	if len(in.Files) > 0 {
		out.Files = make([]patchformat.RenderedFile, 0, len(in.Files))
		for _, file := range in.Files {
			copyFile := file
			if len(file.Diff) > 0 {
				copyFile.Diff = append([]string(nil), file.Diff...)
			}
			out.Files = append(out.Files, copyFile)
		}
	}
	if len(in.SummaryLines) > 0 {
		out.SummaryLines = append([]patchformat.RenderedLine(nil), in.SummaryLines...)
	}
	if len(in.DetailLines) > 0 {
		out.DetailLines = append([]patchformat.RenderedLine(nil), in.DetailLines...)
	}
	return out
}
