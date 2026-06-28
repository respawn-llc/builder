package scrollback

import (
	"fmt"
	"io"
	"runtime/debug"
	"strings"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

type NativeLiveArea interface {
	Render(frame NativeLiveAreaFrame) error
}

type NativeLiveAreaFrame struct {
	Lines  []string
	Cursor NativeLiveAreaCursor
}

type NativeLiveAreaCursor struct {
	Visible bool
	Row     int
	Col     int
}

type NativeLiveAreaImpl struct {
	buffer                *OngoingScrollbackBufferImpl
	terminalWidth         int
	terminalHeight        int
	frame                 NativeLiveAreaFrame
	renderedLines         int
	cursorPlaced          bool
	placedCursor          NativeLiveAreaCursor
	streamAnchored        bool
	pendingPhysicalRender bool
}

func NewNativeLiveAreaImpl(buffer *OngoingScrollbackBufferImpl, terminalWidth int, terminalHeight int) *NativeLiveAreaImpl {
	if buffer == nil {
		panicLiveAreaInvariant("NewNativeLiveAreaImpl", "stable buffer is required", NativeLiveAreaFrame{}, terminalWidth, terminalHeight)
	}
	if terminalWidth <= 0 || terminalHeight <= 0 {
		panicLiveAreaInvariant("NewNativeLiveAreaImpl", "terminal dimensions must be positive", NativeLiveAreaFrame{}, terminalWidth, terminalHeight)
	}
	if terminalWidth != buffer.terminalWidth || terminalHeight != buffer.terminalHeight {
		panicLiveAreaInvariant("NewNativeLiveAreaImpl", "live area terminal dimensions must match stable buffer dimensions", NativeLiveAreaFrame{}, terminalWidth, terminalHeight)
	}
	liveArea := &NativeLiveAreaImpl{
		buffer:         buffer,
		terminalWidth:  terminalWidth,
		terminalHeight: terminalHeight,
	}
	buffer.attachLiveArea(liveArea)
	return liveArea
}

func (area *NativeLiveAreaImpl) Render(frame NativeLiveAreaFrame) error {
	area.validateFrameBeforeLock("render", frame)
	nextFrame := copyNativeLiveAreaFrame(frame)

	area.buffer.mu.Lock()

	if nativeLiveAreaFramesEqual(area.frame, nextFrame) && !area.pendingPhysicalRender && len(area.buffer.heldStableOps) == 0 {
		area.buffer.mu.Unlock()
		return nil
	}
	area.frame = nextFrame
	area.pendingPhysicalRender = true
	if !area.buffer.normalBufferAvailableLocked() {
		area.buffer.mu.Unlock()
		return nil
	}
	if len(area.buffer.heldStableOps) > 0 {
		_, err := area.buffer.flushHoldoffLocked()
		if err != nil {
			area.buffer.mu.Unlock()
			area.buffer.notifyDelayedWriteError(err)
			return nil
		}
		if !area.buffer.isStreaming {
			area.buffer.mu.Unlock()
			return nil
		}
	}
	if area.buffer.isStreaming {
		if err := area.erasePhysicalLocked(); err != nil {
			area.buffer.mu.Unlock()
			return err
		}
		err := area.renderPhysicalDuringAssistantStreamLocked()
		area.buffer.mu.Unlock()
		return err
	}
	if err := area.erasePhysicalLocked(); err != nil {
		area.buffer.mu.Unlock()
		return err
	}
	err := area.renderPhysicalLocked()
	area.buffer.mu.Unlock()
	return err
}

func (area *NativeLiveAreaImpl) erasePhysicalLocked() error {
	if area == nil || area.renderedLines == 0 {
		return nil
	}
	sequence := liveAreaCursorRestoreAnchorSequence(area.cursorPlaced, area.placedCursor, area.renderedLines) + liveAreaEraseSequence(area.renderedLines)
	if area.streamAnchored {
		sequence = area.streamAnchoredLiveAreaEraseSequence(area.renderedLines)
	}
	written, err := io.WriteString(area.buffer.stableWriter, sequence)
	if err != nil {
		return fmt.Errorf("erase live area failed: %s: %w", liveAreaWriteDiagnostics(sequence, area.terminalWidth, area.terminalHeight, written), err)
	}
	if written != len(sequence) {
		return fmt.Errorf("erase live area short write: %s: %w", liveAreaWriteDiagnostics(sequence, area.terminalWidth, area.terminalHeight, written), io.ErrShortWrite)
	}
	area.renderedLines = 0
	area.cursorPlaced = false
	area.placedCursor = NativeLiveAreaCursor{}
	area.streamAnchored = false
	return nil
}

func (area *NativeLiveAreaImpl) renderPhysicalLocked() error {
	if area == nil || len(area.frame.Lines) == 0 {
		return nil
	}
	payload := strings.Join(area.frame.Lines, terminalLineBreak) + liveAreaCursorPlacementSequence(area.frame.Cursor, len(area.frame.Lines))
	written, err := io.WriteString(area.buffer.stableWriter, payload)
	if err != nil {
		return fmt.Errorf("render live area failed: %s: %w", liveAreaWriteDiagnostics(payload, area.terminalWidth, area.terminalHeight, written), err)
	}
	if written != len(payload) {
		return fmt.Errorf("render live area short write: %s: %w", liveAreaWriteDiagnostics(payload, area.terminalWidth, area.terminalHeight, written), io.ErrShortWrite)
	}
	area.renderedLines = len(area.frame.Lines)
	area.pendingPhysicalRender = false
	area.cursorPlaced = area.frame.Cursor.Visible
	if area.cursorPlaced {
		area.placedCursor = area.frame.Cursor
	} else {
		area.placedCursor = NativeLiveAreaCursor{}
	}
	area.streamAnchored = false
	return nil
}

func (area *NativeLiveAreaImpl) renderPhysicalDuringAssistantStreamLocked() error {
	if area == nil || len(area.frame.Lines) == 0 {
		return nil
	}
	payload := terminalSaveCursor + area.streamLiveAreaStartSequence(len(area.frame.Lines)) + strings.Join(area.frame.Lines, terminalLineBreak) + xansi.HideCursor + terminalRestoreCursor
	written, err := io.WriteString(area.buffer.stableWriter, payload)
	if err != nil {
		return fmt.Errorf("render live area during assistant stream failed: %s: %w", liveAreaWriteDiagnostics(payload, area.terminalWidth, area.terminalHeight, written), err)
	}
	if written != len(payload) {
		return fmt.Errorf("render live area during assistant stream short write: %s: %w", liveAreaWriteDiagnostics(payload, area.terminalWidth, area.terminalHeight, written), io.ErrShortWrite)
	}
	area.renderedLines = len(area.frame.Lines)
	area.pendingPhysicalRender = false
	area.cursorPlaced = false
	area.placedCursor = NativeLiveAreaCursor{}
	area.streamAnchored = true
	return nil
}

func (area *NativeLiveAreaImpl) validateFrameBeforeLock(operation string, frame NativeLiveAreaFrame) {
	if area == nil {
		panicLiveAreaInvariant(operation, "nil NativeLiveAreaImpl receiver", frame, 0, 0)
	}
	if area.buffer == nil {
		panicLiveAreaInvariant(operation, "stable buffer is required", frame, area.terminalWidth, area.terminalHeight)
	}
	if area.terminalWidth <= 0 || area.terminalHeight <= 0 {
		panicLiveAreaInvariant(operation, "terminal dimensions must be positive", frame, area.terminalWidth, area.terminalHeight)
	}
	if len(frame.Lines) == 0 {
		panicLiveAreaInvariant(operation, "live area content must not be empty", frame, area.terminalWidth, area.terminalHeight)
	}
	if len(frame.Lines) > area.terminalHeight {
		panicLiveAreaInvariant(operation, "live area content exceeds terminal height", frame, area.terminalWidth, area.terminalHeight)
	}
	for index, line := range frame.Lines {
		if strings.ContainsAny(line, "\r\n") {
			panicLiveAreaInvariant(operation, fmt.Sprintf("live area line %d contains CR or LF", index), frame, area.terminalWidth, area.terminalHeight)
		}
		if lipgloss.Width(line) > area.terminalWidth {
			panicLiveAreaInvariant(operation, fmt.Sprintf("live area line %d exceeds terminal width", index), frame, area.terminalWidth, area.terminalHeight)
		}
	}
	if !frame.Cursor.Visible {
		return
	}
	if frame.Cursor.Row < 0 || frame.Cursor.Row >= len(frame.Lines) {
		panicLiveAreaInvariant(operation, "live area cursor row is outside submitted frame", frame, area.terminalWidth, area.terminalHeight)
	}
	if frame.Cursor.Col < 0 || frame.Cursor.Col >= area.terminalWidth {
		panicLiveAreaInvariant(operation, "live area cursor column is outside terminal width", frame, area.terminalWidth, area.terminalHeight)
	}
}

func copyNativeLiveAreaFrame(frame NativeLiveAreaFrame) NativeLiveAreaFrame {
	return NativeLiveAreaFrame{
		Lines:  append([]string(nil), frame.Lines...),
		Cursor: frame.Cursor,
	}
}

func nativeLiveAreaFramesEqual(left NativeLiveAreaFrame, right NativeLiveAreaFrame) bool {
	if left.Cursor != right.Cursor || len(left.Lines) != len(right.Lines) {
		return false
	}
	for index := range left.Lines {
		if left.Lines[index] != right.Lines[index] {
			return false
		}
	}
	return true
}

func liveAreaEraseSequence(renderedLines int) string {
	if renderedLines <= 0 {
		return ""
	}
	var out strings.Builder
	if renderedLines > 1 {
		out.WriteString(xansi.CursorUp(renderedLines - 1))
	}
	out.WriteString("\r")
	for index := 0; index < renderedLines; index++ {
		if index > 0 {
			out.WriteString(xansi.CursorDown(1))
			out.WriteString("\r")
		}
		out.WriteString(xansi.EraseEntireLine)
	}
	if renderedLines > 1 {
		out.WriteString(xansi.CursorUp(renderedLines - 1))
	}
	out.WriteString("\r")
	return out.String()
}

func (area *NativeLiveAreaImpl) streamAnchoredLiveAreaEraseSequence(renderedLines int) string {
	if renderedLines <= 0 {
		return ""
	}
	return terminalSaveCursor + area.streamLiveAreaStartSequence(renderedLines) + liveAreaEraseForwardSequence(renderedLines) + terminalRestoreCursor
}

func (area *NativeLiveAreaImpl) streamLiveAreaStartSequence(renderedLines int) string {
	if area == nil {
		return streamLiveAreaStartSequence(renderedLines, 0)
	}
	return streamLiveAreaStartSequence(renderedLines, area.terminalHeight)
}

func streamLiveAreaStartSequence(renderedLines int, terminalHeight int) string {
	if renderedLines <= 0 {
		return ""
	}
	if terminalHeight <= 0 {
		return xansi.CursorDown(renderedLines) + "\r"
	}
	row := terminalHeight - renderedLines + 1
	if row < 1 {
		row = 1
	}
	return xansi.CursorPosition(1, row)
}

func liveAreaEraseForwardSequence(renderedLines int) string {
	if renderedLines <= 0 {
		return ""
	}
	var out strings.Builder
	for index := 0; index < renderedLines; index++ {
		if index > 0 {
			out.WriteString(xansi.CursorDown(1))
			out.WriteString("\r")
		}
		out.WriteString(xansi.EraseEntireLine)
	}
	return out.String()
}

func liveAreaCursorPlacementSequence(cursor NativeLiveAreaCursor, renderedLines int) string {
	if !cursor.Visible {
		return xansi.HideCursor
	}
	anchorRow := renderedLines - 1
	rowsUp := anchorRow - cursor.Row
	if rowsUp < 0 {
		rowsUp = 0
	}
	var out strings.Builder
	out.WriteString(xansi.ShowCursor)
	out.WriteString("\r")
	if rowsUp > 0 {
		out.WriteString(xansi.CursorUp(rowsUp))
	}
	if cursor.Col > 0 {
		out.WriteString(xansi.CursorForward(cursor.Col))
	}
	return out.String()
}

func liveAreaCursorRestoreAnchorSequence(cursorPlaced bool, cursor NativeLiveAreaCursor, renderedLines int) string {
	if !cursorPlaced {
		return ""
	}
	anchorRow := renderedLines - 1
	rowsDown := anchorRow - cursor.Row
	if rowsDown <= 0 {
		return "\r"
	}
	return xansi.CursorDown(rowsDown) + "\r"
}

func liveAreaWriteDiagnostics(payload string, terminalWidth int, terminalHeight int, written int) string {
	return fmt.Sprintf(
		"terminal_width=%d terminal_height=%d visual_width=%d byte_len=%d written=%d payload_quoted=%q payload_raw_hex=% x",
		terminalWidth,
		terminalHeight,
		lipgloss.Width(payload),
		len(payload),
		written,
		payload,
		[]byte(payload),
	)
}

const terminalSaveCursor = "\x1b7"
const terminalRestoreCursor = "\x1b8"

func panicLiveAreaInvariant(operation string, reason string, frame NativeLiveAreaFrame, terminalWidth int, terminalHeight int) {
	panic(fmt.Sprintf(
		"NativeLiveArea invariant violation\noperation=%s\nreason=%s\nterminal_width=%d\nterminal_height=%d\nline_count=%d\ncursor_visible=%t\ncursor_row=%d\ncursor_col=%d\nlines_quoted=%q\nlines_raw_hex=% x\nstack:\n%s",
		operation,
		reason,
		terminalWidth,
		terminalHeight,
		len(frame.Lines),
		frame.Cursor.Visible,
		frame.Cursor.Row,
		frame.Cursor.Col,
		frame.Lines,
		[]byte(strings.Join(frame.Lines, "\n")),
		debug.Stack(),
	))
}
