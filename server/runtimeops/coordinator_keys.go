package runtimeops

import (
	"strings"

	"core/shared/clientui"
)

func operationCancellationInterruptsActive(ref clientui.RuntimeOperationRef) bool {
	switch ref.Kind {
	case clientui.RuntimeOperationKindSubmit, clientui.RuntimeOperationKindUserShell, clientui.RuntimeOperationKindCompact, clientui.RuntimeOperationKindPreSubmitCompact:
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
