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
	taskID := workflow.TaskID("task-kent-345")
	sourceNodeID := workflow.NodeID("node-implementation")
	targetNodeID := workflow.NodeID("node-review")
	activeSourceSessionID := runtimeids.NewSessionID()
	rejectedSessionID := runtimeids.NewSessionID()
	detail := workflow.RetainedTargetInvariantDetail{
		TaskID:                    taskID,
		SourceNodeID:              sourceNodeID,
		TargetNodeID:              targetNodeID,
		ActiveSourceSessionID:     &activeSourceSessionID,
		RejectedRetainedSessionID: &rejectedSessionID,
		Reason:                    workflow.RetainedTargetInvariantProoflessCurrentTarget,
	}

	for _, test := range []struct {
		name string
		run  func(workflow.RetainedTargetInvariantDetail)
	}{
		{name: "strict corruption", run: func(detail workflow.RetainedTargetInvariantDetail) {
			reportRetainedTargetInvariantError(workflow.RetainedTargetInvariantError{Detail: detail})
		}},
		{name: "recoverable fallback corruption", run: reportRetainedTargetInvariantAfterCommit},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("KENT_INVARIANT_MODE", "diagnostic")
			records := testsetup.CaptureSlogRecords(t)
			test.run(detail)
			assertRetainedTargetInvariantRecord(t, records.Records(), detail)
		})
	}

	t.Run("debug panic", func(t *testing.T) {
		t.Setenv("KENT_INVARIANT_MODE", "panic")
		defer func() {
			diagnostic, ok := recover().(invariant.Diagnostic)
			if !ok {
				t.Fatalf("panic payload is not invariant.Diagnostic")
			}
			if diagnostic.Fields[invariant.FieldTaskID] != string(taskID) ||
				diagnostic.Fields[invariant.FieldSourceNodeID] != string(sourceNodeID) ||
				diagnostic.Fields[invariant.FieldTargetNodeID] != string(targetNodeID) ||
				diagnostic.Fields[invariant.FieldActiveSourceSessionID] != activeSourceSessionID.String() ||
				diagnostic.Fields[invariant.FieldRejectedRetainedSessionID] != rejectedSessionID.String() {
				t.Fatalf("panic diagnostic = %+v", diagnostic)
			}
		}()
		checkRetainedTargetInvariantBeforeMutation(detail)
	})

	t.Run("ordinary absence and mismatch are not diagnostics", func(t *testing.T) {
		t.Setenv("KENT_INVARIANT_MODE", "diagnostic")
		records := testsetup.CaptureSlogRecords(t)
		reportRetainedTargetInvariantError(workflow.RetainedTargetUnavailableError{
			TaskID: taskID, SourceNodeID: sourceNodeID, TargetNodeID: targetNodeID,
		})
		reportRetainedTargetInvariantError(errors.New("ordinary exact-source mismatch"))
		for _, record := range records.Records() {
			if record.Fields[string(invariant.FieldOperation)] == retainedTargetInvariantOperation {
				t.Fatalf("ordinary retained-target state emitted invariant diagnostic: %+v", record)
			}
		}
	})
}

func assertRetainedTargetInvariantRecord(
	t *testing.T,
	records []testsetup.CapturedSlogRecord,
	detail workflow.RetainedTargetInvariantDetail,
) {
	t.Helper()
	for _, record := range records {
		if record.Level != slog.LevelError ||
			record.Fields["scope"] != invariant.ScopeWorkflowExecution ||
			record.Fields[string(invariant.FieldOperation)] != retainedTargetInvariantOperation {
			continue
		}
		if record.Fields[string(invariant.FieldTaskID)] != string(detail.TaskID) ||
			record.Fields[string(invariant.FieldSourceNodeID)] != string(detail.SourceNodeID) ||
			record.Fields[string(invariant.FieldTargetNodeID)] != string(detail.TargetNodeID) ||
			record.Fields[string(invariant.FieldActiveSourceSessionID)] != detail.ActiveSourceSessionID.String() ||
			record.Fields[string(invariant.FieldRejectedRetainedSessionID)] != detail.RejectedRetainedSessionID.String() ||
			record.Fields[string(invariant.FieldReason)] != string(detail.Reason) {
			t.Fatalf("retained-target diagnostic = %+v", record)
		}
		return
	}
	t.Fatal("retained-target invariant diagnostic not found")
}
