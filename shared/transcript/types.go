package transcript

import (
	"strings"

	"core/shared/toolspec"
	patchformat "core/shared/transcript/patchformat"
)

type ToolPresentationKind string

const (
	ToolPresentationDefault     ToolPresentationKind = "default"
	ToolPresentationShell       ToolPresentationKind = "shell"
	ToolPresentationAskQuestion ToolPresentationKind = "ask_question"
)

type ToolCallRenderBehavior string

const (
	ToolCallRenderBehaviorDefault     ToolCallRenderBehavior = "default"
	ToolCallRenderBehaviorShell       ToolCallRenderBehavior = "shell"
	ToolCallRenderBehaviorAskQuestion ToolCallRenderBehavior = "ask_question"
)

type ToolRenderKind string

type ToolShellDialect string

const (
	ToolRenderKindShell  ToolRenderKind = "shell"
	ToolRenderKindDiff   ToolRenderKind = "diff"
	ToolRenderKindSource ToolRenderKind = "source"
	ToolRenderKindPlain  ToolRenderKind = "plain"

	ToolShellDialectPosix          ToolShellDialect = "posix"
	ToolShellDialectPowerShell     ToolShellDialect = "powershell"
	ToolShellDialectWindowsCommand ToolShellDialect = "windows_command"
)

type ToolRenderHint struct {
	Kind         ToolRenderKind
	Path         string
	ResultOnly   bool
	ShellDialect ToolShellDialect
}

type ToolCallMeta struct {
	ToolName               string
	Presentation           ToolPresentationKind
	RenderBehavior         ToolCallRenderBehavior
	IsShell                bool
	UserInitiated          bool
	Command                string
	CompactText            string
	InlineMeta             string
	TimeoutLabel           string
	PatchSummary           string
	PatchDetail            string
	PatchRender            *patchformat.RenderedPatch
	RenderHint             *ToolRenderHint
	Question               string
	Suggestions            []string
	RecommendedOptionIndex int
	OmitSuccessfulResult   bool
	RawOutputRequested     bool
	OutputTruncated        bool
	MovedToBackground      bool
	ShellExitCode          *int
}

// ToolResultPresentationDelta contains only presentation facts learned from a
// tool result. Tool-call input metadata remains authoritative for every other
// presentation field.
type ToolResultPresentationDelta struct {
	RawOutputRequested     bool
	OutputTruncated        bool
	MovedToBackground      bool
	ShellExitCode          *int
	WholeFileDeletionFacts []patchformat.WholeFileDeletionFact
}

type ToolResultPresentationOutcome uint8

const (
	ToolResultPresentationOutcomeSuccessful ToolResultPresentationOutcome = iota + 1
	ToolResultPresentationOutcomeFailed
)

func ApplyToolResultPresentationDelta(
	meta ToolCallMeta,
	delta *ToolResultPresentationDelta,
	outcome ToolResultPresentationOutcome,
) (ToolCallMeta, *patchformat.WholeFileDeletionFactMismatchError) {
	switch outcome {
	case ToolResultPresentationOutcomeSuccessful, ToolResultPresentationOutcomeFailed:
	default:
		panic("tool result presentation outcome is invalid")
	}
	if delta != nil {
		meta.RawOutputRequested = meta.RawOutputRequested || delta.RawOutputRequested
		meta.OutputTruncated = meta.OutputTruncated || delta.OutputTruncated
		meta.MovedToBackground = meta.MovedToBackground || delta.MovedToBackground
		if delta.ShellExitCode != nil {
			exitCode := *delta.ShellExitCode
			meta.ShellExitCode = &exitCode
		}
	}
	if outcome == ToolResultPresentationOutcomeSuccessful && renderedPatchHasWholeFileDeletions(meta.PatchRender) {
		rendered := patchformat.RenderedPatch{}
		if meta.PatchRender != nil {
			rendered = *meta.PatchRender
		}
		var facts []patchformat.WholeFileDeletionFact
		if delta != nil {
			facts = delta.WholeFileDeletionFacts
		}
		finalized, err := patchformat.ApplyWholeFileDeletionFacts(
			rendered,
			facts,
		)
		if err != nil {
			return NormalizeToolCallMeta(meta), err
		}
		meta.PatchRender = &finalized
		meta.PatchSummary = strings.TrimSpace(finalized.SummaryText())
		meta.PatchDetail = strings.TrimSpace(finalized.DetailText())
		meta.CompactText = meta.PatchSummary
		meta.Command = meta.PatchDetail
	}
	return NormalizeToolCallMeta(meta), nil
}

func renderedPatchHasWholeFileDeletions(rendered *patchformat.RenderedPatch) bool {
	if rendered == nil {
		return false
	}
	for _, file := range rendered.Files {
		if len(file.WholeFileDeletions) > 0 {
			return true
		}
	}
	return false
}

func NormalizeToolCallMeta(in ToolCallMeta) ToolCallMeta {
	out := in
	toolID, knownTool := toolspec.ParseID(out.ToolName)
	knownShellTool := knownTool && (toolID == toolspec.ToolExecCommand || toolID == toolspec.ToolWriteStdin)
	if out.Presentation == "" {
		switch {
		case out.RenderBehavior == ToolCallRenderBehaviorShell || out.IsShell || knownShellTool:
			out.Presentation = ToolPresentationShell
		case out.RenderBehavior == ToolCallRenderBehaviorAskQuestion || strings.TrimSpace(out.Question) != "" || len(out.Suggestions) > 0 || out.RecommendedOptionIndex > 0:
			out.Presentation = ToolPresentationAskQuestion
		default:
			out.Presentation = ToolPresentationDefault
		}
	}
	if out.RenderBehavior == "" {
		switch {
		case out.Presentation == ToolPresentationShell || out.IsShell:
			out.RenderBehavior = ToolCallRenderBehaviorShell
		case out.Presentation == ToolPresentationAskQuestion || strings.TrimSpace(out.Question) != "" || len(out.Suggestions) > 0 || out.RecommendedOptionIndex > 0:
			out.RenderBehavior = ToolCallRenderBehaviorAskQuestion
		default:
			out.RenderBehavior = ToolCallRenderBehaviorDefault
		}
	}
	if out.Presentation == ToolPresentationShell {
		out.IsShell = true
	}
	if out.RenderBehavior == ToolCallRenderBehaviorShell {
		out.IsShell = true
	}
	if out.RenderHint == nil && knownTool && toolID == toolspec.ToolWriteStdin && out.IsShell {
		out.RenderHint = &ToolRenderHint{Kind: ToolRenderKindPlain}
	}
	if strings.TrimSpace(out.InlineMeta) == "" {
		out.InlineMeta = strings.TrimSpace(out.TimeoutLabel)
	}
	if strings.TrimSpace(out.TimeoutLabel) == "" {
		out.TimeoutLabel = strings.TrimSpace(out.InlineMeta)
	}
	if out.PatchRender != nil {
		if strings.TrimSpace(out.PatchSummary) == "" {
			out.PatchSummary = strings.TrimSpace(out.PatchRender.SummaryText())
		}
		if strings.TrimSpace(out.PatchDetail) == "" {
			out.PatchDetail = strings.TrimSpace(out.PatchRender.DetailText())
		}
	}
	if strings.TrimSpace(out.Command) == "" {
		out.Command = strings.TrimSpace(out.PatchDetail)
	}
	if strings.TrimSpace(out.CompactText) == "" {
		if strings.TrimSpace(out.PatchSummary) != "" {
			out.CompactText = strings.TrimSpace(out.PatchSummary)
		} else {
			out.CompactText = strings.TrimSpace(out.Command)
		}
	}
	if out.HasPatchDetail() {
		out.OmitSuccessfulResult = true
	}
	return out
}

func (m *ToolCallMeta) UsesShellRendering() bool {
	if m == nil {
		return false
	}
	behavior := m.RenderBehavior
	if behavior == "" {
		behavior = NormalizeToolCallMeta(*m).RenderBehavior
	}
	return behavior == ToolCallRenderBehaviorShell
}

func (m *ToolCallMeta) UsesAskQuestionRendering() bool {
	if m == nil {
		return false
	}
	behavior := m.RenderBehavior
	if behavior == "" {
		behavior = NormalizeToolCallMeta(*m).RenderBehavior
	}
	return behavior == ToolCallRenderBehaviorAskQuestion
}

func (m *ToolCallMeta) HasRenderHint() bool {
	return m != nil && m.RenderHint != nil && m.RenderHint.Valid()
}

func (m *ToolCallMeta) Valid() bool {
	if m == nil || strings.TrimSpace(m.ToolName) == "" {
		return false
	}
	switch m.Presentation {
	case ToolPresentationDefault, ToolPresentationShell, ToolPresentationAskQuestion:
	default:
		return false
	}
	switch m.RenderBehavior {
	case "", ToolCallRenderBehaviorDefault, ToolCallRenderBehaviorShell, ToolCallRenderBehaviorAskQuestion:
	default:
		return false
	}
	return m.RenderHint == nil || m.RenderHint.Valid()
}

func (m *ToolCallMeta) HasCompactText() bool {
	return m != nil && strings.TrimSpace(m.CompactText) != ""
}

func (m *ToolCallMeta) HasPatchDetail() bool {
	return m != nil && strings.TrimSpace(m.PatchDetail) != ""
}

func (m *ToolCallMeta) HasPatchSummary() bool {
	return m != nil && strings.TrimSpace(m.PatchSummary) != ""
}

func (h *ToolRenderHint) Valid() bool {
	if h == nil {
		return false
	}
	switch h.Kind {
	case ToolRenderKindShell:
		return true
	case ToolRenderKindDiff:
		return true
	case ToolRenderKindSource:
		return strings.TrimSpace(h.Path) != ""
	case ToolRenderKindPlain:
		return true
	default:
		return false
	}
}
