package app

import (
	"testing"

	"core/shared/clientui"
)

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
		testInstallCurrentAskProjection(m)
		m.setInputMode(uiInputModeAsk)
		return
	}
	m.ask.activeProjection = nil
	m.restorePrimaryInputMode()
}

func testInstallCurrentAskProjection(m *uiModel) {
	if m == nil || m.ask.current == nil {
		return
	}
	identity, ok := m.currentQuestionRenderIdentity()
	if !ok {
		identity = questionRenderIdentity{
			questionSource:   m.ask.current.prompt.Question,
			terminalWidth:    80,
			theme:            m.theme,
			linkPresentation: m.markdownLinks,
		}
	}
	result := projectAskQuestionMarkdown(questionRenderRequest{
		currentToken:   m.ask.currentToken,
		operationToken: 1,
		identity:       identity,
		questionSource: identity.questionSource,
	})
	m.ask.activeProjection = &activeQuestionProjection{
		renderedAt: identity,
		rows:       result.rows,
	}
	m.ask.inFlightProjection = nil
	m.ask.latestDesiredProjection = nil
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
	return m.ask.editor.Text()
}

func testAskInputCursor(m *uiModel) int {
	if m == nil {
		return 0
	}
	return runeOffsetForByteCursor(m.ask.editor.Text(), m.ask.editor.Cursor())
}

func testSetMainInput(m *uiModel, text string) {
	m.mainEditor.Replace(text)
	m.mainEditor.SetCursor(len(text))
}

func testSetMainInputAtRuneCursor(m *uiModel, text string, cursor int) {
	m.mainEditor.Replace(text)
	m.mainEditor.SetCursor(byteOffsetForRuneCursor(text, cursor))
}

func testMainInput(m *uiModel) string {
	return m.mainEditor.Text()
}

func testMainInputRuneCursor(m *uiModel) int {
	return runeOffsetForByteCursor(m.mainEditor.Text(), m.mainEditor.Cursor())
}

func testSetPromptHistorySelection(m *uiModel, selection int) {
	m.promptHistorySelection = &selection
}

func testSetAskInput(m *uiModel, text string) {
	m.ask.editor.Replace(text)
	m.ask.editor.SetCursor(len(text))
}

func testSetAskInputAtRuneCursor(m *uiModel, text string, cursor int) {
	m.ask.editor.Replace(text)
	m.ask.editor.SetCursor(byteOffsetForRuneCursor(text, cursor))
}

func testAskInputRuneCursor(m *uiModel) int {
	return runeOffsetForByteCursor(m.ask.editor.Text(), m.ask.editor.Cursor())
}

func testAskPaneContent(m *uiModel, width int) ([]uiInputPaneContentLine, *int) {
	height := 1
	if size := m.terminalGeometry.Size(); size != nil {
		height = size.height
	}
	viewport := m.layout().askInputViewport(width, inputContentLineLimit(height))
	if !viewport.cursor.Visible {
		return viewport.lines, nil
	}
	row := viewport.cursor.Row
	return viewport.lines, &row
}

func testVisibleAskPaneContent(m *uiModel, width int) ([]uiInputPaneContentLine, *int) {
	return testAskPaneContent(m, width)
}

func resolveAnsweredTestAskThroughTranscript(t *testing.T, m *uiModel) {
	t.Helper()
	active := testActiveAsk(m)
	if active == nil {
		t.Fatal("answered ask was resolved before the canonical transcript resolution")
	}
	if m.ask.activeDelivery == nil {
		t.Fatal("answered ask is not awaiting the canonical transcript resolution")
	}
	resolved := cloneTranscriptPromptForAsk(active.prompt)
	resolved.State = clientui.TranscriptPromptStateResolved
	message := clientui.TranscriptMessage{
		Sequence: 2,
		Kind:     clientui.TranscriptMessagePromptResolved,
		Payload: clientui.TranscriptPayload{
			PromptResolved: &resolved,
		},
	}
	if err := message.Validate(); err != nil {
		t.Fatalf("validate prompt resolution transcript message: %v", err)
	}
	if cmd := m.applyAdmittedTranscriptMessageState(message, runtimeTupleMergeResult{}); cmd != nil {
		t.Fatal("prompt resolution transcript message unexpectedly returned a command")
	}
}
