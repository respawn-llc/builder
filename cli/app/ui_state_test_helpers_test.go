package app

import (
	"cmp"
	"slices"
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
	return m.layout().askInputPaneContent(width)
}

func testVisibleAskPaneContent(m *uiModel, width int) ([]uiInputPaneContentLine, *int) {
	content, cursor := testAskPaneContent(m, width)
	height := 1
	if size := m.terminalGeometry.Size(); size != nil {
		height = size.height
	}
	maxLines := inputContentLineLimit(height)
	start := cursorAwareInputPaneViewportStart(len(content), maxLines, cursor)
	end := min(len(content), start+maxLines)
	if cursor == nil {
		return content[start:end], nil
	}
	visibleCursor := *cursor - start
	if visibleCursor < 0 || visibleCursor >= end-start {
		return content[start:end], nil
	}
	return content[start:end], &visibleCursor
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
	if m.ongoingTranscript == nil {
		m.ongoingTranscript = newPromptTestOngoingTranscriptController(m, &ongoingSurfaceSpy{})
	}
	hydration := ongoingHydrationMessage(1)
	hydration.Payload.Hydration.RuntimeReadModelUpdate.Activity = runningPromptTestActivity()
	ownership := m.snapshotTranscriptPromptOwnership()
	hydration.Payload.Hydration.PendingPrompts = make([]clientui.TranscriptPrompt, 0, len(ownership.events))
	for _, event := range ownership.events {
		hydration.Payload.Hydration.PendingPrompts = append(
			hydration.Payload.Hydration.PendingPrompts,
			cloneTranscriptPromptForAsk(event.prompt),
		)
	}
	slices.SortFunc(hydration.Payload.Hydration.PendingPrompts, func(left, right clientui.TranscriptPrompt) int {
		if order := left.CreatedAt.Compare(right.CreatedAt); order != 0 {
			return order
		}
		return cmp.Compare(left.PromptID, right.PromptID)
	})
	if _, _, err := m.ongoingTranscript.AcceptFrom(ongoingTestSessionID(), hydration); err != nil {
		t.Fatalf("accept prompt hydration: %v", err)
	}
	if _, _, err := m.ongoingTranscript.AcceptFrom(ongoingTestSessionID(), message); err != nil {
		t.Fatalf("accept prompt resolution: %v", err)
	}
}
