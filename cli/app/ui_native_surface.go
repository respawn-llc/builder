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
)

type uiNativeSurface struct {
	writer                     io.Writer
	normalBufferAvailable      func() bool
	delayedWriteErrorListener  func(error)
	forceNormalBufferAvailable bool
	width                      int
	height                     int
	buffer                     *scrollback.OngoingScrollbackBufferImpl
	stable                     scrollback.NativeScrollbackBuffer
	live                       scrollback.NativeLiveArea
	lastFrame                  scrollback.NativeLiveAreaFrame
	lastFrameSet               bool
	assistantStreamActive      bool
}

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
	if s.buffer != nil && s.live != nil && s.width == width && s.height == height {
		return true
	}
	s.Close()
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
	)
	s.stable = s.buffer
	s.live = scrollback.NewNativeLiveAreaImpl(s.buffer, width, height)
	return true
}

func (s *uiNativeSurface) ready(width int, height int) bool {
	return s != nil && s.buffer != nil && s.live != nil && s.width == width && s.height == height
}

func (s *uiNativeSurface) Close() {
	if s == nil {
		return
	}
	s.clearLiveFrame()
	s.width = 0
	s.height = 0
	s.buffer = nil
	s.stable = nil
	s.live = nil
	s.lastFrame = scrollback.NativeLiveAreaFrame{}
	s.lastFrameSet = false
	s.assistantStreamActive = false
}

func (s *uiNativeSurface) Render(frame scrollback.NativeLiveAreaFrame) error {
	if s == nil || s.live == nil {
		return errors.New("native live area is not initialized")
	}
	if err := s.live.Render(frame); err != nil {
		return err
	}
	s.lastFrame = copyNativeLiveAreaFrameForApp(frame)
	s.lastFrameSet = true
	return nil
}

func (s *uiNativeSurface) StableBuffer() scrollback.NativeScrollbackBuffer {
	if s == nil || s.stable == nil {
		return nil
	}
	return s.stable
}

func (s *uiNativeSurface) StreamAssistantCommentaryContent(ansi string) error {
	if s == nil || s.stable == nil {
		return errors.New("native scrollback buffer is not initialized")
	}
	if err := s.stable.StreamMarkdownAssistantContent(ansi); err != nil {
		return err
	}
	s.assistantStreamActive = true
	return nil
}

func (s *uiNativeSurface) StreamAssistantFinalAnswerContent(ansi string) error {
	if s == nil || s.stable == nil {
		return errors.New("native scrollback buffer is not initialized")
	}
	if err := s.stable.StreamMarkdownAssistantContent(ansi); err != nil {
		return err
	}
	s.assistantStreamActive = true
	return nil
}

func (s *uiNativeSurface) FinishAssistantStreaming() error {
	if s == nil || !s.assistantStreamActive {
		return nil
	}
	s.assistantStreamActive = false
	if s.stable == nil {
		return nil
	}
	return s.stable.FinishAssistantStreaming()
}

func (s *uiNativeSurface) FlushHoldoff() error {
	if s == nil || s.live == nil || !s.lastFrameSet {
		return nil
	}
	return s.live.Render(s.lastFrame)
}

func (s *uiNativeSurface) AssistantStreaming() bool {
	return s != nil && s.assistantStreamActive
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
	if s == nil || s.live == nil {
		return
	}
	s.forceNormalBufferAvailable = true
	err := s.live.Render(scrollback.NativeLiveAreaFrame{Lines: []string{""}})
	s.forceNormalBufferAvailable = false
	if err != nil && s.delayedWriteErrorListener != nil {
		s.live = nil
		s.buffer = nil
		s.stable = nil
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
	shouldRehydrate := !m.nativeResizeRehydrateScheduled()
	recreate := !m.nativeSurface.ready(width, height)
	wasInitialized := m.nativeSurface.StableBuffer() != nil
	if !m.nativeSurface.ensure(width, height) {
		return false
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

func (m *uiModel) nativeResizeRehydrateScheduled() bool {
	return m != nil && m.nativeResizeRehydrateToken != 0
}

func (m *uiModel) nativeResizeRehydratePending() bool {
	return m != nil && m.nativeResizeRehydrateToken != 0 && !m.nativeResizeRehydrateActive
}

func (m *uiModel) closeNativeSurface() {
	if m == nil || m.nativeSurface == nil {
		return
	}
	m.nativeSurface.Close()
	m.nativeSurface = nil
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
		lines = l.model.view.PendingOngoingLinesWithPendingSpinnerFrame(spinner)
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

func (m *uiModel) nativeCommittedProjectionForEntries(entries []tui.TranscriptEntry) tui.TranscriptProjection {
	if m == nil {
		return tui.TranscriptProjection{}
	}
	return m.view.CommittedOngoingProjectionForEntries(committedTranscriptEntriesForApp(entries))
}

func (m *uiModel) rehydrateNativeStableFromCurrentTranscript() error {
	if m == nil || m.nativeSurface == nil || m.nativeSurface.StableBuffer() == nil {
		return nil
	}
	projection := m.nativeCommittedProjectionForEntries(m.transcriptEntries)
	return m.steerNativeProjectionLines(projection.Lines(tui.TranscriptDivider))
}

func (m *uiModel) steerNativeStableAppend(previous tui.TranscriptProjection, current tui.TranscriptProjection) error {
	if m == nil || m.nativeSurface == nil || m.nativeSurface.StableBuffer() == nil {
		return nil
	}
	if current.Empty() {
		return nil
	}
	if previous.Empty() {
		return m.steerNativeProjectionLines(current.Lines(tui.TranscriptDivider))
	}
	if _, ok := current.RenderAppendDeltaFrom(previous, tui.TranscriptDivider); !ok {
		return m.nativeStableProjectionInvariantError("steerNativeStableAppend", previous, current)
	}
	return m.steerNativeProjectionLines(current.LinesFromBlock(len(previous.Blocks), tui.TranscriptDivider))
}

func (m *uiModel) steerNativeStableAppendFromBlock(current tui.TranscriptProjection, startBlock int) error {
	if m == nil || m.nativeSurface == nil || m.nativeSurface.StableBuffer() == nil {
		return nil
	}
	if current.Empty() || startBlock >= len(current.Blocks) {
		return nil
	}
	return m.steerNativeProjectionLines(current.LinesFromBlock(startBlock, tui.TranscriptDivider))
}

func nativeStableProjectionNeedsDelivery(previous tui.TranscriptProjection, current tui.TranscriptProjection) bool {
	if current.Empty() {
		return false
	}
	if previous.Empty() {
		return true
	}
	if _, ok := current.RenderAppendDeltaFrom(previous, tui.TranscriptDivider); !ok {
		return true
	}
	return len(current.Blocks) > len(previous.Blocks)
}

func (m *uiModel) nativeStableProjectionInvariantError(operation string, previous tui.TranscriptProjection, current tui.TranscriptProjection) error {
	const reason = "native stable append is not contiguous with current transcript projection"
	if m != nil && m.debugMode {
		panic(fmt.Sprintf(
			"Native scrollback invariant violation\noperation=%s\nreason=%s\nprevious_blocks=%d\ncurrent_blocks=%d\nprevious_empty=%t\ncurrent_empty=%t\nstack:\n%s",
			operation,
			reason,
			len(previous.Blocks),
			len(current.Blocks),
			previous.Empty(),
			current.Empty(),
			debug.Stack(),
		))
	}
	return errors.New(reason)
}

func (m *uiModel) steerNativeProjectionLines(lines []tui.TranscriptProjectionLine) error {
	if len(lines) == 0 || m == nil || m.nativeSurface == nil {
		return nil
	}
	stable := m.nativeSurface.StableBuffer()
	if stable == nil {
		return nil
	}
	for _, line := range lines {
		if err := stable.Steer(m.nativeStableProjectionLineText(line)); err != nil {
			return err
		}
	}
	return nil
}

func (m *uiModel) nativeStableProjectionLineText(line tui.TranscriptProjectionLine) string {
	if line.Kind != tui.VisibleLineDivider {
		return line.Text
	}
	width := 120
	theme := ""
	if m != nil {
		width = m.termWidth
		theme = m.theme
	}
	if width <= 0 {
		width = 120
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
