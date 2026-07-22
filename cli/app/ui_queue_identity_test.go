package app

import (
	"fmt"
	"testing"

	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func queuedUserMessagesForTest(texts ...string) []clientui.QueuedUserMessage {
	messages := make([]clientui.QueuedUserMessage, 0, len(texts))
	for index, text := range texts {
		messages = append(messages, clientui.QueuedUserMessage{ID: fmt.Sprintf("queue-test-%d", index), Text: text})
	}
	return messages
}

func queuedInputsForTest(texts ...string) []queuedInputItem {
	items := make([]queuedInputItem, 0, len(texts))
	for index, text := range texts {
		items = append(items, queuedInputItem{ID: fmt.Sprintf("input-queue-test-%d", index), Text: text})
	}
	return items
}

func applyFirstInjectedQueueCreateDoneForTest(t *testing.T, m *uiModel, cmd tea.Cmd) *uiModel {
	t.Helper()
	if cmd == nil {
		return m
	}
	for _, msg := range collectCmdMessages(t, cmd) {
		if typed, ok := msg.(injectedQueueCreateDoneMsg); ok {
			next, _ := m.Update(typed)
			updated, ok := next.(*uiModel)
			if !ok {
				t.Fatalf("updated model = %T, want *uiModel", next)
			}
			return updated
		}
	}
	return m
}

func applyQueuedRuntimeWorkCheckForTest(t *testing.T, m *uiModel, cmd tea.Cmd) (*uiModel, tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return m, nil
	}
	for _, msg := range collectCmdMessages(t, cmd) {
		if typed, ok := msg.(queuedRuntimeWorkCheckDoneMsg); ok {
			next, nextCmd := m.Update(typed)
			updated, ok := next.(*uiModel)
			if !ok {
				t.Fatalf("updated model = %T, want *uiModel", next)
			}
			return updated, nextCmd
		}
	}
	return m, cmd
}

func TestBusyEnterUsesAuthoritativeSubmitPath(t *testing.T) {
	client := &runtimeControlFakeClient{submitQueuedID: "busy-submit-queue"}
	model := newProjectedTestUIModel(client)
	model.setRuntimeActivityBusyForTest(true)
	testSetMainInput(model, "steer while thinking")

	next, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)
	if command == nil {
		t.Fatal("busy Enter produced no runtime command")
	}
	for _, message := range collectCmdMessages(t, command) {
		model = updateUIModel(t, model, message)
	}

	if client.submitText != "steer while thinking" {
		t.Fatalf("submitted text = %q, want authoritative submit", client.submitText)
	}
	if client.submitCalls != 1 {
		t.Fatalf("authoritative submit calls = %d, want 1", client.submitCalls)
	}
}
