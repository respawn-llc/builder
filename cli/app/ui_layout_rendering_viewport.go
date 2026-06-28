package app

import "core/cli/tui"

func (l uiViewLayout) effectiveWidth() int {
	m := l.model
	if m.termWidth > 0 {
		return m.termWidth
	}
	return 120
}

func (l uiViewLayout) effectiveHeight() int {
	m := l.model
	if m.termHeight > 0 {
		return m.termHeight
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
	if l.model.nativeSurfaceConfigured() && (l.model.termWidth <= 0 || l.model.termHeight <= 0) {
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
	return !l.shouldUseRealTerminalCursor() && !inputState.InputLocked && inputState.ShowsMainInput
}

func (l uiViewLayout) shouldUseRealTerminalCursor() bool {
	return l.model.terminalCursor != nil || l.model.nativeSurfaceEnabled()
}
