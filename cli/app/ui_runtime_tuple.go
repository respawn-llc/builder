package app

import (
	"runtime/debug"

	"core/cli/tui/ongoing"
	"core/shared/clientui"
)

type runtimeTupleIngress uint8

const (
	runtimeTupleIngressIncremental runtimeTupleIngress = iota + 1
	runtimeTupleIngressAuthoritativeSnapshot
	runtimeTupleIngressHydration
)

type runtimeTupleDecision uint8

const (
	runtimeTupleIgnore runtimeTupleDecision = iota
	runtimeTupleApply
	runtimeTupleRefresh
)

type runtimeTupleCandidate struct {
	Version             clientui.ReadModelVersion
	Activity            clientui.RuntimeActivity
	InputReconciliation clientui.RuntimeInputReconciliationSnapshot
}

type runtimeTupleMergeResult struct {
	decision runtimeTupleDecision
	view     clientui.RuntimeMainView
	project  bool
}

func decideRuntimeTuple(
	current clientui.ReadModelVersion,
	incoming clientui.ReadModelVersion,
	ingress runtimeTupleIngress,
) runtimeTupleDecision {
	if incoming.Validate() != nil {
		return runtimeTupleIgnore
	}
	if current.Validate() != nil {
		return runtimeTupleApply
	}
	if incoming.Epoch != current.Epoch {
		if ingress == runtimeTupleIngressIncremental {
			return runtimeTupleRefresh
		}
		return runtimeTupleApply
	}
	if incoming.Generation != current.Generation {
		if incoming.Generation < current.Generation {
			return runtimeTupleIgnore
		}
		if ingress == runtimeTupleIngressIncremental {
			return runtimeTupleRefresh
		}
		return runtimeTupleApply
	}
	if incoming.Sequence <= current.Sequence {
		return runtimeTupleIgnore
	}
	return runtimeTupleApply
}

func runtimeTupleFromMainView(view clientui.RuntimeMainView) runtimeTupleCandidate {
	return runtimeTupleCandidate{
		Version:             view.Version,
		Activity:            view.Activity,
		InputReconciliation: view.InputReconciliation,
	}
}

func runtimeTupleFromReadModelUpdate(update clientui.RuntimeReadModelUpdate) runtimeTupleCandidate {
	return runtimeTupleCandidate{
		Version:             update.Version,
		Activity:            update.Activity,
		InputReconciliation: update.InputReconciliation,
	}
}

func applyRuntimeTuple(view *clientui.RuntimeMainView, candidate runtimeTupleCandidate) {
	view.Version = candidate.Version
	view.Activity = candidate.Activity
	view.InputReconciliation = candidate.InputReconciliation
}

func runtimeTupleMatchesView(candidate runtimeTupleCandidate, view clientui.RuntimeMainView) bool {
	if candidate.Version != view.Version || !runtimeActivitiesEqual(candidate.Activity, view.Activity) {
		return false
	}
	return runtimeInputReconciliationSnapshotsEqual(candidate.InputReconciliation, view.InputReconciliation)
}

func runtimeActivitiesEqual(left, right clientui.RuntimeActivity) bool {
	if left.State != right.State ||
		left.QueueAccepting != right.QueueAccepting ||
		left.DiagnosticRecovery != right.DiagnosticRecovery {
		return false
	}
	if left.ActiveStep == nil || right.ActiveStep == nil {
		return left.ActiveStep == nil && right.ActiveStep == nil
	}
	return *left.ActiveStep == *right.ActiveStep
}

func runtimeInputReconciliationSnapshotsEqual(
	left clientui.RuntimeInputReconciliationSnapshot,
	right clientui.RuntimeInputReconciliationSnapshot,
) bool {
	if len(left.Operations) != len(right.Operations) {
		return false
	}
	for index := range left.Operations {
		if !runtimeInputReconciliationsEqual(left.Operations[index], right.Operations[index]) {
			return false
		}
	}
	return true
}

func runtimeInputReconciliationsEqual(left, right clientui.RuntimeInputReconciliation) bool {
	return left.State == right.State && runtimeOperationRefsEqual(left.Operation, right.Operation)
}

func runtimeOperationRefsEqual(left, right clientui.RuntimeOperationRef) bool {
	if left.Kind != right.Kind || left.ClientRequestID != right.ClientRequestID {
		return false
	}
	if left.QueueItemID == nil || right.QueueItemID == nil {
		return left.QueueItemID == nil && right.QueueItemID == nil
	}
	return *left.QueueItemID == *right.QueueItemID
}

func hydrationRuntimeTupleError(current clientui.RuntimeMainView, incoming runtimeTupleCandidate) error {
	return ongoing.DeveloperError{
		Operation: "admit_transcript_hydration_runtime_tuple",
		Reason:    "stale or conflicting runtime tuple",
		Facts: map[string]any{
			"current_version":               current.Version,
			"incoming_version":              incoming.Version,
			"current_activity":              current.Activity,
			"incoming_activity":             incoming.Activity,
			"current_input_reconciliation":  current.InputReconciliation,
			"incoming_input_reconciliation": incoming.InputReconciliation,
		},
		Stack: string(debug.Stack()),
	}
}

func runtimeReadModelResetMainViewRefreshRequest() runtimeMainViewRefreshRequest {
	return runtimeMainViewRefreshRequest{
		cause:    runtimeMainViewRefreshCauseManual,
		class:    runtimeSyncPolicyClassAllowed,
		priority: 100,
	}
}
