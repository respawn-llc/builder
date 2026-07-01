package runtimeops

import (
	"strings"

	"core/shared/clientui"
)

func sameOperationRequest[Req any](existing any, req Req, same func(Req, Req) bool) bool {
	typed, ok := existing.(Req)
	if !ok {
		return false
	}
	if same == nil {
		return true
	}
	return same(typed, req)
}

func operationRequestID(ref clientui.RuntimeOperationRef) string {
	if strings.TrimSpace(ref.ClientRequestID) != "" {
		return strings.TrimSpace(ref.ClientRequestID)
	}
	return strings.TrimSpace(ref.QueueItemID)
}

func operationCancellationInterruptsActive(ref clientui.RuntimeOperationRef) bool {
	switch ref.Kind {
	case clientui.RuntimeOperationKindSubmit, clientui.RuntimeOperationKindUserShell, clientui.RuntimeOperationKindCompact, clientui.RuntimeOperationKindPreSubmitCompact, clientui.RuntimeOperationKindSubmitQueued:
		return true
	default:
		return false
	}
}

func sessionKey(sessionID string) string {
	key := strings.TrimSpace(sessionID)
	if key == "" {
		return "unknown"
	}
	return key
}
