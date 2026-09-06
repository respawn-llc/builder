package transcript

import (
	"strings"

	"core/shared/toolspec"
	patchformat "core/shared/transcript/patchformat"
)

const MessageTypeAgentSteer = "agent_steer"

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
	PatchPresentation      *patchformat.Presentation
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
	ToolResultPresentationOutcomeSuccessful ToolResultPresentationOutcome = iota
	ToolResultPresentationOutcomeFailed
)

func ApplyToolResultPresentationDelta(
	meta ToolCallMeta,
	delta *ToolResultPresentationDelta,
	outcome ToolResultPresentationOutcome,
) (ToolCallMeta, *patchformat.WholeFileDeletionFactMismatch) {
	if delta != nil {
		meta.RawOutputRequested = meta.RawOutputRequested || delta.RawOutputRequested
		meta.OutputTruncated = meta.OutputTruncated || delta.OutputTruncated
		meta.MovedToBackground = meta.MovedToBackground || delta.MovedToBackground
		if delta.ShellExitCode != nil {
			exitCode := *delta.ShellExitCode
			meta.ShellExitCode = &exitCode
		}
	}
	if outcome == ToolResultPresentationOutcomeFailed {
		return NormalizeToolCallMeta(meta), nil
	}

	facts := []patchformat.WholeFileDeletionFact(nil)
	if delta != nil {
		facts = delta.WholeFileDeletionFacts
	}
	if meta.PatchPresentation != nil &&
		meta.PatchPresentation.Variant == patchformat.PresentationVariantChanges &&
		meta.PatchPresentation.Changes != nil {
		finalized, mismatch := patchformat.ApplyWholeFileDeletionFacts(
			*meta.PatchPresentation,
			facts,
		)
		if mismatch != nil {
			return NormalizeToolCallMeta(meta), mismatch
		}
		meta.PatchPresentation = &finalized
	}
	return NormalizeToolCallMeta(meta), nil
}

func NormalizeToolCallMeta(in ToolCallMeta) ToolCallMeta {
	out := in
	toolID, knownTool := toolspec.ParseID(out.ToolName)
	knownShellTool := knownTool && toolspec.IsShellTool(toolID)
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
	if strings.TrimSpace(out.CompactText) == "" {
		out.CompactText = strings.TrimSpace(out.Command)
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
	patchFamily := IsPatchFamilyToolName(m.ToolName)
	if patchFamily != (m.PatchPresentation != nil) {
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
	if patchFamily && !m.PatchPresentation.Valid() {
		return false
	}
	return m.RenderHint == nil || m.RenderHint.Valid()
}

func (m *ToolCallMeta) HasCompactText() bool {
	return m != nil && strings.TrimSpace(m.CompactText) != ""
}

func (h *ToolRenderHint) Valid() bool {
	if h == nil {
		return false
	}
	switch h.ShellDialect {
	case "", ToolShellDialectPosix, ToolShellDialectPowerShell, ToolShellDialectWindowsCommand:
	default:
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
