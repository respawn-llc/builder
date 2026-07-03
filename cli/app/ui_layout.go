package app

import (
	"strings"
)

const (
	ansiHideCursor        = "\x1b[?25l"
	ansiClearLine         = "\x1b[2K"
	statusContextBarWidth = 10
	queuedMessagesLimit   = 5
)

type uiViewLayout struct {
	model *uiModel
}

type uiRenderFrame struct {
	width            int
	height           int
	chatPanel        []string
	pickerPane       []string
	queuePane        []string
	inputPane        []string
	helpPane         []string
	statusLine       string
	padToHeight      bool
	tailOnly         bool
	inputCursor      uiInputFieldCursor
	cursorFrameCount int
}

func (m *uiModel) View() string {
	defer m.enterUIMainThread("View")()
	m.syncRendererOutputGate()
	return m.layout().render()
}

func (m *uiModel) syncRendererOutputGate() {
	if m == nil || m.rendererOutputGate == nil {
		return
	}
	m.rendererOutputGate.SetSuppressRendererWrites(false)
}

func (l uiViewLayout) render() string {
	style := uiThemeStyles(l.model.theme)
	frame, ok := l.composeStandardFrame(style)
	if !ok {
		return ""
	}
	return l.renderFrame(frame)
}

func (l uiViewLayout) composeStandardFrame(style uiStyles) (uiRenderFrame, bool) {
	m := l.model
	width := l.effectiveWidth()
	height := l.effectiveHeight()
	if width <= 0 || height <= 0 {
		return uiRenderFrame{}, false
	}
	frame := uiRenderFrame{width: width, height: height, statusLine: l.renderStatusLine(width, style), padToHeight: true, tailOnly: true}
	if m.surface() == uiSurfaceOngoingTranscript {
		frame.inputPane = l.renderInputLines(width, style)
		frame.inputCursor = l.inputPaneCursor(width)
		frame.queuePane = l.renderQueuedMessagesPane(width)
		frame.pickerPane = l.renderActivePicker(width)
	}
	frame.helpPane = l.renderHelpPane(width, helpPaneMaxLines(height, len(frame.inputPane), len(frame.queuePane), len(frame.pickerPane)), style)
	chatLines := height - len(frame.inputPane) - len(frame.queuePane) - len(frame.pickerPane) - len(frame.helpPane) - 1
	if chatLines < 1 {
		chatLines = 1
	}
	frame.chatPanel = l.renderChatPanel(width, chatLines, style)
	if m.worktrees.inputCursor.Visible {
		frame.inputCursor = m.worktrees.inputCursor
	}
	return frame, true
}

func (l uiViewLayout) renderFrame(frame uiRenderFrame) string {
	l.updateTerminalCursor(frame)
	frame.cursorFrameCount = l.realCursorFrameCount(frame)
	return frame.renderWithCursorVisibility(!l.shouldShowRealTerminalCursor(frame))
}

func (f uiRenderFrame) renderLines() []string {
	allLines := make([]string, 0, f.height)
	allLines = append(allLines, f.chatPanel...)
	allLines = append(allLines, f.pickerPane...)
	allLines = append(allLines, f.queuePane...)
	allLines = append(allLines, f.helpPane...)
	allLines = append(allLines, f.inputPane...)
	if strings.TrimSpace(f.statusLine) != "" || f.height > 0 {
		allLines = append(allLines, f.statusLine)
	}
	if f.padToHeight {
		for len(allLines) < f.height {
			allLines = append(allLines, padRight("", f.width))
		}
	}
	if len(allLines) > f.height {
		if f.tailOnly {
			allLines = allLines[len(allLines)-f.height:]
		} else {
			allLines = allLines[:f.height]
		}
	}
	return allLines
}

func (f uiRenderFrame) renderWithCursorVisibility(hideCursor bool) string {
	rendered := strings.Join(f.renderLines(), "\n")
	if hideCursor {
		return rendered + ansiHideCursor
	}
	if f.cursorFrameCount > 0 {
		return rendered + realCursorFrameMarker(f.cursorFrameCount)
	}
	return rendered
}

func realCursorFrameMarker(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.Repeat("\x1b[m", count)
}

func (l uiViewLayout) realCursorFrameCount(frame uiRenderFrame) int {
	if !l.shouldShowRealTerminalCursor(frame) {
		return 0
	}
	row := frame.inputCursor.Row
	if !frame.inputCursor.Absolute {
		row = len(frame.chatPanel) + len(frame.pickerPane) + len(frame.queuePane) + len(frame.helpPane) + frame.inputCursor.Row
	}
	return max(0, row)*max(1, frame.width) + max(0, frame.inputCursor.Col) + 1
}

func (l uiViewLayout) shouldShowRealTerminalCursor(frame uiRenderFrame) bool {
	return l.model.terminalCursor != nil && frame.inputCursor.Visible
}

func (l uiViewLayout) updateTerminalCursor(frame uiRenderFrame) {
	state := l.model.terminalCursor
	if state == nil {
		return
	}
	cursor := frame.inputCursor
	if !cursor.Visible {
		state.Clear()
		return
	}
	absoluteRow := cursor.Row
	if !cursor.Absolute {
		absoluteRow = len(frame.chatPanel) + len(frame.pickerPane) + len(frame.queuePane) + len(frame.helpPane) + cursor.Row
	}
	lines := frame.renderLines()
	trimStart := 0
	totalBeforeTrim := len(frame.chatPanel) + len(frame.pickerPane) + len(frame.queuePane) + len(frame.helpPane) + len(frame.inputPane)
	if strings.TrimSpace(frame.statusLine) != "" || frame.height > 0 {
		totalBeforeTrim++
	}
	if totalBeforeTrim > len(lines) {
		trimStart = totalBeforeTrim - len(lines)
	}
	absoluteRow -= trimStart
	if absoluteRow < 0 || absoluteRow >= len(lines) {
		state.Clear()
		return
	}
	anchorRow := len(lines) - 1
	state.Set(uiTerminalCursorPlacement{
		Visible:   true,
		CursorRow: absoluteRow,
		CursorCol: cursor.Col,
		AnchorRow: anchorRow,
		AltScreen: l.model.altScreenActive,
	})
}
