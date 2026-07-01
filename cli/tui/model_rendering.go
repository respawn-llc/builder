package tui

import (
	"strings"

	"core/shared/transcript"
)

func (m Model) buildDetailBlockSpecs(includeStreaming bool) []detailBlockSpec {
	blocks := make([]detailBlockSpec, 0, len(m.transcriptInput.Entries)+1)
	consumedResults := make(map[int]struct{})
	resultIndex := buildToolResultIndex(m.transcriptInput.Entries)
	for idx := 0; idx < len(m.transcriptInput.Entries); idx++ {
		if _, consumed := consumedResults[idx]; consumed {
			continue
		}
		if reasoningSpec, ok := m.prefixedReasoningBlockSpec(idx, consumedResults); ok {
			blocks = append(blocks, reasoningSpec)
		}
		entry := m.transcriptInput.Entries[idx]
		role := TranscriptRoleFromWire(string(entry.Role))
		intent := role.DisplayIntent(entry.Phase)
		switch role {
		case TranscriptRoleToolCall:
			blocks = append(blocks, m.detailToolCallSpec(idx, entry, consumedResults, resultIndex))
		case TranscriptRoleToolResult, TranscriptRoleToolResultOK, TranscriptRoleToolResultError:
			meta := cloneToolCallMeta(entry.ToolCall)
			blockRole := resultOnlyToolBlockRole(role, meta)
			text := entry.Text
			absoluteIndex := m.absoluteTranscriptIndex(idx)
			expandable := false
			if m.compactDetail && !m.detailRoleRendersFullWhenCollapsed(blockRole) {
				expandable = m.detailRenderedContentLineCount(blockRole, text) > 1
			}
			blocks = append(blocks, detailBlockSpec{
				role:       blockRole,
				entryIndex: absoluteIndex,
				entryEnd:   absoluteIndex,
				selectable: true,
				expanded:   m.detailEntryExpanded(absoluteIndex),
				expandable: expandable,
				render: func(model Model, symbolOverride string) []string {
					if symbolOverride == "" {
						symbolOverride = model.shellWarningSymbolOverride(blockRole, meta)
					}
					if model.detailEntryExpanded(absoluteIndex) || blockRole.IsToolErrorHeadline() {
						return model.detailWithTreeGuideWithSymbol(blockRole, model.flattenEntryWithMetaAndSymbol(blockRole, text, false, meta, symbolOverride), true, symbolOverride)
					}
					return model.detailWithTreeGuideWithSymbol(blockRole, model.flattenEntryWithMetaAndSymbol(blockRole, model.firstDetailPreviewLine(text, "Tool output"), false, meta, symbolOverride), false, symbolOverride)
				},
			})
		default:
			if role == TranscriptRoleUnknown {
				continue
			}
			blocks = append(blocks, m.detailStandardSpec(idx, entry, intent, consumedResults))
		}
	}
	if includeStreaming {
		if spec, ok := m.detailStreamingReasoningSpec(); ok {
			blocks = append(blocks, spec)
		}
		if spec, ok := m.detailStreamingAssistantSpec(); ok {
			blocks = append(blocks, spec)
		}
	}
	return blocks
}

type transcriptBlockMode int

const (
	transcriptBlockModeDetail transcriptBlockMode = iota
	transcriptBlockModeOngoing
)

type transcriptBlockOptions struct {
	mode             transcriptBlockMode
	includeStreaming bool
	applySelection   bool
}

func (m Model) buildTranscriptBlocks(opts transcriptBlockOptions) []ongoingBlock {
	blocks := make([]ongoingBlock, 0, len(m.transcriptInput.Entries)+1)
	consumedResults := make(map[int]struct{})
	resultIndex := buildToolResultIndex(m.transcriptInput.Entries)
	for idx := 0; idx < len(m.transcriptInput.Entries); idx++ {
		if _, consumed := consumedResults[idx]; consumed {
			continue
		}
		if reasoningBlock, ok := m.prefixedReasoningBlock(idx, consumedResults, opts); ok {
			blocks = append(blocks, reasoningBlock)
		}
		entry := m.transcriptInput.Entries[idx]
		role := TranscriptRoleFromWire(string(entry.Role))
		intent := role.DisplayIntent(entry.Phase)
		if opts.mode == transcriptBlockModeOngoing && !isVisibleInOngoing(entry) {
			continue
		}
		block, ok := m.entryBlock(idx, entry, role, intent, consumedResults, resultIndex, opts)
		if !ok {
			continue
		}
		blocks = append(blocks, block)
	}
	return m.appendStreamingBlocks(blocks, opts)
}

func (m Model) prefixedReasoningBlock(entryIndex int, consumed map[int]struct{}, opts transcriptBlockOptions) (ongoingBlock, bool) {
	if opts.mode != transcriptBlockModeDetail {
		return ongoingBlock{}, false
	}
	thinkingBlock, ok := m.trailingThinkingBlockBeforeEntry(m.transcriptInput.Entries, entryIndex, consumed)
	if !ok {
		return ongoingBlock{}, false
	}
	return ongoingBlock{role: RenderIntentReasoning, lines: thinkingBlock, entryIndex: -1, entryEnd: -1}, true
}

func (m Model) prefixedReasoningBlockSpec(entryIndex int, consumed map[int]struct{}) (detailBlockSpec, bool) {
	thinkingText, ok := m.trailingThinkingTextBeforeEntry(m.transcriptInput.Entries, entryIndex, consumed)
	if !ok {
		return detailBlockSpec{}, false
	}
	return detailBlockSpec{
		role:       RenderIntentReasoning,
		entryIndex: -1,
		entryEnd:   -1,
		render: func(model Model, symbolOverride string) []string {
			return model.flattenEntryWithMetaAndSymbol(RenderIntentReasoning, thinkingText, false, nil, "")
		},
	}, true
}

func (m Model) entryBlock(entryIndex int, entry TranscriptEntry, role TranscriptRole, intent RenderIntent, consumed map[int]struct{}, resultIndex toolResultIndex, opts transcriptBlockOptions) (ongoingBlock, bool) {
	switch role {
	case TranscriptRoleToolCall:
		return m.toolCallBlock(entryIndex, entry, consumed, resultIndex, opts), true
	case TranscriptRoleToolResult, TranscriptRoleToolResultOK, TranscriptRoleToolResultError:
		if opts.mode == transcriptBlockModeOngoing {
			return ongoingBlock{}, false
		}
		meta := cloneToolCallMeta(entry.ToolCall)
		blockRole := resultOnlyToolBlockRole(role, meta)
		symbolOverride := m.shellWarningSymbolOverride(blockRole, meta)
		return ongoingBlock{
			role:       blockRole,
			lines:      m.flattenEntryWithMetaAndSymbol(blockRole, entry.Text, false, meta, symbolOverride),
			entryIndex: m.absoluteTranscriptIndex(entryIndex),
			entryEnd:   m.absoluteTranscriptIndex(entryIndex),
		}, true
	default:
		return m.standardEntryBlock(entryIndex, entry, intent, consumed, opts), true
	}
}

func resultOnlyToolBlockRole(resultRole TranscriptRole, meta *transcript.ToolCallMeta) RenderIntent {
	blockRole := RenderIntentTool
	switch {
	case isAskQuestionToolCall(meta):
		blockRole = RenderIntentToolQuestion
	case isWebSearchToolCall(meta):
		blockRole = RenderIntentToolWebSearch
	case isPatchToolCall(meta):
		blockRole = RenderIntentToolPatch
	case meta != nil && meta.UsesShellRendering():
		blockRole = RenderIntentToolShell
	}
	return blockRole.BaseToolResultIntent(resultRole)
}

func (m Model) toolCallBlock(entryIndex int, entry TranscriptEntry, consumed map[int]struct{}, resultIndex toolResultIndex, opts transcriptBlockOptions) ongoingBlock {
	blockRole := RenderIntentTool
	if isAskQuestionToolCall(entry.ToolCall) {
		return m.askQuestionBlock(entryIndex, entry, consumed, resultIndex, opts, blockRole)
	}
	if isWebSearchToolCall(entry.ToolCall) {
		blockRole = RenderIntentToolWebSearch
	} else if isPatchToolCall(entry.ToolCall) {
		blockRole = RenderIntentToolPatch
	} else if entry.ToolCall != nil && entry.ToolCall.UsesShellRendering() {
		blockRole = RenderIntentToolShell
	}
	combined := m.toolCallDisplayText(entry, blockRole, opts)
	entryEnd := entryIndex
	blockRole, combined, resultIdx := m.applyToolResult(entryIndex, entry.ToolCall, blockRole, combined, consumed, resultIndex, opts)
	if resultIdx >= 0 {
		entryEnd = resultIdx
	}
	effectiveMeta := entry.ToolCall
	if resultIdx >= 0 && m.transcriptInput.Entries[resultIdx].ToolCall != nil {
		effectiveMeta = effectiveToolCallMeta(entry.ToolCall, m.transcriptInput.Entries[resultIdx].ToolCall)
		combined = transcript.CompactToolCallText(effectiveMeta, combined)
		if isPatchToolCall(effectiveMeta) {
			blockRole = RenderIntentToolPatch.BaseToolResultIntent(TranscriptRoleFromWire(string(m.transcriptInput.Entries[resultIdx].Role)))
		}
	}
	symbolOverride := m.shellWarningSymbolOverride(blockRole, effectiveMeta)
	lines := m.flattenEntryWithMetaAndSymbol(blockRole, combined, opts.mode == transcriptBlockModeOngoing, effectiveMeta, symbolOverride)
	if opts.mode == transcriptBlockModeOngoing {
		lines = m.ongoingToolWithTreeGuideWithSymbol(blockRole, lines, symbolOverride)
	}
	return ongoingBlock{
		role:       blockRole,
		lines:      lines,
		entryIndex: m.absoluteTranscriptIndex(entryIndex),
		entryEnd:   m.absoluteTranscriptIndex(entryEnd),
	}
}

func (m Model) detailToolCallSpec(entryIndex int, entry TranscriptEntry, consumed map[int]struct{}, resultIndex toolResultIndex) detailBlockSpec {
	blockRole := RenderIntentTool
	if isAskQuestionToolCall(entry.ToolCall) {
		return m.detailAskQuestionSpec(entryIndex, entry, consumed, resultIndex)
	}
	if isWebSearchToolCall(entry.ToolCall) {
		blockRole = RenderIntentToolWebSearch
	} else if isPatchToolCall(entry.ToolCall) {
		blockRole = RenderIntentToolPatch
	} else if entry.ToolCall != nil && entry.ToolCall.UsesShellRendering() {
		blockRole = RenderIntentToolShell
	}
	combined := toolCallDisplayText(entry.ToolCall, entry.Text)
	entryEnd := entryIndex
	resultText := ""
	resultSummary := ""
	if resultIdx := resultIndex.findMatchingToolResultIndex(m.transcriptInput.Entries, entryIndex, consumed); resultIdx >= 0 {
		resultEntry := m.transcriptInput.Entries[resultIdx]
		if resultEntry.ToolCall != nil {
			combined = toolCallDisplayText(resultEntry.ToolCall, combined)
		}
		resultRole := TranscriptRoleFromWire(string(resultEntry.Role))
		resultSummary = strings.TrimSpace(resultEntry.ToolResultSummary)
		omitSuccessfulResult := entry.ToolCall != nil && entry.ToolCall.OmitSuccessfulResult && resultRole != TranscriptRoleToolResultError
		if trimmedResultText := strings.TrimSpace(resultEntry.Text); trimmedResultText != "" && !omitSuccessfulResult {
			combined += "\n" + resultEntry.Text
			resultText = resultEntry.Text
		}
		if resultRole.IsToolResult() {
			blockRole = blockRole.BaseToolResultIntent(resultRole)
			consumed[resultIdx] = struct{}{}
			entryEnd = resultIdx
		}
	}
	absoluteIndex := m.absoluteTranscriptIndex(entryIndex)
	absoluteEnd := m.absoluteTranscriptIndex(entryEnd)
	meta := cloneToolCallMeta(entry.ToolCall)
	if entryEnd != entryIndex && m.transcriptInput.Entries[entryEnd].ToolCall != nil {
		meta = effectiveToolCallMeta(entry.ToolCall, m.transcriptInput.Entries[entryEnd].ToolCall)
		if isPatchToolCall(meta) {
			blockRole = RenderIntentToolPatch.BaseToolResultIntent(TranscriptRoleFromWire(string(m.transcriptInput.Entries[entryEnd].Role)))
		}
	}
	return detailBlockSpec{
		role:       blockRole,
		entryIndex: absoluteIndex,
		entryEnd:   absoluteEnd,
		selectable: true,
		expanded:   m.detailEntryExpanded(absoluteIndex),
		expandable: m.detailToolCallExpandable(blockRole, entry, resultSummary, combined, meta, resultText),
		render: func(model Model, symbolOverride string) []string {
			if symbolOverride == "" {
				symbolOverride = model.shellWarningSymbolOverride(blockRole, meta)
			}
			if !model.detailEntryExpanded(absoluteIndex) {
				return model.detailCollapsedToolLinesWithSymbol(blockRole, entry, resultSummary, symbolOverride)
			}
			if meta != nil && meta.PatchRender != nil {
				return model.detailWithTreeGuideWithSymbol(blockRole, model.flattenPatchToolBlockWithSymbol(blockRole, meta, resultText, symbolOverride), true, symbolOverride)
			}
			return model.detailWithTreeGuideWithSymbol(blockRole, model.flattenEntryWithMetaAndSymbol(blockRole, combined, false, meta, symbolOverride), true, symbolOverride)
		},
	}
}

func effectiveToolCallMeta(callMeta, resultMeta *transcript.ToolCallMeta) *transcript.ToolCallMeta {
	if resultMeta == nil {
		return cloneToolCallMeta(callMeta)
	}
	normalizedResult := transcript.NormalizeToolCallMeta(*resultMeta)
	if callMeta == nil || !isShellStatusOnlyToolCallMeta(normalizedResult) {
		return cloneToolCallMeta(&normalizedResult)
	}
	merged := transcript.NormalizeToolCallMeta(*callMeta)
	merged.RawOutputRequested = merged.RawOutputRequested || normalizedResult.RawOutputRequested
	merged.OutputTruncated = merged.OutputTruncated || normalizedResult.OutputTruncated
	return &merged
}

func isShellStatusOnlyToolCallMeta(meta transcript.ToolCallMeta) bool {
	return meta.IsShell &&
		strings.TrimSpace(meta.Command) == "" &&
		strings.TrimSpace(meta.CompactText) == "" &&
		strings.TrimSpace(meta.PatchSummary) == "" &&
		strings.TrimSpace(meta.PatchDetail) == "" &&
		meta.PatchRender == nil &&
		meta.Question == "" &&
		len(meta.Suggestions) == 0 &&
		meta.RecommendedOptionIndex == 0 &&
		(meta.RawOutputRequested || meta.OutputTruncated)
}

func (m Model) askQuestionBlock(entryIndex int, entry TranscriptEntry, consumed map[int]struct{}, resultIndex toolResultIndex, opts transcriptBlockOptions, defaultRole RenderIntent) ongoingBlock {
	blockRole := RenderIntentToolQuestion
	question, suggestions, recommendedOptionIndex := askQuestionDisplay(entry.ToolCall, entry.Text)
	answer := ""
	entryEnd := entryIndex
	if resultIdx := resultIndex.findMatchingToolResultIndex(m.transcriptInput.Entries, entryIndex, consumed); resultIdx >= 0 {
		resultEntry := m.transcriptInput.Entries[resultIdx]
		nextRole := TranscriptRoleFromWire(string(resultEntry.Role))
		if nextRole.IsToolResult() {
			answer = strings.TrimSpace(resultEntry.Text)
			if opts.mode == transcriptBlockModeOngoing {
				resultText := resultEntry.Text
				if strings.TrimSpace(resultEntry.CondensedText) != "" {
					resultText = resultEntry.CondensedText
				}
				answer = strings.TrimSpace(resultText)
			}
			blockRole = blockRole.BaseToolResultIntent(nextRole)
			consumed[resultIdx] = struct{}{}
			entryEnd = resultIdx
		}
	}
	lines := m.flattenAskQuestionEntryWithSymbol(blockRole, question, suggestions, recommendedOptionIndex, answer, opts.mode == transcriptBlockModeDetail, "")
	if opts.mode == transcriptBlockModeOngoing {
		lines = m.ongoingToolWithTreeGuideWithSymbol(blockRole, lines, "")
	}
	return ongoingBlock{
		role:       blockRole,
		lines:      lines,
		entryIndex: m.absoluteTranscriptIndex(entryIndex),
		entryEnd:   m.absoluteTranscriptIndex(entryEnd),
	}
}

func (m Model) detailAskQuestionSpec(entryIndex int, entry TranscriptEntry, consumed map[int]struct{}, resultIndex toolResultIndex) detailBlockSpec {
	blockRole := RenderIntentToolQuestion
	question, suggestions, recommendedOptionIndex := askQuestionDisplay(entry.ToolCall, entry.Text)
	answer := ""
	resultSummary := ""
	if resultIdx := resultIndex.findMatchingToolResultIndex(m.transcriptInput.Entries, entryIndex, consumed); resultIdx >= 0 {
		nextRole := TranscriptRoleFromWire(string(m.transcriptInput.Entries[resultIdx].Role))
		if nextRole.IsToolResult() {
			answer = strings.TrimSpace(m.transcriptInput.Entries[resultIdx].Text)
			resultSummary = strings.TrimSpace(m.transcriptInput.Entries[resultIdx].ToolResultSummary)
			blockRole = blockRole.BaseToolResultIntent(nextRole)
			consumed[resultIdx] = struct{}{}
		}
	}
	absoluteIndex := m.absoluteTranscriptIndex(entryIndex)
	expandable := false
	if m.compactDetail && !m.detailRoleRendersFullWhenCollapsed(blockRole) {
		expandable = len(suggestions) > 0 ||
			recommendedOptionIndex > 0 ||
			(strings.TrimSpace(answer) != "" && strings.TrimSpace(answer) != strings.TrimSpace(resultSummary)) ||
			m.detailRenderedContentLineCount(blockRole, question) > 1
	}
	return detailBlockSpec{
		role:       blockRole,
		entryIndex: absoluteIndex,
		entryEnd:   absoluteIndex,
		selectable: true,
		expanded:   m.detailEntryExpanded(absoluteIndex),
		expandable: expandable,
		render: func(model Model, symbolOverride string) []string {
			if model.detailEntryExpanded(absoluteIndex) {
				return model.detailWithTreeGuideWithSymbol(blockRole, model.flattenAskQuestionEntryWithSymbol(blockRole, question, suggestions, recommendedOptionIndex, answer, true, symbolOverride), true, symbolOverride)
			}
			collapsedAnswer := ""
			if resultSummary != "" {
				collapsedAnswer = resultSummary
			}
			return model.detailWithTreeGuideWithSymbol(blockRole, model.flattenAskQuestionEntryWithSymbol(blockRole, question, nil, 0, collapsedAnswer, false, symbolOverride), false, symbolOverride)
		},
	}
}

func (m Model) toolCallDisplayText(entry TranscriptEntry, blockRole RenderIntent, opts transcriptBlockOptions) string {
	if opts.mode == transcriptBlockModeDetail {
		return toolCallDisplayText(entry.ToolCall, entry.Text)
	}
	combined := transcript.CompactToolCallText(entry.ToolCall, entry.Text)
	if blockRole.IsShellPreview() {
		combined = compactOngoingShellPreviewText(combined)
	}
	return combined
}

func (m Model) applyToolResult(entryIndex int, meta *transcript.ToolCallMeta, blockRole RenderIntent, combined string, consumed map[int]struct{}, resultIndex toolResultIndex, opts transcriptBlockOptions) (RenderIntent, string, int) {
	resultIdx := resultIndex.findMatchingToolResultIndex(m.transcriptInput.Entries, entryIndex, consumed)
	if resultIdx < 0 {
		return blockRole, combined, -1
	}
	nextRole := TranscriptRoleFromWire(string(m.transcriptInput.Entries[resultIdx].Role))
	if opts.mode == transcriptBlockModeDetail {
		resultText := m.transcriptInput.Entries[resultIdx].Text
		omitSuccessfulResult := meta != nil && meta.OmitSuccessfulResult && nextRole != TranscriptRoleToolResultError
		if strings.TrimSpace(resultText) != "" && !omitSuccessfulResult {
			combined += "\n" + resultText
		}
	}
	if nextRole.IsToolResult() {
		blockRole = blockRole.BaseToolResultIntent(nextRole)
		consumed[resultIdx] = struct{}{}
	}
	return blockRole, combined, resultIdx
}

func (m Model) standardEntryBlock(entryIndex int, entry TranscriptEntry, role RenderIntent, consumed map[int]struct{}, opts transcriptBlockOptions) ongoingBlock {
	if opts.mode == transcriptBlockModeDetail && TranscriptRole(role).IsThinking() {
		return ongoingBlock{
			role:       role,
			lines:      m.flattenEntryWithMetaAndSymbol(role, m.combinedThinkingText(entryIndex, consumed), false, nil, ""),
			entryIndex: m.absoluteTranscriptIndex(entryIndex),
			entryEnd:   m.absoluteTranscriptIndex(entryIndex),
		}
	}
	text := entry.Text
	if opts.mode == transcriptBlockModeOngoing {
		if strings.TrimSpace(entry.CondensedText) != "" {
			text = entry.CondensedText
		}
		if role == RenderIntentReviewerStatus {
			text = compactReviewerStatusForOngoing(text)
		} else if role == RenderIntentReviewerSuggestions {
			text = strings.TrimSpace(text)
		}
	}
	lines := m.flattenEntryWithMetaAndSymbol(role, text, false, nil, "")
	if opts.mode == transcriptBlockModeOngoing && role == RenderIntentGoalFeedback {
		lines = m.flattenPlainEntryWithIntents(role, text, PrimaryForeground, "")
	}
	absoluteIndex := m.absoluteTranscriptIndex(entryIndex)
	if opts.applySelection {
		lines = m.maybeSelectedUserBlock(absoluteIndex, role, lines)
	}
	return ongoingBlock{role: role, lines: lines, entryIndex: absoluteIndex, entryEnd: absoluteIndex}
}

func (m Model) detailStandardSpec(entryIndex int, entry TranscriptEntry, role RenderIntent, consumed map[int]struct{}) detailBlockSpec {
	text := entry.Text
	if TranscriptRole(role).IsThinking() {
		text = m.combinedThinkingText(entryIndex, consumed)
	}
	absoluteIndex := m.absoluteTranscriptIndex(entryIndex)
	return detailBlockSpec{
		role:       role,
		entryIndex: absoluteIndex,
		entryEnd:   absoluteIndex,
		selectable: true,
		expanded:   m.detailEntryExpanded(absoluteIndex),
		expandable: m.detailStandardExpandable(entry, role, text),
		render: func(model Model, symbolOverride string) []string {
			if model.detailEntryExpanded(absoluteIndex) || model.detailRoleRendersFullWhenCollapsed(role) || model.selectedTranscriptEntryMatches(absoluteIndex) {
				return model.detailWithTreeGuideWithSymbol(role, model.flattenEntryWithMetaAndSymbol(role, text, false, nil, symbolOverride), true, symbolOverride)
			}
			return model.detailCollapsedStandardLinesWithSymbol(entry, role, text, symbolOverride)
		},
	}
}

func (m Model) combinedThinkingText(entryIndex int, consumed map[int]struct{}) string {
	combined := strings.TrimSpace(m.transcriptInput.Entries[entryIndex].Text)
	for idx := entryIndex + 1; idx < len(m.transcriptInput.Entries); idx++ {
		if _, used := consumed[idx]; used {
			break
		}
		if !TranscriptRoleFromWire(string(m.transcriptInput.Entries[idx].Role)).IsThinking() {
			break
		}
		nextText := strings.TrimSpace(m.transcriptInput.Entries[idx].Text)
		if nextText != "" {
			if combined == "" {
				combined = nextText
			} else {
				combined += "\n" + nextText
			}
		}
		consumed[idx] = struct{}{}
	}
	return combined
}

func (m Model) appendStreamingBlocks(blocks []ongoingBlock, opts transcriptBlockOptions) []ongoingBlock {
	if opts.includeStreaming && opts.mode == transcriptBlockModeDetail {
		if lines := m.streamingReasoningLines(); len(lines) > 0 {
			blocks = append(blocks, ongoingBlock{role: RenderIntentReasoning, lines: lines, entryIndex: -1, entryEnd: -1})
		}
	}
	if !opts.includeStreaming || m.transcriptInput.Ongoing == "" {
		return blocks
	}
	lines := m.flattenEntryWithMetaAndSymbol(RenderIntentAssistant, m.transcriptInput.Ongoing, false, nil, "")
	if opts.mode == transcriptBlockModeOngoing {
		lines = m.flattenEntryPlain(RenderIntentAssistant, m.transcriptInput.Ongoing)
	}
	return append(blocks, ongoingBlock{role: RenderIntentAssistant, lines: lines, entryIndex: -1, entryEnd: -1})
}

func (m Model) streamingReasoningLines() []string {
	if len(m.transcriptInput.StreamingReasoning) == 0 {
		return nil
	}
	parts := make([]string, 0, len(m.transcriptInput.StreamingReasoning))
	for _, entry := range m.transcriptInput.StreamingReasoning {
		text := strings.TrimSpace(entry.Text)
		if text == "" {
			continue
		}
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		return nil
	}
	return m.flattenEntryWithMetaAndSymbol(RenderIntentReasoning, strings.Join(parts, "\n"), false, nil, "")
}

func (m Model) detailStreamingReasoningSpec() (detailBlockSpec, bool) {
	if len(m.transcriptInput.StreamingReasoning) == 0 {
		return detailBlockSpec{}, false
	}
	parts := make([]string, 0, len(m.transcriptInput.StreamingReasoning))
	for _, entry := range m.transcriptInput.StreamingReasoning {
		text := strings.TrimSpace(entry.Text)
		if text == "" {
			continue
		}
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		return detailBlockSpec{}, false
	}
	combined := strings.Join(parts, "\n")
	return detailBlockSpec{
		role:       RenderIntentReasoning,
		entryIndex: -1,
		entryEnd:   -1,
		render: func(model Model, symbolOverride string) []string {
			return model.flattenEntryWithMetaAndSymbol(RenderIntentReasoning, combined, false, nil, "")
		},
	}, true
}

func (m Model) detailStreamingAssistantSpec() (detailBlockSpec, bool) {
	if strings.TrimSpace(m.transcriptInput.Ongoing) == "" {
		return detailBlockSpec{}, false
	}
	text := m.transcriptInput.Ongoing
	return detailBlockSpec{
		role:       RenderIntentAssistant,
		entryIndex: -1,
		entryEnd:   -1,
		render: func(model Model, symbolOverride string) []string {
			return model.flattenEntryWithMetaAndSymbol(RenderIntentAssistant, text, false, nil, "")
		},
	}, true
}

func (m Model) trailingThinkingBlockBeforeEntry(entries []TranscriptEntry, idx int, consumed map[int]struct{}) ([]string, bool) {
	combined, ok := m.trailingThinkingTextBeforeEntry(entries, idx, consumed)
	if !ok {
		return nil, false
	}
	return m.flattenEntryWithMetaAndSymbol(RenderIntentReasoning, combined, false, nil, ""), true
}

func (m Model) trailingThinkingTextBeforeEntry(entries []TranscriptEntry, idx int, consumed map[int]struct{}) (string, bool) {
	if idx < 0 || idx >= len(entries) {
		return "", false
	}
	role := TranscriptRoleFromWire(string(entries[idx].Role))
	if role != TranscriptRoleAssistant && role != TranscriptRoleToolCall {
		return "", false
	}
	actionEnd := idx
	for actionEnd+1 < len(entries) {
		next := actionEnd + 1
		if _, used := consumed[next]; used {
			break
		}
		if TranscriptRoleFromWire(string(entries[next].Role)) != TranscriptRoleToolCall {
			break
		}
		actionEnd = next
	}
	thinkingStart := actionEnd + 1
	if thinkingStart >= len(entries) {
		return "", false
	}
	if _, used := consumed[thinkingStart]; used {
		return "", false
	}
	if !TranscriptRoleFromWire(string(entries[thinkingStart].Role)).IsThinking() {
		return "", false
	}

	combined := strings.TrimSpace(entries[thinkingStart].Text)
	consumed[thinkingStart] = struct{}{}
	for j := thinkingStart + 1; j < len(entries); j++ {
		if _, used := consumed[j]; used {
			break
		}
		if !TranscriptRoleFromWire(string(entries[j].Role)).IsThinking() {
			break
		}
		nextText := strings.TrimSpace(entries[j].Text)
		if nextText != "" {
			if combined == "" {
				combined = nextText
			} else {
				combined += "\n" + nextText
			}
		}
		consumed[j] = struct{}{}
	}

	if combined == "" {
		return "", false
	}
	return combined, true
}

func (m Model) ongoingLineRangeForEntry(entryIndex int) (int, int, bool) {
	if entryIndex < 0 {
		return 0, 0, false
	}
	if lineRange, ok := m.transcriptViewProjection().OngoingEntryLineRanges[entryIndex]; ok {
		return lineRange.Start, lineRange.End, true
	}
	return 0, 0, false
}

func (m Model) detailLineRangeForEntry(entryIndex int) (int, int, bool) {
	if entryIndex < 0 {
		return 0, 0, false
	}
	if lineRange, ok := m.detailViewProjection().DetailEntryLineRanges[entryIndex]; ok {
		return lineRange.Start, lineRange.End, true
	}
	return 0, 0, false
}
