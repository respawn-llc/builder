package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"core/cli/tui"

	xansi "github.com/charmbracelet/x/ansi"
)

func (m *uiModel) nativeCommittedProjectionForEntries(entries []tui.TranscriptEntry) tui.TranscriptProjection {
	if m == nil {
		return tui.TranscriptProjection{}
	}
	committedEntries := committedTranscriptEntriesForApp(entries)
	projection := m.view.CommittedOngoingProjectionForEntries(committedEntries)
	m.attachNativeProjectionSourceKeys(&projection, committedEntries)
	return projection
}

func (m *uiModel) attachNativeProjectionSourceKeys(projection *tui.TranscriptProjection, entries []tui.TranscriptEntry) {
	if projection == nil || len(projection.Blocks) == 0 || len(entries) == 0 {
		return
	}
	baseOffset := 0
	if m != nil {
		baseOffset = m.transcriptBaseOffset
	}
	for idx := range projection.Blocks {
		block := &projection.Blocks[idx]
		block.SourceKey = nativeProjectionBlockSourceKey(*block, entries, baseOffset)
	}
}

func nativeProjectionBlockSourceKey(block tui.TranscriptProjectionBlock, entries []tui.TranscriptEntry, baseOffset int) string {
	start, ok := nativeProjectionBlockLocalEntryIndex(block.EntryIndex, entries, baseOffset)
	if !ok {
		return nativeProjectionBlockLineSourceKey(block)
	}
	end, ok := nativeProjectionBlockLocalEntryIndex(block.EntryEnd, entries, baseOffset)
	if !ok || end < start {
		end = start
	}
	end = min(end, len(entries)-1)
	payload, err := json.Marshal(entries[start : end+1])
	if err == nil {
		return string(payload)
	}
	return nativeProjectionEntriesFallbackSourceKey(entries[start : end+1])
}

func nativeProjectionEntriesFallbackSourceKey(entries []tui.TranscriptEntry) string {
	parts := make([]string, 0, len(entries)*14)
	for _, entry := range entries {
		parts = append(parts,
			string(entry.Visibility),
			entry.RollbackTargetID,
			fmt.Sprint(entry.Transient),
			fmt.Sprint(entry.Committed),
			string(entry.Role),
			entry.Text,
			entry.CondensedText,
			string(entry.Phase),
			string(entry.MessageType),
			entry.SourcePath,
			entry.CompactLabel,
			entry.ToolResultSummary,
			entry.ToolCallID,
			entry.NoticeID,
			fmt.Sprintf("%#v", entry.ToolCall),
		)
	}
	return strings.Join(parts, "\x00")
}

func nativeProjectionBlockLocalEntryIndex(absolute int, entries []tui.TranscriptEntry, baseOffset int) (int, bool) {
	if absolute < 0 {
		return 0, false
	}
	local := absolute - baseOffset
	if local >= 0 && local < len(entries) {
		return local, true
	}
	if absolute < len(entries) {
		return absolute, true
	}
	return 0, false
}

func nativeProjectionBlockLineSourceKey(block tui.TranscriptProjectionBlock) string {
	lines := make([]string, 0, len(block.Lines))
	for _, line := range block.Lines {
		lines = append(lines, xansi.Strip(line))
	}
	return strings.Join(lines, "\x00")
}

func (m *uiModel) deliverCurrentNativeStableProjectionAfterResize() error {
	if m == nil || !m.nativeSurfaceConfigured() || !m.nativeStableSurfaceReadyForCurrentGeometry() {
		return nil
	}
	return m.deliverNativeStableProjectionChange(
		m.nativeDeliveredStableProjection,
		m.nativeCommittedProjectionForEntries(m.transcriptEntries),
		true,
		m.nativeSurface.AssistantStreaming(),
		m.nativeAssistantStreamIncomplete,
		m.activeAssistantStreamText(),
	)
}

func (m *uiModel) reprojectNativeDeliveredStableProjectionForCurrentGeometry() {
	if m == nil || m.nativeDeliveredStableProjection.Empty() {
		return
	}
	current := m.nativeCommittedProjectionForEntries(m.transcriptEntries)
	deliveredCount := len(m.nativeDeliveredStableProjection.Blocks)
	if deliveredCount == 0 || deliveredCount > len(current.Blocks) {
		return
	}
	if reprojected, ok := m.reprojectNativeDeliveredStableProjectionPrefix(current, deliveredCount); ok {
		m.nativeDeliveredStableProjection = reprojected
		return
	}
	if reprojected, ok := m.reprojectNativeDeliveredStableProjectionByPhysicalShape(current, deliveredCount); ok {
		m.nativeDeliveredStableProjection = reprojected
	}
}

func (m *uiModel) reprojectNativeDeliveredStableProjectionPrefix(current tui.TranscriptProjection, deliveredCount int) (tui.TranscriptProjection, bool) {
	for idx := 0; idx < deliveredCount; idx++ {
		previousBlock := m.nativeDeliveredStableProjection.Blocks[idx]
		currentBlock := current.Blocks[idx]
		if !nativeStableProjectionBlocksSameReprojectIdentity(previousBlock, currentBlock) {
			return tui.TranscriptProjection{}, false
		}
	}
	reprojected := tui.TranscriptProjection{Blocks: make([]tui.TranscriptProjectionBlock, deliveredCount)}
	for idx := 0; idx < deliveredCount; idx++ {
		block := current.Blocks[idx]
		block.Lines = append([]string(nil), block.Lines...)
		reprojected.Blocks[idx] = block
	}
	return reprojected, true
}

func (m *uiModel) reprojectNativeDeliveredStableProjectionByPhysicalShape(current tui.TranscriptProjection, deliveredCount int) (tui.TranscriptProjection, bool) {
	used := make([]bool, len(current.Blocks))
	currentIndexes := make([]int, deliveredCount)
	reprojected := tui.TranscriptProjection{Blocks: make([]tui.TranscriptProjectionBlock, deliveredCount)}
	for deliveredIdx, deliveredBlock := range m.nativeDeliveredStableProjection.Blocks {
		currentIdx, ok := m.nativeStableCurrentBlockIndexForDeliveredShape(deliveredBlock, current.Blocks, used)
		if !ok {
			return tui.TranscriptProjection{}, false
		}
		used[currentIdx] = true
		currentIndexes[deliveredIdx] = currentIdx
		block := current.Blocks[currentIdx]
		block.Lines = append([]string(nil), block.Lines...)
		reprojected.Blocks[deliveredIdx] = block
	}
	if !nativeStableReprojectIndexesPreserveAllowedPhysicalOrder(reprojected.Blocks, currentIndexes) {
		return tui.TranscriptProjection{}, false
	}
	return reprojected, true
}

func (m *uiModel) nativeStableCurrentBlockIndexForDeliveredShape(delivered tui.TranscriptProjectionBlock, current []tui.TranscriptProjectionBlock, used []bool) (int, bool) {
	deliveredIdentityKey := nativeStableProjectionBlockIdentityKey(delivered)
	candidates := make([]int, 0, 1)
	for idx, block := range current {
		if used[idx] || delivered.Role != block.Role || delivered.DividerGroup != block.DividerGroup {
			continue
		}
		if deliveredIdentityKey == nativeStableProjectionBlockIdentityKey(block) {
			candidates = append(candidates, idx)
		}
	}
	if len(candidates) == 1 {
		return candidates[0], true
	}
	return 0, false
}

func nativeStableReprojectIndexesPreserveAllowedPhysicalOrder(blocks []tui.TranscriptProjectionBlock, currentIndexes []int) bool {
	normalized := make([]int, 0, len(currentIndexes))
	for idx := 0; idx < len(currentIndexes); {
		if nativeStableProjectionBlockCanFinalizeAssistantStream(blocks[idx]) {
			next := idx + 1
			for next < len(currentIndexes) && nativeStablePreviouslyLocalAppendOnlyBlock(blocks[next]) && currentIndexes[next] < currentIndexes[idx] {
				next++
			}
			if next > idx+1 {
				systemIndexes := currentIndexes[idx+1 : next]
				if !nativeStableIndexesStrictlyIncreasing(systemIndexes) ||
					systemIndexes[0] != currentIndexes[idx]-len(systemIndexes) ||
					systemIndexes[len(systemIndexes)-1]+1 != currentIndexes[idx] {
					return false
				}
				normalized = append(normalized, systemIndexes...)
				normalized = append(normalized, currentIndexes[idx])
				idx = next
				continue
			}
		}
		normalized = append(normalized, currentIndexes[idx])
		idx++
	}
	return nativeStableIndexesStrictlyIncreasing(normalized)
}

func nativeStableIndexesStrictlyIncreasing(indexes []int) bool {
	for idx := 1; idx < len(indexes); idx++ {
		if indexes[idx] <= indexes[idx-1] {
			return false
		}
	}
	return true
}

func nativeStableProjectionBlocksSameReprojectIdentity(left tui.TranscriptProjectionBlock, right tui.TranscriptProjectionBlock) bool {
	return left.Role == right.Role &&
		left.DividerGroup == right.DividerGroup &&
		nativeStableProjectionBlockIdentityKey(left) == nativeStableProjectionBlockIdentityKey(right)
}

func nativeStableProjectionBlockIdentityKey(block tui.TranscriptProjectionBlock) string {
	if block.SourceKey != "" {
		return "source\x00" + block.SourceKey
	}
	parts := make([]string, 0, len(block.Lines))
	for _, line := range block.Lines {
		parts = append(parts, xansi.Strip(line))
	}
	return "lines\x00" + strings.Join(parts, "\x00")
}
