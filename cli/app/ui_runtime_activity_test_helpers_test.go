package app

import (
	"context"
	"testing"

	"core/shared/clientui"
	"core/shared/runtimeids"
)

func (m *uiModel) setRuntimeActivityBusyForTest(busy bool) {
	if m == nil {
		return
	}
	if !busy {
		_ = m.applyRuntimeActivityProjection(clientui.RuntimeActivity{State: clientui.RuntimeActivityRegisteredIdle})
		return
	}
	_ = m.applyRuntimeActivityProjection(clientui.RuntimeActivity{
		State: clientui.RuntimeActivityRunning,
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
		OperationRef: clientui.RuntimeOperationRef{
			Kind:            clientui.RuntimeOperationKindSubmit,
			ClientRequestID: runtimeids.NewRuntimeClientRequestID(),
		},
		PreSubmitCompactionOperationRef: clientui.RuntimeOperationRef{
			Kind:            clientui.RuntimeOperationKindPreSubmitCompact,
			ClientRequestID: runtimeids.NewRuntimeClientRequestID(),
		},
		Input: newRuntimeTextInput(text),
	})
}
