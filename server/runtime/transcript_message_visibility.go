package runtime

import (
	"fmt"
	"strings"

	"core/server/llm"
	"core/server/session"
	"core/shared/transcript"
	"core/shared/valuecopy"
)

func visibleUserTranscriptEntry(msg llm.Message) (ChatEntry, bool) {
	content := strings.TrimSpace(msg.Content)
	if content == "" {
		return ChatEntry{}, false
	}
	if msg.MessageType == llm.MessageTypeCompactionSummary {
		return compactionSummaryChatEntry(msg), true
	}
	return ChatEntry{Visibility: transcript.EntryVisibilityOngoing, Role: "user", Text: msg.Content, MessageType: msg.MessageType, SourcePath: strings.TrimSpace(msg.SourcePath), CompactLabel: compactLabelForMessage(msg)}, true
}

func isRollbackCandidateMessage(msg llm.Message) bool {
	if msg.Role != llm.RoleUser {
		return false
	}
	entry, ok := visibleUserTranscriptEntry(msg)
	return ok && strings.TrimSpace(entry.Role) == "user"
}

func visibleDeveloperChatEntry(msg llm.Message) (ChatEntry, bool) {
	if strings.TrimSpace(msg.Content) == "" {
		if isUnknownDeveloperMessageType(msg.MessageType) {
			return ChatEntry{
				Visibility:   transcript.EntryVisibilityDetail,
				Role:         string(transcript.EntryRoleDeveloperContext),
				Text:         "empty developer message",
				MessageType:  msg.MessageType,
				SourcePath:   strings.TrimSpace(msg.SourcePath),
				CompactLabel: compactLabelForMessage(msg),
			}, true
		}
		return ChatEntry{}, false
	}
	switch msg.MessageType {
	case llm.MessageTypeAgentsMD,
		llm.MessageTypeSkills,
		llm.MessageTypeEnvironment,
		llm.MessageTypeHeadlessMode,
		llm.MessageTypeHeadlessModeExit,
		llm.MessageTypeWorkflowMode:
		return developerContextEntry(msg, messageTypeTranscriptVisibility(msg.MessageType)), true
	case llm.MessageTypeWorktreeMode, llm.MessageTypeWorktreeModeExit:
		return developerContextEntry(msg, messageTypeTranscriptVisibility(msg.MessageType)), true
	case llm.MessageTypeCompactionSummary:
		return compactionSummaryChatEntry(msg), true
	case llm.MessageTypeInterruption:
		return ChatEntry{Visibility: messageTypeTranscriptVisibility(msg.MessageType), Role: string(transcript.EntryRoleInterruption), Text: msg.Content, MessageType: msg.MessageType, CompactLabel: compactLabelForMessage(msg)}, true
	case llm.MessageTypeGoal:
		return ChatEntry{Visibility: messageTypeTranscriptVisibility(msg.MessageType), Role: string(transcript.EntryRoleGoalFeedback), Text: msg.Content, CondensedText: msg.CompactContent, MessageType: msg.MessageType, CompactLabel: compactLabelForMessage(msg)}, true
	case llm.MessageTypeErrorFeedback:
		return ChatEntry{Visibility: messageTypeTranscriptVisibility(msg.MessageType), Role: string(transcript.EntryRoleDeveloperFeedback), Text: msg.Content, MessageType: msg.MessageType, CompactLabel: compactLabelForMessage(msg)}, true
	case llm.MessageTypeReviewerFeedback:
		return ChatEntry{}, false
	case llm.MessageTypeCompactionSoonReminder:
		return ChatEntry{Visibility: messageTypeTranscriptVisibility(msg.MessageType), Role: string(transcript.EntryRoleWarning), Text: msg.Content, MessageType: msg.MessageType, CompactLabel: compactLabelForMessage(msg)}, true
	case llm.MessageTypeBackgroundNotice:
		return ChatEntry{Visibility: messageTypeTranscriptVisibility(msg.MessageType), Role: string(transcript.EntryRoleSystem), Text: msg.Content, CondensedText: msg.CompactContent, MessageType: msg.MessageType, CompactLabel: compactLabelForMessage(msg), BackgroundExitCode: valuecopy.Pointer(msg.BackgroundExitCode)}, true
	case llm.MessageTypeHandoffFutureMessage:
		return developerContextEntry(msg, messageTypeTranscriptVisibility(msg.MessageType)), true
	case llm.MessageTypeManualCompactionCarryover:
		return ChatEntry{Visibility: messageTypeTranscriptVisibility(msg.MessageType), Role: string(transcript.EntryRoleManualCompactionCarryover), Text: msg.Content, MessageType: msg.MessageType, CompactLabel: compactLabelForMessage(msg)}, true
	default:
		return developerContextEntry(msg, messageTypeTranscriptVisibility(msg.MessageType)), true
	}
}

func isUnknownDeveloperMessageType(messageType llm.MessageType) bool {
	if strings.TrimSpace(string(messageType)) == "" {
		return false
	}
	switch messageType {
	case llm.MessageTypeAgentsMD,
		llm.MessageTypeSkills,
		llm.MessageTypeSubagents,
		llm.MessageTypeEnvironment,
		llm.MessageTypeCompactionSummary,
		llm.MessageTypeInterruption,
		llm.MessageTypeErrorFeedback,
		llm.MessageTypeCompactionSoonReminder,
		llm.MessageTypeHandoffFutureMessage,
		llm.MessageTypeReviewerFeedback,
		llm.MessageTypeBackgroundNotice,
		llm.MessageTypeCustomToolCallOutput,
		llm.MessageTypeManualCompactionCarryover,
		llm.MessageTypeHeadlessMode,
		llm.MessageTypeHeadlessModeExit,
		llm.MessageTypeWorkflowMode,
		llm.MessageTypeWorktreeMode,
		llm.MessageTypeWorktreeModeExit,
		llm.MessageTypeGoal:
		return false
	default:
		return true
	}
}

func compactionSummaryChatEntry(msg llm.Message) ChatEntry {
	label := compactLabelForMessage(msg)
	return ChatEntry{
		Visibility:    messageTypeTranscriptVisibility(msg.MessageType),
		Role:          string(transcript.EntryRoleCompactionSummary),
		Text:          msg.Content,
		CondensedText: label,
		MessageType:   msg.MessageType,
		SourcePath:    strings.TrimSpace(msg.SourcePath),
		CompactLabel:  label,
	}
}

func messageTypeTranscriptVisibility(messageType llm.MessageType) transcript.EntryVisibility {
	switch messageType {
	case llm.MessageTypeAgentsMD,
		llm.MessageTypeSkills,
		llm.MessageTypeEnvironment,
		llm.MessageTypeSubagents,
		llm.MessageTypeCompactionSoonReminder,
		llm.MessageTypeHandoffFutureMessage,
		llm.MessageTypeManualCompactionCarryover,
		llm.MessageTypeHeadlessMode,
		llm.MessageTypeHeadlessModeExit:
		return transcript.EntryVisibilityDetail
	case llm.MessageTypeBackgroundNotice:
		return transcript.EntryVisibilityOngoingCollapsed
	case llm.MessageTypeWorkflowMode:
		return transcript.EntryVisibilityOngoingCollapsed
	case llm.MessageTypeCompactionSummary,
		llm.MessageTypeInterruption,
		llm.MessageTypeErrorFeedback,
		llm.MessageTypeWorktreeMode,
		llm.MessageTypeWorktreeModeExit,
		llm.MessageTypeGoal:
		return transcript.EntryVisibilityOngoing
	default:
		return transcript.EntryVisibilityOngoing
	}
}

func developerContextEntry(msg llm.Message, visibility transcript.EntryVisibility) ChatEntry {
	return ChatEntry{
		Visibility:         visibility,
		Role:               string(transcript.EntryRoleDeveloperContext),
		Text:               msg.Content,
		CondensedText:      strings.TrimSpace(msg.CompactContent),
		MessageType:        msg.MessageType,
		SourcePath:         strings.TrimSpace(msg.SourcePath),
		WorktreeContext:    session.CloneWorktreeContext(msg.WorktreeContext),
		CompactLabel:       compactLabelForMessage(msg),
		BackgroundExitCode: valuecopy.Pointer(msg.BackgroundExitCode),
	}
}

func compactLabelForMessage(msg llm.Message) string {
	if label := strings.TrimSpace(msg.CompactContent); label != "" {
		return label
	}
	switch msg.MessageType {
	case llm.MessageTypeAgentsMD:
		if sourcePath := strings.TrimSpace(msg.SourcePath); sourcePath != "" {
			return fmt.Sprintf("%s file content", sourcePath)
		}
		return "AGENTS.md file content"
	case llm.MessageTypeSkills:
		return "Skill guidance"
	case llm.MessageTypeEnvironment:
		return "Environment info"
	case llm.MessageTypeHeadlessMode:
		return "Headless mode instructions"
	case llm.MessageTypeHeadlessModeExit:
		return "Interactive mode restored"
	case llm.MessageTypeWorkflowMode:
		return "Workflow mode instructions"
	case llm.MessageTypeWorktreeMode:
		return ""
	case llm.MessageTypeWorktreeModeExit:
		return ""
	case llm.MessageTypeCompactionSummary:
		return "Context compacted"
	case llm.MessageTypeInterruption:
		return "You interrupted"
	case llm.MessageTypeErrorFeedback:
		return ""
	case llm.MessageTypeBackgroundNotice:
		return ""
	case llm.MessageTypeCompactionSoonReminder:
		return "Compaction reminder"
	case llm.MessageTypeHandoffFutureMessage:
		return "Future-agent context"
	case llm.MessageTypeManualCompactionCarryover:
		return "Last user message preserved for compaction"
	default:
		if msg.Role == llm.RoleDeveloper && strings.TrimSpace(string(msg.MessageType)) != "" {
			return "Developer context: " + strings.TrimSpace(string(msg.MessageType))
		}
		return ""
	}
}
