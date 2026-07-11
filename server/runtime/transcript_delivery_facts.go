package runtime

import (
	"encoding/json"
	"strings"

	"core/server/llm"
	"core/server/tools"
	"core/shared/toolspec"
	"core/shared/transcript"
	"core/shared/valuecopy"

	"github.com/google/uuid"
)

type TranscriptCommittedRowFactKind string

const (
	TranscriptCommittedRowFactUser      TranscriptCommittedRowFactKind = "user"
	TranscriptCommittedRowFactAssistant TranscriptCommittedRowFactKind = "assistant"
	TranscriptCommittedRowFactTool      TranscriptCommittedRowFactKind = "tool"
	TranscriptCommittedRowFactNotice    TranscriptCommittedRowFactKind = "notice"
)

type transcriptChatEntryProjection uint8

const (
	transcriptCommittedStreamProjection transcriptChatEntryProjection = iota
	transcriptBoundedDetailProjection
)

type TranscriptCommittedRowFact struct {
	Visibility transcript.EntryVisibility
	Integrity  transcript.RowIntegrity
	Kind       TranscriptCommittedRowFactKind
	User       *TranscriptUserRowFact
	Assistant  *TranscriptAssistantRowFact
	Tool       *TranscriptToolRowFact
	Notice     *TranscriptNoticeRowFact
}

type TranscriptUserRowFact struct {
	Text          string
	CondensedText string
}

type TranscriptAssistantRowFact struct {
	Text          string
	CondensedText string
	Phase         llm.MessagePhase
	StreamID      *uuid.UUID
}

type TranscriptToolRowFact struct {
	ToolCallID    string
	ToolName      string
	Text          string
	IsError       bool
	ResultSummary string
	CondensedText string
	Presentation  *transcript.ToolCallMeta
}

type TranscriptNoticeRowFact struct {
	Reason             string
	Severity           string
	LegacyText         *string
	NoticeID           *string
	MessageType        llm.MessageType
	SourcePath         string
	CondensedText      string
	CompactLabel       string
	BackgroundExitCode *int
	DiagnosticCode     string
	DiagnosticDetail   string
	CacheWarning       *TranscriptCacheWarningFact
}

type TranscriptCacheWarningFact struct {
	Scope           string
	Reason          string
	LostInputTokens int
	Visibility      transcript.EntryVisibility
}

func TranscriptCommittedRowFactsFromEvent(evt Event) []TranscriptCommittedRowFact {
	switch evt.Kind {
	case EventConversationUpdated, EventAssistantMessage:
		return transcriptCommittedRowFactsFromMessage(evt.Message, evt.AssistantTranscriptStreamID, nil, nil)
	case EventUserMessageFlushed:
		if strings.TrimSpace(evt.UserMessage) == "" {
			return nil
		}
		return []TranscriptCommittedRowFact{{Kind: TranscriptCommittedRowFactUser, User: &TranscriptUserRowFact{Text: evt.UserMessage}, Visibility: transcript.EntryVisibilityOngoing}}
	case EventToolCallCompleted:
		if evt.ToolResult == nil {
			return nil
		}
		return []TranscriptCommittedRowFact{transcriptToolRowFactFromResult(*evt.ToolResult)}
	case EventCacheWarning:
		if evt.CacheWarning == nil {
			return nil
		}
		return []TranscriptCommittedRowFact{transcriptCacheWarningFact(*evt.CacheWarning, evt.CacheWarningVisibility)}
	case EventLocalEntryAdded:
		if evt.LocalEntry == nil {
			return nil
		}
		if evt.LocalEntryProjected {
			if fact, ok := transcriptCommittedRowFactFromChatEntry(*evt.LocalEntry, transcriptCommittedStreamProjection); ok {
				return []TranscriptCommittedRowFact{fact}
			}
			return nil
		}
		if fact, ok := transcriptNoticeRowFactFromChatEntry(*evt.LocalEntry); ok {
			return []TranscriptCommittedRowFact{fact}
		}
		return nil
	case EventInFlightClearFailed:
		if strings.TrimSpace(evt.Error) == "" {
			return nil
		}
		return []TranscriptCommittedRowFact{runtimeDiagnosticNoticeFact("in_flight_clear_failed", transcript.NoticeSeverityError, evt.Error)}
	case EventBackgroundUpdated:
		return nil
	default:
		return nil
	}
}

func TranscriptCommittedRowFactsFromSnapshot(snapshot ChatSnapshot) []TranscriptCommittedRowFact {
	facts := make([]TranscriptCommittedRowFact, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if strings.TrimSpace(entry.Role) == "assistant" &&
			strings.TrimSpace(entry.Text) == reviewerNoopToken {
			continue
		}
		fact, ok := transcriptCommittedRowFactFromChatEntry(entry, transcriptBoundedDetailProjection)
		if ok {
			facts = append(facts, fact)
		}
	}
	return facts
}

func TranscriptToolStartFactsFromEvent(evt Event) []TranscriptLiveToolStart {
	switch evt.Kind {
	case EventToolCallStarted:
		if evt.ToolCall == nil {
			return nil
		}
		start := transcriptLiveToolStartFromCall(*evt.ToolCall)
		if strings.TrimSpace(start.ToolCallID) == "" {
			return nil
		}
		return []TranscriptLiveToolStart{start}
	default:
		return nil
	}
}

func transcriptCommittedRowFactsFromMessage(msg llm.Message, streamID *uuid.UUID, completions map[string]tools.Result, materializedToolCalls map[string]struct{}) []TranscriptCommittedRowFact {
	switch msg.Role {
	case llm.RoleUser:
		if msg.MessageType == llm.MessageTypeCompactionSummary {
			return []TranscriptCommittedRowFact{runtimeNoticeFactFromMessage(msg, transcript.NoticeSeverityInfo)}
		}
		if strings.TrimSpace(msg.Content) == "" {
			return nil
		}
		return []TranscriptCommittedRowFact{{Kind: TranscriptCommittedRowFactUser, Visibility: transcript.EntryVisibilityOngoing, User: &TranscriptUserRowFact{Text: msg.Content}}}
	case llm.RoleAssistant:
		out := make([]TranscriptCommittedRowFact, 0, 1+len(msg.ToolCalls))
		if strings.TrimSpace(msg.Content) != "" && !isNoopFinalAnswer(msg) {
			out = append(out, TranscriptCommittedRowFact{Kind: TranscriptCommittedRowFactAssistant, Visibility: assistantTranscriptVisibility(msg.Phase), Assistant: &TranscriptAssistantRowFact{
				Text:     msg.Content,
				Phase:    msg.Phase,
				StreamID: cloneTranscriptStreamID(streamID),
			}})
		}
		for _, call := range msg.ToolCalls {
			if synthesized, ok := synthesizedTranscriptToolResultFact(call, completions, materializedToolCalls); ok {
				out = append(out, synthesized)
			}
		}
		return out
	case llm.RoleTool:
		if msg.MessageType == llm.MessageTypeCustomToolCallOutput {
			return []TranscriptCommittedRowFact{customToolCallOutputRowFact(msg, completions)}
		}
		return []TranscriptCommittedRowFact{toolMessageRowFact(msg, completions)}
	case llm.RoleDeveloper:
		if msg.MessageType == llm.MessageTypeReviewerFeedback {
			return nil
		}
		if strings.TrimSpace(msg.Content) == "" {
			if isUnknownDeveloperMessageType(msg.MessageType) {
				return []TranscriptCommittedRowFact{emptyDeveloperMessageDiagnosticFact(msg)}
			}
			return nil
		}
		return []TranscriptCommittedRowFact{runtimeNoticeFactFromMessage(msg, transcript.NoticeSeverityInfo)}
	default:
		return nil
	}
}

func customToolCallOutputRowFact(msg llm.Message, completions map[string]tools.Result) TranscriptCommittedRowFact {
	return toolMessageRowFact(msg, completions)
}

func toolMessageRowFact(msg llm.Message, completions map[string]tools.Result) TranscriptCommittedRowFact {
	callID := strings.TrimSpace(msg.ToolCallID)
	result := tools.Result{
		CallID: callID,
		Name:   toolspecIDFromString(msg.Name),
		Output: json.RawMessage(msg.Content),
	}
	if completion, ok := completions[callID]; ok {
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
		result.Name = "tool"
	}
	return transcriptToolRowFactFromResult(result)
}

func transcriptCommittedEntryCountFromMessage(msg llm.Message, completions map[string]tools.Result, materializedToolCalls map[string]struct{}) int {
	count := len(VisibleChatEntriesFromMessage(msg))
	if msg.Role != llm.RoleAssistant {
		return count
	}
	for _, call := range msg.ToolCalls {
		if _, ok := synthesizedTranscriptToolResultFact(call, completions, materializedToolCalls); ok {
			count++
		}
	}
	return count
}

func transcriptCommittedRowFactFromChatEntry(entry ChatEntry, projection transcriptChatEntryProjection) (TranscriptCommittedRowFact, bool) {
	visibility := normalizeRuntimeEntryVisibility(entry.Visibility)
	if visibility == transcript.EntryVisibilityHidden {
		return TranscriptCommittedRowFact{}, false
	}
	role := strings.TrimSpace(entry.Role)
	switch role {
	case "user":
		integrity := transcriptTextEntryIntegrity(entry.Text, entry.CondensedText, entry.CompactLabel)
		text := entry.Text
		if strings.TrimSpace(text) == "" {
			text = firstNonBlankTranscriptValue(entry.CondensedText, entry.CompactLabel)
		}
		return TranscriptCommittedRowFact{
			Kind:       TranscriptCommittedRowFactUser,
			User:       &TranscriptUserRowFact{Text: text, CondensedText: entry.CondensedText},
			Visibility: transcriptVisibilityForIntegrity(resolveTranscriptVisibility(visibility, transcript.EntryVisibilityOngoing), integrity),
			Integrity:  integrity,
		}, true
	case "assistant":
		integrity := transcriptTextEntryIntegrity(entry.Text, entry.CondensedText, entry.CompactLabel)
		text := entry.Text
		if strings.TrimSpace(text) == "" {
			text = firstNonBlankTranscriptValue(entry.CondensedText, entry.CompactLabel)
		}
		return TranscriptCommittedRowFact{
			Kind: TranscriptCommittedRowFactAssistant,
			Assistant: &TranscriptAssistantRowFact{
				Text:          text,
				CondensedText: entry.CondensedText,
				Phase:         entry.Phase,
			},
			Visibility: transcriptVisibilityForIntegrity(resolveTranscriptVisibility(visibility, transcript.EntryVisibilityOngoing), integrity),
			Integrity:  integrity,
		}, true
	case "tool_call", "tool_result_ok", "tool_result_error":
		if role == "tool_call" && projection == transcriptCommittedStreamProjection {
			return TranscriptCommittedRowFact{}, false
		}
		if strings.TrimSpace(entry.ToolCallID) == "" {
			return transcriptNoticeRowFactFromChatEntry(entry)
		}
		integrity := transcriptToolEntryIntegrity(entry)
		toolName := "tool"
		if entry.ToolCall != nil && strings.TrimSpace(entry.ToolCall.ToolName) != "" {
			toolName = strings.TrimSpace(entry.ToolCall.ToolName)
		}
		return TranscriptCommittedRowFact{
			Kind:       TranscriptCommittedRowFactTool,
			Visibility: transcriptVisibilityForIntegrity(resolveTranscriptVisibility(visibility, transcript.EntryVisibilityOngoingCollapsed), integrity),
			Integrity:  integrity,
			Tool: &TranscriptToolRowFact{
				ToolCallID:    strings.TrimSpace(entry.ToolCallID),
				ToolName:      toolName,
				Text:          entry.Text,
				IsError:       role == "tool_result_error",
				ResultSummary: strings.TrimSpace(entry.ToolResultSummary),
				CondensedText: strings.TrimSpace(firstNonBlankTranscriptValue(entry.CondensedText, entry.CompactLabel)),
				Presentation:  cloneTranscriptToolCallMeta(entry.ToolCall),
			},
		}, true
	default:
		return transcriptNoticeRowFactFromChatEntry(entry)
	}
}

func transcriptNoticeRowFactFromChatEntry(entry ChatEntry) (TranscriptCommittedRowFact, bool) {
	visibility := normalizeRuntimeEntryVisibility(entry.Visibility)
	if visibility == transcript.EntryVisibilityHidden {
		return TranscriptCommittedRowFact{}, false
	}
	integrity := transcriptNoticeEntryIntegrity(entry)
	fact := localEntryNoticeFact(entry)
	fact.Visibility = transcriptVisibilityForIntegrity(
		resolveTranscriptVisibility(visibility, defaultTranscriptNoticeVisibility(entry)),
		integrity,
	)
	fact.Integrity = integrity
	return fact, true
}

func transcriptTextEntryIntegrity(primary string, alternatives ...string) transcript.RowIntegrity {
	if strings.TrimSpace(primary) != "" {
		return transcript.RowIntegrityValid
	}
	if strings.TrimSpace(firstNonBlankTranscriptValue(alternatives...)) != "" {
		return transcript.RowIntegrityRecoverableMalformed
	}
	return transcript.RowIntegrityUnrecoverableMalformed
}

func transcriptToolEntryIntegrity(entry ChatEntry) transcript.RowIntegrity {
	if !transcriptToolEntryHasRecoverableText(entry) {
		return transcript.RowIntegrityUnrecoverableMalformed
	}
	if !entry.ToolCall.Valid() {
		return transcript.RowIntegrityRecoverableMalformed
	}
	return transcript.RowIntegrityValid
}

func transcriptToolEntryHasRecoverableText(entry ChatEntry) bool {
	if firstNonBlankTranscriptValue(entry.Text, entry.CondensedText, entry.CompactLabel, entry.ToolResultSummary) != "" {
		return true
	}
	meta := entry.ToolCall
	if meta == nil {
		return false
	}
	return firstNonBlankTranscriptValue(
		meta.ToolName,
		meta.Command,
		meta.CompactText,
		meta.PatchSummary,
		meta.PatchDetail,
		meta.Question,
	) != "" || len(meta.Suggestions) > 0 || (meta.RenderHint != nil && strings.TrimSpace(meta.RenderHint.Path) != "")
}

func transcriptNoticeEntryIntegrity(entry ChatEntry) transcript.RowIntegrity {
	if firstNonBlankTranscriptValue(entry.Text, entry.CondensedText, entry.CompactLabel, entry.SourcePath) == "" {
		return transcript.RowIntegrityUnrecoverableMalformed
	}
	messageType := entry.MessageType
	if transcript.IsReviewerEntryRole(strings.TrimSpace(entry.Role)) {
		messageType = llm.MessageTypeReviewerFeedback
	}
	if !knownTranscriptNoticeRole(strings.TrimSpace(entry.Role)) ||
		(strings.TrimSpace(string(messageType)) != "" && isUnknownDeveloperMessageType(messageType)) {
		return transcript.RowIntegrityRecoverableMalformed
	}
	return transcript.RowIntegrityValid
}

func knownTranscriptNoticeRole(role string) bool {
	switch transcript.EntryRole(role) {
	case transcript.EntryRoleSystem,
		transcript.EntryRoleWarning,
		transcript.EntryRoleCompactionSummary,
		transcript.EntryRoleManualCompactionCarryover,
		transcript.EntryRoleDeveloperContext,
		transcript.EntryRoleDeveloperFeedback,
		transcript.EntryRoleDeveloperErrorFeedback,
		transcript.EntryRoleInterruption,
		transcript.EntryRoleGoalFeedback,
		transcript.EntryRoleReviewerStatus,
		transcript.EntryRoleReviewerError,
		transcript.EntryRoleReviewerSuggestions:
		return true
	default:
		return false
	}
}

func transcriptVisibilityForIntegrity(original transcript.EntryVisibility, integrity transcript.RowIntegrity) transcript.EntryVisibility {
	switch integrity {
	case transcript.RowIntegrityValid:
		return original
	case transcript.RowIntegrityRecoverableMalformed:
		return transcript.EntryVisibilityOngoing
	case transcript.RowIntegrityUnrecoverableMalformed:
		return transcript.EntryVisibilityDetail
	default:
		panic("transcript row has invalid integrity classification")
	}
}

func resolveTranscriptVisibility(visibility transcript.EntryVisibility, fallback transcript.EntryVisibility) transcript.EntryVisibility {
	switch normalizeRuntimeEntryVisibility(visibility) {
	case transcript.EntryVisibilityOngoing:
		return transcript.EntryVisibilityOngoing
	case transcript.EntryVisibilityOngoingCollapsed:
		return transcript.EntryVisibilityOngoingCollapsed
	case transcript.EntryVisibilityDetail:
		return transcript.EntryVisibilityDetail
	case transcript.EntryVisibilityHidden:
		return transcript.EntryVisibilityHidden
	default:
		return fallback
	}
}

func defaultTranscriptNoticeVisibility(entry ChatEntry) transcript.EntryVisibility {
	messageType := entry.MessageType
	if transcript.IsReviewerEntryRole(strings.TrimSpace(entry.Role)) {
		messageType = llm.MessageTypeReviewerFeedback
	}
	if strings.TrimSpace(string(messageType)) != "" && !isUnknownDeveloperMessageType(messageType) {
		if messageType != llm.MessageTypeReviewerFeedback {
			return messageTypeTranscriptVisibility(messageType)
		}
	}
	switch transcript.EntryRole(strings.TrimSpace(entry.Role)) {
	case transcript.EntryRoleManualCompactionCarryover,
		transcript.EntryRoleDeveloperContext:
		return transcript.EntryVisibilityDetail
	case transcript.EntryRoleReviewerStatus:
		return transcript.EntryVisibilityOngoingCollapsed
	case transcript.EntryRoleReviewerSuggestions,
		transcript.EntryRoleReviewerError:
		return transcript.EntryVisibilityOngoing
	default:
		return transcript.EntryVisibilityOngoing
	}
}

func firstNonBlankTranscriptValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func localEntryNoticeFact(entry ChatEntry) TranscriptCommittedRowFact {
	if strings.TrimSpace(entry.Role) == "" {
		return legacyUntypedNoticeFactFromLocalEntry(entry)
	}
	return runtimeNoticeFactFromLocalEntry(entry)
}

func synthesizedTranscriptToolResultFact(call llm.ToolCall, completions map[string]tools.Result, materializedToolCalls map[string]struct{}) (TranscriptCommittedRowFact, bool) {
	callID := strings.TrimSpace(call.ID)
	if callID == "" {
		return TranscriptCommittedRowFact{}, false
	}
	if _, ok := materializedToolCalls[callID]; ok {
		return TranscriptCommittedRowFact{}, false
	}
	completion, ok := completions[callID]
	if !ok {
		return TranscriptCommittedRowFact{}, false
	}
	return transcriptToolRowFactFromResult(completion), true
}

func transcriptToolRowFactFromResult(result tools.Result) TranscriptCommittedRowFact {
	if strings.TrimSpace(result.CallID) == "" {
		entry := toolResultChatEntry(result)
		fact, _ := transcriptNoticeRowFactFromChatEntry(entry)
		return fact
	}
	return TranscriptCommittedRowFact{Kind: TranscriptCommittedRowFactTool, Visibility: transcript.EntryVisibilityOngoingCollapsed, Tool: &TranscriptToolRowFact{
		ToolCallID:    strings.TrimSpace(result.CallID),
		ToolName:      strings.TrimSpace(string(result.Name)),
		Text:          tools.FormatToolResultByName(string(result.Name), result.Output, result.IsError),
		IsError:       result.IsError,
		ResultSummary: strings.TrimSpace(result.Summary),
		CondensedText: strings.TrimSpace(result.CondensedText),
		Presentation:  cloneTranscriptToolCallMeta(result.Presentation),
	}}
}

func transcriptCacheWarningFact(warning transcript.CacheWarning, visibility transcript.EntryVisibility) TranscriptCommittedRowFact {
	normalized := resolveTranscriptVisibility(visibility, transcript.EntryVisibilityOngoing)
	return TranscriptCommittedRowFact{Kind: TranscriptCommittedRowFactNotice, Visibility: normalized, Notice: &TranscriptNoticeRowFact{
		Reason:   transcript.NoticeReasonCacheWarning,
		Severity: transcript.NoticeSeverityWarning,
		CacheWarning: &TranscriptCacheWarningFact{
			Scope:           string(warning.Scope),
			Reason:          string(warning.Reason),
			LostInputTokens: warning.LostInputTokens,
			Visibility:      normalized,
		},
	}}
}

func legacyUntypedNoticeFactFromLocalEntry(entry ChatEntry) TranscriptCommittedRowFact {
	noticeID := strings.TrimSpace(entry.NoticeID)
	var noticeIDPtr *string
	if noticeID != "" {
		noticeIDPtr = &noticeID
	}
	legacyText := firstNonEmpty(entry.Text, entry.CondensedText, entry.CompactLabel, entry.ToolResultSummary)
	var legacyTextPtr *string
	if legacyText != "" {
		legacyTextPtr = &legacyText
	}
	return TranscriptCommittedRowFact{Kind: TranscriptCommittedRowFactNotice, Visibility: normalizeRuntimeEntryVisibility(entry.Visibility), Notice: &TranscriptNoticeRowFact{
		Reason:     transcript.NoticeReasonLegacyUntypedNotice,
		Severity:   transcript.LegacyNoticeSeverityForRole(entry.Role),
		LegacyText: legacyTextPtr,
		NoticeID:   noticeIDPtr,
	}}
}

func runtimeNoticeFactFromMessage(msg llm.Message, severity string) TranscriptCommittedRowFact {
	code := strings.TrimSpace(string(msg.MessageType))
	if code == "" {
		code = "runtime_notice"
	}
	return TranscriptCommittedRowFact{Kind: TranscriptCommittedRowFactNotice, Visibility: messageTypeTranscriptVisibility(msg.MessageType), Notice: &TranscriptNoticeRowFact{
		Reason:             transcript.NoticeReasonRuntimeDiagnostic,
		Severity:           normalizeTranscriptNoticeSeverity(severity),
		MessageType:        msg.MessageType,
		SourcePath:         strings.TrimSpace(msg.SourcePath),
		CondensedText:      strings.TrimSpace(msg.CompactContent),
		CompactLabel:       compactLabelForMessage(msg),
		BackgroundExitCode: valuecopy.Pointer(msg.BackgroundExitCode),
		DiagnosticCode:     code,
		DiagnosticDetail:   msg.Content,
	}}
}

func runtimeNoticeFactFromLocalEntry(entry ChatEntry) TranscriptCommittedRowFact {
	noticeID := strings.TrimSpace(entry.NoticeID)
	var noticeIDPtr *string
	if noticeID != "" {
		noticeIDPtr = &noticeID
	}
	detail := firstNonEmpty(entry.Text, entry.CondensedText, entry.CompactLabel, entry.ToolResultSummary)
	messageType := entry.MessageType
	role := strings.TrimSpace(entry.Role)
	if transcript.IsReviewerEntryRole(role) {
		messageType = llm.MessageTypeReviewerFeedback
	}
	return TranscriptCommittedRowFact{Kind: TranscriptCommittedRowFactNotice, Visibility: normalizeRuntimeEntryVisibility(entry.Visibility), Notice: &TranscriptNoticeRowFact{
		Reason:             transcript.NoticeReasonRuntimeDiagnostic,
		Severity:           transcript.LegacyNoticeSeverityForRole(entry.Role),
		NoticeID:           noticeIDPtr,
		MessageType:        messageType,
		SourcePath:         strings.TrimSpace(entry.SourcePath),
		CondensedText:      strings.TrimSpace(entry.CondensedText),
		CompactLabel:       strings.TrimSpace(entry.CompactLabel),
		BackgroundExitCode: valuecopy.Pointer(entry.BackgroundExitCode),
		DiagnosticCode:     role,
		DiagnosticDetail:   detail,
	}}
}

func emptyDeveloperMessageDiagnosticFact(msg llm.Message) TranscriptCommittedRowFact {
	code := strings.TrimSpace(string(msg.MessageType))
	return TranscriptCommittedRowFact{Kind: TranscriptCommittedRowFactNotice, Visibility: transcript.EntryVisibilityDetail, Notice: &TranscriptNoticeRowFact{
		Reason:           transcript.NoticeReasonRuntimeDiagnostic,
		Severity:         transcript.NoticeSeverityInfo,
		MessageType:      msg.MessageType,
		SourcePath:       strings.TrimSpace(msg.SourcePath),
		CompactLabel:     compactLabelForMessage(msg),
		DiagnosticCode:   code,
		DiagnosticDetail: "empty developer message",
	}}
}

func runtimeDiagnosticNoticeFact(code string, severity string, detail string) TranscriptCommittedRowFact {
	code = strings.TrimSpace(code)
	if code == "" {
		code = "runtime_notice"
	}
	return TranscriptCommittedRowFact{Kind: TranscriptCommittedRowFactNotice, Visibility: transcript.EntryVisibilityOngoing, Notice: &TranscriptNoticeRowFact{
		Reason:           transcript.NoticeReasonRuntimeDiagnostic,
		Severity:         normalizeTranscriptNoticeSeverity(severity),
		DiagnosticCode:   code,
		DiagnosticDetail: detail,
	}}
}

func normalizeTranscriptNoticeSeverity(severity string) string {
	severity = strings.TrimSpace(severity)
	if severity == "" {
		return transcript.NoticeSeverityInfo
	}
	return severity
}

func toolspecIDFromString(value string) toolspec.ID {
	return toolspec.ID(strings.TrimSpace(value))
}
