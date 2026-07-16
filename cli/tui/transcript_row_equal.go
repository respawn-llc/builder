package tui

import (
	"core/shared/clientui"
	"core/shared/transcript"
)

func TranscriptCommittedRowEqual(left, right clientui.TranscriptCommittedRow) bool {
	if left.Visibility != right.Visibility ||
		left.Integrity != right.Integrity ||
		left.Kind != right.Kind {
		return false
	}
	return transcriptUserRowEqual(left.User, right.User) &&
		transcriptAssistantRowEqual(left.Assistant, right.Assistant) &&
		transcriptToolRowEqual(left.Tool, right.Tool) &&
		transcriptNoticeRowEqual(left.Notice, right.Notice)
}

func transcriptUserRowEqual(left, right *clientui.TranscriptUserRow) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Text == right.Text &&
		pointersEqual(left.CondensedText, right.CondensedText) &&
		pointersEqual(left.RollbackTargetID, right.RollbackTargetID)
}

func transcriptAssistantRowEqual(left, right *clientui.TranscriptAssistantRow) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Text == right.Text &&
		pointersEqual(left.CondensedText, right.CondensedText) &&
		left.Phase == right.Phase &&
		pointersEqual(left.StreamID, right.StreamID)
}

func transcriptToolRowEqual(left, right *clientui.TranscriptToolRow) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.ToolCallID == right.ToolCallID &&
		left.ToolName == right.ToolName &&
		left.Text == right.Text &&
		left.IsError == right.IsError &&
		pointersEqual(left.ResultSummary, right.ResultSummary) &&
		pointersEqual(left.CondensedText, right.CondensedText) &&
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
		pointersEqual(left.StepID, right.StepID) &&
		pointersEqual(left.MessageType, right.MessageType) &&
		pointersEqual(left.LegacyText, right.LegacyText) &&
		pointersEqual(left.NoticeID, right.NoticeID) &&
		pointersEqual(left.SourcePath, right.SourcePath) &&
		transcriptWorktreeContextEqual(left.Worktree, right.Worktree) &&
		pointersEqual(left.CacheWarning, right.CacheWarning) &&
		transcriptDiagnosticEqual(left.Diagnostic, right.Diagnostic) &&
		pointersEqual(left.Background, right.Background) &&
		pointersEqual(left.CondensedText, right.CondensedText) &&
		pointersEqual(left.CompactLabel, right.CompactLabel)
}

func transcriptDiagnosticEqual(left, right *clientui.TranscriptDiagnostic) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Code == right.Code &&
		left.Detail == right.Detail &&
		transcript.DeveloperDiagnosticEqual(left.Developer, right.Developer)
}

func transcriptWorktreeContextEqual(left, right *clientui.TranscriptWorktreeContext) bool {
	if left == nil || right == nil {
		return left == right
	}
	return pointersEqual(left.Branch, right.Branch) &&
		left.WorktreePath == right.WorktreePath &&
		left.WorkspaceRoot == right.WorkspaceRoot &&
		left.EffectiveCwd == right.EffectiveCwd
}

func pointersEqual[T comparable](left, right *T) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}
