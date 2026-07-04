package app

func testActiveAsk(m *uiModel) *askEvent {
	if m == nil {
		return nil
	}
	return m.ask.current
}

func testSetActiveAsk(m *uiModel, event *askEvent) {
	if m == nil {
		return
	}
	m.ask.currentToken = nextNonZeroToken(m.ask.currentToken)
	m.ask.current = event
	if event != nil {
		m.setInputMode(uiInputModeAsk)
		return
	}
	m.restorePrimaryInputMode()
}

func testAskFreeform(m *uiModel) bool {
	if m == nil {
		return false
	}
	return m.ask.freeform
}

func testAskCursor(m *uiModel) int {
	if m == nil {
		return 0
	}
	return m.ask.cursor
}

func testAskInput(m *uiModel) string {
	if m == nil {
		return ""
	}
	return m.ask.input
}

func testSetAskInput(m *uiModel, input string) {
	if m == nil {
		return
	}
	m.ask.input = input
}

func testAskInputCursor(m *uiModel) int {
	if m == nil {
		return 0
	}
	return m.ask.inputCursor
}

func testSetAskInputCursor(m *uiModel, cursor int) {
	if m == nil {
		return
	}
	m.ask.inputCursor = cursor
}

func testAskQueue(m *uiModel) []askEvent {
	if m == nil {
		return nil
	}
	return m.ask.queue
}

func testProcessListOpen(m *uiModel) bool {
	if m == nil {
		return false
	}
	return m.processList.open
}

func testProcessListSurfaceActive(m *uiModel) bool {
	if m == nil {
		return false
	}
	return m.surface() == uiSurfaceProcessList
}
