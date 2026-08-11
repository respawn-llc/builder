package client

import (
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
)

func runtimeSubmitUserTurnRequestForTest(sessionID, text string) serverapi.RuntimeSubmitUserTurnRequest {
	submitID := runtimeids.NewRuntimeClientRequestID()
	return serverapi.RuntimeSubmitUserTurnRequest{
		ClientRequestID: submitID.String(),
		SessionID:       sessionID,
		Input:           runtimeinput.Text(text),
	}
}
