package runtime

import (
	"fmt"
	"strings"

	"core/server/llm"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/textutil"
	"core/shared/transcript"
)

func visibleUserTranscriptEntry(msg llm.Message) (ChatEntry, bool) {
	if msg.Content == nil {
		return ChatEntry{}, false
	}
	content := strings.TrimSpace(*msg.Content)
	if content == "" {
		return ChatEntry{}, false
	}
	messageType, _ := textutil.OptionalValue(msg.MessageType)
	sourcePath, _ := textutil.OptionalTrimmed(msg.SourcePath)
	if messageType == llm.MessageTypeCompactionSummary {
		return compactionSummaryChatEntry(msg), true
	}
	return ChatEntry{Visibility: transcript.EntryVisibilityOngoing, Role: "user", Text: *msg.Content, MessageType: messageType, SourcePath: sourcePath, CompactLabel: compactLabelForMessage(msg)}, true
}

func isRollbackCandidateMessage(msg llm.Message) bool {
	if msg.Role != llm.RoleUser {
		return false
	}
	entry, ok := visibleUserTranscriptEntry(msg)
	return ok && strings.TrimSpace(entry.Role) == "user"
}

func visibleDeveloperChatEntry(msg llm.Message) (ChatEntry, bool) {
	messageType, _ := textutil.OptionalValue(msg.MessageType)
	sourcePath, _ := textutil.OptionalTrimmed(msg.SourcePath)
	if msg.Content == nil {
		if isUnknownDeveloperMessageType(msg.MessageType) {
			return ChatEntry{
				Visibility:   transcript.EntryVisibilityDetail,
				Role:         string(transcript.EntryRoleDeveloperContext),
				Text:         "empty developer message",
				MessageType:  messageType,
				SourcePath:   sourcePath,
				CompactLabel: compactLabelForMessage(msg),
			}, true
		}
		return ChatEntry{}, false
	}
	switch messageType {
	case llm.MessageTypeAgentsMD,
		llm.MessageTypeSkills,
		llm.MessageTypeEnvironment,
		llm.MessageTypeHeadlessMode,
		llm.MessageTypeHeadlessModeExit,
		llm.MessageTypeActiveGoalContinuation,
		llm.MessageTypeWorkflowMode:
		return developerContextEntry(msg, messageTypeTranscriptVisibility(msg.MessageType)), true
	case llm.MessageTypeWorktreeMode, llm.MessageTypeWorktreeModeExit, llm.MessageTypeSessionRebind:
		return developerContextEntry(msg, messageTypeTranscriptVisibility(msg.MessageType)), true
	case llm.MessageTypeCompactionSummary:
		return compactionSummaryChatEntry(msg), true
	case llm.MessageTypeInterruption:
		return ChatEntry{Visibility: messageTypeTranscriptVisibility(msg.MessageType), Role: string(transcript.EntryRoleInterruption), Text: *msg.Content, MessageType: messageType, CompactLabel: compactLabelForMessage(msg)}, true
	case llm.MessageTypeGoal:
		condensed, _ := textutil.OptionalExact(msg.CompactContent)
		return ChatEntry{Visibility: messageTypeTranscriptVisibility(msg.MessageType), Role: string(transcript.EntryRoleGoalFeedback), Text: *msg.Content, CondensedText: condensed, MessageType: messageType, CompactLabel: compactLabelForMessage(msg)}, true
	case llm.MessageTypeErrorFeedback:
		return ChatEntry{Visibility: messageTypeTranscriptVisibility(msg.MessageType), Role: string(transcript.EntryRoleDeveloperFeedback), Text: *msg.Content, MessageType: messageType, CompactLabel: compactLabelForMessage(msg)}, true
	case llm.MessageTypeAgentSteer:
		return ChatEntry{Visibility: messageTypeTranscriptVisibility(msg.MessageType), Role: string(transcript.EntryRoleDeveloperFeedback), Text: *msg.Content, MessageType: messageType, CompactLabel: compactLabelForMessage(msg)}, true
	case llm.MessageTypeReviewerFeedback:
		return ChatEntry{}, false
	case llm.MessageTypeCompactionSoonReminder:
		return ChatEntry{Visibility: messageTypeTranscriptVisibility(msg.MessageType), Role: string(transcript.EntryRoleWarning), Text: *msg.Content, MessageType: messageType, CompactLabel: compactLabelForMessage(msg)}, true
	case llm.MessageTypeBackgroundNotice:
		condensed, _ := textutil.OptionalExact(msg.CompactContent)
		activityID, _ := textutil.OptionalTrimmed(msg.BackgroundActivityID)
		processID, _ := textutil.OptionalTrimmed(msg.Name)
		return ChatEntry{
			Visibility:           messageTypeTranscriptVisibility(msg.MessageType),
			Role:                 string(transcript.EntryRoleSystem),
			Text:                 *msg.Content,
			CondensedText:        condensed,
			MessageType:          messageType,
			CompactLabel:         compactLabelForMessage(msg),
			BackgroundActivityID: activityID,
			BackgroundProcessID:  processID,
			BackgroundExitCode:   textutil.Pointer(msg.BackgroundExitCode),
		}, true
	case llm.MessageTypeHandoffFutureMessage:
		return developerContextEntry(msg, messageTypeTranscriptVisibility(msg.MessageType)), true
	case llm.MessageTypeCompactionPreservedUserMessage:
		return ChatEntry{Visibility: messageTypeTranscriptVisibility(msg.MessageType), Role: string(transcript.EntryRoleCompactionPreservedUserMessage), Text: *msg.Content, MessageType: messageType, CompactLabel: compactLabelForMessage(msg)}, true
	default:
		return developerContextEntry(msg, messageTypeTranscriptVisibility(msg.MessageType)), true
	}
}

func isUnknownDeveloperMessageType(messageType *llm.MessageType) bool {
	if messageType == nil || strings.TrimSpace(string(*messageType)) == "" {
		return false
	}
	switch *messageType {
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
		llm.MessageTypeCompactionPreservedUserMessage,
		llm.MessageTypeHeadlessMode,
		llm.MessageTypeHeadlessModeExit,
		llm.MessageTypeWorkflowMode,
		llm.MessageTypeWorktreeMode,
		llm.MessageTypeWorktreeModeExit,
		llm.MessageTypeSessionRebind,
		llm.MessageTypeGoal,
		llm.MessageTypeActiveGoalContinuation,
		llm.MessageTypeAgentSteer:
		return false
	default:
		return true
	}
}

func compactionSummaryChatEntry(msg llm.Message) ChatEntry {
	label, _ := textutil.OptionalTrimmed(msg.CompactContent)
	text := ""
	if msg.Content != nil {
		text = *msg.Content
	}
	messageType, _ := textutil.OptionalValue(msg.MessageType)
	sourcePath, _ := textutil.OptionalTrimmed(msg.SourcePath)
	return ChatEntry{
		Visibility:    messageTypeTranscriptVisibility(msg.MessageType),
		Role:          string(transcript.EntryRoleCompactionSummary),
		Text:          text,
		CondensedText: label,
		MessageType:   messageType,
		SourcePath:    sourcePath,
		CompactLabel:  label,
	}
}

func messageTypeTranscriptVisibility(messageType *llm.MessageType) transcript.EntryVisibility {
	if messageType == nil {
		return transcript.EntryVisibilityOngoing
	}
	switch *messageType {
	case llm.MessageTypeAgentsMD,
		llm.MessageTypeSkills,
		llm.MessageTypeEnvironment,
		llm.MessageTypeSubagents,
		llm.MessageTypeCompactionSoonReminder,
		llm.MessageTypeHandoffFutureMessage,
		llm.MessageTypeCompactionPreservedUserMessage,
		llm.MessageTypeHeadlessMode,
		llm.MessageTypeHeadlessModeExit:
		return transcript.EntryVisibilityDetail
	case llm.MessageTypeActiveGoalContinuation:
		return transcript.EntryVisibilityDetail
	case llm.MessageTypeBackgroundNotice:
		return transcript.EntryVisibilityOngoingCollapsed
	case llm.MessageTypeWorkflowMode, llm.MessageTypeWorkflowModeExit:
		return transcript.EntryVisibilityOngoingCollapsed
	case llm.MessageTypeSessionRebind:
		return transcript.EntryVisibilityOngoingCollapsed
	case llm.MessageTypeCompactionSummary,
		llm.MessageTypeInterruption,
		llm.MessageTypeErrorFeedback,
		llm.MessageTypeWorktreeMode,
		llm.MessageTypeWorktreeModeExit,
		llm.MessageTypeGoal,
		llm.MessageTypeAgentSteer:
		return transcript.EntryVisibilityOngoing
	default:
		return transcript.EntryVisibilityOngoing
	}
}

func developerContextEntry(msg llm.Message, visibility transcript.EntryVisibility) ChatEntry {
	text := ""
	if msg.Content != nil {
		text = *msg.Content
	}
	condensedText, _ := textutil.OptionalTrimmed(msg.CompactContent)
	messageType, _ := textutil.OptionalValue(msg.MessageType)
	sourcePath, _ := textutil.OptionalTrimmed(msg.SourcePath)
	backgroundActivityID, _ := textutil.OptionalTrimmed(msg.BackgroundActivityID)
	backgroundProcessID, _ := textutil.OptionalTrimmed(msg.Name)
	return ChatEntry{
		Visibility:           visibility,
		Role:                 string(transcript.EntryRoleDeveloperContext),
		Text:                 text,
		CondensedText:        condensedText,
		MessageType:          messageType,
		SourcePath:           sourcePath,
		WorktreeContext:      session.CloneWorktreeContext(msg.WorktreeContext),
		CompactLabel:         compactLabelForMessage(msg),
		BackgroundActivityID: backgroundActivityID,
		BackgroundProcessID:  backgroundProcessID,
		BackgroundExitCode:   textutil.Pointer(msg.BackgroundExitCode),
	}
}

func compactLabelForMessage(msg llm.Message) string {
	if label, present := textutil.OptionalTrimmed(msg.CompactContent); present {
		return label
	}
	messageType, _ := textutil.OptionalValue(msg.MessageType)
	switch messageType {
	case llm.MessageTypeAgentsMD:
		if sourcePath, present := textutil.OptionalTrimmed(msg.SourcePath); present {
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
	case llm.MessageTypeWorkflowModeExit:
		return "Workflow mode cleared"
	case llm.MessageTypeWorktreeMode:
		return ""
	case llm.MessageTypeWorktreeModeExit:
		return ""
	case llm.MessageTypeSessionRebind:
		return clientui.SessionRebindCompactLabel
	case llm.MessageTypeCompactionSummary:
		return ""
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
	case llm.MessageTypeCompactionPreservedUserMessage:
		return "Last user message preserved for compaction"
	default:
		if msg.Role == llm.RoleDeveloper && strings.TrimSpace(string(messageType)) != "" {
			return "Developer context: " + strings.TrimSpace(string(messageType))
		}
		return ""
	}
}
