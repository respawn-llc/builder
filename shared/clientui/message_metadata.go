package clientui

import "core/shared/transcript"

type MessagePhase string

const (
	MessagePhaseCommentary MessagePhase = MessagePhase(transcript.AssistantPhaseCommentary)
	MessagePhaseFinal      MessagePhase = MessagePhase(transcript.AssistantPhaseFinal)
)

type MessageType string

const (
	MessageTypeAgentsMD               MessageType = "agents.md"
	MessageTypeSkills                 MessageType = "skills"
	MessageTypeSubagents              MessageType = "subagents"
	MessageTypeEnvironment            MessageType = "environment"
	MessageTypeCompactionSummary      MessageType = "compaction_summary"
	MessageTypeInterruption           MessageType = "interruption"
	MessageTypeErrorFeedback          MessageType = "error_feedback"
	MessageTypeCompactionSoonReminder MessageType = "compaction_soon_reminder"
	MessageTypeHandoffFutureMessage   MessageType = "handoff_future_message"
	MessageTypeReviewerFeedback       MessageType = "reviewer_feedback"
	MessageTypeBackgroundNotice       MessageType = "background_notice"
	MessageTypeCustomToolCallOutput   MessageType = "custom_tool_call_output"
	// MessageTypeCompactionPreservedUserMessage retains its legacy serialized
	// value so existing Session logs remain readable without migration.
	MessageTypeCompactionPreservedUserMessage MessageType = "manual_compaction_carryover"
	MessageTypeHeadlessMode                   MessageType = "headless_mode"
	MessageTypeHeadlessModeExit               MessageType = "headless_mode_exit"
	MessageTypeWorkflowMode                   MessageType = "workflow_mode"
	MessageTypeWorkflowModeExit               MessageType = "workflow_mode_exit"
	MessageTypeWorktreeMode                   MessageType = "worktree_mode"
	MessageTypeWorktreeModeExit               MessageType = "worktree_mode_exit"
	MessageTypeGoal                           MessageType = "goal"
	MessageTypeActiveGoalContinuation         MessageType = "active_goal_continuation"
	MessageTypeAgentSteer                     MessageType = MessageType(transcript.MessageTypeAgentSteer)
)

const GoalNudgeCompactLabel = "Goal nudge"
