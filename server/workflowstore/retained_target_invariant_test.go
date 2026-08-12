package workflowstore

import (
	"errors"
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
	records := testsetup.CaptureSlogRecords(t)
	reportRetainedTargetInvariantError(workflow.RetainedTargetInvariantError{Detail: detail})
	reportRetainedTargetInvariantAfterCommit(detail)
	for _, fields := range []map[string]any{records.Records()[0].Fields, records.Records()[1].Fields} {
		if fields[string(invariant.FieldTaskID)] != "task" ||
			fields[string(invariant.FieldSourceNodeID)] != "source" ||
			fields[string(invariant.FieldTargetNodeID)] != "target" ||
			fields[string(invariant.FieldActiveSourceSessionID)] != source.String() ||
			fields[string(invariant.FieldRejectedRetainedSessionID)] != rejected.String() {
			t.Fatalf("typed invariant fields = %+v", fields)
		}
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
	records = testsetup.CaptureSlogRecords(t)
	reportRetainedTargetInvariantError(workflow.RetainedTargetUnavailableError{})
	reportRetainedTargetInvariantError(errors.New("ordinary mismatch"))
	for _, record := range records.Records() {
		if record.Fields[string(invariant.FieldOperation)] == retainedTargetInvariantOperation {
			t.Fatal("ordinary retained-target state emitted invariant diagnostic")
		}
	}
}
