package app

import "core/shared/apicontract"

func newHeadlessRunPromptClient(server *embeddedAppServer) apicontract.RunPromptService {
	return server.RunPromptClient()
}
