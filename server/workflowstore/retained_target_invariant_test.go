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
	t.Setenv("KENT_INVARIANT_MODE", "diagnostic")
	for _, emit := range []func(){
		func() { reportRetainedTargetInvariantError(workflow.RetainedTargetInvariantError{Detail: detail}) },
		func() { reportRetainedTargetInvariantAfterCommit(detail) },
	} {
		records := testsetup.CaptureSlogRecords(t)
		emit()
		assertRetainedTargetInvariantDiagnostic(t, records.Records(), source, rejected)
	}
	t.Setenv("KENT_INVARIANT_MODE", "panic")
	func() {
		defer func() {
			if _, ok := recover().(invariant.Diagnostic); !ok {
				t.Fatal("debug invariant did not panic with typed diagnostic")
			}
		}()
		checkRetainedTargetInvariantBeforeMutation(detail)
	}()
	t.Setenv("KENT_INVARIANT_MODE", "diagnostic")
	records := testsetup.CaptureSlogRecords(t)
	reportRetainedTargetInvariantError(workflow.RetainedTargetUnavailableError{})
	reportRetainedTargetInvariantError(errors.New("ordinary mismatch"))
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
