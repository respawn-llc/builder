package app

import (
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
	Version  clientui.ReadModelVersion
	Activity clientui.RuntimeActivity
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
		Version:  view.Version,
		Activity: view.Activity,
	}
}

func runtimeTupleFromReadModelUpdate(update clientui.RuntimeReadModelUpdate) runtimeTupleCandidate {
	return runtimeTupleCandidate{
		Version:  update.Version,
		Activity: update.Activity,
	}
}

func applyRuntimeTuple(view *clientui.RuntimeMainView, candidate runtimeTupleCandidate) {
	view.Version = candidate.Version
	view.Activity = candidate.Activity
}

func runtimeTupleMatchesView(candidate runtimeTupleCandidate, view clientui.RuntimeMainView) bool {
	return candidate.Version == view.Version && runtimeActivitiesEqual(candidate.Activity, view.Activity)
}

func runtimeActivitiesEqual(left, right clientui.RuntimeActivity) bool {
	if left.State != right.State ||
		left.Reviewer != right.Reviewer ||
		left.QueueAccepting != right.QueueAccepting ||
		left.DiagnosticRecovery != right.DiagnosticRecovery {
		return false
	}
	if left.ActiveStep == nil || right.ActiveStep == nil {
		return left.ActiveStep == nil && right.ActiveStep == nil
	}
	return *left.ActiveStep == *right.ActiveStep
}

type hydrationRuntimeTupleConflictError struct {
	current  clientui.RuntimeMainView
	incoming runtimeTupleCandidate
}

func (e hydrationRuntimeTupleConflictError) Error() string {
	return "stale or conflicting transcript hydration runtime tuple"
}

func (e hydrationRuntimeTupleConflictError) facts() map[string]any {
	return map[string]any{
		"current_version":   e.current.Version,
		"incoming_version":  e.incoming.Version,
		"current_activity":  e.current.Activity,
		"incoming_activity": e.incoming.Activity,
	}
}

func hydrationRuntimeTupleError(current clientui.RuntimeMainView, incoming runtimeTupleCandidate) error {
	return hydrationRuntimeTupleConflictError{current: current, incoming: incoming}
}

func runtimeReadModelResetMainViewRefreshRequest() runtimeMainViewRefreshRequest {
	return runtimeMainViewRefreshRequest{
		cause:    runtimeMainViewRefreshCauseManual,
		class:    runtimeSyncPolicyClassAllowed,
		priority: 100,
	}
}
