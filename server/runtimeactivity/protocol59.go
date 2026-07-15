package runtimeactivity

import (
	"fmt"

	"core/server/runtimefeed"
	"core/shared/clientui"
)

func Protocol59ResponseSnapshot(update runtimefeed.RuntimeReadModelUpdate) ResponseSnapshot {
	if err := update.Validate(); err != nil {
		panic(fmt.Sprintf("project invalid runtime read-model update to protocol 59: %+v: %v", update, err))
	}
	return ResponseSnapshot{
		Version:             update.Version,
		Activity:            Protocol59RuntimeActivity(update.Activity),
		InputReconciliation: Protocol59InputReconciliation(update.Version, update.InputReconciliation),
	}
}

func Protocol59RuntimeActivity(activity runtimefeed.RuntimeActivity) clientui.RuntimeActivity {
	projected := clientui.RuntimeActivity{
		State:              activity.State,
		QueueAccepting:     activity.QueueAccepting,
		DiagnosticRecovery: activity.DiagnosticRecovery,
	}
	if activity.ActiveStep != nil {
		projected.ActiveKind = activity.ActiveStep.ActiveKind
		projected.RunID = activity.ActiveStep.RunID.String()
		projected.StepID = activity.ActiveStep.StepID.String()
	}
	return projected
}

func Protocol59InputReconciliation(
	version clientui.ReadModelVersion,
	snapshot runtimefeed.RuntimeInputReconciliationSnapshot,
) clientui.RuntimeInputReconciliationSnapshot {
	projected := clientui.RuntimeInputReconciliationSnapshot{
		Version:    version,
		Operations: make([]clientui.RuntimeInputReconciliation, 0, len(snapshot.Operations)),
	}
	for _, reconciliation := range snapshot.Operations {
		ref := clientui.RuntimeOperationRef{
			Kind:            reconciliation.Operation.Kind,
			ClientRequestID: reconciliation.Operation.ClientRequestID.String(),
		}
		if reconciliation.Operation.QueueItemID != nil {
			ref.ClientRequestID = ""
			ref.QueueItemID = reconciliation.Operation.QueueItemID.String()
		}
		projected.Operations = append(projected.Operations, clientui.RuntimeInputReconciliation{
			Version:      version,
			OperationRef: ref,
			State:        reconciliation.State,
		})
	}
	return projected
}
