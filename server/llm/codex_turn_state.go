package llm

import "net/http"

func (c *CodexDispatchContext) observeTurnStateHTTPHeader(header http.Header) {
	if c == nil || header == nil {
		return
	}
	values := header.Values(codexTurnStateHeader)
	if len(values) != 0 {
		c.observeTurnStateCandidate(values[0], codexTurnStateSourceHTTPHeader)
	}
}

func observeCodexTurnStateResponseHeader(dispatch *CodexDispatchContext, response *http.Response, observed *bool) {
	if observed == nil || *observed || response == nil {
		return
	}
	*observed = true
	dispatch.observeTurnStateHTTPHeader(response.Header)
}
