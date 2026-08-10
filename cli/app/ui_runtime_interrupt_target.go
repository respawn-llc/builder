package app

import "core/shared/clientui"

func runtimeOperationKindForActiveActivity(kind clientui.RuntimeActivityActiveKind) clientui.RuntimeOperationKind {
	switch kind {
	case clientui.RuntimeActivityActiveKindPreSubmitCompaction:
		return clientui.RuntimeOperationKindPreSubmitCompact
	case clientui.RuntimeActivityActiveKindCompaction:
		return clientui.RuntimeOperationKindCompact
	case clientui.RuntimeActivityActiveKindUserShell:
		return clientui.RuntimeOperationKindUserShell
	case clientui.RuntimeActivityActiveKindUserTurn:
		return clientui.RuntimeOperationKindSubmit
	default:
		return ""
	}
}
