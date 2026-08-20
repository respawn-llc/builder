package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/shared/invariant"
)

const retainedTargetInvariantOperation = "workflow.retained_target.resolve"
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
func checkRetainedTargetInvariantBeforeMutation(
	policy invariant.Policy,
	detail workflow.RetainedTargetInvariantDetail,
) {
	if policy.Mode() == invariant.ModePanic {
		policy.Check(false, retainedTargetInvariantDiagnostic(detail))
	}
}
func reportRetainedTargetInvariantAfterCommit(
	policy invariant.Policy,
	detail workflow.RetainedTargetInvariantDetail,
) {
	if policy.Mode() == invariant.ModeDiagnostic {
		policy.Check(false, retainedTargetInvariantDiagnostic(detail))
	}
}
func retainedTargetInvariantDiagnostic(detail workflow.RetainedTargetInvariantDetail) invariant.Diagnostic {
	fields := map[invariant.Field]string{
		invariant.FieldOperation:    retainedTargetInvariantOperation,
		invariant.FieldTaskID:       string(detail.TaskID),
		invariant.FieldSourceNodeID: string(detail.SourceNodeID),
		invariant.FieldTargetNodeID: string(detail.TargetNodeID),
		invariant.FieldReason:       string(detail.Reason),
	}
	if detail.ActiveSourceSessionID != nil {
		fields[invariant.FieldActiveSourceSessionID] = detail.ActiveSourceSessionID.String()
	}
	if detail.RejectedRetainedSessionID != nil {
		fields[invariant.FieldRejectedRetainedSessionID] = detail.RejectedRetainedSessionID.String()
	}
	return invariant.Diagnostic{
		Scope:  invariant.ScopeWorkflowExecution,
		Fields: fields,
	}
}

func (s *Store) validateRetainedTargetSessionCreationAuthorization(
	ctx context.Context,
	q *sqlitegen.Queries,
	enteringEdge workflow.Edge,
	sourceNodeID workflow.NodeID,
	currentNode workflow.CurrentNode,
) error {
	contextSource := workflow.CanonicalContextSource(enteringEdge.ContextSource).Kind
	if currentNode.SessionID != nil ||
		enteringEdge.ContextMode == workflow.ContextModeNewSession ||
		(contextSource != workflow.ContextSourcePreviousTarget &&
			contextSource != workflow.ContextSourcePreviousTargetOrNew) {
		return nil
	}
	activeSourceSessionID, exact := currentNode.ContinuationSource.ExactSessionID()
	if !exact {
		return nil
	}
	association, err := currentTaskSessionForNode(
		sqlitegen.WithExpectedNoRows(ctx),
		q,
		currentNode.Reference,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if association.SourceSessionID != activeSourceSessionID {
		return nil
	}
	detail := workflow.RetainedTargetInvariantDetail{
		TaskID:                    currentNode.Reference.TaskID,
		SourceNodeID:              sourceNodeID,
		TargetNodeID:              currentNode.Reference.NodeID,
		ActiveSourceSessionID:     &activeSourceSessionID,
		RejectedRetainedSessionID: &association.SessionID,
		Reason:                    workflow.RetainedTargetInvariantUnauthorizedSessionCreation,
	}
	checkRetainedTargetInvariantBeforeMutation(s.invariantPolicy, detail)
	return workflow.RetainedTargetInvariantError{Detail: detail}
}

func reportRetainedTargetInvariantError(policy invariant.Policy, err error) {
	var invariantErr workflow.RetainedTargetInvariantError
	if errors.As(err, &invariantErr) {
		policy.Check(false, retainedTargetInvariantDiagnostic(invariantErr.Detail))
	}
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
	reportRetainedTargetInvariantError(policy, err)
	reportLegacyContinuationSourceError(policy, err)
}
