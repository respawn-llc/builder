package workflowstore

import (
	"errors"
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/workflow"
	"core/shared/invariant"
)

func TestRejectLegacyCurrentFanoutJoinSourceReturnsTypedErrorInProduction(t *testing.T) {
	source, err := workflow.NewCurrentNodeReference("task-legacy-join", "node-branch", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	records := testsetup.CaptureSlogRecords(t)
	policy := invariant.NewPolicy(
		invariant.WithMode(invariant.ModeDiagnostic),
		invariant.WithSink(workflowInvariantSlogSink{}),
	)
	err = rejectLegacyCurrentFanoutJoinSource(
		policy,
		source,
		"node-join",
		"edge-join",
		[]currentFanoutJoinArrival{{
			BranchKey:          "branch-a",
			ContinuationSource: workflow.LegacyMaterializedContinuationSource(),
		}},
	)
	var unresolved workflow.LegacyContinuationSourceUnresolvedError
	if !errors.As(err, &unresolved) {
		t.Fatalf("reject legacy Join source error = %v, want LegacyContinuationSourceUnresolvedError", err)
	}
	if branchKey, branchScoped := unresolved.Source.TransitionBranchKey(); !branchScoped || branchKey != "branch-a" {
		t.Fatalf("unresolved legacy Join source = %v, want branch-a", unresolved.Source)
	}
	reportWorkflowInvariantError(policy, err)
	var diagnostics int
	for _, record := range records.Records() {
		if record.Fields[string(invariant.FieldOperation)] == legacyContinuationSourceOperation {
			diagnostics++
		}
	}
	if diagnostics != 1 {
		t.Fatalf("legacy Join diagnostics = %d, want exactly 1", diagnostics)
	}
}

func TestRejectLegacyCurrentFanoutJoinSourceFailsFastInDebug(t *testing.T) {
	source, err := workflow.NewCurrentNodeReference("task-legacy-join", "node-branch", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("reject legacy Join source did not panic in debug")
		}
	}()
	_ = rejectLegacyCurrentFanoutJoinSource(
		invariant.NewPolicy(invariant.WithMode(invariant.ModePanic)),
		source,
		"node-join",
		"edge-join",
		[]currentFanoutJoinArrival{{
			BranchKey:          "branch-a",
			ContinuationSource: workflow.LegacyMaterializedContinuationSource(),
		}},
	)
}
