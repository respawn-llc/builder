package workflowstore

import (
	"errors"
	"log/slog"
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/workflow"
	"core/shared/invariant"
	"core/shared/runtimeids"
)

func TestRetainedTargetInvariantDiagnostics(t *testing.T) {
	source, rejected := runtimeids.NewSessionID(), runtimeids.NewSessionID()
	detail := workflow.RetainedTargetInvariantDetail{
		TaskID: "task", SourceNodeID: "source", TargetNodeID: "target",
		ActiveSourceSessionID: &source, RejectedRetainedSessionID: &rejected,
		Reason: workflow.RetainedTargetInvariantProoflessCurrentTarget,
	}
	diagnosticPolicy := invariant.NewPolicy(
		invariant.WithMode(invariant.ModeDiagnostic),
		invariant.WithSink(workflowInvariantSlogSink{}),
	)
	for _, emit := range []func(){
		func() {
			reportRetainedTargetInvariantError(
				diagnosticPolicy,
				workflow.RetainedTargetInvariantError{Detail: detail},
			)
		},
		func() { reportRetainedTargetInvariantAfterCommit(diagnosticPolicy, detail) },
	} {
		records := testsetup.CaptureSlogRecords(t)
		emit()
		assertRetainedTargetInvariantDiagnostic(t, records.Records(), source, rejected)
	}
	func() {
		defer func() {
			if _, ok := recover().(invariant.Diagnostic); !ok {
				t.Fatal("debug invariant did not panic with typed diagnostic")
			}
		}()
		checkRetainedTargetInvariantBeforeMutation(
			invariant.NewPolicy(invariant.WithMode(invariant.ModePanic)),
			detail,
		)
	}()
	records := testsetup.CaptureSlogRecords(t)
	reportRetainedTargetInvariantError(diagnosticPolicy, workflow.RetainedTargetUnavailableError{})
	reportRetainedTargetInvariantError(diagnosticPolicy, errors.New("ordinary mismatch"))
	for _, record := range records.Records() {
		if record.Fields[string(invariant.FieldOperation)] == retainedTargetInvariantOperation {
			t.Fatal("ordinary retained-target state emitted invariant diagnostic")
		}
	}
}

func assertRetainedTargetInvariantDiagnostic(t *testing.T, records []testsetup.CapturedSlogRecord, source, rejected runtimeids.SessionID) {
	t.Helper()
	for _, record := range records {
		fields := record.Fields
		if record.Level == slog.LevelError &&
			fields["scope"] == invariant.ScopeWorkflowExecution &&
			fields[string(invariant.FieldOperation)] == retainedTargetInvariantOperation &&
			fields[string(invariant.FieldReason)] == string(workflow.RetainedTargetInvariantProoflessCurrentTarget) &&
			fields[string(invariant.FieldTaskID)] == "task" && fields[string(invariant.FieldSourceNodeID)] == "source" &&
			fields[string(invariant.FieldTargetNodeID)] == "target" && fields[string(invariant.FieldActiveSourceSessionID)] == source.String() &&
			fields[string(invariant.FieldRejectedRetainedSessionID)] == rejected.String() {
			return
		}
	}
	t.Fatalf("typed retained-target invariant diagnostic not found: %+v", records)
}

func TestRetainedTargetSessionCreationPanicsWhenMatchingSessionExists(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	review := nodeByKey(t, definition, "review")
	audit := nodeByKey(t, definition, "audit")
	var reworkEdgeID workflow.EdgeID
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		reworkEdgeID = appendManualMoveRetainedReviewEdge(
			req,
			workflowID,
			audit,
			review,
			"unauthorized-create",
			workflow.ContextSourcePreviousTarget,
		)
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	lineageSessionID := associateAndBindCurrentNodeSessionForTest(
		t,
		ctx,
		store,
		binding,
		cfg,
		started.Reference,
	)
	lineageSource, err := workflow.NewExactMaterializedContinuationSource(lineageSessionID)
	if err != nil {
		t.Fatalf("NewExactMaterializedContinuationSource: %v", err)
	}
	reviewReference := replaceSerialCurrentNodeBindingFixture(
		t,
		ctx,
		store,
		started,
		workflow.NodeIDOf(review),
		nil,
		lineageSource,
	)
	retainedReviewSessionID := associateAndBindCurrentNodeSessionForTest(
		t,
		ctx,
		store,
		binding,
		cfg,
		reviewReference,
	)
	taskRow, err := store.queries.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	taskRecord, err := taskRecordFromTask(taskRow)
	if err != nil {
		t.Fatalf("taskRecordFromTask: %v", err)
	}
	definition, workflowRecord, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition after graph save: %v", err)
	}
	store.invariantPolicy = invariant.NewPolicy(invariant.WithMode(invariant.ModePanic))
	unauthorized := workflow.CurrentNode{
		Reference:          reviewReference,
		EnteredByEdgeID:    &reworkEdgeID,
		ContinuationSource: lineageSource,
		Scheduling: &workflow.CurrentNodeScheduling{
			State: workflow.CurrentNodeSchedulingReady,
		},
		AgentExecutionSelection: &workflow.AgentExecutionSelection{
			Assignee: "coder",
			Origin:   workflow.AssigneeOriginConfiguredFallback,
		},
	}

	defer func() {
		recovered, ok := recover().(invariant.Diagnostic)
		if !ok {
			t.Fatalf("unauthorized retained-target Session creation panic = %T %v, want invariant.Diagnostic", recovered, recovered)
		}
		if recovered.Fields[invariant.FieldReason] != string(workflow.RetainedTargetInvariantUnauthorizedSessionCreation) ||
			recovered.Fields[invariant.FieldRejectedRetainedSessionID] != retainedReviewSessionID.String() {
			t.Fatalf("unauthorized creation diagnostic = %+v", recovered)
		}
	}()
	_, _ = store.resolveMaterializedCurrentNodeStartContext(
		ctx,
		store.queries,
		taskRecord,
		workflowRecord,
		definition,
		unauthorized,
		nil,
	)
}
