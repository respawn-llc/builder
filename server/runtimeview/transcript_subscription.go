package runtimeview

import (
	"fmt"
	"strings"

	"core/server/runtime"
	"core/shared/clientui"

	"github.com/google/uuid"
)

func TranscriptHydrationMessage(snapshot runtime.TranscriptHydrationSnapshot) clientui.TranscriptMessage {
	hydration := TranscriptHydrationFromSnapshot(snapshot)
	return clientui.TranscriptMessage{
		Kind:      clientui.TranscriptMessageHydration,
		Hydration: &hydration,
	}
}

func TranscriptHydrationFromSnapshot(runtimeSnapshot runtime.TranscriptHydrationSnapshot) clientui.TranscriptHydration {
	hydration := clientui.TranscriptHydration{
		CommittedRows: transcriptRowsFromFacts(runtimeSnapshot.CommittedRows),
	}
	if stream := transcriptAssistantStream(runtimeSnapshot.ActiveAssistantText, runtimeSnapshot.ActiveAssistantStreamID, clientui.MessagePhase(runtimeSnapshot.ActiveAssistantPhase)); stream != nil {
		hydration.ActiveAssistantStream = stream
	}
	return hydration
}

func TranscriptToolStartsFromRuntime(starts []runtime.TranscriptLiveToolStart) []clientui.TranscriptToolStart {
	if len(starts) == 0 {
		return nil
	}
	out := make([]clientui.TranscriptToolStart, 0, len(starts))
	for _, start := range starts {
		toolCallID := strings.TrimSpace(start.ToolCallID)
		if toolCallID == "" {
			continue
		}
		out = append(out, clientui.TranscriptToolStart{
			ToolCallID:       toolCallID,
			ToolName:         strings.TrimSpace(start.ToolName),
			ToolPresentation: cloneToolCallMeta(start.Presentation),
		})
	}
	return out
}

func TranscriptMessagesFromRuntimeEvent(evt runtime.Event) []clientui.TranscriptMessage {
	switch evt.Kind {
	case runtime.EventAssistantDelta:
		if evt.AssistantDelta == "" {
			return nil
		}
		var streamID uuid.UUID
		if evt.AssistantTranscriptStreamID != nil {
			streamID = *evt.AssistantTranscriptStreamID
		}
		return []clientui.TranscriptMessage{{
			Kind: clientui.TranscriptMessageAssistantDelta,
			AssistantDelta: &clientui.TranscriptAssistantDelta{
				StreamID: streamID,
				Delta:    evt.AssistantDelta,
				Phase:    clientui.MessagePhase(evt.AssistantDeltaPhase),
			},
		}}
	case runtime.EventAssistantDeltaReset:
		if strings.TrimSpace(evt.AssistantStreamAbortReason) == "" || evt.AssistantTranscriptStreamID == nil {
			return nil
		}
		streamID := *evt.AssistantTranscriptStreamID
		return []clientui.TranscriptMessage{{
			Kind: clientui.TranscriptMessageAssistantStreamAbort,
			AssistantStreamAbort: &clientui.TranscriptAssistantStreamAbort{
				StreamID: streamID,
				Reason:   clientui.TranscriptAssistantStreamAbortReason(strings.TrimSpace(evt.AssistantStreamAbortReason)),
			},
		}}
	case runtime.EventToolCallStarted:
		return transcriptToolStartMessages(runtime.TranscriptToolStartFactsFromEvent(evt))
	case runtime.EventToolCallAborted:
		reason := clientui.TranscriptToolAbortReason(strings.TrimSpace(evt.ToolAbortReason))
		if reason == "" {
			reason = clientui.TranscriptToolAbortCanceled
		}
		toolCallID := ""
		if evt.ToolCall != nil {
			toolCallID = strings.TrimSpace(evt.ToolCall.ID)
		}
		abort := clientui.TranscriptToolAbort{
			ToolCallID: toolCallID,
			Reason:     reason,
		}
		return []clientui.TranscriptMessage{{Kind: clientui.TranscriptMessageToolAbort, ToolAbort: &abort}}
	case runtime.EventQueuedUserMessageStatus:
		if evt.QueuedUserMessageStatus == nil {
			return nil
		}
		state := clientui.TranscriptQueuedOrSteeredMessageState{
			SessionID:       strings.TrimSpace(evt.QueuedUserMessageStatus.SessionID),
			QueueItemID:     strings.TrimSpace(evt.QueuedUserMessageStatus.QueueItemID),
			ClientRequestID: strings.TrimSpace(evt.QueuedUserMessageStatus.ClientRequestID),
			Status:          clientui.QueuedUserMessageStatus(evt.QueuedUserMessageStatus.Status),
			FailureReason:   clientui.QueuedUserMessageFailureReason(evt.QueuedUserMessageStatus.FailureReason),
			UserText:        evt.QueuedUserMessageStatus.RestoreText,
		}
		return []clientui.TranscriptMessage{{Kind: clientui.TranscriptMessageQueuedOrSteeredMessageState, QueuedOrSteeredMessageState: &state}}
	case runtime.EventRunStateChanged:
		if evt.RunState == nil {
			return nil
		}
		state := runtimeRunStateToClient(*evt.RunState)
		return []clientui.TranscriptMessage{{Kind: clientui.TranscriptMessageRunState, RunState: &state}}
	default:
		messages := transcriptFeedStateMessages(evt)
		messages = append(messages, transcriptCommittedRowMessages(evt)...)
		return messages
	}
}

func transcriptFeedStateMessages(evt runtime.Event) []clientui.TranscriptMessage {
	out := make([]clientui.TranscriptMessage, 0, 4)
	if evt.Compaction != nil {
		state := clientui.TranscriptCompactionStatus{
			Mode:  evt.Compaction.Mode,
			Count: evt.Compaction.Count,
			State: string(evt.Kind),
		}
		out = append(out, clientui.TranscriptMessage{Kind: clientui.TranscriptMessageCompactionStatus, CompactionStatus: &state})
	}
	if evt.ContextUsage != nil {
		usage := clientui.RuntimeContextUsage{
			UsedTokens:            evt.ContextUsage.UsedTokens,
			WindowTokens:          evt.ContextUsage.WindowTokens,
			CacheHitPercent:       evt.ContextUsage.CacheHitPercent,
			HasCacheHitPercentage: evt.ContextUsage.HasCacheHitPercentage,
		}
		out = append(out, clientui.TranscriptMessage{Kind: clientui.TranscriptMessageContextUsage, ContextUsage: &usage})
	}
	if evt.GoalStatus != nil {
		goal := clientui.TranscriptGoalStatus{
			ID:        strings.TrimSpace(evt.GoalStatus.State.ID),
			Objective: evt.GoalStatus.State.Objective,
			Status:    clientui.RuntimeGoalStatus(strings.TrimSpace(string(evt.GoalStatus.State.Status))),
			Cleared:   evt.GoalStatus.Cleared,
		}
		out = append(out, clientui.TranscriptMessage{Kind: clientui.TranscriptMessageGoalStatus, GoalStatus: &goal})
	}
	if evt.Background != nil {
		background := transcriptBackgroundActivity(*evt.Background)
		if evt.Background.ExitCode != nil {
			exitCode := *evt.Background.ExitCode
			background.ExitCode = &exitCode
		}
		out = append(out, clientui.TranscriptMessage{Kind: clientui.TranscriptMessageBackgroundActivity, BackgroundActivity: &background})
	}
	return out
}

func transcriptBackgroundActivity(evt runtime.BackgroundShellEvent) clientui.TranscriptBackgroundActivity {
	if evt.ActivityID == uuid.Nil || evt.ActivityID.Version() != 4 {
		panic(fmt.Sprintf("runtime background transcript activity missing UUIDv4 activity id: process_id=%q activity_id=%q", evt.ID, evt.ActivityID))
	}
	return clientui.TranscriptBackgroundActivity{
		ID:                evt.ActivityID.String(),
		State:             evt.State,
		Command:           evt.Command,
		Workdir:           evt.Workdir,
		LogPath:           evt.LogPath,
		Preview:           evt.Preview,
		Removed:           evt.Removed > 0,
		UserRequestedKill: evt.UserRequestedKill,
	}
}

func TranscriptSessionIdentityFromRuntime(engine *runtime.Engine) clientui.TranscriptSessionIdentity {
	if engine == nil {
		return clientui.TranscriptSessionIdentity{}
	}
	return clientui.TranscriptSessionIdentity{
		SessionID:             engine.SessionID(),
		SessionName:           engine.SessionName(),
		ConversationFreshness: ConversationFreshnessFromSession(engine.ConversationFreshness()),
	}
}

func transcriptCommittedRowMessages(evt runtime.Event) []clientui.TranscriptMessage {
	startMessages := transcriptToolStartMessages(runtime.TranscriptToolStartFactsFromEvent(evt))
	rowFacts := runtime.TranscriptCommittedRowFactsFromEvent(evt)
	if len(rowFacts) == 0 {
		return startMessages
	}
	out := make([]clientui.TranscriptMessage, 0, len(startMessages)+len(rowFacts))
	for _, fact := range rowFacts {
		row := transcriptRowFromFact(fact)
		out = append(out, clientui.TranscriptMessage{
			Kind:         clientui.TranscriptMessageCommittedRow,
			CommittedRow: &row,
		})
	}
	out = append(out, startMessages...)
	return out
}

func transcriptRowsFromFacts(facts []runtime.TranscriptCommittedRowFact) []clientui.TranscriptCommittedRow {
	if len(facts) == 0 {
		return nil
	}
	rows := make([]clientui.TranscriptCommittedRow, 0, len(facts))
	for _, fact := range facts {
		rows = append(rows, transcriptRowFromFact(fact))
	}
	return rows
}

func transcriptAssistantStream(text string, streamID *uuid.UUID, phase clientui.MessagePhase) *clientui.TranscriptAssistantStream {
	if streamID == nil {
		return nil
	}
	if text == "" {
		return nil
	}
	return &clientui.TranscriptAssistantStream{
		StreamID: *streamID,
		Text:     text,
		Phase:    phase,
	}
}

func transcriptToolStartMessages(starts []runtime.TranscriptLiveToolStart) []clientui.TranscriptMessage {
	projected := TranscriptToolStartsFromRuntime(starts)
	if len(projected) == 0 {
		return nil
	}
	out := make([]clientui.TranscriptMessage, 0, len(projected))
	for _, start := range projected {
		copyStart := start
		out = append(out, clientui.TranscriptMessage{Kind: clientui.TranscriptMessageToolStart, ToolStart: &copyStart})
	}
	return out
}

func transcriptRowFromFact(fact runtime.TranscriptCommittedRowFact) clientui.TranscriptCommittedRow {
	visibility := clientui.EntryVisibility(fact.Visibility)
	switch fact.Kind {
	case runtime.TranscriptCommittedRowFactUser:
		if fact.User == nil {
			return clientui.TranscriptCommittedRow{Visibility: visibility, Kind: clientui.TranscriptRowNotice, Notice: &clientui.TranscriptNoticeRow{Reason: clientui.TranscriptNoticeRuntimeDiagnostic, Severity: clientui.TranscriptNoticeError}}
		}
		return clientui.TranscriptCommittedRow{Visibility: visibility, Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{Text: fact.User.Text}}
	case runtime.TranscriptCommittedRowFactAssistant:
		if fact.Assistant == nil {
			return clientui.TranscriptCommittedRow{Visibility: visibility, Kind: clientui.TranscriptRowNotice, Notice: &clientui.TranscriptNoticeRow{Reason: clientui.TranscriptNoticeRuntimeDiagnostic, Severity: clientui.TranscriptNoticeError}}
		}
		row := clientui.TranscriptAssistantRow{Text: fact.Assistant.Text, Phase: clientui.MessagePhase(fact.Assistant.Phase)}
		if fact.Assistant.StreamID != nil {
			parsed := *fact.Assistant.StreamID
			row.StreamID = &parsed
		}
		return clientui.TranscriptCommittedRow{Visibility: visibility, Kind: clientui.TranscriptRowAssistant, Assistant: &row}
	case runtime.TranscriptCommittedRowFactTool:
		if fact.Tool == nil {
			return clientui.TranscriptCommittedRow{Visibility: visibility, Kind: clientui.TranscriptRowNotice, Notice: &clientui.TranscriptNoticeRow{Reason: clientui.TranscriptNoticeRuntimeDiagnostic, Severity: clientui.TranscriptNoticeError}}
		}
		return clientui.TranscriptCommittedRow{Visibility: visibility, Kind: clientui.TranscriptRowTool, Tool: &clientui.TranscriptToolRow{
			ToolCallID:       strings.TrimSpace(fact.Tool.ToolCallID),
			ToolName:         strings.TrimSpace(fact.Tool.ToolName),
			Text:             fact.Tool.Text,
			IsError:          fact.Tool.IsError,
			ResultSummary:    fact.Tool.ResultSummary,
			CondensedText:    fact.Tool.CondensedText,
			ToolPresentation: cloneToolCallMeta(fact.Tool.Presentation),
		}}
	default:
		return clientui.TranscriptCommittedRow{Visibility: visibility, Kind: clientui.TranscriptRowNotice, Notice: transcriptNoticeFromFact(fact.Notice)}
	}
}

func transcriptNoticeFromFact(fact *runtime.TranscriptNoticeRowFact) *clientui.TranscriptNoticeRow {
	if fact == nil {
		return &clientui.TranscriptNoticeRow{Reason: clientui.TranscriptNoticeRuntimeDiagnostic, Severity: clientui.TranscriptNoticeError}
	}
	notice := &clientui.TranscriptNoticeRow{
		Reason:   clientui.TranscriptNoticeReason(strings.TrimSpace(fact.Reason)),
		Severity: clientui.TranscriptNoticeSeverity(strings.TrimSpace(fact.Severity)),
		Data: clientui.TranscriptNoticeData{
			LegacyText:         fact.LegacyText,
			NoticeID:           fact.NoticeID,
			MessageType:        clientui.MessageType(strings.TrimSpace(string(fact.MessageType))),
			SourcePath:         strings.TrimSpace(fact.SourcePath),
			CondensedText:      strings.TrimSpace(fact.CondensedText),
			CompactLabel:       strings.TrimSpace(fact.CompactLabel),
			BackgroundExitCode: cloneOptionalInt(fact.BackgroundExitCode),
		},
	}
	if strings.TrimSpace(fact.DiagnosticCode) != "" || strings.TrimSpace(fact.DiagnosticDetail) != "" {
		diagnostic := &clientui.TranscriptDiagnosticData{
			Code:   strings.TrimSpace(fact.DiagnosticCode),
			Detail: strings.TrimSpace(fact.DiagnosticDetail),
		}
		notice.Diagnostic = diagnostic
		notice.Data.RuntimeDiagnostic = diagnostic
	}
	if fact.CacheWarning != nil {
		notice.Data.CacheWarning = &clientui.TranscriptCacheWarningData{
			Scope:           strings.TrimSpace(fact.CacheWarning.Scope),
			Reason:          strings.TrimSpace(fact.CacheWarning.Reason),
			LostInputTokens: fact.CacheWarning.LostInputTokens,
			Visibility:      clientui.EntryVisibility(fact.CacheWarning.Visibility),
		}
	}
	if notice.Reason == "" {
		notice.Reason = clientui.TranscriptNoticeRuntimeDiagnostic
	}
	if notice.Severity == "" {
		notice.Severity = clientui.TranscriptNoticeInfo
	}
	return notice
}
