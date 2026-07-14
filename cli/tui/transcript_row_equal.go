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
		left.CondensedText == right.CondensedText &&
		pointersEqual(left.RollbackTargetID, right.RollbackTargetID)
}

func transcriptAssistantRowEqual(left, right *clientui.TranscriptAssistantRow) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Text == right.Text &&
		left.CondensedText == right.CondensedText &&
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
		left.ResultSummary == right.ResultSummary &&
		left.CondensedText == right.CondensedText &&
		transcript.ToolCallMetaEqual(
			transcriptToolCallMeta(left.ToolPresentation),
			transcriptToolCallMeta(right.ToolPresentation),
		)
}

func transcriptNoticeRowEqual(left, right *clientui.TranscriptNoticeRow) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Reason == right.Reason &&
		left.Severity == right.Severity &&
		pointersEqual(left.Data.LegacyText, right.Data.LegacyText) &&
		pointersEqual(left.Data.NoticeID, right.Data.NoticeID) &&
		pointersEqual(left.Data.CacheWarning, right.Data.CacheWarning) &&
		pointersEqual(left.Data.RuntimeDiagnostic, right.Data.RuntimeDiagnostic) &&
		left.Data.MessageType == right.Data.MessageType &&
		left.Data.SourcePath == right.Data.SourcePath &&
		transcriptWorktreeContextEqual(left.Data.WorktreeContext, right.Data.WorktreeContext) &&
		left.Data.CondensedText == right.Data.CondensedText &&
		left.Data.CompactLabel == right.Data.CompactLabel &&
		pointersEqual(left.Data.BackgroundExitCode, right.Data.BackgroundExitCode) &&
		pointersEqual(left.Diagnostic, right.Diagnostic)
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

func transcriptToolCallMeta(meta *clientui.ToolCallMeta) *transcript.ToolCallMeta {
	if meta == nil {
		return nil
	}
	out := transcript.ToolCallMeta{
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
		PatchRender:            meta.PatchRender,
		Question:               meta.Question,
		Suggestions:            append([]string(nil), meta.Suggestions...),
		RecommendedOptionIndex: meta.RecommendedOptionIndex,
		OmitSuccessfulResult:   meta.OmitSuccessfulResult,
		RawOutputRequested:     meta.RawOutputRequested,
		OutputTruncated:        meta.OutputTruncated,
		MovedToBackground:      meta.MovedToBackground,
		ShellExitCode:          meta.ShellExitCode,
	}
	if meta.RenderHint != nil {
		out.RenderHint = &transcript.ToolRenderHint{
			Kind:         transcript.ToolRenderKind(meta.RenderHint.Kind),
			Path:         meta.RenderHint.Path,
			ResultOnly:   meta.RenderHint.ResultOnly,
			ShellDialect: transcript.ToolShellDialect(meta.RenderHint.ShellDialect),
		}
	}
	return &out
}
