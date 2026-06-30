package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"strings"

	"core/cli/tui"
	"core/cli/tui/scrollback"
	"core/shared/clientui"

	xansi "github.com/charmbracelet/x/ansi"
)

type uiNativeSurface struct {
	writer                     io.Writer
	normalBufferAvailable      func() bool
	delayedWriteErrorListener  func(error)
	assistantMarkdownRenderer  scrollback.AssistantMarkdownRenderer
	forceNormalBufferAvailable bool
	width                      int
	height                     int
	buffer                     *scrollback.OngoingScrollbackBufferImpl
	surface                    scrollback.NativeOngoingSurface
	lastFrame                  scrollback.NativeLiveAreaFrame
	lastFrameSet               bool
}

var errNativeAssistantStreamResized = errors.New("native assistant stream resized before finalization")

func newUINativeSurface(writer io.Writer, normalBufferAvailable func() bool, delayedWriteErrorListener func(error)) *uiNativeSurface {
	if writer == nil {
		return nil
	}
	return &uiNativeSurface{
		writer:                    writer,
		normalBufferAvailable:     normalBufferAvailable,
		delayedWriteErrorListener: delayedWriteErrorListener,
	}
}

func (s *uiNativeSurface) ensure(width int, height int) bool {
	if s == nil || s.writer == nil || width <= 0 || height <= 0 {
		return false
	}
	if s.surface != nil && s.width == width && s.height == height {
		return true
	}
	s.Drop()
	s.width = width
	s.height = height
	s.buffer = scrollback.NewOngoingScrollbackBufferImpl(
		context.Background(),
		width,
		height,
		s.writer,
		nil,
		scrollback.WithNormalBufferAvailability(s.normalBufferAvailableForBuffer),
		scrollback.WithDelayedWriteErrorListener(s.delayedWriteErrorListener),
		scrollback.WithAssistantMarkdownRenderer(s.assistantMarkdownRenderer),
		scrollback.WithNormalBufferPreparation(),
	)
	s.surface = s.buffer
	return true
}

func (s *uiNativeSurface) ready(width int, height int) bool {
	return s != nil && s.surface != nil && s.width == width && s.height == height
}

func (s *uiNativeSurface) initialized() bool {
	return s != nil && s.surface != nil
}

func (s *uiNativeSurface) Close() {
	s.close(true)
}

func (s *uiNativeSurface) Drop() {
	s.close(false)
}

func (s *uiNativeSurface) close(clearLive bool) {
	if s == nil {
		return
	}
	if clearLive {
		s.clearLiveFrame()
	}
	s.width = 0
	s.height = 0
	s.buffer = nil
	s.surface = nil
	s.lastFrame = scrollback.NativeLiveAreaFrame{}
	s.lastFrameSet = false
}

func (s *uiNativeSurface) Render(frame scrollback.NativeLiveAreaFrame) error {
	if s == nil || s.surface == nil {
		return errors.New("native ongoing surface is not initialized")
	}
	if err := s.surface.RenderLive(frame); err != nil {
		return err
	}
	s.lastFrame = copyNativeLiveAreaFrameForApp(frame)
	s.lastFrameSet = true
	return nil
}

func (s *uiNativeSurface) StreamAssistantCommentaryContent(ansi string) error {
	if s == nil || s.surface == nil {
		return errors.New("native ongoing surface is not initialized")
	}
	return s.surface.StreamMarkdownAssistantContent(ansi)
}

func (s *uiNativeSurface) StreamAssistantFinalAnswerContent(ansi string) error {
	if s == nil || s.surface == nil {
		return errors.New("native ongoing surface is not initialized")
	}
	return s.surface.StreamMarkdownAssistantContent(ansi)
}

func (s *uiNativeSurface) FinishAssistantStreaming() error {
	if s == nil || s.surface == nil || !s.surface.AssistantStreaming() {
		return nil
	}
	return s.surface.FinishAssistantStreaming()
}

func (s *uiNativeSurface) FlushHoldoff() error {
	if s == nil || s.surface == nil {
		return nil
	}
	return s.surface.FlushHoldoff()
}

func (s *uiNativeSurface) InvalidateNormalBufferPreparation() {
	if s == nil || s.buffer == nil {
		return
	}
	scrollback.InvalidateNormalBufferPreparation(s.buffer)
}

func (s *uiNativeSurface) AssistantStreaming() bool {
	return s != nil && s.surface != nil && s.surface.AssistantStreaming()
}

func (s *uiNativeSurface) AssistantStreamTailLines() []string {
	if s == nil || s.surface == nil {
		return nil
	}
	return s.surface.AssistantStreamTailLines()
}

func (s *uiNativeSurface) normalBufferAvailableForBuffer() bool {
	if s == nil {
		return false
	}
	if s.forceNormalBufferAvailable {
		return true
	}
	if s.normalBufferAvailable == nil {
		return true
	}
	return s.normalBufferAvailable()
}

func (s *uiNativeSurface) clearLiveFrame() {
	if s == nil || s.surface == nil {
		return
	}
	s.forceNormalBufferAvailable = true
	err := s.surface.RenderLive(scrollback.NativeLiveAreaFrame{Lines: []string{""}})
	s.forceNormalBufferAvailable = false
	if err != nil && s.delayedWriteErrorListener != nil {
		s.buffer = nil
		s.surface = nil
		s.delayedWriteErrorListener(err)
	}
}

func copyNativeLiveAreaFrameForApp(frame scrollback.NativeLiveAreaFrame) scrollback.NativeLiveAreaFrame {
	return scrollback.NativeLiveAreaFrame{
		Lines:  append([]string(nil), frame.Lines...),
		Cursor: frame.Cursor,
	}
}

func (m *uiModel) nativeSurfaceEnabled() bool {
	return m != nil && m.nativeSurface != nil && m.surface() == uiSurfaceOngoingTranscript
}

func (m *uiModel) nativeSurfaceConfigured() bool {
	return m != nil && m.nativeSurface != nil
}

func (m *uiModel) nativeNormalBufferAvailable() bool {
	return m != nil &&
		m.nativeSurface != nil &&
		m.surface() == uiSurfaceOngoingTranscript &&
		!m.altScreenActive &&
		!m.nativePhysicalAltScreenActive() &&
		!m.nativeResizeRehydratePending()
}

func (m *uiModel) ensureNativeSurface(width int, height int) bool {
	if m == nil || m.nativeSurface == nil {
		return false
	}
	m.nativeSurface.assistantMarkdownRenderer = m.nativeAssistantMarkdownRenderer()
	shouldRehydrate := !m.nativeResizeRehydrateScheduled()
	recreate := !m.nativeSurface.ready(width, height)
	wasInitialized := m.nativeSurface.initialized()
	if !m.nativeSurface.ensure(width, height) {
		return false
	}
	if recreate && wasInitialized {
		m.reprojectNativeDeliveredStableProjectionForCurrentGeometry()
	}
	if recreate && shouldRehydrate && !wasInitialized {
		if err := m.rehydrateNativeStableFromCurrentTranscript(); err != nil {
			m.nativeLiveAreaError = err
			m.logf("native.stable.rehydrate err=%q", err.Error())
		} else {
			if strings.TrimSpace(m.view.OngoingStreamingText()) == "" {
				m.nativeAssistantStreamIncomplete = false
			}
		}
	}
	return true
}

func (m *uiModel) nativeAssistantMarkdownRenderer() scrollback.AssistantMarkdownRenderer {
	theme := ""
	if m != nil {
		theme = m.theme
	}
	return func(source string, width int) []string {
		rendered := tui.RenderAssistantMarkdownStreamingProjection(source, theme, width)
		if len(rendered) == 0 {
			return nil
		}
		lines := make([]string, 0, len(rendered))
		for _, line := range rendered {
			lines = append(lines, line.Text)
		}
		return lines
	}
}

func (m *uiModel) nativeResizeRehydrateScheduled() bool {
	return m != nil && m.nativeResizeRehydrateToken != 0
}

func (m *uiModel) nativeResizeRehydratePending() bool {
	return m != nil && m.nativeResizeRehydrateToken != 0 && !m.nativeResizeRehydrateActive
}

func (m *uiModel) nativeStableSurfaceReadyForCurrentGeometry() bool {
	return m != nil &&
		m.nativeSurface != nil &&
		m.nativeSurface.ready(m.termWidth, m.termHeight)
}

func (m *uiModel) ensureNativeStableSurfaceForCurrentGeometry() bool {
	if m == nil || m.nativeSurface == nil || m.termWidth <= 0 || m.termHeight <= 0 {
		return false
	}
	if m.nativeSurface.ready(m.termWidth, m.termHeight) {
		return true
	}
	return m.ensureNativeSurface(m.termWidth, m.termHeight)
}

func (m *uiModel) closeNativeSurface() {
	if m == nil || m.nativeSurface == nil {
		return
	}
	m.nativeSurface.Close()
	m.nativeSurface = nil
	m.nativeDeliveredStableProjection = tui.TranscriptProjection{}
	m.nativePendingStableIntent = nativeStableDeliveryIntent{}
	m.nativeAssistantStreamIncomplete = false
	m.nativeResizeRehydrateToken = 0
	m.nativeResizeRehydrateSettled = false
	m.nativeResizeRehydrateActive = false
	m.syncRendererOutputGate()
}

func (m *uiModel) dropNativeSurface() {
	if m == nil || m.nativeSurface == nil {
		return
	}
	m.nativeSurface.Drop()
	m.nativeSurface = nil
	m.nativeDeliveredStableProjection = tui.TranscriptProjection{}
	m.nativePendingStableIntent = nativeStableDeliveryIntent{}
	m.nativeAssistantStreamIncomplete = false
	m.nativeResizeRehydrateToken = 0
	m.nativeResizeRehydrateSettled = false
	m.nativeResizeRehydrateActive = false
	m.syncRendererOutputGate()
}

func (m *uiModel) nativePhysicalAltScreenActive() bool {
	return m != nil && m.rendererOutputGate != nil && m.rendererOutputGate.PhysicalAltScreenActive()
}

func (m *uiModel) flushNativeSurfaceHoldoff() error {
	if m == nil || m.nativeSurface == nil {
		return nil
	}
	return m.nativeSurface.FlushHoldoff()
}

func (m *uiModel) handleNativeDelayedWriteError(err error) {
	if m == nil || err == nil {
		return
	}
	m.nativeLiveAreaError = err
	if m.nativeSurface != nil {
		m.closeNativeSurface()
	}
	m.logf("native.surface delayed_write err=%q", err.Error())
}

func (l uiViewLayout) renderNativeLiveChatPanel(width int, height int, style uiStyles) []string {
	if width < 1 || height <= 0 {
		return nil
	}
	spinner := pendingToolSpinnerFrame(l.model.spinnerFrame)
	lines := l.model.view.LiveOngoingLinesWithPendingSpinnerFrame(spinner)
	if l.model.nativeSurface.AssistantStreaming() {
		if err := l.model.nativeSurface.FlushHoldoff(); err != nil {
			l.model.nativeLiveAreaError = err
			l.model.logf("native.surface.flush_holdoff err=%q", err.Error())
		}
		lines = l.model.view.PendingOngoingLinesWithPendingSpinnerFrame(spinner)
		if tailLines := l.model.nativeSurface.AssistantStreamTailLines(); len(tailLines) > 0 {
			if len(lines) > 0 {
				lines = append(lines, tui.TranscriptProjectionLine{Kind: tui.VisibleLineDivider, Text: tui.TranscriptDivider})
			}
			for _, line := range tailLines {
				lines = append(lines, tui.TranscriptProjectionLine{Kind: tui.VisibleLineContent, Text: line})
			}
		}
	}
	if len(lines) == 0 {
		return nil
	}
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}
	rawLines := make([]string, 0, len(lines))
	lineKinds := make([]tui.VisibleLineKind, 0, len(lines))
	for _, line := range lines {
		rawLines = append(rawLines, line.Text)
		lineKinds = append(lineKinds, line.Kind)
	}
	return l.renderChatContentLines(rawLines, lineKinds, width, style)
}

func (m *uiModel) rehydrateNativeStableFromCurrentTranscript() error {
	if m == nil || m.nativeSurface == nil || !m.nativeSurface.initialized() {
		return nil
	}
	projection := m.nativeCommittedProjectionForEntries(m.transcriptEntries)
	if err := m.steerNativeProjectionLines(projection.Lines(tui.TranscriptDivider)); err != nil {
		return err
	}
	m.nativeDeliveredStableProjection = projection.Clone()
	return nil
}

func (m *uiModel) steerNativeStableAppend(previous tui.TranscriptProjection, current tui.TranscriptProjection) error {
	if m == nil || m.nativeSurface == nil || !m.nativeSurface.initialized() {
		return nil
	}
	if current.Empty() {
		return nil
	}
	if previous.Empty() {
		if err := m.steerNativeProjectionLines(current.Lines(tui.TranscriptDivider)); err != nil {
			return err
		}
		m.nativeDeliveredStableProjection = current.Clone()
		return nil
	}
	appendBlocks, ok := m.nativeStableAppendBlocksForProjectionChange(nativeStableLiveAppendIntent("steerNativeStableAppend"), previous, current)
	if !ok {
		return m.nativeStableProjectionInvariantError("steerNativeStableAppend", nativeStableProjectionNonContiguousReason, previous, current)
	}
	if err := m.steerNativeStableAppendBlocks(current, previous, appendBlocks); err != nil {
		return err
	}
	m.nativeDeliveredStableProjection = nativeStableProjectionWithAppendedBlocks(previous, current, appendBlocks)
	return nil
}

func (m *uiModel) steerNativeStableProjectionChange(operation string, previous tui.TranscriptProjection, current tui.TranscriptProjection) error {
	if m == nil || m.nativeSurface == nil || !m.nativeSurface.initialized() {
		return nil
	}
	if current.Empty() {
		return nil
	}
	if previous.Empty() {
		if err := m.steerNativeProjectionLines(current.Lines(tui.TranscriptDivider)); err != nil {
			return err
		}
		m.nativeDeliveredStableProjection = current.Clone()
		return nil
	}
	if appendBlocks, ok := m.nativeStableAppendBlocksForProjectionChange(nativeStableLiveAppendIntent(operation), previous, current); ok {
		if err := m.steerNativeStableAppendBlocks(current, previous, appendBlocks); err != nil {
			return err
		}
		m.nativeDeliveredStableProjection = nativeStableProjectionWithAppendedBlocks(previous, current, appendBlocks)
		return nil
	}
	return m.nativeStableProjectionInvariantError(operation, nativeStableProjectionNonContiguousReason, previous, current)
}

func (m *uiModel) steerNativeStableRuntimeProjectionChange(intent nativeStableDeliveryIntent, previous tui.TranscriptProjection, current tui.TranscriptProjection) error {
	if m == nil || m.nativeSurface == nil || !m.nativeSurface.initialized() {
		return nil
	}
	if current.Empty() {
		return nil
	}
	if previous.Empty() {
		if err := m.steerNativeProjectionLines(current.Lines(tui.TranscriptDivider)); err != nil {
			return err
		}
		m.nativeDeliveredStableProjection = current.Clone()
		return nil
	}
	if appendBlocks, ok := m.nativeStableAppendBlocksForProjectionChange(intent, previous, current); ok {
		if err := m.steerNativeStableAppendBlocks(current, previous, appendBlocks); err != nil {
			return err
		}
		m.nativeDeliveredStableProjection = nativeStableProjectionWithAppendedBlocks(previous, current, appendBlocks)
		return nil
	}
	return m.nativeStableProjectionDeliveryError(intent, nativeStableProjectionNonContiguousReason, previous, current)
}

func (m *uiModel) steerNativeStableAppendFromBlock(current tui.TranscriptProjection, startBlock int) error {
	if m == nil || m.nativeSurface == nil || !m.nativeSurface.initialized() {
		return nil
	}
	if current.Empty() || startBlock >= len(current.Blocks) {
		return nil
	}
	return m.steerNativeProjectionLines(current.LinesFromBlock(startBlock, tui.TranscriptDivider))
}

func (m *uiModel) steerNativeStableAppendBlocks(current tui.TranscriptProjection, previous tui.TranscriptProjection, blockIndexes []int) error {
	if m == nil || m.nativeSurface == nil || !m.nativeSurface.initialized() || len(blockIndexes) == 0 {
		return nil
	}
	lines := nativeStableAppendLinesForBlockIndexes(current, previous, blockIndexes)
	return m.steerNativeProjectionLines(lines)
}

func nativeStableProjectionWithAppendedBlocks(previous tui.TranscriptProjection, current tui.TranscriptProjection, blockIndexes []int) tui.TranscriptProjection {
	if previous.Empty() {
		return current.Clone()
	}
	if len(blockIndexes) == 0 {
		return previous.Clone()
	}
	out := previous.Clone()
	for _, blockIndex := range blockIndexes {
		if blockIndex < 0 || blockIndex >= len(current.Blocks) {
			continue
		}
		block := current.Blocks[blockIndex]
		block.Lines = append([]string(nil), block.Lines...)
		out.Blocks = append(out.Blocks, block)
	}
	return out
}

func (m *uiModel) nativeStableProjectionNeedsDelivery(intent nativeStableDeliveryIntent, previous tui.TranscriptProjection, current tui.TranscriptProjection) bool {
	if current.Empty() {
		return false
	}
	if previous.Empty() {
		return true
	}
	appendBlocks, ok := m.nativeStableAppendBlocksForProjectionChange(intent, previous, current)
	if !ok {
		return true
	}
	return len(appendBlocks) > 0
}

func (m *uiModel) nativeStableAppendBlocksForProjectionChange(intent nativeStableDeliveryIntent, previous tui.TranscriptProjection, current tui.TranscriptProjection) ([]int, bool) {
	if current.Empty() {
		return nil, true
	}
	if previous.Empty() {
		if !intent.allowCommittedStableAppend() {
			return nil, false
		}
		return nativeStableBlockIndexRange(0, len(current.Blocks)), true
	}
	if m.nativeStableProjectionHasAppendPrefix(previous, current) {
		if !intent.allowCommittedStableAppend() {
			if len(previous.Blocks) == len(current.Blocks) {
				return nil, true
			}
			return nil, false
		}
		return nativeStableBlockIndexRange(len(previous.Blocks), len(current.Blocks)), true
	}
	if intent.allowRecentTailOverlapAppend() {
		if overlap := m.nativeStableSharedSuffixPrefixBlockCount(current, previous); overlap > 0 &&
			!m.nativeStableOverlapAppendWouldReplayDeliveredPrefix(previous, current, overlap) {
			return nativeStableBlockIndexRange(overlap, len(current.Blocks)), true
		}
	}
	if intent.allowCompactionEpochTransition() && nativeStableProjectionStartsCompactionReset(current) && !m.nativeStableProjectionContainsReprojectIdentity(previous.Blocks, current.Blocks[0]) {
		return nativeStableBlockIndexRange(0, len(current.Blocks)), true
	}
	if intent.allowLocalAppendOnlyReconciliation() {
		if blockIndexes, ok := m.nativeStableAppendBlockIndexesForLocalReconciliation(previous, current); ok {
			return blockIndexes, true
		}
	}
	return nil, false
}

func nativeStableProjectionStartsCompactionReset(projection tui.TranscriptProjection) bool {
	if len(projection.Blocks) == 0 {
		return false
	}
	switch projection.Blocks[0].Role {
	case tui.RenderIntentCompactionSummary, tui.RenderIntentManualCompactionCarryover:
		return true
	default:
		return false
	}
}

func nativeStableBlockIndexRange(start int, end int) []int {
	if start < 0 {
		start = 0
	}
	if end <= start {
		return nil
	}
	indexes := make([]int, 0, end-start)
	for idx := start; idx < end; idx++ {
		indexes = append(indexes, idx)
	}
	return indexes
}

func (m *uiModel) nativeStableProjectionHasAppendPrefix(previous tui.TranscriptProjection, current tui.TranscriptProjection) bool {
	if len(previous.Blocks) > len(current.Blocks) {
		return false
	}
	for idx, block := range previous.Blocks {
		if !m.nativeStableProjectionBlocksEqual(block, current.Blocks[idx]) {
			return false
		}
	}
	return true
}

func (m *uiModel) nativeStableSharedPrefixBlockCount(current tui.TranscriptProjection, previous tui.TranscriptProjection) int {
	limit := min(len(current.Blocks), len(previous.Blocks))
	for idx := 0; idx < limit; idx++ {
		if !m.nativeStableProjectionBlocksEqual(current.Blocks[idx], previous.Blocks[idx]) {
			return idx
		}
	}
	return limit
}

func (m *uiModel) nativeStableSharedSuffixPrefixBlockCount(current tui.TranscriptProjection, previous tui.TranscriptProjection) int {
	limit := min(len(current.Blocks), len(previous.Blocks))
	for overlap := limit; overlap > 0; overlap-- {
		start := len(previous.Blocks) - overlap
		matches := true
		for idx := 0; idx < overlap; idx++ {
			if !m.nativeStableProjectionBlocksEqual(current.Blocks[idx], previous.Blocks[start+idx]) {
				matches = false
				break
			}
		}
		if matches {
			return overlap
		}
	}
	return 0
}

func (m *uiModel) nativeStableOverlapAppendWouldReplayDeliveredPrefix(previous tui.TranscriptProjection, current tui.TranscriptProjection, overlap int) bool {
	if overlap <= 0 || overlap > len(previous.Blocks) || overlap > len(current.Blocks) {
		return false
	}
	deliveredPrefix := previous.Blocks[:len(previous.Blocks)-overlap]
	for _, block := range current.Blocks[overlap:] {
		if m.nativeStableProjectionContainsDeliveredIdentity(deliveredPrefix, block) {
			return true
		}
	}
	return false
}

func (m *uiModel) nativeStableProjectionBlocksEqual(left tui.TranscriptProjectionBlock, right tui.TranscriptProjectionBlock) bool {
	if (nativeStablePreviouslyLocalAppendOnlyBlock(left) || nativeStableCurrentLocalAppendOnlyBlock(right)) && (left.SourceKey != "" || right.SourceKey != "") {
		return nativeStableProjectionBlocksSameReprojectIdentity(left, right)
	}
	if left.Role != right.Role || left.DividerGroup != right.DividerGroup || len(left.Lines) != len(right.Lines) {
		return false
	}
	for idx := range left.Lines {
		if m.nativeStableContentLineText(left.Lines[idx]) != m.nativeStableContentLineText(right.Lines[idx]) {
			return false
		}
	}
	return true
}

func (m *uiModel) nativeStableContentLineText(text string) string {
	return m.nativeStableProjectionLineText(tui.TranscriptProjectionLine{Kind: tui.VisibleLineContent, Text: text})
}

func (m *uiModel) nativeStableAppendBlockIndexesForLocalReconciliation(previous tui.TranscriptProjection, current tui.TranscriptProjection) ([]int, bool) {
	prefix := m.nativeStableSharedPrefixBlockCount(current, previous)
	previousSuffixIsLocal := prefix < len(previous.Blocks)
	for _, block := range previous.Blocks[prefix:] {
		if !nativeStablePreviouslyLocalAppendOnlyBlock(block) {
			previousSuffixIsLocal = false
			break
		}
	}
	if previousSuffixIsLocal {
		blockIndexes := make([]int, 0, len(current.Blocks)-prefix)
		for idx := prefix; idx < len(current.Blocks); idx++ {
			block := current.Blocks[idx]
			if nativeStableCurrentLocalAppendOnlyBlock(block) && m.nativeStableProjectionContainsLocalAppendOnlyIdentity(previous.Blocks[prefix:], block) {
				continue
			}
			blockIndexes = append(blockIndexes, idx)
		}
		return blockIndexes, true
	}
	matchedPrevious := 0
	blockIndexes := make([]int, 0)
	for idx, block := range current.Blocks {
		if matchedPrevious < len(previous.Blocks) && m.nativeStableProjectionBlocksEqual(previous.Blocks[matchedPrevious], block) {
			matchedPrevious++
			continue
		}
		if nativeStableCurrentLocalAppendOnlyBlock(block) {
			if m.nativeStableProjectionContainsLocalAppendOnlyIdentity(previous.Blocks, block) {
				continue
			}
			blockIndexes = append(blockIndexes, idx)
			continue
		}
		if matchedPrevious < len(previous.Blocks) {
			matchedPrevious = m.nativeStableSkipDeliveredLocalBlocksPresentInCurrent(previous.Blocks, current.Blocks, matchedPrevious)
			if matchedPrevious < len(previous.Blocks) && m.nativeStableProjectionBlocksEqual(previous.Blocks[matchedPrevious], block) {
				matchedPrevious++
				continue
			}
			if matchedPrevious < len(previous.Blocks) {
				return nil, false
			}
		}
		blockIndexes = append(blockIndexes, idx)
	}
	matchedPrevious = m.nativeStableSkipDeliveredLocalBlocksPresentInCurrent(previous.Blocks, current.Blocks, matchedPrevious)
	if matchedPrevious != len(previous.Blocks) {
		return nil, false
	}
	return blockIndexes, true
}

func (m *uiModel) nativeStableSkipDeliveredLocalBlocksPresentInCurrent(previous []tui.TranscriptProjectionBlock, current []tui.TranscriptProjectionBlock, start int) int {
	for start < len(previous) {
		block := previous[start]
		if !nativeStablePreviouslyLocalAppendOnlyBlock(block) {
			return start
		}
		start++
	}
	return start
}

func (m *uiModel) nativeStableProjectionContainsBlock(blocks []tui.TranscriptProjectionBlock, target tui.TranscriptProjectionBlock) bool {
	for _, block := range blocks {
		if m.nativeStableProjectionBlocksEqual(block, target) {
			return true
		}
	}
	return false
}

func (m *uiModel) nativeStableProjectionContainsDeliveredIdentity(blocks []tui.TranscriptProjectionBlock, target tui.TranscriptProjectionBlock) bool {
	for _, block := range blocks {
		if block.SourceKey != "" || target.SourceKey != "" {
			if nativeStableProjectionBlocksSameReprojectIdentity(block, target) {
				return true
			}
			continue
		}
		if m.nativeStableProjectionBlocksEqual(block, target) {
			return true
		}
	}
	return false
}

func (m *uiModel) nativeStableProjectionContainsLocalAppendOnlyIdentity(blocks []tui.TranscriptProjectionBlock, target tui.TranscriptProjectionBlock) bool {
	for _, block := range blocks {
		if block.SourceKey != "" || target.SourceKey != "" {
			if nativeStableProjectionBlocksSameReprojectIdentity(block, target) {
				return true
			}
			continue
		}
		if m.nativeStableProjectionBlocksEqual(block, target) {
			return true
		}
	}
	return false
}

func (m *uiModel) nativeStableProjectionContainsReprojectIdentity(blocks []tui.TranscriptProjectionBlock, target tui.TranscriptProjectionBlock) bool {
	for _, block := range blocks {
		if nativeStableProjectionBlocksSameReprojectIdentity(block, target) {
			return true
		}
	}
	return false
}

func nativeStableAppendLinesForBlockIndexes(current tui.TranscriptProjection, previous tui.TranscriptProjection, blockIndexes []int) []tui.TranscriptProjectionLine {
	if len(blockIndexes) == 0 {
		return nil
	}
	lines := make([]tui.TranscriptProjectionLine, 0, len(blockIndexes)*2)
	lastGroup := ""
	haveLastGroup := false
	if len(previous.Blocks) > 0 {
		lastGroup = previous.Blocks[len(previous.Blocks)-1].DividerGroup
		haveLastGroup = true
	}
	for _, blockIndex := range blockIndexes {
		if blockIndex < 0 || blockIndex >= len(current.Blocks) {
			continue
		}
		block := current.Blocks[blockIndex]
		if haveLastGroup && lastGroup != block.DividerGroup {
			lines = append(lines, tui.TranscriptProjectionLine{Kind: tui.VisibleLineDivider, Text: tui.TranscriptDivider})
		}
		for _, line := range block.Lines {
			lines = append(lines, tui.TranscriptProjectionLine{Kind: tui.VisibleLineContent, Text: line})
		}
		lastGroup = block.DividerGroup
		haveLastGroup = true
	}
	return lines
}

func nativeStablePreviouslyLocalAppendOnlyBlock(block tui.TranscriptProjectionBlock) bool {
	if block.LocalAppendOnly {
		return true
	}
	switch block.Role {
	case tui.RenderIntentSystem:
		return true
	default:
		return false
	}
}

func nativeStableCurrentLocalAppendOnlyBlock(block tui.TranscriptProjectionBlock) bool {
	if block.LocalAppendOnly {
		return true
	}
	switch block.Role {
	case tui.RenderIntentSystem:
		return true
	default:
		return nativeStablePreviouslyLocalAppendOnlyBlock(block)
	}
}

const nativeStableProjectionNonContiguousReason = "native stable append is not contiguous with current transcript projection"
const nativeStableProjectionActiveStreamMismatchReason = "native active assistant stream does not match committed transcript projection"

type nativeStableDeliverySource string

const (
	nativeStableDeliveryLiveAppend        nativeStableDeliverySource = "live_append"
	nativeStableDeliveryRecoveryReconcile nativeStableDeliverySource = "recovery_reconcile"
	nativeStableDeliveryGeometryReproject nativeStableDeliverySource = "geometry_reproject"
)

type nativeStableDeliveryIntent struct {
	operation string
	source    nativeStableDeliverySource
	policies  nativeStableDeliveryPolicies
}

type nativeStableDeliveryPolicies struct {
	committedStableAppend        bool
	recentTailOverlapAppend      bool
	compactionEpochTransition    bool
	localAppendOnlyReconcile     bool
	activeStreamFinalizeFromText bool
}

func nativeStableLiveAppendIntent(operation string) nativeStableDeliveryIntent {
	return nativeStableDeliveryIntent{
		operation: operation,
		source:    nativeStableDeliveryLiveAppend,
		policies: nativeStableDeliveryPolicies{
			committedStableAppend:        true,
			recentTailOverlapAppend:      true,
			compactionEpochTransition:    true,
			localAppendOnlyReconcile:     true,
			activeStreamFinalizeFromText: true,
		},
	}
}

func nativeStableRecoveryReconcileIntent(operation string) nativeStableDeliveryIntent {
	return nativeStableDeliveryIntent{
		operation: operation,
		source:    nativeStableDeliveryRecoveryReconcile,
		policies: nativeStableDeliveryPolicies{
			committedStableAppend:    true,
			recentTailOverlapAppend:  true,
			localAppendOnlyReconcile: true,
		},
	}
}

func nativeStableGeometryReprojectIntent(operation string) nativeStableDeliveryIntent {
	return nativeStableDeliveryIntent{
		operation: operation,
		source:    nativeStableDeliveryGeometryReproject,
	}
}

func nativeStableMergePendingDeliveryIntent(existing nativeStableDeliveryIntent, incoming nativeStableDeliveryIntent) nativeStableDeliveryIntent {
	if !existing.set() {
		return incoming
	}
	if !incoming.set() {
		return existing
	}
	if existing.source == nativeStableDeliveryRecoveryReconcile || incoming.source == nativeStableDeliveryRecoveryReconcile {
		return nativeStableRecoveryReconcileIntent(incoming.operationLabel())
	}
	if existing.source == nativeStableDeliveryGeometryReproject {
		return incoming
	}
	if incoming.source == nativeStableDeliveryGeometryReproject {
		return existing
	}
	return existing
}

func (intent nativeStableDeliveryIntent) set() bool {
	return intent.source != ""
}

func (intent nativeStableDeliveryIntent) operationLabel() string {
	if strings.TrimSpace(intent.operation) == "" {
		return "deliverNativeStableProjectionChange"
	}
	return intent.operation
}

func (intent nativeStableDeliveryIntent) debugInvariantViolation() bool {
	return intent.source == "" || intent.source == nativeStableDeliveryLiveAppend
}

func (intent nativeStableDeliveryIntent) allowCommittedStableAppend() bool {
	return intent.policies.committedStableAppend
}

func (intent nativeStableDeliveryIntent) allowRecentTailOverlapAppend() bool {
	return intent.policies.recentTailOverlapAppend
}

func (intent nativeStableDeliveryIntent) allowCompactionEpochTransition() bool {
	return intent.policies.compactionEpochTransition
}

func (intent nativeStableDeliveryIntent) allowLocalAppendOnlyReconciliation() bool {
	return intent.policies.localAppendOnlyReconcile
}

func (intent nativeStableDeliveryIntent) allowActiveStreamFinalizeFromText() bool {
	return intent.policies.activeStreamFinalizeFromText
}

func (m *uiModel) nativeStableProjectionDeliveryError(intent nativeStableDeliveryIntent, reason string, previous tui.TranscriptProjection, current tui.TranscriptProjection) error {
	if intent.debugInvariantViolation() {
		return m.nativeStableProjectionInvariantError(intent.operationLabel(), reason, previous, current)
	}
	return fmt.Errorf("%s: %s", intent.source, reason)
}

func (m *uiModel) nativeAssistantStreamMatchesProjectionBlock(streamText string, block tui.TranscriptProjectionBlock) bool {
	if streamText == "" {
		return false
	}
	if block.Role != tui.RenderIntentAssistant && block.Role != tui.RenderIntentAssistantCommentary {
		return false
	}
	phase := clientui.MessagePhaseFinal
	if block.Role == tui.RenderIntentAssistantCommentary {
		phase = clientui.MessagePhaseCommentary
	}
	projection := m.nativeCommittedProjectionForEntries([]tui.TranscriptEntry{{
		Role:      tui.TranscriptRoleAssistant,
		Text:      streamText,
		Committed: true,
		Phase:     phase,
	}})
	if len(projection.Blocks) != 1 {
		return false
	}
	return m.nativeStableProjectionBlocksEqual(projection.Blocks[0], block)
}

func (m *uiModel) nativeStableProjectionInvariantError(operation string, reason string, previous tui.TranscriptProjection, current tui.TranscriptProjection) error {
	if m != nil && m.debugMode {
		panic(fmt.Sprintf(
			"Native scrollback invariant violation\noperation=%s\nreason=%s\nprevious_blocks=%d\ncurrent_blocks=%d\nprevious_empty=%t\ncurrent_empty=%t\nprevious_tail=%s\ncurrent_tail=%s\nstack:\n%s",
			operation,
			reason,
			len(previous.Blocks),
			len(current.Blocks),
			previous.Empty(),
			current.Empty(),
			nativeProjectionTailSummary(previous, 3),
			nativeProjectionTailSummary(current, 5),
			debug.Stack(),
		))
	}
	return errors.New(reason)
}

func nativeProjectionTailSummary(projection tui.TranscriptProjection, limit int) string {
	if limit <= 0 || len(projection.Blocks) == 0 {
		return "[]"
	}
	start := max(0, len(projection.Blocks)-limit)
	parts := make([]string, 0, len(projection.Blocks)-start)
	for idx := start; idx < len(projection.Blocks); idx++ {
		block := projection.Blocks[idx]
		line := ""
		if len(block.Lines) > 0 {
			line = block.Lines[0]
		}
		parts = append(parts, fmt.Sprintf(
			"{idx=%d role=%s group=%s lines=%d first=%q}",
			idx,
			block.Role,
			block.DividerGroup,
			len(block.Lines),
			xansi.Cut(line, 0, min(80, xansi.StringWidth(line))),
		))
	}
	return "[" + strings.Join(parts, " ") + "]"
}

func (m *uiModel) steerNativeProjectionLines(lines []tui.TranscriptProjectionLine) error {
	if len(lines) == 0 || m == nil || m.nativeSurface == nil {
		return nil
	}
	if !m.nativeSurface.initialized() {
		return nil
	}
	for _, line := range lines {
		if err := m.nativeSurface.surface.Steer(m.nativeStableProjectionLineText(line)); err != nil {
			return err
		}
	}
	return nil
}

func (m *uiModel) nativeStableProjectionLineText(line tui.TranscriptProjectionLine) string {
	width := 120
	theme := ""
	if m != nil {
		width = m.termWidth
		theme = m.theme
	}
	if width <= 0 {
		width = 120
	}
	if line.Kind != tui.VisibleLineDivider {
		return truncateANSIRight(line.Text, width)
	}
	return uiThemeStyles(theme).meta.Render(strings.Repeat("─", width))
}

func (l uiViewLayout) renderNativeLiveAreaFrame(frame uiRenderFrame) string {
	m := l.model
	if m == nil || !m.windowSizeKnown || m.termWidth <= 0 || m.termHeight <= 0 {
		return ""
	}
	if !m.ensureNativeSurface(frame.width, frame.height) {
		return frame.renderWithCursorVisibility(!l.shouldShowRealTerminalCursor(frame))
	}
	lines := frame.renderLines()
	if len(lines) == 0 {
		lines = []string{""}
	}
	nativeFrame := scrollback.NativeLiveAreaFrame{
		Lines:  lines,
		Cursor: l.nativeLiveAreaCursor(frame, lines),
	}
	if err := m.nativeSurface.Render(nativeFrame); err != nil {
		m.nativeLiveAreaError = err
		m.logf("native.live.render err=%q", err.Error())
		m.closeNativeSurface()
		fallbackFrame, ok := l.composeStandardFrame(uiThemeStyles(m.theme))
		if !ok {
			return ""
		}
		return l.renderFrame(fallbackFrame)
	}
	m.nativeLiveAreaError = nil
	return ""
}

func (l uiViewLayout) nativeLiveAreaCursor(frame uiRenderFrame, lines []string) scrollback.NativeLiveAreaCursor {
	if !l.shouldShowRealTerminalCursor(frame) {
		return scrollback.NativeLiveAreaCursor{}
	}
	cursor := frame.inputCursor
	absoluteRow := cursor.Row
	if !cursor.Absolute {
		absoluteRow = len(frame.chatPanel) + len(frame.pickerPane) + len(frame.queuePane) + len(frame.helpPane) + cursor.Row
	}
	totalBeforeTrim := len(frame.chatPanel) + len(frame.pickerPane) + len(frame.queuePane) + len(frame.helpPane) + len(frame.inputPane)
	if strings.TrimSpace(frame.statusLine) != "" || frame.height > 0 {
		totalBeforeTrim++
	}
	if totalBeforeTrim > len(lines) {
		absoluteRow -= totalBeforeTrim - len(lines)
	}
	if absoluteRow < 0 || absoluteRow >= len(lines) {
		return scrollback.NativeLiveAreaCursor{}
	}
	return scrollback.NativeLiveAreaCursor{
		Visible: true,
		Row:     absoluteRow,
		Col:     cursor.Col,
	}
}
