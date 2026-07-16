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

func testAskInputCursor(m *uiModel) int {
	if m == nil {
		return 0
	}
	return m.ask.inputCursor
}

func resolveAnsweredTestAskThroughTranscript(t *testing.T, m *uiModel) {
	t.Helper()
	active := testActiveAsk(m)
	if active == nil {
		t.Fatal("answered ask was resolved before the canonical transcript resolution")
	}
	if !m.ask.answerPending {
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
