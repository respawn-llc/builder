package transcript

import (
	"slices"
	"strings"

	"core/shared/textutil"
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
		patchPresentationsEqual(normalizedLeft.PatchPresentation, normalizedRight.PatchPresentation) &&
		toolRenderHintsEqual(normalizedLeft.RenderHint, normalizedRight.RenderHint) &&
		normalizedLeft.Question == normalizedRight.Question &&
		slices.Equal(normalizedLeft.Suggestions, normalizedRight.Suggestions) &&
		normalizedLeft.RecommendedOptionIndex == normalizedRight.RecommendedOptionIndex &&
		normalizedLeft.OmitSuccessfulResult == normalizedRight.OmitSuccessfulResult &&
		normalizedLeft.RawOutputRequested == normalizedRight.RawOutputRequested &&
		normalizedLeft.OutputTruncated == normalizedRight.OutputTruncated &&
		normalizedLeft.MovedToBackground == normalizedRight.MovedToBackground &&
		textutil.EqualOptional(normalizedLeft.ShellExitCode, normalizedRight.ShellExitCode)
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
		meta.PatchPresentation == nil &&
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

func patchPresentationsEqual(left, right *patchformat.Presentation) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if left.Variant != right.Variant {
		return false
	}
	if left.InvalidInput == nil || right.InvalidInput == nil {
		if left.InvalidInput != nil || right.InvalidInput != nil {
			return false
		}
	} else if left.InvalidInput.InputDetail != right.InvalidInput.InputDetail {
		return false
	}
	if left.Changes == nil || right.Changes == nil {
		return left.Changes == nil && right.Changes == nil
	}
	return slices.EqualFunc(left.Changes.Files, right.Changes.Files, fileChangesEqual)
}

func fileChangesEqual(left, right patchformat.FileChange) bool {
	if left.Path != right.Path ||
		left.Added != right.Added ||
		!textutil.EqualOptional(left.Removed, right.Removed) {
		return false
	}
	return slices.EqualFunc(left.Operations, right.Operations, fileOperationsEqual)
}

func fileOperationsEqual(left, right patchformat.FileOperation) bool {
	if left.Kind != right.Kind {
		return false
	}
	if left.Source == nil || right.Source == nil {
		if left.Source != nil || right.Source != nil {
			return false
		}
	} else if *left.Source != *right.Source {
		return false
	}
	if !wholeFileDeletionOperationPointersEqual(left.Deletion, right.Deletion) {
		return false
	}
	return slices.EqualFunc(left.Groups, right.Groups, func(left, right patchformat.ChangeGroup) bool {
		return slices.Equal(left.Lines, right.Lines)
	})
}

func wholeFileDeletionOperationPointersEqual(
	left *patchformat.WholeFileDeletionOperation,
	right *patchformat.WholeFileDeletionOperation,
) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return wholeFileDeletionOperationsEqual(*left, *right)
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

func wholeFileDeletionOperationsEqual(
	left patchformat.WholeFileDeletionOperation,
	right patchformat.WholeFileDeletionOperation,
) bool {
	if left.ID != right.ID {
		return false
	}
	if left.Disposition == nil || right.Disposition == nil {
		return left.Disposition == nil && right.Disposition == nil
	}
	return left.Disposition.PhysicalGroup == right.Disposition.PhysicalGroup &&
		left.Disposition.Removed == right.Disposition.Removed
}
