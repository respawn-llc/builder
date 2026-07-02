package app

import (
	"context"
	"errors"
	"io"
	"strings"

	"core/cli/tui"
	"core/cli/tui/scrollback"
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

const nativeLiveAssistantTailMaxLines = 8

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

func (s *uiNativeSurface) DiscardAssistantStreaming() error {
	if s == nil || s.surface == nil || !s.surface.AssistantStreaming() {
		return nil
	}
	return s.surface.DiscardAssistantStreaming()
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
	return m != nil &&
		m.nativeSurface != nil &&
		m.surface() == uiSurfaceOngoingTranscript &&
		m.nativeSurfaceGeometrySupported()
}

func (m *uiModel) nativeSurfaceGeometrySupported() bool {
	return m != nil && (!m.windowSizeKnown || m.termHeight > 1)
}

func (m *uiModel) dropNativeSurfaceIfGeometryUnsupported() {
	if m == nil || m.nativeSurface == nil || m.nativeSurfaceGeometrySupported() {
		return
	}
	m.nativeSurface.Drop()
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
	if height <= 1 {
		m.nativeSurface.Drop()
		return false
	}
	m.nativeSurface.assistantMarkdownRenderer = m.nativeAssistantMarkdownRenderer()
	if !m.nativeSurface.ensure(width, height) {
		return false
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
	m.nativePendingEmissions = nil
	m.nativeScratchHydrationPending = false
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
	m.nativePendingEmissions = nil
	m.nativeScratchHydrationPending = false
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
	l.model.forwardToView(tui.SetViewportSizeMsg{Lines: height, Width: width})
	spinner := pendingToolSpinnerFrame(l.model.spinnerFrame)
	lines := l.model.view.PendingOngoingLinesWithPendingSpinnerFrame(spinner)
	streamLines := l.model.view.StreamingOngoingLines()
	if l.model.nativeSurface.AssistantStreaming() {
		if err := l.model.nativeSurface.FlushHoldoff(); err != nil {
			l.model.nativeLiveAreaError = err
			l.model.logf("native.surface.flush_holdoff err=%q", err.Error())
		}
		if tailLines := l.model.nativeSurface.AssistantStreamTailLines(); len(tailLines) > 0 {
			streamLines = make([]tui.TranscriptProjectionLine, 0, len(tailLines))
			for _, line := range tailLines {
				streamLines = append(streamLines, tui.TranscriptProjectionLine{Kind: tui.VisibleLineContent, Text: line})
			}
		}
	}
	streamLines = tailProjectionLines(streamLines, min(height, nativeLiveAssistantTailMaxLines))
	if len(streamLines) > 0 {
		if len(lines) > 0 {
			lines = append(lines, tui.TranscriptProjectionLine{Kind: tui.VisibleLineDivider, Text: tui.TranscriptDivider})
		}
		lines = append(lines, streamLines...)
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

func tailProjectionLines(lines []tui.TranscriptProjectionLine, maxLines int) []tui.TranscriptProjectionLine {
	if maxLines <= 0 || len(lines) == 0 {
		return nil
	}
	if len(lines) <= maxLines {
		return append([]tui.TranscriptProjectionLine(nil), lines...)
	}
	return append([]tui.TranscriptProjectionLine(nil), lines[len(lines)-maxLines:]...)
}

func (m *uiModel) steerNativeProjectionLines(lines []tui.TranscriptProjectionLine) error {
	if len(lines) == 0 || m == nil || m.nativeSurface == nil {
		return nil
	}
	if !m.nativeSurface.initialized() {
		return nil
	}
	for _, line := range lines {
		if line.Kind == tui.VisibleLineDivider || strings.TrimSpace(line.Text) == "" {
			continue
		}
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
	lines := nativeLiveAreaFrameLines(frame)
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

func nativeLiveAreaFrameLines(frame uiRenderFrame) []string {
	lines := frame.renderLines()
	if len(lines) == 0 {
		lines = []string{""}
	}
	maxRows := frame.height - 1
	if maxRows < 1 {
		maxRows = 1
	}
	if len(lines) > maxRows {
		lines = lines[len(lines)-maxRows:]
	}
	return lines
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
