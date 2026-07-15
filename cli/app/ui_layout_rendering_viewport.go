package app

import "core/cli/tui"

func (l uiViewLayout) effectiveWidth() int {
	if size := l.model.terminalGeometry.Size(); size != nil {
		return size.width
	}
	return 120
}

func (l uiViewLayout) effectiveHeight() int {
	if size := l.model.terminalGeometry.Size(); size != nil {
		return size.height
	}
	return 32
}

func (l uiViewLayout) calcChatLines() int {
	height := l.effectiveHeight()
	if l.model.surface() != uiSurfaceOngoingTranscript {
		chat := height - 1
		if chat < 1 {
			return 1
		}
		return chat
	}

	inputLines := l.inputPanelLineCount(l.effectiveWidth(), height)
	queuedLines := l.queuedPaneLineCount()
	pickerLines := l.model.activePickerPresentation().lineCount
	helpLines := len(l.renderHelpPane(l.effectiveWidth(), helpPaneMaxLines(height, inputLines, queuedLines, pickerLines), uiThemeStyles(l.model.theme)))
	chat := height - inputLines - queuedLines - pickerLines - helpLines - 1
	if chat < 1 {
		return 1
	}
	return chat
}

func (l uiViewLayout) syncViewport() {
	if l.model == nil {
		return
	}
	width := l.effectiveWidth()
	l.model.forwardToView(tui.SetViewportSizeMsg{
		Lines: l.calcChatLines(),
		Width: width,
	})
}

func (l uiViewLayout) shouldRenderSoftCursor() bool {
	inputState := l.model.inputModeState()
	return !l.shouldUseRealTerminalCursor() && inputState.ShowsMainInput
}

func (l uiViewLayout) shouldUseRealTerminalCursor() bool {
	return l.model.terminalCursor != nil
}
