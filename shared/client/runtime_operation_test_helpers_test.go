package client

import (
	"core/shared/runtimeinput"
	"core/shared/serverapi"
)

func runtimeSubmitUserTurnRequestForTest(sessionID, text string) serverapi.RuntimeSubmitUserTurnRequest {
	return serverapi.RuntimeSubmitUserTurnRequest{
		SessionID: sessionID,
		Input:     runtimeinput.Text(text),
	}
}
