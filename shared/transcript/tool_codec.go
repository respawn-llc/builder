package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/shared/toolspec"
	patchformat "core/shared/transcript/patchformat"
)

type ToolCallMetaDecodeKind uint8

const (
	ToolCallMetaDecodeAbsent ToolCallMetaDecodeKind = iota
	ToolCallMetaDecodeCurrent
	ToolCallMetaDecodeLegacyNormalized
	ToolCallMetaDecodeInvalid
)

type ToolCallMetaDecodeResult struct {
	Kind  ToolCallMetaDecodeKind
	Meta  *ToolCallMeta
	Cause error
}

type toolCallMetaWireCommon struct {
	ToolName               string
	Presentation           ToolPresentationKind
	RenderBehavior         ToolCallRenderBehavior
	IsShell                bool
	UserInitiated          bool
	Command                string
	CompactText            string
	InlineMeta             string
	TimeoutLabel           string
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

type currentToolCallMetaWire struct {
	toolCallMetaWireCommon
	PatchPresentation *patchformat.Presentation
}

type legacyToolCallMetaWire struct {
	toolCallMetaWireCommon
	PatchSummary string
	PatchDetail  string
	PatchRender  *patchformat.RenderedPatch
}

func EncodeToolCallMeta(meta ToolCallMeta) json.RawMessage {
	raw, err := TryEncodeToolCallMeta(meta)
	if err != nil {
		panic(err)
	}
	return raw
}

func TryEncodeToolCallMeta(meta ToolCallMeta) (json.RawMessage, error) {
	normalized := NormalizeToolCallMeta(meta)
	if !normalized.Valid() {
		return nil, errors.New("tool call metadata is structurally invalid")
	}
	raw, err := json.Marshal(currentToolCallMetaWireFromMeta(normalized))
	if err != nil {
		return nil, fmt.Errorf("encode tool call metadata: %w", err)
	}
	return raw, nil
}

func DecodeToolCallMeta(raw json.RawMessage) ToolCallMetaDecodeResult {
	if len(raw) == 0 {
		return ToolCallMetaDecodeResult{Kind: ToolCallMetaDecodeAbsent}
	}
	if !json.Valid(raw) {
		return invalidToolCallMetaDecode(errors.New("tool call metadata is invalid JSON"))
	}
	current, currentErr := decodeCurrentToolCallMeta(raw)
	if currentErr == nil {
		return ToolCallMetaDecodeResult{
			Kind: ToolCallMetaDecodeCurrent,
			Meta: &current,
		}
	}
	legacy, legacyErr := decodeLegacyToolCallMeta(raw)
	if legacyErr == nil {
		return ToolCallMetaDecodeResult{
			Kind: ToolCallMetaDecodeLegacyNormalized,
			Meta: &legacy,
		}
	}
	return invalidToolCallMetaDecode(errors.Join(currentErr, legacyErr))
}

var currentToolCallMetaRequiredFields = [...]string{
	"ToolName",
	"Presentation",
	"RenderBehavior",
	"IsShell",
	"UserInitiated",
	"Command",
	"CompactText",
	"InlineMeta",
	"TimeoutLabel",
	"PatchPresentation",
	"RenderHint",
	"Question",
	"Suggestions",
	"RecommendedOptionIndex",
	"OmitSuccessfulResult",
	"RawOutputRequested",
	"OutputTruncated",
	"MovedToBackground",
	"ShellExitCode",
}

func decodeCurrentToolCallMeta(raw json.RawMessage) (ToolCallMeta, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ToolCallMeta{}, fmt.Errorf("inspect current tool call metadata: %w", err)
	}
	for _, name := range currentToolCallMetaRequiredFields {
		if _, present := fields[name]; !present {
			return ToolCallMeta{}, fmt.Errorf("current tool call metadata is missing %s", name)
		}
	}
	var wire currentToolCallMetaWire
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return ToolCallMeta{}, fmt.Errorf("decode current tool call metadata: %w", err)
	}
	normalized := NormalizeToolCallMeta(wire.meta())
	if !normalized.Valid() {
		return ToolCallMeta{}, errors.New("current tool call metadata is structurally invalid")
	}
	return normalized, nil
}

func decodeLegacyToolCallMeta(raw json.RawMessage) (ToolCallMeta, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ToolCallMeta{}, fmt.Errorf("inspect legacy tool call metadata: %w", err)
	}
	if _, mixed := fields["PatchPresentation"]; mixed {
		return ToolCallMeta{}, errors.New("legacy tool call metadata contains current patch presentation")
	}
	for _, name := range []string{"PatchSummary", "PatchDetail", "PatchRender"} {
		if _, present := fields[name]; !present {
			return ToolCallMeta{}, fmt.Errorf("legacy tool call metadata is missing %s", name)
		}
	}

	var wire legacyToolCallMetaWire
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return ToolCallMeta{}, fmt.Errorf("decode legacy tool call metadata: %w", err)
	}
	meta := wire.toolCallMetaWireCommon.meta()
	presentation, err := normalizeLegacyPatchPresentation(wire)
	if err != nil {
		return ToolCallMeta{}, err
	}
	meta.PatchPresentation = presentation
	if IsPatchFamilyToolName(meta.ToolName) {
		meta.Command = ""
		meta.CompactText = ""
	}
	normalized := NormalizeToolCallMeta(meta)
	if !normalized.Valid() {
		return ToolCallMeta{}, errors.New("normalized legacy tool call metadata is structurally invalid")
	}
	return normalized, nil
}

func (wire toolCallMetaWireCommon) meta() ToolCallMeta {
	return ToolCallMeta{
		ToolName:               wire.ToolName,
		Presentation:           wire.Presentation,
		RenderBehavior:         wire.RenderBehavior,
		IsShell:                wire.IsShell,
		UserInitiated:          wire.UserInitiated,
		Command:                wire.Command,
		CompactText:            wire.CompactText,
		InlineMeta:             wire.InlineMeta,
		TimeoutLabel:           wire.TimeoutLabel,
		RenderHint:             wire.RenderHint,
		Question:               wire.Question,
		Suggestions:            wire.Suggestions,
		RecommendedOptionIndex: wire.RecommendedOptionIndex,
		OmitSuccessfulResult:   wire.OmitSuccessfulResult,
		RawOutputRequested:     wire.RawOutputRequested,
		OutputTruncated:        wire.OutputTruncated,
		MovedToBackground:      wire.MovedToBackground,
		ShellExitCode:          wire.ShellExitCode,
	}
}

func normalizeLegacyPatchPresentation(wire legacyToolCallMetaWire) (*patchformat.Presentation, error) {
	if !IsPatchFamilyToolName(wire.ToolName) {
		if wire.PatchSummary != "" || wire.PatchDetail != "" || wire.PatchRender != nil {
			return nil, errors.New("non-Patch legacy metadata contains patch presentation")
		}
		return nil, nil
	}
	if wire.PatchRender == nil {
		inputDetail, err := legacyNoRenderInputDetail(wire)
		if err != nil {
			return nil, err
		}
		presentation := patchformat.InvalidInputPresentation(inputDetail)
		return &presentation, nil
	}
	if len(wire.PatchRender.Files) == 0 {
		if !isPatchToolName(wire.ToolName) {
			return nil, errors.New("legacy raw presentation is not a Patch tool")
		}
		inputDetail, err := legacyRawPatchInputDetail(wire)
		if err != nil {
			return nil, err
		}
		presentation := patchformat.InvalidInputPresentation(inputDetail)
		return &presentation, nil
	}
	if wire.PatchSummary != wire.PatchRender.SummaryText() ||
		wire.PatchDetail != wire.PatchRender.DetailText() ||
		wire.CompactText != wire.PatchSummary ||
		wire.Command != wire.PatchDetail {
		return nil, errors.New("legacy structured Patch metadata text is inconsistent")
	}

	changes := patchformat.Changes{
		Files: make([]patchformat.FileChange, 0, len(wire.PatchRender.Files)),
	}
	for fileIndex, rendered := range wire.PatchRender.Files {
		file, err := normalizeLegacyRenderedFile(rendered)
		if err != nil {
			return nil, fmt.Errorf("normalize legacy Patch file %d: %w", fileIndex, err)
		}
		changes.Files = append(changes.Files, file)
	}
	if err := validateLegacyRenderedLines(*wire.PatchRender); err != nil {
		return nil, err
	}
	presentation := &patchformat.Presentation{
		Variant: patchformat.PresentationVariantChanges,
		Changes: &changes,
	}
	if !presentation.Valid() {
		return nil, errors.New("normalized legacy Patch changes are structurally invalid")
	}
	return presentation, nil
}

func legacyNoRenderInputDetail(wire legacyToolCallMetaWire) (string, error) {
	switch {
	case isPatchToolName(wire.ToolName):
		if wire.Command == "" ||
			wire.Command != wire.CompactText ||
			wire.Command != wire.PatchSummary ||
			wire.Command != wire.PatchDetail {
			return "", errors.New("legacy failed Patch metadata text is inconsistent")
		}
		return wire.PatchDetail, nil
	case IsPatchFamilyToolName(wire.ToolName):
		if wire.Command == "" ||
			wire.Command != wire.CompactText ||
			wire.PatchSummary != "" ||
			wire.PatchDetail != "" {
			return "", errors.New("legacy failed Edit metadata text is inconsistent")
		}
		return wire.Command, nil
	default:
		return "", errors.New("legacy no-render metadata is not a Patch-family tool")
	}
}

func isPatchToolName(toolName string) bool {
	id, ok := toolspec.ParseID(toolName)
	return ok && id == toolspec.ToolPatch
}

func legacyRawPatchInputDetail(wire legacyToolCallMetaWire) (string, error) {
	rendered := wire.PatchRender
	if rendered == nil ||
		len(rendered.Files) != 0 ||
		len(rendered.SummaryLines) != 1 ||
		len(rendered.DetailLines) == 0 {
		return "", errors.New("legacy raw Patch presentation is malformed")
	}
	for _, line := range rendered.SummaryLines {
		if line.Kind != patchformat.RenderedLineKindRaw ||
			line.FileIndex != -1 ||
			line.Path != "" {
			return "", errors.New("legacy raw Patch summary is malformed")
		}
	}
	for _, line := range rendered.DetailLines {
		if line.Kind != patchformat.RenderedLineKindRaw ||
			line.FileIndex != -1 ||
			line.Path != "" {
			return "", errors.New("legacy raw Patch detail is malformed")
		}
	}
	if rendered.SummaryLines[0].Text != rendered.DetailLines[0].Text ||
		wire.PatchSummary != rendered.SummaryText() ||
		wire.PatchDetail != rendered.DetailText() ||
		wire.CompactText != wire.PatchSummary ||
		wire.Command != wire.PatchDetail {
		return "", errors.New("legacy raw Patch text is inconsistent")
	}
	return wire.PatchDetail, nil
}

func normalizeLegacyRenderedFile(rendered patchformat.RenderedFile) (patchformat.FileChange, error) {
	if rendered.AbsPath == "" || rendered.RelPath == "" ||
		rendered.Added < 0 || rendered.Removed < 0 {
		return patchformat.FileChange{}, errors.New("legacy rendered file has invalid path or counts")
	}

	operations := make([]patchformat.FileOperation, 0, len(rendered.WholeFileDeletions)+1)
	groups := make([]patchformat.ChangeGroup, 0, 4)
	current := patchformat.ChangeGroup{}
	added := 0
	removed := 0
	deletionIndex := 0
	flushGroup := func() {
		if len(current.Lines) == 0 {
			return
		}
		groups = append(groups, current)
		current = patchformat.ChangeGroup{}
	}
	flushUpdate := func() {
		flushGroup()
		if len(groups) == 0 {
			return
		}
		operations = append(operations, patchformat.FileOperation{
			Kind:   patchformat.FileOperationUpdate,
			Groups: groups,
		})
		groups = nil
	}
	for _, diff := range rendered.Diff {
		if diff == "-<deleted file>" {
			flushUpdate()
			if deletionIndex >= len(rendered.WholeFileDeletions) {
				return patchformat.FileChange{}, errors.New("legacy deletion marker has no operation")
			}
			deletion := rendered.WholeFileDeletions[deletionIndex]
			operations = append(operations, patchformat.FileOperation{
				Kind:     patchformat.FileOperationDelete,
				Deletion: &deletion,
			})
			deletionIndex++
			continue
		}
		if len(diff) == 0 ||
			diff == "*** End of File" ||
			diff[0] == ' ' ||
			diff[0] == '@' {
			flushGroup()
			continue
		}
		if diff[0] != '+' && diff[0] != '-' {
			return patchformat.FileChange{}, errors.New("legacy rendered file has an invalid diff line")
		}
		kind := patchformat.ChangedLineAdded
		if diff[0] == '-' {
			kind = patchformat.ChangedLineRemoved
			removed++
		} else {
			added++
		}
		current.Lines = append(current.Lines, patchformat.ChangedLine{
			Kind:    kind,
			Content: diff[1:],
		})
	}
	flushUpdate()
	if deletionIndex != len(rendered.WholeFileDeletions) {
		return patchformat.FileChange{}, errors.New("legacy deletion operation has no marker")
	}
	if added != rendered.Added || removed != rendered.Removed {
		return patchformat.FileChange{}, errors.New("legacy rendered file counts are inconsistent")
	}
	if len(operations) == 0 {
		operations = append(operations, patchformat.FileOperation{
			Kind: patchformat.FileOperationUpdate,
		})
	}

	file := patchformat.FileChange{
		Path: patchformat.Path{
			Absolute: rendered.AbsPath,
			Relative: rendered.RelPath,
		},
		Added:      rendered.Added,
		Operations: operations,
	}
	file.Removed = normalizedLegacyRemovedLineCount(rendered)
	return file, nil
}

func normalizedLegacyRemovedLineCount(file patchformat.RenderedFile) *int {
	total := file.Removed
	groups := make(map[patchformat.WholeFileDeletionGroupID]struct{}, len(file.WholeFileDeletions))
	for _, operation := range file.WholeFileDeletions {
		if operation.Disposition == nil {
			return nil
		}
		group := operation.Disposition.PhysicalGroup
		if _, exists := groups[group]; exists {
			continue
		}
		groups[group] = struct{}{}
		total += operation.Disposition.Removed
	}
	return &total
}

func validateLegacyRenderedLines(rendered patchformat.RenderedPatch) error {
	if len(rendered.SummaryLines) != len(rendered.Files) {
		return errors.New("legacy Patch summary lines do not match files")
	}
	detailIndex := 0
	for fileIndex, file := range rendered.Files {
		removed := patchformat.RemovedLineCount(file)
		summary := file.RelPath
		if summary == "" {
			summary = file.AbsPath
		}
		if file.Added > 0 {
			summary += fmt.Sprintf(" +%d", file.Added)
		}
		if removed != nil {
			summary += fmt.Sprintf(" -%d", *removed)
		}
		summaryLine := rendered.SummaryLines[fileIndex]
		if summaryLine.Kind != patchformat.RenderedLineKindFile ||
			summaryLine.Text != summary ||
			summaryLine.FileIndex != fileIndex ||
			summaryLine.Path != file.RelPath {
			return errors.New("legacy Patch summary line is invalid")
		}

		if detailIndex >= len(rendered.DetailLines) {
			return errors.New("legacy Patch detail is missing a file header")
		}
		detailPath := file.AbsPath
		if strings.TrimSpace(detailPath) == "" {
			detailPath = file.RelPath
		}
		header := rendered.DetailLines[detailIndex]
		if header.Kind != patchformat.RenderedLineKindFile ||
			header.Text != detailPath ||
			header.FileIndex != fileIndex ||
			header.Path != detailPath {
			return errors.New("legacy Patch detail file header is invalid")
		}
		detailIndex++
		for _, diff := range file.Diff {
			if detailIndex >= len(rendered.DetailLines) {
				return errors.New("legacy Patch detail is missing a diff line")
			}
			line := rendered.DetailLines[detailIndex]
			if line.Kind != patchformat.RenderedLineKindDiff ||
				line.Text != diff ||
				line.FileIndex != fileIndex ||
				line.Path != "" {
				return errors.New("legacy Patch detail diff line is invalid")
			}
			detailIndex++
		}
	}
	if detailIndex != len(rendered.DetailLines) {
		return errors.New("legacy Patch detail contains unexpected lines")
	}
	return nil
}

func currentToolCallMetaWireFromMeta(meta ToolCallMeta) currentToolCallMetaWire {
	return currentToolCallMetaWire{
		toolCallMetaWireCommon: toolCallMetaWireCommonFromMeta(meta),
		PatchPresentation:      meta.PatchPresentation,
	}
}

func toolCallMetaWireCommonFromMeta(meta ToolCallMeta) toolCallMetaWireCommon {
	return toolCallMetaWireCommon{
		ToolName:               meta.ToolName,
		Presentation:           meta.Presentation,
		RenderBehavior:         meta.RenderBehavior,
		IsShell:                meta.IsShell,
		UserInitiated:          meta.UserInitiated,
		Command:                meta.Command,
		CompactText:            meta.CompactText,
		InlineMeta:             meta.InlineMeta,
		TimeoutLabel:           meta.TimeoutLabel,
		RenderHint:             meta.RenderHint,
		Question:               meta.Question,
		Suggestions:            meta.Suggestions,
		RecommendedOptionIndex: meta.RecommendedOptionIndex,
		OmitSuccessfulResult:   meta.OmitSuccessfulResult,
		RawOutputRequested:     meta.RawOutputRequested,
		OutputTruncated:        meta.OutputTruncated,
		MovedToBackground:      meta.MovedToBackground,
		ShellExitCode:          meta.ShellExitCode,
	}
}

func (wire currentToolCallMetaWire) meta() ToolCallMeta {
	meta := wire.toolCallMetaWireCommon.meta()
	meta.PatchPresentation = wire.PatchPresentation
	return meta
}

func invalidToolCallMetaDecode(cause error) ToolCallMetaDecodeResult {
	return ToolCallMetaDecodeResult{
		Kind:  ToolCallMetaDecodeInvalid,
		Cause: cause,
	}
}
