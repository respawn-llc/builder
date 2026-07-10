package tui

import (
	"strings"

	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"core/shared/transcript"
	"core/shared/valuecopy"
)

const (
	detailRoleUser      = "user"
	detailRoleAssistant = "assistant"
	detailRoleToolCall  = "tool_call"
	detailRoleToolOK    = "tool_result_ok"
	detailRoleToolError = "tool_result_error"
	detailFallbackTool  = "tool"
)

type detailEntry struct {
	rowData   clientui.TranscriptCommittedRow
	integrity transcriptrender.DetailIntegrity
}

func detailEntryFromChatEntry(entry clientui.ChatEntry) (detailEntry, bool) {
	visibility := transcript.NormalizeEntryVisibility(transcript.EntryVisibility(entry.Visibility))
	if visibility == transcript.EntryVisibilityHidden {
		return detailEntry{}, false
	}
	role := strings.TrimSpace(entry.Role)
	switch role {
	case detailRoleUser:
		integrity := detailTextIntegrity(entry.Text, entry.CondensedText, entry.CompactLabel)
		text := entry.Text
		if strings.TrimSpace(text) == "" {
			text = firstNonBlankValue(entry.CondensedText, entry.CompactLabel)
		}
		return newDetailEntry(clientui.TranscriptCommittedRow{
			Visibility: detailVisibility(visibility, integrity),
			Kind:       clientui.TranscriptRowUser,
			User:       &clientui.TranscriptUserRow{Text: text, CondensedText: entry.CondensedText},
		}, integrity), true
	case detailRoleAssistant:
		integrity := detailTextIntegrity(entry.Text, entry.CondensedText, entry.CompactLabel)
		text := entry.Text
		if strings.TrimSpace(text) == "" {
			text = firstNonBlankValue(entry.CondensedText, entry.CompactLabel)
		}
		return newDetailEntry(clientui.TranscriptCommittedRow{
			Visibility: detailVisibility(visibility, integrity),
			Kind:       clientui.TranscriptRowAssistant,
			Assistant: &clientui.TranscriptAssistantRow{
				Text:          text,
				CondensedText: entry.CondensedText,
				Phase:         transcript.ClassifyAssistantPhase(string(entry.Phase)),
			},
		}, integrity), true
	case detailRoleToolCall, detailRoleToolOK, detailRoleToolError:
		integrity := detailToolIntegrity(entry)
		return newDetailEntry(clientui.TranscriptCommittedRow{
			Visibility: detailVisibility(visibility, integrity),
			Kind:       clientui.TranscriptRowTool,
			Tool: &clientui.TranscriptToolRow{
				ToolCallID:       strings.TrimSpace(entry.ToolCallID),
				ToolName:         detailToolName(entry),
				Text:             entry.Text,
				IsError:          role == detailRoleToolError,
				ResultSummary:    strings.TrimSpace(entry.ToolResultSummary),
				CondensedText:    strings.TrimSpace(firstNonBlankValue(entry.CondensedText, entry.CompactLabel)),
				ToolPresentation: entry.ToolCall,
			},
		}, integrity), true
	default:
		integrity := detailNoticeIntegrity(entry)
		var diagnostic *clientui.TranscriptDiagnosticData
		if transcript.IsReviewerEntryRole(role) {
			diagnostic = &clientui.TranscriptDiagnosticData{Code: role, Detail: firstNonBlankValue(entry.Text, entry.CondensedText, entry.CompactLabel)}
		}
		return newDetailEntry(clientui.TranscriptCommittedRow{
			Visibility: detailVisibility(visibility, integrity),
			Kind:       clientui.TranscriptRowNotice,
			Notice: &clientui.TranscriptNoticeRow{
				Reason:   clientui.TranscriptNoticeReason(transcript.LegacyNoticeReasonForRole(role)),
				Severity: clientui.TranscriptNoticeSeverity(transcript.LegacyNoticeSeverityForRole(role)),
				Data: clientui.TranscriptNoticeData{
					LegacyText:         stringPointerIfNonBlank(entry.Text),
					NoticeID:           stringPointer(strings.TrimSpace(entry.NoticeID)),
					MessageType:        entry.MessageType,
					SourcePath:         entry.SourcePath,
					CondensedText:      entry.CondensedText,
					CompactLabel:       entry.CompactLabel,
					BackgroundExitCode: valuecopy.Pointer(entry.BackgroundExitCode),
				},
				Diagnostic: diagnostic,
			},
		}, integrity), true
	}
}

func newDetailEntry(row clientui.TranscriptCommittedRow, integrity transcriptrender.DetailIntegrity) detailEntry {
	return detailEntry{rowData: row, integrity: integrity}
}

func (entry detailEntry) row() clientui.TranscriptCommittedRow {
	return entry.rowData
}

func (entry detailEntry) presentation(width int, themeName string) transcriptrender.DetailPresentation {
	return transcriptrender.RenderDetailPresentation(entry.rowData, width, themeName, entry.integrity)
}

func sameDetailGroup(left, right detailEntry) bool {
	return left.rowData.Kind == right.rowData.Kind
}

func detailTextIntegrity(primary string, alternatives ...string) transcriptrender.DetailIntegrity {
	if strings.TrimSpace(primary) != "" {
		return transcriptrender.DetailIntegrityValid
	}
	if strings.TrimSpace(firstNonBlankValue(alternatives...)) != "" {
		return transcriptrender.DetailIntegrityRecoverableMalformed
	}
	return transcriptrender.DetailIntegrityUnrecoverableMalformed
}

func detailToolIntegrity(entry clientui.ChatEntry) transcriptrender.DetailIntegrity {
	if !detailToolHasRecoverableText(entry) {
		return transcriptrender.DetailIntegrityUnrecoverableMalformed
	}
	if !detailToolPresentationValid(entry.ToolCall) {
		return transcriptrender.DetailIntegrityRecoverableMalformed
	}
	return transcriptrender.DetailIntegrityValid
}

func detailToolHasRecoverableText(entry clientui.ChatEntry) bool {
	if firstNonBlankValue(entry.Text, entry.CondensedText, entry.CompactLabel, entry.ToolResultSummary) != "" {
		return true
	}
	meta := entry.ToolCall
	if meta == nil {
		return false
	}
	return firstNonBlankValue(
		meta.ToolName,
		meta.Command,
		meta.CompactText,
		meta.PatchSummary,
		meta.PatchDetail,
		meta.Question,
	) != "" || len(meta.Suggestions) > 0 || (meta.RenderHint != nil && strings.TrimSpace(meta.RenderHint.Path) != "")
}

func detailToolPresentationValid(meta *clientui.ToolCallMeta) bool {
	if meta == nil || strings.TrimSpace(meta.ToolName) == "" {
		return false
	}
	switch meta.Presentation {
	case clientui.ToolPresentationDefault, clientui.ToolPresentationShell, clientui.ToolPresentationAskQuestion:
	default:
		return false
	}
	switch meta.RenderBehavior {
	case "", clientui.ToolCallRenderBehaviorDefault, clientui.ToolCallRenderBehaviorShell, clientui.ToolCallRenderBehaviorAskQuestion:
	default:
		return false
	}
	if meta.RenderHint == nil {
		return true
	}
	switch meta.RenderHint.Kind {
	case clientui.ToolRenderKindShell, clientui.ToolRenderKindDiff, clientui.ToolRenderKindPlain:
		return true
	case clientui.ToolRenderKindSource:
		return strings.TrimSpace(meta.RenderHint.Path) != ""
	default:
		return false
	}
}

func detailNoticeIntegrity(entry clientui.ChatEntry) transcriptrender.DetailIntegrity {
	if firstNonBlankValue(entry.Text, entry.CondensedText, entry.CompactLabel, entry.SourcePath) == "" {
		return transcriptrender.DetailIntegrityUnrecoverableMalformed
	}
	if !detailNoticeRoleKnown(strings.TrimSpace(entry.Role)) || !detailMessageTypeKnown(entry.MessageType) {
		return transcriptrender.DetailIntegrityRecoverableMalformed
	}
	return transcriptrender.DetailIntegrityValid
}

func detailNoticeRoleKnown(role string) bool {
	switch transcript.EntryRole(role) {
	case transcript.EntryRoleCompactionSummary,
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

func detailMessageTypeKnown(messageType clientui.MessageType) bool {
	switch messageType {
	case clientui.MessageTypeAgentsMD,
		clientui.MessageTypeSkills,
		clientui.MessageTypeSubagents,
		clientui.MessageTypeEnvironment,
		clientui.MessageTypeCompactionSummary,
		clientui.MessageTypeInterruption,
		clientui.MessageTypeErrorFeedback,
		clientui.MessageTypeCompactionSoonReminder,
		clientui.MessageTypeHandoffFutureMessage,
		clientui.MessageTypeReviewerFeedback,
		clientui.MessageTypeBackgroundNotice,
		clientui.MessageTypeCustomToolCallOutput,
		clientui.MessageTypeManualCompactionCarryover,
		clientui.MessageTypeHeadlessMode,
		clientui.MessageTypeHeadlessModeExit,
		clientui.MessageTypeWorkflowMode,
		clientui.MessageTypeWorktreeMode,
		clientui.MessageTypeWorktreeModeExit,
		clientui.MessageTypeGoal:
		return true
	default:
		return false
	}
}

func detailVisibility(original transcript.EntryVisibility, integrity transcriptrender.DetailIntegrity) clientui.EntryVisibility {
	switch integrity {
	case transcriptrender.DetailIntegrityValid:
		return clientui.EntryVisibility(original)
	case transcriptrender.DetailIntegrityRecoverableMalformed:
		return clientui.EntryVisibilityOngoing
	case transcriptrender.DetailIntegrityUnrecoverableMalformed:
		return clientui.EntryVisibilityDetail
	default:
		panic("detail entry has invalid integrity classification")
	}
}

func detailToolName(entry clientui.ChatEntry) string {
	if entry.ToolCall != nil && strings.TrimSpace(entry.ToolCall.ToolName) != "" {
		return strings.TrimSpace(entry.ToolCall.ToolName)
	}
	return detailFallbackTool
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func stringPointerIfNonBlank(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func firstNonBlankValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
