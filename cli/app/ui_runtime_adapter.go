package app

import (
	"strings"

	"core/cli/app/internal/runtimestate"
	"core/cli/tui"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

type uiRuntimeAdapter struct {
	model *uiModel
}

type runtimeEventApplyResult struct {
	cmd               tea.Cmd
	transcriptMutated bool
	awaitsHydration   bool
	fatal             bool
}

func (a uiRuntimeAdapter) applyProjectedRuntimeEventsBatch(events []clientui.Event) runtimeEventApplyResult {
	cmds := make([]tea.Cmd, 0, len(events))
	transcriptMutated := false
	fatal := false
	for _, evt := range events {
		result := a.applyProjectedRuntimeEvent(evt)
		cmds = append(cmds, result.cmd)
		transcriptMutated = transcriptMutated || result.transcriptMutated
		if result.fatal {
			fatal = true
			break
		}
	}
	return runtimeEventApplyResult{cmd: batchCmds(cmds...), transcriptMutated: transcriptMutated, fatal: fatal}
}

func (a uiRuntimeAdapter) applyProjectedRuntimeEvent(evt clientui.Event) runtimeEventApplyResult {
	m := a.model
	if m == nil {
		return runtimeEventApplyResult{}
	}
	if runtimeEventHasReadModelPayload(evt) && evt.ReadModelVersion.Validate() != nil {
		decision := m.startRuntimeMainViewRefreshRequest(runtimeReadModelResetMainViewRefreshRequest())
		return runtimeEventApplyResult{cmd: tea.Batch(decision.cmd, m.sendTransientStatusWithNoticeID("invalid runtime read-model update ignored; refreshing session view", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))}
	}
	if runtimeEventHasReadModelPayload(evt) {
		switch m.acceptRuntimeReadModelVersion(evt.ReadModelVersion, false) {
		case runtimeReadModelVersionIgnore:
			return runtimeEventApplyResult{cmd: m.runtimeReadModelConflictDiagnosticCmd(evt)}
		case runtimeReadModelVersionRefresh:
			decision := m.startRuntimeMainViewRefreshRequest(runtimeReadModelResetMainViewRefreshRequest())
			return runtimeEventApplyResult{cmd: decision.cmd}
		}
	}
	if m.turnQueueHook != nil {
		m.turnQueueHook.OnProjectedRuntimeEvent(evt)
	}
	reduction := runtimestate.ReduceRuntimeEvent(
		a.runtimeRunState(),
		a.runtimeConversationState(),
		a.pendingInputState(),
		a.runtimeReasoningState(),
		m.activity == uiActivityRunning,
		evt,
	)
	m.markActiveSubmitFlushed(evt)
	m.trackRuntimeActivityToken(evt)
	m.applyRuntimeEventStatus(evt)
	if !m.processList.open {
		m.applyBackgroundProcessEventToCache(evt.Background)
	}
	cmds := []tea.Cmd{
		a.applyRuntimeEventReduction(reduction),
		a.reconcileInterruptFromRunState(evt),
		a.reconcileInterruptFromRuntimeActivity(evt),
	}
	transcriptMutated := false
	if len(evt.TranscriptEntries) > 0 {
		m.appendRuntimeTranscriptEntries(evt.TranscriptEntries)
		transcriptMutated = true
	}
	for _, streamCommand := range reduction.Transcript.AssistantStream {
		switch streamCommand.Kind {
		case runtimestate.RuntimeAssistantStreamAppend:
			delta := streamCommand.Delta
			if delta == "" {
				continue
			}
			m.sawAssistantDelta = true
			m.activeAssistantStreamSource += delta
			if streamCommand.AssistantStreamMetadata != nil {
				m.activeAssistantStreamIdentity = uiAssistantStreamIdentity{StepID: streamCommand.AssistantStreamMetadata.StepID}
			}
			m.forwardToView(tui.StreamAssistantMsg{Delta: delta})
			transcriptMutated = true
		case runtimestate.RuntimeAssistantStreamClear:
			m.sawAssistantDelta = false
			m.activeAssistantStreamSource = ""
			m.activeAssistantStreamIdentity = uiAssistantStreamIdentity{}
			m.forwardToView(tui.ClearOngoingAssistantMsg{})
			transcriptMutated = true
		}
	}
	for _, streamCommand := range reduction.Reasoning.Stream {
		switch streamCommand.Kind {
		case runtimestate.RuntimeReasoningStreamUpsert:
			if streamCommand.Delta != nil {
				m.forwardToView(tui.UpsertStreamingReasoningMsg{Key: streamCommand.Delta.Key, Role: streamCommand.Delta.Role, Text: streamCommand.Delta.Text})
			}
		case runtimestate.RuntimeReasoningStreamClear:
			m.forwardToView(tui.ClearStreamingReasoningMsg{})
		}
	}
	return runtimeEventApplyResult{cmd: batchCmds(cmds...), transcriptMutated: transcriptMutated}
}

func (a uiRuntimeAdapter) applyProjectedSessionMetadata(session clientui.RuntimeSessionView) tea.Cmd {
	if a.model == nil {
		return nil
	}
	if strings.TrimSpace(session.SessionID) != "" {
		a.model.sessionID = strings.TrimSpace(session.SessionID)
	}
	if strings.TrimSpace(session.SessionName) != "" {
		a.model.sessionName = strings.TrimSpace(session.SessionName)
	}
	a.model.conversationFreshness = session.ConversationFreshness
	return nil
}

func (m *uiModel) appendRuntimeTranscriptEntries(entries []clientui.ChatEntry) {
	for _, entry := range entries {
		transcriptEntry := tuiTranscriptEntryFromClientEntry(entry)
		if transcriptEntryIsEmpty(transcriptEntry) {
			transcriptEntry = tui.TranscriptEntry{
				Role: tui.TranscriptRoleError,
				Text: "invalid empty transcript entry from runtime",
			}
		}
		m.transcriptEntries = append(m.transcriptEntries, transcriptEntry)
		m.forwardToView(appendTranscriptMsgFromEntry(transcriptEntry))
	}
	m.transcriptBaseOffset = 0
	m.transcriptTotalEntries = len(m.transcriptEntries)
	m.refreshRollbackCandidates()
}

func tuiTranscriptEntryFromClientEntry(entry clientui.ChatEntry) tui.TranscriptEntry {
	text := entry.Text
	if strings.TrimSpace(text) == "" && entry.ToolCall != nil {
		text = entry.ToolCall.Command
	}
	return tui.TranscriptEntry{
		Visibility:        entry.Visibility,
		RollbackTargetID:  entry.RollbackTargetID,
		Role:              tui.TranscriptRoleFromWire(entry.Role),
		Text:              text,
		CondensedText:     entry.CondensedText,
		Phase:             clientui.MessagePhase(entry.Phase),
		MessageType:       clientui.MessageType(entry.MessageType),
		SourcePath:        entry.SourcePath,
		CompactLabel:      entry.CompactLabel,
		ToolResultSummary: entry.ToolResultSummary,
		ToolCallID:        entry.ToolCallID,
		NoticeID:          entry.NoticeID,
		ToolCall:          transcriptToolCallMetaFromClient(entry.ToolCall),
	}
}

func transcriptEntryIsEmpty(entry tui.TranscriptEntry) bool {
	return strings.TrimSpace(entry.Text) == "" &&
		strings.TrimSpace(entry.CondensedText) == "" &&
		strings.TrimSpace(entry.CompactLabel) == "" &&
		strings.TrimSpace(entry.ToolResultSummary) == "" &&
		strings.TrimSpace(entry.ToolCallID) == "" &&
		strings.TrimSpace(entry.NoticeID) == "" &&
		entry.ToolCall == nil
}

func appendTranscriptMsgFromEntry(entry tui.TranscriptEntry) tui.AppendTranscriptMsg {
	return tui.AppendTranscriptMsg{
		Visibility:        entry.Visibility,
		Committed:         entry.Committed,
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
		ToolCall:          entry.ToolCall,
	}
}
