package tui

import (
	"core/shared/clientui"
	"core/shared/textutil"
	"core/shared/transcript"
	"slices"
)

func TranscriptCommittedRowEqual(left, right clientui.TranscriptCommittedRow) bool {
	if left.Visibility != right.Visibility ||
		left.Integrity != right.Integrity ||
		left.Kind != right.Kind ||
		left.Locator != right.Locator {
		return false
	}
	return transcriptUserRowEqual(left.User, right.User) &&
		transcriptAssistantRowEqual(left.Assistant, right.Assistant) &&
		transcriptToolRowEqual(left.Tool, right.Tool) &&
		transcriptNoticeRowEqual(left.Notice, right.Notice) &&
		transcriptReviewerFeedbackRowEqual(left.ReviewerFeedback, right.ReviewerFeedback) &&
		transcriptReviewerErrorRowEqual(left.ReviewerError, right.ReviewerError)
}

func transcriptReviewerFeedbackRowEqual(left, right *clientui.TranscriptReviewerFeedbackRow) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.ID == right.ID && left.StepID == right.StepID &&
		left.SuggestionCount == right.SuggestionCount &&
		slices.Equal(left.Suggestions, right.Suggestions)
}

func transcriptReviewerErrorRowEqual(left, right *clientui.TranscriptReviewerErrorRow) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.ID == right.ID && left.StepID == right.StepID && left.Detail == right.Detail
}

func transcriptUserRowEqual(left, right *clientui.TranscriptUserRow) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Text == right.Text &&
		textutil.EqualOptional(left.CondensedText, right.CondensedText) &&
		textutil.EqualOptional(left.RollbackTargetID, right.RollbackTargetID)
}

func transcriptAssistantRowEqual(left, right *clientui.TranscriptAssistantRow) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Text == right.Text &&
		textutil.EqualOptional(left.CondensedText, right.CondensedText) &&
		left.Phase == right.Phase &&
		textutil.EqualOptional(left.StreamID, right.StreamID)
}

func transcriptToolRowEqual(left, right *clientui.TranscriptToolRow) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.ToolCallID == right.ToolCallID &&
		left.ToolName == right.ToolName &&
		left.Text == right.Text &&
		left.IsError == right.IsError &&
		textutil.EqualOptional(left.ResultSummary, right.ResultSummary) &&
		textutil.EqualOptional(left.CondensedText, right.CondensedText) &&
		transcript.ToolCallMetaEqual(
			left.Presentation,
			right.Presentation,
		)
}

func transcriptNoticeRowEqual(left, right *clientui.TranscriptNoticeRow) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Reason == right.Reason &&
		left.Severity == right.Severity &&
		textutil.EqualOptional(left.StepID, right.StepID) &&
		textutil.EqualOptional(left.MessageType, right.MessageType) &&
		textutil.EqualOptional(left.LegacyText, right.LegacyText) &&
		textutil.EqualOptional(left.NoticeID, right.NoticeID) &&
		textutil.EqualOptional(left.SourcePath, right.SourcePath) &&
		transcriptWorktreeContextEqual(left.Worktree, right.Worktree) &&
		textutil.EqualOptional(left.CacheWarning, right.CacheWarning) &&
		textutil.EqualOptional(left.Diagnostic, right.Diagnostic) &&
		textutil.EqualOptional(left.Background, right.Background) &&
		textutil.EqualOptional(left.CondensedText, right.CondensedText) &&
		textutil.EqualOptional(left.CompactLabel, right.CompactLabel)
}

func transcriptWorktreeContextEqual(left, right *clientui.TranscriptWorktreeContext) bool {
	if left == nil || right == nil {
		return left == right
	}
	return textutil.EqualOptional(left.Branch, right.Branch) &&
		left.WorktreePath == right.WorktreePath &&
		left.WorkspaceRoot == right.WorkspaceRoot &&
		left.EffectiveCwd == right.EffectiveCwd
}
