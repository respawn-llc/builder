package workflowstore

import (
	"errors"
	"log/slog"

	"core/server/workflow"
	"core/shared/invariant"
)

const retainedTargetInvariantOperation = "workflow.retained_target.resolve"

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

func retainedTargetInvariantPolicy() invariant.Policy {
	return invariant.NewPolicy(invariant.WithSink(workflowInvariantSlogSink{}))
}

func checkRetainedTargetInvariantBeforeMutation(detail workflow.RetainedTargetInvariantDetail) {
	policy := retainedTargetInvariantPolicy()
	if policy.Mode() == invariant.ModePanic {
		policy.Check(false, retainedTargetInvariantDiagnostic(detail))
	}
}

func reportRetainedTargetInvariantAfterCommit(detail workflow.RetainedTargetInvariantDetail) {
	policy := retainedTargetInvariantPolicy()
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

func reportRetainedTargetInvariantError(err error) {
	var invariantErr workflow.RetainedTargetInvariantError
	if errors.As(err, &invariantErr) {
		retainedTargetInvariantPolicy().Check(false, retainedTargetInvariantDiagnostic(invariantErr.Detail))
	}
}
