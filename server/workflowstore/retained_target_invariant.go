package workflowstore

import (
	"errors"
	"log/slog"

	"core/server/workflow"
	"core/shared/invariant"
)

const legacyContinuationSourceOperation = "workflow.legacy_continuation_source.resolve"

type workflowInvariantSlogSink struct{}

func (workflowInvariantSlogSink) RecordInvariantDiagnostic(diagnostic invariant.Diagnostic) {
	attributes := make([]any, 0, 2+len(diagnostic.Fields)*2)
	attributes = append(attributes, "scope", diagnostic.Scope)
	for field, value := range diagnostic.Fields {
		attributes = append(attributes, string(field), value)
	}
	attributes = append(attributes, "stack", diagnostic.Stack)
	slog.Error("workflow invariant violation", attributes...)
}

type legacyContinuationSourceFallbackDetail struct {
	Source       workflow.CurrentNodeReference
	TargetNodeID workflow.NodeID
	EdgeID       workflow.EdgeID
	Scope        workflow.LegacyContinuationSourceScope
}

func legacyContinuationSourceDiagnostic(detail legacyContinuationSourceFallbackDetail) invariant.Diagnostic {
	fields := map[invariant.Field]string{
		invariant.FieldOperation:    legacyContinuationSourceOperation,
		invariant.FieldTaskID:       string(detail.Source.TaskID),
		invariant.FieldSourceNodeID: string(detail.Source.NodeID),
		invariant.FieldTargetNodeID: string(detail.TargetNodeID),
		invariant.FieldEdgeID:       string(detail.EdgeID),
		invariant.FieldReason:       string(detail.Scope),
	}
	if branchKey, branchScoped := detail.Source.TransitionBranchKey(); branchScoped {
		fields[invariant.FieldTransitionBranchKey] = string(branchKey)
	}
	return invariant.Diagnostic{
		Scope:  invariant.ScopeWorkflowExecution,
		Fields: fields,
	}
}

func resolveLegacyContinuationSource(
	detail legacyContinuationSourceFallbackDetail,
	contextSource workflow.ContextSourceKind,
	targetKind workflow.NodeKind,
) (transitionContextResolution, error) {
	if contextSource == workflow.ContextSourcePreviousTargetOrNew &&
		targetKind == workflow.NodeKindAgent {
		return transitionContextResolution{
			TargetSession:  workflow.CreateTargetSessionIntent(),
			ActiveSource:   workflow.DeferredSelfMaterializedContinuationSource(),
			legacyFallback: &detail,
		}, nil
	}
	return transitionContextResolution{}, workflow.LegacyContinuationSourceUnresolvedError{
		Source:       detail.Source,
		TargetNodeID: detail.TargetNodeID,
		EdgeID:       detail.EdgeID,
		Scope:        detail.Scope,
	}
}

func legacyContinuationSourceError(
	policy invariant.Policy,
	detail legacyContinuationSourceFallbackDetail,
) error {
	if policy.Mode() == invariant.ModePanic {
		policy.Check(false, legacyContinuationSourceDiagnostic(detail))
	}
	return workflow.LegacyContinuationSourceUnresolvedError{
		Source:       detail.Source,
		TargetNodeID: detail.TargetNodeID,
		EdgeID:       detail.EdgeID,
		Scope:        detail.Scope,
	}
}

func checkLegacyContinuationSourceBeforeMutation(
	policy invariant.Policy,
	detail legacyContinuationSourceFallbackDetail,
) {
	if policy.Mode() == invariant.ModePanic {
		policy.Check(false, legacyContinuationSourceDiagnostic(detail))
	}
}

func reportLegacyContinuationSourceAfterCommit(
	policy invariant.Policy,
	detail legacyContinuationSourceFallbackDetail,
) {
	if policy.Mode() == invariant.ModeDiagnostic {
		policy.Check(false, legacyContinuationSourceDiagnostic(detail))
	}
}

func reportLegacyContinuationSourceError(policy invariant.Policy, err error) {
	var unresolved workflow.LegacyContinuationSourceUnresolvedError
	if !errors.As(err, &unresolved) {
		return
	}
	policy.Check(false, legacyContinuationSourceDiagnostic(legacyContinuationSourceFallbackDetail{
		Source:       unresolved.Source,
		TargetNodeID: unresolved.TargetNodeID,
		EdgeID:       unresolved.EdgeID,
		Scope:        unresolved.Scope,
	}))
}

func reportWorkflowInvariantError(policy invariant.Policy, err error) {
	reportLegacyContinuationSourceError(policy, err)
}
