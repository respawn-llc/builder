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

func queuedUserMessageIDsForTest(messages []clientui.QueuedUserMessage) []string {
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
	}
	return ids
}

func queuedInputsForTest(texts ...string) []queuedInputItem {
	items := make([]queuedInputItem, 0, len(texts))
	for index, text := range texts {
		items = append(items, queuedInputItem{ID: fmt.Sprintf("input-queue-test-%d", index), Text: text})
	}
	return items
}

func applyInterruptedRunStateForTest(t *testing.T, m *uiModel) *uiModel {
	t.Helper()
	return applyIdleRuntimeActivityForTest(t, m)
}

func applyIdleRuntimeActivityForTest(t *testing.T, m *uiModel) *uiModel {
	t.Helper()
	activity := clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{QueueAccepting: true})
	next, _ := m.Update(runtimeEventMsg{event: clientui.Event{Kind: clientui.EventRuntimeActivityChanged, ReadModelVersion: nextRuntimeReadModelVersionForTest(m), RuntimeActivity: &activity}})
	updated, ok := next.(*uiModel)
	if !ok {
		t.Fatalf("updated model = %T, want *uiModel", next)
	}
	return updated
}

func applyRunningRuntimeActivityForTest(t *testing.T, m *uiModel, runID, stepID string) *uiModel {
	t.Helper()
	activity := clientui.MustRuntimeActivity(clientui.RuntimeActivityRunning, clientui.RuntimeActivityOptions{
		ActiveKind:     clientui.RuntimeActivityActiveKindUserTurn,
		RunID:          runID,
		StepID:         stepID,
		QueueAccepting: true,
	})
	next, _ := m.Update(runtimeEventMsg{event: clientui.Event{Kind: clientui.EventRuntimeActivityChanged, ReadModelVersion: nextRuntimeReadModelVersionForTest(m), RuntimeActivity: &activity}})
	updated, ok := next.(*uiModel)
	if !ok {
		t.Fatalf("updated model = %T, want *uiModel", next)
	}
	return updated
}

func nextRuntimeReadModelVersionForTest(m *uiModel) clientui.ReadModelVersion {
	if m != nil && m.runtimeReadModelVersion.Validate() == nil {
		return clientui.ReadModelVersion{
			Epoch:      m.runtimeReadModelVersion.Epoch,
			Generation: m.runtimeReadModelVersion.Generation,
			Sequence:   m.runtimeReadModelVersion.Sequence + 1,
		}
	}
	return clientui.ReadModelVersion{Epoch: "test-runtime-read-model", Generation: 1, Sequence: 1}
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
