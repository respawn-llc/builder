package transcript

import (
	"slices"
	"strings"

	patchformat "core/shared/transcript/patchformat"
)

// EntryPayload is the transcript-domain shape used for overlap and replacement
// decisions. It intentionally includes render-affecting tool metadata so stale
// UI projections cannot survive when only metadata changed.
type EntryPayload struct {
	Visibility        EntryVisibility
	RollbackTargetID  *string
	Role              string
	Text              string
	CondensedText     string
	Phase             string
	MessageType       string
	SourcePath        string
	CompactLabel      string
	ToolResultSummary string
	ToolCallID        string
	NoticeID          string
	ToolCall          *ToolCallMeta
}

// EntryPayloadEqual defines canonical transcript-entry equality for client
// overlap, page replacement, and stale-page checks.
func EntryPayloadEqual(left, right EntryPayload) bool {
	return NormalizeEntryVisibility(left.Visibility) == NormalizeEntryVisibility(right.Visibility) &&
		optionalTrimmedStringEqual(left.RollbackTargetID, right.RollbackTargetID) &&
		strings.ToLower(strings.TrimSpace(left.Role)) == strings.ToLower(strings.TrimSpace(right.Role)) &&
		left.Text == right.Text &&
		left.CondensedText == right.CondensedText &&
		strings.TrimSpace(left.Phase) == strings.TrimSpace(right.Phase) &&
		strings.TrimSpace(left.MessageType) == strings.TrimSpace(right.MessageType) &&
		strings.TrimSpace(left.SourcePath) == strings.TrimSpace(right.SourcePath) &&
		strings.TrimSpace(left.CompactLabel) == strings.TrimSpace(right.CompactLabel) &&
		strings.TrimSpace(left.ToolResultSummary) == strings.TrimSpace(right.ToolResultSummary) &&
		strings.TrimSpace(left.ToolCallID) == strings.TrimSpace(right.ToolCallID) &&
		strings.TrimSpace(left.NoticeID) == strings.TrimSpace(right.NoticeID) &&
		ToolCallMetaEqual(left.ToolCall, right.ToolCall)
}

func optionalTrimmedStringEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return strings.TrimSpace(*left) == strings.TrimSpace(*right)
}

func ToolCallMetaEqual(left, right *ToolCallMeta) bool {
	if left == nil || right == nil {
		if left == nil && right == nil {
			return true
		}
		if left == nil {
			normalizedRight := NormalizeToolCallMeta(*right)
			return toolCallMetaEmpty(normalizedRight)
		}
		normalizedLeft := NormalizeToolCallMeta(*left)
		return toolCallMetaEmpty(normalizedLeft)
	}
	normalizedLeft := NormalizeToolCallMeta(*left)
	normalizedRight := NormalizeToolCallMeta(*right)
	return normalizedLeft.ToolName == normalizedRight.ToolName &&
		normalizedLeft.Presentation == normalizedRight.Presentation &&
		normalizedLeft.RenderBehavior == normalizedRight.RenderBehavior &&
		normalizedLeft.IsShell == normalizedRight.IsShell &&
		normalizedLeft.UserInitiated == normalizedRight.UserInitiated &&
		normalizedLeft.Command == normalizedRight.Command &&
		normalizedLeft.CompactText == normalizedRight.CompactText &&
		normalizedLeft.InlineMeta == normalizedRight.InlineMeta &&
		normalizedLeft.TimeoutLabel == normalizedRight.TimeoutLabel &&
		normalizedLeft.PatchSummary == normalizedRight.PatchSummary &&
		normalizedLeft.PatchDetail == normalizedRight.PatchDetail &&
		renderedPatchesEqual(normalizedLeft.PatchRender, normalizedRight.PatchRender) &&
		toolRenderHintsEqual(normalizedLeft.RenderHint, normalizedRight.RenderHint) &&
		normalizedLeft.Question == normalizedRight.Question &&
		slices.Equal(normalizedLeft.Suggestions, normalizedRight.Suggestions) &&
		normalizedLeft.RecommendedOptionIndex == normalizedRight.RecommendedOptionIndex &&
		normalizedLeft.OmitSuccessfulResult == normalizedRight.OmitSuccessfulResult &&
		normalizedLeft.RawOutputRequested == normalizedRight.RawOutputRequested &&
		normalizedLeft.OutputTruncated == normalizedRight.OutputTruncated &&
		normalizedLeft.MovedToBackground == normalizedRight.MovedToBackground &&
		optionalIntEqual(normalizedLeft.ShellExitCode, normalizedRight.ShellExitCode)
}

func toolCallMetaEmpty(meta ToolCallMeta) bool {
	return meta.ToolName == "" &&
		meta.Presentation == ToolPresentationDefault &&
		meta.RenderBehavior == ToolCallRenderBehaviorDefault &&
		!meta.IsShell &&
		!meta.UserInitiated &&
		meta.Command == "" &&
		meta.CompactText == "" &&
		meta.InlineMeta == "" &&
		meta.TimeoutLabel == "" &&
		meta.PatchSummary == "" &&
		meta.PatchDetail == "" &&
		meta.PatchRender == nil &&
		meta.RenderHint == nil &&
		meta.Question == "" &&
		len(meta.Suggestions) == 0 &&
		meta.RecommendedOptionIndex == 0 &&
		!meta.OmitSuccessfulResult &&
		!meta.RawOutputRequested &&
		!meta.OutputTruncated &&
		!meta.MovedToBackground &&
		meta.ShellExitCode == nil
}

func optionalIntEqual(left, right *int) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func toolRenderHintsEqual(left, right *ToolRenderHint) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Kind == right.Kind &&
		left.Path == right.Path &&
		left.ResultOnly == right.ResultOnly &&
		left.ShellDialect == right.ShellDialect
}

func renderedPatchesEqual(left, right *patchformat.RenderedPatch) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return slices.EqualFunc(left.Files, right.Files, func(a, b patchformat.RenderedFile) bool {
		return a.AbsPath == b.AbsPath &&
			a.RelPath == b.RelPath &&
			a.Added == b.Added &&
			a.Removed == b.Removed &&
			slices.Equal(a.Diff, b.Diff)
	}) &&
		slices.EqualFunc(left.SummaryLines, right.SummaryLines, func(a, b patchformat.RenderedLine) bool {
			return a.Kind == b.Kind &&
				a.Text == b.Text &&
				a.FileIndex == b.FileIndex &&
				a.Path == b.Path
		}) &&
		slices.EqualFunc(left.DetailLines, right.DetailLines, func(a, b patchformat.RenderedLine) bool {
			return a.Kind == b.Kind &&
				a.Text == b.Text &&
				a.FileIndex == b.FileIndex &&
				a.Path == b.Path
		})
}
