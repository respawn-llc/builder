package app

import (
	"context"
	"testing"

	"core/shared/clientui"

	"github.com/google/uuid"
)

func (m *uiModel) setRuntimeActivityBusyForTest(busy bool) {
	if m == nil {
		return
	}
	if !busy {
		_ = m.applyRuntimeActivityProjection(clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{}))
		return
	}
	_ = m.applyRuntimeActivityProjection(clientui.MustRuntimeActivity(clientui.RuntimeActivityRunning, clientui.RuntimeActivityOptions{
		ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
		RunID:      "test-run",
		StepID:     "test-step",
	}))
}

func (m *uiModel) setRuntimeGoalRunForTest(goalRun bool) {
	if m == nil {
		return
	}
	if !goalRun {
		if m.isBusy() {
			_ = m.applyRuntimeActivityProjection(clientui.MustRuntimeActivity(clientui.RuntimeActivityRunning, clientui.RuntimeActivityOptions{
				ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
				RunID:      "test-run",
				StepID:     "test-step",
			}))
		}
		return
	}
	_ = m.applyRuntimeActivityProjection(clientui.MustRuntimeActivity(clientui.RuntimeActivityRunning, clientui.RuntimeActivityOptions{
		ActiveKind: clientui.RuntimeActivityActiveKindGoalLoop,
		RunID:      "test-run",
		StepID:     "test-step",
	}))
}

func submitRuntimeClientForTest(t *testing.T, client clientui.RuntimeClient, text string) (clientui.UserTurnSubmission, error) {
	t.Helper()
	return client.SubmitRuntimeInput(context.Background(), clientui.RuntimeSubmitRequest{
		OperationRef: clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: uuid.NewString()},
		Text:         text,
	})
}

func queueRuntimeClientForTest(t *testing.T, client clientui.RuntimeClient, text string) (clientui.QueuedUserMessage, error) {
	t.Helper()
	return client.QueueRuntimeUserMessage(clientui.RuntimeQueueUserMessageRequest{
		OperationRef: clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindQueuedMessage, ClientRequestID: uuid.NewString()},
		Text:         text,
	})
}
