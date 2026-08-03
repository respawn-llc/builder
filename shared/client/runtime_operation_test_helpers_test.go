package client

import (
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
)

func runtimeSubmitUserTurnRequestForTest(sessionID, text string) serverapi.RuntimeSubmitUserTurnRequest {
	submitID := runtimeids.NewRuntimeClientRequestID()
	preSubmitID := runtimeids.NewRuntimeClientRequestID()
	return serverapi.RuntimeSubmitUserTurnRequest{
		ClientRequestID: submitID.String(),
		SessionID:       sessionID,
		Input:           runtimeinput.Text(text),
		OperationRef: clientui.RuntimeOperationRef{
			Kind:            clientui.RuntimeOperationKindSubmit,
			ClientRequestID: submitID,
		},
		PreSubmitCompactionOperationRef: clientui.RuntimeOperationRef{
			Kind:            clientui.RuntimeOperationKindPreSubmitCompact,
			ClientRequestID: preSubmitID,
		},
	}
}
