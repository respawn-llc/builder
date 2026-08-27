package app

import (
	"context"
	"testing"

	"core/shared/clientui"
	"core/shared/runtimeinput"
)

func (m *uiModel) setRuntimeActivityBusyForTest(busy bool) {
	if m == nil {
		return
	}
	if !busy {
		_ = m.applyRuntimeActivityProjection(clientui.RuntimeActivity{
			State:    clientui.RuntimeActivityRegisteredIdle,
			Reviewer: clientui.ReviewerActivityInactive,
		})
		return
	}
	_ = m.applyRuntimeActivityProjection(clientui.RuntimeActivity{
		State:    clientui.RuntimeActivityRunning,
		Reviewer: clientui.ReviewerActivityInactive,
		ActiveStep: &clientui.RuntimeActiveStep{
			ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
			RunID:      ongoingTestRunID(),
			StepID:     ongoingTestStepID(),
		},
	})
}

func submitRuntimeClientForTest(t *testing.T, client clientui.RuntimeClient, text string) (clientui.UserTurnSubmission, error) {
	t.Helper()
	return client.SubmitRuntimeInput(context.Background(), clientui.RuntimeSubmitRequest{
		Input: runtimeinput.Text(text),
	})
}
